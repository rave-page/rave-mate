package musiclib

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shared replacement cue set: hotcue pad 2 + memory cue + loop pad 3.
func testCueSet() []CuePoint {
	return []CuePoint{
		{Name: "Drop", Kind: CueHot, StartMs: 61250, Hotcue: 2},
		{Name: "", Kind: CuePlain, StartMs: 90000, Hotcue: -1},
		{Name: "Build", Kind: CueLoop, StartMs: 120000, LenMs: 8000, Hotcue: 3},
		{Name: "Grid", Kind: CueGrid, StartMs: 0, Hotcue: -1}, // must be ignored
	}
}

// ── Traktor NML ──

func TestApplyCuesNML(t *testing.T) {
	path := writeFixture(t, gridFixture)
	up := CueUpdate{Path: resolveLocation("C:", "/:Music/:", "a.mp3"), Cues: testCueSet()}
	res, err := ApplyCuesNML(path, []CueUpdate{up,
		{Path: resolveLocation("C:", "/:Music/:", "missing.mp3"), Cues: testCueSet()}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 {
		t.Fatalf("updated=%d; want 1", res.Updated)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	byPath := map[string]Track{}
	if _, err := ParseCollection(f, func(tr Track) { byPath[tr.Path] = tr }); err != nil {
		t.Fatal(err)
	}
	got := byPath[up.Path]
	var musical []CuePoint // the NML reader keeps grid cues in Cues too - filter them
	for _, c := range got.Cues {
		if c.Kind != CueGrid {
			musical = append(musical, c)
		}
	}
	if len(musical) != 3 {
		t.Fatalf("cues = %+v, want 3", musical)
	}
	if musical[0].Name != "Drop" || musical[0].Kind != CueHot || musical[0].Hotcue != 2 || musical[0].StartMs != 61250 {
		t.Errorf("hotcue = %+v", musical[0])
	}
	if musical[1].Kind != CuePlain || musical[1].Hotcue != -1 {
		t.Errorf("memory cue = %+v", musical[1])
	}
	if musical[2].Kind != CueLoop || musical[2].LenMs != 8000 || musical[2].Hotcue != 3 {
		t.Errorf("loop = %+v", musical[2])
	}
	// grid (2 TYPE-4 anchors) + tempo survive; old musical cues gone; neighbors untouched
	if len(got.Beatgrid) != 2 || got.Beatgrid[0].PositionMs != 55 {
		t.Errorf("beatgrid mutated: %+v", got.Beatgrid)
	}
	s := readFileStr(t, path)
	for _, want := range []string{`BPM="127.500000"`, `START="55.0"`, `START="30055.0"`,
		`NAME="n.n."`, `BPM="122.000000"`, `START="10.0"`} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %s", want)
		}
	}
	for _, gone := range []string{`NAME="Cue A"`, `START="1234.5"`, `LEN="4000.0"`} {
		if strings.Contains(s, gone) {
			t.Errorf("old cue survived: %s", gone)
		}
	}
	unrelated := byPath[resolveLocation("C:", "/:Music/:", "c.mp3")]
	for _, c := range unrelated.Cues {
		if c.Kind != CueGrid {
			t.Errorf("unrelated entry gained cue %+v", c)
		}
	}
	if len(unrelated.Beatgrid) != 1 {
		t.Errorf("unrelated entry grid mutated: %+v", unrelated.Beatgrid)
	}
}

// ── Rekordbox XML ──

func TestApplyCuesRekordboxXML(t *testing.T) {
	xmlPath, fixPath, otherPath, _ := writeRBGridFixture(t)
	res, err := ApplyCuesRekordboxXML(xmlPath, []CueUpdate{{Path: fixPath, Cues: testCueSet()}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 {
		t.Fatalf("updated=%d; want 1", res.Updated)
	}
	byPath, playlists := parseRBFile(t, xmlPath)
	got := byPath[fixPath]
	if len(got.Cues) != 3 {
		t.Fatalf("cues = %+v, want 3", got.Cues)
	}
	if got.Cues[0].Kind != CueHot || got.Cues[0].Hotcue != 2 || got.Cues[0].StartMs != 61250 {
		t.Errorf("hotcue = %+v", got.Cues[0])
	}
	if got.Cues[1].Kind != CuePlain || got.Cues[1].Hotcue != -1 {
		t.Errorf("memory cue = %+v", got.Cues[1])
	}
	if got.Cues[2].Kind != CueLoop || got.Cues[2].LenMs != 8000 {
		t.Errorf("loop = %+v", got.Cues[2])
	}
	// grid untouched, old marks gone, TEMPO stays before POSITION_MARK
	if len(got.Beatgrid) != 2 || got.Beatgrid[0].PositionMs != 25 {
		t.Errorf("beatgrid mutated: %+v", got.Beatgrid)
	}
	s := readFileStr(t, xmlPath)
	if strings.Contains(s, `Start="61.250" Num="0"`) || strings.Contains(s, `Start="122.500"`) {
		t.Error("old POSITION_MARKs survived")
	}
	blk := s[strings.Index(s, `TrackID="1"`):]
	ti, mi := strings.Index(blk, "<TEMPO"), strings.Index(blk, "<POSITION_MARK")
	if ti < 0 || mi < 0 || ti > mi {
		t.Errorf("TEMPO not before POSITION_MARK (tempo@%d mark@%d)", ti, mi)
	}
	// loop mark carries End = start+len seconds
	if !strings.Contains(s, `End="128.000"`) {
		t.Error("loop POSITION_MARK missing End")
	}
	other := byPath[otherPath]
	if len(other.Cues) != 1 || other.Cues[0].Name != "Intro" {
		t.Errorf("unmatched track mutated: %+v", other.Cues)
	}
	if len(playlists) != 1 || len(playlists[0].Paths) != 2 {
		t.Errorf("playlists mutated: %+v", playlists)
	}
}

// ── VirtualDJ ──

func vdjCueFixture(aPath, bPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<VirtualDJ_Database Version="2024">
 <Song FilePath="%s" FileSize="1024">
  <Tags Author="A" Title="Alpha" Bpm="0.468750"/>
  <Infos SongLength="240.5"/>
  <Scan Bpm="0.468750" Key="Am"/>
  <Poi Pos="0.500000" Type="beatgrid" Bpm="0.468750"/>
  <Poi Pos="30.0" Type="cue" Num="1" Name="Old"/>
  <Poi Pos="45.0" Type="remix" Name="OldRemix"/>
 </Song>
 <Song FilePath="%s" FileSize="2048">
  <Tags Author="B" Title="Beta" Bpm="0.344828"/>
  <Poi Pos="1.0" Type="beatgrid" Bpm="0.344828"/>
  <Poi Pos="10.0" Type="cue" Num="2" Name="Keep"/>
 </Song>
</VirtualDJ_Database>
`, aPath, bPath)
}

func TestApplyCuesVirtualDJ(t *testing.T) {
	dir := t.TempDir()
	aPath := filepath.Join(dir, "a.mp3")
	bPath := filepath.Join(dir, "b.mp3")
	dbPath := filepath.Join(dir, "database.xml")
	if err := os.WriteFile(dbPath, []byte(vdjCueFixture(aPath, bPath)), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ApplyCuesVirtualDJ(dbPath, []CueUpdate{{Path: aPath, BPM: 128, Cues: testCueSet()}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 {
		t.Fatalf("updated=%d; want 1", res.Updated)
	}
	f, err := os.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	byPath := map[string]Track{}
	if _, err := ParseVirtualDJ(f, func(tr Track) { byPath[tr.Path] = tr }); err != nil {
		t.Fatal(err)
	}
	got := byPath[aPath]
	if len(got.Cues) != 3 {
		t.Fatalf("cues = %+v, want 3", got.Cues)
	}
	if got.Cues[0].Kind != CueHot || got.Cues[0].Hotcue != 2 || got.Cues[0].StartMs != 61250 {
		t.Errorf("hotcue = %+v", got.Cues[0])
	}
	if got.Cues[1].Kind != CuePlain || got.Cues[1].Hotcue != -1 {
		t.Errorf("remix point = %+v", got.Cues[1])
	}
	if got.Cues[2].Kind != CueLoop || got.Cues[2].Hotcue != 3 {
		t.Errorf("loop = %+v", got.Cues[2])
	}
	if len(got.Beatgrid) != 1 || got.Beatgrid[0].PositionMs != 500 {
		t.Errorf("beatgrid mutated: %+v", got.Beatgrid)
	}
	s := readFileStr(t, dbPath)
	// 1-based pad numbering + remix type + loop Size in beats (8000ms @128 = 17.067)
	for _, want := range []string{`Num="3"`, `Type="remix"`, `Type="loop"`, `Size="17.067"`, `Num="4"`} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %s", want)
		}
	}
	for _, gone := range []string{`Name="Old"`, `Name="OldRemix"`} {
		if strings.Contains(s, gone) {
			t.Errorf("old Poi survived: %s", gone)
		}
	}
	other := byPath[bPath]
	if len(other.Cues) != 1 || other.Cues[0].Name != "Keep" || other.Cues[0].Hotcue != 1 {
		t.Errorf("unmatched song mutated: %+v", other.Cues)
	}
}

// VDJ pad numbering is 1-based on disk, 0-based in the model - full round-trip.
func TestVDJPadNumberRoundTrip(t *testing.T) {
	pois := vdjCuePois([]CuePoint{{Kind: CueHot, StartMs: 1000, Hotcue: 0}}, 0)
	if len(pois) != 1 || pois[0].Num != 1 {
		t.Fatalf("pois = %+v, want Num=1 for slot 0", pois)
	}
}

// anchorFixture: one entry, single AutoGrid at 100ms on a 120 BPM lattice (period 500ms).
const anchorFixture = `<?xml version="1.0" encoding="UTF-8"?>
<NML VERSION="20">
  <HEAD COMPANY="native" PROGRAM="Traktor"></HEAD>
  <COLLECTION ENTRIES="1">
    <ENTRY TITLE="AnchorTrack" ARTIST="A">
      <LOCATION DIR="/:Music/:" FILE="anchor.mp3" VOLUME="C:"></LOCATION>
      <INFO GENRE="DnB" PLAYTIME="200"></INFO>
      <TEMPO BPM="120.000000" BPM_QUALITY="100.000000"></TEMPO>
      <CUE_V2 NAME="Cue A" DISPL_ORDER="0" TYPE="0" START="1234.5" LEN="0.000000" REPEATS="-1" HOTCUE="0"></CUE_V2>
      <CUE_V2 NAME="AutoGrid" DISPL_ORDER="0" TYPE="4" START="100.0" LEN="0.000000" REPEATS="-1" HOTCUE="-1"><GRID BPM="120.000000"></GRID></CUE_V2>
    </ENTRY>
  </COLLECTION>
</NML>`

// GridAnchor writes Traktor's native two-cue form: a TYPE-4 grid cue (HOTCUE=-1, GRID
// child) on the EXISTING lattice's point nearest the earliest hotcue - the hotcue itself
// stays a plain pad cue at its own position, so an off-grid cue never shifts the grid.
func TestApplyCuesNMLGridAnchor(t *testing.T) {
	path := writeFixture(t, anchorFixture)
	up := CueUpdate{Path: resolveLocation("C:", "/:Music/:", "anchor.mp3"), GridAnchor: true,
		Cues: []CuePoint{
			{Name: "Intro", Kind: CueHot, StartMs: 30110, Hotcue: 0}, // 10ms off the 100+k*500 lattice
			{Name: "Drop", Kind: CueHot, StartMs: 90100, Hotcue: 1},
		}}
	if _, err := ApplyCuesNML(path, []CueUpdate{up}); err != nil {
		t.Fatal(err)
	}
	s := readFileStr(t, path)
	// grid cue re-anchored near the first hotcue, phase preserved (30100, not 30110), no pad
	if !strings.Contains(s, `NAME="AutoGrid" DISPL_ORDER="0" TYPE="4" START="30100.000000" LEN="0.000000" REPEATS="-1" HOTCUE="-1"`) {
		t.Errorf("re-anchored grid cue wrong:\n%s", s)
	}
	if !strings.Contains(s, `<GRID BPM="120.000000">`) {
		t.Error("anchor missing GRID child BPM")
	}
	// the hotcue stays a PLAIN pad cue at its own position - never a padded TYPE-4
	if !strings.Contains(s, `NAME="Intro" DISPL_ORDER="0" TYPE="0" START="30110.000000" LEN="0.000000" REPEATS="-1" HOTCUE="0"`) {
		t.Errorf("earliest hotcue not written as plain pad cue:\n%s", s)
	}
	if strings.Contains(s, `START="100.0"`) {
		t.Error("old grid cue survived the re-anchor")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	byPath := map[string]Track{}
	if _, err := ParseCollection(f, func(tr Track) { byPath[tr.Path] = tr }); err != nil {
		t.Fatal(err)
	}
	got := byPath[up.Path]
	if len(got.Beatgrid) != 1 || got.Beatgrid[0].PositionMs != 30100 {
		t.Errorf("beatgrid = %+v, want single anchor at 30100", got.Beatgrid)
	}
	var pads int
	for _, c := range got.Cues {
		if c.Kind == CueHot && c.Hotcue >= 0 {
			pads++
			if c.Type == 4 {
				t.Errorf("padded TYPE-4 written: %+v", c)
			}
		}
	}
	if pads != 2 {
		t.Errorf("pads = %d, want 2", pads)
	}

	// no hotcues in the update → grid passthrough
	path2 := writeFixture(t, anchorFixture)
	up2 := CueUpdate{Path: up.Path, GridAnchor: true,
		Cues: []CuePoint{{Kind: CuePlain, StartMs: 5000, Hotcue: -1}}}
	if _, err := ApplyCuesNML(path2, []CueUpdate{up2}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFileStr(t, path2), `START="100.0"`) {
		t.Error("memory-only update dropped the grid")
	}
}

// Entries with several grid cues (flexible/manual grids) never re-anchor - the
// markers pass through even with GridAnchor on.
func TestApplyCuesNMLGridAnchorMultiGridProtected(t *testing.T) {
	path := writeFixture(t, gridFixture) // a.mp3 carries TWO TYPE-4 markers
	up := CueUpdate{Path: resolveLocation("C:", "/:Music/:", "a.mp3"), GridAnchor: true,
		Cues: []CuePoint{{Name: "Intro", Kind: CueHot, StartMs: 61250, Hotcue: 0}}}
	if _, err := ApplyCuesNML(path, []CueUpdate{up}); err != nil {
		t.Fatal(err)
	}
	s := readFileStr(t, path)
	for _, keep := range []string{`START="55.0"`, `START="30055.0"`} {
		if !strings.Contains(s, keep) {
			t.Errorf("flexible grid marker dropped: %s missing", keep)
		}
	}
	if !strings.Contains(s, `NAME="Intro" DISPL_ORDER="0" TYPE="0" START="61250.000000" LEN="0.000000" REPEATS="-1" HOTCUE="0"`) {
		t.Error("hotcue not written on the protected entry")
	}
}

// A passthrough grid cue always loses its pad: the old single-cue anchor form
// (TYPE-4 with HOTCUE>=0, which Traktor never keeps) degrades cleanly into the
// two-cue form on the next write, with the pad on the emitted plain cue.
func TestApplyCuesNMLPaddedGridCueSplits(t *testing.T) {
	fixture := strings.Replace(anchorFixture,
		`NAME="AutoGrid" DISPL_ORDER="0" TYPE="4" START="100.0" LEN="0.000000" REPEATS="-1" HOTCUE="-1"`,
		`NAME="AutoGrid" DISPL_ORDER="0" TYPE="4" START="100.0" LEN="0.000000" REPEATS="-1" HOTCUE="3"`, 1)
	path := writeFixture(t, fixture)
	up := CueUpdate{Path: resolveLocation("C:", "/:Music/:", "anchor.mp3"),
		Cues: []CuePoint{ // renumbered set: the imported padded anchor is now slot 0
			{Name: "AutoGrid", Kind: CueHot, Type: 4, StartMs: 100, Hotcue: 0},
			{Name: "Drop", Kind: CueHot, StartMs: 60100, Hotcue: 1},
		}}
	if _, err := ApplyCuesNML(path, []CueUpdate{up}); err != nil {
		t.Fatal(err)
	}
	s := readFileStr(t, path)
	if !strings.Contains(s, `TYPE="4" START="100.0" LEN="0.000000" REPEATS="-1" HOTCUE="-1"`) {
		t.Errorf("passthrough grid cue kept its pad:\n%s", s)
	}
	if !strings.Contains(s, `NAME="AutoGrid" DISPL_ORDER="0" TYPE="0" START="100.000000" LEN="0.000000" REPEATS="-1" HOTCUE="0"`) {
		t.Errorf("split pad cue missing:\n%s", s)
	}
}

func TestApplyCuesNoop(t *testing.T) {
	path := writeFixture(t, gridFixture)
	before := readFileStr(t, path)
	if res, err := ApplyCuesNML(path, nil); err != nil || res != (WritebackResult{}) {
		t.Fatalf("noop: res=%+v err=%v", res, err)
	}
	if readFileStr(t, path) != before {
		t.Error("noop rewrote the file")
	}
}

func readFileStr(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
