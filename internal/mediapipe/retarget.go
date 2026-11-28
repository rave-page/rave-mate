package mediapipe

import (
	"math"
	"strings"
	"time"
)

// retarget.go - live bitrate/fps for the ffmpeg substitute. ffmpeg's CLI cannot retarget a running
// encode, so a hint that MATERIALLY changes the spawn rate respawns the child (forced IDR: a fresh
// child opens on one; the route stays up, the receiver sees a short gap). Control plane only (hint +
// 1 Hz supervisor tick); the per-frame fps drop is fpsGate in the feeder.

const (
	retargetMinGap  = 5 * time.Second // <= 1 respawn per window; > ratewin.Span so a measurement never spans two children
	retargetKbpsRel = 0.15            // kbps change >= 15% of the running value is material
	retargetFPSRel  = 0.10            // fps change >= 10% (and >= 1 fps) is material
)

// spawnRate is what one ffmpeg child is told: -b:v/-maxrate kbps and the declared -framerate.
type spawnRate struct {
	kbps int
	fps  float64
}

// retarget decides the substitute's spawn rate. Not concurrency-safe (encoder.mu guards it).
//
// Ceiling = offer budget, then the latest MaxBitrateKbps hint. Spawn kbps = ceiling * scalePct: VA-API
// pre-compensates radeonsi's measured RC overshoot (same seeds as the native engine: HEVC 68%, H.264
// 90%; ~1.2-1.8x unscaled - the 3.2 Mbps-on-a-1916-kbps-target webcam) and corrects it closed-loop from
// the measured output (observe). Spawn fps = min(source, receiver fps cap).
type retarget struct {
	ceilKbps int
	srcFPS   float64
	capFPS   int
	scalePct int  // 30..100
	closed   bool // closed-loop overshoot correction (vaapi)
	running  spawnRate
	last     time.Time // last respawn (or spawn) - debounce + measurement settle
	satRuns  int       // consecutive saturated-under windows (raise hysteresis)
}

// newRetarget seeds the decision for encoder (ceilKbps > 0, srcFPS > 0).
func newRetarget(encoder string, ceilKbps int, srcFPS float64, now time.Time) *retarget {
	r := &retarget{ceilKbps: ceilKbps, srcFPS: srcFPS, scalePct: 100, last: now}
	if strings.HasSuffix(encoder, "_vaapi") {
		r.closed = true
		r.scalePct = 90
		if strings.HasPrefix(encoder, "hevc") {
			r.scalePct = 68
		}
	}
	return r
}

// effFPS is the frame rate in force: the source rate, lowered by a receiver cap (>= 1).
func (r *retarget) effFPS() float64 {
	if r.capFPS > 0 && float64(r.capFPS) < r.srcFPS {
		return float64(r.capFPS)
	}
	return r.srcFPS
}

// want is the spawn rate the next child should get.
func (r *retarget) want() spawnRate {
	k := r.ceilKbps * r.scalePct / 100
	if k < 1 {
		k = 1
	}
	return spawnRate{kbps: k, fps: r.effFPS()}
}

// hint applies a receiver rate hint (MaxBitrateKbps 0 = keep the ceiling; MaxFPS 0 = clear the cap).
func (r *retarget) hint(kbps, fps int) {
	if kbps > 0 {
		r.ceilKbps = kbps
	}
	r.capFPS = max(fps, 0)
}

// observe feeds the measured output (kbps over ratewin.Span) of the RUNNING child. Closed loop: an
// overshoot > 1.1x the ceiling scales the spawn target down proportionally (to ~0.95x); a child that
// saturates its own target (>= 0.9x) while well under the ceiling (< 0.85x) for 3 ticks gets x1.2 back.
// Ignored until the window holds only this child's output.
func (r *retarget) observe(now time.Time, measuredKbps float64) {
	if !r.closed || measuredKbps <= 0 || r.ceilKbps <= 0 || now.Sub(r.last) < retargetMinGap {
		return
	}
	ceil := float64(r.ceilKbps)
	switch {
	case measuredKbps > ceil*1.10:
		r.scalePct = clampPct(int(float64(r.scalePct) * ceil * 0.95 / measuredKbps))
		r.satRuns = 0
	case measuredKbps < ceil*0.85 && measuredKbps >= float64(r.running.kbps)*0.9 && r.scalePct < 100:
		if r.satRuns++; r.satRuns >= 3 {
			r.scalePct = clampPct(int(math.Ceil(float64(r.scalePct) * 1.2)))
			r.satRuns = 0
		}
	default:
		r.satRuns = 0
	}
}

func clampPct(p int) int { return min(max(p, 30), 100) }

// decide reports whether the running child must respawn now: want() differs materially from the
// running rate AND the last respawn is >= retargetMinGap old. A debounced change is not lost - the
// next tick re-evaluates against the latest want().
func (r *retarget) decide(now time.Time) bool {
	if now.Sub(r.last) < retargetMinGap {
		return false
	}
	w, cur := r.want(), r.running
	material := cur.kbps <= 0 || math.Abs(float64(w.kbps-cur.kbps)) >= retargetKbpsRel*float64(cur.kbps)
	if d := math.Abs(w.fps - cur.fps); d >= 1 && d >= retargetFPSRel*cur.fps {
		material = true
	}
	if material {
		r.last = now
	}
	return material
}

// spawned records the rate a child was started with.
func (r *retarget) spawned(s spawnRate, now time.Time) {
	r.running = s
	r.last = now
}

// fpsGate drops frames before the encoder's stdin to hold a receiver fps cap: first frame per 1/fps
// slot by PTS, tolerance p/4 (evenly spaced; a source slower than the cap passes every frame). fps 0 =
// no gate. Pure; the feeder's only per-frame cost is this compare.
type fpsGate struct{ next int64 }

func (g *fpsGate) pass(pts int64, fps int) bool {
	if fps <= 0 {
		g.next = 0
		return true
	}
	period := int64(time.Second) / int64(fps)
	tol := period / 4
	if g.next == 0 || pts+4*period < g.next { // prime / PTS jumped back / cap raised
		g.next = pts + period
		return true
	}
	if pts+tol < g.next {
		return false
	}
	g.next += period
	if g.next <= pts+tol { // catch up after a gap / slow source
		g.next += ((pts+tol-g.next)/period + 1) * period
	}
	return true
}
