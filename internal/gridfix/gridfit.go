// Package gridfix fits a constant beatgrid to neural beat detections (port of the
// Python traktor-grid-fix engine; numerics mirror numpy incl. half-to-even rounding).
package gridfix

import (
	"math"
	"sort"

	"rave.page/mate/internal/musiclib"
)

// GridFit is a constant grid anchored on detected beats.
type GridFit struct {
	Anchor    float64 // time (s) of reference beat
	Period    float64 // seconds per beat
	Coverage  float64 // fraction of gridlines in played sections with a detected beat
	Explained float64 // fraction of detected beats lying on the grid
	NBeats    int
	PhaseR    float64 // circular concentration of all beat phases (1 = tight)
}

// BPM converts the fitted period to beats per minute.
func (f GridFit) BPM() float64 { return 60.0 / f.Period }

// CandidatePeriods lists plausible beat periods: interval-histogram modes (incl.
// pair sums, which recover the quarter note when the tracker emits swung eighths),
// the bar length from detected downbeats, and the stored BPM as a prior.
func CandidatePeriods(beats, downbeats []float64, oldBPM float64) []float64 {
	d := diff(beats)
	var iv []float64
	if len(d) > 1 {
		iv = make([]float64, 0, 2*len(d)-1)
		iv = append(iv, d...)
		for i := 0; i+1 < len(d); i++ {
			iv = append(iv, d[i]+d[i+1])
		}
	} else {
		iv = d
	}
	filtered := iv[:0]
	for _, v := range iv {
		if v > 0.15 && v < 2.0 {
			filtered = append(filtered, v)
		}
	}
	iv = filtered

	var cands []float64
	// stored BPM first: when it's right it's exact, and when it's within a couple %
	// of the truth the windowed refinement converges to the truth anyway
	if oldBPM > 0 {
		for _, f := range []float64{1.0, 2.0, 0.5} {
			cands = append(cands, 60.0/(oldBPM*f))
		}
	}
	if len(iv) > 0 {
		// histogram of intervals rounded to 10ms; top-4 modes, ties broken by value
		// (mirrors np.unique ascending + stable argsort(-counts))
		counts := map[float64]int{}
		for _, v := range iv {
			counts[round2(v)]++
		}
		type vc struct {
			v float64
			c int
		}
		vcs := make([]vc, 0, len(counts))
		for v, c := range counts {
			vcs = append(vcs, vc{v, c})
		}
		sort.Slice(vcs, func(i, j int) bool {
			if vcs[i].c != vcs[j].c {
				return vcs[i].c > vcs[j].c
			}
			return vcs[i].v < vcs[j].v
		})
		if len(vcs) > 4 {
			vcs = vcs[:4]
		}
		for _, m := range vcs {
			// beat times are quantized to the model's 20ms frames; averaging the
			// +-1 frame cluster recovers the true period sub-frame
			var sum float64
			var n int
			for _, v := range iv {
				if math.Abs(v-m.v) <= 0.021 {
					sum += v
					n++
				}
			}
			if n > 0 {
				cands = append(cands, sum/float64(n))
			} else {
				cands = append(cands, m.v)
			}
		}
	}
	if len(downbeats) >= 8 {
		bar := median(diff(downbeats))
		cands = append(cands, bar/4, bar/3)
	}
	var out []float64
	for _, c := range cands {
		dup := false
		for _, o := range out {
			if math.Abs(c/o-1.0) < 0.02 {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, c)
		}
	}
	return out
}

