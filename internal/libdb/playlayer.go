package libdb

import (
	"database/sql"
	"errors"
	"time"
)

// Play-layer backend-sync state (internal/playsync). Two idempotency ledgers so the sync is
// safe to re-run / drain repeatedly without duplicate backend writes:
//
//   - track_links: local track identity (track_hash) → resolved canonical/provisional
//     backend track_id. Set once a fingerprint identifies (or we seed a provisional). The
//     change_log stays append-only; this is the mutable "already linked" marker the brief asks
//     for, kept out-of-band rather than overloading change_log.reverted.
//   - set_uploads: recorder Recording.ID → backend stream_id, once an offline/recorded set has
//     been uploaded (Gap 2). Skips re-upload of an already-published set.
//   - library_sync: per-track payload-hash ledger for the library metadata uploader - a re-run
//     skips every track whose uploaded payload is unchanged. library_track_id (lib_…, added by
//     migration) is the server library row id the media uploads PUT against.
//   - media_sync: per-track waveform/artwork upload ledger (sha256 of the uploaded bytes, or a
//     sentinel: "none" = nothing embedded, "unsupported" = can't recompress). A non-empty value
//     means "done" - re-runs skip the expensive probe/extract entirely.
const playLayerSchema = `
CREATE TABLE IF NOT EXISTS track_links (
  track_hash  TEXT PRIMARY KEY,              -- portable identity (see TrackHash)
  track_id    TEXT NOT NULL,                 -- backend canonical/provisional track id
  provisional INTEGER NOT NULL DEFAULT 0,    -- 1 = we minted it (fingerprint miss)
  isrc        TEXT,
  confidence  REAL,                          -- acoustic-match confidence (0 for provisional)
  synced_at   TEXT NOT NULL                  -- RFC3339 UTC
);

CREATE TABLE IF NOT EXISTS set_uploads (
  recording_id TEXT PRIMARY KEY,             -- recorder Recording.ID
  stream_id    TEXT NOT NULL,                -- backend stream id the set was published as
  track_count  INTEGER NOT NULL DEFAULT 0,
  uploaded_at  TEXT NOT NULL                 -- RFC3339 UTC
);

CREATE TABLE IF NOT EXISTS library_sync (
  track_hash   TEXT PRIMARY KEY,             -- duration-0 TrackHash (same keys as track_links)
  payload_hash TEXT NOT NULL,                -- sha256 hex of the uploaded JSON payload
  synced_at    TEXT NOT NULL                 -- RFC3339 UTC
);

CREATE TABLE IF NOT EXISTS media_sync (
  track_hash    TEXT PRIMARY KEY,            -- duration-0 TrackHash
  waveform_hash TEXT,                        -- sha256 hex of uploaded peak buckets
  artwork_hash  TEXT,                        -- sha256 hex of uploaded artwork | none | unsupported
  synced_at     TEXT NOT NULL                -- RFC3339 UTC
);
`

// TrackLink is the resolved local→backend track identity for a played track.
type TrackLink struct {
	TrackHash   string
	TrackID     string
	Provisional bool
	ISRC        string
	Confidence  float64
	SyncedAt    time.Time
}

// GetTrackLink returns the resolved link for a track hash, ok=false if not yet linked.
func (d *DB) GetTrackLink(trackHash string) (TrackLink, bool, error) {
	if d == nil || d.db == nil || trackHash == "" {
		return TrackLink{}, false, nil
	}
	var l TrackLink
	var prov int
	var syncedAt string
	err := d.db.QueryRow(`
		SELECT track_hash, track_id, provisional, COALESCE(isrc,''), COALESCE(confidence,0), synced_at
		  FROM track_links WHERE track_hash=?`, trackHash).
		Scan(&l.TrackHash, &l.TrackID, &prov, &l.ISRC, &l.Confidence, &syncedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TrackLink{}, false, nil
		}
		return TrackLink{}, false, err
	}
	l.Provisional = prov != 0
	l.SyncedAt, _ = time.Parse(time.RFC3339, syncedAt)
	return l, true, nil
}

