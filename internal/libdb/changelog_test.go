package libdb

import (
	"path/filepath"
	"testing"

	"rave.page/mate/internal/musiclib"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	d.SetNodeID("node-A")
	return d
}

func TestTrackHashStable(t *testing.T) {
	a := TrackHash("Daft Punk", "Aerodynamic", 212.4)
	b := TrackHash("  daft punk ", "AERODYNAMIC", 212.0) // case/space-insensitive, rounded dur
	if a != b {
		t.Errorf("hash not stable across case/space/rounding: %q vs %q", a, b)
	}
	if TrackHash("A", "B", 100) == TrackHash("A", "C", 100) {
		t.Error("different titles collided")
	}
}

// importTrack runs a one-track sync for the given source + play count.
func importTrack(t *testing.T, d *DB, srcID int64, path string, plays int) {
	t.Helper()
	sy, err := d.BeginTrackSync(srcID)
	if err != nil {
		t.Fatalf("begin sync: %v", err)
	}
	if err := sy.Add(musiclib.Track{Path: path, Artist: "Artist", Title: "Title", DurationSec: 100, PlayCount: plays}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := sy.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestImportDiffAppendAndRevert(t *testing.T) {
	d := openTestDB(t)
	src, err := d.UpsertSource(musiclib.Source{App: "traktor", Path: "/coll.nml"}, 0)
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	hash := TrackHash("Artist", "Title", 100)

	// First import (new track) → one "_import" baseline event.
	importTrack(t, d, src.ID, "/a.mp3", 3)
	evs, err := d.ChangesForTrack(hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Field != "_import" {
		t.Fatalf("want 1 _import event, got %+v", evs)
	}

	// Refresh with a higher play count → a play_count "set" event (old 3 → new 7).
	importTrack(t, d, src.ID, "/a.mp3", 7)
	evs, err = d.ChangesForTrack(hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("want 2 events after change, got %d: %+v", len(evs), evs)
	}
	// newest-first + monotonic seq
	if !(evs[0].Seq > evs[1].Seq) {
		t.Errorf("seq not monotonic/newest-first: %d then %d", evs[0].Seq, evs[1].Seq)
	}
	top := evs[0]
	if top.Field != "play_count" || top.OldValue != "3" || top.NewValue != "7" {
		t.Fatalf("unexpected change event: %+v", top)
	}

	// Revert the play_count change → tracks row back to 3, event flagged.
	if err := d.RevertChange(top.ID); err != nil {
		t.Fatalf("revert: %v", err)
	}
	tracks, err := d.LoadTracks(src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[0].PlayCount != 3 {
		t.Fatalf("revert didn't restore play_count to 3: %+v", tracks)
	}
	got, ok, err := d.LatestChange(hash, "play_count")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Errorf("expected no non-reverted play_count change after revert, got %+v", got)
	}
}

func TestAppendChangesEmptyAndNil(t *testing.T) {
	d := openTestDB(t)
	if err := d.AppendChanges(nil); err != nil {
		t.Errorf("nil events should be a no-op: %v", err)
	}
	var nild *DB
	if err := nild.AppendChanges([]ChangeEvent{{Field: "x", Op: "set", Origin: "manual", TrackHash: "h"}}); err != nil {
		t.Errorf("nil DB should be a no-op: %v", err)
	}
}

// TestLibraryVersionIsInMemoryEpoch: phase B4b moved LibraryVersion off SELECT MAX(seq) onto an
// in-memory epoch, so a hot caller (the webui render lane) never queues behind the single writer
// conn. Proof by execution: the epoch still advances with appends AND still answers after the SQL
// handle is closed - the query path returned 0 there.
func TestLibraryVersionIsInMemoryEpoch(t *testing.T) {
	d := openTestDB(t)
	if v := d.LibraryVersion(); v != 0 {
		t.Fatalf("empty log version = %d, want 0", v)
	}
	ev := []ChangeEvent{{TrackHash: "h1", Field: "play_count", Op: "set", NewValue: "1", Origin: "test"}}
	if err := d.AppendChanges(ev); err != nil {
		t.Fatalf("append: %v", err)
	}
	v1 := d.LibraryVersion()
	if v1 == 0 {
		t.Fatal("version did not advance on append")
	}
	// still MAX(seq) for the committed log: persisted store caches compare epochs across restarts.
	var max int64
	if err := d.db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM change_log WHERE node_id=?`, d.nodeID).Scan(&max); err != nil {
		t.Fatalf("max seq: %v", err)
	}
	if v1 != max {
		t.Fatalf("epoch %d != MAX(seq) %d", v1, max)
	}
	if err := d.AppendChanges(ev); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	if v2 := d.LibraryVersion(); v2 <= v1 {
		t.Fatalf("second append: %d not > %d", v2, v1)
	}
	// the read no longer touches SQL at all
	want := d.LibraryVersion()
	if err := d.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := d.LibraryVersion(); got != want {
		t.Fatalf("after Close: %d, want %d (read still hits SQL)", got, want)
	}
}

// TestLibraryVersionSeedsFromDisk: a reopened DB reports the persisted MAX(seq), not 0 - store
// mtime slots keyed on the epoch must not go stale-valid across a restart.
func TestLibraryVersionSeedsFromDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lib.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	d.SetNodeID("node-A")
	if err := d.AppendChanges([]ChangeEvent{{TrackHash: "h1", Field: "rating", Op: "set", NewValue: "5"}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	first := d.LibraryVersion()
	_ = d.Close()

	d2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = d2.Close() })
	d2.SetNodeID("node-A")
	if got := d2.LibraryVersion(); got != first {
		t.Fatalf("reopened epoch = %d, want the persisted %d", got, first)
	}
}
