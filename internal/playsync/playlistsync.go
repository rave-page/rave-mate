package playsync

// Two-way playlist sync (local libdb playlists ↔ rave.page /playlists). Per-playlist ledger
// (libdb.playlist_sync) records each side's content hash at the last sync point; status is
// computed by comparing each side's CURRENT hash against its OWN ledger entry - never across
// sides (the server enriches items, e.g. links canonical ids, so cross-side equality lies).
//
// Pull writes only rave-mate's libdb working copy - Traktor's NML is never modified (write-back
// is out of scope; the UI says so). PRIVACY: local file paths never go on the wire -
// api.PlaylistItemIn has no path field by construction (leak-pinned test in playlistsync_test).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/libdb"
)

// PlaylistStatus is a sync pair's computed state.
type PlaylistStatus string

const (
	PlaylistInSync      PlaylistStatus = "in_sync"
	PlaylistLocalAhead  PlaylistStatus = "local_ahead"
	PlaylistRemoteAhead PlaylistStatus = "remote_ahead"
	PlaylistDiverged    PlaylistStatus = "diverged"
	PlaylistLocalOnly   PlaylistStatus = "local_only"
	PlaylistRemoteOnly  PlaylistStatus = "remote_only"
)

// remotePathPrefix marks unresolved pulled items' sentinel paths in playlist_tracks (no local
// file; title/artist stored alongside).
const remotePathPrefix = "remote://"

// PlaylistItemRef is one playlist item in engine space. Path is local resolution/storage only;
// wire payloads go through wireItems → api.PlaylistItemIn, which has no path field.
type PlaylistItemRef struct {
	Title       string `json:"title,omitempty"`
	Artist      string `json:"artist,omitempty"`
	CanonicalID string `json:"canonical_id,omitempty"`
	LibraryID   string `json:"library_id,omitempty"`
	Path        string `json:"path,omitempty"` // stays in local snapshots, never wire
}

// PlaylistPair is one local/remote playlist sync row (LocalID 0 = remote-only).
type PlaylistPair struct {
	LocalID     int64          `json:"local_id,omitempty"`
	LocalName   string         `json:"local_name,omitempty"`
	Kind        string         `json:"kind,omitempty"` // manual|imported
	Folder      string         `json:"folder,omitempty"`
	RemoteID    string         `json:"remote_id,omitempty"`
	RemoteTitle string         `json:"remote_title,omitempty"`
	LocalCount  int            `json:"local_count,omitempty"`
	RemoteCount int            `json:"remote_count,omitempty"`
	Status      PlaylistStatus `json:"status"`
	SyncedAt    time.Time      `json:"-"`
}

// PlaylistOverview is the full sync picture.
type PlaylistOverview struct {
	Pairs      []PlaylistPair // every syncable local playlist (manual + imported; smart excluded)
	RemoteOnly []PlaylistPair // owned remote playlists with no local mapping
}

// PlaylistDiff is the current local-vs-remote content delta of a mapped pair.
// AddedLocal = items only local has (push uploads them / a pull removes them);
// AddedRemote = items only remote has (pull downloads them / a push removes them).
type PlaylistDiff struct {
	AddedLocal   []PlaylistItemRef
	AddedRemote  []PlaylistItemRef
	Moved        int // shared items whose relative position differs
	TitleChanged bool
	LocalName    string
	RemoteTitle  string
}

// PlaylistSyncResult reports one SyncAllPlaylists run.
type PlaylistSyncResult struct {
	Total      int            `json:"total"`
	Pushed     int            `json:"pushed"`
	Pulled     int            `json:"pulled"`
	InSync     int            `json:"in_sync"`
	Diverged   int            `json:"diverged"`
	LocalOnly  int            `json:"local_only"`
	RemoteOnly int            `json:"remote_only"`
	Failed     int            `json:"failed"`
	Playlists  []PlaylistPair `json:"playlists,omitempty"`
}

