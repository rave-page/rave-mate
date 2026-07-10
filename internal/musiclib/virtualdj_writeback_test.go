package musiclib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const vdjFixture = `<?xml version="1.0" encoding="UTF-8"?>
<VirtualDJ_Database Version="2024">
<Song FilePath="C:\Music\a.mp3" FileSize="5242880" Flag="4">
 <Tags Author="VA" Title="TrackA" Genre="House" Key="Am" Bpm="0.480000" Year="2024"/>
 <Infos SongLength="300.0" Bitrate="320" PlayCount="3"/>
 <Scan Version="801" Bpm="0.480000" Key="Am" Flag="32768"/>
 <Poi Pos="0.500000" Type="beatgrid" Bpm="0.480000"/>
 <Poi Pos="7.700000" Type="beatgrid" Bpm="0.480000"/>
 <Poi Pos="30.0" Type="cue" Num="1" Name="Verse"/>
 <Poi Pos="90.0" Type="loop" Num="2" Name="Build"/>
 <Comment>keep me</Comment>
</Song>
<Song FilePath="C:\Music\b.mp3" FileSize="1024">
 <Tags Author="B" Title="NoGrid" Genre="DnB"/>
 <Infos SongLength="200.0" PlayCount="1"/>
</Song>
<Song FilePath="C:\Music\c.mp3" FileSize="2048">
 <Tags Author="C" Title="Untouched" Genre="Techno" Bpm="0.500000"/>
 <Scan Bpm="0.500000"/>
 <Poi Pos="1.250000" Type="beatgrid" Bpm="0.500000"/>
</Song>
</VirtualDJ_Database>`

func writeVDJFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "database.xml")
	if err := os.WriteFile(path, []byte(vdjFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestApplyGridFixesVirtualDJ(t *testing.T) {
	path := writeVDJFixture(t)
	fixes := []GridFixUpdate{
		{Path: `C:\Music\a.mp3`, BPM: 128, StartMs: 62.5, Lock: true}, // Lock ignored (no VDJ flag)
		{Path: `C:\Music\b.mp3`, BPM: 174, StartMs: 100},
		{Path: `C:\Music\missing.mp3`, BPM: 90, StartMs: 0}, // unmatched
	}
	res, err := ApplyGridFixesVirtualDJ(path, fixes)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Updated != 2 {
		t.Fatalf("updated=%d; want 2", res.Updated)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	contains := map[string]bool{
		`Bpm="0.468750"`: true,  // 128 BPM as seconds-per-beat (Tags + Scan + grid Poi for a)
		`Pos="0.062500"`: true,  // new grid anchor for a (62.5ms)
		`Bpm="0.344828"`: true,  // 174 BPM for b (created Scan + grid Poi)
		`Pos="0.100000"`: true,  // created grid anchor for b (100ms)
		`Pos="30.0"`:     true,  // cue Poi byte-preserved
		`Name="Verse"`:   true,  //
		`Name="Build"`:   true,  // loop Poi preserved
		`keep me`:        true,  // Comment element preserved
		`Flag="4"`:       true,  // unknown Song attr preserved
		`Version="801"`:  true,  // unknown Scan attrs preserved
		`Bpm="0.500000"`: true,  // untouched song c keeps its tempo
		`Pos="1.250000"`: true,  // untouched song c keeps its grid
		`Bpm="0.480000"`: false, // old tempo gone everywhere on a
		`Pos="0.500000"`: false, // old grid anchors gone
		`Pos="7.700000"`: false,
		`LOCK`:           false, // no Traktor-ism leaked
	}
	for substr, want := range contains {
		if strings.Contains(s, substr) != want {
			t.Errorf("contains(%q) = %v; want %v", substr, !want, want)
		}
	}
	if got := strings.Count(s, `Type="beatgrid"`); got != 3 {
		t.Errorf("beatgrid count = %d; want 3 (a collapsed 2→1, b created 1, c untouched 1)", got)
	}

	// Round-trip via the reader: a's grid moved, BPM everywhere = 128.
	var tracks []Track
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := ParseVirtualDJ(f, func(tr Track) { tracks = append(tracks, tr) }); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if len(tracks) != 3 {
		t.Fatalf("tracks=%d; want 3", len(tracks))
	}
	a := tracks[0]
	if a.BPM < 127.9 || a.BPM > 128.1 {
		t.Errorf("a BPM=%v; want 128", a.BPM)
	}
	if len(a.Beatgrid) != 1 || a.Beatgrid[0].PositionMs != 62.5 {
		t.Errorf("a beatgrid: %+v", a.Beatgrid)
	}
	if len(a.Cues) != 2 {
		t.Errorf("a cues lost: %+v", a.Cues)
	}
	b := tracks[1]
	if b.BPM < 173.9 || b.BPM > 174.1 || len(b.Beatgrid) != 1 || b.Beatgrid[0].PositionMs != 100 {
		t.Errorf("b: bpm=%v grid=%+v", b.BPM, b.Beatgrid)
	}

	tmps, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmps) != 0 {
		t.Errorf("temp files not cleaned: %v", tmps)
	}
}

func TestApplyGridFixesVirtualDJNoop(t *testing.T) {
	path := writeVDJFixture(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := ApplyGridFixesVirtualDJ(path, nil); err != nil || res.Updated != 0 {
		t.Fatalf("noop: res=%+v err=%v", res, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("empty fix list rewrote the file")
	}
}

func TestMergeIntoVirtualDJFile(t *testing.T) {
	path := writeVDJFixture(t)
	updates := []Track{
		{ // existing: managed fields updated, Pois regenerated
			Path: `C:\Music\a.mp3`, Genre: "Tech House", Key: "Gm", BPM: 126, PlayCount: 9,
			Beatgrid: []GridMarker{{PositionMs: 125, BPM: 126}},
			Cues:     []CuePoint{{Name: "Drop", Kind: CueHot, StartMs: 45000, Hotcue: 1}},
		},
		{ // new: appended
			Path: `C:\Music\new.mp3`, Title: "Fresh", Artist: "N", Genre: "Trance", BPM: 138,
			DurationSec: 420, PlayCount: 1,
			Beatgrid: []GridMarker{{PositionMs: 0, BPM: 138}},
		},
	}
	res, err := MergeIntoVirtualDJFile(path, updates)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.Updated != 1 || res.Added != 1 {
		t.Fatalf("res=%+v; want 1 updated, 1 added", res)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, substr := range []string{`keep me`, `Flag="4"`, `Version="801"`} {
		if !strings.Contains(s, substr) {
			t.Errorf("preserved content missing: %q", substr)
		}
	}

	var tracks []Track
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := ParseVirtualDJ(f, func(tr Track) { tracks = append(tracks, tr) }); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if len(tracks) != 4 {
		t.Fatalf("tracks=%d; want 4 (3 existing + 1 appended)", len(tracks))
	}
	a := tracks[0]
	if a.Genre != "Tech House" || a.Key != "Gm" || a.PlayCount != 9 {
		t.Errorf("a managed fields: %+v", a)
	}
	if a.Title != "TrackA" || a.Artist != "VA" {
		t.Errorf("a unmanaged tags clobbered: %+v", a)
	}
	if a.BPM < 125.9 || a.BPM > 126.1 {
		t.Errorf("a BPM=%v; want 126", a.BPM)
	}
	if len(a.Beatgrid) != 1 || a.Beatgrid[0].PositionMs != 125 {
		t.Errorf("a beatgrid: %+v", a.Beatgrid)
	}
	if len(a.Cues) != 1 || a.Cues[0].Name != "Drop" || a.Cues[0].Hotcue != 1 {
		t.Errorf("a cues: %+v", a.Cues)
	}
	c := tracks[2]
	if c.Genre != "Techno" || c.BPM < 119.9 || c.BPM > 120.1 {
		t.Errorf("untouched song drifted: %+v", c)
	}
	n := tracks[3]
	if n.Title != "Fresh" || n.Genre != "Trance" || n.BPM < 137.9 || n.BPM > 138.1 {
		t.Errorf("appended song: %+v", n)
	}
}
