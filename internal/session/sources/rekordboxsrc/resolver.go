package rekordboxsrc

import (
	"database/sql"
	"strconv"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/rekordboxdb"
)

// contentMeta is the resolvable display metadata for one djmdContent row.
type contentMeta struct {
	title, artist, key string
}

// reloadInterval bounds how often a resolver miss triggers a full master.db reload (new
// imports / edits appear). Cheap hits are served from the in-memory map between reloads.
const reloadInterval = 30 * time.Second

// NewResolver opens master.db (auto-detect if dbPath==""; default/env/key-file key if
// dbKey=="") and returns a rekordbox-track-id → metadata lookup for the PRO DJ LINK source
// to show track text for CDJ/XDJ users. The returned func caches in a map and is reopen-
// tolerant: a miss reloads the collection (rate-limited) so newly-imported tracks resolve.
// Returns an error only if the initial open/decrypt fails (wrong key, locked, missing DB).
func NewResolver(dbPath, dbKey string) (func(trackID uint32) (title, artist, key string, ok bool), error) {
	r := &resolver{dbPath: dbPath, dbKey: dbKey}
	if err := r.reload(); err != nil {
		return nil, err
	}
	return r.lookup, nil
}

type resolver struct {
	dbPath, dbKey string

	mu         sync.RWMutex
	byID       map[uint32]contentMeta
	lastReload time.Time
}

// lookup resolves a track id, reloading on a miss at most once per reloadInterval.
func (r *resolver) lookup(trackID uint32) (string, string, string, bool) {
	r.mu.RLock()
	m, ok := r.byID[trackID]
	stale := time.Since(r.lastReload) > reloadInterval
	r.mu.RUnlock()
	if ok {
		return m.title, m.artist, m.key, true
	}
	if stale {
		_ = r.reload() // best-effort; keep prior map on failure
		r.mu.RLock()
		m, ok = r.byID[trackID]
		r.mu.RUnlock()
		if ok {
			return m.title, m.artist, m.key, true
		}
	}
	return "", "", "", false
}

// reload decrypts a fresh snapshot and rebuilds the id→metadata map. The path/key resolve
// the same way the poll backend + libsync/musiclib do (auto-detect + default/env/key-file).
func (r *resolver) reload() error {
	path := r.dbPath
	if path == "" {
		dbs := rekordboxdb.DiscoverRekordboxMasterDB()
		if len(dbs) == 0 {
			return errNoDB
		}
		path = dbs[0]
	}
	db, cleanup, err := rekordboxdb.OpenDecrypted(path, r.dbKey)
	if err != nil {
		return err
	}
	defer func() { _ = cleanup() }()

	m, err := loadContentMeta(db)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.byID = m
	r.lastReload = time.Now()
	r.mu.Unlock()
	return nil
}

// loadContentMeta reads djmdContent (+ FK artist/key) into an id→metadata map. Rekordbox
// IDs are decimal strings; Pro DJ Link broadcasts them as uint32, so non-uint32 ids are
// skipped (they can't be referenced by the wire protocol anyway).
func loadContentMeta(db *sql.DB) (map[uint32]contentMeta, error) {
	if !tableExists(db, "djmdContent") {
		return map[uint32]contentMeta{}, nil
	}
	q := `SELECT c.ID, c.Title, ar.Name, k.ScaleName
		FROM djmdContent c
		LEFT JOIN djmdArtist ar ON ar.ID = c.ArtistID
		LEFT JOIN djmdKey k ON k.ID = c.KeyID`
	if tableColumns(db, "djmdContent")["rb_local_deleted"] {
		q += " WHERE c.rb_local_deleted = 0"
	}
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[uint32]contentMeta{}
	for rows.Next() {
		var id, title, artist, key sql.NullString
		if rows.Scan(&id, &title, &artist, &key) != nil || !id.Valid {
			continue
		}
		n, perr := strconv.ParseUint(strings.TrimSpace(id.String), 10, 32)
		if perr != nil {
			continue
		}
		out[uint32(n)] = contentMeta{
			title:  strings.TrimSpace(title.String),
			artist: strings.TrimSpace(artist.String),
			key:    strings.TrimSpace(key.String),
		}
	}
	return out, rows.Err()
}
