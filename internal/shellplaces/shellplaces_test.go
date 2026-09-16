package shellplaces

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseGTKBookmarks(t *testing.T) {
	in := "file:///home/dy/Music\n" +
		"file:///home/dy/DJ%20Sets Live Sets\n" +
		"smb://nas/share Remote\n" +
		"\n" +
		"file:///data/recordings\n"
	got := parseGTKBookmarks(in)
	if len(got) != 3 {
		t.Fatalf("want 3 file:// bookmarks, got %d: %+v", len(got), got)
	}
	if got[0].Path != filepath.FromSlash("/home/dy/Music") || got[0].Name != "Music" || !got[0].Pinned {
		t.Errorf("bookmark 0 = %+v", got[0])
	}
	if got[1].Path != filepath.FromSlash("/home/dy/DJ Sets") { // %20 decoded
		t.Errorf("percent-decode failed: %q", got[1].Path)
	}
	if got[1].Name != "Live Sets" { // explicit label wins over base
		t.Errorf("label = %q, want %q", got[1].Name, "Live Sets")
	}
}

func TestParseXDGUserDirs(t *testing.T) {
	in := "# comment\n" +
		"XDG_DESKTOP_DIR=\"$HOME/Desktop\"\n" +
		"XDG_MUSIC_DIR=\"$HOME/Music\"\n" +
		"XDG_DOWNLOAD_DIR=\"$HOME\"\n" // == home: dropped
	got := parseXDGUserDirs(in, "/home/dy")
	if len(got) != 2 {
		t.Fatalf("want 2 dirs, got %d: %+v", len(got), got)
	}
	if got[0].Path != filepath.Clean("/home/dy/Desktop") || got[0].Pinned {
		t.Errorf("desktop = %+v", got[0])
	}
	if got[1].Name != "Music" {
		t.Errorf("name = %q, want Music", got[1].Name)
	}
}

func TestNormalizePinnedFirstAndDedup(t *testing.T) {
	// OS-native paths (a Windows literal is one opaque name on Linux). The duplicate is a case
	// variant where the filesystem is case-insensitive, an exact repeat elsewhere.
	home := filepath.Join(string(filepath.Separator), "home", "dy")
	dup := filepath.Join(home, "Downloads")
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		dup = filepath.Join(home, "downloads")
	}
	in := []Place{
		{Path: filepath.Join(home, "Downloads"), Pinned: false},
		{Path: filepath.Join(home, "Music"), Name: "Music", Pinned: true},
		{Path: dup, Pinned: false},  // dup
		{Path: "  ", Pinned: false}, // blank dropped
	}
	got := normalize(in)
	if len(got) != 2 {
		t.Fatalf("want 2 after dedup+blank-drop, got %d: %+v", len(got), got)
	}
	if !got[0].Pinned || got[0].Name != "Music" {
		t.Errorf("pinned must sort first: %+v", got[0])
	}
	if got[1].Name != "Downloads" { // name filled from base
		t.Errorf("name not filled from base: %+v", got[1])
	}
}

// TestPlacesLive runs the real OS enumeration. On Windows it must return the Quick Access folders
// (this machine has pins); elsewhere it must not panic.
func TestPlacesLive(t *testing.T) {
	got := Places()
	for _, p := range got {
		if p.Path == "" {
			t.Fatalf("empty path in result: %+v", got)
		}
	}
	if runtime.GOOS == "windows" && len(got) == 0 {
		t.Log("no Quick Access folders enumerated - unusual but not fatal (empty pins)")
	}
	// cache path: second call returns the same set
	if len(Places()) != len(got) {
		t.Error("cached Places() disagreed with the first call")
	}
	for _, p := range got {
		if strings.TrimSpace(p.Name) == "" {
			t.Errorf("place has no name: %+v", p)
		}
	}
}
