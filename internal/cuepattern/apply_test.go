package cuepattern

import (
	"testing"

	"rave.page/mate/internal/musiclib"
)

// PromoteMemoryToHotcues: promoted + existing pads end up renumbered in track-time
// order (pad 0 = earliest), pads capped at 8, loops/grid untouched, no-op returns the
// original slice.
func TestPromoteMemoryToHotcues(t *testing.T) {
	cues := []musiclib.CuePoint{
		{Kind: musiclib.CueHot, Hotcue: 2, StartMs: 1000},   // renumbers to time position
		{Kind: musiclib.CuePlain, Hotcue: -1, StartMs: 500}, // earliest memory → slot 0
		{Kind: musiclib.CuePlain, Hotcue: -1, StartMs: 2000},
		{Kind: musiclib.CueLoop, Hotcue: -1, StartMs: 1500}, // untouched
		{Kind: musiclib.CueGrid, Hotcue: -1, StartMs: 0},    // untouched
	}
	out, n := PromoteMemoryToHotcues(cues, "", 0)
	if n != 2 {
		t.Fatalf("promoted=%d want 2", n)
	}
	byStart := map[float64]musiclib.CuePoint{}
	for _, c := range out {
		byStart[c.StartMs] = c
	}
	if c := byStart[500]; c.Kind != musiclib.CueHot || c.Hotcue != 0 {
		t.Fatalf("earliest memory cue: %+v want hot slot 0", c)
	}
	if c := byStart[1000]; c.Kind != musiclib.CueHot || c.Hotcue != 1 {
		t.Fatalf("existing hotcue: %+v want hot slot 1 (time order)", c)
	}
	if c := byStart[2000]; c.Kind != musiclib.CueHot || c.Hotcue != 2 {
		t.Fatalf("second memory cue: %+v want hot slot 2", c)
	}
	if c := byStart[1500]; c.Kind != musiclib.CueLoop {
		t.Fatalf("loop touched: %+v", c)
	}

	// pads exhausted: 8 hotcues already → nothing promotes, original returned
	full := make([]musiclib.CuePoint, 0, 9)
	for s := 0; s < 8; s++ {
		full = append(full, musiclib.CuePoint{Kind: musiclib.CueHot, Hotcue: s, StartMs: float64(s * 100)})
	}
	full = append(full, musiclib.CuePoint{Kind: musiclib.CuePlain, Hotcue: -1, StartMs: 900})
	if _, n := PromoteMemoryToHotcues(full, "", 0); n != 0 {
		t.Fatalf("promoted=%d want 0 (pads full)", n)
	}
}
