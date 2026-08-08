package musiclib

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const wbFixture = `<?xml version="1.0" encoding="UTF-8"?>
<NML VERSION="20">
  <HEAD COMPANY="native" PROGRAM="Traktor"></HEAD>
  <COLLECTION ENTRIES="1">
    <ENTRY TITLE="Old" ARTIST="A">
      <LOCATION DIR="/:Music/:" FILE="a.mp3" VOLUME="C:"></LOCATION>
      <INFO GENRE="OldGenre" KEY="Am" PLAYTIME="200"></INFO>
      <TEMPO BPM="120.000000"></TEMPO>
      <CUE_V2 NAME="old" TYPE="0" START="1000.0" LEN="0" HOTCUE="0"></CUE_V2>
    </ENTRY>
  </COLLECTION>
</NML>`

func TestMergeIntoCollectionFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "collection.nml")
	if err := os.WriteFile(path, []byte(wbFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	existingPath := resolveLocation("C:", "/:Music/:", "a.mp3")
	newPath := resolveLocation("C:", "/:Music/:", "b.mp3")

	updates := []Track{
		{ // upsert the existing track: new genre, BPM, and a memory cue (HOTCUE=-1)
			Path: existingPath, Title: "Old", Artist: "A", Genre: "NewGenre", BPM: 128,
			Beatgrid: []GridMarker{{PositionMs: 0, BPM: 128}},
			Cues:     []CuePoint{{Name: "Mem", Kind: CueHot, Hotcue: -1, StartMs: 500}},
		},
		{ // brand-new track → appended
			Path: newPath, Title: "NewTrack", Artist: "B", Genre: "Drum & Bass", BPM: 174,
		},
	}

	res, err := MergeIntoCollectionFile(path, updates)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.Updated != 1 || res.Added != 1 {
		t.Fatalf("counts: updated=%d added=%d; want 1/1", res.Updated, res.Added)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	checks := map[string]bool{
		`ENTRIES="2"`:      true,  // count bumped by the appended track
		`GENRE="NewGenre"`: true,  // existing track's genre overwritten
		`BPM="128`:         true,  // existing track's TEMPO replaced
		`HOTCUE="-1"`:      true,  // memory cue written
		`NewTrack`:         true,  // appended track title
		`OldGenre`:         false, // old genre gone
		`BPM="120`:         false, // old TEMPO gone
		`NAME="old"`:       false, // old cue replaced
	}
	for substr, want := range checks {
		if strings.Contains(s, substr) != want {
			t.Errorf("contains(%q) = %v; want %v", substr, !want, want)
		}
	}

	// Re-running with the same existing track must update, not duplicate (idempotent identity).
	res2, err := MergeIntoCollectionFile(path, updates[:1])
	if err != nil {
		t.Fatalf("re-merge: %v", err)
	}
	if res2.Updated != 1 || res2.Added != 0 {
		t.Errorf("re-merge counts: updated=%d added=%d; want 1/0", res2.Updated, res2.Added)
	}
}

// Grid cues never double on a round-trip: the importer records every TYPE-4 in both
// t.Cues (CueGrid) and t.Beatgrid, so an export that emitted both grew grids 2^n per
// sync. nmlCues emits grids from Beatgrid only, with per-marker GRID BPM.
func TestNMLCuesNoGridDoubling(t *testing.T) {
	fixture := `<?xml version="1.0" encoding="UTF-8"?>
<NML VERSION="20"><COLLECTION ENTRIES="1">
<ENTRY TITLE="Flex" ARTIST="A">
  <LOCATION DIR="/:Music/:" FILE="flex.mp3" VOLUME="C:"></LOCATION>
  <TEMPO BPM="137.004074"></TEMPO>
  <CUE_V2 NAME="Beat Marker" DISPL_ORDER="0" TYPE="4" START="53.76" LEN="0" REPEATS="-1" HOTCUE="-1"><GRID BPM="119.988373"></GRID></CUE_V2>
  <CUE_V2 NAME="Beat Marker" DISPL_ORDER="0" TYPE="4" START="28082.12" LEN="0" REPEATS="-1" HOTCUE="-1"><GRID BPM="137.002792"></GRID></CUE_V2>
  <CUE_V2 NAME="Drop" DISPL_ORDER="0" TYPE="0" START="30000.0" LEN="0" REPEATS="-1" HOTCUE="0"></CUE_V2>
</ENTRY>
</COLLECTION></NML>`
	var tr Track
	if _, err := ParseCollection(strings.NewReader(fixture), func(x Track) { tr = x }); err != nil {
		t.Fatal(err)
	}
	// importer keeps per-marker GRID BPM, not the entry TEMPO
	if len(tr.Beatgrid) != 2 || tr.Beatgrid[0].BPM != 119.988373 || tr.Beatgrid[1].BPM != 137.002792 {
		t.Fatalf("beatgrid = %+v, want per-marker BPMs", tr.Beatgrid)
	}
	cues := nmlCues(tr)
	grids := 0
	for _, c := range cues {
		if c.Type == 4 {
			grids++
			if c.Grid == nil {
				t.Errorf("grid cue missing GRID child: %+v", c)
			}
			if c.Hotcue != -1 {
				t.Errorf("grid cue on a pad: %+v", c)
			}
		}
	}
	if grids != 2 {
		t.Fatalf("grid cues = %d, want 2 (no doubling)", grids)
	}
	if len(cues) != 3 {
		t.Fatalf("cues = %d, want 3 (2 grids + 1 hotcue)", len(cues))
	}
	for i := 1; i < len(cues); i++ { // native file order
		a, _ := strconv.ParseFloat(cues[i-1].Start, 64)
		b, _ := strconv.ParseFloat(cues[i].Start, 64)
		if a > b {
			t.Fatalf("cues not ascending by START: %+v", cues)
		}
	}
}

// Merge write-back rewrites TEMPO BPM in place (quality attrs survive) and leaves the
// grid/cues alone for a canonical track without cue data.
func TestMergeTempoInPlaceAndCueless(t *testing.T) {
	path := writeFixture(t, gridFixture)
	up := Track{Path: resolveLocation("C:", "/:Music/:", "a.mp3"), Title: "GridTrack", Artist: "A", BPM: 128}
	res, err := MergeIntoCollectionFile(path, []Track{up})
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 {
		t.Fatalf("updated=%d want 1", res.Updated)
	}
	s := readFileStr(t, path)
	if !strings.Contains(s, `BPM="128.000000"`) || !strings.Contains(s, `BPM_QUALITY="100.000000"`) {
		t.Errorf("TEMPO not rewritten in place:\n%s", s)
	}
	for _, keep := range []string{`START="55.0"`, `START="30055.0"`, `NAME="Cue A"`, `NAME="Loop"`} {
		if !strings.Contains(s, keep) {
			t.Errorf("cue-less merge wiped %s", keep)
		}
	}
}

// Merge write-back with cue data regenerates CUE_V2 in native shape: GRID child on the
// grid cue, DISPL_ORDER/REPEATS attrs, no doubled markers.
func TestMergeCuesNativeShape(t *testing.T) {
	path := writeFixture(t, gridFixture)
	up := Track{Path: resolveLocation("C:", "/:Music/:", "a.mp3"), Title: "GridTrack", Artist: "A", BPM: 128,
		Beatgrid: []GridMarker{{PositionMs: 62.5, BPM: 128}},
		Cues:     []CuePoint{{Name: "Drop", Kind: CueHot, StartMs: 30062.5, Hotcue: 0}}}
	if _, err := MergeIntoCollectionFile(path, []Track{up}); err != nil {
		t.Fatal(err)
	}
	s := readFileStr(t, path)
	if !strings.Contains(s, `NAME="AutoGrid" DISPL_ORDER="0" TYPE="4" START="62.500000" LEN="0.000000" REPEATS="-1" HOTCUE="-1"`) {
		t.Errorf("grid cue not native shape:\n%s", s)
	}
	if !strings.Contains(s, `<GRID BPM="128.000000">`) {
		t.Error("grid cue missing GRID child")
	}
	if !strings.Contains(s, `NAME="Drop" DISPL_ORDER="0" TYPE="0" START="30062.500000" LEN="0.000000" REPEATS="-1" HOTCUE="0"`) {
		t.Errorf("hotcue not native shape:\n%s", s)
	}
	if n := strings.Count(s[strings.Index(s, "a.mp3"):strings.Index(s, "b.mp3")], `TYPE="4"`); n != 1 {
		t.Errorf("grid cues in merged entry = %d, want 1", n)
	}
}
