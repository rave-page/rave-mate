package libsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/musiclib"
)

const vdjTestDB = `<?xml version="1.0" encoding="UTF-8"?>
<VirtualDJ_Database Version="2024">
<Song FilePath="C:\Music\a.mp3" FileSize="5242880">
 <Tags Author="VA" Title="TrackA" Genre="House" Key="Am" Bpm="0.480000"/>
 <Infos SongLength="300.0" PlayCount="3"/>
 <Scan Version="801" Bpm="0.480000" Key="Am"/>
 <Poi Pos="0.500000" Type="beatgrid" Bpm="0.480000"/>
 <Poi Pos="30.0" Type="cue" Num="1" Name="Verse"/>
</Song>
</VirtualDJ_Database>`

func vdjTracks() []musiclib.Track {
	return []musiclib.Track{
		{ // matches the existing song
			Path: `C:\Music\a.mp3`, Title: "TrackA", Artist: "VA", Genre: "Tech House", BPM: 126,
			PlayCount: 7, Beatgrid: []musiclib.GridMarker{{PositionMs: 125, BPM: 126}},
			Cues: []musiclib.CuePoint{{Name: "Drop", Kind: musiclib.CueHot, StartMs: 45000, Hotcue: 1}},
		},
		{ // new
			Path: `C:\Music\new.mp3`, Title: "Fresh", Artist: "N", Genre: "Trance", BPM: 138,
			Beatgrid: []musiclib.GridMarker{{PositionMs: 0, BPM: 138}},
		},
	}
}

func parseVDJ(t *testing.T, path string) []musiclib.Track {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var tracks []musiclib.Track
	if _, err := musiclib.ParseVirtualDJ(f, func(tr musiclib.Track) { tracks = append(tracks, tr) }); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return tracks
}

// ModeFile: importable database.xml written + readable by the VirtualDJ reader.
func TestVirtualDJFileTarget(t *testing.T) {
	out := filepath.Join(t.TempDir(), "database.xml")
	res, err := applyTarget(config.SyncTarget{App: AppVirtualDJ, Mode: ModeFile, OutputPath: out}, vdjTracks())
	if err != nil {
		t.Fatalf("applyTarget: %v", err)
	}
	if res.Added != 2 || res.Path != out {
		t.Fatalf("outcome: %+v", res)
	}
	if !strings.Contains(res.Note, "database.xml") {
		t.Errorf("note should explain VDJ merge convention: %q", res.Note)
	}
	tracks := parseVDJ(t, out)
	if len(tracks) != 2 {
		t.Fatalf("tracks=%d; want 2", len(tracks))
	}
	if tracks[0].Title != "TrackA" || tracks[0].BPM < 125.9 || tracks[0].BPM > 126.1 {
		t.Errorf("track0: %+v", tracks[0])
	}
	if len(tracks[0].Beatgrid) != 1 || len(tracks[0].Cues) != 1 {
		t.Errorf("track0 grid/cues: %+v %+v", tracks[0].Beatgrid, tracks[0].Cues)
	}
}

// ModeWriteback: in-place merge into an existing database.xml, backed up first.
func TestVirtualDJWritebackTarget(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("RAVE_MATE_CONFIG_DIR", cfgDir) // isolate backupBeforeWrite
	db := filepath.Join(t.TempDir(), "database.xml")
	if err := os.WriteFile(db, []byte(vdjTestDB), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := applyTarget(config.SyncTarget{App: AppVirtualDJ, Mode: ModeWriteback, OutputPath: db}, vdjTracks())
	if err != nil {
		t.Fatalf("applyTarget: %v", err)
	}
	if res.Updated != 1 || res.Added != 1 {
		t.Fatalf("outcome: %+v", res)
	}

	tracks := parseVDJ(t, db)
	if len(tracks) != 2 {
		t.Fatalf("tracks=%d; want 2", len(tracks))
	}
	a := tracks[0]
	if a.Genre != "Tech House" || a.PlayCount != 7 || a.BPM < 125.9 || a.BPM > 126.1 {
		t.Errorf("merged song: %+v", a)
	}
	if a.Title != "TrackA" || a.Artist != "VA" {
		t.Errorf("unmanaged tags clobbered: %+v", a)
	}
	if len(a.Beatgrid) != 1 || a.Beatgrid[0].PositionMs != 125 {
		t.Errorf("beatgrid: %+v", a.Beatgrid)
	}
	if tracks[1].Title != "Fresh" {
		t.Errorf("appended song: %+v", tracks[1])
	}

	backups, err := filepath.Glob(filepath.Join(cfgDir, "library-backups", "database.xml.*.bak"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups=%v; want exactly 1", backups)
	}
	orig, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(orig) != vdjTestDB {
		t.Error("backup is not the pre-write content")
	}
}

// Writeback with no path + no discoverable database errors cleanly.
func TestVirtualDJWritebackNoDatabase(t *testing.T) {
	t.Setenv("RAVE_MATE_CONFIG_DIR", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows os.UserHomeDir
	if _, err := applyTarget(config.SyncTarget{App: AppVirtualDJ, Mode: ModeWriteback}, vdjTracks()); err == nil {
		t.Fatal("want error when no database.xml exists")
	}
}
