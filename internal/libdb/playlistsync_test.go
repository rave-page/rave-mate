package libdb

import (
	"fmt"
	"testing"

	"rave.page/mate/internal/musiclib"
)

func TestPlaylistSyncLedgerCRUD(t *testing.T) {
	d := openTestPlaylistDB(t)
	id, _ := d.CreatePlaylist("P", PlaylistManual, "")

	if _, ok, err := d.GetPlaylistSync(id); ok || err != nil {
		t.Fatalf("unmapped: ok=%v err=%v", ok, err)
	}
	r := PlaylistSyncRow{LocalPlaylistID: id, RemoteID: "pl_1", LocalHash: "lh", RemoteHash: "rh"}
	if err := d.SavePlaylistSync(r); err != nil {
		t.Fatal(err)
	}
	got, ok, err := d.GetPlaylistSync(id)
	if err != nil || !ok || got.RemoteID != "pl_1" || got.LocalHash != "lh" || got.SyncedAt.IsZero() {
		t.Fatalf("get: %+v %v %v", got, ok, err)
	}
	r.RemoteHash = "rh2"
	if err := d.SavePlaylistSync(r); err != nil { // upsert
		t.Fatal(err)
	}
	all, err := d.PlaylistSyncRows()
	if err != nil || len(all) != 1 || all[id].RemoteHash != "rh2" {
		t.Fatalf("rows: %+v %v", all, err)
	}
	if err := d.DeletePlaylistSync(id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.GetPlaylistSync(id); ok {
		t.Fatal("unlink failed")
	}
}

func TestDeletePlaylistCascadesSyncState(t *testing.T) {
	d := openTestPlaylistDB(t)
	id, _ := d.CreatePlaylist("P", PlaylistManual, "")
	if err := d.SavePlaylistSync(PlaylistSyncRow{LocalPlaylistID: id, RemoteID: "pl_1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddPlaylistUndo(id, "pull", `{}`); err != nil {
		t.Fatal(err)
	}
	if err := d.DeletePlaylist(id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := d.GetPlaylistSync(id); ok {
		t.Fatal("sync mapping not cascaded")
	}
	if undos, _ := d.PlaylistUndos(id); len(undos) != 0 {
		t.Fatal("undo rows not cascaded")
	}
}

func TestPlaylistUndoPruneKeepsTen(t *testing.T) {
	d := openTestPlaylistDB(t)
	id, _ := d.CreatePlaylist("P", PlaylistManual, "")
	for i := 0; i < 13; i++ {
		if _, err := d.AddPlaylistUndo(id, "pull", fmt.Sprintf(`{"title":"v%d"}`, i)); err != nil {
			t.Fatal(err)
		}
	}
	undos, err := d.PlaylistUndos(id)
	if err != nil || len(undos) != playlistUndoKeep {
		t.Fatalf("kept %d, want %d (%v)", len(undos), playlistUndoKeep, err)
	}
	if undos[0].SnapshotJSON != `{"title":"v12"}` { // newest first
		t.Fatalf("order: %+v", undos[0])
	}
	got, ok, err := d.GetPlaylistUndo(undos[0].ID)
	if err != nil || !ok || got.Direction != "pull" {
		t.Fatalf("get undo: %+v %v %v", got, ok, err)
	}
	if err := d.DeletePlaylistUndo(undos[0].ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := d.PlaylistUndos(id); len(left) != playlistUndoKeep-1 {
		t.Fatalf("delete: %d left", len(left))
	}
}

func TestReplacePlaylistItemsRoundTrip(t *testing.T) {
	d := openTestPlaylistDB(t)
	id, _ := d.CreatePlaylist("P", PlaylistManual, "")
	items := []PlaylistItemRow{
		{Path: `C:\m\a.mp3`},
		{Path: "remote://h1", Title: "Cloud Track", Artist: "X"},
		{Path: `C:\m\b.mp3`},
		{Path: `C:\m\a.mp3`}, // dup dropped
	}
	if err := d.ReplacePlaylistItems(id, items); err != nil {
		t.Fatal(err)
	}
	got, err := d.PlaylistItems(id)
	if err != nil || len(got) != 3 {
		t.Fatalf("items: %+v %v", got, err)
	}
	if got[0].Unresolved() || !got[1].Unresolved() || got[1].Title != "Cloud Track" || got[1].Artist != "X" {
		t.Fatalf("unresolved flags: %+v", got)
	}
	if got[2].Path != `C:\m\b.mp3` {
		t.Fatalf("order: %+v", got)
	}
	// PlaylistTracks (path-only view) still works for legacy callers
	if paths, _ := d.PlaylistTracks(id); len(paths) != 3 || paths[1] != "remote://h1" {
		t.Fatalf("paths: %v", paths)
	}
}

func TestSetPlaylistPulled(t *testing.T) {
	d := openTestPlaylistDB(t)
	id, _ := d.CreatePlaylist("P", PlaylistManual, "")
	if err := d.SetPlaylistPulled(id); err != nil {
		t.Fatal(err)
	}
	r, ok, err := d.PlaylistByID(id)
	if err != nil || !ok || r.PulledAt == "" {
		t.Fatalf("pulled_at: %+v %v %v", r, ok, err)
	}
	ls, _ := d.ListPlaylists()
	if len(ls) != 1 || ls[0].PulledAt == "" {
		t.Fatalf("list pulled_at: %+v", ls)
	}
}

// re-import must keep imported playlist ids stable (sync ledger keys on them) and clean up
// sync state of playlists that vanished from the source.
func TestSyncImportedPlaylistsStableIDs(t *testing.T) {
	d := openTestPlaylistDB(t)
	src, _ := d.UpsertSource(musiclib.Source{App: "traktor", Path: "c.nml"}, 1)
	pls := []musiclib.Playlist{
		{Name: "Festival", Folder: "Sets", Paths: []string{"a.mp3"}},
		{Name: "Warmup", Paths: []string{"b.mp3"}},
	}
	if err := d.SyncImportedPlaylists(src.ID, pls); err != nil {
		t.Fatal(err)
	}
	idOf := func(name string) int64 {
		t.Helper()
		ls, _ := d.ListPlaylists()
		for _, r := range ls {
			if r.Name == name {
				return r.ID
			}
		}
		t.Fatalf("%s not found", name)
		return 0
	}
	festID, warmID := idOf("Festival"), idOf("Warmup")
	if err := d.SavePlaylistSync(PlaylistSyncRow{LocalPlaylistID: festID, RemoteID: "pl_f"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SavePlaylistSync(PlaylistSyncRow{LocalPlaylistID: warmID, RemoteID: "pl_w"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPlaylistPulled(festID); err != nil {
		t.Fatal(err)
	}

	// re-import: Festival content changes, Warmup vanishes
	pls2 := []musiclib.Playlist{{Name: "Festival", Folder: "Sets", Paths: []string{"a.mp3", "c.mp3"}}}
	if err := d.SyncImportedPlaylists(src.ID, pls2); err != nil {
		t.Fatal(err)
	}
	if got := idOf("Festival"); got != festID {
		t.Fatalf("Festival id changed: %d → %d", festID, got)
	}
	if paths, _ := d.PlaylistTracks(festID); len(paths) != 2 {
		t.Fatalf("refresh content: %v", paths)
	}
	if r, _, _ := d.PlaylistByID(festID); r.PulledAt != "" {
		t.Fatal("re-import should clear pulled_at")
	}
	if _, ok, _ := d.GetPlaylistSync(festID); !ok {
		t.Fatal("Festival mapping lost on re-import")
	}
	if _, ok, _ := d.GetPlaylistSync(warmID); ok {
		t.Fatal("vanished playlist's mapping not cleaned")
	}
	if undos, _ := d.PlaylistUndos(warmID); len(undos) != 0 {
		t.Fatalf("vanished playlist's undo rows not cleaned: %+v", undos)
	}
	ls, _ := d.ListPlaylists()
	if len(ls) != 1 {
		t.Fatalf("vanished playlist not deleted: %+v", ls)
	}
}
