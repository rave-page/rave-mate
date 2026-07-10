package tagwrite

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	id3v2 "github.com/bogem/id3v2/v2"
)

// roundTrip writes tags, reads them back, and asserts each field - then clears one and
// asserts the clear (the revert path) - on whatever file `path` points at.
func roundTrip(t *testing.T, path string) {
	t.Helper()
	want := Tags{
		FieldTitle: "Graveyard Filler", FieldArtist: "Stonx", FieldAlbum: "Premiere",
		FieldGenre: "Hard Techno", FieldComment: "ripped by rave-mate", FieldBPM: "150", FieldKey: "8A",
		FieldYear: "2024", FieldLabel: "Synthetic Records", FieldRating: "204",
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
	// Clearing the new fields (revert primitive) must remove them.
	if err := Write(path, Tags{FieldYear: "", FieldLabel: "", FieldRating: ""}); err != nil {
		t.Fatalf("clear new fields: %v", err)
	}
	got3, _ := Read(path)
	for _, f := range []string{FieldYear, FieldLabel, FieldRating} {
		if got3[f] != "" {
			t.Errorf("field %q not cleared: %q", f, got3[f])
		}
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

// TestFLACRatingScale: canonical 0-255 ↔ on-disk 0-100, and a raw 0-255 RATING (already
// Traktor-scaled by another writer) reads back unscaled.
func TestFLACRatingScale(t *testing.T) {
	src, err := os.ReadFile("testdata/silence.flac")
	if err != nil {
		t.Skipf("no flac fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "r.flac")
	if err := os.WriteFile(path, src, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ canon, roundTrip string }{
		{"255", "255"}, {"204", "204"}, {"153", "153"}, {"102", "102"}, {"51", "51"}, {"0", "0"},
	} {
		if err := Write(path, Tags{FieldRating: tc.canon}); err != nil {
			t.Fatalf("write %s: %v", tc.canon, err)
		}
		got, _ := Read(path)
		if got[FieldRating] != tc.roundTrip {
			t.Errorf("rating %s round-trips to %q, want %q", tc.canon, got[FieldRating], tc.roundTrip)
		}
	}
	if err := Write(path, Tags{FieldRating: "999"}); err == nil {
		t.Error("out-of-range rating accepted")
	}
}

// TestMP3RatingForeignPOPM: our POPM write/clear preserves another writer's POPM frame.
func TestMP3RatingForeignPOPM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.mp3")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	tag.AddFrame("POPM", id3v2.PopularimeterFrame{Email: "other@example", Rating: 100, Counter: big.NewInt(7)})
	if err := tag.Save(); err != nil {
		t.Fatal(err)
	}
	_ = tag.Close()

	if err := Write(path, Tags{FieldRating: "255"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, _ := Read(path)
	if got[FieldRating] != "255" { // ours wins over foreign
		t.Errorf("rating = %q, want 255", got[FieldRating])
	}
	if err := Write(path, Tags{FieldRating: ""}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got2, _ := Read(path)
	if got2[FieldRating] != "100" { // foreign frame survived, now the fallback
		t.Errorf("foreign POPM lost: rating = %q, want 100", got2[FieldRating])
	}
}

// TestMP3YearV23: a v2.3 tag gets TYER, not TDRC.
func TestMP3YearV23(t *testing.T) {
	path := filepath.Join(t.TempDir(), "y.mp3")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	tag.SetVersion(3)
	tag.AddTextFrame("TIT2", id3v2.EncodingISO, "x") // non-empty so the v3 header persists
	if err := tag.Save(); err != nil {
		t.Fatal(err)
	}
	_ = tag.Close()

	if err := Write(path, Tags{FieldYear: "1999"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	tag2, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tag2.Close() }()
	if f := tag2.GetTextFrame("TYER"); f.Text != "1999" {
		t.Errorf("TYER = %q, want 1999", f.Text)
	}
	if f := tag2.GetTextFrame("TDRC"); f.Text != "" {
		t.Errorf("unexpected TDRC %q on a v2.3 tag", f.Text)
	}
	got, _ := Read(path)
	if got[FieldYear] != "1999" {
		t.Errorf("year read = %q", got[FieldYear])
	}
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
