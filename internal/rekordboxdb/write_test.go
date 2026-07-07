package rekordboxdb

import (
	"os"
	"path/filepath"
	"testing"

	"rave.page/mate/internal/musiclib"
)

// TestInsertIntoRealMasterDB inserts tracks into the master.db at RAVE_REKORDBOX_MASTER_RW
// (must be a COPY - it's rewritten) and re-reads to confirm they landed. Skipped otherwise.
func TestInsertIntoRealMasterDB(t *testing.T) {
	path := os.Getenv("RAVE_REKORDBOX_MASTER_RW")
	if path == "" {
		t.Skip("set RAVE_REKORDBOX_MASTER_RW to a COPY of master.db to run")
	}
	before, err := Open(path, "")
	if err != nil {
		t.Fatalf("open before: %v", err)
	}
	dir := t.TempDir()
	tracks := []musiclib.Track{
		{Path: filepath.Join(dir, "ravesync-a.mp3"), Title: "Rave Sync Probe A", Artist: "rave-mate", Genre: "Neurofunk", BPM: 174, DurationSec: 312, BitrateBps: 320000},
		{Path: filepath.Join(dir, "ravesync-b.flac"), Title: "Rave Sync Probe B", Artist: "rave-mate", Genre: "Techno", BPM: 132, DurationSec: 401},
	}
	res, err := InsertTracks(path, "", tracks)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if res.Inserted != 2 {
		t.Fatalf("inserted=%d skipped=%d, want 2 inserted", res.Inserted, res.Skipped)
	}
	after, err := Open(path, "")
	if err != nil {
		t.Fatalf("open after (re-encrypt may be broken): %v", err)
	}
	if len(after.Tracks) != len(before.Tracks)+2 {
		t.Fatalf("track count %d → %d, want +2", len(before.Tracks), len(after.Tracks))
	}
	found := map[string]bool{}
	for _, tr := range after.Tracks {
		if tr.Title == "Rave Sync Probe A" || tr.Title == "Rave Sync Probe B" {
			found[tr.Title] = true
			t.Logf("found %q artist=%q genre=%q bpm=%v", tr.Title, tr.Artist, tr.Genre, tr.BPM)
		}
	}
	if len(found) != 2 {
		t.Errorf("inserted tracks not all read back: %v", found)
	}

	// Idempotent: a second insert of the same paths adds nothing.
	res2, err := InsertTracks(path, "", tracks)
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}
	if res2.Inserted != 0 || res2.Skipped != 2 {
		t.Errorf("re-insert: inserted=%d skipped=%d, want 0/2", res2.Inserted, res2.Skipped)
	}
}
