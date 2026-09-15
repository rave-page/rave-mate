// Package mixxx reads Mixxx's SQLite library DB (mixxxdb.sqlite) for now-playing. Mixxx logs
// each played track into a "set-log"/history playlist, so the newest entry in a play-event
// playlist (Auto-DJ hidden=1 / set-log hidden=2) is the most-recently-played track. Mixxx's DB
// carries NO per-deck state, so this yields MASTER-scope now-playing only (documented honestly;
// no per-deck faking). Pure Go via modernc.org/sqlite; read-only + WAL-live-lock tolerant
// (copies the db + sidecars on a busy DB), mirroring internal/serato/history4.go. Set-log query
// technique adopted from erikrichardlarson/unbox. Reads defensively: a missing table/column just
// omits that field.
package mixxx

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// NowPlaying is the newest play-event track read from mixxxdb.sqlite (master scope - Mixxx's DB
// exposes no per-deck data).
type NowPlaying struct {
	Artist, Title, Album, Genre, Key, Path string
	BPM                                    float64
	DurationSec                            int
}

// DefaultDBPath resolves mixxxdb.sqlite per-OS: Windows %LOCALAPPDATA%\Mixxx; macOS the sandboxed
// container path then the standard Application Support path (prefers whichever exists, sandboxed
// first); Linux ~/.mixxx then ~/.config/Mixxx. Prefers an existing candidate; else returns the
// primary default (even if absent) so the caller's existence check drives liveness. Errors only
// when the home/env base is unresolvable or the OS is unsupported.
func DefaultDBPath() (string, error) {
	cands, primary, err := dbCandidates()
	if err != nil {
		return "", err
	}
	for _, c := range cands {
		if fi, serr := os.Stat(c); serr == nil && !fi.IsDir() {
			return c, nil
		}
	}
	return primary, nil // primary default (may be absent)
}

// dbCandidates returns the per-OS mixxxdb.sqlite paths to probe (preference order) and the
// primary default to return when none exists. macOS probes the sandboxed container first but
// defaults to the standard Application Support path (the common, non-sandboxed install).
func dbCandidates() (cands []string, primary string, err error) {
	switch runtime.GOOS {
	case "windows":
		la := os.Getenv("LOCALAPPDATA")
		if la == "" {
			return nil, "", errors.New("mixxx db: LOCALAPPDATA unset")
		}
		p := filepath.Join(la, "Mixxx", "mixxxdb.sqlite")
		return []string{p}, p, nil
	case "darwin":
		home, herr := os.UserHomeDir()
		if herr != nil {
			return nil, "", herr
		}
		sandbox := filepath.Join(home, "Library", "Containers", "org.mixxx.mixxx", "Data", "Library", "Application Support", "Mixxx", "mixxxdb.sqlite")
		std := filepath.Join(home, "Library", "Application Support", "Mixxx", "mixxxdb.sqlite")
		return []string{sandbox, std}, std, nil
	case "linux":
		home, herr := os.UserHomeDir()
		if herr != nil {
			return nil, "", herr
		}
		classic := filepath.Join(home, ".mixxx", "mixxxdb.sqlite")
		xdg := filepath.Join(home, ".config", "Mixxx", "mixxxdb.sqlite")
		return []string{classic, xdg}, classic, nil
	default:
		return nil, "", fmt.Errorf("mixxx db: unsupported OS %q", runtime.GOOS)
	}
}