// SaveTrackLink upserts a resolved track link (nil-safe). synced_at is stamped now if zero.
func (d *DB) SaveTrackLink(l TrackLink) error {
	if d == nil || d.db == nil || l.TrackHash == "" || l.TrackID == "" {
		return nil
	}
	at := l.SyncedAt
	if at.IsZero() {
		at = time.Now()
	}
	prov := 0
	if l.Provisional {
		prov = 1
	}
	_, err := d.db.Exec(`
		INSERT INTO track_links (track_hash, track_id, provisional, isrc, confidence, synced_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(track_hash) DO UPDATE SET
		  track_id=excluded.track_id, provisional=excluded.provisional,
		  isrc=excluded.isrc, confidence=excluded.confidence, synced_at=excluded.synced_at`,
		l.TrackHash, l.TrackID, prov, nullIfEmpty(l.ISRC), l.Confidence, at.UTC().Format(time.RFC3339))
	return err
}

// SetUpload records that a recorded set was published to the backend as a stream.
type SetUpload struct {
	RecordingID string
	StreamID    string
	TrackCount  int
	UploadedAt  time.Time
}

// GetSetUpload returns the upload record for a recording, ok=false if not yet uploaded.
func (d *DB) GetSetUpload(recordingID string) (SetUpload, bool, error) {
	if d == nil || d.db == nil || recordingID == "" {
		return SetUpload{}, false, nil
	}
	var s SetUpload
	var at string
	err := d.db.QueryRow(`
		SELECT recording_id, stream_id, track_count, uploaded_at
		  FROM set_uploads WHERE recording_id=?`, recordingID).
		Scan(&s.RecordingID, &s.StreamID, &s.TrackCount, &at)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SetUpload{}, false, nil
		}
		return SetUpload{}, false, err
	}
	s.UploadedAt, _ = time.Parse(time.RFC3339, at)
	return s, true, nil
}

// SaveSetUpload upserts a recorded-set upload record (nil-safe).
func (d *DB) SaveSetUpload(s SetUpload) error {
	if d == nil || d.db == nil || s.RecordingID == "" || s.StreamID == "" {
		return nil
	}
	at := s.UploadedAt
	if at.IsZero() {
		at = time.Now()
	}
	_, err := d.db.Exec(`
		INSERT INTO set_uploads (recording_id, stream_id, track_count, uploaded_at)
		VALUES (?,?,?,?)
		ON CONFLICT(recording_id) DO UPDATE SET
		  stream_id=excluded.stream_id, track_count=excluded.track_count, uploaded_at=excluded.uploaded_at`,
		s.RecordingID, s.StreamID, s.TrackCount, at.UTC().Format(time.RFC3339))
	return err
}

// LibrarySyncHashes returns track_hash → payload_hash for every library-synced track (the
// uploader's skip-unchanged ledger). Empty map when none.
func (d *DB) LibrarySyncHashes() (map[string]string, error) {
	out := map[string]string{}
	if d == nil || d.db == nil {
		return out, nil
	}
	rows, err := d.db.Query(`SELECT track_hash, payload_hash FROM library_sync`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var h, p string
		if err := rows.Scan(&h, &p); err != nil {
			return nil, err
		}
		out[h] = p
	}
	return out, rows.Err()
}

// LibrarySyncRow is one library_sync ledger entry to upsert.
type LibrarySyncRow struct {
	PayloadHash    string
	LibraryTrackID string // server lib_… row id; "" preserves any stored value
}