// RefineGrid anchors a grid of the given period on the detected beats (circular-mean
// phase), then iteratively least-squares refines using only beats that lie on the
// grid. Gridline runs with no beats at all (breakdowns) are excluded from coverage;
// scattered misses count against it. Returns nil when the grid doesn't hold.
func RefineGrid(beats []float64, period float64) *GridFit {
	tol := math.Min(0.030, 0.12*period)
	anchor := 0.0
	// refine over growing windows: a slightly-off candidate period accumulates phase
	// drift over hundreds of beats, so lock it in on a short prefix first
	for _, n := range []int{32, 128, 512, len(beats)} {
		m := n
		if m > len(beats) {
			m = len(beats)
		}
		sub := beats[:m]
		anchor = circularAnchor(sub, period)
		for it := 0; it < 3; it++ {
			ks := make([]float64, 0, len(sub))
			ys := make([]float64, 0, len(sub))
			for _, b := range sub {
				k := math.RoundToEven((b - anchor) / period)
				if math.Abs(b-(anchor+k*period)) < tol {
					ks = append(ks, k)
					ys = append(ys, b)
				}
			}
			min := len(sub) / 8
			if min < 8 {
				min = 8
			}
			if len(ks) < min {
				return nil
			}
			anchor, period = lstsq2(ks, ys)
		}
		if n >= len(beats) {
			break
		}
	}
	// final on-grid mask over all beats
	var kOn []int
	onGrid := 0
	for _, b := range beats {
		k := math.RoundToEven((b - anchor) / period)
		if math.Abs(b-(anchor+k*period)) < tol {
			kOn = append(kOn, int(k))
			onGrid++
		}
	}
	if onGrid < 12 {
		return nil
	}
	kiSet := map[int]bool{}
	kMin, kMax := kOn[0], kOn[0]
	for _, k := range kOn {
		kiSet[k] = true
		if k < kMin {
			kMin = k
		}
		if k > kMax {
			kMax = k
		}
	}
	// an uncovered gridline is excused only if NO beat was detected anywhere near it
	// (true beatless section) - a nearby off-grid beat means the grid is wrong there
	// and must count as a miss
	have, miss := 0, 0
	for k := kMin; k <= kMax; k++ {
		if kiSet[k] {
			have++
			continue
		}
		gt := anchor + float64(k)*period
		idx := sort.SearchFloat64s(beats, gt)
		if idx < 1 {
			idx = 1
		}
		if idx > len(beats)-1 {
			idx = len(beats) - 1
		}
		nearest := math.Min(math.Abs(gt-beats[idx-1]), math.Abs(gt-beats[idx]))
		if nearest < 0.75*period {
			miss++
		}
	}
	denom := have + miss
	if denom < 1 {
		denom = 1
	}
	var re, im float64
	for _, b := range beats {
		ph := 2 * math.Pi * (b - anchor) / period
		re += math.Cos(ph)
		im += math.Sin(ph)
	}
	n := float64(len(beats))
	phaseR := math.Hypot(re/n, im/n)
	return &GridFit{
		Anchor:    anchor,
		Period:    period,
		Coverage:  float64(have) / float64(denom),
		Explained: float64(onGrid) / n,
		NBeats:    len(beats),
		PhaseR:    phaseR,
	}
}

// FitConstantGrid tries all candidate periods, keeps grids that are well covered,
// prefers the one explaining the most detected beats (tie-break: closest to old BPM).
func FitConstantGrid(beats, downbeats []float64, oldBPM float64) *GridFit {
	if len(beats) < 16 {
		return nil
	}
	var fits []*GridFit
	for _, p := range CandidatePeriods(beats, downbeats, oldBPM) {
		if f := RefineGrid(beats, p); f != nil {
			fits = append(fits, f)
		}
	}
	if len(fits) == 0 {
		return nil
	}
	bestCov := 0.0
	for _, f := range fits {
		if f.Coverage > bestCov {
			bestCov = f.Coverage
		}
	}
	covMin := math.Min(0.90, bestCov)
	good := fits[:0]
	for _, f := range fits {
		if f.Coverage >= covMin {
			good = append(good, f)
		}
	}
	// most explained beats first (in coarse bands), then closest to current BPM
	sort.SliceStable(good, func(i, j int) bool {
		ei := round1(good[i].Explained)
		ej := round1(good[j].Explained)
		if ei != ej {
			return ei > ej
		}
		if oldBPM > 0 {
			return math.Abs(math.Log(good[i].BPM()/oldBPM)) < math.Abs(math.Log(good[j].BPM()/oldBPM))
		}
		return false
	})
	return good[0]
}

