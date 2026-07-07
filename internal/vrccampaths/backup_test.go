package vrccampaths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBackupAndLatest(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "path.json")
	if err := os.WriteFile(src, []byte(`[{"index":0}]`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, ok := LatestBackup(dir, "wrld_1"); ok {
		t.Fatal("no backup expected yet")
	}
	if _, err := Backup(dir, src, "", "X"); err == nil {
		t.Fatal("empty worldID must error")
	}

	e, err := Backup(dir, src, "wrld_1", "Club A")
	if err != nil {
		t.Fatal(err)
	}
	if e.WorldName != "Club A" || e.WorldID != "wrld_1" {
		t.Fatalf("bad entry: %+v", e)
	}

	got, ok := LatestBackup(dir, "wrld_1")
	if !ok || got.File != e.File {
		t.Fatalf("LatestBackup = %+v, %v", got, ok)
	}
	if b, _ := os.ReadFile(got.File); string(b) != `[{"index":0}]` {
		t.Fatalf("backup content mismatch: %q", b)
	}

	// Overwrite: latest reflects the newer source, one file per world.
	if err := os.WriteFile(src, []byte(`[{"index":9}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Backup(dir, src, "wrld_1", "Club A"); err != nil {
		t.Fatal(err)
	}
	got2, _ := LatestBackup(dir, "wrld_1")
	if got2.File != e.File {
		t.Fatalf("expected same per-world file, got %s vs %s", got2.File, e.File)
	}
	if b, _ := os.ReadFile(got2.File); string(b) != `[{"index":9}]` {
		t.Fatalf("overwrite failed: %q", b)
	}

	// Missing file → not restorable.
	if err := os.Remove(got2.File); err != nil {
		t.Fatal(err)
	}
	if _, ok := LatestBackup(dir, "wrld_1"); ok {
		t.Fatal("deleted backup must not be reported")
	}
}
