package gridfix

import (
	"fmt"
	"math"
)

// Status of a per-track plan.
type Status string

const (
	StatusFix  Status = "FIX"  // grid marker + BPM should be rewritten
	StatusOK   Status = "OK"   // already aligned - nothing to do
	StatusSkip Status = "SKIP" // fit not trustworthy - route to manual-prep playlist
)

// Plan is the decided per-track action (port of fix_grids.process_entry minus the
// NML mutation - the writeback layer applies it per DJ software).
type Plan struct {
	Status    Status
	Detail    string
	OldBPM    float64 // 0 = none stored
	NewBPM    float64
	NewStartS float64 // grid marker position (s); valid when Status==FIX
	OffsetMS  float64 // signed marker correction; NaN when a marker was created
	Created   bool    // no marker existed - one must be created at NewStartS
}

// PlanInput describes the track's current grid state.
type PlanInput struct {
	OldBPM      float64  // stored BPM, 0 = none
	OldStartS   *float64 // existing single grid marker (s); nil = none
	MultiMarker bool     // >1 grid markers: manually gridded, never touch
	BiasS       float64  // calibrated systematic detector offset (s)
	MinQuality  float64  // min grid coverage to auto-fix (Python default 0.85)
	ThresholdMS float64  // ignore corrections smaller than this (Python default 12)
}

// PlanFix decides FIX/OK/SKIP for a fitted grid. fit must be the raw FitConstantGrid
// result (octave choice happens here, mirroring process_entry).
func PlanFix(fit GridFit, downbeats []float64, in PlanInput) Plan {
	if in.MultiMarker {
		return Plan{Status: StatusSkip, Detail: "multiple grid markers (manually gridded?) - not touching"}
	}
	fit = ChooseOctave(fit, in.OldBPM, downbeats)
	fitted := fit.BPM()
	// confident integer tempo: snap, correcting stored artifacts like 173.999
	var newBPM float64
	tempoAgrees := false
	if snapped := math.RoundToEven(fitted); math.Abs(fitted-snapped) < 0.02 {
		newBPM = snapped
		tempoAgrees = in.OldBPM > 0 && math.Abs(newBPM-in.OldBPM) < 0.1
	} else if in.OldBPM > 0 && math.Abs(fitted-in.OldBPM) < 0.1 {
		// non-integer measurement within jitter noise of the stored BPM: trust the
		// stored value (keeping it costs <15ms drift over a track)
		newBPM = in.OldBPM
		tempoAgrees = true
	} else {
		newBPM = fitted
	}
	bpmChanged := in.OldBPM <= 0 || math.Abs(newBPM-in.OldBPM) > 5e-4
	// phase-only fixes (tempo agrees) are safe at lower coverage - half-time sections
	// dent coverage without making the phase estimate unreliable, and detector jitter
	// on soft-transient tracks is fine if phases stay concentrated (a drifting tempo
	// smears the phase distribution instead)
	minCov := in.MinQuality
	if tempoAgrees {
		minCov -= 0.15
	}
	if fit.Coverage < minCov && !(tempoAgrees && fit.PhaseR >= 0.70) {
		return Plan{
			Status: StatusSkip,
			Detail: fmt.Sprintf("tempo unstable (grid coverage %.0f%%, phase concentration %.2f) - fix manually",
				fit.Coverage*100, fit.PhaseR),
			OldBPM: in.OldBPM,
		}
	}

	if in.OldStartS != nil {
		// corrected offset: raw phase difference minus the calibrated bias
		off := PhaseOffsetS(*in.OldStartS, fit) - in.BiasS
		newStart := *in.OldStartS - off // snap marker onto nearest true gridline
		if math.Abs(off)*1000 < in.ThresholdMS && !bpmChanged {
			return Plan{Status: StatusOK, Detail: "grid already aligned",
				OldBPM: in.OldBPM, NewBPM: newBPM, OffsetMS: off * 1000}
		}
		return Plan{Status: StatusFix,
			Detail: fmt.Sprintf("(grid coverage %.0f%%)", fit.Coverage*100),
			OldBPM: in.OldBPM, NewBPM: newBPM,
			NewStartS: newStart, OffsetMS: off * 1000}
	}
	// no grid marker at all: anchor on first downbeat (or the fit anchor) >= 0
	cands := downbeats
	if len(cands) == 0 {
		cands = []float64{fit.Anchor}
	}
	base := math.Max(fit.Anchor, 0.0)
	for _, c := range cands {
		if c >= 0 {
			base = c
			break
		}
	}
	k := math.RoundToEven((base - fit.Anchor) / fit.Period)
	newStart := fit.Anchor + k*fit.Period + in.BiasS
	return Plan{Status: StatusFix, Detail: "created grid marker",
		OldBPM: in.OldBPM, NewBPM: newBPM,
		NewStartS: newStart, OffsetMS: math.NaN(), Created: true}
}
