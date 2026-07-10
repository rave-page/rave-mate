package train

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rave.page/mate/internal/gridfix"
)

func writeTemp(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuildManifest(t *testing.T) {
	dir := t.TempDir()
	a := writeTemp(t, dir, "a.mp3")
	b := writeTemp(t, dir, "b.flac")
	verified := []gridfix.VerifiedGrid{
		{Path: a, BPM: 174, StartMs: 120.5},
		{Path: filepath.Join(dir, "gone.mp3"), BPM: 128, StartMs: 0}, // missing file
		{Path: b, BPM: 128.5, StartMs: 33},
		{Path: a, BPM: 0, StartMs: 1}, // bad bpm
	}
	out := filepath.Join(dir, "models")
	p, skipped, err := BuildManifest(verified, out, TrainOptions{Epochs: 2})
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 2 {
		t.Fatalf("skipped = %d, want 2", skipped)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Audio) != 2 || m.Audio[0].Path != a || m.Audio[1].BPM != 128.5 {
		t.Fatalf("audio = %+v", m.Audio)
	}
	if m.Audio[0].StartMs != 120.5 {
		t.Fatalf("startMs = %v", m.Audio[0].StartMs)
	}
	// defaults applied except explicit epochs
	if m.Epochs != 2 || m.BaseCheckpoint != "final0" || m.LR != 5e-5 || m.ValSplit != 0.1 || m.Seed != 1 {
		t.Fatalf("opts = %+v", m)
	}
	if m.OutDir != out {
		t.Fatalf("outDir = %q", m.OutDir)
	}
}

func TestBuildManifestTooFew(t *testing.T) {
	dir := t.TempDir()
	a := writeTemp(t, dir, "a.mp3")
	_, skipped, err := BuildManifest([]gridfix.VerifiedGrid{
		{Path: a, BPM: 140, StartMs: 0},
		{Path: filepath.Join(dir, "gone.mp3"), BPM: 140, StartMs: 0},
	}, dir, TrainOptions{})
	if err == nil {
		t.Fatal("want error for <2 usable tracks")
	}
	if skipped != 1 {
		t.Fatalf("skipped = %d, want 1", skipped)
	}
}

func TestListCheckpoints(t *testing.T) {
	dir := t.TempDir()
	old := writeTemp(t, dir, "finetuned-20250101-0000.ckpt")
	newer := writeTemp(t, dir, "finetuned-20260101-0000.ckpt")
	writeTemp(t, dir, "not-a-checkpoint.txt")
	past := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}
	got := ListCheckpoints(dir)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Path != newer || got[1].Path != old {
		t.Fatalf("order = %q, %q", got[0].Path, got[1].Path)
	}
	if got[0].Name != "finetuned-20260101-0000" {
		t.Fatalf("name = %q", got[0].Name)
	}
	if ListCheckpoints(filepath.Join(dir, "missing")) != nil {
		t.Fatal("missing dir should yield nil")
	}
}
