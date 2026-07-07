package rekordboxsrc

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"rave.page/mate/internal/rekordboxdb"
	"rave.page/mate/internal/session"
)

const dbPollInterval = 3 * time.Second

// runDBPoll polls master.db's djmdSongHistory for the newest play every ~3s and emits a
// master-scope observation. Transparent: this is RECENTLY-PLAYED, not real-time - the lag is
// rekordbox's "Playback time setting" (Preferences → Advanced → Browse), how long a track must
// play before it's logged to History. Default ~60s; the user can drop it to 1–10s. Re-decrypts
// only when the source file's mtime
// changes (a played track appends a history row + rewrites the DB). Locked/contended DB is
// logged once and retried next tick (never fatal).
func (s *Source) runDBPoll(ctx context.Context, emit func(session.Observation)) {
	path, err := s.dbPath()
	if err != nil {
		s.log.Warn(logSource, "db poll disabled: "+err.Error(), nil)
		return
	}
	s.log.Info(logSource, "rekordbox master.db poll started (recently-played, ~60s lag)", map[string]any{"path": path})

	var (
		gate    onceLog
		lastMod time.Time
		lastKey string // title|artist of the last emitted play (Loaded boundary)
	)
	tick := time.NewTicker(dbPollInterval)
	defer tick.Stop()
	for {
		// Skip decrypt when the DB hasn't changed since last successful read.
		if fi, statErr := os.Stat(path); statErr == nil {
			if !fi.ModTime().After(lastMod) && !lastMod.IsZero() {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					continue
				}
			}
		}

		p, ok := s.queryLatestPlay(path, &gate)
		if ok {
			if fi, statErr := os.Stat(path); statErr == nil {
				lastMod = fi.ModTime()
			}
			key := p.title + "\x00" + p.artist
			loaded := key != lastKey
			lastKey = key
			emit(playObservation(p, loaded))
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// latestPlay is the newest djmdSongHistory entry, resolved to display metadata.
type latestPlay struct {
	title, artist, key, path string
	bpm                      float64
}

// queryLatestPlay opens a decrypted snapshot, reads the newest play, and closes it. Errors are
// logged once (state-change gated) and surface as ok=false.
func (s *Source) queryLatestPlay(path string, gate *onceLog) (latestPlay, bool) {
	db, cleanup, err := rekordboxdb.OpenDecrypted(path, s.cfg.DBKey)
	if err != nil {
		if gate.changed(err.Error()) {
			s.log.Warn(logSource, "master.db locked/unreadable (retrying): "+err.Error(), nil)
		}
		return latestPlay{}, false
	}
	defer func() { _ = cleanup() }()

	if !tableExists(db, "djmdSongHistory") || !tableExists(db, "djmdContent") {
		if gate.changed("no-history") {
			s.log.Warn(logSource, "master.db has no play history tables", nil)
		}
		return latestPlay{}, false
	}
	cols := tableColumns(db, "djmdContent")
	pathCol := firstCol(cols, "FolderPath", "Filepath", "FilePath")
	sel := []string{"c.Title", "ar.Name", "c.BPM", "k.ScaleName"}
	if pathCol != "" {
		sel = append(sel, "c."+pathCol)
	}
	// Newest play = highest rowid in djmdSongHistory (rows append as tracks play).
	q := "SELECT " + strings.Join(sel, ", ") + ` FROM djmdSongHistory sh
		JOIN djmdContent c ON c.ID = sh.ContentID
		LEFT JOIN djmdArtist ar ON ar.ID = c.ArtistID
		LEFT JOIN djmdKey k ON k.ID = c.KeyID
		ORDER BY sh.rowid DESC LIMIT 1`

	dst := make([]sql.NullString, len(sel))
	scan := make([]any, len(sel))
	for i := range dst {
		scan[i] = &dst[i]
	}
	if err := db.QueryRow(q).Scan(scan...); err != nil {
		if err != sql.ErrNoRows && gate.changed(err.Error()) {
			s.log.Warn(logSource, "history query failed: "+err.Error(), nil)
		}
		return latestPlay{}, false
	}
	gate.changed("") // recovered

	get := func(i int) string {
		if i < len(dst) && dst[i].Valid {
			return strings.TrimSpace(dst[i].String)
		}
		return ""
	}
	p := latestPlay{title: get(0), artist: get(1), key: get(3), bpm: centiBPM(get(2))}
	if pathCol != "" {
		p.path = filepath.FromSlash(get(4))
	}
	if p.title == "" && p.artist == "" {
		return latestPlay{}, false
	}
	return p, true
}

// playObservation builds the master-scope Observation for a play.
func playObservation(p latestPlay, loaded bool) session.Observation {
	fields := map[string]any{}
	if p.title != "" {
		fields[session.FieldTitle] = p.title
	}
	if p.artist != "" {
		fields[session.FieldArtist] = p.artist
	}
	if p.key != "" {
		fields[session.FieldKey] = p.key
	}
	if p.path != "" {
		fields[session.FieldPath] = p.path
	}
	if p.bpm > 0 {
		fields[session.FieldBPM] = p.bpm
	}
	return session.Observation{
		Source:     session.SourceRekordboxDB,
		Scope:      session.Scope{Kind: session.ScopeMaster},
		Fields:     fields,
		Confidence: confDB,
		Loaded:     loaded,
	}
}

// dbPath resolves the configured path, else rekordbox's own options.json (authoritative - the path
// rekordbox actually uses, even a relocated library), else auto-detects the default master.db.
func (s *Source) dbPath() (string, error) {
	if s.cfg.DBPath != "" {
		return s.cfg.DBPath, nil
	}
	if p := optionsDBPath(); p != "" {
		return p, nil
	}
	dbs := rekordboxdb.DiscoverRekordboxMasterDB()
	if len(dbs) == 0 {
		return "", errNoDB
	}
	return dbs[0], nil
}

// optionsDBPath reads rekordboxAgent's options.json and returns the master.db it points at (the
// authoritative location). Format: {"options":[[key,value],…]}; the db lives in one pair's value
// (a master.db file or its dir). Iterates + stat-validates (more robust than assuming index 0).
// Technique adopted from erikrichardlarson/unbox (DB-poll, no process memory). "" on any failure.
func optionsDBPath() string {
	p := optionsJSONPath()
	if p == "" {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var nf struct {
		Options [][]any `json:"options"`
	}
	if json.Unmarshal(data, &nf) != nil {
		return ""
	}
	for _, pair := range nf.Options {
		if len(pair) < 2 {
			continue
		}
		s, ok := pair[1].(string)
		if !ok || s == "" {
			continue
		}
		cand := s
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
			cand = filepath.Join(cand, "master.db")
		}
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}
	return ""
}

// optionsJSONPath is rekordboxAgent's options.json location per OS (empty if unknown).
func optionsJSONPath() string {
	switch runtime.GOOS {
	case "windows":
		if d, err := os.UserConfigDir(); err == nil { // %APPDATA%
			return filepath.Join(d, "Pioneer", "rekordboxAgent", "storage", "options.json")
		}
	case "darwin":
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, "Library", "Application Support", "Pioneer", "rekordboxAgent", "storage", "options.json")
		}
	}
	return ""
}

var errNoDB = &noDBError{}

type noDBError struct{}

func (*noDBError) Error() string { return "no Rekordbox master.db found (set dbPath)" }

// ── small SQL helpers (live snapshot is a plain SQLite file) ─────────────────

func tableExists(db *sql.DB, name string) bool {
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return n > 0
}

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
	return cols
}

func firstCol(cols map[string]bool, names ...string) string {
	for _, n := range names {
		if cols[n] {
			return n
		}
	}
	return ""
}

// centiBPM converts rekordbox's BPM×100 integer string to real BPM.
func centiBPM(s string) float64 {
	if s == "" {
		return 0
	}
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f / 100
}