// HasDB reports whether path is set and names an existing file.
func HasDB(path string) bool {
	if path == "" {
		return false
	}
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// ReadNowPlaying returns the newest play-event track from mixxxdb.sqlite (ok=false when the
// library has no play history yet). Prefers a play-event playlist (Auto-DJ hidden=1 / set-log
// hidden=2) so a track merely dragged into a normal playlist isn't mistaken for "playing"; falls
// back to the plain newest PlaylistTracks row on an older schema. Enriches from library (+
// track_locations for the path) defensively - a missing column just omits that field. Opens
// read-only and tolerates Mixxx's live WAL lock: on a lock/busy error it reads a private copy of
// the db + -wal/-shm sidecars (mirrors internal/serato/history4.go). Never writes the DB.
func ReadNowPlaying(path string) (*NowPlaying, bool, error) {
	if path == "" {
		return nil, false, errors.New("mixxx db: empty path")
	}
	// Primary: read the live DB read-only. Unlike Serato 4 (always WAL), Mixxx's DB is usually in
	// the default rollback journal mode, so we do NOT force journal_mode(wal) here - forcing WAL on
	// a non-WAL DB makes a read-only connection see no rows. busy_timeout waits out a transient
	// writer lock before failing over to the copy.
	dsn := "file:" + path + "?mode=ro&_pragma=busy_timeout(2000)"
	np, ok, err := readDB(dsn)
	if err == nil {
		return np, ok, nil
	}
	np, ok, cerr := readCopy(path)
	if cerr != nil {
		return nil, false, fmt.Errorf("mixxx db: read failed (direct: %v; copy: %w)", err, cerr)
	}
	return np, ok, nil
}

// readCopy copies mixxxdb.sqlite + its -wal/-shm sidecars to a private temp dir (removed on
// return) and reads the copy read-only. Fallback for a live-locked DB. Sidecars are best-effort
// (absent when Mixxx has checkpointed / isn't running); the copy DSN drops journal_mode so a
// non-WAL copy still reads read-only.
func readCopy(path string) (*NowPlaying, bool, error) {
	tmp, err := os.MkdirTemp("", "rave-mixxx-*")
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	dst := filepath.Join(tmp, "mixxxdb.sqlite")
	if err := copyFile(path, dst); err != nil {
		return nil, false, err
	}
	for _, s := range []string{"-wal", "-shm"} {
		_ = copyFile(path+s, dst+s) // best-effort: absence is fine (checkpointed DB)
	}
	return readDB("file:" + dst + "?mode=ro&_pragma=busy_timeout(2000)")
}

// readDB opens dsn (a modernc sqlite DSN) and reads the newest play-event track, enriched.
func readDB(dsn string) (*NowPlaying, bool, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1) // single read-only conn, reused across the play + enrichment reads

	trackID, ok, err := latestPlayedTrackID(db)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil // no play history yet
	}
	np := enrichTrack(db, trackID)
	if np.Title == "" && np.Artist == "" && np.Path == "" {
		return nil, false, nil // play row references a vanished library entry: nothing to show
	}
	return np, true, nil
}

