package rekordboxdb

import (
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver

	"rave.page/mate/internal/musiclib"
)

// Probe cheaply verifies the resolved key can decrypt path's first page (one KDF + page-1
// HMAC; ~150ms). Lets the UI show real lock status without reading the whole DB every tick.
func Probe(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, scPageSize)
	if _, err := io.ReadFull(f, buf); err != nil {
		return err
	}
	_, err = decryptSQLCipher(buf, resolveKey(""))
	return err
}

// Open decrypts a Rekordbox master.db (SQLCipher-4) with the given passphrase ("" = default
// key, or RAVE_REKORDBOX_KEY) and reads it into the normalized model: tracks + playlists +
// play-history sessions (per-track timestamps from djmdSongHistory). The decrypted image is
// written to a private temp file and deleted before returning.
func Open(path, key string) (musiclib.Library, error) {
	key = resolveKey(key)
	data, err := os.ReadFile(path)
	if err != nil {
		return musiclib.Library{}, err
	}
	plain, err := decryptSQLCipher(data, key)
	if err != nil {
		return musiclib.Library{}, err
	}
	tmp, err := os.CreateTemp("", "rave-rbx-*.db")
	if err != nil {
		return musiclib.Library{}, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(plain); err != nil {
		_ = tmp.Close()
		return musiclib.Library{}, err
	}
	if err := tmp.Close(); err != nil {
		return musiclib.Library{}, err
	}

	db, err := sql.Open("sqlite", tmpName)
	if err != nil {
		return musiclib.Library{}, err
	}
	defer func() { _ = db.Close() }()

	lib := musiclib.Library{Source: musiclib.Source{App: "rekordbox", Path: path}}
	r := &reader{db: db}
	artists := r.names("djmdArtist", "Name")
	albums := r.names("djmdAlbum", "Name")
	genres := r.names("djmdGenre", "Name")
	labels := r.names("djmdLabel", "Name")
	keys := r.names("djmdKey", "ScaleName")

	trackPath := map[string]string{} // ContentID → path
	lib.Tracks = r.tracks(artists, albums, genres, labels, keys, trackPath)
	lib.Playlists = r.playlists(trackPath)
	lib.Sessions = r.histories(trackPath)
	return lib, r.err
}

type reader struct {
	db  *sql.DB
	err error // first non-fatal query error (kept for the caller's log)
}

// hasTable reports whether a table exists.
func (r *reader) hasTable(name string) bool {
	var n int
	_ = r.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	return n > 0
}

// columns returns the column set of a table.
func (r *reader) columns(table string) map[string]bool {
	cols := map[string]bool{}
	rows, err := r.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
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

// names builds an ID→name map from a simple Rekordbox lookup table (ID + a name column).
func (r *reader) names(table, nameCol string) map[string]string {
	m := map[string]string{}
	if !r.hasTable(table) {
		return m
	}
	rows, err := r.db.Query(`SELECT ID, ` + nameCol + ` FROM ` + table)
	if err != nil {
		r.note(err)
		return m
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, name sql.NullString
		if rows.Scan(&id, &name) == nil && id.Valid {
			m[id.String] = name.String
		}
	}
	return m
}

// tracks reads djmdContent, resolving FK names + recording ContentID→path for playlists/history.
func (r *reader) tracks(artists, albums, genres, labels, keys map[string]string, trackPath map[string]string) []musiclib.Track {
	if !r.hasTable("djmdContent") {
		return nil
	}
	cols := r.columns("djmdContent")
	has := func(c string) bool { return cols[c] }
	// path column varies across versions.
	pathCol := firstPresent(cols, "FolderPath", "Filepath", "FilePath")
	sel := []string{"ID", "Title", "ArtistID", "AlbumID", "GenreID", "LabelID", "KeyID",
		"BPM", "Length", "BitRate", "FileSize", "DJPlayCount", "Rating",
		"ReleaseDate", "DateCreated", "Commnt"}
	if pathCol != "" {
		sel = append(sel, pathCol)
	}
	present := make([]string, 0, len(sel))
	for _, c := range sel {
		if has(c) {
			present = append(present, c)
		}
	}
	q := `SELECT ` + strings.Join(present, ", ") + ` FROM djmdContent`
	if has("rb_local_deleted") {
		q += ` WHERE rb_local_deleted = 0`
	}
	rows, err := r.db.Query(q)
	if err != nil {
		r.note(err)
		return nil
	}
	defer func() { _ = rows.Close() }()

	idx := map[string]int{}
	for i, c := range present {
		idx[c] = i
	}
	var out []musiclib.Track
	for rows.Next() {
		vals := make([]any, len(present))
		holders := make([]sql.NullString, len(present))
		for i := range holders {
			vals[i] = &holders[i]
		}
		if rows.Scan(vals...) != nil {
			continue
		}
		get := func(c string) string {
			if i, ok := idx[c]; ok && holders[i].Valid {
				return holders[i].String
			}
			return ""
		}
		id := get("ID")
		if id == "" {
			continue
		}
		path := get(pathCol)
		t := musiclib.Track{
			Path:        filepath.FromSlash(path),
			Title:       get("Title"),
			Artist:      artists[get("ArtistID")],
			Album:       albums[get("AlbumID")],
			Genre:       genres[get("GenreID")],
			Label:       labels[get("LabelID")],
			Key:         keys[get("KeyID")],
			Comment:     get("Commnt"),
			BPM:         bpmFromCenti(get("BPM")),
			DurationSec: atof(get("Length")),
			BitrateBps:  atoi(get("BitRate")) * 1000,
			FileSizeKB:  atoi(get("FileSize")) / 1024,
			PlayCount:   atoi(get("DJPlayCount")),
			Rating:      ratingStars(atoi(get("Rating"))),
			ReleaseDate: get("ReleaseDate"),
			ImportDate:  get("DateCreated"),
		}
		if t.Path == "" && t.Title == "" {
			continue
		}
		out = append(out, t)
		trackPath[id] = t.Path
	}
	return out
}

// playlists reads djmdPlaylist (folders + lists) + djmdSongPlaylist (entries).
func (r *reader) playlists(trackPath map[string]string) []musiclib.Playlist {
	if !r.hasTable("djmdPlaylist") || !r.hasTable("djmdSongPlaylist") {
		return nil
	}
	type node struct {
		parent, name string
		isFolder     bool
	}
	nodes := map[string]node{}
	order := []string{}
	rows, err := r.db.Query(`SELECT ID, Name, ParentID, Attribute FROM djmdPlaylist`)
	if err != nil {
		r.note(err)
		return nil
	}
	for rows.Next() {
		var id, name, parent, attr sql.NullString
		if rows.Scan(&id, &name, &parent, &attr) != nil || !id.Valid {
			continue
		}
		// Attribute: 0 = playlist, 1 = folder, 4 = smart playlist.
		nodes[id.String] = node{parent: parent.String, name: name.String, isFolder: attr.String == "1"}
		order = append(order, id.String)
	}
	_ = rows.Close()

	type pe struct {
		seq     int
		content string
	}
	ent := map[string][]pe{}
	erows, err := r.db.Query(`SELECT PlaylistID, ContentID, TrackNo FROM djmdSongPlaylist`)
	if err == nil {
		for erows.Next() {
			var pid, cid, no sql.NullString
			if erows.Scan(&pid, &cid, &no) == nil && pid.Valid {
				ent[pid.String] = append(ent[pid.String], pe{seq: atoi(no.String), content: cid.String})
			}
		}
		_ = erows.Close()
	} else {
		r.note(err)
	}

	folderPath := func(id string) string {
		var segs []string
		seen := map[string]bool{}
		for p := nodes[id].parent; p != "" && p != "0" && !seen[p]; {
			seen[p] = true
			n, ok := nodes[p]
			if !ok {
				break
			}
			segs = append([]string{n.name}, segs...)
			p = n.parent
		}
		return strings.Join(segs, "/")
	}
	var out []musiclib.Playlist
	for _, id := range order {
		n := nodes[id]
		if n.isFolder {
			continue
		}
		es := ent[id]
		sort.SliceStable(es, func(i, j int) bool { return es[i].seq < es[j].seq })
		paths := make([]string, 0, len(es))
		for _, e := range es {
			if p := trackPath[e.content]; p != "" {
				paths = append(paths, p)
			}
		}
		out = append(out, musiclib.Playlist{Name: n.name, Folder: folderPath(id), Paths: paths})
	}
	return out
}

// histories reads djmdHistory (sessions) + djmdSongHistory (played tracks, with timestamps).
func (r *reader) histories(trackPath map[string]string) []musiclib.Session {
	if !r.hasTable("djmdHistory") || !r.hasTable("djmdSongHistory") {
		return nil
	}
	type hist struct {
		name    string
		created string
	}
	hs := map[string]hist{}
	order := []string{}
	rows, err := r.db.Query(`SELECT ID, Name, DateCreated FROM djmdHistory`)
	if err != nil {
		r.note(err)
		return nil
	}
	for rows.Next() {
		var id, name, created sql.NullString
		if rows.Scan(&id, &name, &created) == nil && id.Valid {
			hs[id.String] = hist{name: name.String, created: created.String}
			order = append(order, id.String)
		}
	}
	_ = rows.Close()

	shCols := r.columns("djmdSongHistory")
	tsCol := firstPresent(shCols, "created_at", "DateCreated") // per-track play time, if present
	type he struct {
		seq     int
		content string
		ts      string
	}
	ent := map[string][]he{}
	sel := "HistoryID, ContentID, TrackNo"
	if tsCol != "" {
		sel += ", " + tsCol
	}
	erows, err := r.db.Query(`SELECT ` + sel + ` FROM djmdSongHistory`)
	if err == nil {
		for erows.Next() {
			var hid, cid, no, ts sql.NullString
			dst := []any{&hid, &cid, &no}
			if tsCol != "" {
				dst = append(dst, &ts)
			}
			if erows.Scan(dst...) == nil && hid.Valid {
				ent[hid.String] = append(ent[hid.String], he{seq: atoi(no.String), content: cid.String, ts: ts.String})
			}
		}
		_ = erows.Close()
	} else {
		r.note(err)
	}

	var out []musiclib.Session
	for _, id := range order {
		es := ent[id]
		sort.SliceStable(es, func(i, j int) bool { return es[i].seq < es[j].seq })
		played := make([]musiclib.PlayedTrack, 0, len(es))
		for _, e := range es {
			p := trackPath[e.content]
			if p == "" {
				continue
			}
			played = append(played, musiclib.PlayedTrack{Path: p, StartedAt: parseRBTime(e.ts)})
		}
		if len(played) == 0 {
			continue
		}
		out = append(out, musiclib.Session{
			Name:      hs[id].name,
			Played:    played,
			StartedAt: parseRBTime(hs[id].created),
		})
	}
	return out
}

func (r *reader) note(err error) {
	if r.err == nil {
		r.err = err
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func firstPresent(cols map[string]bool, names ...string) string {
	for _, n := range names {
		if cols[n] {
			return n
		}
	}
	return ""
}

func atoi(s string) int     { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }
func atof(s string) float64 { f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64); return f }

// bpmFromCenti turns Rekordbox's BPM×100 integer into real BPM.
func bpmFromCenti(s string) float64 {
	if s == "" {
		return 0
	}
	return atof(s) / 100
}

// ratingStars normalizes a rating to 0-5 (Rekordbox stores 0,51,102,153,204,255 or 0-5).
func ratingStars(raw int) int {
	if raw > 5 {
		return raw / 51
	}
	return raw
}

var rbTimeLayouts = []string{
	"2006-01-02 15:04:05.999 -07:00",
	"2006-01-02 15:04:05.999-07:00",
	"2006-01-02 15:04:05 -07:00",
	"2006-01-02 15:04:05.999",
	"2006-01-02 15:04:05",
	time.RFC3339,
}

// parseRBTime parses a Rekordbox timestamp string (several formats seen); zero on failure.
func parseRBTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, l := range rbTimeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// resolveKey picks the SQLCipher passphrase: explicit arg → RAVE_REKORDBOX_KEY env → the
// rekordbox.key file → the default RB6 key. Newer Rekordbox versions use a per-install key
// (wrapped in options.json); supply it via env or the key file (see SaveKey).
func resolveKey(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if k := strings.TrimSpace(os.Getenv("RAVE_REKORDBOX_KEY")); k != "" {
		return k
	}
	if p, err := KeyFilePath(); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return s
			}
		}
	}
	return DefaultRekordboxKey
}