// plSnapshot is the undo payload: the overwritten side's title + ordered items.
type plSnapshot struct {
	Title string            `json:"title"`
	Items []PlaylistItemRef `json:"items"`
}

// PlaylistUndo is one restorable snapshot (UI/ctl listing).
type PlaylistUndo struct {
	ID        int64
	LocalID   int64
	Direction string // push|pull
	Title     string
	Items     int
	CreatedAt time.Time
}

// ── identity + hashing ────────────────────────────────────────────────────────

// itemHashKey is the link-independent item identity used for content hashing: canonical-link
// enrichment between runs must not flip a playlist to "changed".
func itemHashKey(it PlaylistItemRef) string { return libdb.TrackHash(it.Artist, it.Title, 0) }

// playlistHash fingerprints title + ordered item identities.
func playlistHash(title string, items []PlaylistItemRef) string {
	h := sha256.New()
	h.Write([]byte(title))
	h.Write([]byte{0})
	for _, it := range items {
		h.Write([]byte(itemHashKey(it)))
		h.Write([]byte{0x1f})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ── local resolution maps ─────────────────────────────────────────────────────

// plResolver maps between local paths and backend identities (built once per operation).
type plResolver struct {
	byPath   map[string]plTrack // local path → track meta
	byHash   map[string]string  // TrackHash → path
	byCanon  map[string]string  // canonical trk_… → path
	byLib    map[string]string  // library lib_… → path
	links    map[string]string  // TrackHash → canonical trk_…
	libIDs   map[string]string  // TrackHash → library lib_…
	dividers map[string]bool    // set-builder divider paths: local-only, never pushed
}

type plTrack struct{ title, artist string }

func (s *Syncer) plResolver() (*plResolver, error) {
	tracks, err := s.lib.LoadAllTracks()
	if err != nil {
		return nil, err
	}
	links, err := s.lib.AllTrackLinks()
	if err != nil {
		return nil, err
	}
	libIDs, err := s.lib.LibraryTrackIDs()
	if err != nil {
		return nil, err
	}
	dividers, err := s.lib.DividerPaths()
	if err != nil {
		return nil, err
	}
	r := &plResolver{
		byPath: make(map[string]plTrack, len(tracks)), byHash: make(map[string]string, len(tracks)),
		byCanon: map[string]string{}, byLib: map[string]string{},
		links: make(map[string]string, len(links)), libIDs: libIDs, dividers: dividers,
	}
	for h, l := range links {
		r.links[h] = l.TrackID
	}
	for _, t := range tracks {
		r.byPath[t.Path] = plTrack{title: t.Title, artist: t.Artist}
		h := libdb.TrackHash(t.Artist, t.Title, 0)
		if _, ok := r.byHash[h]; !ok {
			r.byHash[h] = t.Path
		}
		if id := r.links[h]; id != "" {
			if _, ok := r.byCanon[id]; !ok {
				r.byCanon[id] = t.Path
			}
		}
		if id := libIDs[h]; id != "" {
			if _, ok := r.byLib[id]; !ok {
				r.byLib[id] = t.Path
			}
		}
	}
	return r, nil
}

// localPlaylistItems loads a local playlist's items as refs (links attached where known).
func (s *Syncer) localPlaylistItems(r *plResolver, localID int64) ([]PlaylistItemRef, error) {
	rows, err := s.lib.PlaylistItems(localID)
	if err != nil {
		return nil, err
	}
	out := make([]PlaylistItemRef, 0, len(rows))
	for _, row := range rows {
		if r.dividers[row.Path] { // divider markers are local playlist furniture - never pushed
			continue
		}
		ref := PlaylistItemRef{Path: row.Path}
		switch {
		case row.Unresolved():
			ref.Title, ref.Artist = row.Title, row.Artist
		default:
			if t, ok := r.byPath[row.Path]; ok && t.title != "" {
				ref.Title, ref.Artist = t.title, t.artist
			} else { // file not in collection - degrade to the bare file name (no dir leaks)
				base := filepath.Base(row.Path)
				ref.Title = strings.TrimSuffix(base, filepath.Ext(base))
			}
		}
		h := itemHashKey(ref)
		ref.CanonicalID, ref.LibraryID = r.links[h], r.libIDs[h]
		out = append(out, ref)
	}
	return out, nil
}

// remoteRefs maps wire items to engine refs.
func remoteRefs(items []api.PlaylistItemOut) []PlaylistItemRef {
	out := make([]PlaylistItemRef, 0, len(items))
	for _, it := range items {
		out = append(out, PlaylistItemRef{
			Title: it.Title, Artist: it.ArtistText,
			CanonicalID: it.CanonicalTrackID, LibraryID: it.LibraryTrackID,
		})
	}
	return out
}

// wireItems builds the replace-all payload. api.PlaylistItemIn has no path field - the
// compiler enforces the privacy invariant; the leak test pins the JSON.
func wireItems(items []PlaylistItemRef) []api.PlaylistItemIn {
	out := make([]api.PlaylistItemIn, 0, len(items))
	for _, it := range items {
		title := it.Title
		if title == "" && it.CanonicalID == "" && it.LibraryID == "" {
			title = "Unknown track" // backend requires title for free-text items
		}
		out = append(out, api.PlaylistItemIn{
			Title: title, ArtistText: it.Artist,
			CanonicalTrackID: it.CanonicalID, LibraryTrackID: it.LibraryID,
		})
	}
	return out
}

// resolveRemoteItem maps one remote item to a local row: canonical link → library link →
// artist/title hash → unresolved snapshot (sentinel path, displayable, no local file).
func resolveRemoteItem(r *plResolver, it api.PlaylistItemOut) libdb.PlaylistItemRow {
	if p, ok := r.byCanon[it.CanonicalTrackID]; ok && it.CanonicalTrackID != "" {
		return libdb.PlaylistItemRow{Path: p}
	}
	if p, ok := r.byLib[it.LibraryTrackID]; ok && it.LibraryTrackID != "" {
		return libdb.PlaylistItemRow{Path: p}
	}
	h := libdb.TrackHash(it.ArtistText, it.Title, 0)
	if p, ok := r.byHash[h]; ok {
		return libdb.PlaylistItemRow{Path: p}
	}
	title := it.Title
	if title == "" {
		title = "Unknown track"
	}
	return libdb.PlaylistItemRow{Path: remotePathPrefix + h, Title: title, Artist: it.ArtistText}
}

// ── status + diff ─────────────────────────────────────────────────────────────

// computeStatus compares each side's current hash to its own ledger entry.
func computeStatus(led libdb.PlaylistSyncRow, localHash, remoteHash string) PlaylistStatus {
	lChanged, rChanged := localHash != led.LocalHash, remoteHash != led.RemoteHash
	switch {
	case lChanged && rChanged:
		return PlaylistDiverged
	case lChanged:
		return PlaylistLocalAhead
	case rChanged:
		return PlaylistRemoteAhead
	default:
		return PlaylistInSync
	}
}

// diffPlaylists matches items canonical-id-first, then artist/title hash, and reports each
// side's unmatched items + how many shared items sit at a different relative position.
func diffPlaylists(localName string, local []PlaylistItemRef, remoteTitle string, remote []PlaylistItemRef) PlaylistDiff {
	d := PlaylistDiff{LocalName: localName, RemoteTitle: remoteTitle, TitleChanged: localName != remoteTitle}

	unmatchedL := make([]int, 0, len(local)) // local indices still unmatched
	for i := range local {
		unmatchedL = append(unmatchedL, i)
	}
	type match struct{ li, ri int }
	var matches []match
	take := func(ri int, key func(PlaylistItemRef) string, want string) bool {
		if want == "" {
			return false
		}
		for n, li := range unmatchedL {
			if key(local[li]) == want {
				matches = append(matches, match{li, ri})
				unmatchedL = append(unmatchedL[:n], unmatchedL[n+1:]...)
				return true
			}
		}
		return false
	}
	canon := func(it PlaylistItemRef) string { return it.CanonicalID }
	matchedR := make([]bool, len(remote))
	for ri, rit := range remote { // pass 1: canonical id
		if take(ri, canon, rit.CanonicalID) {
			matchedR[ri] = true
		}
	}
	for ri, rit := range remote { // pass 2: artist/title hash
		if !matchedR[ri] && take(ri, itemHashKey, itemHashKey(rit)) {
			matchedR[ri] = true
		}
	}
	for _, li := range unmatchedL {
		d.AddedLocal = append(d.AddedLocal, local[li])
	}
	for ri, ok := range matchedR {
		if !ok {
			d.AddedRemote = append(d.AddedRemote, remote[ri])
		}
	}
	// moved: compact both sides to matched-only rank order; count rank mismatches
	localRank := map[int]int{}
	ranks := make([]int, 0, len(matches))
	for _, m := range matches {
		ranks = append(ranks, m.li)
	}
	sortInts(ranks)
	for rank, li := range ranks {
		localRank[li] = rank
	}
	// matches were appended in remote traversal order across two passes; rebuild in remote order
	byRemote := make([]int, len(remote))
	for i := range byRemote {
		byRemote[i] = -1
	}
	for _, m := range matches {
		byRemote[m.ri] = m.li
	}
	rank := 0
	for _, li := range byRemote {
		if li < 0 {
			continue
		}
		if localRank[li] != rank {
			d.Moved++
		}
		rank++
	}
	return d
}

func sortInts(s []int) {
	for i := 1; i < len(s); i++ { // tiny n - insertion sort, no extra import
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ── overview ──────────────────────────────────────────────────────────────────

// PlaylistOverviewCtx computes every pair's status. Needs auth (ErrUnauthenticated otherwise).
func (s *Syncer) PlaylistOverviewCtx(ctx context.Context) (PlaylistOverview, error) {
	if s == nil || s.lib == nil {
		return PlaylistOverview{}, fmt.Errorf("playsync: no library")
	}
	token, err := s.tok()
	if err != nil {
		return PlaylistOverview{}, err
	}
	r, err := s.plResolver()
	if err != nil {
		return PlaylistOverview{}, err
	}
	locals, err := s.lib.ListPlaylists()
	if err != nil {
		return PlaylistOverview{}, err
	}
	ledger, err := s.lib.PlaylistSyncRows()
	if err != nil {
		return PlaylistOverview{}, err
	}
	remotes, err := s.api.ListPlaylists(ctx, token)
	if err != nil {
		return PlaylistOverview{}, fmt.Errorf("list remote playlists: %w", err)
	}
	remoteByID := make(map[string]api.PlaylistOut, len(remotes))
	for _, rp := range remotes {
		remoteByID[rp.ID] = rp
	}

	var ov PlaylistOverview
	mapped := map[string]bool{}
	for _, pl := range locals {
		if pl.Kind == libdb.PlaylistSmart { // rule-based, evaluated live - not syncable content
			continue
		}
		items, err := s.localPlaylistItems(r, pl.ID)
		if err != nil {
			return PlaylistOverview{}, err
		}
		pair := PlaylistPair{
			LocalID: pl.ID, LocalName: pl.Name, Kind: pl.Kind, Folder: pl.Folder,
			LocalCount: len(items), Status: PlaylistLocalOnly,
		}
		if led, ok := ledger[pl.ID]; ok {
			pair.RemoteID, pair.SyncedAt = led.RemoteID, led.SyncedAt
			mapped[led.RemoteID] = true
			if _, exists := remoteByID[led.RemoteID]; exists {
				rp, err := s.api.GetPlaylist(ctx, token, led.RemoteID, true)
				if err != nil {
					return PlaylistOverview{}, fmt.Errorf("fetch %s: %w", led.RemoteID, err)
				}
				rrefs := remoteRefs(rp.Items)
				pair.RemoteTitle, pair.RemoteCount = rp.Title, len(rrefs)
				pair.Status = computeStatus(led, playlistHash(pl.Name, items), playlistHash(rp.Title, rrefs))
			} // remote deleted → stays local_only; a push recreates it
		}
		ov.Pairs = append(ov.Pairs, pair)
	}
	for _, rp := range remotes {
		if mapped[rp.ID] || rp.Access != "owner" { // shared/public lists aren't sync targets
			continue
		}
		ov.RemoteOnly = append(ov.RemoteOnly, PlaylistPair{
			RemoteID: rp.ID, RemoteTitle: rp.Title, Status: PlaylistRemoteOnly,
		})
	}
	return ov, nil
}

// PlaylistDiffFor computes the live diff of a mapped pair.
func (s *Syncer) PlaylistDiffFor(ctx context.Context, localID int64) (PlaylistDiff, error) {
	token, err := s.tok()
	if err != nil {
		return PlaylistDiff{}, err
	}
	led, ok, err := s.lib.GetPlaylistSync(localID)
	if err != nil {
		return PlaylistDiff{}, err
	}
	if !ok {
		return PlaylistDiff{}, fmt.Errorf("playlist %d not linked", localID)
	}
	pl, ok, err := s.lib.PlaylistByID(localID)
	if err != nil || !ok {
		return PlaylistDiff{}, fmt.Errorf("playlist %d missing (%v)", localID, err)
	}
	r, err := s.plResolver()
	if err != nil {
		return PlaylistDiff{}, err
	}
	local, err := s.localPlaylistItems(r, localID)
	if err != nil {
		return PlaylistDiff{}, err
	}
	rp, err := s.api.GetPlaylist(ctx, token, led.RemoteID, true)
	if err != nil {
		return PlaylistDiff{}, fmt.Errorf("fetch %s: %w", led.RemoteID, err)
	}
	return diffPlaylists(pl.Name, local, rp.Title, remoteRefs(rp.Items)), nil
}

// ── push / pull / import / unlink ─────────────────────────────────────────────

// PushPlaylist replaces the mapped remote playlist with the local content (creating the remote
// when unmapped or deleted). The prior remote state is snapshotted for undo first.
func (s *Syncer) PushPlaylist(ctx context.Context, localID int64) error {
	token, err := s.tok()
	if err != nil {
		return err
	}
	pl, ok, err := s.lib.PlaylistByID(localID)
	if err != nil || !ok {
		return fmt.Errorf("playlist %d missing (%v)", localID, err)
	}
	if pl.Kind == libdb.PlaylistSmart {
		return fmt.Errorf("smart playlists don't sync")
	}
	r, err := s.plResolver()
	if err != nil {
		return err
	}
	items, err := s.localPlaylistItems(r, localID)
	if err != nil {
		return err
	}

	led, mapped, err := s.lib.GetPlaylistSync(localID)
	if err != nil {
		return err
	}
	remoteID := led.RemoteID
	if mapped {
		if rp, err := s.api.GetPlaylist(ctx, token, remoteID, true); err == nil {
			snap, _ := json.Marshal(plSnapshot{Title: rp.Title, Items: remoteRefs(rp.Items)})
			if _, err := s.lib.AddPlaylistUndo(localID, "push", string(snap)); err != nil {
				return err
			}
			if rp.Title != pl.Name {
				if _, err := s.api.UpdatePlaylist(ctx, token, remoteID, pl.Name, "", ""); err != nil {
					s.warn("push title update", err)
				}
			}
		} else {
			mapped = false // remote gone → recreate below
		}
	}
	if !mapped {
		rp, err := s.api.CreatePlaylist(ctx, token, pl.Name, "Synced from rave-mate", "private")
		if err != nil {
			return fmt.Errorf("create remote playlist: %w", err)
		}
		remoteID = rp.ID
	}
	resp, err := s.api.PutPlaylistItems(ctx, token, remoteID, wireItems(items))
	if err != nil {
		return fmt.Errorf("put items: %w", err)
	}
	rrefs := remoteRefs(resp.Items)
	rtitle := resp.Title
	if rtitle == "" {
		rtitle = pl.Name
	}
	if err := s.lib.SavePlaylistSync(libdb.PlaylistSyncRow{
		LocalPlaylistID: localID, RemoteID: remoteID,
		LocalHash: playlistHash(pl.Name, items), RemoteHash: playlistHash(rtitle, rrefs),
	}); err != nil {
		return err
	}
	s.info("playlist pushed", map[string]any{"playlist": pl.Name, "remote": remoteID, "items": len(items)})
	return nil
}

// PullPlaylist replaces the local playlist's items with the mapped remote content (libdb only -
// the DJ software's own files are never written). Prior local state is snapshotted for undo.
func (s *Syncer) PullPlaylist(ctx context.Context, localID int64) error {
	token, err := s.tok()
	if err != nil {
		return err
	}
	led, ok, err := s.lib.GetPlaylistSync(localID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("playlist %d not linked", localID)
	}
	pl, ok, err := s.lib.PlaylistByID(localID)
	if err != nil || !ok {
		return fmt.Errorf("playlist %d missing (%v)", localID, err)
	}
	r, err := s.plResolver()
	if err != nil {
		return err
	}
	rp, err := s.api.GetPlaylist(ctx, token, led.RemoteID, true)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", led.RemoteID, err)
	}

	prior, err := s.localPlaylistItems(r, localID)
	if err != nil {
		return err
	}
	snap, _ := json.Marshal(plSnapshot{Title: pl.Name, Items: prior})
	if _, err := s.lib.AddPlaylistUndo(localID, "pull", string(snap)); err != nil {
		return err
	}

	name, unresolved, err := s.applyRemoteToLocal(r, localID, pl, rp)
	if err != nil {
		return err
	}
	rrefs := remoteRefs(rp.Items)
	local, err := s.localPlaylistItems(r, localID)
	if err != nil {
		return err
	}
	if err := s.lib.SavePlaylistSync(libdb.PlaylistSyncRow{
		LocalPlaylistID: localID, RemoteID: led.RemoteID,
		LocalHash: playlistHash(name, local), RemoteHash: playlistHash(rp.Title, rrefs),
	}); err != nil {
		return err
	}
	s.info("playlist pulled", map[string]any{"playlist": name, "remote": led.RemoteID, "items": len(rrefs), "unresolved": unresolved})
	return nil
}

// applyRemoteToLocal writes remote items into the local playlist (+ pulled marker; manual
// playlists also adopt the remote title). Returns the (possibly renamed) name + unresolved count.
func (s *Syncer) applyRemoteToLocal(r *plResolver, localID int64, pl libdb.PlaylistRow, rp api.PlaylistOut) (string, int, error) {
	rows := make([]libdb.PlaylistItemRow, 0, len(rp.Items))
	unresolved := 0
	for _, it := range rp.Items {
		row := resolveRemoteItem(r, it)
		if row.Unresolved() {
			unresolved++
		}
		rows = append(rows, row)
	}
	if err := s.lib.ReplacePlaylistItems(localID, rows); err != nil {
		return pl.Name, 0, err
	}
	if err := s.lib.SetPlaylistPulled(localID); err != nil {
		return pl.Name, 0, err
	}
	name := pl.Name
	if pl.Kind == libdb.PlaylistManual && rp.Title != "" && rp.Title != pl.Name {
		if err := s.lib.RenamePlaylist(localID, rp.Title); err == nil {
			name = rp.Title
		}
	}
	return name, unresolved, nil
}

// ImportRemotePlaylist materializes an unmapped remote playlist as a new local manual playlist
// and links the pair. Returns the new local id.
func (s *Syncer) ImportRemotePlaylist(ctx context.Context, remoteID string) (int64, error) {
	token, err := s.tok()
	if err != nil {
		return 0, err
	}
	r, err := s.plResolver()
	if err != nil {
		return 0, err
	}
	rp, err := s.api.GetPlaylist(ctx, token, remoteID, true)
	if err != nil {
		return 0, fmt.Errorf("fetch %s: %w", remoteID, err)
	}
	title := rp.Title
	if title == "" {
		title = "Imported playlist"
	}
	localID, err := s.lib.CreatePlaylist(title, libdb.PlaylistManual, "")
	if err != nil {
		return 0, err
	}
	pl, _, err := s.lib.PlaylistByID(localID)
	if err != nil {
		return 0, err
	}
	name, unresolved, err := s.applyRemoteToLocal(r, localID, pl, rp)
	if err != nil {
		return 0, err
	}
	rrefs := remoteRefs(rp.Items)
	local, err := s.localPlaylistItems(r, localID)
	if err != nil {
		return 0, err
	}
	if err := s.lib.SavePlaylistSync(libdb.PlaylistSyncRow{
		LocalPlaylistID: localID, RemoteID: remoteID,
		LocalHash: playlistHash(name, local), RemoteHash: playlistHash(rp.Title, rrefs),
	}); err != nil {
		return 0, err
	}
	s.info("playlist imported", map[string]any{"playlist": name, "remote": remoteID, "items": len(rrefs), "unresolved": unresolved})
	return localID, nil
}

// DeleteRemotePlaylist removes a rave.page playlist (remote-only cleanup; local data untouched).
func (s *Syncer) DeleteRemotePlaylist(ctx context.Context, remoteID string) error {
	token, err := s.tok()
	if err != nil {
		return err
	}
	if err := s.api.DeletePlaylist(ctx, token, remoteID); err != nil {
		return err
	}
	s.info("remote playlist deleted", map[string]any{"remote": remoteID})
	return nil
}

// UnlinkPlaylist drops the pair mapping; both sides keep their content.
func (s *Syncer) UnlinkPlaylist(localID int64) error {
	if s == nil || s.lib == nil {
		return fmt.Errorf("playsync: no library")
	}
	return s.lib.DeletePlaylistSync(localID)
}

// ── undo ──────────────────────────────────────────────────────────────────────

// PlaylistUndos lists a playlist's restorable snapshots, newest first.
func (s *Syncer) PlaylistUndos(localID int64) ([]PlaylistUndo, error) {
	rows, err := s.lib.PlaylistUndos(localID)
	if err != nil {
		return nil, err
	}
	out := make([]PlaylistUndo, 0, len(rows))
	for _, row := range rows {
		var snap plSnapshot
		_ = json.Unmarshal([]byte(row.SnapshotJSON), &snap)
		out = append(out, PlaylistUndo{
			ID: row.ID, LocalID: row.LocalPlaylistID, Direction: row.Direction,
			Title: snap.Title, Items: len(snap.Items), CreatedAt: row.CreatedAt,
		})
	}
	return out, nil
}

// RestorePlaylistUndo reverses one apply: a pull snapshot restores the local item set; a push
// snapshot re-PUTs the prior remote state. The consumed snapshot row is deleted. The sync
// ledger is left untouched - the restored side then reads as ahead, which is accurate.
func (s *Syncer) RestorePlaylistUndo(ctx context.Context, undoID int64) error {
	if s == nil || s.lib == nil {
		return fmt.Errorf("playsync: no library")
	}
	row, ok, err := s.lib.GetPlaylistUndo(undoID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("undo %d missing", undoID)
	}
	var snap plSnapshot
	if err := json.Unmarshal([]byte(row.SnapshotJSON), &snap); err != nil {
		return fmt.Errorf("undo %d corrupt: %w", undoID, err)
	}
	switch row.Direction {
	case "pull": // restore the local side
		rows := make([]libdb.PlaylistItemRow, 0, len(snap.Items))
		for _, it := range snap.Items {
			if it.Path != "" {
				rows = append(rows, libdb.PlaylistItemRow{Path: it.Path})
				continue
			}
			rows = append(rows, libdb.PlaylistItemRow{
				Path: remotePathPrefix + itemHashKey(it), Title: it.Title, Artist: it.Artist,
			})
		}
		if err := s.lib.ReplacePlaylistItems(row.LocalPlaylistID, rows); err != nil {
			return err
		}
		if pl, ok, _ := s.lib.PlaylistByID(row.LocalPlaylistID); ok &&
			pl.Kind == libdb.PlaylistManual && snap.Title != "" && snap.Title != pl.Name {
			_ = s.lib.RenamePlaylist(row.LocalPlaylistID, snap.Title)
		}
	case "push": // restore the remote side
		token, err := s.tok()
		if err != nil {
			return err
		}
		led, ok, err := s.lib.GetPlaylistSync(row.LocalPlaylistID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("playlist %d no longer linked", row.LocalPlaylistID)
		}
		if _, err := s.api.PutPlaylistItems(ctx, token, led.RemoteID, wireItems(snap.Items)); err != nil {
			return fmt.Errorf("restore remote items: %w", err)
		}
		if snap.Title != "" {
			if _, err := s.api.UpdatePlaylist(ctx, token, led.RemoteID, snap.Title, "", ""); err != nil {
				s.warn("undo title restore", err)
			}
		}
	default:
		return fmt.Errorf("undo %d: unknown direction %q", undoID, row.Direction)
	}
	if err := s.lib.DeletePlaylistUndo(undoID); err != nil {
		return err
	}
	s.info("playlist undo restored", map[string]any{"undo": undoID, "direction": row.Direction, "items": len(snap.Items)})
	return nil
}

// ── sync all ──────────────────────────────────────────────────────────────────

// SyncAllPlaylists pushes every local_ahead pair and pulls every remote_ahead pair; diverged,
// local_only and remote_only are counted but untouched (explicit user action required).
func (s *Syncer) SyncAllPlaylists(ctx context.Context) (PlaylistSyncResult, error) {
	ov, err := s.PlaylistOverviewCtx(ctx)
	if err != nil {
		return PlaylistSyncResult{}, err
	}
	res := PlaylistSyncResult{Total: len(ov.Pairs), RemoteOnly: len(ov.RemoteOnly)}
	for i, p := range ov.Pairs {
		if ctx.Err() != nil {
			res.Failed += len(ov.Pairs) - i
			break
		}
		switch p.Status {
		case PlaylistInSync:
			res.InSync++
		case PlaylistDiverged:
			res.Diverged++
		case PlaylistLocalOnly:
			res.LocalOnly++
		case PlaylistLocalAhead:
			if err := s.PushPlaylist(ctx, p.LocalID); err != nil {
				res.Failed++
				s.warn("sync push "+p.LocalName, err)
			} else {
				res.Pushed++
				ov.Pairs[i].Status = PlaylistInSync
			}
		case PlaylistRemoteAhead:
			if err := s.PullPlaylist(ctx, p.LocalID); err != nil {
				res.Failed++
				s.warn("sync pull "+p.LocalName, err)
			} else {
				res.Pulled++
				ov.Pairs[i].Status = PlaylistInSync
			}
		}
	}
	res.Playlists = append(ov.Pairs, ov.RemoteOnly...)
	s.info("playlists synced", map[string]any{
		"total": res.Total, "pushed": res.Pushed, "pulled": res.Pulled, "inSync": res.InSync,
		"diverged": res.Diverged, "localOnly": res.LocalOnly, "remoteOnly": res.RemoteOnly, "failed": res.Failed,
	})
	return res, nil
}
