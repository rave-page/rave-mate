package seratolib

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"rave.page/mate/internal/musiclib"
)

// --- envelope builders (big-endian [tag][len][payload], UTF-16BE text) ---

func chunk(tag string, payload []byte) []byte {
	out := []byte(tag)
	out = binary.BigEndian.AppendUint32(out, uint32(len(payload)))
	return append(out, payload...)
}

func utf16be(s string) []byte {
	var out []byte
	for _, r := range s {
		out = binary.BigEndian.AppendUint16(out, uint16(r))
	}
	return out
}

func textChunk(tag, s string) []byte { return chunk(tag, utf16be(s)) }

// seratoDirFixture writes a synthetic _Serato_ dir: database V2 with two tracks + one crate.
func seratoDirFixture(t *testing.T) (dir string, relTrack string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "_Serato_")
	if err := os.MkdirAll(filepath.Join(dir, "Subcrates"), 0o755); err != nil {
		t.Fatal(err)
	}
	relTrack = "Music/Fixtures/track one.mp3"
	if runtime.GOOS != "windows" {
		relTrack = "tmp/fixtures/track one.mp3"
	}
	db := chunk("vrsn", utf16be("2.0/Serato Scratch LIVE Database"))
	db = append(db, chunk("otrk", append(append(append(
		textChunk("pfil", relTrack),
		textChunk("tsng", "Track One")...),
		textChunk("tart", "Artist A")...),
		textChunk("tbpm", "174.00")...))...)
	db = append(db, chunk("otrk", append(
		textChunk("pfil", "Music/Fixtures/two.flac"),
		textChunk("tsng", "Two")...))...)
	if err := os.WriteFile(filepath.Join(dir, "database V2"), db, 0o644); err != nil {
		t.Fatal(err)
	}
	crate := chunk("vrsn", utf16be("1.0/Serato ScratchLive Crate"))
	crate = append(crate, chunk("otrk", textChunk("ptrk", relTrack))...)
	if err := os.WriteFile(filepath.Join(dir, "Subcrates", "House%%Deep.crate"), crate, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, relTrack
}

func TestReadDatabaseAndCrates(t *testing.T) {
	dir, rel := seratoDirFixture(t)
	lib, err := ReadDatabase(dir)
	if err != nil {
		t.Fatal(err)
	}
	if lib.Source.App != "serato" || len(lib.Tracks) != 2 {
		t.Fatalf("source %+v tracks %d", lib.Source, len(lib.Tracks))
	}
	tr := lib.Tracks[0]
	if tr.Title != "Track One" || tr.Artist != "Artist A" || math.Abs(tr.BPM-174) > 1e-9 {
		t.Fatalf("track: %+v", tr)
	}
	want := resolvePath(dir, rel)
	if tr.Path != want || !filepath.IsAbs(tr.Path) {
		t.Fatalf("path %q want abs %q", tr.Path, want)
	}
	pls, err := ReadCrates(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pls) != 1 || pls[0].Folder != "House" || pls[0].Name != "Deep" {
		t.Fatalf("crates: %+v", pls)
	}
	if len(pls[0].Paths) != 1 || pls[0].Paths[0] != want {
		t.Fatalf("crate paths: %v", pls[0].Paths)
	}
}

func TestResolvePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		got := resolvePath(`D:\Stuff\_Serato_`, "Users/x/a.mp3")
		if got != `D:\Users\x\a.mp3` {
			t.Fatalf("got %q", got)
		}
		if got := resolvePath(`D:\_Serato_`, `C:\direct\b.mp3`); got != `C:\direct\b.mp3` {
			t.Fatalf("abs passthrough: %q", got)
		}
	} else {
		if got := resolvePath("/home/x/Music/_Serato_", "home/x/a.mp3"); got != "/home/x/a.mp3" {
			t.Fatalf("got %q", got)
		}
	}
}

func TestApplyGridFixesSerato(t *testing.T) {
	dir, _ := seratoDirFixture(t)
	// Synthetic mp3 target file.
	mp3 := filepath.Join(t.TempDir(), "fix me.mp3")
	tag := tagBytes(t, 3, 0, 32, textFrame(t, 3, "TIT2", "Fix Me"))
	if err := os.WriteFile(mp3, mp3File(t, tag), 0o644); err != nil {
		t.Fatal(err)
	}
	m4a := filepath.Join(t.TempDir(), "skip.m4a")
	if err := os.WriteFile(m4a, []byte{0, 0, 0, 8, 'f', 't', 'y', 'p'}, 0o644); err != nil {
		t.Fatal(err)
	}

	orig := seratoRunning
	seratoRunning = func() bool { return true }
	if _, err := ApplyGridFixesSerato(dir, []musiclib.GridFixUpdate{{Path: mp3, BPM: 174, StartMs: 125}}); !errors.Is(err, ErrSeratoRunning) {
		t.Fatalf("running not refused: %v", err)
	}
	seratoRunning = func() bool { return false }
	defer func() { seratoRunning = orig }()

	res, err := ApplyGridFixesSerato(dir, []musiclib.GridFixUpdate{
		{Path: mp3, BPM: 174, StartMs: 125, Lock: true},
		{Path: m4a, BPM: 120, StartMs: 0}, // unsupported: skipped, not an error
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 {
		t.Fatalf("updated %d", res.Updated)
	}
	markers, found, err := ReadBeatgrid(mp3)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if math.Abs(markers[0].BPM-174) > 1e-4 || math.Abs(markers[0].PositionMs-125) > 1e-2 {
		t.Fatalf("markers %+v", markers)
	}
	// WriteBeatgrid public path too.
	if err := WriteBeatgrid(mp3, 90, 2000); err != nil {
		t.Fatal(err)
	}
	markers, _, err = ReadBeatgrid(mp3)
	if err != nil || math.Abs(markers[0].BPM-90) > 1e-4 {
		t.Fatalf("rewrite: %+v %v", markers, err)
	}
	// Missing file reported as error.
	if _, err := ApplyGridFixesSerato(dir, []musiclib.GridFixUpdate{{Path: filepath.Join(dir, "nope.mp3"), BPM: 120, StartMs: 0}}); err == nil {
		t.Fatal("missing file not reported")
	}
	// Bad serato dir refused.
	if _, err := ApplyGridFixesSerato(filepath.Join(dir, "missing-sub"), []musiclib.GridFixUpdate{{Path: mp3, BPM: 120, StartMs: 0}}); err == nil {
		t.Fatal("bad dir not refused")
	}
}

func TestAttachBeatgrids(t *testing.T) {
	mp3 := filepath.Join(t.TempDir(), "g.mp3")
	if err := os.WriteFile(mp3, mp3File(t, tagBytes(t, 3, 0, 8)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeBeatgridFile(mp3, []musiclib.GridMarker{{PositionMs: 50, BPM: 140}}); err != nil {
		t.Fatal(err)
	}
	tracks := []musiclib.Track{{Path: mp3}, {Path: filepath.Join(t.TempDir(), "missing.mp3")}}
	if n := AttachBeatgrids(tracks); n != 1 {
		t.Fatalf("attached %d", n)
	}
	if len(tracks[0].Beatgrid) != 1 || math.Abs(tracks[0].Beatgrid[0].BPM-140) > 1e-4 {
		t.Fatalf("grid %+v", tracks[0].Beatgrid)
	}
}
