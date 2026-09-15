package mixxx

import (
	"database/sql"
	"path/filepath"
	"runtime"
	"testing"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// buildFixture writes a mixxxdb.sqlite with the Mixxx schema subset this package reads and
// returns its path. It seeds a set-log playlist (hidden=2) with two tracks at different
// pl_datetime_added and a NORMAL playlist (hidden=0) holding a track added even LATER - so a
// correct read returns the newest SET-LOG track, not the normal-playlist drag.
func buildFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mixxxdb.sqlite")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = db.Close() }()

	stmts := []string{
		`CREATE TABLE track_locations (id INTEGER PRIMARY KEY, location TEXT)`,
		`CREATE TABLE library (id INTEGER PRIMARY KEY, artist TEXT, title TEXT, album TEXT,
			genre TEXT, bpm REAL, key TEXT, duration REAL, location INTEGER)`,
		`CREATE TABLE Playlists (id INTEGER PRIMARY KEY, name TEXT, hidden INTEGER)`,
		`CREATE TABLE PlaylistTracks (id INTEGER PRIMARY KEY, playlist_id INTEGER, track_id INTEGER,
			position INTEGER, pl_datetime_added TEXT)`,

		`INSERT INTO track_locations (id, location) VALUES
			(1, '/music/a.mp3'), (2, '/music/b.mp3'), (3, '/music/c.mp3')`,
		`INSERT INTO library (id, artist, title, album, genre, bpm, key, duration, location) VALUES
			(1, 'Artist A', 'Title A', 'Album A', 'House',  128.0, '8A',  300.0, 1),
			(2, 'Artist B', 'Title B', 'Album B', 'Techno', 130.5, '9A',  320.0, 2),
			(3, 'Artist C', 'Title C', 'Album C', 'Trance', 138.0, '10A', 400.0, 3)`,
		`INSERT INTO Playlists (id, name, hidden) VALUES
			(10, 'History 2024', 2),
			(20, 'My Crate',     0)`,
		// set-log: track 1 played earlier, track 2 later (track 2 is the newest PLAY).
		`INSERT INTO PlaylistTracks (id, playlist_id, track_id, position, pl_datetime_added) VALUES
			(1, 10, 1, 0, '2024-01-01 10:00:00'),
			(2, 10, 2, 1, '2024-01-01 10:05:00'),
			(3, 20, 3, 0, '2024-01-01 11:00:00')`, // normal-playlist drag, added LATEST overall
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed fixture: %v\nstmt: %s", err, s)
		}
	}
	return path
}

func TestReadNowPlayingSetLogWins(t *testing.T) {
	np, ok, err := ReadNowPlaying(buildFixture(t))
	if err != nil {
		t.Fatalf("ReadNowPlaying: %v", err)
	}
	if !ok {
		t.Fatal("want ok=true (a set-log play exists)")
	}
	// The newest SET-LOG track (Title B), NOT the newer normal-playlist drag (Title C).
	if np.Title != "Title B" || np.Artist != "Artist B" {
		t.Errorf("want Title/Artist B, got %q / %q", np.Title, np.Artist)
	}
	if np.Album != "Album B" || np.Genre != "Techno" || np.Key != "9A" {
		t.Errorf("enrichment wrong: album=%q genre=%q key=%q", np.Album, np.Genre, np.Key)
	}
	if np.BPM != 130.5 {
		t.Errorf("want BPM 130.5, got %v", np.BPM)
	}
	if np.Path != "/music/b.mp3" {
		t.Errorf("want path /music/b.mp3, got %q", np.Path)
	}
	if np.DurationSec != 320 {
		t.Errorf("want duration 320, got %d", np.DurationSec)
	}
}

func TestReadNowPlayingEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mixxxdb.sqlite")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Schema present, but no play history rows.
	for _, s := range []string{
		`CREATE TABLE library (id INTEGER PRIMARY KEY, artist TEXT, title TEXT)`,
		`CREATE TABLE Playlists (id INTEGER PRIMARY KEY, name TEXT, hidden INTEGER)`,
		`CREATE TABLE PlaylistTracks (id INTEGER PRIMARY KEY, playlist_id INTEGER, track_id INTEGER,
			position INTEGER, pl_datetime_added TEXT)`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	_ = db.Close()

	np, ok, err := ReadNowPlaying(path)
	if err != nil {
		t.Fatalf("ReadNowPlaying: %v", err)
	}
	if ok || np != nil {
		t.Errorf("want ok=false, nil np for an empty library; got ok=%v np=%+v", ok, np)
	}
}

func TestHasDBAndDefaultPath(t *testing.T) {
	if HasDB("") {
		t.Error("HasDB(\"\") must be false")
	}
	if HasDB(filepath.Join(t.TempDir(), "nope.sqlite")) {
		t.Error("HasDB of a missing file must be false")
	}
	if !HasDB(buildFixture(t)) {
		t.Error("HasDB of the fixture must be true")
	}

	switch runtime.GOOS {
	case "windows", "darwin", "linux":
		p, err := DefaultDBPath()
		if err != nil {
			t.Fatalf("DefaultDBPath on %s: %v", runtime.GOOS, err)
		}
		if p == "" {
			t.Fatal("DefaultDBPath returned empty")
		}
		if filepath.Base(p) != "mixxxdb.sqlite" {
			t.Errorf("DefaultDBPath basename = %q, want mixxxdb.sqlite", filepath.Base(p))
		}
	default:
		if _, err := DefaultDBPath(); err == nil {
			t.Errorf("DefaultDBPath should error on unsupported OS %q", runtime.GOOS)
		}
	}
}
