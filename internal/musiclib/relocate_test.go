package musiclib

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── ScanMissing ──────────────────────────────────────────────────────────────

func TestScanMissing(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "exists.mp3")
	if err := os.WriteFile(existing, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	tracks := []Track{
		{Path: existing, Title: "Exists"},
		{Path: filepath.Join(dir, "gone.mp3"), Title: "Gone"},
	}
	present, missing := ScanMissing(tracks)
	if len(present) != 1 || present[0].Title != "Exists" {
		t.Errorf("present: %+v", present)
	}
	if len(missing) != 1 || missing[0].Title != "Gone" {
		t.Errorf("missing: %+v", missing)
	}
}

// ── BuildIndex + Relocate ────────────────────────────────────────────────────

func TestRelocateSingleMatch(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "new_folder", "track.mp3")
	if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
		t.Fatal(err)
	}
	// Write ~5 KB so size-match is irrelevant (unique basename wins).
	if err := os.WriteFile(newPath, make([]byte, 5000), 0644); err != nil {
		t.Fatal(err)
	}

	idx, err := BuildIndex([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	missing := []Track{
		{Path: filepath.Join("C:\\", "old", "track.mp3"), FileSizeKB: 5},
	}
	cands := Relocate(missing, idx)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].NewPath != newPath {
		t.Errorf("NewPath = %q, want %q", cands[0].NewPath, newPath)
	}
	if cands[0].Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0", cands[0].Confidence)
	}
}

func TestRelocatePrefersSizeMatch(t *testing.T) {
	dir := t.TempDir()

	// Two files with same basename; one matches file size.
	fileA := filepath.Join(dir, "a", "track.mp3")
	fileB := filepath.Join(dir, "b", "track.mp3")
	for _, p := range []string{fileA, fileB} {
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// fileA: ~4 KB, fileB: ~10 KB. Track says 10 KB → fileB should win.
	if err := os.WriteFile(fileA, make([]byte, 4096), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, make([]byte, 10240), 0644); err != nil {
		t.Fatal(err)
	}

	idx, err := BuildIndex([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	missing := []Track{
		{Path: filepath.Join("C:\\", "old", "track.mp3"), FileSizeKB: 10},
	}
	cands := Relocate(missing, idx)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].NewPath != fileB {
		t.Errorf("expected size-match winner %q, got %q", fileB, cands[0].NewPath)
	}
	if cands[0].Confidence != 0.9 {
		t.Errorf("Confidence = %v, want 0.9", cands[0].Confidence)
	}
}

func TestRelocateNoMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "other.mp3"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	idx, _ := BuildIndex([]string{dir})
	cands := Relocate([]Track{{Path: "/old/track.mp3"}}, idx)
	if len(cands) != 0 {
		t.Errorf("expected no candidates, got %+v", cands)
	}
}

func TestBuildIndexSkipsUnreadable(t *testing.T) {
	// Non-existent root should not error - just return empty index.
	idx, err := BuildIndex([]string{filepath.Join(t.TempDir(), "nonexistent")})
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 0 {
		t.Errorf("expected empty index, got %v", idx)
	}
}

// ── WriteFixedCollection ─────────────────────────────────────────────────────

// minimalNML is a small but valid collection with one track and one unrelated
// tag so we can verify untouched content is preserved.
const minimalNML = `<?xml version="1.0" encoding="UTF-8" standalone="no" ?>
<NML VERSION="20">
<COLLECTION ENTRIES="1">
<ENTRY TITLE="Lost Track" ARTIST="DJ Test">
<LOCATION DIR="/:old/:folder/:" FILE="track.mp3" VOLUME="C:" VOLUMEID="abc"></LOCATION>
<INFO BITRATE="320000" FILESIZE="9765"></INFO>
</ENTRY>
</COLLECTION>
<PLAYLISTS><NODE TYPE="PLAYLIST" NAME="keep"></NODE></PLAYLISTS>
</NML>`

func TestWriteFixedCollection(t *testing.T) {
	// Build new path in a temp dir so splitPath round-trips properly.
	newDir := t.TempDir()
	newPath := filepath.Join(newDir, "new_folder", "track.mp3")
	if err := os.MkdirAll(filepath.Dir(newPath), 0755); err != nil {
		t.Fatal(err)
	}

	// The old path as resolveLocation would build it on this OS.
	oldPath := resolveLocation("C:", "/:old/:folder/:", "track.mp3")

	plan := FixPlan{Fixes: []Candidate{
		{
			Track:      Track{Path: oldPath, Title: "Lost Track"},
			NewPath:    newPath,
			Confidence: 1.0,
		},
	}}

	var buf bytes.Buffer
	fixed, err := WriteFixedCollection(strings.NewReader(minimalNML), plan, &buf)
	if err != nil {
		t.Fatalf("WriteFixedCollection: %v", err)
	}
	if fixed != 1 {
		t.Errorf("fixed = %d, want 1", fixed)
	}

	out := buf.String()

	// New file basename must appear in the output.
	if !strings.Contains(out, "track.mp3") {
		t.Error("output missing track.mp3 filename")
	}

	// Unrelated content must be preserved.
	if !strings.Contains(out, "keep") {
		t.Error("output missing preserved PLAYLISTS node")
	}
	if !strings.Contains(out, "DJ Test") {
		t.Error("output missing artist attribute")
	}

	// The old DIR path should no longer appear.
	if strings.Contains(out, "/:old/:folder/:") {
		t.Error("old DIR path still present in output")
	}

	// Verify new path components are present (volume + some dir segment).
	_, newDirComponent, _ := splitPath(newPath)
	if !strings.Contains(out, newDirComponent) {
		t.Errorf("new DIR component %q not found in output:\n%s", newDirComponent, out)
	}
}

func TestWriteFixedCollectionNoMatchesPreservesAll(t *testing.T) {
	plan := FixPlan{} // empty plan
	var buf bytes.Buffer
	fixed, err := WriteFixedCollection(strings.NewReader(minimalNML), plan, &buf)
	if err != nil {
		t.Fatalf("WriteFixedCollection: %v", err)
	}
	if fixed != 0 {
		t.Errorf("fixed = %d, want 0", fixed)
	}
	out := buf.String()
	if !strings.Contains(out, "Lost Track") {
		t.Error("track title missing from unmodified output")
	}
}
