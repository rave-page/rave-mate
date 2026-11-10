package gridfix

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerifiedStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenVerifiedStore(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if s.Count() != 0 || s.Has("a.mp3") {
		t.Fatal("fresh store not empty")
	}
	if err := s.Mark("b.mp3", 174, 1234.5); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if err := s.Mark("a.mp3", 128, 10); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if !s.Has("a.mp3") || s.Count() != 2 {
		t.Fatal("marks not recorded")
	}
	all := s.All()
	if len(all) != 2 || all[0].Path != "a.mp3" || all[1].Path != "b.mp3" {
		t.Fatalf("All not sorted by path: %+v", all)
	}
	if all[1].BPM != 174 || all[1].StartMs != 1234.5 || all[1].VerifiedAt == "" {
		t.Fatalf("captured values wrong: %+v", all[1])
	}
	// re-mark refreshes captured values
	if err := s.Mark("a.mp3", 130, 20); err != nil {
		t.Fatalf("re-mark: %v", err)
	}
	if s.Count() != 2 || s.All()[0].BPM != 130 {
		t.Fatal("re-mark did not refresh")
	}
	if err := s.Unmark("b.mp3"); err != nil {
		t.Fatalf("unmark: %v", err)
	}
	if s.Has("b.mp3") || s.Count() != 1 {
		t.Fatal("unmark did not remove")
	}
	// persistence across reopen
	s2, err := OpenVerifiedStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if s2.Count() != 1 || !s2.Has("a.mp3") || s2.All()[0].StartMs != 20 {
		t.Fatalf("reload mismatch: %+v", s2.All())
	}
}

// TestVerifiedGridStale: a pin matches the collection only within float noise / marker jitter;
// a re-analyzed BPM or moved marker makes it stale (must not protect the new grid).
func TestVerifiedGridStale(t *testing.T) {
	pin := VerifiedGrid{Path: "x.flac", BPM: 173.999512, StartMs: -38.12}
	cases := []struct {
		name  string
		bpm   float64
		start float64
		stale bool
	}{
		{"exact", 173.999512, -38.12, false},
		{"float noise + jitter", 174.0, -30, false},
		{"re-analyzed off by one", 173.0, -38.12, true},
		{"octave", 87.0, -38.12, true},
		{"marker moved a beat", 174.0, 306.7, true},
	}
	for _, c := range cases {
		if got := pin.Stale(c.bpm, c.start); got != c.stale {
			t.Errorf("%s: Stale(%v, %v) = %v, want %v", c.name, c.bpm, c.start, got, c.stale)
		}
	}
	s, err := OpenVerifiedStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Pinned("x.flac"); ok {
		t.Fatal("unmarked path reported pinned")
	}
	if err := s.Mark("x.flac", 174, -38); err != nil {
		t.Fatal(err)
	}
	if p, ok := s.Pinned("x.flac"); !ok || p.BPM != 174 || p.StartMs != -38 {
		t.Fatalf("Pinned = %+v, %v", p, ok)
	}
}

func TestVerifiedStoreCorruptFileErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "verified.json"), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenVerifiedStore(dir); err == nil {
		t.Fatal("expected error on corrupt store")
	}
}
