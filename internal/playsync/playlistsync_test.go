package playsync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

// fakePlaylists is the in-memory remote playlist store behind fakeAPI.
type fakePlaylists struct {
	seq         int
	lists       map[string]*api.PlaylistOut
	order       []string
	putBodies   [][]api.PlaylistItemIn // every PUT payload (leak pin)
	canonByName map[string]string      // title → canonical id the "server" links on PUT
}

func (f *fakeAPI) playlists() *fakePlaylists {
	if f.pls == nil {
		f.pls = &fakePlaylists{lists: map[string]*api.PlaylistOut{}}
	}
	return f.pls
}

func (f *fakeAPI) ListPlaylists(_ context.Context, _ string) ([]api.PlaylistOut, error) {
	p := f.playlists()
	out := make([]api.PlaylistOut, 0, len(p.order))
	for _, id := range p.order {
		if pl, ok := p.lists[id]; ok {
			cp := *pl
			cp.Items = nil
			out = append(out, cp)
		}
	}
	return out, nil
}

func (f *fakeAPI) GetPlaylist(_ context.Context, _ string, id string, includeItems bool) (api.PlaylistOut, error) {
	pl, ok := f.playlists().lists[id]
	if !ok {
		return api.PlaylistOut{}, fmt.Errorf("/playlists/%s -> 404: not found", id)
	}
	cp := *pl
	if !includeItems {
		cp.Items = nil
	}
	return cp, nil
}

func (f *fakeAPI) CreatePlaylist(_ context.Context, _ string, title, description, visibility string) (api.PlaylistOut, error) {
	p := f.playlists()
	p.seq++
	if visibility == "" {
		visibility = "private"
	}
	pl := &api.PlaylistOut{ID: fmt.Sprintf("pl_%d", p.seq), Title: title, Description: description,
		Visibility: visibility, Access: "owner"}
	p.lists[pl.ID] = pl
	p.order = append(p.order, pl.ID)
	return *pl, nil
}

func (f *fakeAPI) UpdatePlaylist(_ context.Context, _ string, id, title, description, visibility string) (api.PlaylistOut, error) {
	pl, ok := f.playlists().lists[id]
	if !ok {
		return api.PlaylistOut{}, fmt.Errorf("/playlists/%s -> 404: not found", id)
	}
	if title != "" {
		pl.Title = title
	}
	if description != "" {
		pl.Description = description
	}
	if visibility != "" {
		pl.Visibility = visibility
	}
	return *pl, nil
}

func (f *fakeAPI) DeletePlaylist(_ context.Context, _ string, id string) error {
	delete(f.playlists().lists, id)
	return nil
}

func (f *fakeAPI) PutPlaylistItems(_ context.Context, _ string, id string, items []api.PlaylistItemIn) (api.PlaylistOut, error) {
	p := f.playlists()
	pl, ok := p.lists[id]
	if !ok {
		return api.PlaylistOut{}, fmt.Errorf("/playlists/%s/items -> 404: not found", id)
	}
	p.putBodies = append(p.putBodies, items)
	pl.Items = pl.Items[:0]
	for i, in := range items {
		out := api.PlaylistItemOut{
			ID: fmt.Sprintf("pli_%d_%d", p.seq, i), Position: i,
			Title: in.Title, ArtistText: in.ArtistText,
			CanonicalTrackID: in.CanonicalTrackID, LibraryTrackID: in.LibraryTrackID,
		}
		if out.CanonicalTrackID == "" { // server-side link enrichment
			out.CanonicalTrackID = p.canonByName[in.Title]
		}
		pl.Items = append(pl.Items, out)
	}
	return *pl, nil
}

// setRemoteItems mutates the fake remote directly (an "edit from the web app").
func (f *fakeAPI) setRemoteItems(id string, items ...api.PlaylistItemOut) {
	pl := f.playlists().lists[id]
	pl.Items = items
	for i := range pl.Items {
		pl.Items[i].Position = i
	}
}

// ── fixtures ──────────────────────────────────────────────────────────────────

