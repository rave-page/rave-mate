package rekordboxsrc

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	_ "modernc.org/sqlite" // pure-Go SQLite driver

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

// newFixtureDB builds a PLAIN (unencrypted) SQLite DB whose tables + columns match exactly what
// the production DB-poll query SELECTs/JOINs: djmdSongHistory.ContentID → djmdContent
// (ID/Title/BPM/ArtistID/KeyID/FolderPath) → djmdArtist (ID/Name) + djmdKey (ID/ScaleName).
func newFixtureDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	stmts := []string{
		`CREATE TABLE djmdArtist (ID TEXT, Name TEXT)`,
		`CREATE TABLE djmdKey (ID TEXT, ScaleName TEXT)`,
		// BPM stored as rekordbox's BPM×100 integer (centiBPM divides by 100).
		`CREATE TABLE djmdContent (ID TEXT, Title TEXT, ArtistID TEXT, KeyID TEXT, BPM INTEGER, FolderPath TEXT)`,
		`CREATE TABLE djmdSongHistory (ContentID TEXT)`,
		`INSERT INTO djmdArtist (ID, Name) VALUES ('1','Artist One'),('2','Artist Two')`,
		`INSERT INTO djmdKey (ID, ScaleName) VALUES ('10','8A'),('20','5A')`,
		`INSERT INTO djmdContent (ID, Title, ArtistID, KeyID, BPM, FolderPath)
			VALUES ('100','Old Track','1','10',12000,'/music/old.mp3'),
			       ('200','New Track','2','20',12800,'/music/new.mp3')`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	return db
}

func testSource() *Source { return &Source{log: logbus.New(64), cfg: Config{DBPoll: true}} }

// TestLatestPlayFromDB asserts the SQL core returns the NEWEST play (highest djmdSongHistory
// rowid) with title/artist/key/bpm/path correctly joined across the four tables.
func TestLatestPlayFromDB(t *testing.T) {
	db := newFixtureDB(t)
	// Older play first (lower rowid), then the newer one - newest = rowid DESC LIMIT 1.
	if _, err := db.Exec(`INSERT INTO djmdSongHistory (ContentID) VALUES ('100')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO djmdSongHistory (ContentID) VALUES ('200')`); err != nil {
		t.Fatal(err)
	}

	s := testSource()
	p, ok := s.latestPlayFromDB(db, &onceLog{})
	if !ok {
		t.Fatal("expected a play")
	}
	if p.title != "New Track" {
		t.Errorf("title=%q, want New Track", p.title)
	}
	if p.artist != "Artist Two" {
		t.Errorf("artist=%q, want Artist Two", p.artist)
	}
	if p.key != "5A" {
		t.Errorf("key=%q, want 5A", p.key)
	}
	if p.bpm != 128 {
		t.Errorf("bpm=%v, want 128", p.bpm)
	}
	if want := filepath.FromSlash("/music/new.mp3"); p.path != want {
		t.Errorf("path=%q, want %q", p.path, want)
	}

	// The mapping to a master-scope Observation carries the joined fields.
	obs := playObservation(p, true)
	if obs.Source != session.SourceRekordboxDB {
		t.Errorf("source=%q, want %q", obs.Source, session.SourceRekordboxDB)
	}
	if obs.Scope.Kind != session.ScopeMaster || obs.Scope.ID != "" {
		t.Errorf("scope=%+v, want master/''", obs.Scope)
	}
	if obs.Confidence != confDB {
		t.Errorf("confidence=%v, want %v", obs.Confidence, confDB)
	}
	if !obs.Loaded {
		t.Error("loaded=false, want true")
	}
	if obs.Fields[session.FieldTitle] != "New Track" {
		t.Errorf("field title=%v", obs.Fields[session.FieldTitle])
	}
	if obs.Fields[session.FieldArtist] != "Artist Two" {
		t.Errorf("field artist=%v", obs.Fields[session.FieldArtist])
	}
	if obs.Fields[session.FieldKey] != "5A" {
		t.Errorf("field key=%v", obs.Fields[session.FieldKey])
	}
	if obs.Fields[session.FieldBPM] != 128.0 {
		t.Errorf("field bpm=%v", obs.Fields[session.FieldBPM])
	}
	if obs.Fields[session.FieldPath] != filepath.FromSlash("/music/new.mp3") {
		t.Errorf("field path=%v", obs.Fields[session.FieldPath])
	}
}

