package libdb

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// TestBackupToRunsConcurrentlyWithOpenWrite proves BackupTo no longer monopolises the shared pool.
// The pool is SetMaxOpenConns(1); the OLD BackupTo (VACUUM INTO on d.db) would queue behind ANY held
// connection - here an open write tx - and only run once it freed. The dedicated-connection version
// takes its own WAL read snapshot and completes while the write is still open.
func TestBackupToRunsConcurrentlyWithOpenWrite(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "lib.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()

	// A little committed data so the snapshot has content to verify.
	if _, err := d.EnsureSource("traktor", "C:/music"); err != nil {
		t.Fatal(err)
	}

	// Hold an OPEN WRITE on the shared pool: a real INSERT (not a bare BEGIN, which SQLite defers)
	// pins the one pooled connection for the duration of the test.
	tx, err := d.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`INSERT INTO sources(app,path) VALUES('rekordbox','C:/other')`); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(dir, "backup.db")
	done := make(chan error, 1)
	go func() { done <- d.BackupTo(dest) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("BackupTo failed while a write tx was open: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("BackupTo queued behind the open write tx - it must run on a dedicated connection")
	}

	// The backup file is a valid, queryable SQLite DB reflecting the committed snapshot (the
	// uncommitted rekordbox row is absent, so exactly the one committed source is present).
	b, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	var n int
	if err := b.QueryRow(`SELECT COUNT(*) FROM sources`).Scan(&n); err != nil {
		t.Fatalf("backup is not a valid queryable sqlite DB: %v", err)
	}
	if n != 1 {
		t.Fatalf("backup has %d sources, want 1 (only the committed row)", n)
	}
}
