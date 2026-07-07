package tagwrite

import (
	"os"
	"path/filepath"
	"testing"
)

// roundTrip writes tags, reads them back, and asserts each field - then clears one and
// asserts the clear (the revert path) - on whatever file `path` points at.
func roundTrip(t *testing.T, path string) {
	t.Helper()
	want := Tags{
		FieldTitle: "Graveyard Filler", FieldArtist: "Stonx", FieldAlbum: "Premiere",
		FieldGenre: "Hard Techno", FieldComment: "ripped by rave-mate", FieldBPM: "150", FieldKey: "8A",
	}
	if err := Write(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("field %q = %q, want %q", k, got[k], v)
		}
	}
	// Clearing a field (the revert primitive) must remove it.
	if err := Write(path, Tags{FieldKey: ""}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got2, _ := Read(path)
	if got2[FieldKey] != "" {
		t.Errorf("key not cleared: %q", got2[FieldKey])
	}
	// Clearing key must not disturb a sibling field.
	if got2[FieldBPM] != "150" {
		t.Errorf("bpm disturbed by key clear: %q", got2[FieldBPM])
	}
}

func TestMP3RoundTrip(t *testing.T) {
	// id3v2 creates a valid tag on an empty file - no audio needed for tag round-trip.
	path := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	roundTrip(t, path)
}

func TestFLACRoundTrip(t *testing.T) {
	src, err := os.ReadFile("testdata/silence.flac")
	if err != nil {
		t.Skipf("no flac fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "track.flac")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	roundTrip(t, path)
}

func TestSupported(t *testing.T) {
	for _, p := range []string{"a.mp3", "A.FLAC"} {
		if !Supported(p) {
			t.Errorf("Supported(%q) = false", p)
		}
	}
	for _, p := range []string{"a.m4a", "a.wav", "a.aiff", "a.ogg"} {
		if Supported(p) {
			t.Errorf("Supported(%q) = true (should be unsupported for now)", p)
		}
	}
}
