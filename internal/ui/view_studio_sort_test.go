package ui

import (
	"testing"

	"rave.page/mate/internal/musiclib"
)

func TestSortCollShown(t *testing.T) {
	sv := &studioView{
		tracks: []musiclib.Track{
			{Title: "Zulu", Artist: "Beta", BPM: 128, Key: "8A", Rating: 3, PlayCount: 10, Genre: "Techno", Label: "Drumcode"},
			{Title: "Alpha", Artist: "Alpha", BPM: 174, Key: "1A", Rating: 5, PlayCount: 2, Genre: "Neurofunk", Label: "Critical"},
			{Title: "Mid", Artist: "alpha", BPM: 140, Key: "xx", Rating: 1, PlayCount: 50, Genre: "Liquid DnB", Label: "Hospital"},
		},
	}
	idx := func() []int { return []int{0, 1, 2} }

	cases := []struct {
		by     string
		desc   bool
		want   []int // expected track indices (by original order) top→bottom
		reason string
	}{
		{"Artist", false, []int{1, 2, 0}, "Alpha, alpha (ci tie→title), Beta"},
		{"Title", false, []int{1, 2, 0}, "Alpha, Mid, Zulu"},
		{"BPM", false, []int{0, 2, 1}, "128,140,174 asc"},
		{"BPM", true, []int{1, 2, 0}, "174,140,128 desc"},
		{"Rating", true, []int{1, 0, 2}, "5,3,1 desc"},
		{"Plays", true, []int{2, 0, 1}, "50,10,2 desc"},
		{"Key", false, []int{1, 0, 2}, "1A,8A,then unparseable xx"},
		{"Genre", false, []int{2, 1, 0}, "DnB family: Liquid<Neuro (raw genre tie-break), then Techno"},
		{"Label", false, []int{1, 0, 2}, "Critical,Drumcode,Hospital asc"},
	}
	for _, c := range cases {
		sv.collShown = idx()
		sv.collSortBy, sv.collSortDesc = c.by, c.desc
		sv.sortCollShown()
		for k, want := range c.want {
			if sv.collShown[k] != want {
				t.Errorf("%s desc=%v (%s): pos %d = track %d, want %d (got order %v)",
					c.by, c.desc, c.reason, k, sv.collShown[k], want, sv.collShown)
				break
			}
		}
	}
}

// sortIndicesBy backs the Playlist + History track tables too. Verify the shared accessor sort
// and that the natural-order sentinel ("") is a no-op (Playlist "Default" / History "Play order").
func TestSortIndicesBy(t *testing.T) {
	tracks := []musiclib.Track{
		{Artist: "Beta", BPM: 128},
		{Artist: "Alpha", BPM: 174},
		{Artist: "Gamma", BPM: 140},
	}
	get := func(i int) musiclib.Track { return tracks[i] }

	// natural order: empty field leaves the slice untouched
	shown := []int{0, 1, 2}
	sortIndicesBy(shown, get, "", false)
	for i, v := range []int{0, 1, 2} {
		if shown[i] != v {
			t.Fatalf("natural order mutated: %v", shown)
		}
	}

	shown = []int{0, 1, 2}
	sortIndicesBy(shown, get, "BPM", false)
	if got := []int{shown[0], shown[1], shown[2]}; got[0] != 0 || got[1] != 2 || got[2] != 1 {
		t.Errorf("BPM asc = %v, want [0 2 1]", got)
	}

	shown = []int{0, 1, 2}
	sortIndicesBy(shown, get, "BPM", true)
	if got := []int{shown[0], shown[1], shown[2]}; got[0] != 1 || got[1] != 2 || got[2] != 0 {
		t.Errorf("BPM desc = %v, want [1 2 0]", got)
	}
}
