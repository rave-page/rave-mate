package musiclib

import (
	"strings"
	"testing"
)

func TestSmartRulesCompat(t *testing.T) {
	tracks := []Track{
		{Title: "Anchor", Path: "a.mp3", BPM: 140},
		{Title: "Partner", Path: "b.mp3", BPM: 150},
		{Title: "Other", Path: "c.mp3", BPM: 145},
	}
	r := SmartRules{CompatWith: "a.mp3"}
	if r.Empty() {
		t.Fatal("compat rule must not read Empty")
	}
	// fail-closed: no prepared set → nothing matches
	if got := FilterSmart(tracks, r); len(got) != 0 {
		t.Fatalf("no prep: %+v", got)
	}
	set := map[string]bool{"a.mp3": true, "b.mp3": true}
	got := FilterSmartPrep(tracks, r, SmartPrep{Compat: set})
	if len(got) != 2 || got[0].Path != "a.mp3" || got[1].Path != "b.mp3" {
		t.Fatalf("prep: %+v", got)
	}
	// compat ANDs with the other rules
	r.BPMMin = 145
	if got := FilterSmartPrep(tracks, r, SmartPrep{Compat: set}); len(got) != 1 || got[0].Path != "b.mp3" {
		t.Fatalf("compat+bpm: %+v", got)
	}
	d := SmartRules{CompatWith: `D:\Music\Anchor Track.mp3`, CompatDepth: 2}.Describe()
	if !strings.Contains(d, "Anchor Track") || !strings.Contains(d, "depth 2") {
		t.Fatalf("describe: %q", d)
	}
}
