// Package libdb is rave-mate's relational library store (SQLite via modernc, pure-Go, no
// cgo). It persists the imported DJ library - sources, tracks (with cues/beatgrid),
// and play-history sessions - so the collection survives restarts and a "Refresh" only
// upserts what changed instead of re-importing the whole NML every launch. Enrichment
// (analysis → DB) and tag-edit history (revertible file writes) build on this schema.
package libdb

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (registers "sqlite")

	"rave.page/mate/internal/musiclib"
)

const schema = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;

CREATE TABLE IF NOT EXISTS sources (
  id               INTEGER PRIMARY KEY,
  app              TEXT NOT NULL,
  version          TEXT,
  path             TEXT NOT NULL,
  collection_mtime INTEGER,
  imported_at      TEXT,
  UNIQUE(app, path)
);

CREATE TABLE IF NOT EXISTS tracks (
  id           INTEGER PRIMARY KEY,
  source_id    INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  path         TEXT NOT NULL,
  title        TEXT, artist TEXT, album TEXT, genre TEXT, label TEXT, comment TEXT,
  key          TEXT, bpm REAL,
  duration_sec REAL, bitrate_bps INTEGER, file_size_kb INTEGER,
  play_count   INTEGER, rating INTEGER,
  import_date  TEXT, release_date TEXT, last_played TEXT,
  cues         TEXT, beatgrid TEXT,            -- JSON
  fingerprint  TEXT, file_mtime INTEGER,        -- enrichment / change-tracking
  updated_at   TEXT,
  UNIQUE(source_id, path)
);
CREATE INDEX IF NOT EXISTS idx_tracks_source ON tracks(source_id);
CREATE INDEX IF NOT EXISTS idx_tracks_artist ON tracks(artist);
CREATE INDEX IF NOT EXISTS idx_tracks_title  ON tracks(title);

CREATE TABLE IF NOT EXISTS sessions (
  id         INTEGER PRIMARY KEY,
  source_id  INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  started_at TEXT,
  played     TEXT,                              -- JSON []PlayedTrack
  UNIQUE(source_id, name)
);

