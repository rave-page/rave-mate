package gridfix

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

// golden vectors generated from the Python engine (fix_grids.py) over real cached
// beat detections; anonymous float arrays only.
type goldenRec struct {
	Beats     []float64 `json:"beats"`
	Downbeats []float64 `json:"downbeats"`
	OldBPM    *float64  `json:"old_bpm"`
	Fit       *struct {
		Anchor    float64 `json:"anchor"`
		Period    float64 `json:"period"`
		BPM       float64 `json:"bpm"`
		Coverage  float64 `json:"coverage"`
		Explained float64 `json:"explained"`
		PhaseR    float64 `json:"phase_r"`
		NBeats    int     `json:"n_beats"`
	} `json:"fit"`
	Octave *struct {
		Anchor float64 `json:"anchor"`
		Period float64 `json:"period"`
		BPM    float64 `json:"bpm"`
	} `json:"octave"`
}

func loadGolden(t *testing.T) []goldenRec {
	t.Helper()
	raw, err := os.ReadFile("testdata/gridfit_golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var recs []goldenRec
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("no golden records")
	}
	return recs
}

func TestFitConstantGridGolden(t *testing.T) {
	recs := loadGolden(t)
	const tolT = 1e-6 // seconds (anchor/period); Python lstsq is SVD, ours normal equations
	const tolF = 1e-6 // fractions (coverage/explained/phase_r)
	for i, r := range recs {
		oldBPM := 0.0
		if r.OldBPM != nil {
			oldBPM = *r.OldBPM
		}
		fit := FitConstantGrid(r.Beats, r.Downbeats, oldBPM)
		if r.Fit == nil {
			if fit != nil {
				t.Errorf("rec %d: expected no fit, got bpm=%.3f", i, fit.BPM())
			}
			continue
		}
		if fit == nil {
			t.Errorf("rec %d: expected fit bpm=%.3f, got none", i, r.Fit.BPM)
			continue
		}
		if math.Abs(fit.Anchor-r.Fit.Anchor) > tolT ||
			math.Abs(fit.Period-r.Fit.Period) > tolT {
			t.Errorf("rec %d: anchor/period got (%.9f, %.9f) want (%.9f, %.9f)",
				i, fit.Anchor, fit.Period, r.Fit.Anchor, r.Fit.Period)
		}
		if math.Abs(fit.Coverage-r.Fit.Coverage) > tolF ||
			math.Abs(fit.Explained-r.Fit.Explained) > tolF ||
			math.Abs(fit.PhaseR-r.Fit.PhaseR) > tolF {
			t.Errorf("rec %d: cov/expl/phase got (%.9f, %.9f, %.9f) want (%.9f, %.9f, %.9f)",
				i, fit.Coverage, fit.Explained, fit.PhaseR,
				r.Fit.Coverage, r.Fit.Explained, r.Fit.PhaseR)
		}
		if fit.NBeats != r.Fit.NBeats {
			t.Errorf("rec %d: n_beats got %d want %d", i, fit.NBeats, r.Fit.NBeats)
		}
		if r.Octave != nil {
			oct := ChooseOctave(*fit, oldBPM, r.Downbeats)
			if math.Abs(oct.Anchor-r.Octave.Anchor) > tolT ||
				math.Abs(oct.Period-r.Octave.Period) > tolT {
				t.Errorf("rec %d: octave got (%.9f, %.9f) want (%.9f, %.9f)",
					i, oct.Anchor, oct.Period, r.Octave.Anchor, r.Octave.Period)
			}
		}
	}
}

func TestPhaseOffset(t *testing.T) {
	fit := GridFit{Anchor: 10.0, Period: 0.5}
	for _, tc := range []struct{ start, want float64 }{
		{10.0, 0}, {10.1, 0.1}, {9.9, -0.1}, {10.25, 0.25}, {12.6, 0.1},
	} {
		if got := PhaseOffsetS(tc.start, fit); math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("PhaseOffsetS(%v) = %v, want %v", tc.start, got, tc.want)
		}
	}
}
