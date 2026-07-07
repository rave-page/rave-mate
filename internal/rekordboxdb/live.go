package rekordboxdb

import (
	"database/sql"
	"os"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (registers "sqlite")
)

// OpenDecrypted decrypts a SQLCipher master.db to a private temp file and opens it read-only
// for LIVE queries (now-playing history poll + id→metadata resolver in session/sources/
// rekordboxsrc). Reuses the package's existing SQLCipher decrypt - no crypto reimplemented.
// The plaintext temp persists only for the returned handle's lifetime; cleanup closes the
// DB and removes the temp. Re-call to pick up new writes (rekordbox checkpoints play-history
// rows into the main file); callers should re-open per poll rather than hold the handle.
func OpenDecrypted(path, key string) (*sql.DB, func() error, error) {
	key = resolveKey(key)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	plain, err := decryptSQLCipher(data, key)
	if err != nil {
		return nil, nil, err
	}
	tmp, err := os.CreateTemp("", "rave-rbx-live-*.db")
	if err != nil {
		return nil, nil, err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(plain); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return nil, nil, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return nil, nil, err
	}
	db, err := sql.Open("sqlite", tmpName)
	if err != nil {
		_ = os.Remove(tmpName)
		return nil, nil, err
	}
	// Read-only: this is a throwaway snapshot of rekordbox's live DB.
	_, _ = db.Exec("PRAGMA query_only=ON")
	cleanup := func() error {
		cerr := db.Close()
		_ = os.Remove(tmpName)
		return cerr
	}
	return db, cleanup, nil
}
