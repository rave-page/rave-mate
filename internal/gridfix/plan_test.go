package gridfix

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

type planRec struct {
	I         int       `json:"i"`
	Scenario  string    `json:"scenario"`
	Downbeats []float64 `json:"downbeats"`
	Fit       struct {
		Anchor    float64 `json:"anchor"`
		Period    float64 `json:"period"`
		Coverage  float64 `json:"coverage"`
		Explained float64 `json:"explained"`
		PhaseR    float64 `json:"phase_r"`
		NBeats    int     `json:"n_beats"`
	} `json:"fit"`
	In struct {
		OldBPM *float64  `json:"old_bpm"`
		CuesMS []float64 `json:"cues_ms"`
		BiasS  float64   `json:"bias_s"`
	} `json:"in"`
	Out struct {
		Status   string   `json:"status"`
		OldBPM   *float64 `json:"old_bpm"`
		NewBPM   *float64 `json:"new_bpm"`
		OffsetMS *float64 `json:"offset_ms"`
		StartMS  *float64 `json:"start_ms"`
		GridBPM  *float64 `json:"grid_bpm"`
	} `json:"out"`
}

// TestPlanFixGolden replays decisions the real Python process_entry made on
// synthetic entries (all branches: no cue, aligned, offset, bad bpm, multi-cue).
func TestPlanFixGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/plan_golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var recs []planRec
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatalf("parse golden: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("no golden records")
	}
	const tol = 1e-6
	for _, r := range recs {
		fit := GridFit{Anchor: r.Fit.Anchor, Period: r.Fit.Period,
			Coverage: r.Fit.Coverage, Explained: r.Fit.Explained,
			NBeats: r.Fit.NBeats, PhaseR: r.Fit.PhaseR}
		in := PlanInput{MinQuality: 0.85, ThresholdMS: 12.0, BiasS: r.In.BiasS}
		if r.In.OldBPM != nil {
			in.OldBPM = *r.In.OldBPM
		}
		switch len(r.In.CuesMS) {
		case 0:
		case 1:
			s := r.In.CuesMS[0] / 1000.0
			in.OldStartS = &s
		default:
			in.MultiMarker = true
		}
		p := PlanFix(fit, r.Downbeats, in)
		id := func() string { return r.Scenario + "/" + itoa(r.I) }
		if string(p.Status) != r.Out.Status {
			t.Errorf("%s: status got %s want %s (%s)", id(), p.Status, r.Out.Status, p.Detail)
			continue
		}
		if r.Out.NewBPM != nil && p.Status != StatusSkip {
			if math.Abs(p.NewBPM-*r.Out.NewBPM) > tol {
				t.Errorf("%s: new_bpm got %.9f want %.9f", id(), p.NewBPM, *r.Out.NewBPM)
			}
		}
		if p.Status == StatusFix && r.Out.StartMS != nil && !r.OutMulti() {
			if math.Abs(p.NewStartS*1000-*r.Out.StartMS) > 1e-3 { // START serialized at 6 decimals of ms
				t.Errorf("%s: start_ms got %.6f want %.6f", id(), p.NewStartS*1000, *r.Out.StartMS)
			}
		}
		if r.Out.OffsetMS != nil {
			if math.IsNaN(p.OffsetMS) {
				t.Errorf("%s: offset got NaN want %.4f", id(), *r.Out.OffsetMS)
			} else if math.Abs(p.OffsetMS-*r.Out.OffsetMS) > tol {
				t.Errorf("%s: offset_ms got %.9f want %.9f", id(), p.OffsetMS, *r.Out.OffsetMS)
			}
		}
	}
}

// OutMulti reports the golden scenario left multiple cues in the entry (start_ms is nil then).
func (r planRec) OutMulti() bool { return len(r.In.CuesMS) > 1 }

func itoa(i int) string {
	return string(rune('0'+i/10)) + string(rune('0'+i%10))
}
