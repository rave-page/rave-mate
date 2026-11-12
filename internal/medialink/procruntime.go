package medialink

import (
	"math"
	"runtime/metrics"
	"sync"
)

// procruntime.go samples the Go runtime's stop-the-world + scheduler latencies in THIS process (the
// media plane - the media child in child mode, else the daemon). This is the direct evidence for the
// owner directive (2026-11-12): whether a Go GC/scheduler pause is the source of the frame-path
// micro-hangs. Read off the realtime thread (ctl sampling, ~1 Hz); each read reports the delta since
// the previous read, so a dashboard shows "pauses in the last second".

const (
	mGCPauses = "/gc/pauses:seconds"       // stop-the-world GC pause durations
	mSchedLat = "/sched/latencies:seconds" // time goroutines waited to be scheduled
)

// ProcRuntime reads the two runtime histograms and diffs them against the previous read.
type ProcRuntime struct {
	mu      sync.Mutex
	rms     []metrics.Sample // reused read buffer
	prevGC  []uint64
	prevSch []uint64
}

// PauseDist is one histogram's distribution over the sampling interval (all ms).
type PauseDist struct {
	Count    uint64  `json:"n,omitempty"`
	P50Ms    float64 `json:"p50,omitempty"`
	P99Ms    float64 `json:"p99,omitempty"`
	MaxMs    float64 `json:"max,omitempty"` // upper bound of the highest bucket that grew
	Over1ms  uint64  `json:"o1,omitempty"`
	Over5ms  uint64  `json:"o5,omitempty"`
	Over10ms uint64  `json:"o10,omitempty"`
}

// ProcRuntimeSnapshot is the media-plane process's runtime-pause evidence for one interval.
type ProcRuntimeSnapshot struct {
	GCPause  PauseDist `json:"gcPause"`
	SchedLat PauseDist `json:"schedLat"`
}

// NewProcRuntime builds a sampler (reusable read buffer).
func NewProcRuntime() *ProcRuntime {
	return &ProcRuntime{rms: []metrics.Sample{{Name: mGCPauses}, {Name: mSchedLat}}}
}

// Snapshot reads both histograms and returns the delta distribution since the previous Snapshot.
func (p *ProcRuntime) Snapshot() ProcRuntimeSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	metrics.Read(p.rms)
	var out ProcRuntimeSnapshot
	if v := p.rms[0].Value; v.Kind() == metrics.KindFloat64Histogram {
		out.GCPause, p.prevGC = pauseDelta(v.Float64Histogram(), p.prevGC)
	}
	if v := p.rms[1].Value; v.Kind() == metrics.KindFloat64Histogram {
		out.SchedLat, p.prevSch = pauseDelta(v.Float64Histogram(), p.prevSch)
	}
	return out
}

// pauseDelta computes the distribution of the bucket-count growth vs prev, returning it plus the new
// cumulative counts (for the next delta). Percentiles/max use bucket UPPER bounds; over-N counts use
// a bucket's LOWER bound (conservative: only buckets wholly above the threshold).
func pauseDelta(h *metrics.Float64Histogram, prev []uint64) (PauseDist, []uint64) {
	cur := make([]uint64, len(h.Counts))
	copy(cur, h.Counts)
	delta := make([]uint64, len(cur))
	var total uint64
	for i := range cur {
		var pv uint64
		if i < len(prev) {
			pv = prev[i]
		}
		if cur[i] >= pv {
			delta[i] = cur[i] - pv
		} else {
			delta[i] = cur[i] // counter reset (unlikely): take the current
		}
		total += delta[i]
	}
	var d PauseDist
	d.Count = total
	if total == 0 {
		return d, cur
	}
	p50t, p99t := uint64(0.50*float64(total)), uint64(0.99*float64(total))
	var cum uint64
	gotP50, gotP99 := false, false
	for i, n := range delta {
		if n == 0 {
			continue
		}
		lo := h.Buckets[i]
		if lo >= 0.001 {
			d.Over1ms += n
		}
		if lo >= 0.005 {
			d.Over5ms += n
		}
		if lo >= 0.010 {
			d.Over10ms += n
		}
	}
	for i, n := range delta {
		if n > 0 {
			d.MaxMs = bucketUpperMs(h, i)
		}
		cum += n
		if !gotP50 && cum >= p50t {
			d.P50Ms, gotP50 = bucketUpperMs(h, i), true
		}
		if !gotP99 && cum >= p99t {
			d.P99Ms, gotP99 = bucketUpperMs(h, i), true
		}
	}
	return d, cur
}

// bucketUpperMs is bucket i's upper bound in ms, finite-clamped at +Inf.
func bucketUpperMs(h *metrics.Float64Histogram, i int) float64 {
	if hi := h.Buckets[i+1]; !math.IsInf(hi, 1) {
		return hi * 1000
	}
	return h.Buckets[i] * 1000
}
