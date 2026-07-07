package deckclock

import (
	"math"
	"testing"
	"time"
)

// Steady playback with a 1 Hz elapsed feed (DJ sources report ~1/s in ~1s steps), ticked at 30fps,
// must scroll monotonically at a near-constant velocity (no per-frame ripple, no backward steps).
func TestSteadyPlaybackSmooth(t *testing.T) {
	c := &Clock{}
	base := time.Unix(1000, 0)
	var last float64
	var lastT time.Time
	first := true
	minV, maxV := math.Inf(1), math.Inf(-1)
	for i := 0; i <= 300; i++ { // 10s @ 30fps
		tsec := float64(i) / 30
		now := base.Add(time.Duration(tsec * float64(time.Second)))
		reported := math.Floor(tsec) // 1 Hz, +1s steps, stale between
		pos := c.Tick(reported, true, 300, now)
		if !first && i > 45 { // after warmup
			dt := now.Sub(lastT).Seconds()
			v := (pos - last) / dt
			if v < minV {
				minV = v
			}
			if v > maxV {
				maxV = v
			}
			if pos < last-1e-9 {
				t.Fatalf("position went backward at i=%d: %.4f < %.4f", i, pos, last)
			}
		}
		last, lastT, first = pos, now, false
	}
	if minV < 0.7 || maxV > 1.3 {
		t.Fatalf("velocity not smooth: min=%.3f max=%.3f (want ~1.0)", minV, maxV)
	}
}

// A large fresh jump (seek / beat-jump) must snap the display near the new position quickly.
func TestSeekSnaps(t *testing.T) {
	c := &Clock{}
	base := time.Unix(2000, 0)
	for i := 0; i <= 150; i++ { // 5s steady
		tsec := float64(i) / 30
		c.Tick(math.Floor(tsec), true, 300, base.Add(time.Duration(tsec*float64(time.Second))))
	}
	// jump elapsed to 120s
	now := base.Add(6 * time.Second)
	pos := c.Tick(120, true, 300, now)
	if math.Abs(pos-120) > 1.0 {
		t.Fatalf("seek did not snap: pos=%.3f want ~120", pos)
	}
}

// While stopped, the position must hold (not advance with wall-clock).
func TestStoppedHolds(t *testing.T) {
	c := &Clock{}
	base := time.Unix(3000, 0)
	var pos float64
	for i := 0; i <= 60; i++ {
		now := base.Add(time.Duration(float64(i) / 30 * float64(time.Second)))
		pos = c.Tick(50, false, 300, now)
	}
	if math.Abs(pos-50) > 0.05 {
		t.Fatalf("stopped clock drifted: pos=%.3f want 50", pos)
	}
}
