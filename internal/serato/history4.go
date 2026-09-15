// Serato 4.x moved play history from the binary History\Sessions\*.session files into a
// SQLite database (master.sqlite): history_session + history_entry, with optional per-track
// metadata in asset. During a live set Serato writes each played track (and the currently
// loaded track, end_time=0/NULL) here instead of the old TLV files, so this is the only live
// now-playing source on Serato 4. Pure Go via modernc.org/sqlite; reads decode into the shared
// serato.Track. Read-only + WAL-live-lock tolerant (copies the db+sidecars on a busy DB).
package serato

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// DefaultV4DBPath resolves the Serato 4 master.sqlite path per-OS (Windows %LOCALAPPDATA%,
// macOS ~/Library/Application Support). Errors on any other OS.
func DefaultV4DBPath() (string, error) {
	switch runtime.GOOS {
	case "windows":
		la := os.Getenv("LOCALAPPDATA")
		if la == "" {
			return "", errors.New("serato 4 db: LOCALAPPDATA unset")
		}
		return filepath.Join(la, "Serato", "Library", "master.sqlite"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "Serato", "Library", "master.sqlite"), nil
	default:
		return "", fmt.Errorf("serato 4 db: unsupported OS %q", runtime.GOOS)
	}
}

// HasV4DB reports whether dbPath is set and names an existing file.
func HasV4DB(dbPath string) bool {
	if dbPath == "" {
		return false
	}
	fi, err := os.Stat(dbPath)
	return err == nil && !fi.IsDir()
}

// ReadLatestV4Session reads the newest history_session's entries from a Serato 4 master.sqlite
// into Track records (Path←file_name|portable_id, Title←name, Deck←parsed deck, Played←played==1,
// StartedAt/EndedAt←unix secs; Album/Genre/Key enriched from asset best-effort). Opens read-only
// (Serato keeps the DB open in WAL while live, so SQLite reads the -wal data without mutating it);
// on a lock/busy error it reads a private copy of the db + -wal/-shm sidecars instead. Never writes
// the user's DB. Empty slice (nil error) when no session exists yet.
func ReadLatestV4Session(dbPath string) ([]Track, error) {
	if dbPath == "" {
		return nil, errors.New("serato 4 db: empty path")
	}
	// Primary: read the live DB read-only. journal_mode(wal) matches the live DB's mode; the
	// real Serato 4 DB is always WAL so this is a no-op read, and busy_timeout waits out a
	// transient writer lock before failing over to the copy path.
	dsn := "file:" + dbPath + "?mode=ro&_pragma=busy_timeout(2000)&_pragma=journal_mode(wal)"
	tracks, err := readV4DB(dsn)
	if err == nil {
		return tracks, nil
	}
	tracks, cerr := readV4Copy(dbPath)
	if cerr != nil {
		return nil, fmt.Errorf("serato 4 db: read failed (direct: %v; copy: %w)", err, cerr)
	}
	return tracks, nil
}

// readV4Copy copies master.sqlite + its -wal/-shm sidecars to a private temp dir (removed on
// return) and reads the copy read-only. Fallback for a live-locked DB. Sidecars are best-effort
// (absent when Serato has checkpointed / isn't running); the copy DSN drops journal_mode so a
// non-WAL copy still reads read-only.
func readV4Copy(dbPath string) ([]Track, error) {
	tmp, err := os.MkdirTemp("", "rave-serato4-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	dst := filepath.Join(tmp, "master.sqlite")
	if err := copyFile(dbPath, dst); err != nil {
		return nil, err
	}
	for _, s := range []string{"-wal", "-shm"} {
		_ = copyFile(dbPath+s, dst+s) // best-effort: absence is fine (checkpointed DB)
	}
	return readV4DB("file:" + dst + "?mode=ro&_pragma=busy_timeout(2000)")
}

// readV4DB opens dsn (a modernc sqlite DSN) and reads the latest history session into Tracks.
func readV4DB(dsn string) ([]Track, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1) // single read-only conn, reused across the session + entry + asset reads

	var sessionID int64
	err = db.QueryRow(`SELECT id FROM history_session ORDER BY start_time DESC LIMIT 1`).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // no sessions yet
	}
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(
		`SELECT name, artist, deck, start_time, end_time, played, bpm, file_name, portable_id
		   FROM history_entry WHERE session_id = ? ORDER BY start_time ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Track
	var portableIDs []string
	for rows.Next() {
		var (
			name, artist, deck, fileName, portableID sql.NullString
			startTime, endTime, played               sql.NullInt64
			bpm                                      sql.NullFloat64
		)
		if err := rows.Scan(&name, &artist, &deck, &startTime, &endTime, &played, &bpm, &fileName, &portableID); err != nil {
			return nil, err
		}
		path := fileName.String
		if path == "" {
			path = portableID.String
		}
		out = append(out, Track{
			Path:      path,
			Title:     name.String,
			Artist:    artist.String,
			BPM:       bpm.Float64,
			Deck:      parseDeckValue(deck.String),
			Played:    played.Int64 == 1,
			StartedAt: startTime.Int64,
			EndedAt:   endTime.Int64, // 0 (or NULL→0) = still on the deck
		})
		portableIDs = append(portableIDs, portableID.String)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = rows.Close() // free the single conn before the asset lookups

	enrichV4Assets(db, out, portableIDs)
	return out, nil
}

// enrichV4Assets fills Album/Genre/Key from the asset table by portable_id. Best-effort: a
// missing table/column (older schema) fails the Prepare and returns silently; a per-row miss
// is skipped. Never fails the whole read.
func enrichV4Assets(db *sql.DB, tracks []Track, portableIDs []string) {
	stmt, err := db.Prepare(`SELECT album, genre, key FROM asset WHERE portable_id = ? LIMIT 1`)
	if err != nil {
		return // asset table/columns absent
	}
	defer func() { _ = stmt.Close() }()
	for i := range tracks {
		pid := portableIDs[i]
		if pid == "" {
			continue
		}
		var album, genre, key sql.NullString
		if err := stmt.QueryRow(pid).Scan(&album, &genre, &key); err != nil {
			continue // not found / scan mismatch: leave track's fields as-is
		}
		tracks[i].Album = album.String
		tracks[i].Genre = genre.String
		tracks[i].Key = key.String
	}
}

// parseDeckValue maps a Serato 4 deck value to a deck number: "1".."4"→1..4, "A".."D"→1..4
// (case-insensitive). Anything else → 0 (no deck; handled by the master fallback upstream).
func parseDeckValue(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if n, err := strconv.Atoi(s); err == nil {
		if n >= 1 && n <= 4 {
			return n
		}
		return 0
	}
	switch strings.ToUpper(s) {
	case "A":
		return 1
	case "B":
		return 2
	case "C":
		return 3
	case "D":
		return 4
	}
	return 0
}

// copyFile copies src to dst (used for the WAL live-lock fallback copy).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, in)
	if closeErr := out.Close(); cerr == nil {
		cerr = closeErr
	}
	return cerr
}
