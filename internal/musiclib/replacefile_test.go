package musiclib

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplaceFile(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "collection.nml")
	if err := os.WriteFile(dst, []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, "collection-x.nml.tmp")
	if err := os.WriteFile(tmp, []byte("NEW"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(tmp, dst); err != nil {
		t.Fatalf("replaceFile: %v", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "NEW" {
		t.Fatalf("content = %q, want NEW", got)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Fatal("temp not consumed by rename")
	}
}

// A read-only target must not block an explicit write (Windows: clear FILE_ATTRIBUTE_READONLY -
// os.Rename over a read-only target fails with ErrPermission there; on Unix rename ignores target
// perms so this is a trivial pass). The gridfix apply is a deliberate write, not an accident.
func TestReplaceFileReadOnlyTarget(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "collection.nml")
	if err := os.WriteFile(dst, []byte("OLD"), 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dst, 0o644) }) // replaceFile restores read-only; let TempDir remove it
	tmp := filepath.Join(dir, "collection-y.nml.tmp")
	if err := os.WriteFile(tmp, []byte("NEW"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(tmp, dst); err != nil {
		t.Fatalf("replaceFile over read-only target: %v", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "NEW" {
		t.Fatalf("content = %q, want NEW", got)
	}
}
