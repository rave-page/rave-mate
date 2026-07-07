package libdb

import (
	"database/sql"
	"errors"
	"time"
)

// Playlist two-way sync state (internal/playsync/playlistsync.go):
//
//   - playlist_sync: local playlist ↔ remote pl_… mapping with the content hashes recorded at
//     the last sync point. Status is computed by comparing CURRENT side hashes against these
//     (local≠ledger = local_ahead, remote≠ledger = remote_ahead, both = diverged) - the two
//     sides' hashes are never compared to each other (the server may enrich items, e.g. link
//     canonical ids, so cross-side equality is meaningless).
//   - playlist_sync_undo: pre-apply snapshot of whichever side an apply overwrites (push →
//     remote state, pull → local state), JSON, last 10 per playlist. Restore reverses the apply.
const playlistSyncSchema = `
CREATE TABLE IF NOT EXISTS playlist_sync (
  local_playlist_id INTEGER PRIMARY KEY,
  remote_id   TEXT NOT NULL,              -- backend pl_…
  local_hash  TEXT NOT NULL DEFAULT '',   -- local content hash at last sync
  remote_hash TEXT NOT NULL DEFAULT '',   -- remote content hash at last sync
  synced_at   TEXT NOT NULL               -- RFC3339 UTC
);
CREATE INDEX IF NOT EXISTS idx_playlist_sync_remote ON playlist_sync(remote_id);

CREATE TABLE IF NOT EXISTS playlist_sync_undo (
  id                INTEGER PRIMARY KEY,
  local_playlist_id INTEGER NOT NULL,
  direction         TEXT NOT NULL,        -- push (snapshot = prior remote) | pull (= prior local)
  snapshot_json     TEXT NOT NULL,
  created_at        TEXT NOT NULL         -- RFC3339 UTC
);
CREATE INDEX IF NOT EXISTS idx_plsync_undo_pl ON playlist_sync_undo(local_playlist_id, id);
`

// PlaylistSyncRow is one local↔remote playlist mapping.
type PlaylistSyncRow struct {
	LocalPlaylistID int64
	RemoteID        string
	LocalHash       string
	RemoteHash      string
	SyncedAt        time.Time
}

// GetPlaylistSync returns the mapping for a local playlist, ok=false if unmapped.
func (d *DB) GetPlaylistSync(localID int64) (PlaylistSyncRow, bool, error) {
	if d == nil || d.db == nil {
		return PlaylistSyncRow{}, false, nil
	}
	var r PlaylistSyncRow
	var at string
	err := d.db.QueryRow(`SELECT local_playlist_id, remote_id, local_hash, remote_hash, synced_at
		FROM playlist_sync WHERE local_playlist_id=?`, localID).
		Scan(&r.LocalPlaylistID, &r.RemoteID, &r.LocalHash, &r.RemoteHash, &at)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PlaylistSyncRow{}, false, nil
		}
		return PlaylistSyncRow{}, false, err
	}
	r.SyncedAt, _ = time.Parse(time.RFC3339, at)
	return r, true, nil
}

// PlaylistSyncRows returns every mapping keyed by local playlist id.
func (d *DB) PlaylistSyncRows() (map[int64]PlaylistSyncRow, error) {
	out := map[int64]PlaylistSyncRow{}
	if d == nil || d.db == nil {
		return out, nil
	}
	rows, err := d.db.Query(`SELECT local_playlist_id, remote_id, local_hash, remote_hash, synced_at FROM playlist_sync`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var r PlaylistSyncRow
		var at string
		if err := rows.Scan(&r.LocalPlaylistID, &r.RemoteID, &r.LocalHash, &r.RemoteHash, &at); err != nil {
			return nil, err
		}
		r.SyncedAt, _ = time.Parse(time.RFC3339, at)
		out[r.LocalPlaylistID] = r
	}
	return out, rows.Err()
}

// SavePlaylistSync upserts a mapping. SyncedAt zero = now.
func (d *DB) SavePlaylistSync(r PlaylistSyncRow) error {
	if d == nil || d.db == nil || r.LocalPlaylistID == 0 || r.RemoteID == "" {
		return nil
	}
	at := r.SyncedAt
	if at.IsZero() {
		at = time.Now()
	}
	_, err := d.db.Exec(`
		INSERT INTO playlist_sync (local_playlist_id, remote_id, local_hash, remote_hash, synced_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(local_playlist_id) DO UPDATE SET
		  remote_id=excluded.remote_id, local_hash=excluded.local_hash,
		  remote_hash=excluded.remote_hash, synced_at=excluded.synced_at`,
		r.LocalPlaylistID, r.RemoteID, r.LocalHash, r.RemoteHash, at.UTC().Format(time.RFC3339))
	return err
}

