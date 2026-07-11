package libdb

import (
	"database/sql"
	"encoding/json"
	"time"

	"rave.page/mate/internal/musiclib"
)

// TrackSync streams an import into the DB inside one transaction, upserting each track and
// deleting any that vanished from the source - the "refresh" path. Usage:
//
//	sy, _ := db.BeginTrackSync(sourceID)
//	musiclib.ParseCollection(r, func(t musiclib.Track) { _ = sy.Add(t) })
//	res, _ := sy.Commit()
type TrackSync struct {
	db       *DB
	sourceID int64
	tx       *sql.Tx
	stmt     *sql.Stmt
	existing map[string]trackState // rows present before this sync (for diff + added-vs-updated + removal)
	seen     map[string]bool
	events   []ChangeEvent // buffered change_log rows, flushed atomically in Commit
	res      SyncResult
}

// trackState is the prior value of the change-tracked columns for one path.
type trackState struct {
	playCount                       int
	rating                          int
	bpm                             float64
	key, genre, comment, lastPlayed string
	cues, beatgrid                  string // JSON text as stored
}

// BeginTrackSync starts a refresh transaction, preloading the source's existing tracks
// (the change-tracked columns) so Add can diff old→new and journal real changes.
func (d *DB) BeginTrackSync(sourceID int64) (*TrackSync, error) {
	existing := map[string]trackState{}
	rows, err := d.db.Query(`
		SELECT path, COALESCE(play_count,0), COALESCE(rating,0), COALESCE(bpm,0),
		       COALESCE(key,''), COALESCE(genre,''), COALESCE(comment,''), COALESCE(last_played,''),
		       COALESCE(cues,''), COALESCE(beatgrid,'')
		FROM tracks WHERE source_id=?`, sourceID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p string
		var s trackState
		if err := rows.Scan(&p, &s.playCount, &s.rating, &s.bpm, &s.key, &s.genre,
			&s.comment, &s.lastPlayed, &s.cues, &s.beatgrid); err != nil {
			_ = rows.Close()
			return nil, err
		}
		existing[p] = s
	}
	_ = rows.Close()

	tx, err := d.db.Begin()
	if err != nil {
		return nil, err
	}
	stmt, err := tx.Prepare(`
		INSERT INTO tracks (source_id, path, title, artist, album, genre, label, comment,
			key, bpm, duration_sec, bitrate_bps, file_size_kb, play_count, rating,
			import_date, release_date, last_played, cues, beatgrid, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(source_id, path) DO UPDATE SET
			title=excluded.title, artist=excluded.artist, album=excluded.album,
			genre=excluded.genre, label=excluded.label, comment=excluded.comment,
			key=excluded.key, bpm=excluded.bpm, duration_sec=excluded.duration_sec,
			bitrate_bps=excluded.bitrate_bps, file_size_kb=excluded.file_size_kb,
			play_count=excluded.play_count, rating=excluded.rating,
			import_date=excluded.import_date, release_date=excluded.release_date,
			last_played=excluded.last_played, cues=excluded.cues, beatgrid=excluded.beatgrid,
			updated_at=excluded.updated_at`)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return &TrackSync{db: d, sourceID: sourceID, tx: tx, stmt: stmt, existing: existing, seen: map[string]bool{}}, nil
}

