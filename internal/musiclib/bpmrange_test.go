package musiclib

import "testing"

func TestFoldBPM(t *testing.T) {
	dnb := BPMRange{Min: 160, Max: 190}
	cases := []struct {
		name string
		bpm  float64
		r    BPMRange
		want float64
		ok   bool
	}{
		{"half-time doubles", 87, dnb, 174, true},
		{"quarter-time quadruples", 43.5, dnb, 174, true},
		{"double-time halves", 348, dnb, 174, true},
		{"in range untouched", 174, dnb, 174, true},
		{"boundary min", 160, dnb, 160, true},
		{"boundary max", 190, dnb, 190, true},
		{"gap in narrow band", 100, BPMRange{Min: 210, Max: 220}, 100, false},
		{"zero bpm", 0, dnb, 0, false},
		{"invalid range", 87, BPMRange{}, 87, false},
		{"inverted range", 87, BPMRange{Min: 190, Max: 160}, 87, false},
		{"non-integer folds exact", 87.0003, dnb, 174.0006, true},
	}
	for _, c := range cases {
		got, ok := FoldBPM(c.bpm, c.r)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: FoldBPM(%g,%v) = %g,%v want %g,%v", c.name, c.bpm, c.r, got, ok, c.want, c.ok)
		}
	}
}

func TestFoldTrack(t *testing.T) {
	tr := Track{BPM: 87, Beatgrid: []GridMarker{{PositionMs: 1234.5, BPM: 87}, {PositionMs: 60000, BPM: 87.5}}}
	f, changed := FoldTrack(&tr, BPMRange{Min: 160, Max: 190})
	if !changed || f != 2 {
		t.Fatalf("factor=%g changed=%v", f, changed)
	}
	if tr.BPM != 174 || tr.Beatgrid[0].BPM != 174 || tr.Beatgrid[1].BPM != 175 {
		t.Fatalf("bpm not folded: %+v", tr)
	}
	if tr.Beatgrid[0].PositionMs != 1234.5 || tr.Beatgrid[1].PositionMs != 60000 {
		t.Fatalf("marker positions moved: %+v", tr.Beatgrid)
	}
	// in-range track untouched
	f, changed = FoldTrack(&tr, BPMRange{Min: 160, Max: 190})
	if changed || f != 1 {
		t.Fatalf("in-range track changed: factor=%g changed=%v", f, changed)
	}
}
