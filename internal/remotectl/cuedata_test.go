package remotectl

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

// cueLib seeds a temp libdb with one imported track whose Path is a real temp audio file
// (.wav → tagwrite unsupported, so writeCueData never touches the file). Returns db, track
// path and the file's content.
func cueLib(t *testing.T) (*libdb.DB, string, []byte) {
	t.Helper()
	lib, err := libdb.Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lib.Close() })
	content := bytes.Repeat([]byte("rave-mate-chunk!"), 200) // 3200 bytes
	path := filepath.Join(t.TempDir(), "track.wav")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := lib.UpsertSource(musiclib.Source{App: "traktor", Path: "/c/collection.nml"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	sy, err := lib.BeginTrackSync(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	tr := musiclib.Track{
		Path: path, Title: "One", Artist: "A", BPM: 128, DurationSec: 300,
		Cues:     []musiclib.CuePoint{{Name: "in", Kind: musiclib.CueHot, StartMs: 1000, Hotcue: 0}},
		Beatgrid: []musiclib.GridMarker{{PositionMs: 12.5, BPM: 128}},
	}
	if err := sy.Add(tr); err != nil {
		t.Fatal(err)
	}
	if _, err := sy.Commit(); err != nil {
		t.Fatal(err)
	}
	return lib, path, content
}

// TestTrackDetailRPC: state round-trip, StateSHA stability + client-side reproducibility,
// unknown path rejection.
func TestTrackDetailRPC(t *testing.T) {
	lib, path, content := cueLib(t)
	server, client := loopback()
	RegisterLibraryCueEdit(server, lib, nil, nil, "")
	rc := NewClient(client, "server")

	d, err := rc.LibraryTrackDetail(ctx(t), path)
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if d.Track.Title != "One" || len(d.Track.Cues) != 1 || len(d.Track.Beatgrid) != 1 {
		t.Fatalf("track=%+v", d.Track)
	}
	if d.Drops == nil || len(d.Drops) != 0 {
		t.Fatalf("drops=%v want empty non-nil", d.Drops)
	}
	if d.SizeBytes != int64(len(content)) || d.MTimeUnix == 0 {
		t.Fatalf("size=%d mtime=%d", d.SizeBytes, d.MTimeUnix)
	}
	// StateSHA is stable AND reproducible client-side (the P2 contract).
	d2, err := rc.LibraryTrackDetail(ctx(t), path)
	if err != nil || d2.StateSHA != d.StateSHA {
		t.Fatalf("sha unstable: %q vs %q (err=%v)", d.StateSHA, d2.StateSHA, err)
	}
	if want := CueStateSHA(d.Track.Cues, d.Track.Beatgrid, d.Drops); want != d.StateSHA {
		t.Fatalf("client recompute %q != server %q", want, d.StateSHA)
	}
	if _, err := rc.LibraryTrackDetail(ctx(t), `C:\nope\missing.mp3`); err == nil {
		t.Fatal("unknown path must error")
	}
}

// TestLibFileChunkRPC: chunked reassembly + EOF, bounds rejection, unknown-path rejection.
func TestLibFileChunkRPC(t *testing.T) {
	lib, path, content := cueLib(t)
	server, client := loopback()
	RegisterLibraryCueEdit(server, lib, nil, nil, "")
	rc := NewClient(client, "server")

	var got []byte
	for off := int64(0); ; {
		r, err := rc.LibraryFileChunk(ctx(t), path, off, 1000)
		if err != nil {
			t.Fatalf("chunk @%d: %v", off, err)
		}
		if r.Total != int64(len(content)) || r.MTimeUnix == 0 {
			t.Fatalf("total=%d mtime=%d", r.Total, r.MTimeUnix)
		}
		b, err := base64.StdEncoding.DecodeString(r.DataBase64)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, b...)
		off += int64(len(b))
		if r.EOF {
			break
		}
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("reassembled %d bytes != original %d", len(got), len(content))
	}
	// offset at EOF → empty last chunk, EOF set.
	if r, err := rc.LibraryFileChunk(ctx(t), path, int64(len(content)), 100); err != nil || !r.EOF || r.DataBase64 != "" {
		t.Fatalf("at-eof chunk=%+v err=%v", r, err)
	}
	// bounds + security.
	if _, err := rc.LibraryFileChunk(ctx(t), path, -1, 10); err == nil {
		t.Fatal("negative offset must error")
	}
	if _, err := rc.LibraryFileChunk(ctx(t), path, 0, 0); err == nil {
		t.Fatal("len<=0 must error")
	}
	if _, err := rc.LibraryFileChunk(ctx(t), filepath.Join(t.TempDir(), "not-in-lib.wav"), 0, 10); err == nil {
		t.Fatal("non-library path must be rejected")
	}
}

// TestWriteCueDataRPC: happy path (write lands, fresh detail + trackchanged published),
// conflict path (stale BaseSHA → Conflict, NO mutation), Force override.
func TestWriteCueDataRPC(t *testing.T) {
	lib, path, _ := cueLib(t)
	var pubTopic string
	var pubData json.RawMessage
	pub := func(topic string, data json.RawMessage) { pubTopic, pubData = topic, data }
	server, client := loopback()
	RegisterLibraryCueEdit(server, lib, pub, nil, "")
	rc := NewClient(client, "server")

	base, err := rc.LibraryTrackDetail(ctx(t), path)
	if err != nil {
		t.Fatal(err)
	}
	newCues := []musiclib.CuePoint{
		{Name: "drop", Kind: musiclib.CueHot, StartMs: 61000, Hotcue: 1},
		{Kind: musiclib.CuePlain, StartMs: 122000, Hotcue: -1},
	}
	newGrid := []musiclib.GridMarker{{PositionMs: 10, BPM: 130}}
	res, err := rc.WriteCueData(ctx(t), WriteCueDataParams{
		Path: path, Cues: newCues, Beatgrid: newGrid,
		Drops: []float64{61000}, DropsSet: true, BaseSHA: base.StateSHA,
	})
	if err != nil || !res.OK || res.Conflict {
		t.Fatalf("write=%+v err=%v", res, err)
	}
	if len(res.Detail.Track.Cues) != 2 || res.Detail.Track.Beatgrid[0].BPM != 130 || len(res.Detail.Drops) != 1 {
		t.Fatalf("fresh detail=%+v", res.Detail)
	}
	if res.Detail.StateSHA == base.StateSHA {
		t.Fatal("StateSHA must advance after a write")
	}
	if pubTopic != libdb.TopicTrackChanged {
		t.Fatalf("published topic=%q", pubTopic)
	}
	var ev libdb.TrackChangedEvent
	if json.Unmarshal(pubData, &ev) != nil || ev.Path != path || !strings.HasPrefix(ev.Origin, "peer:") {
		t.Fatalf("event=%+v", ev)
	}
	// persisted (not just echoed): DB now carries the new state.
	tr, ok, err := lib.TrackByPath(path)
	if err != nil || !ok || len(tr.Cues) != 2 || tr.Beatgrid[0].BPM != 130 {
		t.Fatalf("db track=%+v ok=%v err=%v", tr, ok, err)
	}
	if drops, _ := lib.Drops(path); len(drops) != 1 || drops[0] != 61000 {
		t.Fatalf("db drops=%v", drops)
	}

	// conflict: the original BaseSHA is stale now → Conflict + fresh detail, NO mutation.
	pubTopic = ""
	res2, err := rc.WriteCueData(ctx(t), WriteCueDataParams{
		Path: path, Cues: nil, Drops: nil, DropsSet: true, BaseSHA: base.StateSHA,
	})
	if err != nil || !res2.Conflict || res2.OK {
		t.Fatalf("conflict=%+v err=%v", res2, err)
	}
	if res2.Detail.StateSHA != res.Detail.StateSHA {
		t.Fatalf("conflict detail sha=%q want current %q", res2.Detail.StateSHA, res.Detail.StateSHA)
	}
	if pubTopic != "" {
		t.Fatal("conflict must not publish trackchanged")
	}
	if tr, _, _ := lib.TrackByPath(path); len(tr.Cues) != 2 {
		t.Fatalf("conflict mutated cues: %+v", tr.Cues)
	}
	if drops, _ := lib.Drops(path); len(drops) != 1 {
		t.Fatalf("conflict mutated drops: %v", drops)
	}

	// Force: stale BaseSHA still lands.
	res3, err := rc.WriteCueData(ctx(t), WriteCueDataParams{
		Path: path, Cues: newCues[:1], BaseSHA: "stale", Force: true,
	})
	if err != nil || !res3.OK || res3.Conflict {
		t.Fatalf("force=%+v err=%v", res3, err)
	}
	if len(res3.Detail.Track.Cues) != 1 {
		t.Fatalf("force detail=%+v", res3.Detail.Track.Cues)
	}
	// Beatgrid nil = leave; drops not sent = leave.
	if len(res3.Detail.Track.Beatgrid) != 1 || len(res3.Detail.Drops) != 1 {
		t.Fatalf("nil fields must leave grid/drops: %+v", res3.Detail)
	}
}

// TestPlaylistTracksRPC + writeCuesTo target rejection.
func TestPlaylistTracksRPC(t *testing.T) {
	lib, path, _ := cueLib(t)
	id, err := lib.CreatePlaylist("Prep", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lib.AddToPlaylist(id, path); err != nil {
		t.Fatal(err)
	}
	server, client := loopback()
	RegisterLibraryCueEdit(server, lib, nil, nil, "")
	rc := NewClient(client, "server")

	paths, err := rc.LibraryPlaylistTracks(ctx(t), id)
	if err != nil || len(paths) != 1 || paths[0] != path {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	if paths, err := rc.LibraryPlaylistTracks(ctx(t), 9999); err != nil || len(paths) != 0 {
		t.Fatalf("empty playlist paths=%v err=%v", paths, err)
	}
	// writeCuesTo with a software key that can never be detected → clean error.
	if _, err := rc.WriteCuesTo(ctx(t), "nope", []string{path}); err == nil {
		t.Fatal("undetected target must error")
	}
}
