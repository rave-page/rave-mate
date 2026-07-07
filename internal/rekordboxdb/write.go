package rekordboxdb

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rave.page/mate/internal/musiclib"
)

// InsertResult reports a master.db insert.
type InsertResult struct {
	Inserted int `json:"inserted"`
	Skipped  int `json:"skipped"` // already present (by path) or unsupported file type
}

// fileTypeFor maps an extension to Rekordbox's djmdContent.FileType enum (0 = unsupported).
func fileTypeFor(path string) int {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "mp3":
		return 1
	case "m4a", "aac":
		return 4
	case "flac":
		return 5
	case "wav":
		return 11
	case "aiff", "aif":
		return 12
	}
	return 0
}

// InsertTracks adds tracks to a Rekordbox master.db collection: decrypt → INSERT into djmdContent
// (resolving/creating djmdArtist/Album/Genre/Label/Key FK rows) with correct USN bookkeeping →
// re-encrypt → atomic replace. Tracks whose FolderPath already exists, or whose type Rekordbox
// can't play, are skipped. Rekordbox MUST be closed (the file is replaced). Caller backs up first.
// Replicates pyrekordbox's add_content field-for-field (proven to render in Rekordbox).
func InsertTracks(path, key string, tracks []musiclib.Track) (InsertResult, error) {
	var res InsertResult
	if len(tracks) == 0 {
		return res, nil
	}
	key = resolveKey(key)
	data, err := os.ReadFile(path)
	if err != nil {
		return res, err
	}
	plain, err := decryptSQLCipher(data, key)
	if err != nil {
		return res, err
	}
	tmp, err := os.CreateTemp("", "rave-rbx-w-*.db")
	if err != nil {
		return res, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(plain); err != nil {
		_ = tmp.Close()
		return res, err
	}
	if err := tmp.Close(); err != nil {
		return res, err
	}

	db, err := sql.Open("sqlite", tmpName)
	if err != nil {
		return res, err
	}
	// Force a rollback journal (not WAL) so every change lands in the main file before we
	// re-encrypt it; disable FK enforcement (Rekordbox doesn't use it).
	for _, p := range []string{"PRAGMA journal_mode=DELETE", "PRAGMA foreign_keys=OFF"} {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return res, err
		}
	}

	res, err = insertInto(db, tracks)
	if cerr := db.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return InsertResult{}, err
	}
	if res.Inserted == 0 {
		return res, nil // nothing added → don't rewrite the DB
	}

	// Re-encrypt the modified plaintext + atomically replace the original.
	plain2, err := os.ReadFile(tmpName)
	if err != nil {
		return res, err
	}
	enc, err := encryptSQLCipher(plain2, key)
	if err != nil {
		return res, err
	}
	if err := writeAtomic(path, enc); err != nil {
		return res, err
	}
	return res, nil
}

