package tagsync

import (
	"os"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
	"rave.page/mate/internal/tagwrite"
)

// TestApplyAndRevert: write a track's analysis into a real file, then revert it back to the
// pre-write state (here: empty), proving the DB-snapshot round-trips through the file.
func TestApplyAndRevert(t *testing.T) {
	db, err := libdb.Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	mp3 := filepath.Join(t.TempDir(), "track.mp3")
	if err := os.WriteFile(mp3, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	tr := musiclib.Track{Path: mp3, BPM: 128, Key: "8A", Genre: "Techno", Comment: "rave-mate"}
	written, err := Apply(db, tr)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if written[tagwrite.FieldBPM] != "128" || written[tagwrite.FieldKey] != "8A" {
		t.Fatalf("written = %v", written)
	}
	// Title/artist must NOT be written (analysis-only).
	if _, ok := written[tagwrite.FieldTitle]; ok {
		t.Fatal("title should not be written")
	}

	got, _ := tagwrite.Read(mp3)
	if got[tagwrite.FieldBPM] != "128" || got[tagwrite.FieldKey] != "8A" || got[tagwrite.FieldGenre] != "Techno" {
		t.Fatalf("file tags after apply = %v", got)
	}

	// Revert restores the pre-write state (all four fields were empty before).
	if err := Revert(db, mp3); err != nil {
		t.Fatalf("revert: %v", err)
	}
	after, _ := tagwrite.Read(mp3)
	for _, f := range []string{tagwrite.FieldBPM, tagwrite.FieldKey, tagwrite.FieldGenre, tagwrite.FieldComment} {
		if after[f] != "" {
			t.Errorf("field %q not reverted: %q", f, after[f])
		}
	}
	// A second revert has nothing to undo.
	if err := Revert(db, mp3); err == nil {
		t.Error("expected error reverting with no pending edit")
	}
}

// TestApplyRating: library rating syncs to the file tag, normalized to canonical 0-255
// (stars *51; raw Traktor 0-255 passes through).
func TestApplyRating(t *testing.T) {
	for _, tc := range []struct {
		lib  int
		want string
	}{{4, "204"}, {204, "204"}, {5, "255"}} {
		mp3 := filepath.Join(t.TempDir(), "r.mp3")
		if err := os.WriteFile(mp3, []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}
		written, err := Apply(nil, musiclib.Track{Path: mp3, Rating: tc.lib})
		if err != nil {
			t.Fatalf("apply rating %d: %v", tc.lib, err)
		}
		if written[tagwrite.FieldRating] != tc.want {
			t.Errorf("rating %d written as %q, want %q", tc.lib, written[tagwrite.FieldRating], tc.want)
		}
		got, _ := tagwrite.Read(mp3)
		if got[tagwrite.FieldRating] != tc.want {
			t.Errorf("rating %d in file = %q, want %q", tc.lib, got[tagwrite.FieldRating], tc.want)
		}
	}
}

// TestApplyTagsRevert: an explicit ApplyTags set (incl. title, which auto-sync never
// writes) snapshots + reverts like Apply.
func TestApplyTagsRevert(t *testing.T) {
	db, err := libdb.Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()
	mp3 := filepath.Join(t.TempDir(), "e.mp3")
	if err := os.WriteFile(mp3, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tagwrite.Write(mp3, tagwrite.Tags{tagwrite.FieldTitle: "Old Title"}); err != nil {
		t.Fatal(err)
	}
	desired := tagwrite.Tags{tagwrite.FieldTitle: "New Title", tagwrite.FieldYear: "2024"}
	if _, err := ApplyTags(db, musiclib.Track{Path: mp3, Title: "New Title"}, desired); err != nil {
		t.Fatalf("apply tags: %v", err)
	}
	got, _ := tagwrite.Read(mp3)
	if got[tagwrite.FieldTitle] != "New Title" || got[tagwrite.FieldYear] != "2024" {
		t.Fatalf("after apply: %v", got)
	}
	if err := Revert(db, mp3); err != nil {
		t.Fatalf("revert: %v", err)
	}
	after, _ := tagwrite.Read(mp3)
	if after[tagwrite.FieldTitle] != "Old Title" || after[tagwrite.FieldYear] != "" {
		t.Errorf("after revert: %v", after)
	}
}

func TestApplyUnsupported(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "x.wav")
	_ = os.WriteFile(wav, []byte("RIFF"), 0o644)
	if _, err := Apply(nil, musiclib.Track{Path: wav, BPM: 120}); err != ErrUnsupported {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
}
