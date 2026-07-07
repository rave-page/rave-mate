package libdb

import "fmt"

// Collection maintenance: consistent on-disk snapshot + cleanup of tracks whose files are gone
// (and the playlist references to them). All LOCAL - operates only on the rave-mate library DB,
// never the source DJ-software collection.

// CleanupResult reports what a missing-file cleanup removed.
type CleanupResult struct {
	TracksDeleted          int `json:"tracksDeleted"`          // track rows removed (across all sources)
	PlaylistEntriesDeleted int `json:"playlistEntriesDeleted"` // playlist_tracks rows pointing at a removed path
	EmptyPlaylistsDeleted  int `json:"emptyPlaylistsDeleted"`  // manual/imported playlists left empty by the cleanup
}

// BackupTo writes a consistent snapshot of the whole library DB to dest (VACUUM INTO - a single
// transactionally-clean file, safe to copy while the DB is open). dest must not already exist.
func (d *DB) BackupTo(dest string) error {
	if d == nil || d.db == nil {
		return fmt.Errorf("libdb: nil database")
	}
	// VACUUM INTO can't bind the path as a parameter; dest is an internal config path, not user
	// input, but quote-escape defensively anyway.
	_, err := d.db.Exec(`VACUUM INTO '` + escapeSQLLiteral(dest) + `'`)
	return err
}

// DeleteTracksByPaths removes the given track paths from the library and every playlist that
// referenced them, then deletes any manual/imported playlist the cleanup left empty (smart
// playlists hold no track rows, so they're untouched). One transaction - all-or-nothing.
func (d *DB) DeleteTracksByPaths(paths []string) (CleanupResult, error) {
	var res CleanupResult
	if d == nil || d.db == nil || len(paths) == 0 {
		return res, nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return res, err
	}
	fail := func(err error) (CleanupResult, error) { _ = tx.Rollback(); return CleanupResult{}, err }

	if _, err := tx.Exec(`CREATE TEMP TABLE IF NOT EXISTS _cleanup_paths (path TEXT PRIMARY KEY)`); err != nil {
		return fail(err)
	}
	if _, err := tx.Exec(`DELETE FROM _cleanup_paths`); err != nil {
		return fail(err)
	}
	ins, err := tx.Prepare(`INSERT OR IGNORE INTO _cleanup_paths (path) VALUES (?)`)
	if err != nil {
		return fail(err)
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := ins.Exec(p); err != nil {
			_ = ins.Close()
			return fail(err)
		}
	}
	_ = ins.Close()

	// Playlists that reference a doomed path (need an empty-check after deletion).
	affected := map[int64]bool{}
	rows, err := tx.Query(`SELECT DISTINCT playlist_id FROM playlist_tracks
		WHERE path IN (SELECT path FROM _cleanup_paths)`)
	if err != nil {
		return fail(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return fail(err)
		}
		affected[id] = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fail(err)
	}
	_ = rows.Close()

	plRes, err := tx.Exec(`DELETE FROM playlist_tracks WHERE path IN (SELECT path FROM _cleanup_paths)`)
	if err != nil {
		return fail(err)
	}
	if n, _ := plRes.RowsAffected(); n > 0 {
		res.PlaylistEntriesDeleted = int(n)
	}

	trRes, err := tx.Exec(`DELETE FROM tracks WHERE path IN (SELECT path FROM _cleanup_paths)`)
	if err != nil {
		return fail(err)
	}
	if n, _ := trRes.RowsAffected(); n > 0 {
		res.TracksDeleted = int(n)
	}

	// Drop manual/imported playlists the cleanup emptied (+ their sync/undo state).
	for id := range affected {
		var remaining int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM playlist_tracks WHERE playlist_id=?`, id).Scan(&remaining); err != nil {
			return fail(err)
		}
		if remaining > 0 {
			continue
		}
		var kind string
		if err := tx.QueryRow(`SELECT kind FROM playlists WHERE id=?`, id).Scan(&kind); err != nil {
			return fail(err)
		}
		if kind == PlaylistSmart {
			continue
		}
		for _, q := range []string{
			`DELETE FROM playlists WHERE id=?`,
			`DELETE FROM playlist_sync WHERE local_playlist_id=?`,
			`DELETE FROM playlist_sync_undo WHERE local_playlist_id=?`,
		} {
			if _, err := tx.Exec(q, id); err != nil {
				return fail(err)
			}
		}
		res.EmptyPlaylistsDeleted++
	}

	if _, err := tx.Exec(`DROP TABLE IF EXISTS _cleanup_paths`); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	return res, nil
}

// escapeSQLLiteral doubles single quotes for a SQL string literal (VACUUM INTO path).
func escapeSQLLiteral(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'')
		}
		out = append(out, s[i])
	}
	return string(out)
}
