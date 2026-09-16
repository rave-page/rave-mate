package webui

import (
	"runtime"
	"testing"

	"rave.page/mate/internal/localmedia"
)

func TestEnforceExt(t *testing.T) {
	cases := []struct{ name, ext, want string }{
		{"set", "mp4", "set.mp4"},
		{"set.mp4", "mp4", "set.mp4"},
		{"set.MP4", "mp4", "set.MP4"}, // case-insensitive match, keep the user's spelling
		{"set.mov", "mp4", "set.mov.mp4"},
		{"set", "", "set"},
		{"", "mp4", ""},
	}
	for _, c := range cases {
		if got := enforceExt(c.name, c.ext); got != c.want {
			t.Errorf("enforceExt(%q,%q)=%q want %q", c.name, c.ext, got, c.want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{{0, "0 B"}, {512, "512 B"}, {2048, "2 KB"}, {1572864, "1.5 MB"}, {2147483648, "2.0 GB"}}
	for _, c := range cases {
		if got := humanSize(c.n); got != c.want {
			t.Errorf("humanSize(%d)=%q want %q", c.n, got, c.want)
		}
	}
}

func TestPkCrumbSegs(t *testing.T) {
	// OS-native path: the crumb root is the volume on Windows ("C:") and "/" elsewhere. A Windows
	// literal on Linux is one opaque name - what ubuntu CI caught.
	dir, root := "/home/dy/Music", "/"
	if runtime.GOOS == "windows" {
		dir, root = `C:\Users\dy\Music`, "C:"
	}
	segs := pkCrumbSegs(dir)
	if len(segs) != 4 {
		t.Fatalf("want 4 crumb segments, got %d: %+v", len(segs), segs)
	}
	if segs[0][0] != root || segs[len(segs)-1][0] != "Music" {
		t.Errorf("crumb ends = %q..%q", segs[0][0], segs[len(segs)-1][0])
	}
	// each cumulative path deepens
	if segs[1][1] == segs[2][1] {
		t.Errorf("cumulative crumb paths not distinct: %+v", segs)
	}
}

func mkEntries() []localmedia.Entry {
	return []localmedia.Entry{
		{Name: "b.mp3", IsDirectory: false, SizeBytes: 30, ModifiedAt: "2026-01-02T00:00:00Z", Kind: "audio", Extension: "mp3"},
		{Name: "sub", IsDirectory: true, ModifiedAt: "2026-01-01T00:00:00Z", Kind: "directory"},
		{Name: "a.txt", IsDirectory: false, SizeBytes: 10, ModifiedAt: "2026-01-03T00:00:00Z", Kind: "other", Extension: "txt"},
		{Name: "c.png", IsDirectory: false, SizeBytes: 20, ModifiedAt: "2026-01-04T00:00:00Z", Kind: "image", Extension: "png"},
	}
}

func TestPkVisibleDirKindHidesFiles(t *testing.T) {
	s := &pkState{kind: "dir", entries: mkEntries(), sortBy: "name"}
	vis, total := pkVisible(s)
	if total != 1 || len(vis) != 1 || !vis[0].IsDirectory {
		t.Fatalf("dir kind must show only folders, got %d: %+v", total, vis)
	}
}

func TestPkVisibleExtFilter(t *testing.T) {
	s := &pkState{kind: "file", entries: mkEntries(), sortBy: "name",
		filter: pickFilter{exts: []string{"mp3"}}}
	vis, _ := pkVisible(s)
	// dir always shown + the mp3; txt/png filtered out
	names := map[string]bool{}
	for _, e := range vis {
		names[e.Name] = true
	}
	if !names["sub"] || !names["b.mp3"] || names["a.txt"] || names["c.png"] {
		t.Fatalf("ext filter wrong: %+v", names)
	}
	// "All files" override shows everything
	s.filterAll = true
	if vis, _ := pkVisible(s); len(vis) != 4 {
		t.Fatalf("All files must show 4, got %d", len(vis))
	}
}

func TestPkVisibleSearchAndSort(t *testing.T) {
	s := &pkState{kind: "file", entries: mkEntries(), sortBy: "size", sortDesc: true}
	vis, _ := pkVisible(s)
	// dirs first regardless of sort key
	if !vis[0].IsDirectory {
		t.Fatalf("dirs must sort first: %+v", vis)
	}
	// files by size desc: b.mp3(30) > c.png(20) > a.txt(10)
	if vis[1].Name != "b.mp3" || vis[2].Name != "c.png" || vis[3].Name != "a.txt" {
		t.Fatalf("size-desc order wrong: %v %v %v", vis[1].Name, vis[2].Name, vis[3].Name)
	}
	s.search = "png"
	if vis, _ := pkVisible(s); len(vis) != 1 || vis[0].Name != "c.png" {
		t.Fatalf("search wrong: %+v", vis)
	}
}

func TestPkVisibleCap(t *testing.T) {
	many := make([]localmedia.Entry, pkMaxRows+50)
	for i := range many {
		many[i] = localmedia.Entry{Name: "f" + string(rune('a'+i%26)), Extension: "txt", Kind: "other"}
	}
	s := &pkState{kind: "file", entries: many, sortBy: "name"}
	vis, total := pkVisible(s)
	if total != pkMaxRows+50 || len(vis) != pkMaxRows {
		t.Fatalf("cap failed: total=%d shown=%d", total, len(vis))
	}
}
