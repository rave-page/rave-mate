package libdb

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"rave.page/mate/internal/musiclib"
)

// TagEdit is one revertible tag write to a file.
type TagEdit struct {
	ID        int64
	Path      string
	WrittenAt string
	Before    map[string]string // pre-write field snapshot (for revert)
	After     map[string]string // values written
	Reverted  bool
}

// RecordTagEdit persists a tag write (before/after snapshots) and returns its id.
func (d *DB) RecordTagEdit(path string, before, after map[string]string) (int64, error) {
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	res, err := d.db.Exec(
		`INSERT INTO tag_edits (path, written_at, before, after) VALUES (?,?,?,?)`,
		path, time.Now().UTC().Format(time.RFC3339), string(b), string(a))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// LatestTagEdit returns the most recent non-reverted edit for path (ok=false if none).
func (d *DB) LatestTagEdit(path string) (TagEdit, bool, error) {
	var e TagEdit
	var before, after string
	var reverted int
	err := d.db.QueryRow(
		`SELECT id, path, written_at, before, after, reverted FROM tag_edits
		   WHERE path=? AND reverted=0 ORDER BY id DESC LIMIT 1`, path).
		Scan(&e.ID, &e.Path, &e.WrittenAt, &before, &after, &reverted)
	if err == sql.ErrNoRows {
		return TagEdit{}, false, nil
	}
	if err != nil {
		return TagEdit{}, false, err
	}
	_ = json.Unmarshal([]byte(before), &e.Before)
	_ = json.Unmarshal([]byte(after), &e.After)
	e.Reverted = reverted != 0
	return e, true, nil
}

// MarkReverted flags an edit as reverted (after its before-snapshot has been re-applied).
func (d *DB) MarkReverted(id int64) error {
	_, err := d.db.Exec(`UPDATE tag_edits SET reverted=1 WHERE id=?`, id)
	return err
}

// ── shared library resolver (used by tag-writing + recorder enrichment) ───────

// TrackByPath returns the stored track for an exact file path (ok=false if absent).
func (d *DB) TrackByPath(path string) (musiclib.Track, bool, error) {
	return d.scanTrack(d.db.QueryRow(trackSelect+` WHERE path=? LIMIT 1`, path))
}

// TrackByTitleArtist resolves a track by case-insensitive title+artist (the now-playing
// identity the recorder/session use). ok=false if no unambiguous-enough match.
func (d *DB) TrackByTitleArtist(title, artist string) (musiclib.Track, bool, error) {
	return d.scanTrack(d.db.QueryRow(trackSelect+
		` WHERE title=? COLLATE NOCASE AND artist=? COLLATE NOCASE LIMIT 1`,
		strings.TrimSpace(title), strings.TrimSpace(artist)))
}

const trackSelect = `SELECT path, title, artist, album, genre, label, comment, key, bpm,
	duration_sec, bitrate_bps, file_size_kb, play_count, rating, import_date, release_date,
	last_played, COALESCE(cues,''), COALESCE(beatgrid,'') FROM tracks`

func (d *DB) scanTrack(row *sql.Row) (musiclib.Track, bool, error) {
	var t musiclib.Track
	var cues, grid string
	err := row.Scan(&t.Path, &t.Title, &t.Artist, &t.Album, &t.Genre, &t.Label, &t.Comment,
		&t.Key, &t.BPM, &t.DurationSec, &t.BitrateBps, &t.FileSizeKB, &t.PlayCount, &t.Rating,
		&t.ImportDate, &t.ReleaseDate, &t.LastPlayed, &cues, &grid)
	if err == sql.ErrNoRows {
		return musiclib.Track{}, false, nil
	}
	if err != nil {
		return musiclib.Track{}, false, err
	}
	if cues != "" {
		_ = json.Unmarshal([]byte(cues), &t.Cues)
	}
	if grid != "" {
		_ = json.Unmarshal([]byte(grid), &t.Beatgrid)
	}
	return t, true, nil
}
