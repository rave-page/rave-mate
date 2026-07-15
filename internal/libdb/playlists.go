package libdb

import (
	"database/sql"
	"fmt"
	"time"

	"rave.page/mate/internal/musiclib"
)

// Playlist kinds. Manual = user-created in-app; imported = synced from a DJ-software
// source (replaced wholesale on re-import); smart = rule-based (rules JSON, evaluated
// live against the loaded collection - no playlist_tracks rows).
const (
	PlaylistManual   = "manual"
	PlaylistImported = "imported"
	PlaylistSmart    = "smart"
)

// PlaylistRow is one stored playlist (TrackCount filled by ListPlaylists).
type PlaylistRow struct {
	ID          int64
	Name        string
	Kind        string // manual|imported|smart
	Folder      string // imported: source folder path
	Rules       string // smart: JSON musiclib.SmartRules
	PulledAt    string // RFC3339 of last remote→local sync apply ("" = never)
	AutoRefresh bool   // folder-bound: pick up new folder files automatically
	TrackCount  int
}

const playlistSchema = `
CREATE TABLE IF NOT EXISTS playlists (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL DEFAULT 'manual',
  source_id  INTEGER,                 -- imported: owning source (cascade-orphaned manually)
  folder     TEXT NOT NULL DEFAULT '',
  rules      TEXT NOT NULL DEFAULT '',
  created_at TEXT, updated_at TEXT
);
CREATE TABLE IF NOT EXISTS playlist_tracks (
  playlist_id INTEGER NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
  position    INTEGER NOT NULL,
  path        TEXT NOT NULL,
  UNIQUE(playlist_id, path)
);
CREATE INDEX IF NOT EXISTS idx_playlist_tracks_pl   ON playlist_tracks(playlist_id, position);
CREATE INDEX IF NOT EXISTS idx_playlist_tracks_path ON playlist_tracks(path);
`

