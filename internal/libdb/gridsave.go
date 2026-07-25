package libdb

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"rave.page/mate/internal/musiclib"
)

// HasTrackPath reports whether any source carries a track row for path.
func (d *DB) HasTrackPath(path string) (bool, error) {
	if d == nil || d.db == nil || path == "" {
		return false, nil
	}
	var one int
	err := d.db.QueryRow(`SELECT 1 FROM tracks WHERE path=? LIMIT 1`, path).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// UpdateTrackGrid persists an analyzed BPM + beatgrid on every source row carrying the
// path and journals both fields - the save path for folder-imported tracks, which have
// no DJ-software file the gridfix Apply router could write into. t = the pre-save track
// (old values for the journal).
func (d *DB) UpdateTrackGrid(t musiclib.Track, bpm float64, grid []musiclib.GridMarker) error {
	if d == nil || d.db == nil || t.Path == "" {
		return nil
	}
	raw, err := json.Marshal(grid)
	if err != nil {
		return err
	}
	oldRaw, _ := json.Marshal(t.Beatgrid)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := d.db.Exec(`UPDATE tracks SET bpm=?, beatgrid=?, updated_at=? WHERE path=?`,
		bpm, string(raw), now, t.Path); err != nil {
		return err
	}
	d.bumpTracks() // invalidate LoadAllTracks snapshot
	h := TrackHash(t.Artist, t.Title, t.DurationSec)
	return d.AppendChanges([]ChangeEvent{
		{TrackHash: h, Path: t.Path, Field: "bpm", Op: "set",
			OldValue: jsonNum(t.BPM), NewValue: jsonNum(bpm), Origin: "gridfix"},
		{TrackHash: h, Path: t.Path, Field: "beatgrid", Op: "set",
			OldValue: string(oldRaw), NewValue: string(raw), Origin: "gridfix"},
	})
}
