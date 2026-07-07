package localmedia

import (
	"os"
	"path/filepath"
	"testing"
)

func mkFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRename(t *testing.T) {
	dir := t.TempDir()
	p := mkFile(t, dir, "a.txt", "x")
	got, err := Rename(p, "b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "b.txt") {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatal("renamed file missing")
	}
	if _, err := Rename(got, "../evil"); err == nil {
		t.Fatal("want separator rejection")
	}
	if _, err := Rename(got, ""); err == nil {
		t.Fatal("want empty rejection")
	}
	mkFile(t, dir, "c.txt", "y")
	if _, err := Rename(got, "c.txt"); err == nil {
		t.Fatal("want exists rejection")
	}
}

func TestMoveFileAndGuards(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	p := mkFile(t, dir, "a.txt", "x")
	got, err := Move(p, sub)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(sub, "a.txt") {
		t.Fatalf("got %q", got)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("source not removed")
	}
	// dest exists
	p2 := mkFile(t, dir, "a.txt", "z")
	if _, err := Move(p2, sub); err == nil {
		t.Fatal("want exists rejection")
	}
	// dir into itself
	if _, err := Move(sub, sub); err == nil {
		t.Fatal("want self-move rejection")
	}
}

func TestDuplicate(t *testing.T) {
	dir := t.TempDir()
	p := mkFile(t, dir, "a.txt", "x")
	c1, err := Duplicate(p)
	if err != nil {
		t.Fatal(err)
	}
	if c1 != filepath.Join(dir, "a copy.txt") {
		t.Fatalf("got %q", c1)
	}
	c2, err := Duplicate(p)
	if err != nil {
		t.Fatal(err)
	}
	if c2 != filepath.Join(dir, "a copy 2.txt") {
		t.Fatalf("got %q", c2)
	}
	if _, err := Duplicate(dir); err == nil {
		t.Fatal("want dir rejection")
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	p := mkFile(t, dir, "a.txt", "x")
	if err := Delete(p); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(filepath.Join(sub, "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	mkFile(t, sub, "b.txt", "x")
	if err := Delete(sub); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Fatal("dir not removed")
	}
	if err := Delete(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("want stat error")
	}
}