// ChooseOctave keeps the user's tempo octave when the fitted BPM is a x0.5/x2
// relative of the existing BPM (e.g. DnB gridded at 174 while the tracker says 87).
func ChooseOctave(fit GridFit, oldBPM float64, downbeats []float64) GridFit {
	if oldBPM <= 0 {
		// no stored BPM to respect: normalize into the DJ-conventional range
		for fit.BPM() < 90.0 {
			fit.Period /= 2
		}
		for fit.BPM() >= 180.0 {
			fit.Period *= 2
		}
		return fit
	}
	for _, factor := range []float64{1.0, 2.0, 0.5} {
		cand := fit.BPM() * factor
		if math.Abs(cand/oldBPM-1.0) < 0.04 {
			if factor == 1.0 {
				return fit
			}
			if factor == 0.5 {
				return halveDensity(fit, downbeats)
			}
			fit.Period /= factor
			return fit
		}
	}
	return fit
}

// halveDensity doubles the beat period, picking the anchor parity that matches
// the detected downbeats so the surviving gridlines stay on the strong beats.
func halveDensity(fit GridFit, downbeats []float64) GridFit {
	anchor := fit.Anchor
	if len(downbeats) > 0 {
		par := make([]float64, len(downbeats))
		for i, db := range downbeats {
			k := math.RoundToEven((db - fit.Anchor) / fit.Period)
			par[i] = pymod(k, 2)
		}
		if median(par) >= 0.5 {
			anchor += fit.Period
		}
	}
	fit.Anchor, fit.Period = anchor, fit.Period*2
	return fit
}

// ChooseOctaveRange is ChooseOctave plus a target tempo band [lo,hi] (0,0 = none):
// after the legacy choice the result folds into the band by power-of-2 shifts, so
// a track whose stored BPM sits in the wrong octave (DnB tagged 87) lands at 174
// even though the stored octave "wins" the legacy heuristic. Doubling density
// keeps the anchor; halving realigns parity to the downbeats.
func ChooseOctaveRange(fit GridFit, oldBPM float64, downbeats []float64, lo, hi float64) GridFit {
	fit = ChooseOctave(fit, oldBPM, downbeats)
	folded, ok := musiclib.FoldBPM(fit.BPM(), musiclib.BPMRange{Min: lo, Max: hi})
	if !ok {
		return fit
	}
	for fit.BPM() < folded-1e-9 {
		fit.Period /= 2
	}
	for fit.BPM() > folded+1e-9 {
		fit = halveDensity(fit, downbeats)
	}
	return fit
}

// PhaseOffsetS is the signed distance (s) from the old grid marker to the nearest
// fitted gridline.
func PhaseOffsetS(oldStartS float64, fit GridFit) float64 {
	d := pymod(oldStartS-fit.Anchor, fit.Period)
	if d <= fit.Period/2 {
		return d
	}
	return d - fit.Period
}

// circularAnchor is the circular-mean phase of beats modulo period, as a time offset.
func circularAnchor(beats []float64, period float64) float64 {
	var re, im float64
	for _, b := range beats {
		ph := 2 * math.Pi * b / period
		re += math.Cos(ph)
		im += math.Sin(ph)
	}
	n := float64(len(beats))
	return math.Atan2(im/n, re/n) / (2 * math.Pi) * period
}

// lstsq2 solves y ~= a + b*k (normal equations; matches np.linalg.lstsq on this
// well-conditioned 2-column system).
func lstsq2(k, y []float64) (a, b float64) {
	n := float64(len(k))
	var sk, skk, sy, sky float64
	for i := range k {
		sk += k[i]
		skk += k[i] * k[i]
		sy += y[i]
		sky += k[i] * y[i]
	}
	det := n*skk - sk*sk
	a = (skk*sy - sk*sky) / det
	b = (n*sky - sk*sy) / det
	return a, b
}

func diff(x []float64) []float64 {
	if len(x) < 2 {
		return nil
	}
	d := make([]float64, len(x)-1)
	for i := 1; i < len(x); i++ {
		d[i-1] = x[i] - x[i-1]
	}
	return d
}

func median(x []float64) float64 {
	if len(x) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), x...)
	sort.Float64s(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

// pymod mirrors Python's % operator (result sign follows the divisor).
func pymod(a, b float64) float64 {
	m := math.Mod(a, b)
	if m != 0 && (m < 0) != (b < 0) {
		m += b
	}
	return m
}

// round2/round1 mirror np.round / Python round (half-to-even).
func round2(v float64) float64 { return math.RoundToEven(v*100) / 100 }
func round1(v float64) float64 { return math.RoundToEven(v*10) / 10 }