// DeletePlaylistSync removes a mapping (unlink - both sides keep their content).
func (d *DB) DeletePlaylistSync(localID int64) error {
	if d == nil || d.db == nil {
		return nil
	}
	_, err := d.db.Exec(`DELETE FROM playlist_sync WHERE local_playlist_id=?`, localID)
	return err
}

// PlaylistUndoRow is one pre-apply snapshot.
type PlaylistUndoRow struct {
	ID              int64
	LocalPlaylistID int64
	Direction       string // push|pull
	SnapshotJSON    string
	CreatedAt       time.Time
}

// playlistUndoKeep caps stored snapshots per playlist.
const playlistUndoKeep = 10

// AddPlaylistUndo stores a snapshot and prunes to the newest playlistUndoKeep per playlist.
func (d *DB) AddPlaylistUndo(localID int64, direction, snapshotJSON string) (int64, error) {
	if d == nil || d.db == nil || localID == 0 {
		return 0, nil
	}
	res, err := d.db.Exec(`INSERT INTO playlist_sync_undo (local_playlist_id, direction, snapshot_json, created_at)
		VALUES (?,?,?,?)`, localID, direction, snapshotJSON, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	_, err = d.db.Exec(`DELETE FROM playlist_sync_undo WHERE local_playlist_id=? AND id NOT IN
		(SELECT id FROM playlist_sync_undo WHERE local_playlist_id=? ORDER BY id DESC LIMIT ?)`,
		localID, localID, playlistUndoKeep)
	return id, err
}

// PlaylistUndos lists a playlist's snapshots, newest first.
func (d *DB) PlaylistUndos(localID int64) ([]PlaylistUndoRow, error) {
	if d == nil || d.db == nil {
		return nil, nil
	}
	rows, err := d.db.Query(`SELECT id, local_playlist_id, direction, snapshot_json, created_at
		FROM playlist_sync_undo WHERE local_playlist_id=? ORDER BY id DESC`, localID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []PlaylistUndoRow
	for rows.Next() {
		var r PlaylistUndoRow
		var at string
		if err := rows.Scan(&r.ID, &r.LocalPlaylistID, &r.Direction, &r.SnapshotJSON, &at); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339, at)
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetPlaylistUndo returns one snapshot, ok=false if absent.
func (d *DB) GetPlaylistUndo(id int64) (PlaylistUndoRow, bool, error) {
	if d == nil || d.db == nil {
		return PlaylistUndoRow{}, false, nil
	}
	var r PlaylistUndoRow
	var at string
	err := d.db.QueryRow(`SELECT id, local_playlist_id, direction, snapshot_json, created_at
		FROM playlist_sync_undo WHERE id=?`, id).
		Scan(&r.ID, &r.LocalPlaylistID, &r.Direction, &r.SnapshotJSON, &at)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PlaylistUndoRow{}, false, nil
		}
		return PlaylistUndoRow{}, false, err
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339, at)
	return r, true, nil
}

// DeletePlaylistUndo removes a consumed snapshot.
func (d *DB) DeletePlaylistUndo(id int64) error {
	if d == nil || d.db == nil {
		return nil
	}
	_, err := d.db.Exec(`DELETE FROM playlist_sync_undo WHERE id=?`, id)
	return err
}

// AllTrackLinks returns every track_hash → resolved backend track link (for bulk reverse
// mapping; per-hash GetTrackLink stays the hot path).
func (d *DB) AllTrackLinks() (map[string]TrackLink, error) {
	out := map[string]TrackLink{}
	if d == nil || d.db == nil {
		return out, nil
	}
	rows, err := d.db.Query(`SELECT track_hash, track_id, provisional, COALESCE(confidence,0) FROM track_links`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var l TrackLink
		var prov int
		if err := rows.Scan(&l.TrackHash, &l.TrackID, &prov, &l.Confidence); err != nil {
			return nil, err
		}
		l.Provisional = prov != 0
		out[l.TrackHash] = l
	}
	return out, rows.Err()
}
