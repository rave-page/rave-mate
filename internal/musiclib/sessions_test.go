package musiclib

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// historyNML builds a minimal Traktor history NML with one played track entry.
func historyNML(title, path string, durationSec float64) string {
	return fmt.Sprintf(`<?xml version="1.0" ?>
<NML VERSION="20">
<COLLECTION ENTRIES="1">
  <ENTRY TITLE=%q ARTIST="Test Artist">
    <LOCATION DIR="/:Music/:" FILE=%q VOLUME="C:"></LOCATION>
    <INFO BITRATE="320000"></INFO>
  </ENTRY>
</COLLECTION>
<PLAYLISTS>
  <NODE TYPE="PLAYLIST" NAME="HISTORY">
    <PLAYLIST>
      <ENTRY>
        <PRIMARYKEY TYPE="TRACK" KEY=%q></PRIMARYKEY>
        <EXTENDEDDATA DECK="1" DURATION="%f" PLAYEDPUBLIC="1" STARTDATE="132710940" STARTTIME="52627"></EXTENDEDDATA>
      </ENTRY>
    </PLAYLIST>
  </NODE>
</PLAYLISTS>
</NML>`, title, title+".flac", path, durationSec)
}

func writeHistoryFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseHistoryFilename(t *testing.T) {
	cases := []struct {
		name    string
		wantOK  bool
		wantY   int
		wantMon time.Month
		wantD   int
	}{
		{"history_2025y02m28d_14h47m09s.nml", true, 2025, time.February, 28},
		{"history_2026y06m04d_01h26m44s.nml", true, 2026, time.June, 4},
		{"collection.nml", false, 0, 0, 0},
		{"history_bad.nml", false, 0, 0, 0},
	}
	for _, c := range cases {
		ts, ok := ParseHistoryFilename(c.name)
		if ok != c.wantOK {
			t.Errorf("%s: ok=%v want %v", c.name, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if ts.Year() != c.wantY || ts.Month() != c.wantMon || ts.Day() != c.wantD {
			t.Errorf("%s: got %v, want %d-%02d-%02d", c.name, ts, c.wantY, c.wantMon, c.wantD)
		}
	}
}

func TestLoadSessionsOrder(t *testing.T) {
	dir := t.TempDir()

	// Three history files: oldest, newest, middle - written out of order.
	files := []struct {
		name     string
		title    string
		duration float64
	}{
		{"history_2025y01m10d_20h00m00s.nml", "OldSet", 3600.0},
		{"history_2025y06m15d_22h30m00s.nml", "NewSet", 5400.0},
		{"history_2025y03m20d_21h00m00s.nml", "MidSet", 4200.0},
	}
	for _, f := range files {
		path := "C:/:Music/:" + f.title + ".flac"
		writeHistoryFile(t, dir, f.name, historyNML(f.title, path, f.duration))
	}

	sessions, err := LoadSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("want 3 sessions, got %d", len(sessions))
	}
	// Newest first.
	if sessions[0].Name != "history_2025y06m15d_22h30m00s.nml" {
		t.Errorf("first (newest): got %q", sessions[0].Name)
	}
	if sessions[1].Name != "history_2025y03m20d_21h00m00s.nml" {
		t.Errorf("second (mid): got %q", sessions[1].Name)
	}
	if sessions[2].Name != "history_2025y01m10d_20h00m00s.nml" {
		t.Errorf("third (oldest): got %q", sessions[2].Name)
	}
}

func TestLoadSessionsSkipBad(t *testing.T) {
	dir := t.TempDir()

	// One valid file + one corrupt file.
	writeHistoryFile(t, dir, "history_2025y05m01d_18h00m00s.nml",
		historyNML("GoodSet", "C:/:Music/:GoodSet.flac", 3000.0))
	writeHistoryFile(t, dir, "history_2025y04m01d_18h00m00s.nml", "not xml at all <<>>")

	sessions, err := LoadSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("want 1 session (bad skipped), got %d", len(sessions))
	}
	if sessions[0].Name != "history_2025y05m01d_18h00m00s.nml" {
		t.Errorf("unexpected session name %q", sessions[0].Name)
	}
}

func TestSummarize(t *testing.T) {
	s := Session{
		Name: "TestSet",
		Played: []PlayedTrack{
			{Path: "a.flac", DurationSec: 300.5},
			{Path: "b.flac", DurationSec: 420.0},
			{Path: "c.flac", DurationSec: 179.5},
		},
		StartedAt: time.Date(2025, 6, 15, 22, 30, 0, 0, time.UTC),
	}
	sum := Summarize(s)
	if sum.TrackCount != 3 {
		t.Errorf("TrackCount: got %d want 3", sum.TrackCount)
	}
	want := 300.5 + 420.0 + 179.5
	if sum.TotalDurationSec != want {
		t.Errorf("TotalDurationSec: got %v want %v", sum.TotalDurationSec, want)
	}
	if !sum.StartedAt.Equal(s.StartedAt) {
		t.Errorf("StartedAt mismatch")
	}
	if sum.Name != "TestSet" {
		t.Errorf("Name: got %q", sum.Name)
	}
}
