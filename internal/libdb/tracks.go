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

// LoadAllTracks returns every track across all sources incl. cues/beatgrid -
// the library metadata uploader's working set.
func (d *DB) LoadAllTracks() ([]musiclib.Track, error) {
	rows, err := d.db.Query(`
		SELECT path, title, artist, album, genre, label, comment, key, bpm, duration_sec,
			play_count, rating, COALESCE(import_date,''), release_date, last_played,
			COALESCE(cues,''), COALESCE(beatgrid,'')
		FROM tracks ORDER BY artist COLLATE NOCASE, title COLLATE NOCASE`)
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

// AllSourcedTracks returns every stored track across all sources, each tagged with its source
// app. The sync engine groups these by portable hash and merges them into one canonical track.
func (d *DB) AllSourcedTracks() ([]SourcedTrack, error) {
	rows, err := d.db.Query(`
		SELECT s.app, t.path, t.title, t.artist, t.album, t.genre, t.label, t.comment, t.key,
			t.bpm, t.duration_sec, t.bitrate_bps, t.file_size_kb, t.play_count, t.rating,
			t.import_date, t.release_date, t.last_played, COALESCE(t.cues,''), COALESCE(t.beatgrid,'')
		FROM tracks t JOIN sources s ON s.id = t.source_id
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

// UpsertTrack inserts/updates one track under sourceID outside a sync (synthetic rows
// like set-builder dividers; no change_log - these aren't user library mutations).
func (d *DB) UpsertTrack(sourceID int64, t musiclib.Track) error {
	if t.Path == "" {
		return nil
	}
	cues, _ := json.Marshal(t.Cues)
	grid, _ := json.Marshal(t.Beatgrid)
	_, err := d.db.Exec(`
		INSERT INTO tracks (source_id, path, title, artist, album, genre, label, comment,
			key, bpm, duration_sec, bitrate_bps, file_size_kb, play_count, rating,
			import_date, release_date, last_played, cues, beatgrid, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(source_id, path) DO UPDATE SET
			title=excluded.title, artist=excluded.artist, duration_sec=excluded.duration_sec,
			updated_at=excluded.updated_at`,
		sourceID, t.Path, t.Title, t.Artist, t.Album, t.Genre, t.Label, t.Comment,
		t.Key, t.BPM, t.DurationSec, t.BitrateBps, t.FileSizeKB, t.PlayCount, t.Rating,
		t.ImportDate, t.ReleaseDate, t.LastPlayed, string(cues), string(grid),
		time.Now().UTC().Format(time.RFC3339))
	return err
}
