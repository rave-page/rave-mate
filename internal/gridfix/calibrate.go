package gridfix

// Detector-bias calibration (port of fix_grids.py calibrate()/bias_for): the beat
// detector has a small systematic phase offset vs the DJ software's grids that
// varies by codec. Measured against user-trusted grids (verified/locked), stored
// per file extension, and subtracted from every planned correction (PlanInput.BiasS).

import (
	"math"
	"path/filepath"
	"sort"
	"strings"
)

// Calibration is the systematic detector phase bias (s) per lowercase file
// extension (incl. dot); "*" is the overall fallback. Empty map = no bias.
type Calibration map[string]float64

// ForPath returns the bias for a path's extension, falling back to "*" then 0
// (mirror of bias_for).
func (c Calibration) ForPath(path string) float64 {
	if b, ok := c[strings.ToLower(filepath.Ext(path))]; ok {
		return b
	}
	return c["*"]
}

// CalibrationOffset measures one trusted track's detector-vs-grid phase offset (s).
// ok=false when no trustworthy fit exists (coverage < 0.9) or the fitted tempo
// disagrees with the stored BPM by >0.5% (phase comparison meaningless then) -
// exact mirror of the calibrate() per-track gate.
func CalibrationOffset(beats, downbeats []float64, oldBPM, oldStartS float64) (offset float64, ok bool) {
	fit := FitConstantGrid(beats, downbeats, oldBPM)
	if fit == nil || fit.Coverage < 0.9 {
		return 0, false
	}
	f := ChooseOctave(*fit, oldBPM, downbeats)
	if math.Abs(f.BPM()/oldBPM-1.0) > 0.005 {
		return 0, false
	}
	return PhaseOffsetS(oldStartS, f), true
}

// CalibrationQuota is the per-extension sample quota for a target of n tracks
// spread over nExts extensions (mirror of calibrate()'s per_ext).
func CalibrationQuota(nExts, n int) int {
	if nExts < 1 {
		nExts = 1
	}
	q := n / nExts
	if q < 10 {
		q = 10
	}
	return q
}

// StrideIndices picks up to perExt evenly-strided indices out of count items
// (mirror of v[::step][:per_ext]).
func StrideIndices(count, perExt int) []int {
	if count <= 0 || perExt <= 0 {
		return nil
	}
	step := count / perExt
	if step < 1 {
		step = 1
	}
	out := make([]int, 0, perExt)
	for i := 0; i < count && len(out) < perExt; i += step {
		out = append(out, i)
	}
	return out
}

// CalibrationStat reports one extension's measured offsets.
type CalibrationStat struct {
	N       int
	MedianS float64
	MADS    float64 // median absolute deviation (spread)
}

// SummarizeCalibration reduces per-extension offset samples to the stored bias
// map (per-ext median + "*" overall median) and display stats (mirror of the
// calibrate() summary; np.median = mean of middle two for even n, as median()).
func SummarizeCalibration(offsets map[string][]float64) (Calibration, map[string]CalibrationStat) {
	bias := Calibration{}
	stats := map[string]CalibrationStat{}
	var everything []float64
	exts := make([]string, 0, len(offsets))
	for ext := range offsets {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	for _, ext := range exts {
		v := offsets[ext]
		if len(v) == 0 {
			continue
		}
		m := median(v)
		bias[ext] = m
		dev := make([]float64, len(v))
		for i, d := range v {
			dev[i] = math.Abs(d - m)
		}
		stats[ext] = CalibrationStat{N: len(v), MedianS: m, MADS: median(dev)}
		everything = append(everything, v...)
	}
	if len(everything) > 0 {
		bias["*"] = median(everything)
	}
	return bias, stats
}
