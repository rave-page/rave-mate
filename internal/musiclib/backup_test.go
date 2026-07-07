package musiclib

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// syntheticInstall builds a temp Traktor "install" with a fake collection.nml and one
// History .nml. Returns the TraktorInstall and a cleanup func.
func syntheticInstall(t *testing.T) TraktorInstall {
	t.Helper()
	dir := t.TempDir()

	collContent := []byte(`<?xml version="1.0" encoding="UTF-8"?><NML VERSION="19"></NML>`)
	collPath := filepath.Join(dir, "collection.nml")
	if err := os.WriteFile(collPath, collContent, 0o644); err != nil {
		t.Fatal(err)
	}

	histDir := filepath.Join(dir, "History")
	if err := os.Mkdir(histDir, 0o755); err != nil {
		t.Fatal(err)
	}
	histContent := []byte(`<?xml version="1.0" encoding="UTF-8"?><NML VERSION="19"><HISTORY></HISTORY></NML>`)
	if err := os.WriteFile(filepath.Join(histDir, "history-001.nml"), histContent, 0o644); err != nil {
		t.Fatal(err)
	}

	return TraktorInstall{
		Version:    "4.2.0",
		Dir:        dir,
		Collection: collPath,
		HistoryDir: histDir,
	}
}

func TestBackupCollectionAt(t *testing.T) {
	in := syntheticInstall(t)
	backupRoot := t.TempDir()
	now := time.Date(2025, 3, 15, 12, 30, 45, 0, time.UTC)

	bk, err := BackupCollectionAt(in, backupRoot, now)
	if err != nil {
		t.Fatalf("BackupCollectionAt: %v", err)
	}

	// correct dir name
	want := filepath.Join(backupRoot, "traktor-4.2.0-20250315-123045")
	if bk.Path != want {
		t.Errorf("Path = %q; want %q", bk.Path, want)
	}
	if bk.When != now {
		t.Errorf("When = %v; want %v", bk.When, now)
	}
	if bk.Source != in.Collection {
		t.Errorf("Source = %q; want %q", bk.Source, in.Collection)
	}

	// collection copy exists + content matches
	destColl := filepath.Join(bk.Path, "collection.nml")
	got, err := os.ReadFile(destColl)
	if err != nil {
		t.Fatalf("read backed-up collection: %v", err)
	}
	orig, _ := os.ReadFile(in.Collection)
	if !bytes.Equal(got, orig) {
		t.Error("backed-up collection content differs from original")
	}

	// history copy exists
	destHist := filepath.Join(bk.Path, "History", "history-001.nml")
	gotH, err := os.ReadFile(destHist)
	if err != nil {
		t.Fatalf("read backed-up history file: %v", err)
	}
	origH, _ := os.ReadFile(filepath.Join(in.HistoryDir, "history-001.nml"))
	if !bytes.Equal(gotH, origH) {
		t.Error("backed-up history content differs from original")
	}

	// original collection is unchanged (same content, stat still works)
	origAfter, err := os.ReadFile(in.Collection)
	if err != nil {
		t.Fatalf("re-read original collection: %v", err)
	}
	if !bytes.Equal(origAfter, orig) {
		t.Error("original collection was modified by backup")
	}
	if _, err := os.Stat(in.Collection); err != nil {
		t.Errorf("original collection stat failed: %v", err)
	}

	// SizeBytes > 0
	if bk.SizeBytes <= 0 {
		t.Errorf("SizeBytes = %d; want > 0", bk.SizeBytes)
	}
}

func TestListBackups(t *testing.T) {
	in := syntheticInstall(t)
	backupRoot := t.TempDir()

	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)

	_, err := BackupCollectionAt(in, backupRoot, t1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = BackupCollectionAt(in, backupRoot, t2)
	if err != nil {
		t.Fatal(err)
	}

	backups, err := ListBackups(backupRoot)
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(backups) != 2 {
		t.Fatalf("got %d backups; want 2", len(backups))
	}
	// newest first
	if !backups[0].When.After(backups[1].When) {
		t.Error("backups not sorted newest-first")
	}
	for _, b := range backups {
		if b.SizeBytes <= 0 {
			t.Errorf("backup %s has SizeBytes %d; want > 0", b.Path, b.SizeBytes)
		}
	}
}

func TestPruneBackups(t *testing.T) {
	in := syntheticInstall(t)
	backupRoot := t.TempDir()

	t1 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	bk1, _ := BackupCollectionAt(in, backupRoot, t1)
	bk2, _ := BackupCollectionAt(in, backupRoot, t2)

	if err := PruneBackups(backupRoot, 1); err != nil {
		t.Fatalf("PruneBackups: %v", err)
	}

	remaining, err := ListBackups(backupRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Fatalf("got %d remaining; want 1", len(remaining))
	}
	// older one should be gone
	if _, err := os.Stat(bk1.Path); !os.IsNotExist(err) {
		t.Error("older backup was not removed")
	}
	// newer one must still exist
	if remaining[0].Path != bk2.Path {
		t.Errorf("kept backup path = %q; want %q", remaining[0].Path, bk2.Path)
	}
}

func TestPruneBackupsGuards(t *testing.T) {
	if err := PruneBackups("", 5); err == nil {
		t.Error("expected error for empty backupRoot")
	}
	if err := PruneBackups(`C:\Users\DJ\Documents\Native Instruments\Traktor 4.2.0`, 5); err == nil {
		t.Error("expected error for library path")
	}
}

func TestBackupCollectionAtNoCollection(t *testing.T) {
	in := TraktorInstall{Version: "4.2.0", Dir: t.TempDir()}
	_, err := BackupCollectionAt(in, t.TempDir(), time.Now())
	if err == nil {
		t.Error("expected error when Collection is empty")
	}
}
