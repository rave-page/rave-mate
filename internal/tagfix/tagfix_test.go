package tagfix

import (
	"os"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/tagsync"
	"rave.page/mate/internal/tagwrite"
)

// v1OnlyMP3 writes a synthetic mp3: junk audio bytes + a 128-byte ID3v1 trailer, no ID3v2.
func v1OnlyMP3(t *testing.T, dir string) string {
	t.Helper()
	trailer := make([]byte, 128)
	copy(trailer, "TAG")
	pad := func(off int, s string) { copy(trailer[off:], s) }
	pad(3, "Synth Anthem") // title
	pad(33, "Test Artist") // artist
	pad(63, "Test Album")  // album
	pad(93, "1999")        // year
	trailer[127] = 31      // genre: Trance
	body := append([]byte{0xFF, 0xFB, 0x90, 0x00, 0x00, 0x00}, trailer...)
	path := filepath.Join(dir, "v1only.mp3")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func kindsOf(ps []Problem) map[Kind][]string {
	m := map[Kind][]string{}
	for _, p := range ps {
		m[p.Kind] = append(m[p.Kind], p.Field)
	}
	return m
}

func TestScanApplyV1Only(t *testing.T) {
	path := v1OnlyMP3(t, t.TempDir())
	ps, err := Scan([]musiclib.Track{{Path: path}}, Options{Kinds: []Kind{KindV1Only}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		tagwrite.FieldTitle: "Synth Anthem", tagwrite.FieldArtist: "Test Artist",
		tagwrite.FieldAlbum: "Test Album", tagwrite.FieldYear: "1999", tagwrite.FieldGenre: "Trance",
	}
	if len(ps) != len(want) {
		t.Fatalf("problems = %+v, want %d", ps, len(want))
	}
	for _, p := range ps {
		if p.Kind != KindV1Only || p.Current != "" || want[p.Field] != p.Proposed {
			t.Errorf("problem %+v", p)
		}
	}

	applied, err := Apply(nil, ps)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if applied != len(want) {
		t.Errorf("applied = %d, want %d", applied, len(want))
	}
	got, err := tagwrite.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	for f, v := range want {
		if got[f] != v {
			t.Errorf("after apply %s = %q, want %q", f, got[f], v)
		}
	}
	// File now has a v2 tag: rescan proposes nothing.
	ps2, _ := Scan([]musiclib.Track{{Path: path}}, Options{Kinds: []Kind{KindV1Only}})
	if len(ps2) != 0 {
		t.Errorf("rescan after apply: %+v", ps2)
	}
}

func TestScanMissingMismatchNoBasics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.mp3")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	// File: bpm 150.2, key "8a", artist set; no title/genre.
	if err := tagwrite.Write(path, tagwrite.Tags{
		tagwrite.FieldBPM: "150.2", tagwrite.FieldKey: "8a", tagwrite.FieldArtist: "Someone",
	}); err != nil {
		t.Fatal(err)
	}
	// Library: bpm 150 (mismatch), key 8A (normalized equal - no problem), genre (missing),
	// title (no_basics), artist equal (no problem).
	tr := musiclib.Track{Path: path, BPM: 150, Key: "8A", Genre: "Techno", Title: "Track One", Artist: "Someone"}
	ps, err := Scan([]musiclib.Track{tr}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	k := kindsOf(ps)
	if len(ps) != 3 {
		t.Fatalf("problems = %+v", ps)
	}
	if f := k[KindMismatch]; len(f) != 1 || f[0] != tagwrite.FieldBPM {
		t.Errorf("mismatch = %v", f)
	}
	if f := k[KindMissing]; len(f) != 1 || f[0] != tagwrite.FieldGenre {
		t.Errorf("missing = %v", f)
	}
	if f := k[KindNoBasics]; len(f) != 1 || f[0] != tagwrite.FieldTitle {
		t.Errorf("no_basics = %v", f)
	}

	// Kind filter narrows the scan.
	only, _ := Scan([]musiclib.Track{tr}, Options{Kinds: []Kind{KindMissing}})
	if len(only) != 1 || only[0].Kind != KindMissing {
		t.Errorf("filtered scan = %+v", only)
	}

	// BPM within 0.05 is NOT a mismatch.
	tr2 := tr
	tr2.BPM = 150.16
	ps2, _ := Scan([]musiclib.Track{tr2}, Options{Kinds: []Kind{KindMismatch}})
	if len(ps2) != 0 {
		t.Errorf("delta 0.04 flagged: %+v", ps2)
	}
}

func TestScanMojibakeEndToEnd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.mp3")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	bad := "Caf" + rs(0xC3, 0xA9) // latin1-mislabeled UTF-8 e-acute
	if err := tagwrite.Write(path, tagwrite.Tags{tagwrite.FieldTitle: bad}); err != nil {
		t.Fatal(err)
	}
	ps, err := Scan([]musiclib.Track{{Path: path}}, Options{Kinds: []Kind{KindMojibake}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Field != tagwrite.FieldTitle || ps[0].Proposed != "Caf"+rs(0xE9) {
		t.Fatalf("problems = %+v", ps)
	}
	if _, err := Apply(nil, ps); err != nil {
		t.Fatal(err)
	}
	got, _ := tagwrite.Read(path)
	if got[tagwrite.FieldTitle] != "Caf"+rs(0xE9) {
		t.Errorf("title after repair = %q", got[tagwrite.FieldTitle])
	}
}

func TestScanFLACMissing(t *testing.T) {
	src, err := os.ReadFile("../tagwrite/testdata/silence.flac")
	if err != nil {
		t.Skipf("no flac fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "s.flac")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	tr := musiclib.Track{Path: path, BPM: 140, Key: "5A"}
	ps, err := Scan([]musiclib.Track{tr}, Options{Kinds: []Kind{KindMissing}})
	if err != nil {
		t.Fatal(err)
	}
	k := kindsOf(ps)
	if f := k[KindMissing]; len(f) != 2 {
		t.Fatalf("missing = %v", f)
	}
	if n, err := Apply(nil, ps); err != nil || n != 2 {
		t.Fatalf("apply = %d, %v", n, err)
	}
	got, _ := tagwrite.Read(path)
	if got[tagwrite.FieldBPM] != "140" || got[tagwrite.FieldKey] != "5A" {
		t.Errorf("flac after apply: %v", got)
	}
}

func TestScanSkipsUnsupported(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "x.wav")
	_ = os.WriteFile(wav, []byte("RIFF"), 0o644)
	skipped, calls := 0, 0
	ps, err := Scan([]musiclib.Track{{Path: wav}}, Options{
		Skipped:  &skipped,
		Progress: func(done, total int) { calls++ },
	})
	if err != nil || len(ps) != 0 {
		t.Fatalf("ps=%v err=%v", ps, err)
	}
	if skipped != 1 || calls != 1 {
		t.Errorf("skipped=%d progress calls=%d", skipped, calls)
	}
}

// TestApplyStaleSkipped: a problem whose Current no longer matches the file is not applied.
func TestApplyStaleSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.mp3")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	tr := musiclib.Track{Path: path, Genre: "Techno"}
	ps, _ := Scan([]musiclib.Track{tr}, Options{Kinds: []Kind{KindMissing}})
	if len(ps) != 1 {
		t.Fatalf("ps = %+v", ps)
	}
	// File changes between scan and apply.
	if err := tagwrite.Write(path, tagwrite.Tags{tagwrite.FieldGenre: "House"}); err != nil {
		t.Fatal(err)
	}
	n, err := Apply(nil, ps)
	if err != nil || n != 0 {
		t.Errorf("applied = %d, err = %v (stale problem must be skipped)", n, err)
	}
	got, _ := tagwrite.Read(path)
	if got[tagwrite.FieldGenre] != "House" {
		t.Errorf("genre = %q, stale apply overwrote", got[tagwrite.FieldGenre])
	}
}

// TestApplyRevertible: repairs applied with a DB are revertible via tagsync.
func TestApplyRevertible(t *testing.T) {
	db, err := libdb.Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	path := filepath.Join(t.TempDir(), "r.mp3")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	tr := musiclib.Track{Path: path, Genre: "Techno", Key: "3A"}
	ps, _ := Scan([]musiclib.Track{tr}, Options{Kinds: []Kind{KindMissing}})
	if len(ps) != 2 {
		t.Fatalf("ps = %+v", ps)
	}
	if n, err := Apply(db, ps); err != nil || n != 2 {
		t.Fatalf("apply = %d, %v", n, err)
	}
	got, _ := tagwrite.Read(path)
	if got[tagwrite.FieldGenre] != "Techno" || got[tagwrite.FieldKey] != "3A" {
		t.Fatalf("after apply: %v", got)
	}
	if err := tagsync.Revert(db, path); err != nil {
		t.Fatalf("revert: %v", err)
	}
	after, _ := tagwrite.Read(path)
	if after[tagwrite.FieldGenre] != "" || after[tagwrite.FieldKey] != "" {
		t.Errorf("after revert: %v", after)
	}
}
