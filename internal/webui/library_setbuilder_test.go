package webui

import (
	"reflect"
	"testing"

	"rave.page/mate/internal/libdb"
	"rave.page/mate/internal/musiclib"
)

func tk(path, key string, bpm float64) musiclib.Track {
	return musiclib.Track{Path: path, Key: key, BPM: bpm}
}

func groupPaths(gs []plGroup) [][]string {
	out := make([][]string, len(gs))
	for i, g := range gs {
		out[i] = g.paths
	}
	return out
}

func TestPlGroupKeyHarmonicOrder(t *testing.T) {
	tracks := []musiclib.Track{
		tk("x", "??", 0),     // unparseable → last
		tk("b8", "8B", 0),    // 8B after 8A
		tk("a1", "1A", 0),    // Camelot direct
		tk("a8", "Am", 0),    // Am = 8A
		tk("a1b", "1A", 128), // same bucket, keeps order
	}
	gs := plGroupTracks(tracks, "key", nil)
	want := [][]string{{"a1", "a1b"}, {"a8"}, {"b8"}, {"x"}}
	if !reflect.DeepEqual(groupPaths(gs), want) {
		t.Fatalf("groups: %+v", gs)
	}
	if gs[0].label != "1A" || gs[1].label != "8A" || gs[2].label != "8B" || gs[3].label != "" {
		t.Fatalf("labels: %+v", gs)
	}
}

func TestPlGroupEnergyBands(t *testing.T) {
	tracks := []musiclib.Track{
		tk("hard", "", 160),
		tk("chill", "", 100),
		tk("gap", "", 116), // gap between presets resolves to the lower band
		tk("none", "", 0),  // unknown BPM last
		tk("peak", "", 140),
		tk("chill2", "", 90), // within-band BPM ascending
	}
	gs := plGroupTracks(tracks, "energy", nil)
	want := [][]string{{"chill2", "chill", "gap"}, {"peak"}, {"hard"}, {"none"}}
	if !reflect.DeepEqual(groupPaths(gs), want) {
		t.Fatalf("groups: %+v", gs)
	}
}

func TestPlGroupCompatClusters(t *testing.T) {
	tracks := []musiclib.Track{tk("a", "", 0), tk("b", "", 0), tk("c", "", 0), tk("d", "", 0), tk("e", "", 0)}
	adj := map[string][]libdb.CompatRow{
		"a": {{Path: "c", Kind: "blend"}},
		"c": {{Path: "a", Kind: "blend"}},
		"b": {{Path: "e", Kind: "energy"}},
		"e": {{Path: "b", Kind: "energy"}},
		"d": {{Path: "zz-not-member", Kind: "blend"}}, // edge outside the playlist ignored
	}
	gs := plGroupTracks(tracks, "compat", adj)
	// cluster {a,c} first (a occurs first), cluster {b,e}, singleton group {d} last
	want := [][]string{{"a", "c"}, {"b", "e"}, {"d"}}
	if !reflect.DeepEqual(groupPaths(gs), want) {
		t.Fatalf("groups: %+v", gs)
	}
	if gs[2].label != "" {
		t.Fatalf("singleton label: %q", gs[2].label)
	}
}

func TestPlGroupDateNewestFirst(t *testing.T) {
	tracks := []musiclib.Track{
		{Path: "old", ImportDate: "2023/1/5"},
		{Path: "newer", ImportDate: "2024-06-20"},
		{Path: "newest", ImportDate: "2024-06-25"},
		{Path: "none"},
	}
	gs := plGroupTracks(tracks, "added", nil)
	want := [][]string{{"newest", "newer"}, {"old"}, {"none"}}
	if !reflect.DeepEqual(groupPaths(gs), want) {
		t.Fatalf("groups: %+v", gs)
	}
	if gs[0].label != "2024-06" || gs[1].label != "2023-01" {
		t.Fatalf("labels: %+v", gs)
	}
}

func TestParseDateLoose(t *testing.T) {
	for _, s := range []string{"2024-06-20", "2024/6/20", "2024-06-20 10:11:12", "2024-06-20T10:11:12Z"} {
		if _, ok := parseDateLoose(s); !ok {
			t.Fatalf("should parse: %s", s)
		}
	}
	for _, s := range []string{"", "n/a", "yesterday"} {
		if _, ok := parseDateLoose(s); ok {
			t.Fatalf("should not parse: %s", s)
		}
	}
}

func TestPlInterleave(t *testing.T) {
	groups := []plGroup{{paths: []string{"a", "b"}}, {paths: []string{"c"}}, {paths: []string{"d"}}}
	got := plInterleave(groups, []string{"D1", "D2"})
	want := []string{"a", "b", "D1", "c", "D2", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("interleave: %v", got)
	}
	// fewer dividers than boundaries → skip silently (degrade path)
	got = plInterleave(groups, []string{"D1"})
	want = []string{"a", "b", "D1", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("short dividers: %v", got)
	}
	// no dividers, single group: never leading/trailing
	got = plInterleave(groups[:1], nil)
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("single group: %v", got)
	}
}
