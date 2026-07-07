package libdb

import "time"

// SetRecording capture kinds.
const (
	SetKindIcecast = "icecast" // audio broadcast captured by the local Icecast receiver
	SetKindOBS     = "obs"     // finished OBS recording (usually video)
	SetKindNative  = "native"  // native ffmpeg audio-device capture (FLAC/WAV/MP3/AAC)
)

// SetRecording is one captured set file - Icecast audio broadcast or a finished OBS
// recording - time-linked to a recorder Recording via RecordingID. StartedAt is media t=0,
// so a track's offset into the file = track.StartedAt − StartedAt.
type SetRecording struct {
	ID          string    `json:"id"`
	RecordingID string    `json:"recordingId,omitempty"`
	Path        string    `json:"path"`
	Format      string    `json:"format,omitempty"`
	Mount       string    `json:"mount,omitempty"`
	Kind        string    `json:"kind,omitempty"` // SetKindIcecast (default) | SetKindOBS
	StartedAt   time.Time `json:"startedAt"`
	EndedAt     time.Time `json:"endedAt,omitzero"`
	Bytes       int64     `json:"bytes"`
}

// SaveSetRecording upserts a captured-set row (nil-safe). Keyed by ID so the capture-end
// update overwrites the capture-start insert with final ended_at/bytes.
func (d *DB) SaveSetRecording(s SetRecording) error {
	if d == nil || d.db == nil {
		return nil
	}
	var ended string
	if !s.EndedAt.IsZero() {
		ended = s.EndedAt.UTC().Format(time.RFC3339)
	}
	if s.Kind == "" {
		s.Kind = SetKindIcecast
	}
	_, err := d.db.Exec(`
		INSERT INTO set_recordings (id, recording_id, path, format, mount, kind, started_at, ended_at, bytes)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
		  recording_id=excluded.recording_id, path=excluded.path, format=excluded.format,
		  mount=excluded.mount, kind=excluded.kind, ended_at=excluded.ended_at, bytes=excluded.bytes`,
		s.ID, s.RecordingID, s.Path, s.Format, s.Mount, s.Kind,
		s.StartedAt.UTC().Format(time.RFC3339), ended, s.Bytes)
	return err
}

// SetRecordingsFor returns captured-set files linked to a recording, newest first (nil-safe).
func (d *DB) SetRecordingsFor(recordingID string) ([]SetRecording, error) {
	if d == nil || d.db == nil || recordingID == "" {
		return nil, nil
	}
	return d.querySetRecordings(`WHERE recording_id=? ORDER BY started_at DESC`, recordingID)
}

// ListSetRecordings returns recent captured-set files, newest first (nil-safe).
func (d *DB) ListSetRecordings(limit int) ([]SetRecording, error) {
	if d == nil || d.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	return d.querySetRecordings(`ORDER BY started_at DESC LIMIT ?`, limit)
}

// UnlinkedSetRecordings returns finished captures with no recording link yet (nil-safe) -
// re-link candidates once an overlapping recording finalizes.
func (d *DB) UnlinkedSetRecordings() ([]SetRecording, error) {
	if d == nil || d.db == nil {
		return nil, nil
	}
	return d.querySetRecordings(
		`WHERE COALESCE(recording_id,'')='' AND COALESCE(ended_at,'')!='' ORDER BY started_at DESC LIMIT 50`)
}

// RelinkSetRecording sets the recording link on one capture row (nil-safe).
func (d *DB) RelinkSetRecording(id, recordingID string) error {
	if d == nil || d.db == nil {
		return nil
	}
	_, err := d.db.Exec(`UPDATE set_recordings SET recording_id=? WHERE id=?`, recordingID, id)
	return err
}

// DeleteSetRecording removes one capture row (nil-safe). The file on disk is the caller's
// to remove (UI confirms first).
func (d *DB) DeleteSetRecording(id string) error {
	if d == nil || d.db == nil {
		return nil
	}
	_, err := d.db.Exec(`DELETE FROM set_recordings WHERE id=?`, id)
	return err
}

func (d *DB) querySetRecordings(where string, args ...any) ([]SetRecording, error) {
	rows, err := d.db.Query(`
		SELECT id, COALESCE(recording_id,''), path, COALESCE(format,''), COALESCE(mount,''),
		       COALESCE(kind,'icecast'), started_at, COALESCE(ended_at,''), bytes
		  FROM set_recordings `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []SetRecording
	for rows.Next() {
		var s SetRecording
		var started, ended string
		if err := rows.Scan(&s.ID, &s.RecordingID, &s.Path, &s.Format, &s.Mount, &s.Kind, &started, &ended, &s.Bytes); err != nil {
			return nil, err
		}
		s.StartedAt, _ = time.Parse(time.RFC3339, started)
		if ended != "" {
			s.EndedAt, _ = time.Parse(time.RFC3339, ended)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
