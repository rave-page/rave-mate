package libdb

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"rave.page/mate/internal/wirecrypto"
)

// ChangeEvent is one append-only library-state mutation. Old/New are JSON-encoded scalars
// (or compact snapshots). For a track-field change, Field is the column and Op is "set";
// for a recorder confirmation, Field is "play_event" and Op is "play".
type ChangeEvent struct {
	ID        int64
	TS        string // RFC3339 UTC; filled at append time if empty
	NodeID    string
	Seq       int64
	TrackFP   string // Chromaprint when known (usually empty today)
	TrackHash string // portable identity - see TrackHash
	SourceID  int64  // 0 = unknown/none (stored NULL)
	Path      string
	Field     string
	Op        string
	OldValue  string // JSON
	NewValue  string // JSON
	Origin    string // import|tagsync|recorder|manual
	Reverted  bool
}

// TrackHash is the portable cross-machine track identity: b64url(sha256(lower(artist) |
// "\x00" | lower(title) | "\x00" | round(durationSec))). File paths differ per machine;
// this survives. durationSec may be 0 when unknown (e.g. recorder now-playing) - then the
// hash keys on artist|title only, which the recorder path accepts.
func TrackHash(artist, title string, durationSec float64) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(strings.TrimSpace(artist)))
	b.WriteByte(0)
	b.WriteString(strings.ToLower(strings.TrimSpace(title)))
	b.WriteByte(0)
	b.WriteString(fmt.Sprintf("%d", int64(math.Round(durationSec))))
	sum := sha256.Sum256([]byte(b.String()))
	return wirecrypto.EncB64url(sum[:])
}

