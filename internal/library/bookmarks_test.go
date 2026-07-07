package library

import (
	"path/filepath"
	"testing"
)

func TestBookmarksPersist(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bm.json")
	b := LoadBookmarks(file)

	if b.Has("/music") {
		t.Fatal("empty store should not have /music")
	}
	if !b.Toggle("/music", "Music") {
		t.Fatal("Toggle add should return true")
	}
	if !b.Has("/music") {
		t.Fatal("after add, Has should be true")
	}
	b.Toggle("/sets", "Sets")
	if len(b.List()) != 2 {
		t.Fatalf("want 2, got %d", len(b.List()))
	}

	// Reload from disk → persisted.
	b2 := LoadBookmarks(file)
	if !b2.Has("/music") || !b2.Has("/sets") {
		t.Fatalf("not persisted: %+v", b2.List())
	}
	if b2.List()[0].Label != "Music" {
		t.Errorf("label not persisted: %q", b2.List()[0].Label)
	}

	// Toggle off removes.
	if b2.Toggle("/music", "") {
		t.Fatal("Toggle remove should return false")
	}
	if b2.Has("/music") {
		t.Fatal("after remove, Has should be false")
	}
}
