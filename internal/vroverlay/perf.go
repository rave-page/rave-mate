package vroverlay

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Hot-loop tick instrumentation for the perfmon report (`ctl perf` → [vroverlay] section):
// the 100ms render tick, the ~90Hz handleActions/pointer loop, and the pointer-cast cost -
// specifically to prove/clear the touchCast suspicion (it adds extra castRay passes per
// pointer frame). Observed on the VR goroutine; read from the ctl goroutine (mutex/atomics).

// loopBucketsMs are the loopStat histogram upper bounds (ms) for the p99-ish readout.
var loopBucketsMs = [...]float64{0.25, 0.5, 1, 2, 5, 10, 25, 50, 100}

// loopStat tracks one hot loop's tick duration: EWMA + max-since-start + count over budget
// + coarse buckets for a p99-ish percentile.
type loopStat struct {
	budgetMs float64 // over-budget threshold; 0 = no budget line

	mu      sync.Mutex
	ewmaMs  float64
	maxMs   float64
	n       uint64
	over    uint64
	buckets [len(loopBucketsMs) + 1]uint64
}

const loopEwmaAlpha = 0.05

// observe records one tick duration.
func (s *loopStat) observe(d time.Duration) {
	ms := float64(d.Nanoseconds()) / 1e6
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	if s.ewmaMs == 0 {
		s.ewmaMs = ms
	} else {
		s.ewmaMs += loopEwmaAlpha * (ms - s.ewmaMs)
	}
	if ms > s.maxMs {
		s.maxMs = ms
	}
	if s.budgetMs > 0 && ms > s.budgetMs {
		s.over++
	}
	i := 0
	for i < len(loopBucketsMs) && ms > loopBucketsMs[i] {
		i++
	}
	s.buckets[i]++
}

// p99Ms is the upper bound of the bucket where the cumulative count reaches 99%.
// (locked by caller)
func (s *loopStat) p99Ms() float64 {
	if s.n == 0 {
		return 0
	}
	target := s.n - s.n/100 // ceil-ish 99%
	var cum uint64
	for i, c := range s.buckets {
		cum += c
		if cum >= target {
			if i < len(loopBucketsMs) {
				return loopBucketsMs[i]
			}
			return s.maxMs // overflow bucket - only the max bounds it
		}
	}
	return s.maxMs
}

// String renders "ewma=0.42ms max=3.10ms p99≤1ms n=12345 over-budget=3 (>11ms)".
func (s *loopStat) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.n == 0 {
		return "n=0"
	}
	out := fmt.Sprintf("ewma=%.2fms max=%.2fms p99≤%.4gms n=%d", s.ewmaMs, s.maxMs, s.p99Ms(), s.n)
	if s.budgetMs > 0 {
		out += fmt.Sprintf(" over-budget=%d (>%.0fms)", s.over, s.budgetMs)
	}
	return out
}

// vrPerfCounters holds the pointer-cast + texture-upload counters read by PerfProbe.
type vrPerfCounters struct {
	ptrFrames   atomic.Uint64 // pointer frames that resolved a cast (updatePointer runs)
	touchFrames atomic.Uint64 // frames resolved by the near-field touch cast
	castRayNs   atomic.Uint64 // castRayN invocations (touchCast adds 2 extra passes/frame)
	texTotal    atomic.Uint64 // cumulative overlay texture uploads
	lastTexRate atomic.Int64  // uploads/s from the last 1s perf publish
}

// PerfProbe is the perfmon section for VR overlays: hot-loop tick durations, pointer-cast
// cost (touch vs ray share + castRayN passes per frame), texture-upload rate.
func (m *Manager) PerfProbe() string {
	out := fmt.Sprintf("connected=%v\n", m.rt.Available())
	out += "render tick (100ms):  " + m.renderStat.String() + "\n"
	out += "input loop (~90Hz):   " + m.inputStat.String() + "\n"
	out += "pointer cast:         " + m.castStat.String() + "\n"
	frames := m.perfC.ptrFrames.Load()
	if frames > 0 {
		out += fmt.Sprintf("  casts/frame=%.2f touch-share=%.0f%% (touchCast 2nd-pass cost is inside 'pointer cast')\n",
			float64(m.perfC.castRayNs.Load())/float64(frames),
			float64(m.perfC.touchFrames.Load())/float64(frames)*100)
	}
	out += fmt.Sprintf("tex uploads: last=%d/s total=%d", m.perfC.lastTexRate.Load(), m.perfC.texTotal.Load())
	if r := m.rend; r != nil && r.zig {
		// fallback>0 = a display list was rejected (cap hit / foreign glyph mask) and that render
		// cost the Go raster instead; ok=0 on a zigvr build means the Zig path never runs at all.
		out += fmt.Sprintf("\nzig raster: ok=%d fallback=%d", r.zigOK.Load(), r.zigFB.Load())
	}
	return out
}
