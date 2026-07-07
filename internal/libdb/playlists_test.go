package libdb

import (
	"path/filepath"
	"testing"

	"rave.page/mate/internal/musiclib"
)

func openTestPlaylistDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestPlaylistCRUD(t *testing.T) {
	d := openTestPlaylistDB(t)
	id, err := d.CreatePlaylist("Peak Time", PlaylistManual, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := d.CreatePlaylist("", PlaylistManual, ""); err == nil {
		t.Fatal("empty name should fail")
	}

	added, err := d.AddToPlaylist(id, "a.mp3", "b.mp3", "a.mp3", "")
	if err != nil || added != 2 {
		t.Fatalf("add: %d %v", added, err)
	}
	added, err = d.AddToPlaylist(id, "c.mp3", "b.mp3")
	if err != nil || added != 1 {
		t.Fatalf("add dedupe: %d %v", added, err)
	}
	paths, err := d.PlaylistTracks(id)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.mp3", "b.mp3", "c.mp3"}
	if len(paths) != 3 || paths[0] != want[0] || paths[1] != want[1] || paths[2] != want[2] {
		t.Fatalf("order: %v", paths)
	}

	// reorder
	if err := d.ReplacePlaylistTracks(id, []string{"c.mp3", "a.mp3", "b.mp3", "c.mp3"}); err != nil {
		t.Fatal(err)
	}
	paths, _ = d.PlaylistTracks(id)
	if len(paths) != 3 || paths[0] != "c.mp3" || paths[2] != "b.mp3" {
		t.Fatalf("reorder: %v", paths)
	}

	if err := d.RemoveFromPlaylist(id, "a.mp3"); err != nil {
		t.Fatal(err)
	}
	if paths, _ = d.PlaylistTracks(id); len(paths) != 2 {
		t.Fatalf("remove: %v", paths)
	}

	if err := d.RenamePlaylist(id, "Peak"); err != nil {
		t.Fatal(err)
	}
	ls, err := d.ListPlaylists()
	if err != nil || len(ls) != 1 || ls[0].Name != "Peak" || ls[0].TrackCount != 2 {
		t.Fatalf("list: %+v %v", ls, err)
	}

	pls, err := d.PlaylistsForTrack("b.mp3")
	if err != nil || len(pls) != 1 || pls[0].ID != id {
		t.Fatalf("for track: %+v %v", pls, err)
	}

	if err := d.DeletePlaylist(id); err != nil {
		t.Fatal(err)
	}
	if ls, _ = d.ListPlaylists(); len(ls) != 0 {
		t.Fatalf("delete: %+v", ls)
	}
	if paths, _ = d.PlaylistTracks(id); len(paths) != 0 {
		t.Fatalf("cascade: %v", paths)
	}
}

func TestSmartPlaylistRules(t *testing.T) {
	d := openTestPlaylistDB(t)
	id, err := d.CreatePlaylist("High BPM", PlaylistSmart, `{"bpmMin":140}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetSmartRules(id, `{"bpmMin":150}`); err != nil {
		t.Fatal(err)
	}
	r, ok, err := d.PlaylistByID(id)
	if err != nil || !ok || r.Kind != PlaylistSmart || r.Rules != `{"bpmMin":150}` {
		t.Fatalf("by id: %+v %v %v", r, ok, err)
	}
}

func TestSyncImportedPlaylists(t *testing.T) {
	d := openTestPlaylistDB(t)
	src, err := d.UpsertSource(musiclib.Source{App: "traktor", Path: "c.nml"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	manID, _ := d.CreatePlaylist("Mine", PlaylistManual, "")
	_, _ = d.AddToPlaylist(manID, "x.mp3")

	pls := []musiclib.Playlist{
		{Name: "Festival", Folder: "Sets", Paths: []string{"a.mp3", "b.mp3", "a.mp3"}},
		{Name: "Warmup", Paths: []string{"c.mp3"}},
		{Name: ""}, // skipped
	}
	if err := d.SyncImportedPlaylists(src.ID, pls); err != nil {
		t.Fatal(err)
	}
	ls, _ := d.ListPlaylists()
	if len(ls) != 3 { // manual + 2 imported
		t.Fatalf("list: %+v", ls)
	}
	if ls[0].Kind != PlaylistManual { // manual sorts first
		t.Fatalf("sort: %+v", ls)
	}
	var fest PlaylistRow
	for _, r := range ls {
		if r.Name == "Festival" {
			fest = r
		}
	}
	if fest.ID == 0 || fest.Folder != "Sets" || fest.TrackCount != 2 {
		t.Fatalf("festival: %+v", fest)
	}

	// re-sync replaces imported, keeps manual
	if err := d.SyncImportedPlaylists(src.ID, pls[:1]); err != nil {
		t.Fatal(err)
	}
	ls, _ = d.ListPlaylists()
	if len(ls) != 2 {
		t.Fatalf("resync: %+v", ls)
	}
	if got, _ := d.PlaylistTracks(manID); len(got) != 1 {
		t.Fatalf("manual kept: %v", got)
	}
}
