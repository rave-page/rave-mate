package filexfer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildManifestSingleFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "track.flac")
	if err := os.WriteFile(p, []byte("abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	name, files, total, err := BuildManifest(p)
	if err != nil {
		t.Fatal(err)
	}
	if name != "track.flac" || total != 6 || len(files) != 1 || files[0].Path != "track.flac" || files[0].Size != 6 {
		t.Fatalf("got name=%q files=%+v total=%d", name, files, total)
	}
}

func TestBuildManifestDirectoryWalk(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "set")
	mustWrite(t, filepath.Join(root, "a.txt"), 3)
	mustWrite(t, filepath.Join(root, "sub", "b.bin"), 5)
	mustWrite(t, filepath.Join(root, "sub", "deep", "c"), 0)
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	name, files, total, err := BuildManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if name != "set" || total != 8 || len(files) != 3 {
		t.Fatalf("got name=%q total=%d files=%+v", name, total, files)
	}
	want := map[string]int64{"set/a.txt": 3, "set/sub/b.bin": 5, "set/sub/deep/c": 0}
	for _, f := range files {
		if want[f.Path] != f.Size {
			t.Fatalf("unexpected entry %+v", f)
		}
		delete(want, f.Path)
	}
	if len(want) != 0 {
		t.Fatalf("missing entries: %v", want)
	}
	if err := checkManifest(files); err != nil {
		t.Fatalf("own manifest rejected: %v", err)
	}
}

func TestBuildManifestEmptyDirRejected(t *testing.T) {
	if _, _, _, err := BuildManifest(t.TempDir()); err == nil {
		t.Fatal("want error for empty dir")
	}
}

func TestCheckManifestRejectsHostilePaths(t *testing.T) {
	bad := [][]FileEntry{
		{},
		{{Path: "../evil", Size: 1}},
		{{Path: "/abs", Size: 1}},
		{{Path: `a\b`, Size: 1}},
		{{Path: "a/../../b", Size: 1}},
		{{Path: "ok", Size: -1}},
		{{Path: "dup", Size: 1}, {Path: "dup", Size: 2}},
		{{Path: "a//b", Size: 1}},
		{{Path: "a/./b", Size: 1}},
	}
	for i, files := range bad {
		if err := checkManifest(files); err == nil {
			t.Fatalf("case %d: hostile manifest %+v accepted", i, files)
		}
	}
	if err := checkManifest([]FileEntry{{Path: "dir/file.txt", Size: 1}, {Path: "z", Size: 0}}); err != nil {
		t.Fatalf("good manifest rejected: %v", err)
	}
}

func mustWrite(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
