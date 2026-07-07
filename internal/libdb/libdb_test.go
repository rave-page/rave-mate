package libdb

import (
	"path/filepath"
	"testing"

	"rave.page/mate/internal/musiclib"
)

func openTmp(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func syncAll(t *testing.T, d *DB, src int64, tracks []musiclib.Track) SyncResult {
	t.Helper()
	sy, err := d.BeginTrackSync(src)
	if err != nil {
		t.Fatalf("begin sync: %v", err)
	}
	for _, tr := range tracks {
		if err := sy.Add(tr); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	res, err := sy.Commit()
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return res
}

// TestIncrementalRefresh is the core Phase-1 win: import once, then a refresh upserts only
// the delta (added / updated / removed) instead of re-importing everything.
func TestIncrementalRefresh(t *testing.T) {
	d := openTmp(t)
	src, err := d.UpsertSource(musiclib.Source{App: "traktor", Path: "/c/collection.nml", Version: "4"}, 100)
	if err != nil {
		t.Fatalf("source: %v", err)
	}

	// Initial import: 2 tracks.
	r1 := syncAll(t, d, src.ID, []musiclib.Track{
		{Path: "/m/a.mp3", Artist: "A", Title: "One", BPM: 128, Key: "8A", Cues: []musiclib.CuePoint{{Name: "in", StartMs: 0}}},
		{Path: "/m/b.flac", Artist: "B", Title: "Two", BPM: 140},
	})
	if r1.Added != 2 || r1.Updated != 0 || r1.Removed != 0 {
		t.Fatalf("import1 = %+v, want added=2", r1)
	}

	// Refresh: a.mp3 changed BPM, b.flac gone, c.wav new.
	r2 := syncAll(t, d, src.ID, []musiclib.Track{
		{Path: "/m/a.mp3", Artist: "A", Title: "One", BPM: 130},
		{Path: "/m/c.wav", Artist: "C", Title: "Three"},
	})
	if r2.Added != 1 || r2.Updated != 1 || r2.Removed != 1 {
		t.Fatalf("refresh = %+v, want added=1 updated=1 removed=1", r2)
	}

	got, err := d.LoadTracks(src.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("loaded %d tracks, want 2", len(got))
	}
	// Ordered by artist: A then C. A's BPM is the updated 130.
	if got[0].Artist != "A" || got[0].BPM != 130 {
		t.Fatalf("track[0] = %+v, want A bpm=130 (updated)", got[0])
	}
	if got[1].Artist != "C" {
		t.Fatalf("track[1] = %+v, want C", got[1])
	}
}

func TestSourceRefreshGuard(t *testing.T) {
	d := openTmp(t)
	s := musiclib.Source{App: "traktor", Path: "/c/collection.nml"}
	if _, err := d.UpsertSource(s, 100); err != nil {
		t.Fatal(err)
	}
	row, err := d.SourceByAppPath("traktor", "/c/collection.nml")
	if err != nil || row.ID == 0 || row.CollectionMtime != 100 {
		t.Fatalf("source row = %+v err=%v", row, err)
	}
	// Re-import with a newer mtime updates in place (no duplicate source).
	if _, err := d.UpsertSource(s, 200); err != nil {
		t.Fatal(err)
	}
	first, ok, err := d.FirstSource()
	if err != nil || !ok || first.CollectionMtime != 200 {
		t.Fatalf("first source = %+v ok=%v err=%v", first, ok, err)
	}
}

func TestSessionsRoundTrip(t *testing.T) {
	d := openTmp(t)
	src, _ := d.UpsertSource(musiclib.Source{App: "traktor", Path: "/c/collection.nml"}, 1)
	err := d.SyncSessions(src.ID, []musiclib.Session{
		{Name: "history_2026y06m04d_22h00m00s.nml", Played: []musiclib.PlayedTrack{{Path: "/m/a.mp3", Deck: 1}}},
	})
	if err != nil {
		t.Fatalf("sync sessions: %v", err)
	}
	got, err := d.LoadSessions(src.ID)
	if err != nil || len(got) != 1 || len(got[0].Played) != 1 || got[0].Played[0].Path != "/m/a.mp3" {
		t.Fatalf("sessions = %+v err=%v", got, err)
	}
}
