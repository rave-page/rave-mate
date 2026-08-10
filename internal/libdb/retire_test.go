package libdb

import (
	"testing"

	"rave.page/mate/internal/musiclib"
)

// The Traktor-upgrade shape: same app, new collection path, near-total path overlap.
// The older source must vanish - tracks, imported playlists, source row - and its
// sessions re-home to the keeper. Second run is a no-op.
func TestRetireStaleAppSources(t *testing.T) {
	d := openTmp(t)
	tracks := []musiclib.Track{
		{Path: "/m/a.flac", Title: "A"}, {Path: "/m/b.flac", Title: "B"}, {Path: "/m/c.flac", Title: "C"},
	}
	old, err := d.UpsertSource(musiclib.Source{App: "traktor", Version: "4.2.0", Path: "/t/4.2.0/collection.nml"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	syncAll(t, d, old.ID, tracks)
	if err := d.SyncImportedPlaylists(old.ID, []musiclib.Playlist{{Name: "Heat", Paths: []string{"/m/a.flac"}}}); err != nil {
		t.Fatal(err)
	}
	if err := d.SyncSessions(old.ID, []musiclib.Session{{Name: "2026-08-01"}}); err != nil {
		t.Fatal(err)
	}
	keep, err := d.UpsertSource(musiclib.Source{App: "traktor", Version: "4.5.1", Path: "/t/4.5.1/collection.nml"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	syncAll(t, d, keep.ID, append(tracks, musiclib.Track{Path: "/m/d.flac", Title: "D"}))
	if err := d.SyncImportedPlaylists(keep.ID, []musiclib.Playlist{{Name: "Heat", Paths: []string{"/m/a.flac"}}}); err != nil {
		t.Fatal(err)
	}

	lines, err := d.RetireStaleAppSources()
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if len(lines) == 0 {
		t.Fatal("no retirement reported")
	}
	if r, _ := d.SourceByAppPath("traktor", "/t/4.2.0/collection.nml"); r.ID != 0 {
		t.Fatal("old source survived")
	}
	if n, _ := d.CountTracks(keep.ID); n != 4 {
		t.Fatalf("keeper tracks = %d, want 4", n)
	}
	var total, pls, sess int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM tracks`).Scan(&total); err != nil || total != 4 {
		t.Fatalf("total tracks = %d err=%v, want 4", total, err)
	}
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM playlists WHERE kind='imported'`).Scan(&pls); err != nil || pls != 1 {
		t.Fatalf("imported playlists = %d err=%v, want 1", pls, err)
	}
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE source_id=?`, keep.ID).Scan(&sess); err != nil || sess != 1 {
		t.Fatalf("re-homed sessions = %d err=%v, want 1", sess, err)
	}
	if lines, err = d.RetireStaleAppSources(); err != nil || len(lines) != 0 {
		t.Fatalf("second run lines=%v err=%v, want none", lines, err)
	}
}

// A genuinely different library (disjoint paths) must coexist, never be deleted.
func TestRetireKeepsDistinctLibrary(t *testing.T) {
	d := openTmp(t)
	a, _ := d.UpsertSource(musiclib.Source{App: "traktor", Path: "/pcA/collection.nml"}, 1)
	syncAll(t, d, a.ID, []musiclib.Track{{Path: "/a/1.flac"}, {Path: "/a/2.flac"}})
	b, _ := d.UpsertSource(musiclib.Source{App: "traktor", Path: "/pcB/collection.nml"}, 2)
	syncAll(t, d, b.ID, []musiclib.Track{{Path: "/b/1.flac"}, {Path: "/b/2.flac"}})

	if _, err := d.RetireStaleAppSources(); err != nil {
		t.Fatal(err)
	}
	if r, _ := d.SourceByAppPath("traktor", "/pcA/collection.nml"); r.ID == 0 {
		t.Fatal("distinct library was deleted")
	}
	var total int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM tracks`).Scan(&total); err != nil || total != 4 {
		t.Fatalf("total tracks = %d err=%v, want 4", total, err)
	}
}

// Folder-source rows shadowed by a real import drop; unique folder rows stay; a folder
// source left empty vanishes.
func TestRetireDeshadowsFolderRows(t *testing.T) {
	d := openTmp(t)
	tk, _ := d.UpsertSource(musiclib.Source{App: "traktor", Path: "/t/collection.nml"}, 1)
	syncAll(t, d, tk.ID, []musiclib.Track{{Path: "/m/a.flac"}, {Path: "/m/b.flac"}})
	fMixed, _ := d.EnsureSource("folder", "/m")
	syncAll(t, d, fMixed, []musiclib.Track{{Path: "/m/a.flac"}, {Path: "/m/loose.wav"}})
	fShadow, _ := d.EnsureSource("folder", "/m2")
	syncAll(t, d, fShadow, []musiclib.Track{{Path: "/m/b.flac"}})

	if _, err := d.RetireStaleAppSources(); err != nil {
		t.Fatal(err)
	}
	if n, _ := d.CountTracks(fMixed); n != 1 {
		t.Fatalf("mixed folder source tracks = %d, want 1 (loose only)", n)
	}
	var n int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM sources WHERE app='folder' AND path='/m2'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("fully-shadowed folder source survived (n=%d err=%v)", n, err)
	}
}

// EnsureSource-created rows have NULL version; every later lookup must still scan clean
// (the 2026-08-10 folder-refresh persist failures).
func TestEnsureSourceNullVersionLookup(t *testing.T) {
	d := openTmp(t)
	id1, err := d.EnsureSource("folder", "/m/incoming")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := d.EnsureSource("folder", "/m/incoming")
	if err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if id1 != id2 {
		t.Fatalf("ids differ: %d vs %d", id1, id2)
	}
	if r, err := d.SourceByAppPath("folder", "/m/incoming"); err != nil || r.ID != id1 {
		t.Fatalf("SourceByAppPath id=%d err=%v", r.ID, err)
	}
}
