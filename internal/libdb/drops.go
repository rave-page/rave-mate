package libdb

import (
	"database/sql"
	"encoding/json"
	"time"

	"rave.page/mate/internal/musiclib"
)

// Drop markers (rave-mate enrichment): per-FILE, not per-source - they survive source
// re-imports and deletions, so they live in their own path-keyed table (the collection
// tables are rebuilt on every import). Mirrored into the audio file's tag by the caller.

const dropsSchema = `
CREATE TABLE IF NOT EXISTS track_drops (
  path       TEXT PRIMARY KEY,
  drops      TEXT NOT NULL,      -- JSON []float64 (ms, sorted)
  updated_at TEXT NOT NULL
);`

// SetDrops stores a track's drop markers (ms, sorted by caller) and journals the mutation.
// Clearing keeps a `[]` tombstone row (never deletes): library sync sends drops_ms for
// exactly the rows in track_drops, so the tombstone propagates the clear to the server;
// clearing a track that never had a row is a no-op (no tombstone, no journal).
func (d *DB) SetDrops(path, artist, title string, durationSec float64, drops []float64) error {
	if d == nil || d.db == nil {
		return nil
	}
	old, _ := d.Drops(path)
	var err error
	if len(drops) == 0 {
		res, uerr := d.db.Exec(`UPDATE track_drops SET drops='[]', updated_at=? WHERE path=?`,
			time.Now().UTC().Format(time.RFC3339), path)
		if uerr != nil {
			return uerr
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
	} else {
		raw, jerr := json.Marshal(drops)
		if jerr != nil {
			return jerr
		}
		_, err = d.db.Exec(`
			INSERT INTO track_drops (path, drops, updated_at) VALUES (?,?,?)
			ON CONFLICT(path) DO UPDATE SET drops=excluded.drops, updated_at=excluded.updated_at`,
			path, string(raw), time.Now().UTC().Format(time.RFC3339))
	}
	if err != nil {
		return err
	}
	return d.AppendChanges([]ChangeEvent{{
		TrackHash: TrackHash(artist, title, durationSec),
		Path:      path, Field: "drops", Op: "set",
		OldValue: jsonStr(old), NewValue: jsonStr(drops), Origin: "manual",
	}})
}

// Drops returns a track's drop markers (nil when no row; empty non-nil for a cleared
// tombstone - callers gate on len).
func (d *DB) Drops(path string) ([]float64, error) {
	if d == nil || d.db == nil {
		return nil, nil
	}
	var raw string
	err := d.db.QueryRow(`SELECT drops FROM track_drops WHERE path=?`, path).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []float64
	if jerr := json.Unmarshal([]byte(raw), &out); jerr != nil {
		return nil, jerr
	}
	return out, nil
}

// DropRows returns every track_drops row keyed by path, INCLUDING cleared `[]` tombstones.
// Library sync's send rule: drops_ms goes on the wire for exactly these rows (empty array
// = explicit server-side clear); tracks without a row omit the field.
func (d *DB) DropRows() (map[string][]float64, error) {
	if d == nil || d.db == nil {
		return nil, nil
	}
	rows, err := d.db.Query(`SELECT path, drops FROM track_drops`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]float64{}
	for rows.Next() {
		var p, raw string
		if err := rows.Scan(&p, &raw); err != nil {
			return nil, err
		}
		var ds []float64
		if json.Unmarshal([]byte(raw), &ds) == nil {
			out[p] = ds
		}
	}
	return out, rows.Err()
}

// AllDrops returns every path with ≥1 drop marker (for the collection filter; tombstones
// excluded - use DropRows for sync).
func (d *DB) AllDrops() (map[string][]float64, error) {
	if d == nil || d.db == nil {
		return nil, nil
	}
	rows, err := d.db.Query(`SELECT path, drops FROM track_drops`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]float64{}
	for rows.Next() {
		var p, raw string
		if err := rows.Scan(&p, &raw); err != nil {
			return nil, err
		}
		var ds []float64
		if json.Unmarshal([]byte(raw), &ds) == nil && len(ds) > 0 {
			out[p] = ds
		}
	}
	return out, rows.Err()
}

// UpdateTrackCues replaces a track's cue list on EVERY source row carrying the path
// (the collection view is the path-merged union) and journals old→new.
func (d *DB) UpdateTrackCues(t musiclib.Track, cues []musiclib.CuePoint) error {
	if d == nil || d.db == nil {
		return nil
	}
	raw, err := json.Marshal(cues)
	if err != nil {
		return err
	}
	oldRaw, _ := json.Marshal(t.Cues)
	if _, err := d.db.Exec(`UPDATE tracks SET cues=?, updated_at=? WHERE path=?`,
		string(raw), time.Now().UTC().Format(time.RFC3339), t.Path); err != nil {
		return err
	}
	d.bumpTracks() // invalidate LoadAllTracks snapshot
	return d.AppendChanges([]ChangeEvent{{
		TrackHash: TrackHash(t.Artist, t.Title, t.DurationSec),
		Path:      t.Path, Field: "cues", Op: "set",
		OldValue: string(oldRaw), NewValue: string(raw), Origin: "manual",
	}})
}

// UpdateTrackBeatgrid replaces a track's grid markers on every source row carrying the
// path (manual grid alignment from the cue editor) and journals old→new.
func (d *DB) UpdateTrackBeatgrid(t musiclib.Track, grid []musiclib.GridMarker) error {
	if d == nil || d.db == nil {
		return nil
	}
	raw, err := json.Marshal(grid)
	if err != nil {
		return err
	}
	oldRaw, _ := json.Marshal(t.Beatgrid)
	if _, err := d.db.Exec(`UPDATE tracks SET beatgrid=?, updated_at=? WHERE path=?`,
		string(raw), time.Now().UTC().Format(time.RFC3339), t.Path); err != nil {
		return err
	}
	d.bumpTracks() // invalidate LoadAllTracks snapshot
	return d.AppendChanges([]ChangeEvent{{
		TrackHash: TrackHash(t.Artist, t.Title, t.DurationSec),
		Path:      t.Path, Field: "beatgrid", Op: "set",
		OldValue: string(oldRaw), NewValue: string(raw), Origin: "manual",
	}})
}
