package medialink

import (
	"fmt"
	"time"
)

// Rate is an SMPTE frame rate. Nominal is the labelled fps (24/25/30/50/60); Drop marks
// drop-frame counting (only valid for 30/60 → 29.97/59.94), which skips frame numbers to keep
// wall-clock alignment. Nominal==0 = "no timecode".
type Rate struct {
	Nominal int
	Drop    bool
}

// Common rates.
var (
	FPS24    = Rate{Nominal: 24}
	FPS25    = Rate{Nominal: 25}
	FPS30    = Rate{Nominal: 30}
	FPS2997  = Rate{Nominal: 30, Drop: true} // 29.97 drop-frame
	FPS50    = Rate{Nominal: 50}
	FPS60    = Rate{Nominal: 60}
	FPS5994  = Rate{Nominal: 60, Drop: true} // 59.94 drop-frame
	RateNone = Rate{}
)

// Valid reports whether the rate is usable (known nominal; drop only on 30/60).
func (r Rate) Valid() bool {
	switch r.Nominal {
	case 24, 25, 30, 50, 60:
		return !r.Drop || r.Nominal == 30 || r.Nominal == 60
	default:
		return false
	}
}

// exact returns the true fps as num/den (drop = nominal*1000/1001).
func (r Rate) exact() (num, den int64) {
	if r.Drop {
		return int64(r.Nominal) * 1000, 1001
	}
	return int64(r.Nominal), 1
}

// Exact is the exported true-fps ratio num/den (drop = nominal*1000/1001) - lets a house-clock
// generator advance a monotonic frame counter without re-deriving drop-frame arithmetic.
func (r Rate) Exact() (num, den int64) { return r.exact() }

// ltcSupported reports whether SMPTE 12M-1 LTC can carry this rate natively: 24/25/30 fps (frame
// tens is a 2-bit field → max 39). 50/59.94/60 need SMPTE 12M-2 field-doubling (not yet emitted).
func (r Rate) ltcSupported() bool {
	switch r.Nominal {
	case 24, 25:
		return !r.Drop
	case 30:
		return true
	default:
		return false
	}
}

// dropSkip is how many frame numbers drop-frame skips each minute (2 @30, 4 @60), 0 otherwise.
func (r Rate) dropSkip() int64 {
	if !r.Drop {
		return 0
	}
	return int64(r.Nominal) / 15 // 30→2, 60→4
}

// Timecode is an SMPTE position (hours:minutes:seconds:frames) at a Rate.
type Timecode struct {
	H, M, S, F int
	Rate       Rate
}

// Zero reports the "no timecode" sentinel (rate unset).
func (t Timecode) Zero() bool { return t.Rate.Nominal == 0 }

// Frames returns the absolute frame index from 00:00:00:00 (drop-frame aware).
func (t Timecode) Frames() int64 {
	n := int64(t.Rate.Nominal)
	fn := ((int64(t.H)*60+int64(t.M))*60+int64(t.S))*n + int64(t.F)
	if d := t.Rate.dropSkip(); d != 0 {
		totalMin := int64(t.H)*60 + int64(t.M)
		fn -= d * (totalMin - totalMin/10)
	}
	return fn
}

// TimecodeFromFrames rebuilds a Timecode from an absolute frame index at rate (drop-frame aware).
func TimecodeFromFrames(frames int64, rate Rate) Timecode {
	if frames < 0 {
		frames = 0
	}
	n := int64(rate.Nominal)
	if n == 0 {
		return Timecode{}
	}
	if d := rate.dropSkip(); d != 0 {
		framesPer10Min := n*600 - 9*d
		framesPerMin := n*60 - d
		ten := frames / framesPer10Min
		rem := frames % framesPer10Min
		if rem >= d {
			frames += d*9*ten + d*((rem-d)/framesPerMin)
		} else {
			frames += d * 9 * ten
		}
	}
	f := int(frames % n)
	s := int((frames / n) % 60)
	m := int((frames / (n * 60)) % 60)
	h := int((frames / (n * 3600)) % 24)
	return Timecode{H: h, M: m, S: s, F: f, Rate: rate}
}

// ToDuration returns the wall-clock offset of this timecode from 00:00:00:00.
func (t Timecode) ToDuration() time.Duration {
	num, den := t.Rate.exact()
	if num == 0 {
		return 0
	}
	// seconds = frames * den/num
	return time.Duration(t.Frames()) * time.Second * time.Duration(den) / time.Duration(num)
}

// TimecodeFromDuration converts a wall-clock offset to a Timecode at rate.
func TimecodeFromDuration(d time.Duration, rate Rate) Timecode {
	num, den := rate.exact()
	if num == 0 {
		return Timecode{}
	}
	frames := int64(d) * num / (int64(time.Second) * den)
	return TimecodeFromFrames(frames, rate)
}

// String formats HH:MM:SS:FF (";" before frames for drop-frame, per SMPTE convention).
func (t Timecode) String() string {
	sep := ':'
	if t.Rate.Drop {
		sep = ';'
	}
	return fmt.Sprintf("%02d:%02d:%02d%c%02d", t.H, t.M, t.S, sep, t.F)
}