// CreatePlaylist inserts a playlist and returns its id. rules only meaningful for smart.
func (d *DB) CreatePlaylist(name, kind, rules string) (int64, error) {
	if name == "" {
		return 0, fmt.Errorf("playlist name empty")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := d.db.Exec(`INSERT INTO playlists (name, kind, rules, created_at, updated_at) VALUES (?,?,?,?,?)`,
		name, kind, rules, now, now)
	if err != nil {
		return 0, err
	}
	d.bumpPlaylists()
	return res.LastInsertId()
}

// CreateFolderPlaylist creates a manual playlist bound to a source folder (the
// "mark directory as playlist" gesture) and fills it with the given tracks.
func (d *DB) CreateFolderPlaylist(name, folder string, paths []string) (int64, error) {
	if name == "" || folder == "" {
		return 0, fmt.Errorf("playlist name/folder empty")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := d.db.Exec(`INSERT INTO playlists (name, kind, folder, created_at, updated_at) VALUES (?,?,?,?,?)`,
		name, PlaylistManual, folder, now, now)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := d.AddToPlaylist(id, paths...); err != nil {
		return id, err
	}
	return id, nil
}

// RenamePlaylist updates a playlist's name.
func (d *DB) RenamePlaylist(id int64, name string) error {
	if name == "" {
		return fmt.Errorf("playlist name empty")
	}
	return d.touchPlaylist(id, `name=?`, name)
}

// SetSmartRules stores a smart playlist's rules JSON.
func (d *DB) SetSmartRules(id int64, rules string) error {
	return d.touchPlaylist(id, `rules=?`, rules)
}

func (d *DB) touchPlaylist(id int64, setClause string, v any) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.db.Exec(`UPDATE playlists SET `+setClause+`, updated_at=? WHERE id=?`, v, now, id)
	if err == nil {
		d.bumpPlaylists()
	}
	return err
}

// DeletePlaylist removes a playlist (tracks cascade; sync mapping + undo history cleaned -
// the remote playlist itself is untouched).
func (d *DB) DeletePlaylist(id int64) error {
	for _, q := range []string{
		`DELETE FROM playlists WHERE id=?`,
		`DELETE FROM playlist_sync WHERE local_playlist_id=?`,
		`DELETE FROM playlist_sync_undo WHERE local_playlist_id=?`,
	} {
		if _, err := d.db.Exec(q, id); err != nil {
			return err
		}
	}
	d.bumpPlaylists()
	return nil
}

// ListPlaylists returns all playlists with track counts: manual first, then smart, then
// imported (grouped by folder), each name-sorted.
func (d *DB) ListPlaylists() ([]PlaylistRow, error) {
	rows, err := d.db.Query(`
		SELECT p.id, p.name, p.kind, p.folder, p.rules, COALESCE(p.pulled_at,''), COALESCE(p.auto_refresh,0),
		       (SELECT COUNT(*) FROM playlist_tracks t WHERE t.playlist_id = p.id)
		FROM playlists p
		ORDER BY CASE p.kind WHEN 'manual' THEN 0 WHEN 'smart' THEN 1 ELSE 2 END,
		         p.folder COLLATE NOCASE, p.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PlaylistRow
	for rows.Next() {
		var r PlaylistRow
		var ar int
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &r.Folder, &r.Rules, &r.PulledAt, &ar, &r.TrackCount); err != nil {
			return nil, err
		}
		r.AutoRefresh = ar != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetPlaylistAutoRefresh flags a folder-bound playlist for automatic folder pickup.
func (d *DB) SetPlaylistAutoRefresh(id int64, on bool) error {
	v := 0
	if on {
		v = 1
	}
	return d.touchPlaylist(id, `auto_refresh=?`, v)
}

// PlaylistByID returns one playlist, ok=false if absent.
func (d *DB) PlaylistByID(id int64) (PlaylistRow, bool, error) {
	var r PlaylistRow
	var ar int
	err := d.db.QueryRow(`SELECT id, name, kind, folder, rules, COALESCE(pulled_at,''), COALESCE(auto_refresh,0) FROM playlists WHERE id=?`, id).
		Scan(&r.ID, &r.Name, &r.Kind, &r.Folder, &r.Rules, &r.PulledAt, &ar)
	if err == sql.ErrNoRows {
		return PlaylistRow{}, false, nil
	}
	r.AutoRefresh = ar != 0
	return r, err == nil, err
}

// PlaylistTracks returns the playlist's track paths in order.
func (d *DB) PlaylistTracks(id int64) ([]string, error) {
	rows, err := d.db.Query(`SELECT path FROM playlist_tracks WHERE playlist_id=? ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AddToPlaylist appends paths (deduped vs existing) and returns how many were added.
func (d *DB) AddToPlaylist(id int64, paths ...string) (int, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return 0, err
	}
	var next int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(position),-1)+1 FROM playlist_tracks WHERE playlist_id=?`, id).Scan(&next); err != nil {
		_ = tx.Rollback()
		return 0, err
	}
	added := 0
	for _, p := range paths {
		if p == "" {
			continue
		}
		res, err := tx.Exec(`INSERT OR IGNORE INTO playlist_tracks (playlist_id, position, path) VALUES (?,?,?)`, id, next, p)
		if err != nil {
			_ = tx.Rollback()
			return 0, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
			next++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	d.bumpPlaylists()
	return added, nil
}

// RemoveFromPlaylist deletes one path from a playlist.
func (d *DB) RemoveFromPlaylist(id int64, path string) error {
	_, err := d.db.Exec(`DELETE FROM playlist_tracks WHERE playlist_id=? AND path=?`, id, path)
	if err == nil {
		d.bumpPlaylists()
	}
	return err
}

// ReplacePlaylistTracks rewrites the playlist's full ordered track list (the reorder path -
// playlists are small, a wholesale rewrite is simpler than position shuffling).
func (d *DB) ReplacePlaylistTracks(id int64, paths []string) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM playlist_tracks WHERE playlist_id=?`, id); err != nil {
		_ = tx.Rollback()
		return err
	}
	seen := map[string]bool{}
	pos := 0
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if _, err := tx.Exec(`INSERT INTO playlist_tracks (playlist_id, position, path) VALUES (?,?,?)`, id, pos, p); err != nil {
			_ = tx.Rollback()
			return err
		}
		pos++
	}
	return tx.Commit()
}

// PlaylistItemRow is one stored playlist item. Title/Artist are set only for unresolved
// remote-pulled items (no matching local file); resolved items carry just the path.
type PlaylistItemRow struct {
	Path   string
	Title  string
	Artist string
}

// Unresolved reports whether the item is a remote snapshot without a local file.
func (r PlaylistItemRow) Unresolved() bool { return r.Title != "" || r.Artist != "" }

// PlaylistItems returns the playlist's ordered items incl. unresolved remote snapshots.
func (d *DB) PlaylistItems(id int64) ([]PlaylistItemRow, error) {
	rows, err := d.db.Query(`SELECT path, COALESCE(title,''), COALESCE(artist,'')
		FROM playlist_tracks WHERE playlist_id=? ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PlaylistItemRow
	for rows.Next() {
		var r PlaylistItemRow
		if err := rows.Scan(&r.Path, &r.Title, &r.Artist); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReplacePlaylistItems rewrites the playlist's full ordered item set (the sync-pull path -
// like ReplacePlaylistTracks but keeps unresolved items' title/artist).
func (d *DB) ReplacePlaylistItems(id int64, items []PlaylistItemRow) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM playlist_tracks WHERE playlist_id=?`, id); err != nil {
		_ = tx.Rollback()
		return err
	}
	seen := map[string]bool{}
	pos := 0
	for _, it := range items {
		if it.Path == "" || seen[it.Path] {
			continue
		}
		seen[it.Path] = true
		if _, err := tx.Exec(`INSERT INTO playlist_tracks (playlist_id, position, path, title, artist) VALUES (?,?,?,?,?)`,
			id, pos, it.Path, nullIfEmpty(it.Title), nullIfEmpty(it.Artist)); err != nil {
			_ = tx.Rollback()
			return err
		}
		pos++
	}
	return tx.Commit()
}