func ref(artist, title string) PlaylistItemRef { return PlaylistItemRef{Title: title, Artist: artist} }

// seedLocalPlaylist creates a manual playlist over already-seeded library paths.
func seedLocalPlaylist(t *testing.T, d *libdb.DB, name string, paths ...string) int64 {
	t.Helper()
	id, err := d.CreatePlaylist(name, libdb.PlaylistManual, "")
	if err != nil {
		t.Fatalf("create playlist: %v", err)
	}
	if len(paths) > 0 {
		if _, err := d.AddToPlaylist(id, paths...); err != nil {
			t.Fatalf("fill playlist: %v", err)
		}
	}
	return id
}

// ── hash + diff units ─────────────────────────────────────────────────────────

func TestPlaylistHashStability(t *testing.T) {
	items := []PlaylistItemRef{ref("A1", "T1"), ref("A2", "T2")}
	// Two independently-built but equal inputs must hash the same (stability), not the same
	// slice twice (which the compiler/linter would see as a trivially-true comparison).
	equal := []PlaylistItemRef{ref("A1", "T1"), ref("A2", "T2")}
	if playlistHash("P", items) != playlistHash("P", equal) {
		t.Fatal("equal content hashes differently")
	}
	reordered := []PlaylistItemRef{items[1], items[0]}
	if playlistHash("P", items) == playlistHash("P", reordered) {
		t.Fatal("order change not detected")
	}
	if playlistHash("P", items) == playlistHash("Q", items) {
		t.Fatal("title change not detected")
	}
	// canonical-link enrichment must NOT change the hash (identity = artist/title)
	linked := []PlaylistItemRef{{Title: "T1", Artist: "A1", CanonicalID: "trk_x"}, items[1]}
	if playlistHash("P", items) != playlistHash("P", linked) {
		t.Fatal("link enrichment changed the hash")
	}
}

func TestDiffPlaylists(t *testing.T) {
	local := []PlaylistItemRef{
		{Title: "T1", Artist: "A1", CanonicalID: "trk_1"},
		ref("A2", "T2"),
		ref("A3", "T3"), // local-only
	}
	remote := []PlaylistItemRef{
		ref("A2", "T2"),
		{Title: "Renamed remotely", Artist: "Other", CanonicalID: "trk_1"}, // matches via canonical
		ref("A4", "T4"), // remote-only
	}
	d := diffPlaylists("Mine", local, "Theirs", remote)
	if !d.TitleChanged {
		t.Fatal("title change missed")
	}
	if len(d.AddedLocal) != 1 || d.AddedLocal[0].Title != "T3" {
		t.Fatalf("added_local: %+v", d.AddedLocal)
	}
	if len(d.AddedRemote) != 1 || d.AddedRemote[0].Title != "T4" {
		t.Fatalf("added_remote: %+v", d.AddedRemote)
	}
	if d.Moved != 2 { // T1 and T2 swapped relative order
		t.Fatalf("moved = %d, want 2", d.Moved)
	}
	same := diffPlaylists("P", local, "P", local)
	if len(same.AddedLocal) != 0 || len(same.AddedRemote) != 0 || same.Moved != 0 || same.TitleChanged {
		t.Fatalf("self-diff not empty: %+v", same)
	}
}

// ── status flow: push / pull / sync-all ───────────────────────────────────────

