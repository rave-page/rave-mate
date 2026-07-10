package musiclib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const gridFixture = `<?xml version="1.0" encoding="UTF-8"?>
<NML VERSION="20">
  <HEAD COMPANY="native" PROGRAM="Traktor"></HEAD>
  <COLLECTION ENTRIES="3">
    <ENTRY TITLE="GridTrack" ARTIST="A">
      <LOCATION DIR="/:Music/:" FILE="a.mp3" VOLUME="C:"></LOCATION>
      <INFO GENRE="Techno" PLAYTIME="200"></INFO>
      <TEMPO BPM="127.500000" BPM_QUALITY="100.000000"></TEMPO>
      <CUE_V2 NAME="Cue A" DISPL_ORDER="0" TYPE="0" START="1234.5" LEN="0.000000" REPEATS="-1" HOTCUE="0"></CUE_V2>
      <CUE_V2 NAME="AutoGrid" DISPL_ORDER="0" TYPE="4" START="55.0" LEN="0.000000" REPEATS="-1" HOTCUE="-1"><GRID BPM="127.500000"></GRID></CUE_V2>
      <CUE_V2 NAME="AutoGrid" DISPL_ORDER="0" TYPE="4" START="30055.0" LEN="0.000000" REPEATS="-1" HOTCUE="-1"><GRID BPM="127.500000"></GRID></CUE_V2>
      <CUE_V2 NAME="Loop" DISPL_ORDER="0" TYPE="5" START="9000.0" LEN="4000.0" REPEATS="-1" HOTCUE="1"></CUE_V2>
    </ENTRY>
    <ENTRY TITLE="NoTempo" ARTIST="B">
      <LOCATION DIR="/:Music/:" FILE="b.mp3" VOLUME="C:"></LOCATION>
      <INFO GENRE="DnB"></INFO>
    </ENTRY>
    <ENTRY TITLE="Untouched" ARTIST="C">
      <LOCATION DIR="/:Music/:" FILE="c.mp3" VOLUME="C:"></LOCATION>
      <INFO GENRE="House"></INFO>
      <TEMPO BPM="122.000000"></TEMPO>
      <CUE_V2 NAME="Beat Marker" TYPE="4" START="10.0" LEN="0.000000" HOTCUE="-1"><GRID BPM="122.000000"></GRID></CUE_V2>
    </ENTRY>
  </COLLECTION>
</NML>`

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "collection.nml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestApplyGridFixes(t *testing.T) {
	path := writeFixture(t, gridFixture)
	fixes := []GridFixUpdate{
		{Path: resolveLocation("C:", "/:Music/:", "a.mp3"), BPM: 128, StartMs: 62.5, Lock: true},
		{Path: resolveLocation("C:", "/:Music/:", "b.mp3"), BPM: 174, StartMs: 100},
		{Path: resolveLocation("C:", "/:Music/:", "missing.mp3"), BPM: 90, StartMs: 0}, // unmatched
	}
	res, err := ApplyGridFixes(path, fixes)
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
		`BPM="128.000000"`:           true,  // new TEMPO + GRID for a
		`START="62.500000"`:          true,  // new grid marker position
		`BPM="174.000000"`:           true,  // created TEMPO + GRID for b
		`START="100.000000"`:         true,  // created grid marker
		`NAME="Cue A"`:               true,  // hotcue preserved
		`START="1234.5"`:             true,  // hotcue attrs byte-preserved
		`LEN="4000.0"`:               true,  // loop preserved
		`BPM="122.000000"`:           true,  // unrelated entry untouched
		`START="10.0"`:               true,  // unrelated grid untouched
		`LOCK="1"`:                   true,  // lock set on a
		`LOCK_MODIFICATION_TIME="20`: true,  // local timestamp stamped
		`BPM="127.500000"`:           false, // old tempo gone
		`START="55.0"`:               false, // old grid markers gone
		`START="30055.0"`:            false,
	}
	for substr, want := range contains {
		if strings.Contains(s, substr) != want {
			t.Errorf("contains(%q) = %v; want %v", substr, !want, want)
		}
	}
	counts := map[string]int{
		`TYPE="4"`:                 3, // a collapsed 2→1, b created 1, c untouched 1
		`NAME="AutoGrid"`:          2, // a + b (c keeps "Beat Marker")
		`LOCK="1"`:                 1, // only a locked
		`BPM_QUALITY="100.000000"`: 2, // a existing + b created
	}
	for substr, want := range counts {
		if got := strings.Count(s, substr); got != want {
			t.Errorf("count(%q) = %d; want %d", substr, got, want)
		}
	}
	// TEMPO created after INFO for b (fix_grids ~478-485).
	info := strings.Index(s, `GENRE="DnB"`)
	tempo := strings.Index(s, `BPM="174.000000"`)
	next := strings.Index(s, `TITLE="Untouched"`)
	if !(info >= 0 && info < tempo && tempo < next) {
		t.Errorf("TEMPO not placed after INFO: info=%d tempo=%d next=%d", info, tempo, next)
	}
	// Atomic write: no temp files left behind.
	tmps, err := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmps) != 0 {
		t.Errorf("temp files not cleaned: %v", tmps)
	}
}

func TestApplyGridFixesNoop(t *testing.T) {
	path := writeFixture(t, gridFixture)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := ApplyGridFixes(path, nil); err != nil || res.Updated != 0 {
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