// insertInto performs the row inserts inside one transaction.
func insertInto(db *sql.DB, tracks []musiclib.Track) (InsertResult, error) {
	var res InsertResult
	w := &rbWriter{}

	usn, err := w.localUSN(db)
	if err != nil {
		return res, fmt.Errorf("read USN: %w", err)
	}
	contentLink, err := w.trackMenuUSN(db)
	if err != nil {
		return res, fmt.Errorf("read TRACK menu: %w", err)
	}
	deviceID, masterDBID, err := w.device(db)
	if err != nil {
		return res, fmt.Errorf("read device: %w", err)
	}

	existing := w.stringSet(db, "SELECT FolderPath FROM djmdContent WHERE FolderPath IS NOT NULL")
	w.usedIDs = w.stringSet(db, "SELECT ID FROM djmdContent")
	artists := w.fkMap(db, "djmdArtist", "Name")
	albums := w.fkMap(db, "djmdAlbum", "Name")
	genres := w.fkMap(db, "djmdGenre", "Name")
	labels := w.fkMap(db, "djmdLabel", "Name")
	keys := w.fkMap(db, "djmdKey", "ScaleName")

	tx, err := db.Begin()
	if err != nil {
		return res, err
	}
	now := rbNow()
	today := time.Now().UTC().Format("2006-01-02")

	for _, t := range tracks {
		fp := filepath.ToSlash(t.Path)
		if fp == "" || existing[fp] {
			res.Skipped++
			continue
		}
		ft := fileTypeFor(t.Path)
		if ft == 0 {
			res.Skipped++
			continue
		}
		artistID := w.resolveFK(tx, &usn, "djmdArtist", "Name", artists, t.Artist, now)
		albumID := w.resolveFK(tx, &usn, "djmdAlbum", "Name", albums, t.Album, now)
		genreID := w.resolveFK(tx, &usn, "djmdGenre", "Name", genres, t.Genre, now)
		labelID := w.resolveFK(tx, &usn, "djmdLabel", "Name", labels, t.Label, now)
		keyID := w.resolveFK(tx, &usn, "djmdKey", "ScaleName", keys, t.Key, now)
		if w.err != nil {
			_ = tx.Rollback()
			return InsertResult{}, w.err
		}

		id := w.newID()
		usn++
		_, err := tx.Exec(`INSERT INTO djmdContent
			(ID, FolderPath, FileNameL, Title, ArtistID, AlbumID, GenreID, LabelID, KeyID,
			 BPM, Length, BitRate, SampleRate, FileSize, FileType, Rating, ReleaseYear, Commnt,
			 DateCreated, StockDate, ContentLink, DeviceID, MasterDBID, MasterSongID, rb_file_id,
			 HotCueAutoLoad, UUID, rb_data_status, rb_local_data_status, rb_local_deleted,
			 rb_local_synced, rb_local_usn, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?,?, ?,?,0,0,0, 0,?,?,?)`,
			id, fp, filepath.Base(t.Path), nullify(t.Title), nullify(artistID), nullify(albumID),
			nullify(genreID), nullify(labelID), nullify(keyID),
			int(t.BPM*100), int(t.DurationSec), t.BitrateBps/1000, 0, t.FileSizeKB*1024, ft,
			t.Rating*51, yearOf(t.ReleaseDate), nullify(t.Comment),
			today, today, contentLink, deviceID, masterDBID, id, w.newBigID(),
			"on", newUUID(), usn, now, now)
		if err != nil {
			_ = tx.Rollback()
			return InsertResult{}, fmt.Errorf("insert content: %w", err)
		}
		existing[fp] = true
		res.Inserted++
	}

	// Bump the global local USN to the highest value we assigned.
	if _, err := tx.Exec(`UPDATE agentRegistry SET int_1=?, updated_at=? WHERE registry_id='localUpdateCount'`, usn, now); err != nil {
		_ = tx.Rollback()
		return InsertResult{}, fmt.Errorf("bump USN: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return InsertResult{}, err
	}
	return res, nil
}

// rbWriter holds ID-generation + first-error state across the insert.
type rbWriter struct {
	usedIDs map[string]bool
	err     error
}

// localUSN reads the global local update count (the USN counter).
func (w *rbWriter) localUSN(db *sql.DB) (int64, error) {
	var n int64
	err := db.QueryRow(`SELECT int_1 FROM agentRegistry WHERE registry_id='localUpdateCount'`).Scan(&n)
	return n, err
}

// trackMenuUSN reads the "TRACK" menu item's rb_local_usn (djmdContent.ContentLink value).
func (w *rbWriter) trackMenuUSN(db *sql.DB) (int64, error) {
	var n int64
	err := db.QueryRow(`SELECT rb_local_usn FROM djmdMenuItems WHERE Name='TRACK' LIMIT 1`).Scan(&n)
	return n, err
}

// device returns the first djmdDevice's ID + MasterDBID.
func (w *rbWriter) device(db *sql.DB) (string, string, error) {
	var id, master sql.NullString
	err := db.QueryRow(`SELECT ID, MasterDBID FROM djmdDevice LIMIT 1`).Scan(&id, &master)
	return id.String, master.String, err
}

// stringSet runs a single-column query into a set.
func (w *rbWriter) stringSet(db *sql.DB, query string) map[string]bool {
	set := map[string]bool{}
	rows, err := db.Query(query)
	if err != nil {
		return set
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var s sql.NullString
		if rows.Scan(&s) == nil && s.Valid {
			set[s.String] = true
		}
	}
	return set
}

// fkMap loads a lookup table's lower(name) → ID map (and seeds usedIDs with its IDs).
func (w *rbWriter) fkMap(db *sql.DB, table, nameCol string) map[string]string {
	m := map[string]string{}
	rows, err := db.Query(`SELECT ID, ` + nameCol + ` FROM ` + table)
	if err != nil {
		return m
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, name sql.NullString
		if rows.Scan(&id, &name) == nil && id.Valid && name.Valid {
			if k := strings.ToLower(strings.TrimSpace(name.String)); k != "" {
				if _, ok := m[k]; !ok {
					m[k] = id.String
				}
			}
		}
	}
	return m
}

// resolveFK returns the ID for name, creating the lookup row (with USN) if absent. "" name → "".
func (w *rbWriter) resolveFK(tx *sql.Tx, usn *int64, table, nameCol string, cache map[string]string, name, now string) string {
	name = strings.TrimSpace(name)
	if name == "" || w.err != nil {
		return ""
	}
	k := strings.ToLower(name)
	if id, ok := cache[k]; ok {
		return id
	}
	id := w.newID()
	*usn++
	_, err := tx.Exec(`INSERT INTO `+table+` (ID, `+nameCol+`, UUID, rb_data_status, rb_local_data_status,
		rb_local_deleted, rb_local_synced, rb_local_usn, created_at, updated_at)
		VALUES (?,?,?,0,0,0,0,?,?,?)`, id, name, newUUID(), *usn, now, now)
	if err != nil {
		w.err = fmt.Errorf("insert %s: %w", table, err)
		return ""
	}
	cache[k] = id
	return id
}

// newID returns a fresh 8-digit content/FK ID not used by any table we've seen.
func (w *rbWriter) newID() string {
	for {
		id := randDigits(8)
		if !w.usedIDs[id] {
			w.usedIDs[id] = true
			return id
		}
	}
}

// newBigID returns a 17-digit rb_file_id (matches Rekordbox's format; uniqueness not enforced).
func (w *rbWriter) newBigID() string { return randDigits(17) }

// ── small helpers ──────────────────────────────────────────────────────────────

// rbNow formats a timestamp as Rekordbox stores created_at/updated_at.
func rbNow() string { return time.Now().UTC().Format("2006-01-02 15:04:05.000 -07:00") }

// nullify maps "" → SQL NULL (so empty strings don't become searchable empty FKs).
func nullify(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// yearOf extracts a 4-digit year from a release date string (0 if none).
func yearOf(d string) int {
	if len(d) >= 4 {
		if y, err := time.Parse("2006", d[:4]); err == nil {
			return y.Year()
		}
	}
	return 0
}

// randDigits returns an n-digit decimal string (first digit non-zero), crypto-random.
func randDigits(n int) string {
	const digits = "0123456789"
	b := make([]byte, n)
	for i := range b {
		lo := 0
		if i == 0 {
			lo = 1 // no leading zero
		}
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(10-lo)))
		b[i] = digits[lo+int(k.Int64())]
	}
	return string(b)
}

// newUUID returns a random RFC-4122 v4 UUID string.
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// writeAtomic writes data to a temp file in the same dir, then renames over path.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	werr := writeAndSync(tmp, data) // capture: a swallowed write error would rename a partial file over the real db
	if cerr := tmp.Close(); cerr != nil {
		_ = os.Remove(tmpName)
		return cerr
	}
	if werr != nil {
		_ = os.Remove(tmpName)
		return werr
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// writeAndSync writes data to f and flushes it to disk, returning the first error.
func writeAndSync(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}