// TestLatestPlayFromDB_EmptyHistory: tables present but no history rows ⇒ ok=false (ErrNoRows).
func TestLatestPlayFromDB_EmptyHistory(t *testing.T) {
	db := newFixtureDB(t) // no djmdSongHistory rows inserted
	s := testSource()
	if _, ok := s.latestPlayFromDB(db, &onceLog{}); ok {
		t.Error("expected ok=false on empty history")
	}
}

// TestLatestPlayFromDB_NoHistoryTable: missing history table ⇒ ok=false (state-gated warn).
func TestLatestPlayFromDB_NoHistoryTable(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE djmdContent (ID TEXT)`); err != nil {
		t.Fatal(err)
	}
	s := testSource()
	if _, ok := s.latestPlayFromDB(db, &onceLog{}); ok {
		t.Error("expected ok=false when djmdSongHistory is absent")
	}
}

// ── R2: optionsDBPath parsing ─────────────────────────────────────────────────

// TestOptionsDBPathFrom_ArrayFileValue: a pair value pointing at an existing master.db file is
// returned verbatim ({"options":[[key,value],…]} shape).
func TestOptionsDBPathFrom_ArrayFileValue(t *testing.T) {
	dir := t.TempDir()
	dbFile := filepath.Join(dir, "master.db")
	if err := os.WriteFile(dbFile, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := mustOptionsJSON(t, [][]any{
		{"someSetting", "not-a-path"},
		{"dbPath", dbFile},
	})
	if got := optionsDBPathFrom(data); got != dbFile {
		t.Errorf("got %q, want %q", got, dbFile)
	}
}

// TestOptionsDBPathFrom_ArrayDirValue: a pair value that is a directory resolves to dir/master.db.
func TestOptionsDBPathFrom_ArrayDirValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "master.db"), []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := mustOptionsJSON(t, [][]any{{"dbDir", dir}})
	if got := optionsDBPathFrom(data); got != filepath.Join(dir, "master.db") {
		t.Errorf("got %q, want %q", got, filepath.Join(dir, "master.db"))
	}
}

// TestOptionsDBPathFrom_NoValidPath: no pair value stat-validates ⇒ "".
func TestOptionsDBPathFrom_NoValidPath(t *testing.T) {
	data := mustOptionsJSON(t, [][]any{
		{"k", filepath.Join(t.TempDir(), "does-not-exist.db")},
		{"n", 42}, // non-string value ignored
	})
	if got := optionsDBPathFrom(data); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// TestOptionsDBPathFrom_MapShapeUnsupported: the older map shape isn't parsed ⇒ "".
func TestOptionsDBPathFrom_MapShapeUnsupported(t *testing.T) {
	if got := optionsDBPathFrom([]byte(`{"options":{"dbPath":"/x"}}`)); got != "" {
		t.Errorf("map shape should not parse, got %q", got)
	}
	if got := optionsDBPathFrom([]byte(`not json`)); got != "" {
		t.Errorf("malformed JSON should yield empty, got %q", got)
	}
}

// TestOptionsDBPath_Wired exercises the full optionsDBPath() (optionsJSONPath + ReadFile + parse)
// by redirecting the OS config dir to a temp tree. Runs only where optionsJSONPath is defined.
func TestOptionsDBPath_Wired(t *testing.T) {
	var storageDir string
	switch runtime.GOOS {
	case "windows":
		root := t.TempDir()
		t.Setenv("AppData", root)
		storageDir = filepath.Join(root, "Pioneer", "rekordboxAgent", "storage")
	case "darwin":
		home := t.TempDir()
		t.Setenv("HOME", home)
		storageDir = filepath.Join(home, "Library", "Application Support", "Pioneer", "rekordboxAgent", "storage")
	default:
		t.Skipf("optionsJSONPath undefined on %s", runtime.GOOS)
	}
	if optionsJSONPath() == "" {
		t.Skip("optionsJSONPath empty (UserConfigDir/UserHomeDir unset)")
	}
	if err := os.MkdirAll(storageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbFile := filepath.Join(storageDir, "master.db")
	if err := os.WriteFile(dbFile, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storageDir, "options.json"),
		mustOptionsJSON(t, [][]any{{"dbPath", dbFile}}), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := optionsDBPath(); got != dbFile {
		t.Errorf("optionsDBPath()=%q, want %q", got, dbFile)
	}
}

func mustOptionsJSON(t *testing.T, options [][]any) []byte {
	t.Helper()
	b, err := json.Marshal(struct {
		Options [][]any `json:"options"`
	}{Options: options})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
