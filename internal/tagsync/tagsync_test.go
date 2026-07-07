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

func TestApplyUnsupported(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "x.wav")
	_ = os.WriteFile(wav, []byte("RIFF"), 0o644)
	if _, err := Apply(nil, musiclib.Track{Path: wav, BPM: 120}); err != ErrUnsupported {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
}