// SaveLibrarySyncBatch upserts synced-track ledger rows in one tx (nil-safe, no-op on empty).
// An empty LibraryTrackID never clobbers a stored one.
func (d *DB) SaveLibrarySyncBatch(rows map[string]LibrarySyncRow) error {
	if d == nil || d.db == nil || len(rows) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for h, r := range rows {
		if h == "" || r.PayloadHash == "" {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO library_sync (track_hash, payload_hash, library_track_id, synced_at) VALUES (?,?,?,?)
			ON CONFLICT(track_hash) DO UPDATE SET
			  payload_hash=excluded.payload_hash,
			  library_track_id=COALESCE(NULLIF(excluded.library_track_id,''), library_track_id),
			  synced_at=excluded.synced_at`, h, r.PayloadHash, nullIfEmpty(r.LibraryTrackID), now); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// LibraryTrackIDs returns track_hash → server library_track_id (lib_…) for every ledger row
// that has one. Empty map when none.
func (d *DB) LibraryTrackIDs() (map[string]string, error) {
	out := map[string]string{}
	if d == nil || d.db == nil {
		return out, nil
	}
	rows, err := d.db.Query(`SELECT track_hash, library_track_id FROM library_sync
		WHERE library_track_id IS NOT NULL AND library_track_id != ''`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var h, id string
		if err := rows.Scan(&h, &id); err != nil {
			return nil, err
		}
		out[h] = id
	}
	return out, rows.Err()
}

// SaveLibraryTrackIDs backfills track_hash → library_track_id mappings (e.g. matched from a
// GET /library pull for libraries synced before lib ids were persisted). Inserts a placeholder
// ledger row (empty payload_hash → next SyncLibrary still uploads metadata) when absent.
func (d *DB) SaveLibraryTrackIDs(ids map[string]string) error {
	if d == nil || d.db == nil || len(ids) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for h, id := range ids {
		if h == "" || id == "" {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO library_sync (track_hash, payload_hash, library_track_id, synced_at) VALUES (?,'',?,?)
			ON CONFLICT(track_hash) DO UPDATE SET library_track_id=excluded.library_track_id`,
			h, id, now); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// MediaSyncRow is the per-track waveform/artwork upload state ("" = not yet uploaded).
type MediaSyncRow struct {
	WaveformHash string
	ArtworkHash  string
}

// MediaSyncRows returns track_hash → media upload state for every ledger row.
func (d *DB) MediaSyncRows() (map[string]MediaSyncRow, error) {
	out := map[string]MediaSyncRow{}
	if d == nil || d.db == nil {
		return out, nil
	}
	rows, err := d.db.Query(`SELECT track_hash, COALESCE(waveform_hash,''), COALESCE(artwork_hash,'') FROM media_sync`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var h string
		var r MediaSyncRow
		if err := rows.Scan(&h, &r.WaveformHash, &r.ArtworkHash); err != nil {
			return nil, err
		}
		out[h] = r
	}
	return out, rows.Err()
}

// SaveMediaSync upserts one track's media upload state. Empty waveform/artwork hashes preserve
// the stored values, so waveform and artwork progress independently.
func (d *DB) SaveMediaSync(trackHash, waveformHash, artworkHash string) error {
	if d == nil || d.db == nil || trackHash == "" {
		return nil
	}
	_, err := d.db.Exec(`
		INSERT INTO media_sync (track_hash, waveform_hash, artwork_hash, synced_at) VALUES (?,?,?,?)
		ON CONFLICT(track_hash) DO UPDATE SET
		  waveform_hash=COALESCE(NULLIF(excluded.waveform_hash,''), waveform_hash),
		  artwork_hash=COALESCE(NULLIF(excluded.artwork_hash,''), artwork_hash),
		  synced_at=excluded.synced_at`,
		trackHash, nullIfEmpty(waveformHash), nullIfEmpty(artworkHash), time.Now().UTC().Format(time.RFC3339))
	return err
}

// FingerprintForTrack returns the most recent Chromaprint recorded for a track identity (from
// the change_log fingerprint events written by internal/setfp), ok=false if none.
func (d *DB) FingerprintForTrack(trackHash string) (string, bool, error) {
	ev, ok, err := d.LatestChange(trackHash, "fingerprint")
	if err != nil || !ok {
		return "", false, err
	}
	if ev.TrackFP != "" {
		return ev.TrackFP, true, nil
	}
	return "", false, nil
}
