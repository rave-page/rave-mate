package library

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadDir_orderAndKinds(t *testing.T) {
	dir := t.TempDir()

	// Create: subdir, a.mp3, b.png, .hidden, unknown.xyz
	must(t, os.Mkdir(filepath.Join(dir, "subdir"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "a.mp3"), []byte("x"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "b.png"), []byte("x"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "unknown.xyz"), []byte("x"), 0o644))

	t.Run("hidden excluded", func(t *testing.T) {
		entries, err := readDir(dir, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.name == ".hidden" {
				t.Error("dotfile should be hidden")
			}
		}
		// First entry must be the directory.
		if len(entries) == 0 {
			t.Fatal("no entries")
		}
		if entries[0].kind != kindDirectory {
			t.Errorf("first entry should be dir, got %s", entries[0].kind)
		}
		if entries[0].name != "subdir" {
			t.Errorf("first entry name = %q, want subdir", entries[0].name)
		}
		// Files follow: a.mp3, b.png, unknown.xyz - alpha order
		names := fileNames(entries[1:])
		want := []string{"a.mp3", "b.png", "unknown.xyz"}
		if !sliceEq(names, want) {
			t.Errorf("file order %v, want %v", names, want)
		}
		// Verify kinds
		kindOf := func(name string) kind {
			for _, e := range entries {
				if e.name == name {
					return e.kind
				}
			}
			return ""
		}
		if k := kindOf("a.mp3"); k != kindAudio {
			t.Errorf("a.mp3 kind=%s, want audio", k)
		}
		if k := kindOf("b.png"); k != kindImage {
			t.Errorf("b.png kind=%s, want image", k)
		}
		if k := kindOf("unknown.xyz"); k != kindOther {
			t.Errorf("unknown.xyz kind=%s, want other", k)
		}
	})

	t.Run("hidden included", func(t *testing.T) {
		entries, err := readDir(dir, true)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range entries {
			if e.name == ".hidden" {
				found = true
				break
			}
		}
		if !found {
			t.Error("dotfile should appear when includeHidden=true")
		}
	})
}

func TestClassifyKind(t *testing.T) {
	cases := []struct {
		isDir bool
		ext   string
		want  kind
	}{
		{true, "", kindDirectory},
		{false, "mp4", kindVideo},
		{false, "mov", kindVideo},
		{false, "mp3", kindAudio},
		{false, "flac", kindAudio},
		{false, "png", kindImage},
		{false, "jpg", kindImage},
		{false, "txt", kindOther},
		{false, "", kindOther},
	}
	for _, c := range cases {
		got := classifyKind(c.isDir, c.ext)
		if got != c.want {
			t.Errorf("classifyKind(%v,%q) = %s, want %s", c.isDir, c.ext, got, c.want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	cases := []struct {
		b    int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}
	for _, c := range cases {
		got := humanSize(c.b)
		if got != c.want {
			t.Errorf("humanSize(%d) = %q, want %q", c.b, got, c.want)
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func fileNames(entries []entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.name
	}
	return out
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
