package libdb

import (
	"encoding/json"
	"time"

	"rave.page/mate/internal/musiclib"
)

// SyncSessions replaces the stored play-history sessions for a source (history is small +
// append-only in practice, so a full replace is simplest + correct). Played entries persist
// as JSON; their StartedAt timestamps drive recording→tracklist matching.
func (d *DB) SyncSessions(sourceID int64, sessions []musiclib.Session) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE source_id=?`, sourceID); err != nil {
		_ = tx.Rollback()
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO sessions (source_id, name, started_at, played) VALUES (?,?,?,?)`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, s := range sessions {
		played, _ := json.Marshal(s.Played)
		var started string
		if !s.StartedAt.IsZero() {
			started = s.StartedAt.UTC().Format(time.RFC3339)
		}
		if _, err := stmt.Exec(sourceID, s.Name, started, string(played)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// LoadSessions returns the stored sessions for a source, newest first.
func (d *DB) LoadSessions(sourceID int64) ([]musiclib.Session, error) {
	rows, err := d.db.Query(
		`SELECT name, COALESCE(started_at,''), COALESCE(played,'') FROM sessions
		   WHERE source_id=? ORDER BY started_at DESC`, sourceID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []musiclib.Session
	for rows.Next() {
		var s musiclib.Session
		var started, played string
		if err := rows.Scan(&s.Name, &started, &played); err != nil {
			return nil, err
		}
		if started != "" {
			s.StartedAt, _ = time.Parse(time.RFC3339, started)
		}
		if played != "" {
			_ = json.Unmarshal([]byte(played), &s.Played)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
