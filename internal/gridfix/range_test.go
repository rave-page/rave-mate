package gridfix

import (
	"math"
	"testing"
)

// synthetic constant-tempo detection: beats at bpm from anchor, downbeats every 4th
func synthBeats(bpm, anchor float64, n int) (beats, downbeats []float64) {
	p := 60.0 / bpm
	for i := 0; i < n; i++ {
		t := anchor + float64(i)*p
		beats = append(beats, t)
		if i%4 == 0 {
			downbeats = append(downbeats, t)
		}
	}
	return
}

// The user's failure case: DnB track detected/stored at 87, actually 174. Without a
// range the stored octave wins (legacy behavior, asserted); with a 160-190 band the
// plan folds to 174 and demands a FIX even though the marker is already aligned.
func TestPlanFixTargetRange(t *testing.T) {
	beats, downbeats := synthBeats(174, 0.05, 400)
	start := 0.05

	legacyFit := FitConstantGrid(beats, downbeats, 87)
	legacy := PlanFix(*legacyFit, downbeats, PlanInput{OldBPM: 87, OldStartS: &start, MinQuality: 0.85, ThresholdMS: 12})
	if math.Abs(legacy.NewBPM-87) > 0.01 {
		t.Fatalf("legacy octave keep broken: NewBPM=%g want 87", legacy.NewBPM)
	}

	prior := 174.0 // what Batch.Run folds the stored 87 into before fitting
	fit := FitConstantGrid(beats, downbeats, prior)
	plan := PlanFix(*fit, downbeats, PlanInput{OldBPM: 87, OldStartS: &start,
		MinQuality: 0.85, ThresholdMS: 12, RangeLo: 160, RangeHi: 190})
	if plan.Status != StatusFix {
		t.Fatalf("want FIX, got %s (%s)", plan.Status, plan.Detail)
	}
	if math.Abs(plan.NewBPM-174) > 0.01 {
		t.Fatalf("NewBPM=%g want 174", plan.NewBPM)
	}
	if math.Abs(plan.NewStartS-start) > 0.005 {
		t.Fatalf("marker moved: NewStartS=%g want ~%g", plan.NewStartS, start)
	}
}

// ChooseOctaveRange folds a detection with no stored BPM into the band.
func TestChooseOctaveRangeNoPrior(t *testing.T) {
	fit := GridFit{Anchor: 0.1, Period: 60.0 / 87.0}
	got := ChooseOctaveRange(fit, 0, nil, 160, 190)
	if math.Abs(got.BPM()-174) > 1e-9 {
		t.Fatalf("BPM=%g want 174", got.BPM())
	}
	if got.Anchor != 0.1 {
		t.Fatalf("densifying moved the anchor: %g", got.Anchor)
	}
	// halving direction: 348 detection into the band
	fit = GridFit{Anchor: 0.1, Period: 60.0 / 348.0}
	got = ChooseOctaveRange(fit, 0, nil, 160, 190)
	if math.Abs(got.BPM()-174) > 1e-9 {
		t.Fatalf("BPM=%g want 174", got.BPM())
	}
	// band outside the legacy 90-180 normalization: the fold loops do the work
	fit = GridFit{Anchor: 0.1, Period: 60.0 / 87.0}
	got = ChooseOctaveRange(fit, 0, nil, 280, 360)
	if math.Abs(got.BPM()-348) > 1e-9 || got.Anchor != 0.1 {
		t.Fatalf("BPM=%g anchor=%g want 348 @ 0.1", got.BPM(), got.Anchor)
	}
	// unfoldable gap: unchanged
	fit = GridFit{Anchor: 0.1, Period: 60.0 / 100.0}
	got = ChooseOctaveRange(fit, 0, nil, 210, 220)
	if math.Abs(got.BPM()-100) > 1e-9 {
		t.Fatalf("gap case changed BPM: %g", got.BPM())
	}
}
