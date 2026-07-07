package vroverlay

import "math"

// filter.go - signal-smoothing primitives for the pointer pipeline: a 1€ filter (adaptive low-pass,
// cutoff rises with speed → steady at rest, low lag when moving) and its vector form. Shared by the
// pose-level ray filter and the hit filter. Pure + unit-tested; no OpenVR/cgo.

// 1€-filter default params (tuned for a VR ray cursor: kill rest-jitter, stay responsive on sweeps).
// minCutoff 2.5 Hz / beta 1.0 was tuned live 2026-07-02: at 1.2 Hz the cursor trailed a moving aim by
// ~130 ms (read as "drift"); the higher cutoff cuts that lag, the higher beta opens the cutoff further
// on deliberate sweeps.
const (
	euroMinCutoff = 2.5 // Hz - lower = smoother at rest, higher = less trailing lag
	euroBeta      = 1.0 // speed coefficient - higher = less lag when moving fast
	euroDCutoff   = 1.0 // Hz - derivative low-pass
)

func euroAlpha(cutoff, dt float64) float64 {
	tau := 1.0 / (2 * math.Pi * cutoff)
	return 1.0 / (1.0 + tau/dt)
}

// lowpass is an exponential smoother remembering its last output.
type lowpass struct {
	y   float64
	set bool
}

func (l *lowpass) filter(x, alpha float64) float64 {
	if !l.set {
		l.y, l.set = x, true
		return x
	}
	l.y += alpha * (x - l.y)
	return l.y
}

func (l *lowpass) reset() { l.set = false }

// oneEuro is a single-scalar 1€ filter (adaptive low-pass: cutoff rises with speed).
type oneEuro struct {
	x, dx   lowpass
	lastX   float64
	hasLast bool
}

func (f *oneEuro) filter(x, dt float64) float64 {
	if dt <= 0 {
		dt = 1.0 / 90
	}
	var dv float64
	if f.hasLast {
		dv = (x - f.lastX) / dt
	}
	f.lastX, f.hasLast = x, true
	edx := f.dx.filter(dv, euroAlpha(euroDCutoff, dt))
	cutoff := euroMinCutoff + euroBeta*math.Abs(edx)
	return f.x.filter(x, euroAlpha(cutoff, dt))
}

func (f *oneEuro) reset() { f.x.reset(); f.dx.reset(); f.hasLast = false }

// oneEuro3 filters a 3-vector component-wise (world hit point, ray origin, packed UV).
type oneEuro3 struct{ c [3]oneEuro }

func (v *oneEuro3) filter(x [3]float32, dt float64) [3]float32 {
	return [3]float32{
		float32(v.c[0].filter(float64(x[0]), dt)),
		float32(v.c[1].filter(float64(x[1]), dt)),
		float32(v.c[2].filter(float64(x[2]), dt)),
	}
}

func (v *oneEuro3) reset() {
	for i := range v.c {
		v.c[i].reset()
	}
}

func clamp01(x float32) float32 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}
