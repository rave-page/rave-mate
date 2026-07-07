package cuesheet

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// sample mirrors audiorec.cueSheet output, incl. a track past 60:00 (minutes uncapped).
const sample = `PERFORMER "DJ Test"
TITLE "Long Set"
FILE "set.flac" WAVE
  TRACK 01 AUDIO
    TITLE "Opener"
    PERFORMER "Artist A"
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    TITLE "Second"
    PERFORMER "Artist B"
    INDEX 01 03:30:37
  TRACK 03 AUDIO
    TITLE "Past The Hour"
    PERFORMER "Artist C"
    INDEX 01 72:15:00
`

func TestParse(t *testing.T) {
	sh, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if sh.Performer != "DJ Test" || sh.Title != "Long Set" || sh.File != "set.flac" {
		t.Fatalf("header mismatch: %+v", sh)
	}
	if len(sh.Tracks) != 3 {
		t.Fatalf("want 3 tracks, got %d", len(sh.Tracks))
	}

	want := []Track{
		{Num: 1, Title: "Opener", Performer: "Artist A", Start: 0},
		{Num: 2, Title: "Second", Performer: "Artist B", Start: 3*time.Minute + 30*time.Second + 37*(time.Second/75)},
		{Num: 3, Title: "Past The Hour", Performer: "Artist C", Start: 72*time.Minute + 15*time.Second},
	}
	for i, w := range want {
		g := sh.Tracks[i]
		if g.Num != w.Num || g.Title != w.Title || g.Performer != w.Performer || g.Start != w.Start {
			t.Errorf("track %d: got %+v want %+v", i, g, w)
		}
	}
	// minutes not clamped to 59
	if sh.Tracks[2].Start <= 60*time.Minute {
		t.Errorf("track 3 start %v should exceed 60m (minutes clamped?)", sh.Tracks[2].Start)
	}
}

func TestParseTolerant(t *testing.T) {
	in := `REM GENRE Techno
TITLE "Set"
GARBAGE line here
  TRACK 01 AUDIO
    TITLE "Only"
    INDEX 00 00:00:00
    INDEX 01 01:02:03
`
	sh, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if sh.Title != "Set" || len(sh.Tracks) != 1 {
		t.Fatalf("unexpected: %+v", sh)
	}
	if sh.Tracks[0].Title != "Only" {
		t.Errorf("title = %q", sh.Tracks[0].Title)
	}
	// INDEX 01 wins, INDEX 00 ignored
	want := 1*time.Minute + 2*time.Second + 3*(time.Second/75)
	if sh.Tracks[0].Start != want {
		t.Errorf("start = %v want %v", sh.Tracks[0].Start, want)
	}
}

func TestParseMalformed(t *testing.T) {
	cases := []string{
		"  TRACK 01 AUDIO\n    INDEX 01 bad:time\n",
		"  TRACK 01 AUDIO\n    INDEX 01 01:02\n",
		"  TRACK xx AUDIO\n",
		"    INDEX 01 00:00:00\n", // INDEX before TRACK
	}
	for _, c := range cases {
		if _, err := Parse(strings.NewReader(c)); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

// roundTrip renders a Sheet in audiorec's format then re-parses; offsets/titles must survive.
func TestRoundTrip(t *testing.T) {
	orig := Sheet{
		Performer: "RT Perf",
		Title:     "RT Title",
		File:      "rt.flac",
		Tracks: []Track{
			{Num: 1, Title: "A", Performer: "PA", Start: 0},
			{Num: 2, Title: "B", Performer: "PB", Start: 65*time.Minute + 12*time.Second + 30*(time.Second/75)},
		},
	}
	var b strings.Builder
	fmt.Fprintf(&b, "PERFORMER %q\n", orig.Performer)
	fmt.Fprintf(&b, "TITLE %q\n", orig.Title)
	fmt.Fprintf(&b, "FILE %q WAVE\n", orig.File)
	for _, tr := range orig.Tracks {
		fmt.Fprintf(&b, "  TRACK %02d AUDIO\n", tr.Num)
		fmt.Fprintf(&b, "    TITLE %q\n", tr.Title)
		fmt.Fprintf(&b, "    PERFORMER %q\n", tr.Performer)
		mm := int(tr.Start / time.Minute)
		ss := int((tr.Start % time.Minute) / time.Second)
		ff := int((tr.Start % time.Second) / (time.Second / 75))
		fmt.Fprintf(&b, "    INDEX 01 %02d:%02d:%02d\n", mm, ss, ff)
	}
	got, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Performer != orig.Performer || got.Title != orig.Title || got.File != orig.File {
		t.Fatalf("header drift: %+v", got)
	}
	for i, w := range orig.Tracks {
		if got.Tracks[i] != w {
			t.Errorf("track %d drift: got %+v want %+v", i, got.Tracks[i], w)
		}
	}
}
