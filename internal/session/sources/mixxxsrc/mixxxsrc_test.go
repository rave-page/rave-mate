package mixxxsrc

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

// newFixture writes a mixxxdb.sqlite with a set-log playlist (hidden=2) holding one played track
// (Title A) and returns its path.
func newFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mixxxdb.sqlite")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	for _, s := range []string{
		`CREATE TABLE track_locations (id INTEGER PRIMARY KEY, location TEXT)`,
		`CREATE TABLE library (id INTEGER PRIMARY KEY, artist TEXT, title TEXT, album TEXT,
			genre TEXT, bpm REAL, key TEXT, duration REAL, location INTEGER)`,
		`CREATE TABLE Playlists (id INTEGER PRIMARY KEY, name TEXT, hidden INTEGER)`,
		`CREATE TABLE PlaylistTracks (id INTEGER PRIMARY KEY, playlist_id INTEGER, track_id INTEGER,
			position INTEGER, pl_datetime_added TEXT)`,
		`INSERT INTO track_locations (id, location) VALUES (1, '/music/a.mp3'), (2, '/music/b.mp3')`,
		`INSERT INTO library (id, artist, title, bpm, key, duration, location) VALUES
			(1, 'Artist A', 'Title A', 124.0, '1A', 200.0, 1),
			(2, 'Artist B', 'Title B', 126.0, '2A', 210.0, 2)`,
		`INSERT INTO Playlists (id, name, hidden) VALUES (10, 'History', 2)`,
		`INSERT INTO PlaylistTracks (id, playlist_id, track_id, position, pl_datetime_added) VALUES
			(1, 10, 1, 0, '2024-01-01 10:00:00')`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed: %v\nstmt: %s", err, s)
		}
	}
	return path
}

// addNewerPlay appends a newer set-log row (track 2) and bumps the file mtime so poll re-reads.
func addNewerPlay(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO PlaylistTracks (id, playlist_id, track_id, position, pl_datetime_added)
		VALUES (2, 10, 2, 1, '2024-01-01 10:05:00')`); err != nil {
		_ = db.Close()
		t.Fatalf("insert newer play: %v", err)
	}
	_ = db.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	nt := fi.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(path, nt, nt); err != nil {
		t.Fatal(err)
	}
}

// TestSeedThenChange: first poll seeds the past-session track (no emit); after a newer set-log
// row + mtime bump, the second poll emits one master Observation for the new track.
func TestSeedThenChange(t *testing.T) {
	path := newFixture(t)
	s := New(logbus.New(16), path)

	var emitted []session.Observation
	emit := func(o session.Observation) { emitted = append(emitted, o) }

	s.poll(emit) // seed only
	if len(emitted) != 0 {
		t.Fatalf("first poll must not emit (seed the past-session track), got %d", len(emitted))
	}

	addNewerPlay(t, path)
	s.poll(emit)
	if len(emitted) != 1 {
		t.Fatalf("second poll should emit exactly one observation, got %d", len(emitted))
	}
	o := emitted[0]
	if o.Source != session.SourceMixxx || o.Scope.Kind != session.ScopeMaster {
		t.Errorf("wrong source/scope: %s / %s", o.Source, o.Scope.Kind)
	}
	if v, _ := o.Fields[session.FieldTitle].(string); v != "Title B" {
		t.Errorf("want Title B, got %q", v)
	}
	if v, _ := o.Fields[session.FieldArtist].(string); v != "Artist B" {
		t.Errorf("want Artist B, got %q", v)
	}
	if p, _ := o.Fields[session.FieldIsPlaying].(bool); !p {
		t.Error("want isPlaying=true")
	}
	if !o.Loaded {
		t.Error("want Loaded=true on a track change")
	}

	// A third poll with no DB change must not re-emit.
	s.poll(emit)
	if len(emitted) != 1 {
		t.Errorf("unchanged DB must not re-emit, got %d total", len(emitted))
	}
}
