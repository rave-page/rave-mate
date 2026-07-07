// Package setalign time-aligns two recordings of the same performance (e.g. an OBS video and a
// broadcast audio capture of one set) by cross-correlating their RMS energy envelopes. Envelopes
// come from the probe worker (probe.envelope); the correlation itself is pure CPU, bounded by the
// envelope sizes (a 2 h set at 50 Hz ≈ 360k float32 ≈ 1.4 MB per side).
package setalign

import (
	"errors"
	"math"
)

// Result is a computed time offset between two recordings.
type Result struct {
	// OffsetSec places B's t=0 on A's timeline: B starts OffsetSec after A starts
	// (negative = B starts before A).
	OffsetSec float64
	// Confidence 0..1 - normalized correlation at the peak, damped when the peak
	// isn't decisive. ≥0.6 high, ≥0.35 medium, else low (see Label).
	Confidence float64
	// Decisive reports the peak clearly beat the best score outside its neighborhood.
	Decisive bool
}

// Label buckets Confidence for display.
func (r Result) Label() string {
	switch {
	case r.Confidence >= 0.6:
		return "high"
	case r.Confidence >= 0.35:
		return "medium"
	default:
		return "low"
	}
}

const (
	minOverlapSec = 10.0 // less shared audio than this = no meaningful alignment
	coarseRateHz  = 5.0  // decimated pass rate (full-range search stays cheap)
	neighborSec   = 5.0  // peak neighborhood excluded when checking decisiveness
)

// Align cross-correlates envA/envB (RMS envelopes at rateHz) and returns where B's start lands on
// A's timeline. prior (used when hasPrior) is the expected OffsetSec - e.g. from the captures'
// wall-clock start timestamps - and windowSec>0 limits the lag search to prior±windowSec, which
// both speeds the search up and rejects far-away false peaks. Coarse-to-fine: a decimated ~5 Hz
// pass finds the peak, a full-rate pass refines it.
func Align(envA, envB []float64, rateHz float64, prior float64, hasPrior bool, windowSec float64) (Result, error) {
	if rateHz <= 0 {
		return Result{}, errors.New("setalign: rate must be positive")
	}
	minOv := int(minOverlapSec * rateHz)
	if len(envA) < minOv || len(envB) < minOv {
		return Result{}, errors.New("setalign: recordings too short to align")
	}
	// NCC over a sliver of smooth envelope is meaninglessly high - require the candidate
	// overlap to cover a real chunk of the shorter recording.
	if q := min(len(envA), len(envB)) / 4; q > minOv {
		minOv = q
	}
	a := normalize(envA)
	b := normalize(envB)
	if a == nil || b == nil {
		return Result{}, errors.New("setalign: flat envelope (no audio energy variation)")
	}

	// Lag = B-start sample index on A's timeline. Full range keeps ≥ minOv overlap.
	loLag, hiLag := -(len(b) - minOv), len(a)-minOv
	if hasPrior && windowSec > 0 {
		pl := int(math.Round(prior * rateHz))
		w := int(math.Ceil(windowSec * rateHz))
		loLag = max(loLag, pl-w)
		hiLag = min(hiLag, pl+w)
		if loLag > hiLag {
			return Result{}, errors.New("setalign: prior window outside the alignable range")
		}
	}

	// Coarse pass on decimated envelopes.
	dec := max(1, int(math.Round(rateHz/coarseRateHz)))
	ca, cb := decimate(a, dec), decimate(b, dec)
	cLo, cHi := intDivFloor(loLag, dec), intDivCeil(hiLag, dec)
	scores := make([]float64, 0, cHi-cLo+1)
	bestLag, bestScore := cLo, math.Inf(-1)
	for lag := cLo; lag <= cHi; lag++ {
		s := ncc(ca, cb, lag, minOv/dec)
		scores = append(scores, s)
		if s > bestScore {
			bestScore, bestLag = s, lag
		}
	}
	if math.IsInf(bestScore, -1) {
		return Result{}, errors.New("setalign: no overlapping region in the search window")
	}

	// Decisiveness: best vs the best score outside the peak's ±neighborSec neighborhood.
	nb := int(neighborSec * coarseRateHz)
	runnerUp := math.Inf(-1)
	for i, s := range scores {
		if lag := cLo + i; lag < bestLag-nb || lag > bestLag+nb {
			runnerUp = math.Max(runnerUp, s)
		}
	}
	decisive := bestScore > 0.2 && (math.IsInf(runnerUp, -1) || bestScore-runnerUp > 0.04)

	// Refine at full rate around the coarse peak.
	rLo, rHi := max(loLag, bestLag*dec-3*dec), min(hiLag, bestLag*dec+3*dec)
	fineLag, fineScore := bestLag*dec, math.Inf(-1)
	for lag := rLo; lag <= rHi; lag++ {
		if s := ncc(a, b, lag, minOv); s > fineScore {
			fineScore, fineLag = s, lag
		}
	}
	if math.IsInf(fineScore, -1) {
		fineLag, fineScore = bestLag*dec, bestScore
	}

	conf := clamp01(fineScore)
	if !decisive {
		conf *= 0.5
	}
	return Result{OffsetSec: float64(fineLag) / rateHz, Confidence: conf, Decisive: decisive}, nil
}

// ncc is the normalized cross-correlation of zero-mean/unit-variance envelopes at lag (b shifted
// by +lag on a's axis); overlap below minOverlap samples returns -Inf.
func ncc(a, b []float64, lag, minOverlap int) float64 {
	aStart, bStart := max(0, lag), max(0, -lag)
	n := min(len(a)-aStart, len(b)-bStart)
	if n < max(minOverlap, 2) {
		return math.Inf(-1)
	}
	var dot, ea, eb float64
	for i := 0; i < n; i++ {
		x, y := a[aStart+i], b[bStart+i]
		dot += x * y
		ea += x * x
		eb += y * y
	}
	den := math.Sqrt(ea * eb)
	if den == 0 {
		return math.Inf(-1)
	}
	return dot / den
}

// normalize returns env zero-mean/unit-variance (nil if flat).
func normalize(env []float64) []float64 {
	var mean float64
	for _, v := range env {
		mean += v
	}
	mean /= float64(len(env))
	var variance float64
	for _, v := range env {
		d := v - mean
		variance += d * d
	}
	variance /= float64(len(env))
	sd := math.Sqrt(variance)
	if sd == 0 {
		return nil
	}
	out := make([]float64, len(env))
	for i, v := range env {
		out[i] = (v - mean) / sd
	}
	return out
}

// decimate averages env into len/d buckets.
func decimate(env []float64, d int) []float64 {
	if d <= 1 {
		return env
	}
	n := len(env) / d
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		var s float64
		for j := i * d; j < (i+1)*d; j++ {
			s += env[j]
		}
		out[i] = s / float64(d)
	}
	return out
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func intDivFloor(a, b int) int { return int(math.Floor(float64(a) / float64(b))) }
func intDivCeil(a, b int) int  { return int(math.Ceil(float64(a) / float64(b))) }
