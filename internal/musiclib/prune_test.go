package musiclib

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// sampleNML mirrors Traktor's structure: a COLLECTION of 2 track ENTRYs (with LOCATION) + a
// PLAYLIST of 2 PRIMARYKEY refs. VOLUME "C:" works the same on any host (resolveLocation just
// joins), but the resolved separators differ, so the test derives expected paths via the same
// resolvers it asserts against.
const sampleNML = `<?xml version="1.0" encoding="UTF-8" standalone="no" ?>
<NML VERSION="20"><HEAD COMPANY="x" PROGRAM="Traktor Pro 4"></HEAD>
<COLLECTION ENTRIES="2"><ENTRY TITLE="Keep" ARTIST="A"><LOCATION DIR="/:Music/:" FILE="keep.mp3" VOLUME="C:"></LOCATION></ENTRY>
<ENTRY TITLE="Gone" ARTIST="B"><LOCATION DIR="/:Music/:" FILE="gone.mp3" VOLUME="C:"></LOCATION></ENTRY>
</COLLECTION>
<PLAYLISTS><NODE TYPE="FOLDER" NAME="$ROOT"><SUBNODES COUNT="1"><NODE TYPE="PLAYLIST" NAME="P"><PLAYLIST ENTRIES="2" TYPE="LIST" UUID="u">` +
	`<ENTRY><PRIMARYKEY TYPE="TRACK" KEY="C:/:Music/:keep.mp3"></PRIMARYKEY></ENTRY>` +
	`<ENTRY><PRIMARYKEY TYPE="TRACK" KEY="C:/:Music/:gone.mp3"></PRIMARYKEY></ENTRY>` +
	`</PLAYLIST></NODE></SUBNODES></NODE></PLAYLISTS></NML>`

func TestPruneCollectionFile(t *testing.T) {
	keep := resolveLocation("C:", "/:Music/:", "keep.mp3")
	gone := resolveLocation("C:", "/:Music/:", "gone.mp3")
	// PRIMARYKEY KEY resolves via resolveKey - must match the LOCATION-derived path for the prune
	// to strip both the track and its playlist ref.
	if got := resolveKey("C:/:Music/:gone.mp3"); got != gone {
		t.Fatalf("resolveKey/resolveLocation disagree: %q vs %q", got, gone)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "collection.nml")
	if err := os.WriteFile(path, []byte(sampleNML), 0o600); err != nil {
		t.Fatal(err)
	}

	res, err := PruneCollectionFile(path, map[string]bool{gone: true})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if res.TracksRemoved != 1 || res.RefsRemoved != 1 {
		t.Fatalf("removed = %d tracks / %d refs, want 1/1", res.TracksRemoved, res.RefsRemoved)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if strings.Contains(s, "gone.mp3") {
		t.Errorf("pruned file still references gone.mp3:\n%s", s)
	}
	if !strings.Contains(s, "keep.mp3") {
		t.Errorf("pruned file lost keep.mp3:\n%s", s)
	}
	// ENTRIES counts decremented (2→1 in both COLLECTION and PLAYLIST).
	if strings.Contains(s, `ENTRIES="2"`) {
		t.Errorf("ENTRIES count not corrected (still 2):\n%s", s)
	}
	if !strings.Contains(s, `ENTRIES="1"`) {
		t.Errorf("expected corrected ENTRIES=1:\n%s", s)
	}
	// The surviving track must still parse back to exactly one track at the kept path.
	var got []Track
	if _, err := ParseCollection(strings.NewReader(s), func(tr Track) { got = append(got, tr) }); err != nil {
		t.Fatalf("re-parse pruned: %v", err)
	}
	if len(got) != 1 || got[0].Path != keep {
		t.Fatalf("re-parse = %d tracks (%v), want 1 [%s]", len(got), got, keep)
	}
	_ = runtime.GOOS
}

// TestPruneRealCollection (env-gated) round-trips a COPY of a real collection.nml: prunes the
// first track + verifies the file stays valid and shrinks by one. Set REAL_NML=<path> to run.
func TestPruneRealCollection(t *testing.T) {
	src := os.Getenv("REAL_NML")
	if src == "" {
		t.Skip("set REAL_NML=<collection.nml> to run the real-file round-trip")
	}
	var first Track
	n0 := 0
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCollection(in, func(tr Track) {
		if n0 == 0 {
			first = tr
		}
		n0++
	}); err != nil {
		_ = in.Close()
		t.Fatal(err)
	}
	_ = in.Close()
	t.Logf("source: %d tracks, first=%q", n0, first.Path)

	dir := t.TempDir()
	copyPath := filepath.Join(dir, "collection.nml")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := PruneCollectionFile(copyPath, map[string]bool{first.Path: true})
	if err != nil {
		t.Fatalf("prune real: %v", err)
	}
	t.Logf("pruned: %d tracks, %d refs", res.TracksRemoved, res.RefsRemoved)
	if res.TracksRemoved < 1 {
		t.Fatalf("expected ≥1 track removed for %q", first.Path)
	}
	out, err := os.Open(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	n1 := 0
	if _, err := ParseCollection(out, func(Track) { n1++ }); err != nil {
		t.Fatalf("pruned real file no longer parses: %v", err)
	}
	if n1 != n0-res.TracksRemoved {
		t.Fatalf("track count = %d, want %d (n0=%d - removed=%d)", n1, n0-res.TracksRemoved, n0, res.TracksRemoved)
	}
}

// TestPruneCollectionFileNoop: empty removed set leaves the file byte-identical.
func TestPruneCollectionFileNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "collection.nml")
	if err := os.WriteFile(path, []byte(sampleNML), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := PruneCollectionFile(path, nil)
	if err != nil {
		t.Fatalf("noop prune: %v", err)
	}
	if res.TracksRemoved != 0 || res.RefsRemoved != 0 {
		t.Fatalf("noop removed %+v", res)
	}
	out, _ := os.ReadFile(path)
	if string(out) != sampleNML {
		t.Errorf("noop changed the file")
	}
}
