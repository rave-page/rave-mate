package timecode

import (
	"errors"
	"math"
	"sync"

	"rave.page/mate/internal/medialink"
)

// ErrUnsupported is returned by the audio/MIDI backends on platforms without a WinMM implementation.
var ErrUnsupported = errors.New("timecode: audio/MIDI output not supported on this platform")

// ltcRiseSamples is the target edge rise time in samples (~40µs @48k, SMPTE ST 12-1) applied by the
// 1-pole slew filter - enough to round the biphase square so it isn't a raw hard edge, negligible
// vs. the ~10-sample half-bit cell (still cleanly decodable).
const ltcRiseSamples = 1.92

// gainToAmp converts a dBFS level to a peak int16 amplitude (clamped to a sane 0.05..1.0 FS).
func gainToAmp(dbfs float64) int16 {
	f := math.Pow(10, dbfs/20)
	if f > 1 {
		f = 1
	}
	if f < 0.05 {
		f = 0.05
	}
	return int16(f * 32767)
}

// ltcGen renders a continuous LTC audio stream, pulled block-by-block by the audio backend (the DAC
// clock paces it). It free-runs its own frame counter (seeded at start / re-seeded on jam), carries
// the medialink encoder's fractional-samples accumulator across frames (exact long-run rate at
// 29.97), and slew-limits every edge via a persistent 1-pole filter.
type ltcGen struct {
	mu         sync.Mutex // guards fill (backend goroutine) vs reseed (jam)
	rate       Rate
	sampleRate int
	amp        int16
	acc        float64 // cross-frame sub-sample accumulator (medialink)
	frame      int64   // free-running absolute frame index
	pending    []int16 // leftover samples from the last encoded frame
	slewCoef   float64
	y          float64 // slew filter state (carried across frames for continuity)
}

// newLTCGen builds an emitter at rate/sampleRate with peak amplitude amp, starting at startFrame.
func newLTCGen(rate Rate, sampleRate int, amp int16, startFrame int64) *ltcGen {
	tau := ltcRiseSamples / 2.197 // 1-pole 10–90% rise ≈ 2.197·τ
	return &ltcGen{
		rate: rate, sampleRate: sampleRate, amp: amp, frame: startFrame,
		slewCoef: 1 - math.Exp(-1/tau),
	}
}

// reseed jumps the free-running frame counter (jam/locate). Keeps the sample accumulator + slew
// state so the audio stream stays continuous across the jump.
func (g *ltcGen) reseed(frame int64) {
	g.mu.Lock()
	g.frame = frame
	g.pending = nil
	g.mu.Unlock()
}

// fill writes exactly len(buf) continuous LTC samples, encoding successive frames on demand. An
// unsupported rate yields silence (keeps the DAC fed without emitting a bogus signal).
func (g *ltcGen) fill(buf []int16) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := 0; i < len(buf); {
		if len(g.pending) == 0 {
			raw := medialink.EncodeLTCInto(medialink.TimecodeFromFrames(g.frame, g.rate), g.sampleRate, g.amp, &g.acc)
			g.frame++
			if raw == nil {
				for j := i; j < len(buf); j++ {
					buf[j] = 0
				}
				return
			}
			g.slew(raw)
			g.pending = raw
		}
		n := copy(buf[i:], g.pending)
		g.pending = g.pending[n:]
		i += n
	}
}

// slew applies the 1-pole edge smoothing in-place (state carried across calls).
func (g *ltcGen) slew(s []int16) {
	for i, v := range s {
		g.y += g.slewCoef * (float64(v) - g.y)
		s[i] = int16(g.y)
	}
}