// latestPlayedTrackID returns the track_id of the newest play-event PlaylistTracks row. Prefers
// Auto-DJ (hidden=1) + set-log (hidden=2) playlists; on an older schema without Playlists.hidden
// (or with no play-event rows) it falls back to the plain newest PlaylistTracks row.
func latestPlayedTrackID(db *sql.DB) (int64, bool, error) {
	if !tableExists(db, "PlaylistTracks") {
		return 0, false, nil
	}
	ptCols := tableColumns(db, "PlaylistTracks")
	trackCol := firstCol(ptCols, "track_id")
	if trackCol == "" {
		return 0, false, nil
	}
	orderCol := firstCol(ptCols, "pl_datetime_added", "position")
	if orderCol == "" {
		orderCol = "rowid" // always present on a rowid table
	}

	// Prefer play-event playlists so a track merely dragged into a normal playlist isn't a "play".
	if tableExists(db, "Playlists") && tableColumns(db, "Playlists")["hidden"] {
		q := "SELECT pt." + quote(trackCol) + " FROM PlaylistTracks pt" +
			" JOIN Playlists pl ON pl.id = pt.playlist_id" +
			" WHERE pl.hidden IN (1,2)" +
			" ORDER BY pt." + quote(orderCol) + " DESC LIMIT 1"
		var id int64
		err := db.QueryRow(q).Scan(&id)
		if err == nil {
			return id, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, false, err
		}
		// no play-event rows: fall through to the plain query (older/empty set-log)
	}

	q := "SELECT " + quote(trackCol) + " FROM PlaylistTracks ORDER BY " + quote(orderCol) + " DESC LIMIT 1"
	var id int64
	err := db.QueryRow(q).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// enrichTrack fills metadata from library (+ track_locations for the path) for one track id.
// Defensive: absent tables/columns are skipped, so a partial schema yields a partial NowPlaying.
func enrichTrack(db *sql.DB, trackID int64) *NowPlaying {
	np := &NowPlaying{}
	if !tableExists(db, "library") {
		return np
	}
	cols := tableColumns(db, "library")

	var (
		sel    []string
		strDst []*string
	)
	addStr := func(col string, dst *string) {
		if cols[col] {
			sel = append(sel, quote(col))
			strDst = append(strDst, dst)
		}
	}
	addStr("artist", &np.Artist)
	addStr("title", &np.Title)
	addStr("album", &np.Album)
	addStr("genre", &np.Genre)
	addStr("key", &np.Key)

	bpmIdx, durIdx, locIdx := -1, -1, -1
	if cols["bpm"] {
		bpmIdx = len(sel)
		sel = append(sel, quote("bpm"))
	}
	if cols["duration"] {
		durIdx = len(sel)
		sel = append(sel, quote("duration"))
	}
	if cols["location"] {
		locIdx = len(sel)
		sel = append(sel, quote("location"))
	}
	if len(sel) == 0 {
		return np
	}

	strVals := make([]sql.NullString, len(strDst))
	scan := make([]any, len(sel))
	for i := range strDst {
		scan[i] = &strVals[i]
	}
	var bpm, dur sql.NullFloat64
	var loc sql.NullInt64
	if bpmIdx >= 0 {
		scan[bpmIdx] = &bpm
	}
	if durIdx >= 0 {
		scan[durIdx] = &dur
	}
	if locIdx >= 0 {
		scan[locIdx] = &loc
	}

	q := "SELECT " + strings.Join(sel, ", ") + " FROM library WHERE id = ? LIMIT 1"
	if err := db.QueryRow(q, trackID).Scan(scan...); err != nil {
		return np // row gone / scan mismatch: return what we have
	}
	for i, dst := range strDst {
		*dst = strings.TrimSpace(strVals[i].String)
	}
	if bpm.Valid {
		np.BPM = bpm.Float64
	}
	if dur.Valid {
		np.DurationSec = int(dur.Float64)
	}
	if locIdx >= 0 && loc.Valid {
		np.Path = resolvePath(db, loc.Int64)
	}
	return np
}

// resolvePath resolves a library.location FK id to the filesystem path via track_locations.
// "" when the table/column is absent or the row is missing.
func resolvePath(db *sql.DB, locID int64) string {
	if !tableExists(db, "track_locations") {
		return ""
	}
	col := firstCol(tableColumns(db, "track_locations"), "location")
	if col == "" {
		return ""
	}
	var p sql.NullString
	if err := db.QueryRow("SELECT "+quote(col)+" FROM track_locations WHERE id = ? LIMIT 1", locID).Scan(&p); err != nil {
		return ""
	}
	return strings.TrimSpace(p.String)
}

// ── small SQL helpers (live snapshot is a plain SQLite file) ─────────────────

// tableExists reports whether a table of the given name is present.
func tableExists(db *sql.DB, name string) bool {
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return n > 0
}

// tableColumns returns the set of column names of table (empty on any error).
func tableColumns(db *sql.DB, table string) map[string]bool {
	cols := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return cols
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil {
			cols[c] = true
		}
	}
	_ = rows.Err()
	return cols
}

// firstCol returns the first of names present in cols ("" if none).
func firstCol(cols map[string]bool, names ...string) string {
	for _, n := range names {
		if cols[n] {
			return n
		}
	}
	return ""
}

// quote wraps a SQLite identifier in double quotes (escaping embedded quotes) so a column name
// that is also a keyword (e.g. "key") is safe in a built query. Names come from the DB schema.
func quote(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
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
