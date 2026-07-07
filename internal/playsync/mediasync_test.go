package playsync

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

// ── fakeAPI media methods ─────────────────────────────────────────────────────

func (f *fakeAPI) ListLibraryTracks(_ context.Context, _ string, limit, offset int) ([]api.LibraryTrackOut, error) {
	f.listCalls++
	if offset >= len(f.listRows) {
		return nil, nil
	}
	end := min(offset+limit, len(f.listRows))
	return f.listRows[offset:end], nil
}

func (f *fakeAPI) UploadTrackWaveform(_ context.Context, _, libID, peaksB64 string, durationMs int) (int, error) {
	f.wfCalls++
	if f.wfErr != nil {
		return 0, f.wfErr
	}
	if f.wfUploads == nil {
		f.wfUploads = map[string]string{}
	}
	f.wfUploads[libID] = peaksB64
	f.wfDurMs = durationMs
	b, _ := base64.StdEncoding.DecodeString(peaksB64)
	return len(b), nil
}

func (f *fakeAPI) UploadTrackArtwork(_ context.Context, _, libID, contentType string, data []byte) error {
	f.artCalls++
	if f.artUploads == nil {
		f.artUploads = map[string][]byte{}
		f.artCT = map[string]string{}
	}
	f.artUploads[libID] = data
	f.artCT[libID] = contentType
	return nil
}

// fakeProbe serves probe.peaks / probe.artwork from canned per-path data.
type fakeProbe struct {
	peaks    map[string][]byte // path → raw buckets
	art      map[string][]byte // path → picture bytes (absent = no art)
	calls    map[string]int    // method → count
	probeErr error
}

func (f *fakeProbe) RunBackground(_ context.Context, _, method string, params any) (json.RawMessage, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[method]++
	if f.probeErr != nil {
		return nil, f.probeErr
	}
	raw, _ := json.Marshal(params)
	var p struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(raw, &p)
	switch method {
	case "probe.peaks":
		return json.Marshal(map[string]any{
			"peaks":           base64.StdEncoding.EncodeToString(f.peaks[p.Path]),
			"durationSeconds": 12.5,
		})
	case "probe.artwork":
		return json.Marshal(map[string]any{"mime": "image/jpeg", "data": f.art[p.Path]})
	}
	return nil, fmt.Errorf("unknown method %s", method)
}

// seedMediaLibrary seeds n titled tracks with real files on disk; returns their paths.
func seedMediaLibrary(t *testing.T, d *libdb.DB, n int) []string {
	t.Helper()
	dir := t.TempDir()
	tracks := make([]musiclib.Track, n)
	paths := make([]string, n)
	for i := range tracks {
		paths[i] = filepath.Join(dir, fmt.Sprintf("t%d.mp3", i))
		if err := os.WriteFile(paths[i], []byte("audio"), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		tracks[i] = musiclib.Track{Path: paths[i], Title: fmt.Sprintf("T%d", i), Artist: fmt.Sprintf("A%d", i)}
	}
	seedLibrary(t, d, tracks...)
	return paths
}

func smallJPEG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatalf("jpeg: %v", err)
	}
	return buf.Bytes()
}

