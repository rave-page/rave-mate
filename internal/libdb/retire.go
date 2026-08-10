package libdb

// Stale-source retirement. A DJ-software version upgrade moves the collection file
// (Traktor 4.2.0\collection.nml → Traktor 4.5.1\...), so the next import creates a NEW
// (app,path) source while the old one keeps its tracks + imported playlists - the whole
// library doubles in every view (live incident 2026-08-10: 13,019 dup paths, every
// imported playlist twice). Runs after every import and once at startup: the newest
// import of an app retires its older siblings, and folder-source rows shadowed by a real
// import drop (restores fiPersistLoose's "unknown to ANY source" invariant).

import "fmt"

// retireOverlapMin: an older same-app source is stale only when the keeper covers at
// least this share of its paths. A genuinely different library (another machine's
// collection) stays; the upgrade case overlaps ~100%.
const retireOverlapMin = 0.6

// RetireStaleAppSources retires superseded DJ-software sources and de-shadows folder
// sources. Per non-folder app with >1 source: newest imported_at is the keeper; an older
// source with ≥retireOverlapMin path overlap (or no tracks) is removed - its imported
// playlists (+ sync state) deleted, sessions re-homed to the keeper, tracks deleted
// explicitly (FK cascade is a per-connection pragma - not trusted). Then folder-source
// track rows whose path a non-folder source now owns are dropped, and empty folder
// sources vanish. Returns human-readable lines describing what changed.
func (d *DB) RetireStaleAppSources() ([]string, error) {
	if d == nil || d.db == nil {
		return nil, nil
	}
	type src struct {
		id        int64
		app, path string
	}
	rows, err := d.db.Query(`SELECT id, app, path FROM sources WHERE app != 'folder'
		ORDER BY app, COALESCE(imported_at,'') DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	byApp := map[string][]src{}
	var order []string
	for rows.Next() {
		var s src
		if err := rows.Scan(&s.id, &s.app, &s.path); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if len(byApp[s.app]) == 0 {
			order = append(order, s.app)
		}
		byApp[s.app] = append(byApp[s.app], s)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var lines []string
	changed := false
	for _, app := range order {
		group := byApp[app]
		keeper := group[0]
		for _, old := range group[1:] {
			var oldN, hit int
			if err := d.db.QueryRow(`SELECT COUNT(*),
				(SELECT COUNT(*) FROM tracks o WHERE o.source_id=?1 AND EXISTS
					(SELECT 1 FROM tracks n WHERE n.source_id=?2 AND n.path=o.path))
				FROM tracks WHERE source_id=?1`, old.id, keeper.id).Scan(&oldN, &hit); err != nil {
				return lines, err
			}
			if oldN > 0 && float64(hit)/float64(oldN) < retireOverlapMin {
				lines = append(lines, fmt.Sprintf("kept older %s source %s (only %d/%d paths in the current import - looks like a distinct library)", app, old.path, hit, oldN))
				continue
			}
			if err := d.retireSource(old.id, keeper.id); err != nil {
				return lines, fmt.Errorf("retire %s source %s: %w", app, old.path, err)
			}
			changed = true
			lines = append(lines, fmt.Sprintf("retired superseded %s source %s (%d tracks, %d/%d paths carried by %s)", app, old.path, oldN, hit, oldN, keeper.path))
		}
	}

	// De-shadow folder sources: a path a real import now owns needs no probe-created twin.
	res, err := d.db.Exec(`DELETE FROM tracks WHERE is_divider=0
		AND source_id IN (SELECT id FROM sources WHERE app='folder')
		AND EXISTS (SELECT 1 FROM tracks n JOIN sources s ON s.id=n.source_id
			WHERE s.app!='folder' AND n.path=tracks.path)`)
	if err != nil {
		return lines, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		changed = true
		lines = append(lines, fmt.Sprintf("dropped %d folder-source rows shadowed by a DJ-software import", n))
	}
	if _, err := d.db.Exec(`DELETE FROM sources WHERE app='folder'
		AND NOT EXISTS (SELECT 1 FROM tracks WHERE tracks.source_id=sources.id)`); err != nil {
		return lines, err
	}

	if changed {
		d.bumpTracks()
		d.bumpPlaylists()
	}
	return lines, nil
}

// retireSource removes one superseded source: imported playlists (+ sync state) go,
// sessions move to the keeper (name clashes drop - the keeper's copy wins), tracks and
// the source row are deleted explicitly.
func (d *DB) retireSource(oldID, keeperID int64) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	fail := func(err error) error { _ = tx.Rollback(); return err }
	plRows, err := tx.Query(`SELECT id FROM playlists WHERE kind=? AND source_id=?`, PlaylistImported, oldID)
	if err != nil {
		return fail(err)
	}
	var plIDs []int64
	for plRows.Next() {
		var id int64
		if err := plRows.Scan(&id); err != nil {
			_ = plRows.Close()
			return fail(err)
		}
		plIDs = append(plIDs, id)
	}
	_ = plRows.Close()
	if err := plRows.Err(); err != nil {
		return fail(err)
	}
	for _, id := range plIDs {
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
	if _, err := tx.Exec(`UPDATE OR IGNORE sessions SET source_id=? WHERE source_id=?`, keeperID, oldID); err != nil {
		return fail(err)
	}
	for _, q := range []string{
		`DELETE FROM sessions WHERE source_id=?`, // name-clash leftovers of the re-home above
		`DELETE FROM tracks WHERE source_id=?`,
		`DELETE FROM sources WHERE id=?`,
	} {
		if _, err := tx.Exec(q, oldID); err != nil {
			return fail(err)
		}
	}
	return tx.Commit()
}
