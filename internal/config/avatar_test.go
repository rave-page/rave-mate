package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A picked avatar outside the managed dir is copied in byte-exact and the managed path is returned.
func TestImportAvatarCopiesIn(t *testing.T) {
	srcDir, dstDir := t.TempDir(), t.TempDir()
	src := filepath.Join(srcDir, "cool.vrm")
	want := []byte("VRM-model-bytes")
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := importAvatarInto(dstDir, src)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got != filepath.Join(dstDir, "cool.vrm") {
		t.Fatalf("managed path = %q, want %s/cool.vrm", got, dstDir)
	}
	b, err := os.ReadFile(got)
	if err != nil || string(b) != string(want) {
		t.Fatalf("copied content wrong: %q err=%v", b, err)
	}
}

// A file already inside the managed dir is returned as-is (no self-copy, no truncation).
func TestImportAvatarAlreadyManaged(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "there.glb")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := importAvatarInto(dir, src)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if got != src {
		t.Fatalf("managed path = %q, want %q", got, src)
	}
	if b, _ := os.ReadFile(got); string(b) != "x" {
		t.Fatal("already-managed file was clobbered")
	}
}

// listAvatarsIn returns only model files (case-insensitive ext), name-sorted, with sizes; dirs +
// foreign files are skipped.
func TestListAvatarsIn(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"zed.vrm":   "12345",
		"alpha.glb": "ab",
		"Mid.GLTF":  "xyz", // uppercase ext still counts
		"notes.txt": "ignored",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.vrm"), 0o755); err != nil { // dir with model ext → skipped
		t.Fatal(err)
	}
	got := listAvatarsIn(dir)
	wantNames := []string{"Mid.GLTF", "alpha.glb", "zed.vrm"}
	if len(got) != len(wantNames) {
		t.Fatalf("got %d entries, want %d: %+v", len(got), len(wantNames), got)
	}
	for i, e := range got {
		if e.Name != wantNames[i] {
			t.Errorf("entry %d name = %q, want %q", i, e.Name, wantNames[i])
		}
		if e.Path != filepath.Join(dir, e.Name) {
			t.Errorf("entry %d path = %q", i, e.Path)
		}
		if e.Size != int64(len(files[e.Name])) {
			t.Errorf("entry %d size = %d, want %d", i, e.Size, len(files[e.Name]))
		}
	}
}

// Empty/missing dirs yield nil, never an error or panic.
func TestListAvatarsInEmpty(t *testing.T) {
	if got := listAvatarsIn(""); got != nil {
		t.Fatalf("empty dir string: got %+v", got)
	}
	if got := listAvatarsIn(filepath.Join(t.TempDir(), "nope")); got != nil {
		t.Fatalf("missing dir: got %+v", got)
	}
	if got := listAvatarsIn(t.TempDir()); len(got) != 0 {
		t.Fatalf("empty dir: got %+v", got)
	}
}

// Empty dir (config dir unresolvable) falls back to the original path + an error, not a panic.
func TestImportAvatarNoDir(t *testing.T) {
	got, err := importAvatarInto("", "/some/path/a.vrm")
	if err == nil {
		t.Fatal("want error for empty dir")
	}
	if got != "/some/path/a.vrm" {
		t.Fatalf("fallback path = %q, want the original", got)
	}
}