func jsonStr(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// execer is satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// nextSeq returns this node's next monotonic change-log counter (single-writer DB).
func (d *DB) nextSeq(q interface {
	QueryRow(string, ...any) *sql.Row
}) (int64, error) {
	var max sql.NullInt64
	if err := q.QueryRow(`SELECT MAX(seq) FROM change_log WHERE node_id=?`, d.nodeID).Scan(&max); err != nil {
		return 0, err
	}
	return max.Int64 + 1, nil
}

// LibraryVersion returns a monotonic counter that advances on every library mutation
// (each change_log append bumps MAX(seq)). Callers version cached per-track resolutions by it
// so any collection/library change invalidates the cache. 0 when unavailable. Cheap: served by
// idx_change_log_seq(node_id, seq).
func (d *DB) LibraryVersion() int64 {
	if d == nil || d.db == nil {
		return 0
	}
	var max sql.NullInt64
	if err := d.db.QueryRow(`SELECT MAX(seq) FROM change_log WHERE node_id=?`, d.nodeID).Scan(&max); err != nil {
		return 0
	}
	return max.Int64
}

// appendTx writes events via ex, starting at startSeq (assigned per event). Used both for
// the standalone AppendChanges (own tx) and the import path (TrackSync's tx - atomic with
// the track writes).
func (d *DB) appendTx(ex execer, startSeq int64, events []ChangeEvent) error {
	now := time.Now().UTC().Format(time.RFC3339)
	seq := startSeq
	for _, e := range events {
		ts := e.TS
		if ts == "" {
			ts = now
		}
		var src any
		if e.SourceID != 0 {
			src = e.SourceID
		}
		var fp any
		if e.TrackFP != "" {
			fp = e.TrackFP
		}
		if _, err := ex.Exec(`
			INSERT INTO change_log
			  (ts, node_id, seq, track_fp, track_hash, source_id, path, field, op, old_value, new_value, origin, reverted)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,0)`,
			ts, d.nodeID, seq, fp, e.TrackHash, src, nullIfEmpty(e.Path),
			e.Field, e.Op, e.OldValue, e.NewValue, e.Origin); err != nil {
			return err
		}
		seq++
	}
	return nil
}

// AppendChanges appends events authored by this node in one transaction.
func (d *DB) AppendChanges(events []ChangeEvent) error {
	if d == nil || d.db == nil || len(events) == 0 {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	startSeq, err := d.nextSeq(tx)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := d.appendTx(tx, startSeq, events); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ChangesForTrack returns every event for a track identity, newest first.
func (d *DB) ChangesForTrack(trackHash string) ([]ChangeEvent, error) {
	if d == nil || d.db == nil {
		return nil, nil
	}
	rows, err := d.db.Query(`
		SELECT id, ts, node_id, seq, COALESCE(track_fp,''), track_hash, COALESCE(source_id,0),
		       COALESCE(path,''), field, op, COALESCE(old_value,''), COALESCE(new_value,''), origin, reverted
		FROM change_log WHERE track_hash=? ORDER BY id DESC`, trackHash)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanChanges(rows)
}

// LatestChange returns the most recent non-reverted event for (track, field).
func (d *DB) LatestChange(trackHash, field string) (ChangeEvent, bool, error) {
	if d == nil || d.db == nil {
		return ChangeEvent{}, false, nil
	}
	rows, err := d.db.Query(`
		SELECT id, ts, node_id, seq, COALESCE(track_fp,''), track_hash, COALESCE(source_id,0),
		       COALESCE(path,''), field, op, COALESCE(old_value,''), COALESCE(new_value,''), origin, reverted
		FROM change_log WHERE track_hash=? AND field=? AND reverted=0 ORDER BY id DESC LIMIT 1`, trackHash, field)
	if err != nil {
		return ChangeEvent{}, false, err
	}
	defer func() { _ = rows.Close() }()
	evs, err := scanChanges(rows)
	if err != nil || len(evs) == 0 {
		return ChangeEvent{}, false, err
	}
	return evs[0], true, nil
}

// RevertChange re-applies an event's old_value to the live tracks row and flags it
// reverted. Only "set" events on a known (source_id,path,field) are revertible; play/
// import events are markers and revert to a no-op write (still flagged). Both happen in
// one transaction.
func (d *DB) RevertChange(id int64) error {
	if d == nil || d.db == nil {
		return nil
	}
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	var (
		field, op, oldVal, srcPath string
		srcID                      sql.NullInt64
		reverted                   int
	)
	err = tx.QueryRow(`SELECT field, op, COALESCE(old_value,''), source_id, COALESCE(path,''), reverted
		FROM change_log WHERE id=?`, id).Scan(&field, &op, &oldVal, &srcID, &srcPath, &reverted)
	if err != nil {
		_ = tx.Rollback()
		if err == sql.ErrNoRows {
			return fmt.Errorf("revert: change %d not found", id)
		}
		return err
	}
	if reverted == 0 && op == "set" && srcID.Valid && srcPath != "" {
		if col, ok := revertColumn(field); ok {
			if err := applyOldValue(tx, col, oldVal, srcID.Int64, srcPath); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
	}
	if _, err := tx.Exec(`UPDATE change_log SET reverted=1 WHERE id=?`, id); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	d.bumpTracks() // a "set" revert wrote back a tracks row; reverts don't append change_log
	return nil
}

// revertColumn maps a change_log field to its tracks column (allowlist - no injection).
func revertColumn(field string) (string, bool) {
	switch field {
	case "play_count", "last_played", "rating", "bpm", "key", "genre", "comment", "cues", "beatgrid":
		return field, true
	}
	return "", false
}

// applyOldValue decodes a JSON-encoded old value and writes it back to the tracks column.
func applyOldValue(tx *sql.Tx, col, oldJSON string, sourceID int64, path string) error {
	var v any
	if oldJSON != "" {
		_ = json.Unmarshal([]byte(oldJSON), &v)
	}
	q := fmt.Sprintf(`UPDATE tracks SET %s=?, updated_at=? WHERE source_id=? AND path=?`, col)
	_, err := tx.Exec(q, v, time.Now().UTC().Format(time.RFC3339), sourceID, path)
	return err
}

func scanChanges(rows *sql.Rows) ([]ChangeEvent, error) {
	var out []ChangeEvent
	for rows.Next() {
		var e ChangeEvent
		var rev int
		if err := rows.Scan(&e.ID, &e.TS, &e.NodeID, &e.Seq, &e.TrackFP, &e.TrackHash,
			&e.SourceID, &e.Path, &e.Field, &e.Op, &e.OldValue, &e.NewValue, &e.Origin, &rev); err != nil {
			return nil, err
		}
		e.Reverted = rev != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