func TestPlaylistStatusAndSyncFlow(t *testing.T) {
	d := openDB(t)
	seedLibrary(t, d,
		musiclib.Track{Path: `C:\Music\t1.mp3`, Title: "T1", Artist: "A1"},
		musiclib.Track{Path: `C:\Music\t2.mp3`, Title: "T2", Artist: "A2"},
		musiclib.Track{Path: `C:\Music\t3.mp3`, Title: "T3", Artist: "A3"},
	)
	plID := seedLocalPlaylist(t, d, "Peak Time", `C:\Music\t1.mp3`, `C:\Music\t2.mp3`)
	f := &fakeAPI{}
	s := New(f, d, nil, tokenFn("tok"))
	ctx := context.Background()

	ov, err := s.PlaylistOverviewCtx(ctx)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if len(ov.Pairs) != 1 || ov.Pairs[0].Status != PlaylistLocalOnly {
		t.Fatalf("want local_only, got %+v", ov.Pairs)
	}

	if err := s.PushPlaylist(ctx, plID); err != nil {
		t.Fatalf("push: %v", err)
	}
	ov, _ = s.PlaylistOverviewCtx(ctx)
	if ov.Pairs[0].Status != PlaylistInSync || ov.Pairs[0].RemoteID == "" {
		t.Fatalf("after push: %+v", ov.Pairs[0])
	}
	remoteID := ov.Pairs[0].RemoteID
	if got := f.playlists().lists[remoteID]; got.Title != "Peak Time" || len(got.Items) != 2 || got.Visibility != "private" {
		t.Fatalf("remote state: %+v", got)
	}

	// local edit → local_ahead; sync-all pushes it
	if _, err := d.AddToPlaylist(plID, `C:\Music\t3.mp3`); err != nil {
		t.Fatal(err)
	}
	ov, _ = s.PlaylistOverviewCtx(ctx)
	if ov.Pairs[0].Status != PlaylistLocalAhead {
		t.Fatalf("want local_ahead, got %s", ov.Pairs[0].Status)
	}
	res, err := s.SyncAllPlaylists(ctx)
	if err != nil || res.Pushed != 1 || res.Failed != 0 {
		t.Fatalf("sync-all push: %+v %v", res, err)
	}

	// remote edit → remote_ahead; sync-all pulls (incl. an unresolvable remote item)
	f.setRemoteItems(remoteID,
		api.PlaylistItemOut{Title: "T2", ArtistText: "A2"},
		api.PlaylistItemOut{Title: "Web Only", ArtistText: "Cloud"},
	)
	ov, _ = s.PlaylistOverviewCtx(ctx)
	if ov.Pairs[0].Status != PlaylistRemoteAhead {
		t.Fatalf("want remote_ahead, got %s", ov.Pairs[0].Status)
	}
	res, err = s.SyncAllPlaylists(ctx)
	if err != nil || res.Pulled != 1 {
		t.Fatalf("sync-all pull: %+v %v", res, err)
	}
	rows, _ := d.PlaylistItems(plID)
	if len(rows) != 2 || rows[0].Path != `C:\Music\t2.mp3` || !rows[1].Unresolved() || rows[1].Title != "Web Only" {
		t.Fatalf("pulled rows: %+v", rows)
	}
	pl, _, _ := d.PlaylistByID(plID)
	if pl.PulledAt == "" {
		t.Fatal("pulled_at not stamped")
	}

	// both sides edited → diverged; sync-all skips it
	if _, err := d.AddToPlaylist(plID, `C:\Music\t1.mp3`); err != nil {
		t.Fatal(err)
	}
	f.setRemoteItems(remoteID, api.PlaylistItemOut{Title: "T3", ArtistText: "A3"})
	res, err = s.SyncAllPlaylists(ctx)
	if err != nil || res.Diverged != 1 || res.Pushed != 0 || res.Pulled != 0 {
		t.Fatalf("diverged skip: %+v %v", res, err)
	}
}

func TestPlaylistPushCarriesLinks(t *testing.T) {
	d := openDB(t)
	seedLibrary(t, d, musiclib.Track{Path: `C:\Music\t1.mp3`, Title: "T1", Artist: "A1"})
	h := libdb.TrackHash("A1", "T1", 0)
	if err := d.SaveTrackLink(libdb.TrackLink{TrackHash: h, TrackID: "trk_canon"}); err != nil {
		t.Fatal(err)
	}
	if err := d.SaveLibraryTrackIDs(map[string]string{h: "lib_row"}); err != nil {
		t.Fatal(err)
	}
	plID := seedLocalPlaylist(t, d, "Linked", `C:\Music\t1.mp3`)
	f := &fakeAPI{}
	s := New(f, d, nil, tokenFn("tok"))
	if err := s.PushPlaylist(context.Background(), plID); err != nil {
		t.Fatalf("push: %v", err)
	}
	puts := f.playlists().putBodies
	if len(puts) != 1 || len(puts[0]) != 1 {
		t.Fatalf("puts: %+v", puts)
	}
	it := puts[0][0]
	if it.CanonicalTrackID != "trk_canon" || it.LibraryTrackID != "lib_row" || it.Title != "T1" || it.ArtistText != "A1" {
		t.Fatalf("wire item: %+v", it)
	}
}