func TestSyncMediaUploadsThenSkips(t *testing.T) {
	d := openDB(t)
	paths := seedMediaLibrary(t, d, 2)
	h0, h1 := libdb.TrackHash("A0", "T0", 0), libdb.TrackHash("A1", "T1", 0)
	if err := d.SaveLibraryTrackIDs(map[string]string{h0: "lib_0", h1: "lib_1"}); err != nil {
		t.Fatalf("seed ids: %v", err)
	}
	art := smallJPEG(t)
	pr := &fakeProbe{
		peaks: map[string][]byte{paths[0]: {1, 2, 3}, paths[1]: {4, 5}},
		art:   map[string][]byte{paths[0]: art}, // track 1 has no art
	}
	f := &fakeAPI{}
	s := New(f, d, nil, tokenFn("tok"))
	s.SetProbe(pr)

	res, err := s.SyncMedia(context.Background(), 0, nil)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Candidates != 2 || res.Waveforms != 2 || res.Artwork != 1 || res.Failed != 0 || res.Remaining != 0 {
		t.Fatalf("result wrong: %+v", res)
	}
	if got := f.wfUploads["lib_0"]; got != base64.StdEncoding.EncodeToString([]byte{1, 2, 3}) {
		t.Fatalf("waveform payload wrong: %q", got)
	}
	if f.wfDurMs != 12500 {
		t.Fatalf("duration_ms = %d, want 12500", f.wfDurMs)
	}
	if !bytes.Equal(f.artUploads["lib_0"], art) || f.artCT["lib_0"] != "image/jpeg" {
		t.Fatalf("artwork payload wrong (ct=%q)", f.artCT["lib_0"])
	}
	// Privacy: nothing crossing the API boundary may contain a local file path.
	for libID, p := range f.wfUploads {
		if strings.Contains(p, ".mp3") || strings.Contains(p, filepath.Dir(paths[0])) {
			t.Fatalf("waveform payload leaks path (%s)", libID)
		}
	}
	for libID, b := range f.artUploads {
		if bytes.Contains(b, []byte(".mp3")) {
			t.Fatalf("artwork payload leaks path (%s)", libID)
		}
	}

	// Re-run: ledger short-circuits - zero probes, zero uploads, all skipped.
	probes, wf, ar := pr.calls["probe.peaks"]+pr.calls["probe.artwork"], f.wfCalls, f.artCalls
	res2, err := s.SyncMedia(context.Background(), 0, nil)
	if err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	if res2.Skipped != 2 || res2.Waveforms != 0 || res2.Artwork != 0 {
		t.Fatalf("re-run wrong: %+v", res2)
	}
	if got := pr.calls["probe.peaks"] + pr.calls["probe.artwork"]; got != probes || f.wfCalls != wf || f.artCalls != ar {
		t.Fatalf("re-run hit probe/API again (probes %d→%d wf %d→%d art %d→%d)", probes, got, wf, f.wfCalls, ar, f.artCalls)
	}
}

func TestSyncMediaWaveformBudget(t *testing.T) {
	d := openDB(t)
	paths := seedMediaLibrary(t, d, 3)
	ids := map[string]string{}
	pk := map[string][]byte{}
	for i, p := range paths {
		ids[libdb.TrackHash(fmt.Sprintf("A%d", i), fmt.Sprintf("T%d", i), 0)] = fmt.Sprintf("lib_%d", i)
		pk[p] = []byte{byte(i + 1)}
	}
	if err := d.SaveLibraryTrackIDs(ids); err != nil {
		t.Fatalf("seed ids: %v", err)
	}
	pr := &fakeProbe{peaks: pk, art: map[string][]byte{}}
	f := &fakeAPI{}
	s := New(f, d, nil, tokenFn("tok"))
	s.SetProbe(pr)

	res, err := s.SyncMedia(context.Background(), 1, nil)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	// Budget caps waveforms at 1; artwork (none embedded) still covers all 3.
	if res.Waveforms != 1 || res.Remaining != 2 || pr.calls["probe.peaks"] != 1 {
		t.Fatalf("budget run wrong: %+v peaks=%d", res, pr.calls["probe.peaks"])
	}
	if pr.calls["probe.artwork"] != 3 {
		t.Fatalf("artwork probes = %d, want 3", pr.calls["probe.artwork"])
	}
	// Second budgeted run picks up the next waveform (resumable, no re-probe of done work).
	res2, _ := s.SyncMedia(context.Background(), 1, nil)
	if res2.Waveforms != 1 || res2.Remaining != 1 || pr.calls["probe.artwork"] != 3 {
		t.Fatalf("resume run wrong: %+v artProbes=%d", res2, pr.calls["probe.artwork"])
	}
}

