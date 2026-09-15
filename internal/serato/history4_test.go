package serato

import (
	"database/sql"
	"path/filepath"
	"runtime"
	"testing"

	_ "modernc.org/sqlite"
)

// buildV4DB creates a WAL-mode master.sqlite fixture (WAL mirrors the live Serato DB, which
// ReadLatestV4Session opens read-only with journal_mode(wal)). Runs stmts in order.
func buildV4DB(t *testing.T, path string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(wal)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	schema := []string{
		`CREATE TABLE history_session(id INTEGER PRIMARY KEY, start_time INT, end_time INT, name TEXT)`,
		`CREATE TABLE history_entry(id INTEGER PRIMARY KEY, session_id INT, name TEXT, artist TEXT,
		   deck TEXT, start_time INT, end_time INT, played INT, bpm REAL, file_name TEXT, portable_id TEXT)`,
	}
	for _, s := range append(schema, stmts...) {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
}

// TestReadLatestV4Session: 3 entries across decks (1 playing, 2 ended, A playing) + asset
// enrichment. Asserts deck numbers 1,2,1, endtime semantics, text/bpm, and album/genre/key.
func TestReadLatestV4Session(t *testing.T) {
	dbp := filepath.Join(t.TempDir(), "master.sqlite")
	buildV4DB(t, dbp,
		`INSERT INTO history_session(id,start_time,end_time,name) VALUES(10,1650000000,0,'Live Set')`,
		// start_time ASC ordering => [deck1, deck2, deckA]
		`INSERT INTO history_entry(session_id,name,artist,deck,start_time,end_time,played,bpm,file_name,portable_id)
		   VALUES(10,'T1','A1','1',1650000100,0,1,128.0,'C:/music/t1.mp3','pid1')`,
		`INSERT INTO history_entry(session_id,name,artist,deck,start_time,end_time,played,bpm,file_name,portable_id)
		   VALUES(10,'T2','A2','2',1650000200,1650000260,1,130.5,'C:/music/t2.mp3','pid2')`,
		`INSERT INTO history_entry(session_id,name,artist,deck,start_time,end_time,played,bpm,file_name,portable_id)
		   VALUES(10,'T3','A3','A',1650000300,0,0,174.0,NULL,'pid3')`,
		`CREATE TABLE asset(location_id INT, portable_id TEXT, type_specific_data TEXT,
		   album TEXT, label TEXT, genre TEXT, key TEXT)`,
		`INSERT INTO asset(location_id,portable_id,album,label,genre,key)
		   VALUES(1,'pid1','Album1','Label1','Techno','8A')`,
		`INSERT INTO asset(location_id,portable_id,album,label,genre,key)
		   VALUES(1,'pid3','Album3','Label3','Trance','5A')`,
	)

	tracks, err := ReadLatestV4Session(dbp)
	if err != nil {
		t.Fatalf("ReadLatestV4Session: %v", err)
	}
	if len(tracks) != 3 {
		t.Fatalf("want 3 tracks, got %d: %+v", len(tracks), tracks)
	}

	// Entry 0: deck "1" -> 1, playing (end_time 0), enriched.
	e0 := tracks[0]
	if e0.Deck != 1 || e0.EndedAt != 0 || e0.StartedAt != 1650000100 {
		t.Errorf("entry0 deck/timing: Deck=%d Started=%d Ended=%d", e0.Deck, e0.StartedAt, e0.EndedAt)
	}
	if e0.Title != "T1" || e0.Artist != "A1" || e0.BPM != 128.0 || e0.Path != "C:/music/t1.mp3" {
		t.Errorf("entry0 fields: %+v", e0)
	}
	if !e0.Played {
		t.Errorf("entry0 played should be true")
	}
	if e0.Album != "Album1" || e0.Genre != "Techno" || e0.Key != "8A" {
		t.Errorf("entry0 enrichment: Album=%q Genre=%q Key=%q", e0.Album, e0.Genre, e0.Key)
	}

	// Entry 1: deck "2" -> 2, ENDED (end_time > 0).
	e1 := tracks[1]
	if e1.Deck != 2 || e1.EndedAt != 1650000260 {
		t.Errorf("entry1 deck/ended: Deck=%d Ended=%d", e1.Deck, e1.EndedAt)
	}
	if e1.Title != "T2" || e1.BPM != 130.5 {
		t.Errorf("entry1 fields: %+v", e1)
	}

	// Entry 2: deck "A" -> 1, playing, no file_name => Path from portable_id, not played, no asset key column populated but row exists.
	e2 := tracks[2]
	if e2.Deck != 1 || e2.EndedAt != 0 {
		t.Errorf("entry2 deck/timing: Deck=%d Ended=%d", e2.Deck, e2.EndedAt)
	}
	if e2.Title != "T3" || e2.Path != "pid3" {
		t.Errorf("entry2 fields (Path should fall back to portable_id): %+v", e2)
	}
	if e2.Played {
		t.Errorf("entry2 played should be false (played=0)")
	}
	if e2.Genre != "Trance" || e2.Key != "5A" {
		t.Errorf("entry2 enrichment: Genre=%q Key=%q", e2.Genre, e2.Key)
	}
}

// TestReadLatestV4SessionOnlyLatest: an older + newer session; only the newest session's
// entries are returned.
func TestReadLatestV4SessionOnlyLatest(t *testing.T) {
	dbp := filepath.Join(t.TempDir(), "master.sqlite")
	buildV4DB(t, dbp,
		`INSERT INTO history_session(id,start_time,end_time,name) VALUES(1,1600000000,1600000900,'Old')`,
		`INSERT INTO history_session(id,start_time,end_time,name) VALUES(2,1700000000,0,'New')`,
		`INSERT INTO history_entry(session_id,name,artist,deck,start_time,end_time,played,bpm,file_name,portable_id)
		   VALUES(1,'OldTrack','OA','1',1600000100,1600000200,1,120.0,'old.mp3','opid')`,
		`INSERT INTO history_entry(session_id,name,artist,deck,start_time,end_time,played,bpm,file_name,portable_id)
		   VALUES(2,'NewTrack','NA','1',1700000100,0,1,125.0,'new.mp3','npid')`,
	)
	tracks, err := ReadLatestV4Session(dbp)
	if err != nil {
		t.Fatalf("ReadLatestV4Session: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("want 1 track from latest session, got %d: %+v", len(tracks), tracks)
	}
	if tracks[0].Title != "NewTrack" {
		t.Errorf("want NewTrack (latest session), got %q", tracks[0].Title)
	}
}

// TestReadLatestV4SessionNoAsset: no asset table => enrichment silently skipped, entries still read.
func TestReadLatestV4SessionNoAsset(t *testing.T) {
	dbp := filepath.Join(t.TempDir(), "master.sqlite")
	buildV4DB(t, dbp,
		`INSERT INTO history_session(id,start_time,end_time,name) VALUES(1,1650000000,0,'S')`,
		`INSERT INTO history_entry(session_id,name,artist,deck,start_time,end_time,played,bpm,file_name,portable_id)
		   VALUES(1,'T','A','1',1650000100,0,1,128.0,'t.mp3','pid')`,
	)
	tracks, err := ReadLatestV4Session(dbp)
	if err != nil {
		t.Fatalf("ReadLatestV4Session: %v", err)
	}
	if len(tracks) != 1 || tracks[0].Title != "T" || tracks[0].Album != "" {
		t.Errorf("no-asset read: %+v", tracks)
	}
}

// TestReadLatestV4SessionEmpty: DB with no sessions => empty slice, no error.
func TestReadLatestV4SessionEmpty(t *testing.T) {
	dbp := filepath.Join(t.TempDir(), "master.sqlite")
	buildV4DB(t, dbp)
	tracks, err := ReadLatestV4Session(dbp)
	if err != nil {
		t.Fatalf("ReadLatestV4Session (empty): %v", err)
	}
	if len(tracks) != 0 {
		t.Errorf("want 0 tracks, got %d", len(tracks))
	}
}

func TestHasV4DB(t *testing.T) {
	dbp := filepath.Join(t.TempDir(), "master.sqlite")
	if HasV4DB(dbp) {
		t.Error("HasV4DB should be false before the file exists")
	}
	buildV4DB(t, dbp)
	if !HasV4DB(dbp) {
		t.Error("HasV4DB should be true for an existing file")
	}
	if HasV4DB("") {
		t.Error("HasV4DB(\"\") should be false")
	}
	if HasV4DB(t.TempDir()) {
		t.Error("HasV4DB should be false for a directory")
	}
}

func TestDefaultV4DBPath(t *testing.T) {
	p, err := DefaultV4DBPath()
	switch runtime.GOOS {
	case "windows", "darwin":
		if err != nil {
			t.Fatalf("DefaultV4DBPath on %s: %v", runtime.GOOS, err)
		}
		if filepath.Base(p) != "master.sqlite" {
			t.Errorf("want path ending in master.sqlite, got %q", p)
		}
		t.Logf("DefaultV4DBPath=%q", p)
	default:
		if err == nil {
			t.Errorf("DefaultV4DBPath should error on %s, got %q", runtime.GOOS, p)
		}
	}
}

func TestParseDeckValue(t *testing.T) {
	cases := map[string]int{
		"1": 1, "2": 2, "3": 3, "4": 4,
		"A": 1, "B": 2, "C": 3, "D": 4,
		"a": 1, "d": 4,
		" 2 ": 2, // trimmed
		"":    0,
		"0":   0, "5": 0, "9": 0,
		"X": 0, "E": 0,
	}
	for in, want := range cases {
		if got := parseDeckValue(in); got != want {
			t.Errorf("parseDeckValue(%q) = %d, want %d", in, got, want)
		}
	}
}
