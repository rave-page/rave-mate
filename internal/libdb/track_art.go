package libdb

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Persistent per-track cover art. Populated at collection import/analysis and lazily on first
// play, so the overlay/Spout/PNG renderers read covers straight from the DB instead of re-probing
// files every tick. Keyed by the audio file path (the same path the import + a live deck carry).
// artist+title are stored too so a live deck can resolve its cover by name during the window before
// Traktor sends the file path. mime="none" + empty data is a definitive "no embedded art" marker.
const trackArtSchema = `
CREATE TABLE IF NOT EXISTS track_art (
  path       TEXT PRIMARY KEY,
  mime       TEXT NOT NULL,            -- image/jpeg | none
  data       BLOB,                     -- normalized JPEG; empty when mime=none
  width      INTEGER NOT NULL DEFAULT 0,
  height     INTEGER NOT NULL DEFAULT 0,
  source     TEXT,                     -- embedded | ffmpeg | api
  artist     TEXT,                     -- name-based resolution before the path arrives
  title      TEXT,
  updated_at TEXT NOT NULL
);
`

// noneMIME marks a file probed to have no usable embedded art (so it isn't re-probed).
const noneMIME = "none"

// TrackArt is a stored cover image (or a no-art marker) for an audio file.
type TrackArt struct {
	Path          string
	MIME          string // image/jpeg | none
	Data          []byte // normalized JPEG; nil when MIME=none
	Width, Height int
	Source        string
	Artist, Title string
}

// HasArt reports whether the row carries usable image bytes (vs a no-art marker).
func (a TrackArt) HasArt() bool { return a.MIME != "" && a.MIME != noneMIME && len(a.Data) > 0 }

// GetTrackArt returns stored art for a path. analyzed=true when the path has a row (even a no-art
// marker), so callers can skip re-extraction.
func (d *DB) GetTrackArt(path string) (TrackArt, bool, error) {
	if d == nil || d.db == nil || path == "" {
		return TrackArt{}, false, nil
	}
	a := TrackArt{Path: path}
	err := d.db.QueryRow(
		`SELECT mime, data, width, height, COALESCE(source,'') FROM track_art WHERE path=?`, path).
		Scan(&a.MIME, &a.Data, &a.Width, &a.Height, &a.Source)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TrackArt{}, false, nil
		}
		return TrackArt{}, false, err
	}
	return a, true, nil
}

// GetTrackArtByMeta resolves cover bytes by artist+title (case-insensitive) for the window before
// Traktor sends the file path. ok=true ONLY on a single unambiguous match that actually has art -
// 0 matches, >1 matches, or a name that maps only to a no-art marker all return ok=false (the
// caller waits for the path). This is the "one exact name match = canonical" rule.
func (d *DB) GetTrackArtByMeta(artist, title string) ([]byte, bool) {
	if d == nil || d.db == nil {
		return nil, false
	}
	artist, title = strings.TrimSpace(artist), strings.TrimSpace(title)
	if title == "" {
		return nil, false
	}
	// Match either track_art's own artist/title (covers tracks not in the library table, populated
	// on store) OR a unique tracks-table row by name → its path (covers the imported library without
	// needing a meta backfill). >1 distinct cover → ambiguous → caller waits for the path.
	rows, err := d.db.Query(`
		SELECT ta.data FROM track_art ta
		 WHERE ta.mime!='none' AND length(COALESCE(ta.data,''))>0
		   AND ( (lower(COALESCE(ta.artist,''))=lower(?1) AND lower(COALESCE(ta.title,''))=lower(?2))
		      OR ta.path IN (SELECT t.path FROM tracks t
		                      WHERE lower(COALESCE(t.artist,''))=lower(?1)
		                        AND lower(COALESCE(t.title,''))=lower(?2)) )
		 LIMIT 2`, artist, title)
	if err != nil {
		return nil, false
	}
	defer func() { _ = rows.Close() }()
	var first []byte
	n := 0
	for rows.Next() {
		n++
		if n > 1 {
			return nil, false // ambiguous - wait for the path
		}
		if err := rows.Scan(&first); err != nil {
			return nil, false
		}
	}
	if n == 1 && len(first) > 0 {
		return first, true
	}
	return nil, false
}

// TrackPathByMeta resolves a local file path by a UNIQUE artist+title match (case-insensitive),
// for the window before a live deck reports its file path (mirrors GetTrackArtByMeta). ok=false on
// 0 or >1 distinct matches - the caller then waits for the deck's own path.
func (d *DB) TrackPathByMeta(artist, title string) (string, bool) {
	if d == nil || d.db == nil {
		return "", false
	}
	artist, title = strings.TrimSpace(artist), strings.TrimSpace(title)
	if title == "" {
		return "", false
	}
	// Check both the library tracks table AND track_art (which records the path of any file probed
	// for cover art) - so a track that isn't imported but whose art resolved still yields a path.
	rows, err := d.db.Query(`
		SELECT DISTINCT path FROM (
		  SELECT path FROM tracks
		    WHERE path<>'' AND lower(COALESCE(artist,''))=lower(?1) AND lower(title)=lower(?2)
		  UNION
		  SELECT path FROM track_art
		    WHERE path<>'' AND lower(COALESCE(artist,''))=lower(?1) AND lower(COALESCE(title,''))=lower(?2)
		) LIMIT 2`, artist, title)
	if err != nil {
		return "", false
	}
	defer func() { _ = rows.Close() }()
	var first string
	n := 0
	for rows.Next() {
		n++
		if n > 1 {
			return "", false // ambiguous - wait for the deck's path
		}
		if err := rows.Scan(&first); err != nil {
			return "", false
		}
	}
	if n == 1 && first != "" {
		return first, true
	}
	return "", false
}

// PutTrackArt upserts stored art (nil-safe). data nil + mime "none" records a definitive no-art
// result so the file is never re-probed. artist/title are stored for name-based resolution.
func (d *DB) PutTrackArt(a TrackArt) error {
	if d == nil || d.db == nil || a.Path == "" {
		return nil
	}
	if a.MIME == "" || !a.HasArt() {
		a.MIME, a.Data = noneMIME, nil
	}
	_, err := d.db.Exec(`
		INSERT INTO track_art (path, mime, data, width, height, source, artist, title, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(path) DO UPDATE SET
		  mime=excluded.mime, data=excluded.data, width=excluded.width,
		  height=excluded.height, source=excluded.source,
		  artist=COALESCE(NULLIF(excluded.artist,''), track_art.artist),
		  title=COALESCE(NULLIF(excluded.title,''), track_art.title),
		  updated_at=excluded.updated_at`,
		a.Path, a.MIME, a.Data, a.Width, a.Height, nullIfEmpty(a.Source),
		nullIfEmpty(a.Artist), nullIfEmpty(a.Title), time.Now().UTC().Format(time.RFC3339))
	return err
}

// HasTrackArt reports whether path has been analyzed (a row exists, art or no-art marker).
func (d *DB) HasTrackArt(path string) (bool, error) {
	if d == nil || d.db == nil || path == "" {
		return false, nil
	}
	var one int
	err := d.db.QueryRow(`SELECT 1 FROM track_art WHERE path=? LIMIT 1`, path).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// CountTrackArt returns counts over analyzed paths: withArt (image stored) and total rows.
func (d *DB) CountTrackArt() (withArt, total int, err error) {
	if d == nil || d.db == nil {
		return 0, 0, nil
	}
	err = d.db.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN mime!='none' AND length(COALESCE(data,''))>0 THEN 1 ELSE 0 END),0),
		COUNT(*) FROM track_art`).Scan(&withArt, &total)
	return
}