// SetPlaylistPulled stamps the last remote→local apply (the "Traktor itself not updated" marker).
func (d *DB) SetPlaylistPulled(id int64) error {
	return d.touchPlaylist(id, `pulled_at=?`, time.Now().UTC().Format(time.RFC3339))
}

// PlaylistsForTrack returns every manual/imported playlist containing path (smart membership
// is computed live by the caller against the loaded collection).
func (d *DB) PlaylistsForTrack(path string) ([]PlaylistRow, error) {
	rows, err := d.db.Query(`
		SELECT p.id, p.name, p.kind, p.folder, p.rules
		FROM playlists p JOIN playlist_tracks t ON t.playlist_id = p.id
		WHERE t.path=? ORDER BY p.name COLLATE NOCASE`, path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PlaylistRow
	for rows.Next() {
		var r PlaylistRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Kind, &r.Folder, &r.Rules); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SyncImportedPlaylists refreshes a source's imported playlists from the freshly parsed set
// (re-import = source of truth for content). Manual + smart playlists are untouched. Existing
// playlists are matched by (folder, name) and UPDATED in place - ids stay stable across
// re-imports, so playlist_sync mappings + undo history survive. Unmatched rows are deleted;
// a content refresh clears the pulled_at marker (re-import overwrote the pulled state).
func (d *DB) SyncImportedPlaylists(sourceID int64, pls []musiclib.Playlist) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	fail := func(err error) error { _ = tx.Rollback(); return err }

	existing := map[[2]string]int64{} // {folder, name} → id
	rows, err := tx.Query(`SELECT id, folder, name FROM playlists WHERE kind='imported' AND source_id=?`, sourceID)
	if err != nil {
		return fail(err)
	}
	for rows.Next() {
		var id int64
		var folder, name string
		if err := rows.Scan(&id, &folder, &name); err != nil {
			_ = rows.Close()
			return fail(err)
		}
		existing[[2]string{folder, name}] = id
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fail(err)
	}
	_ = rows.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	kept := map[int64]bool{}
	for _, pl := range pls {
		if pl.Name == "" {
			continue
		}
		key := [2]string{pl.Folder, pl.Name}
		plID, ok := existing[key]
		if ok {
			if kept[plID] { // duplicate (folder,name) in source - first wins
				continue
			}
			if _, err := tx.Exec(`UPDATE playlists SET updated_at=?, pulled_at=NULL WHERE id=?`, now, plID); err != nil {
				return fail(err)
			}
			if _, err := tx.Exec(`DELETE FROM playlist_tracks WHERE playlist_id=?`, plID); err != nil {
				return fail(err)
			}
		} else {
			res, err := tx.Exec(`INSERT INTO playlists (name, kind, source_id, folder, created_at, updated_at) VALUES (?,?,?,?,?,?)`,
				pl.Name, PlaylistImported, sourceID, pl.Folder, now, now)
			if err != nil {
				return fail(err)
			}
			if plID, err = res.LastInsertId(); err != nil {
				return fail(err)
			}
		}
		kept[plID] = true
		pos := 0
		for _, p := range pl.Paths {
			if p == "" {
				continue
			}
			r, err := tx.Exec(`INSERT OR IGNORE INTO playlist_tracks (playlist_id, position, path) VALUES (?,?,?)`, plID, pos, p)
			if err != nil {
				return fail(err)
			}
			if n, _ := r.RowsAffected(); n > 0 {
				pos++
			}
		}
	}
	// delete imported playlists that vanished from the source (+ their sync state)
	for _, id := range existing {
		if kept[id] {
			continue
		}
		for _, q := range []string{
			`DELETE FROM playlist_tracks WHERE playlist_id=?`,
			`DELETE FROM playlists WHERE id=?`,
			`DELETE FROM playlist_sync WHERE local_playlist_id=?`,
			`DELETE FROM playlist_sync_undo WHERE local_playlist_id=?`,
		} {
			if _, err := tx.Exec(q, id); err != nil {
				return fail(err)
			}
		}
	}
	return tx.Commit()
}