// ── undo round-trips ──────────────────────────────────────────────────────────

func TestPlaylistUndoPullRestoresLocal(t *testing.T) {
	d := openDB(t)
	seedLibrary(t, d,
		musiclib.Track{Path: `C:\Music\t1.mp3`, Title: "T1", Artist: "A1"},
		musiclib.Track{Path: `C:\Music\t2.mp3`, Title: "T2", Artist: "A2"},
	)
	plID := seedLocalPlaylist(t, d, "Mine", `C:\Music\t1.mp3`, `C:\Music\t2.mp3`)
	f := &fakeAPI{}
	s := New(f, d, nil, tokenFn("tok"))
	ctx := context.Background()
	if err := s.PushPlaylist(ctx, plID); err != nil {
		t.Fatal(err)
	}
	led, _, _ := d.GetPlaylistSync(plID)

	f.setRemoteItems(led.RemoteID, api.PlaylistItemOut{Title: "T2", ArtistText: "A2"})
	if err := s.PullPlaylist(ctx, plID); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if rows, _ := d.PlaylistItems(plID); len(rows) != 1 {
		t.Fatalf("pull applied: %+v", rows)
	}

	undos, err := s.PlaylistUndos(plID)
	if err != nil || len(undos) == 0 || undos[0].Direction != "pull" || undos[0].Items != 2 {
		t.Fatalf("undos: %+v %v", undos, err)
	}
	if err := s.RestorePlaylistUndo(ctx, undos[0].ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	rows, _ := d.PlaylistItems(plID)
	if len(rows) != 2 || rows[0].Path != `C:\Music\t1.mp3` || rows[1].Path != `C:\Music\t2.mp3` {
		t.Fatalf("local not restored: %+v", rows)
	}
	if left, _ := s.PlaylistUndos(plID); len(left) != len(undos)-1 {
		t.Fatalf("undo not consumed: %+v", left)
	}
}

func TestPlaylistUndoPushRestoresRemote(t *testing.T) {
	d := openDB(t)
	seedLibrary(t, d, musiclib.Track{Path: `C:\Music\t1.mp3`, Title: "T1", Artist: "A1"})
	plID := seedLocalPlaylist(t, d, "Mine", `C:\Music\t1.mp3`)
	f := &fakeAPI{}
	s := New(f, d, nil, tokenFn("tok"))
	ctx := context.Background()
	if err := s.PushPlaylist(ctx, plID); err != nil { // create remote (no undo: nothing overwritten)
		t.Fatal(err)
	}
	led, _, _ := d.GetPlaylistSync(plID)
	f.setRemoteItems(led.RemoteID,
		api.PlaylistItemOut{Title: "Web A", ArtistText: "WA"},
		api.PlaylistItemOut{Title: "Web B", ArtistText: "WB"},
	)
	if err := s.PushPlaylist(ctx, plID); err != nil { // overwrites the web edits → snapshot
		t.Fatal(err)
	}
	if got := f.playlists().lists[led.RemoteID].Items; len(got) != 1 || got[0].Title != "T1" {
		t.Fatalf("push applied: %+v", got)
	}
	undos, _ := s.PlaylistUndos(plID)
	if len(undos) != 1 || undos[0].Direction != "push" || undos[0].Items != 2 {
		t.Fatalf("undos: %+v", undos)
	}
	if err := s.RestorePlaylistUndo(ctx, undos[0].ID); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got := f.playlists().lists[led.RemoteID].Items
	if len(got) != 2 || got[0].Title != "Web A" || got[1].Title != "Web B" {
		t.Fatalf("remote not restored: %+v", got)
	}
}

// ── import / unlink ───────────────────────────────────────────────────────────

func TestImportRemotePlaylistAndUnlink(t *testing.T) {
	d := openDB(t)
	seedLibrary(t, d, musiclib.Track{Path: `C:\Music\t1.mp3`, Title: "T1", Artist: "A1"})
	f := &fakeAPI{}
	s := New(f, d, nil, tokenFn("tok"))
	ctx := context.Background()
	rp, _ := f.CreatePlaylist(ctx, "tok", "From Web", "", "")
	f.setRemoteItems(rp.ID,
		api.PlaylistItemOut{Title: "T1", ArtistText: "A1"},
		api.PlaylistItemOut{Title: "Cloud Only", ArtistText: "X"},
	)

	ov, _ := s.PlaylistOverviewCtx(ctx)
	if len(ov.RemoteOnly) != 1 || ov.RemoteOnly[0].RemoteID != rp.ID {
		t.Fatalf("remote_only: %+v", ov.RemoteOnly)
	}
	localID, err := s.ImportRemotePlaylist(ctx, rp.ID)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	rows, _ := d.PlaylistItems(localID)
	if len(rows) != 2 || rows[0].Path != `C:\Music\t1.mp3` || !rows[1].Unresolved() {
		t.Fatalf("imported rows: %+v", rows)
	}
	ov, _ = s.PlaylistOverviewCtx(ctx)
	if len(ov.RemoteOnly) != 0 || len(ov.Pairs) != 1 || ov.Pairs[0].Status != PlaylistInSync {
		t.Fatalf("after import: %+v", ov)
	}

	if err := s.UnlinkPlaylist(localID); err != nil {
		t.Fatal(err)
	}
	ov, _ = s.PlaylistOverviewCtx(ctx)
	if ov.Pairs[0].Status != PlaylistLocalOnly || len(ov.RemoteOnly) != 1 {
		t.Fatalf("after unlink: %+v", ov)
	}
}

// ── privacy: no file paths on the wire ────────────────────────────────────────

func TestPlaylistWirePayloadsLeakNoPaths(t *testing.T) {
	d := openDB(t)
	seedLibrary(t, d,
		musiclib.Track{Path: `C:\Users\dj\Music\secret-folder\t1.mp3`, Title: "T1", Artist: "A1"},
		musiclib.Track{Path: `C:\Users\dj\Music\secret-folder\t2.mp3`, Title: "T2", Artist: "A2"},
	)
	plID := seedLocalPlaylist(t, d, "Leaky?", `C:\Users\dj\Music\secret-folder\t1.mp3`, `C:\Users\dj\Music\secret-folder\t2.mp3`)
	f := &fakeAPI{}
	s := New(f, d, nil, tokenFn("tok"))
	ctx := context.Background()
	if err := s.PushPlaylist(ctx, plID); err != nil {
		t.Fatal(err)
	}
	// pull then undo-push exercise the other wire-writing paths
	led, _, _ := d.GetPlaylistSync(plID)
	f.setRemoteItems(led.RemoteID, api.PlaylistItemOut{Title: "T1", ArtistText: "A1"})
	if err := s.PullPlaylist(ctx, plID); err != nil {
		t.Fatal(err)
	}
	if err := s.PushPlaylist(ctx, plID); err != nil {
		t.Fatal(err)
	}
	undos, _ := s.PlaylistUndos(plID)
	for _, u := range undos {
		if u.Direction == "push" {
			if err := s.RestorePlaylistUndo(ctx, u.ID); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	for _, body := range f.playlists().putBodies {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range []string{"secret-folder", ".mp3", `C:\\`, "path", remotePathPrefix} {
			if strings.Contains(string(b), needle) {
				t.Fatalf("wire payload leaks %q: %s", needle, b)
			}
		}
	}
	if len(f.playlists().putBodies) < 3 {
		t.Fatalf("expected ≥3 PUT payloads, got %d", len(f.playlists().putBodies))
	}
}