// Add upserts one track. Cues/beatgrid persist as JSON. Safe to call for every parsed track.
func (s *TrackSync) Add(t musiclib.Track) error {
	if t.Path == "" || s.seen[t.Path] {
		return nil
	}
	s.seen[t.Path] = true
	cues, _ := json.Marshal(t.Cues)
	grid, _ := json.Marshal(t.Beatgrid)
	_, err := s.stmt.Exec(s.sourceID, t.Path, t.Title, t.Artist, t.Album, t.Genre, t.Label,
		t.Comment, t.Key, t.BPM, t.DurationSec, t.BitrateBps, t.FileSizeKB, t.PlayCount,
		t.Rating, t.ImportDate, t.ReleaseDate, t.LastPlayed, string(cues), string(grid),
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	if old, ok := s.existing[t.Path]; ok {
		s.res.Updated++
		s.journalDiff(t, old, string(cues), string(grid))
	} else {
		s.res.Added++
		s.journalImport(t)
	}
	return nil
}

// journalDiff buffers a change_log "set" event for each tracked field that changed.
func (s *TrackSync) journalDiff(t musiclib.Track, old trackState, cues, grid string) {
	hash := TrackHash(t.Artist, t.Title, t.DurationSec)
	add := func(field string, oldV, newV any) {
		s.events = append(s.events, ChangeEvent{
			TrackHash: hash, SourceID: s.sourceID, Path: t.Path, Field: field, Op: "set",
			OldValue: jsonStr(oldV), NewValue: jsonStr(newV), Origin: "import",
		})
	}
	if old.playCount != t.PlayCount {
		add("play_count", old.playCount, t.PlayCount)
	}
	if old.lastPlayed != t.LastPlayed {
		add("last_played", old.lastPlayed, t.LastPlayed)
	}
	if old.rating != t.Rating {
		add("rating", old.rating, t.Rating)
	}
	if old.bpm != t.BPM {
		add("bpm", old.bpm, t.BPM)
	}
	if old.key != t.Key {
		add("key", old.key, t.Key)
	}
	if old.genre != t.Genre {
		add("genre", old.genre, t.Genre)
	}
	if old.comment != t.Comment {
		add("comment", old.comment, t.Comment)
	}
	if old.cues != cues {
		add("cues", json.RawMessage(old.cues), json.RawMessage(cues))
	}
	if old.beatgrid != grid {
		add("beatgrid", json.RawMessage(old.beatgrid), json.RawMessage(grid))
	}
}

// journalImport buffers a single baseline event for a newly-imported track (one row per
// track, not per field) - the play-state snapshot anchors future merges + rollback.
func (s *TrackSync) journalImport(t musiclib.Track) {
	s.events = append(s.events, ChangeEvent{
		TrackHash: TrackHash(t.Artist, t.Title, t.DurationSec), SourceID: s.sourceID, Path: t.Path,
		Field: "_import", Op: "import", Origin: "import",
		NewValue: jsonStr(map[string]any{"playCount": t.PlayCount, "lastPlayed": t.LastPlayed, "rating": t.Rating}),
	})
}

// Commit deletes tracks no longer in the source, commits, and returns the diff counts.
func (s *TrackSync) Commit() (SyncResult, error) {
	for path := range s.existing {
		if !s.seen[path] {
			if _, err := s.tx.Exec(`DELETE FROM tracks WHERE source_id=? AND path=?`, s.sourceID, path); err != nil {
				_ = s.tx.Rollback()
				return SyncResult{}, err
			}
			s.res.Removed++
		}
	}
	// Flush buffered change_log events in the same tx - atomic with the track writes.
	if len(s.events) > 0 {
		startSeq, err := s.db.nextSeq(s.tx)
		if err != nil {
			_ = s.tx.Rollback()
			return SyncResult{}, err
		}
		if err := s.db.appendTx(s.tx, startSeq, s.events); err != nil {
			_ = s.tx.Rollback()
			return SyncResult{}, err
		}
	}
	if err := s.tx.Commit(); err != nil {
		return SyncResult{}, err
	}
	s.res.Total = len(s.seen)
	return s.res, nil
}

// Rollback aborts the sync (e.g. on a parse error mid-stream).
func (s *TrackSync) Rollback() { _ = s.tx.Rollback() }

// LoadAllTracks returns every real track across all sources incl. cues/beatgrid -
// the library metadata uploader's working set. Divider marker rows are excluded HERE so
// every caller (collection view, cloud sync, media sync, cleanup) inherits the exclusion.
func (d *DB) LoadAllTracks() ([]musiclib.Track, error) {
	rows, err := d.db.Query(`
		SELECT path, title, artist, album, genre, label, comment, key, bpm, duration_sec,
			play_count, rating, COALESCE(import_date,''), release_date, last_played,
			COALESCE(cues,''), COALESCE(beatgrid,'')
		FROM tracks WHERE is_divider=0
		ORDER BY artist COLLATE NOCASE, title COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []musiclib.Track
	for rows.Next() {
		var t musiclib.Track
		var cues, grid string
		if err := rows.Scan(&t.Path, &t.Title, &t.Artist, &t.Album, &t.Genre, &t.Label,
			&t.Comment, &t.Key, &t.BPM, &t.DurationSec, &t.PlayCount, &t.Rating,
			&t.ImportDate, &t.ReleaseDate, &t.LastPlayed, &cues, &grid); err != nil {
			return nil, err
		}
		if cues != "" {
			_ = json.Unmarshal([]byte(cues), &t.Cues)
		}
		if grid != "" {
			_ = json.Unmarshal([]byte(grid), &t.Beatgrid)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SourcedTrack is a stored track tagged with its source app (which DJ software it came from).
// Used by cross-software sync to merge candidates per field-priority rules.
type SourcedTrack struct {
	App   string
	Track musiclib.Track
}

// AllSourcedTracks returns every real stored track across all sources, each tagged with its
// source app. The sync engine groups these by portable hash and merges them into one canonical
// track. Divider rows are excluded - they must never be merge/match/enrichment candidates.
func (d *DB) AllSourcedTracks() ([]SourcedTrack, error) {
	rows, err := d.db.Query(`
		SELECT s.app, t.path, t.title, t.artist, t.album, t.genre, t.label, t.comment, t.key,
			t.bpm, t.duration_sec, t.bitrate_bps, t.file_size_kb, t.play_count, t.rating,
			t.import_date, t.release_date, t.last_played, COALESCE(t.cues,''), COALESCE(t.beatgrid,'')
		FROM tracks t JOIN sources s ON s.id = t.source_id
		WHERE t.is_divider=0
		ORDER BY t.artist COLLATE NOCASE, t.title COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []SourcedTrack
	for rows.Next() {
		var st SourcedTrack
		t := &st.Track
		var cues, grid string
		if err := rows.Scan(&st.App, &t.Path, &t.Title, &t.Artist, &t.Album, &t.Genre, &t.Label,
			&t.Comment, &t.Key, &t.BPM, &t.DurationSec, &t.BitrateBps, &t.FileSizeKB,
			&t.PlayCount, &t.Rating, &t.ImportDate, &t.ReleaseDate, &t.LastPlayed,
			&cues, &grid); err != nil {
			return nil, err
		}
		if cues != "" {
			_ = json.Unmarshal([]byte(cues), &t.Cues)
		}
		if grid != "" {
			_ = json.Unmarshal([]byte(grid), &t.Beatgrid)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// LoadTracks returns every track for a source, ordered artist then title (the collection view).
func (d *DB) LoadTracks(sourceID int64) ([]musiclib.Track, error) {
	rows, err := d.db.Query(`
		SELECT path, title, artist, album, genre, label, comment, key, bpm, duration_sec,
			bitrate_bps, file_size_kb, play_count, rating, import_date, release_date,
			last_played, COALESCE(cues,''), COALESCE(beatgrid,'')
		FROM tracks WHERE source_id=?
		ORDER BY artist COLLATE NOCASE, title COLLATE NOCASE`, sourceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []musiclib.Track
	for rows.Next() {
		var t musiclib.Track
		var cues, grid string
		if err := rows.Scan(&t.Path, &t.Title, &t.Artist, &t.Album, &t.Genre, &t.Label,
			&t.Comment, &t.Key, &t.BPM, &t.DurationSec, &t.BitrateBps, &t.FileSizeKB,
			&t.PlayCount, &t.Rating, &t.ImportDate, &t.ReleaseDate, &t.LastPlayed,
			&cues, &grid); err != nil {
			return nil, err
		}
		if cues != "" {
			_ = json.Unmarshal([]byte(cues), &t.Cues)
		}
		if grid != "" {
			_ = json.Unmarshal([]byte(grid), &t.Beatgrid)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpsertDividerTrack inserts/updates one set-builder divider row under sourceID
// (is_divider=1, no change_log). Dividers exist for playlist display/export only:
// LoadAllTracks/AllSourcedTracks and every outbound sync exclude them.
func (d *DB) UpsertDividerTrack(sourceID int64, t musiclib.Track) error {
	if t.Path == "" {
		return nil
	}
	_, err := d.db.Exec(`
		INSERT INTO tracks (source_id, path, title, artist, duration_sec, is_divider, updated_at)
		VALUES (?,?,?,?,?,1,?)
		ON CONFLICT(source_id, path) DO UPDATE SET
			title=excluded.title, artist=excluded.artist, duration_sec=excluded.duration_sec,
			is_divider=1, updated_at=excluded.updated_at`,
		sourceID, t.Path, t.Title, t.Artist, t.DurationSec,
		time.Now().UTC().Format(time.RFC3339))
	return err
}

// DividerTracks returns the divider marker rows (playlist views resolve their display
// titles from these; they never enter the collection working set).
func (d *DB) DividerTracks() ([]musiclib.Track, error) {
	rows, err := d.db.Query(`SELECT path, COALESCE(title,''), COALESCE(duration_sec,0) FROM tracks WHERE is_divider=1`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []musiclib.Track
	for rows.Next() {
		var t musiclib.Track
		if err := rows.Scan(&t.Path, &t.Title, &t.DurationSec); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DividerPaths returns the divider paths as a set (outbound playlist sync filters on it).
func (d *DB) DividerPaths() (map[string]bool, error) {
	ts, err := d.DividerTracks()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ts))
	for _, t := range ts {
		out[t.Path] = true
	}
	return out, nil
}
