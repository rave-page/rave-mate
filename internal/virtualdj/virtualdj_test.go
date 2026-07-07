package virtualdj

import (
	"strings"
	"testing"
)

const sampleDB = `<?xml version="1.0" encoding="UTF-8"?>
<VirtualDJ_Database Version="2024">
  <Song FilePath="C:\Music\a.mp3" FileSize="123">
    <Tags Author="Artist A" Title="Title A" Genre="Techno" Album="Album A" Key="Am" Bpm="0.48" Year="2020"/>
    <Scan Bpm="0.5" AltBpm="0.25" Key="8A"/>
    <Infos SongLength="305.5" Bitrate="320" PlayCount="7"/>
    <Comment>nice</Comment>
  </Song>
  <Song FilePath="C:\Music\b.mp3">
    <Tags Author="Artist B" Title="Title B" Bpm="0.4"/>
  </Song>
</VirtualDJ_Database>`

func TestParseDatabase(t *testing.T) {
	tracks, err := ParseDatabase(strings.NewReader(sampleDB))
	if err != nil {
		t.Fatalf("ParseDatabase: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("want 2 tracks, got %d", len(tracks))
	}

	a := tracks[0]
	// Scan.Bpm 0.5 s/beat → 120 bpm (the critical units conversion).
	if a.BPM != 120 {
		t.Errorf("track A BPM: want 120, got %v", a.BPM)
	}
	if a.Title != "Title A" || a.Artist != "Artist A" || a.Album != "Album A" || a.Genre != "Techno" {
		t.Errorf("track A metadata mismatch: %+v", a)
	}
	if a.Key != "8A" { // Scan.Key wins over Tags.Key
		t.Errorf("track A key: want 8A, got %q", a.Key)
	}
	if a.Path != `C:\Music\a.mp3` {
		t.Errorf("track A path: %q", a.Path)
	}
	if a.LengthSec != 306 { // round(305.5)
		t.Errorf("track A length: want 306, got %d", a.LengthSec)
	}
	if a.PlayCount != 7 {
		t.Errorf("track A playcount: want 7, got %d", a.PlayCount)
	}

	b := tracks[1]
	// No <Scan> → falls back to Tags.Bpm 0.4 s/beat → 150 bpm.
	if b.BPM != 150 {
		t.Errorf("track B BPM (Tags fallback): want 150, got %v", b.BPM)
	}
	if b.Key != "" {
		t.Errorf("track B key: want empty, got %q", b.Key)
	}
}
