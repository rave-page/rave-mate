package libdb

import "time"

// PlayedTrack is one confirmed-played track in the consolidated play log: the fused
// now-playing identity (from any input) with absolute start/end times and the deck it
// played on. It mirrors the recorder's Track but is the durable, queryable DB record -
// keyed by a stable ID so the capture-end update overwrites the in-progress insert.
type PlayedTrack struct {
	ID          string    `json:"id"`
	RecordingID string    `json:"recordingId,omitempty"`
	Artist      string    `json:"artist,omitempty"`
	Title       string    `json:"title"`
	Album       string    `json:"album,omitempty"`
	Key         string    `json:"key,omitempty"`
	BPM         float64   `json:"bpm,omitempty"`
	Deck        string    `json:"deck,omitempty"`
	TitleSource string    `json:"titleSource,omitempty"`
	StartedAt   time.Time `json:"startedAt"`
	EndedAt     time.Time `json:"endedAt,omitzero"`
}

// SavePlayedTrack upserts a played-track row (nil-safe). Keyed by ID so a later call with
// the final ended_at / late-arriving metadata overwrites the confirm-time insert.
func (d *DB) SavePlayedTrack(p PlayedTrack) error {
	if d == nil || d.db == nil || p.ID == "" {
		return nil
	}
	var ended string
	if !p.EndedAt.IsZero() {
		ended = p.EndedAt.UTC().Format(time.RFC3339)
	}
	_, err := d.db.Exec(`
		INSERT INTO played_tracks
		  (id, recording_id, artist, title, album, key, bpm, deck, title_source, started_at, ended_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
		  recording_id=excluded.recording_id, artist=excluded.artist, title=excluded.title,
		  album=excluded.album, key=excluded.key, bpm=excluded.bpm, deck=excluded.deck,
		  title_source=excluded.title_source, ended_at=excluded.ended_at`,
		p.ID, p.RecordingID, p.Artist, p.Title, p.Album, p.Key, p.BPM, p.Deck, p.TitleSource,
		p.StartedAt.UTC().Format(time.RFC3339), ended)
	return err
}

// PlayedTracksFor returns the played tracks linked to a recording, in play order (nil-safe).
func (d *DB) PlayedTracksFor(recordingID string) ([]PlayedTrack, error) {
	if d == nil || d.db == nil || recordingID == "" {
		return nil, nil
	}
	return d.queryPlayedTracks(`WHERE recording_id=? ORDER BY started_at ASC`, recordingID)
}

// ListPlayedTracks returns recent played tracks, newest first (nil-safe).
func (d *DB) ListPlayedTracks(limit int) ([]PlayedTrack, error) {
	if d == nil || d.db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	return d.queryPlayedTracks(`ORDER BY started_at DESC LIMIT ?`, limit)
}

func (d *DB) queryPlayedTracks(where string, args ...any) ([]PlayedTrack, error) {
	rows, err := d.db.Query(`
		SELECT id, COALESCE(recording_id,''), COALESCE(artist,''), title, COALESCE(album,''),
		       COALESCE(key,''), COALESCE(bpm,0), COALESCE(deck,''), COALESCE(title_source,''),
		       started_at, COALESCE(ended_at,'')
		  FROM played_tracks `+where, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []PlayedTrack
	for rows.Next() {
		var p PlayedTrack
		var started, ended string
		if err := rows.Scan(&p.ID, &p.RecordingID, &p.Artist, &p.Title, &p.Album, &p.Key,
			&p.BPM, &p.Deck, &p.TitleSource, &started, &ended); err != nil {
			return nil, err
		}
		p.StartedAt, _ = time.Parse(time.RFC3339, started)
		if ended != "" {
			p.EndedAt, _ = time.Parse(time.RFC3339, ended)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