-- Revertible record of every tag write to a real file: the prior values (before) so a
-- revert restores them exactly, and what we wrote (after). path is the audio file on disk.
CREATE TABLE IF NOT EXISTS tag_edits (
  id         INTEGER PRIMARY KEY,
  path       TEXT NOT NULL,
  written_at TEXT NOT NULL,
  before     TEXT NOT NULL,                     -- JSON map[field]value (pre-write snapshot)
  after      TEXT NOT NULL,                     -- JSON map[field]value (what we wrote)
  reverted   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_tag_edits_path ON tag_edits(path);

-- Append-only history of every library-state mutation (play_count/last_played/rating/
-- metadata/cues + recorder play events). The backbone for cross-machine merge (Phase 3)
-- and rollback. track_hash = portable identity (sha256(lower(artist)|lower(title)|round
-- (duration))); track_fp = Chromaprint when known (usually NULL today). source_id/path =
-- the local row. seq = this node's monotonic counter (Lamport).
CREATE TABLE IF NOT EXISTS change_log (
  id         INTEGER PRIMARY KEY,
  ts         TEXT NOT NULL,                  -- RFC3339 UTC
  node_id    TEXT NOT NULL,
  seq        INTEGER NOT NULL,
  track_fp   TEXT,
  track_hash TEXT NOT NULL,
  source_id  INTEGER,
  path       TEXT,
  field      TEXT NOT NULL,                  -- play_count|last_played|rating|bpm|... |play_event|_import
  op         TEXT NOT NULL,                  -- set|play|import
  old_value  TEXT,                           -- JSON
  new_value  TEXT,                           -- JSON
  origin     TEXT NOT NULL,                  -- import|tagsync|recorder|manual
  reverted   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_change_log_track ON change_log(track_hash);
CREATE INDEX IF NOT EXISTS idx_change_log_seq   ON change_log(node_id, seq);

-- Captured set audio (Traktor broadcast → local Icecast receiver). recording_id links to
-- the recorder's tracklist (bbolt) so a track's offset into the audio = track.StartedAt −
-- started_at. Audio is broadcast-quality lossy (ogg/mp3). One row per captured file.
CREATE TABLE IF NOT EXISTS set_recordings (
  id           TEXT PRIMARY KEY,
  recording_id TEXT,                            -- recorder Recording.ID ("" if unmatched)
  path         TEXT NOT NULL,
  format       TEXT,                            -- ogg|mp3|aac|bin | mkv|mp4|…
  mount        TEXT,
  started_at   TEXT NOT NULL,                   -- RFC3339 UTC (capture start = audio t=0)
  ended_at     TEXT,
  bytes        INTEGER NOT NULL DEFAULT 0,
  kind         TEXT NOT NULL DEFAULT 'icecast'  -- icecast (audio broadcast) | obs (video recording)
);
CREATE INDEX IF NOT EXISTS idx_set_recordings_rec ON set_recordings(recording_id);

-- Consolidated played-track log: every track confirmed as "played" by the live recorder
-- (fused from all inputs - Traktor/NML/MIDI/Icecast metadata), with absolute start/end times
-- and the deck it played on. recording_id links to the recorder Recording it belongs to (""
-- if it played outside any set). This is the durable, queryable record the bbolt recording
-- snapshot is derived from, so played history survives even if a recording is deleted.
CREATE TABLE IF NOT EXISTS played_tracks (
  id           TEXT PRIMARY KEY,
  recording_id TEXT,                            -- recorder Recording.ID ("" if none)
  artist       TEXT, title TEXT, album TEXT, key TEXT, bpm REAL,
  deck         TEXT,
  title_source TEXT,                            -- provenance of the title/artist
  started_at   TEXT NOT NULL,                   -- RFC3339 UTC
  ended_at     TEXT
);
CREATE INDEX IF NOT EXISTS idx_played_tracks_rec     ON played_tracks(recording_id);
CREATE INDEX IF NOT EXISTS idx_played_tracks_started ON played_tracks(started_at);
`

// DB is the relational library store. A nil *DB is NOT valid here (unlike the bbolt store) -
// callers gate on it being non-nil, since the library feature needs persistence to work.
type DB struct {
	db     *sql.DB
	nodeID string // change_log author (this node); set once at startup via SetNodeID
}

// SetNodeID sets the node identity stamped on every change_log row. Call once at startup
// after the identity loads. Empty until set (events still record, with an empty author).
func (d *DB) SetNodeID(id string) {
	if d != nil {
		d.nodeID = id
	}
}

// Open opens (creating if needed) the SQLite DB at path and applies the schema.
func Open(path string) (*DB, error) {
	sdb, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	sdb.SetMaxOpenConns(1) // serialize writers; modernc + WAL is happiest single-writer
	if _, err := sdb.Exec(schema); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if _, err := sdb.Exec(playlistSchema); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("apply playlist schema: %w", err)
	}
	if _, err := sdb.Exec(playLayerSchema); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("apply play-layer schema: %w", err)
	}
	if _, err := sdb.Exec(playlistSyncSchema); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("apply playlist-sync schema: %w", err)
	}
	if _, err := sdb.Exec(trackArtSchema); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("apply track-art schema: %w", err)
	}
	// Additive migrations for pre-existing DBs (CREATE IF NOT EXISTS skips new columns).
	// "duplicate column" = already migrated; ignored.
	for _, m := range []string{
		`ALTER TABLE set_recordings ADD COLUMN kind TEXT NOT NULL DEFAULT 'icecast'`,
		`ALTER TABLE library_sync ADD COLUMN library_track_id TEXT`,
		`ALTER TABLE playlist_tracks ADD COLUMN title TEXT`,  // pull: unresolved remote item snapshot
		`ALTER TABLE playlist_tracks ADD COLUMN artist TEXT`, // pull: unresolved remote item snapshot
		`ALTER TABLE playlists ADD COLUMN pulled_at TEXT`,    // last remote→local apply (NML untouched)
		`ALTER TABLE track_art ADD COLUMN artist TEXT`,       // name-based cover resolution
		`ALTER TABLE track_art ADD COLUMN title TEXT`,
		// Index AFTER the columns exist (an existing track_art predates them; an in-schema index
		// would fail to open the DB → whole library store disabled).
		`CREATE INDEX IF NOT EXISTS idx_track_art_meta ON track_art(artist, title)`,
	} {
		_, _ = sdb.Exec(m)
	}
	return &DB{db: sdb}, nil
}

// Close closes the DB.
func (d *DB) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

// SourceRow is a persisted import source (with its id + last-seen collection mtime).
type SourceRow struct {
	ID              int64
	App, Version    string
	Path            string
	CollectionMtime int64
}

// UpsertSource inserts or updates the source for (app, path) and returns its row.
func (d *DB) UpsertSource(s musiclib.Source, collectionMtime int64) (SourceRow, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.db.Exec(`
		INSERT INTO sources (app, version, path, collection_mtime, imported_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(app, path) DO UPDATE SET
			version = excluded.version,
			collection_mtime = excluded.collection_mtime,
			imported_at = excluded.imported_at`,
		s.App, s.Version, s.Path, collectionMtime, now)
	if err != nil {
		return SourceRow{}, err
	}
	return d.SourceByAppPath(s.App, s.Path)
}

// SourceByAppPath returns the stored source for (app, path), ok=false if absent.
func (d *DB) SourceByAppPath(app, path string) (SourceRow, error) {
	var r SourceRow
	err := d.db.QueryRow(
		`SELECT id, app, version, path, COALESCE(collection_mtime,0) FROM sources WHERE app=? AND path=?`,
		app, path).Scan(&r.ID, &r.App, &r.Version, &r.Path, &r.CollectionMtime)
	if err == sql.ErrNoRows {
		return SourceRow{}, nil
	}
	return r, err
}

// FirstSource returns the most-recently-imported source (the one the UI loads on launch),
// ok=false if none imported yet.
func (d *DB) FirstSource() (SourceRow, bool, error) {
	var r SourceRow
	err := d.db.QueryRow(
		`SELECT id, app, version, path, COALESCE(collection_mtime,0)
		   FROM sources ORDER BY imported_at DESC LIMIT 1`).
		Scan(&r.ID, &r.App, &r.Version, &r.Path, &r.CollectionMtime)
	if err == sql.ErrNoRows {
		return SourceRow{}, false, nil
	}
	return r, err == nil, err
}

// SyncResult reports what an incremental refresh changed.
type SyncResult struct {
	Added, Updated, Removed, Total int
}

// CountTracks returns the number of stored tracks for a source.
func (d *DB) CountTracks(sourceID int64) (int, error) {
	var n int
	err := d.db.QueryRow(`SELECT COUNT(*) FROM tracks WHERE source_id=?`, sourceID).Scan(&n)
	return n, err
}
