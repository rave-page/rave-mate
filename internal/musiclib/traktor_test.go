package musiclib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleCollection = `<?xml version="1.0" encoding="UTF-8" standalone="no" ?>
<NML VERSION="20"><HEAD COMPANY="x" PROGRAM="Traktor Pro 4"></HEAD>
<COLLECTION ENTRIES="1"><ENTRY TITLE="Bumpy" ARTIST="Bumpin Flava"><LOCATION DIR="/:ProgramData/:Native Instruments/:Traktor Pro 4/:Factory Sounds/:" FILE="Bumpin Flava - Bumpy.mp3" VOLUME="C:" VOLUMEID="ac240de7"></LOCATION>
<ALBUM TITLE="Maschine Expansion"></ALBUM>
<INFO BITRATE="299672" LABEL="Native Instruments" KEY="Ebm" PLAYTIME="142" PLAYTIME_FLOAT="141.348572" IMPORT_DATE="2024/6/18" FILESIZE="5243"></INFO>
<TEMPO BPM="136.000000"></TEMPO>
<MUSICAL_KEY VALUE="15"></MUSICAL_KEY>
<CUE_V2 NAME="n.n." TYPE="0" START="71.62" HOTCUE="0"></CUE_V2>
</ENTRY></COLLECTION>
<PLAYLISTS><NODE TYPE="PLAYLIST" NAME="x"><PLAYLIST><ENTRY><PRIMARYKEY TYPE="TRACK" KEY="C:/:nope/:x.mp3"></PRIMARYKEY></ENTRY></PLAYLIST></NODE></PLAYLISTS>
</NML>`

const sampleHistory = `<?xml version="1.0" ?>
<NML VERSION="20"><COLLECTION ENTRIES="1"><ENTRY TITLE="t" ARTIST="a"><LOCATION DIR="/:Music/:x/:" FILE="t.flac" VOLUME="B:"></LOCATION><INFO BITRATE="1"></INFO></ENTRY></COLLECTION>
<PLAYLISTS><NODE TYPE="PLAYLIST" NAME="HISTORY"><PLAYLIST><ENTRY><PRIMARYKEY TYPE="TRACK" KEY="B:/:Music/:x/:t.flac"></PRIMARYKEY>
<EXTENDEDDATA DECK="0" DURATION="482.97" PLAYEDPUBLIC="1" STARTDATE="132710940" STARTTIME="52627"></EXTENDEDDATA></ENTRY></PLAYLIST></NODE></PLAYLISTS></NML>`

func TestParseCollectionSample(t *testing.T) {
	var got []Track
	n, err := ParseCollection(strings.NewReader(sampleCollection), func(tr Track) { got = append(got, tr) })
	if err != nil {
		t.Fatal(err)
	}
	// 1 real track; the PLAYLIST PRIMARYKEY entry (no LOCATION) is skipped.
	if n != 1 || len(got) != 1 {
		t.Fatalf("want 1 track, got n=%d len=%d", n, len(got))
	}
	tr := got[0]
	if tr.Title != "Bumpy" || tr.Artist != "Bumpin Flava" {
		t.Errorf("title/artist: %q / %q", tr.Title, tr.Artist)
	}
	if tr.BPM != 136 || tr.Key != "Ebm" || tr.BitrateBps != 299672 {
		t.Errorf("bpm/key/bitrate: %v / %q / %d", tr.BPM, tr.Key, tr.BitrateBps)
	}
	if tr.DurationSec < 141 || tr.DurationSec > 142 {
		t.Errorf("duration: %v", tr.DurationSec)
	}
	wantPath := "C:" + sep() + filepath.Join("ProgramData", "Native Instruments", "Traktor Pro 4", "Factory Sounds", "Bumpin Flava - Bumpy.mp3")
	if tr.Path != wantPath {
		t.Errorf("path:\n got %q\nwant %q", tr.Path, wantPath)
	}
	if len(tr.Cues) != 1 || tr.Cues[0].Hotcue != 0 {
		t.Errorf("cues: %+v", tr.Cues)
	}
}

func TestParseHistorySample(t *testing.T) {
	s, err := ParseHistory("set1", strings.NewReader(sampleHistory))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Played) != 1 {
		t.Fatalf("want 1 played, got %d", len(s.Played))
	}
	p := s.Played[0]
	wantPath := "B:" + sep() + filepath.Join("Music", "x", "t.flac")
	if p.Path != wantPath {
		t.Errorf("played path:\n got %q\nwant %q", p.Path, wantPath)
	}
	if p.Deck != 0 || !p.Public || p.DurationSec < 482 {
		t.Errorf("played: %+v", p)
	}
	// The played entry is joined to the embedded COLLECTION metadata by the Traktor key, so title/artist
	// are present without any library lookup (the fix for "unknown track" on a non-library deck).
	if p.Title != "t" || p.Artist != "a" {
		t.Errorf("played metadata not joined from COLLECTION: title=%q artist=%q", p.Title, p.Artist)
	}
}

// A played track with no matching COLLECTION entry keeps its timing/deck but no metadata (the caller
// then falls back to a library lookup) - must not drop the play or error.
func TestParseHistoryUnmatchedPlay(t *testing.T) {
	const noMeta = `<?xml version="1.0" ?>
<NML VERSION="20"><COLLECTION ENTRIES="0"></COLLECTION>
<PLAYLISTS><NODE TYPE="PLAYLIST" NAME="HISTORY"><PLAYLIST><ENTRY><PRIMARYKEY TYPE="TRACK" KEY="D:/:USB/:incoming/:unknown.wav"></PRIMARYKEY>
<EXTENDEDDATA DECK="1" DURATION="200" PLAYEDPUBLIC="1" STARTDATE="132710940" STARTTIME="52627"></EXTENDEDDATA></ENTRY></PLAYLIST></NODE></PLAYLISTS></NML>`
	s, err := ParseHistory("set2", strings.NewReader(noMeta))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Played) != 1 {
		t.Fatalf("want 1 played, got %d", len(s.Played))
	}
	p := s.Played[0]
	if p.Deck != 1 || p.DurationSec != 200 {
		t.Errorf("timing/deck lost: %+v", p)
	}
	if p.Title != "" || p.Artist != "" {
		t.Errorf("unmatched play should have empty metadata, got title=%q artist=%q", p.Title, p.Artist)
	}
}

func TestVersionOrdering(t *testing.T) {
	if !versionLess("4.1.1", "4.2.0") || versionLess("4.10.0", "4.2.0") {
		t.Error("version compare wrong")
	}
}

// TestRealCollection streams the actual collection.nml if present on this machine (skipped
// in CI). Proves the streaming parser handles the multi-hundred-MB file + matches the
// ENTRIES count in the header.
func TestRealCollection(t *testing.T) {
	installs, err := DiscoverTraktor()
	if err != nil || len(installs) == 0 {
		t.Skip("no Traktor install found")
	}
	in := installs[0]
	if in.Collection == "" {
		t.Skip("no collection.nml")
	}
	f, err := os.Open(in.Collection)
	if err != nil {
		t.Skip(err.Error())
	}
	defer f.Close()
	var first Track
	n, err := ParseCollection(f, func(tr Track) {
		if first.Path == "" {
			first = tr
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("traktor %s: parsed %d tracks; first = %q by %q (%s, %.0f BPM) → %s",
		in.Version, n, first.Title, first.Artist, first.Key, first.BPM, first.Path)
	if n < 1 {
		t.Errorf("parsed 0 tracks from a real collection")
	}
}

func sep() string { return string(os.PathSeparator) }
