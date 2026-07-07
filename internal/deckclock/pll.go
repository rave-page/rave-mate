// Package deckclock smooths a deck's scrolling-waveform playback position. DJ sources report
// elapsed time only ~1/s (in ~1s steps); naively interpolating + snapping to each reading makes the
// waveform stutter. This is a velocity-PLL: it scrolls at the measured playback RATE and applies a
// small, capped proportional trim to ease out drift - near-constant speed, no per-frame ripple, and
// it only hard-snaps on a real discontinuity (seek / beat-jump / backspin / stop). It is a 1:1 port
// of the browser overlay's clock (internal/overlayserver/assets/overlay.html) so every output -
// browser, Spout, PNG, Syphon, PipeWire - behaves identically. Not safe for concurrent use; each
// deck owns one Clock, ticked from a single goroutine.
package deckclock

import (
	"math"
	"time"
)

// Shared tuning - keep in lockstep with the JS clock.
const (
	rateEMAold = 0.85 // weight on the previous rate estimate
	rateEMAnew = 0.15 // weight on the freshly measured rate
	kp         = 0.5  // proportional drift-correction gain
	maxTrimFr  = 0.2  // velocity trim cap, as a fraction of the playback rate
	snapErr    = 2.5  // |display−true| above this → hard snap (real seek/jump)
	jumpErr    = 1.2  // a fresh reading this far from predicted → mark a discontinuity
	maxFrameDt = 0.1  // clamp per-frame advance (a stalled feed shouldn't lurch)
)

// Clock holds one deck's smoothed playback clock.
type Clock struct {
	srcEt float64   // last FRESH elapsed reading (seconds)
	srcAt time.Time // when that fresh reading was observed
	rate  float64   // estimated playback rate (≈1.0; tracks pitch/tempo)

	dispPos float64   // smoothed displayed position
	dispAt  time.Time // when dispPos was last advanced
	vel     float64   // current scroll velocity

	playing  bool
	dur      float64
	haveSrc  bool
	haveDisp bool
	seek     bool
}

// Tick feeds the latest deck reading (raw elapsed - NOT a pre-interpolated value), play state and
// track length, and returns the smoothed position for `now`. Call once per render frame.
func (c *Clock) Tick(elapsed float64, playing bool, dur float64, now time.Time) float64 {
	c.observe(elapsed, playing, dur, now)
	return c.position(now)
}

// Reset clears the clock (e.g. a new track loaded) so the next Tick re-seeds cleanly.
func (c *Clock) Reset() { *c = Clock{} }

// observe reacts ONLY to a fresh elapsed value (stale repeats - what EQ/fader floods carry - are
// ignored so they can't perturb the scroll), estimating the rate over the real ~1s intervals.
func (c *Clock) observe(elapsed float64, playing bool, dur float64, now time.Time) {
	c.dur = dur
	if !c.haveSrc {
		c.srcEt, c.srcAt, c.rate, c.haveSrc, c.playing = elapsed, now, 1, true, playing
		return
	}
	if elapsed != c.srcEt { // fresh reading
		dtReal := now.Sub(c.srcAt).Seconds()
		dEt := elapsed - c.srcEt
		if playing && c.playing && dtReal > 0.1 && dtReal < 3 && dEt > 0.05 && dEt < 3 {
			if r := dEt / dtReal; r > 0.25 && r < 4 {
				c.rate = c.rate*rateEMAold + r*rateEMAnew
			}
		}
		predicted := c.srcEt
		if playing {
			predicted += c.rate * dtReal
		}
		if !playing || math.Abs(elapsed-predicted) > jumpErr {
			c.seek = true
		}
		c.srcEt, c.srcAt = elapsed, now
	}
	c.playing = playing
}

// position advances the critically-damped follower and returns the clamped display position.
func (c *Clock) position(now time.Time) float64 {
	trueRate := 0.0
	if c.playing {
		trueRate = c.rate
		if trueRate <= 0 {
			trueRate = 1
		}
	}
	truePos := c.srcEt + trueRate*now.Sub(c.srcAt).Seconds()

	if !c.haveDisp || c.seek {
		c.dispPos, c.vel, c.dispAt, c.haveDisp, c.seek = truePos, trueRate, now, true, false
	} else {
		fdt := now.Sub(c.dispAt).Seconds()
		if fdt > maxFrameDt {
			fdt = maxFrameDt
		} else if fdt < 0 {
			fdt = 0
		}
		c.dispAt = now
		err := truePos - c.dispPos
		if math.Abs(err) > snapErr { // real seek/beat-jump/backspin → snap once
			c.dispPos, c.vel = truePos, trueRate
		} else {
			maxTrim := maxTrimFr * trueRate
			trim := err * kp
			if trim > maxTrim {
				trim = maxTrim
			} else if trim < -maxTrim {
				trim = -maxTrim
			}
			c.vel = trueRate
			if c.playing {
				c.vel += trim
			}
			c.dispPos += c.vel * fdt
		}
	}

	pos := c.dispPos
	if c.dur > 0 && pos > c.dur {
		pos = c.dur
	}
	if pos < 0 {
		pos = 0
	}
	return pos
}
