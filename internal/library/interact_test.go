package library

import (
	"os"
	"path/filepath"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
)

// TestBrowserInteract drives the real widget interactions (render rows, select a file,
// navigate into a dir) headlessly - catches panics that tab-switching alone misses.
func TestBrowserInteract(t *testing.T) {
	test.NewApp()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "song.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	b := &Browser{cwd: dir}
	view := b.View()
	w := test.NewWindow(view)
	defer w.Close()
	w.Resize(fyne.NewSize(900, 600))

	if len(b.entries) < 2 {
		t.Fatalf("want >=2 entries, got %d", len(b.entries))
	}
	// idx 0 = "sub" (dir), idx 1 = "song.mp3" (file)
	b.list.Select(1) // file → metaPanel.show
	b.list.Select(0) // dir → navigate into sub
}
