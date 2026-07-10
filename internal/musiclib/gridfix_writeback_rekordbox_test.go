package musiclib

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic rekordbox.xml with two collection tracks + one gridless track + a playlist ref.
// Locations are built via trackLocation so path matching is OS-agnostic.
func rbGridFixtureXML(fixPath, otherPath, gridlessPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<DJ_PLAYLISTS Version="1.0.0">
  <PRODUCT Name="rekordbox" Version="6.7.7" Company="AlphaTheta"/>
  <COLLECTION Entries="3">
    <TRACK TrackID="1" Name="Fix Me" Artist="Synthetic" AverageBpm="127.94" Tonality="8A" Location="%s">
      <TEMPO Inizio="0.025" Bpm="127.94" Metro="4/4" Battito="1"/>
      <TEMPO Inizio="30.512" Bpm="128.31" Metro="4/4" Battito="3"/>
      <POSITION_MARK Name="Drop" Type="0" Start="61.250" Num="0"/>
      <POSITION_MARK Name="" Type="0" Start="122.500" Num="-1"/>
    </TRACK>
    <TRACK TrackID="2" Name="Leave Me" Artist="Synthetic" AverageBpm="140.00" Location="%s">
      <TEMPO Inizio="0.100" Bpm="140.00" Metro="4/4" Battito="1"/>
      <POSITION_MARK Name="Intro" Type="0" Start="0.100" Num="0"/>
    </TRACK>
    <TRACK TrackID="3" Name="No Grid" Artist="Synthetic" AverageBpm="0.00" Location="%s">
      <POSITION_MARK Name="Mark" Type="0" Start="5.000" Num="0"/>
    </TRACK>
  </COLLECTION>
  <PLAYLISTS>
    <NODE Type="0" Name="ROOT" Count="1">
      <NODE Name="Set" Type="1" KeyType="0" Entries="2">
        <TRACK Key="1"/>
        <TRACK Key="2"/>
      </NODE>
    </NODE>
  </PLAYLISTS>
</DJ_PLAYLISTS>
`, trackLocation(fixPath), trackLocation(otherPath), trackLocation(gridlessPath))
}

// writeRBGridFixture writes the fixture into a temp dir, returning xml path + track paths.
func writeRBGridFixture(t *testing.T) (xmlPath, fixPath, otherPath, gridlessPath string) {
	t.Helper()
	dir := t.TempDir()
	fixPath = filepath.Join(dir, "music", "fix me.mp3")
	otherPath = filepath.Join(dir, "music", "leave.mp3")
	gridlessPath = filepath.Join(dir, "music", "nogrid.mp3")
	xmlPath = filepath.Join(dir, "rekordbox.xml")
	if err := os.WriteFile(xmlPath, []byte(rbGridFixtureXML(fixPath, otherPath, gridlessPath)), 0o644); err != nil {
		t.Fatal(err)
	}
	return xmlPath, fixPath, otherPath, gridlessPath
}

// parseRBFile parses the written XML back via the library reader, indexed by path.
func parseRBFile(t *testing.T, path string) (map[string]Track, []Playlist) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	tracks, playlists, err := ParseRekordboxLibrary(f)
	if err != nil {
		t.Fatalf("parse back: %v", err)
	}
	byPath := map[string]Track{}
	for _, tr := range tracks {
		byPath[tr.Path] = tr
	}
	return byPath, playlists
}

func TestApplyGridFixesRekordboxXML_ReplacesGrid(t *testing.T) {
	xmlPath, fixPath, otherPath, _ := writeRBGridFixture(t)

	res, err := ApplyGridFixesRekordboxXML(xmlPath, []GridFixUpdate{
		{Path: fixPath, BPM: 128.05, StartMs: 512, Lock: true},
		{Path: filepath.Join(t.TempDir(), "not-in-collection.mp3"), BPM: 100, StartMs: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 || res.Added != 0 {
		t.Fatalf("result = %+v, want Updated=1 Added=0", res)
	}

	byPath, _ := parseRBFile(t, xmlPath)
	got := byPath[fixPath]
	if len(got.Beatgrid) != 1 {
		t.Fatalf("beatgrid = %+v, want single anchor", got.Beatgrid)
	}
	if got.Beatgrid[0].BPM != 128.05 || got.Beatgrid[0].PositionMs != 512 {
		t.Fatalf("anchor = %+v, want BPM=128.05 PositionMs=512", got.Beatgrid[0])
	}
	if got.BPM != 128.05 {
		t.Fatalf("AverageBpm = %v, want 128.05", got.BPM)
	}

	raw, err := os.ReadFile(xmlPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`Inizio="0.512"`, `Bpm="128.05"`, `Metro="4/4"`, `Battito="1"`, `AverageBpm="128.05"`} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %s", want)
		}
	}
	if strings.Contains(s, `Bpm="127.94"`) || strings.Contains(s, `Bpm="128.31"`) {
		t.Error("old TEMPO elements survived")
	}
	// Lock has no rekordbox XML counterpart - must not invent attrs.
	if strings.Contains(s, "LOCK") {
		t.Error("unexpected LOCK attr in rekordbox XML")
	}
	_ = otherPath
}

func TestApplyGridFixesRekordboxXML_HotcuesIntact(t *testing.T) {
	xmlPath, fixPath, _, _ := writeRBGridFixture(t)

	if _, err := ApplyGridFixesRekordboxXML(xmlPath, []GridFixUpdate{{Path: fixPath, BPM: 128.05, StartMs: 512}}); err != nil {
		t.Fatal(err)
	}
	byPath, _ := parseRBFile(t, xmlPath)
	got := byPath[fixPath]
	if len(got.Cues) != 2 {
		t.Fatalf("cues = %+v, want 2 preserved", got.Cues)
	}
	if got.Cues[0].Name != "Drop" || got.Cues[0].StartMs != 61250 || got.Cues[0].Hotcue != 0 {
		t.Errorf("hotcue mutated: %+v", got.Cues[0])
	}
	if got.Cues[1].Hotcue != -1 || got.Cues[1].StartMs != 122500 {
		t.Errorf("memory cue mutated: %+v", got.Cues[1])
	}
}

func TestApplyGridFixesRekordboxXML_UnmatchedUntouched(t *testing.T) {
	xmlPath, fixPath, otherPath, _ := writeRBGridFixture(t)

	if _, err := ApplyGridFixesRekordboxXML(xmlPath, []GridFixUpdate{{Path: fixPath, BPM: 128.05, StartMs: 512}}); err != nil {
		t.Fatal(err)
	}
	byPath, playlists := parseRBFile(t, xmlPath)
	other := byPath[otherPath]
	if other.BPM != 140 || len(other.Beatgrid) != 1 || other.Beatgrid[0].PositionMs != 100 || other.Beatgrid[0].BPM != 140 {
		t.Errorf("unmatched track mutated: BPM=%v grid=%+v", other.BPM, other.Beatgrid)
	}
	if len(other.Cues) != 1 || other.Cues[0].Name != "Intro" {
		t.Errorf("unmatched track cues mutated: %+v", other.Cues)
	}
	// Playlist TRACK refs (outside COLLECTION) still resolve.
	if len(playlists) != 1 || len(playlists[0].Paths) != 2 {
		t.Fatalf("playlists mutated: %+v", playlists)
	}
	if playlists[0].Paths[0] != fixPath || playlists[0].Paths[1] != otherPath {
		t.Errorf("playlist refs mutated: %+v", playlists[0].Paths)
	}
}

func TestApplyGridFixesRekordboxXML_InsertsTempoWhenAbsent(t *testing.T) {
	xmlPath, _, _, gridlessPath := writeRBGridFixture(t)

	res, err := ApplyGridFixesRekordboxXML(xmlPath, []GridFixUpdate{{Path: gridlessPath, BPM: 174, StartMs: 0}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 {
		t.Fatalf("Updated = %d, want 1", res.Updated)
	}
	byPath, _ := parseRBFile(t, xmlPath)
	got := byPath[gridlessPath]
	if len(got.Beatgrid) != 1 || got.Beatgrid[0].BPM != 174 || got.Beatgrid[0].PositionMs != 0 {
		t.Fatalf("beatgrid = %+v, want single 174 anchor at 0", got.Beatgrid)
	}
	if len(got.Cues) != 1 {
		t.Fatalf("cues = %+v, want 1 preserved", got.Cues)
	}
	// Rekordbox element order: TEMPO before POSITION_MARK inside the rewritten TRACK.
	raw, err := os.ReadFile(xmlPath)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	blk := s[strings.Index(s, `TrackID="3"`):]
	ti, mi := strings.Index(blk, "<TEMPO"), strings.Index(blk, "<POSITION_MARK")
	if ti < 0 || mi < 0 || ti > mi {
		t.Errorf("TEMPO not before POSITION_MARK (tempo@%d mark@%d)", ti, mi)
	}
}

func TestApplyGridFixesRekordboxXML_NoopAndAtomic(t *testing.T) {
	xmlPath, fixPath, _, _ := writeRBGridFixture(t)
	before, err := os.ReadFile(xmlPath)
	if err != nil {
		t.Fatal(err)
	}

	// Empty fixes → untouched file.
	if res, err := ApplyGridFixesRekordboxXML(xmlPath, nil); err != nil || res != (WritebackResult{}) {
		t.Fatalf("noop: res=%+v err=%v", res, err)
	}
	after, err := os.ReadFile(xmlPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("noop rewrote the file")
	}

	// Missing file → error, nothing created.
	missing := filepath.Join(t.TempDir(), "absent.xml")
	if _, err := ApplyGridFixesRekordboxXML(missing, []GridFixUpdate{{Path: fixPath, BPM: 120, StartMs: 0}}); err == nil {
		t.Error("expected error for missing file")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Error("missing-file apply left a file behind")
	}

	// Successful apply leaves no temp files beside the XML.
	if _, err := ApplyGridFixesRekordboxXML(xmlPath, []GridFixUpdate{{Path: fixPath, BPM: 120, StartMs: 0}}); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(filepath.Dir(xmlPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
