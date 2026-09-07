package webui

import (
	"testing"

	"rave.page/mate/internal/gridfix"
)

// TestGFManualSkipPaths: the prep-playlist filter (shared by gfPrep + the gfDoneState button
// count) includes only manual-gridding skips (Plan.Manual) - protection skips (verified/locked/
// multi-marker), FIX/OK results, and errored rows are excluded. Order preserved.
func TestGFManualSkipPaths(t *testing.T) {
	results := []gridfix.TrackResult{
		{Path: "verified.mp3", Plan: gridfix.Plan{Status: gridfix.StatusSkip, Detail: "verified grid - protected"}},
		{Path: "locked.mp3", Plan: gridfix.Plan{Status: gridfix.StatusSkip, Detail: "grid locked - not touching"}},
		{Path: "multi.mp3", Plan: gridfix.Plan{Status: gridfix.StatusSkip, Detail: "multiple grid markers (manually gridded?) - not touching"}},
		{Path: "unstable.mp3", Plan: gridfix.Plan{Status: gridfix.StatusSkip, Manual: true, Detail: "tempo unstable - fix manually"}},
		{Path: "nofit.mp3", Plan: gridfix.Plan{Status: gridfix.StatusSkip, Manual: true, Detail: "no stable constant grid found - fix manually"}},
		{Path: "fixed.mp3", Plan: gridfix.Plan{Status: gridfix.StatusFix, Manual: true}}, // FIX excluded even if Manual set
		{Path: "ok.mp3", Plan: gridfix.Plan{Status: gridfix.StatusOK}},
		{Path: "err.mp3", Err: "decode boom", Plan: gridfix.Plan{Status: gridfix.StatusSkip, Manual: true}}, // error excluded
	}
	got := gfManualSkipPaths(results)
	want := []string{"unstable.mp3", "nofit.mp3"}
	if len(got) != len(want) {
		t.Fatalf("gfManualSkipPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("gfManualSkipPaths = %v, want %v", got, want)
		}
	}
}