func TestSyncMediaBackfillsLibraryIDs(t *testing.T) {
	d := openDB(t)
	paths := seedMediaLibrary(t, d, 2) // no lib ids stored (pre-change sync)
	f := &fakeAPI{listRows: []api.LibraryTrackOut{
		{ID: "lib_a", Title: "T0", ArtistText: "A0"}, // matches track 0 by title|artist identity
		{ID: "lib_x", Title: "Other", ArtistText: "Nobody"},
	}}
	pr := &fakeProbe{peaks: map[string][]byte{paths[0]: {9}}, art: map[string][]byte{}}
	s := New(f, d, nil, tokenFn("tok"))
	s.SetProbe(pr)

	res, err := s.SyncMedia(context.Background(), 0, nil)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if f.listCalls == 0 {
		t.Fatalf("backfill never listed the server library")
	}
	if res.Unmatched != 1 || res.Waveforms != 1 {
		t.Fatalf("backfill result wrong: %+v", res)
	}
	if _, ok := f.wfUploads["lib_a"]; !ok {
		t.Fatalf("matched track not uploaded: %+v", f.wfUploads)
	}
	ids, _ := d.LibraryTrackIDs()
	if ids[libdb.TrackHash("A0", "T0", 0)] != "lib_a" {
		t.Fatalf("backfilled id not persisted: %v", ids)
	}
	// Next run: no candidates missing ids that can match → list not re-paged for matched ones.
	res2, _ := s.SyncMedia(context.Background(), 0, nil)
	if res2.Waveforms != 0 {
		t.Fatalf("re-run re-uploaded: %+v", res2)
	}
}

func TestSyncMediaUnauthed(t *testing.T) {
	d := openDB(t)
	s := New(&fakeAPI{}, d, nil, tokenFn(""))
	s.SetProbe(&fakeProbe{})
	if _, err := s.SyncMedia(context.Background(), 0, nil); err != ErrUnauthenticated {
		t.Fatalf("want ErrUnauthenticated, got %v", err)
	}
}

// ── artwork preparation ───────────────────────────────────────────────────────

func TestPrepareArtworkPassthrough(t *testing.T) {
	jp := smallJPEG(t)
	out, ct, ok, _ := prepareArtwork(jp)
	if !ok || ct != "image/jpeg" || !bytes.Equal(out, jp) {
		t.Fatalf("small jpeg should pass through (ok=%v ct=%q)", ok, ct)
	}
}

func TestPrepareArtworkRecompressesOversized(t *testing.T) {
	// Noise compresses terribly → guaranteed oversized PNG.
	rnd := rand.New(rand.NewSource(1))
	img := image.NewRGBA(image.Rect(0, 0, 1600, 1600))
	for i := range img.Pix {
		img.Pix[i] = uint8(rnd.Intn(256))
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png: %v", err)
	}
	if buf.Len() <= maxArtworkBytes {
		t.Fatalf("test png unexpectedly small: %d", buf.Len())
	}
	out, ct, ok, reason := prepareArtwork(buf.Bytes())
	if !ok {
		t.Fatalf("recompression failed: %s", reason)
	}
	if ct != "image/jpeg" || len(out) > maxArtworkBytes {
		t.Fatalf("bad output: ct=%q bytes=%d", ct, len(out))
	}
	dec, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("output undecodable: %v", err)
	}
	if b := dec.Bounds(); b.Dx() > maxArtworkDim || b.Dy() > maxArtworkDim {
		t.Fatalf("not downscaled: %dx%d", b.Dx(), b.Dy())
	}
}

func TestPrepareArtworkWebp(t *testing.T) {
	hdr := append([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), make([]byte, 16)...)
	// Small webp: stdlib can't decode, but it's a permitted type under the cap → pass through.
	out, ct, ok, _ := prepareArtwork(hdr)
	if !ok || ct != "image/webp" || !bytes.Equal(out, hdr) {
		t.Fatalf("small webp should pass through (ok=%v ct=%q)", ok, ct)
	}
	// Oversized webp: undecodable + over the cap → skipped with a webp reason.
	big := append([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), make([]byte, maxArtworkBytes+1)...)
	if _, _, ok, reason := prepareArtwork(big); ok || !strings.Contains(reason, "webp") {
		t.Fatalf("oversized webp should skip (ok=%v reason=%q)", ok, reason)
	}
}

func TestPrepareArtworkGarbage(t *testing.T) {
	if _, _, ok, _ := prepareArtwork([]byte("not an image at all")); ok {
		t.Fatalf("garbage should not be ok")
	}
	if _, _, ok, reason := prepareArtwork(nil); ok || reason != "empty" {
		t.Fatalf("empty should skip")
	}
}