// KeyConfigured reports whether a non-default key is available (env or key file) - lets the UI
// decide whether to even attempt a newer-Rekordbox master.db.
func KeyConfigured() bool {
	return resolveKey("") != DefaultRekordboxKey
}

// KeyFilePath is where a user-supplied master.db key is persisted (<config>/rave-mate/rekordbox.key).
func KeyFilePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rave-mate", "rekordbox.key"), nil
}

// SaveKey writes the SQLCipher key to KeyFilePath (owner-only). An empty key removes the file.
func SaveKey(key string) error {
	p, err := KeyFilePath()
	if err != nil {
		return err
	}
	if key = strings.TrimSpace(key); key == "" {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(key), 0o600)
}

// DiscoverRekordboxMasterDB returns existing master.db paths in the default Pioneer dirs.
func DiscoverRekordboxMasterDB() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var roots []string
	roots = append(roots,
		filepath.Join(home, "Library", "Pioneer", "rekordbox"), // macOS
	)
	if ad := os.Getenv("APPDATA"); ad != "" {
		roots = append(roots, filepath.Join(ad, "Pioneer", "rekordbox")) // Windows
	}
	var out []string
	for _, root := range roots {
		p := filepath.Join(root, "master.db")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			out = append(out, p)
		}
	}
	return out
}
