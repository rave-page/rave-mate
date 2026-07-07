package musiclib

import (
	"os"
	"path/filepath"
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
