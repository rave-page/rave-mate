// Package perfmon is the always-on, lightweight perf collector behind `ctl perf` /
// `ctl remote-perf`: a 1 Hz ring (last ~10 min) of process CPU%/RSS (procstat) +
// goroutines/heap/GC (runtime/metrics - no ReadMemStats stop-the-world), plus a section
// registry where subsystems expose their own counters without perfmon importing them.
package perfmon

import (
	"context"
	"math"
	"runtime/metrics"
	"sync"
	"time"

	"rave.page/mate/internal/procstat"
)

const (
	sampleEvery = time.Second
	// Unobserved sampling: nobody has read a Snapshot/Report recently → sample at the
	// slow rate. The PDH system probe + metrics.Read at 1 Hz 24/7 buys nothing when no
	// perf card / ctl perf is watching; the ring's T field keeps charts correct across
	// uneven spacing.
	sampleIdle  = 5 * time.Second
	observedFor = 2 * time.Minute
	ringCap     = 600 // ~10 min at 1 Hz (longer when idle-sampled)
)

// runtime/metrics keys sampled each tick (all O(1) reads, no stop-the-world).
const (
	mGoroutines = "/sched/goroutines:goroutines"
	mHeapLive   = "/memory/classes/heap/objects:bytes"
	mRTTotal    = "/memory/classes/total:bytes"
	mHeapObjs   = "/gc/heap/objects:objects"
	mGCCycles   = "/gc/cycles/total:gc-cycles"
	mGCPauses   = "/sched/pauses/total/gc:seconds"
)

// Sample is one 1 Hz snapshot. GC fields are cumulative (window stats come from deltas).
type Sample struct {
	T             time.Time
	CPUPct        float64 // process CPU% (100 = one full core)
	RSSMB         float64
	SysOK         bool    // system-wide fields valid (Windows; first tick warms up)
	SysCPUPct     float64 // system CPU% across all cores
	SysMemUsedMB  float64
	SysMemTotalMB float64
	Goroutines    int
	HeapMB        float64 // live heap objects bytes
	RTTotalMB     float64 // total runtime-mapped memory
	HeapObjects   uint64
	GCCycles      uint64  // cumulative completed cycles
	GCPauseMs     float64 // cumulative pause total (histogram midpoint approx)
	GCMaxPauseMs  float64 // upper bound of the largest pause bucket hit since the prior sample
}

// ChildProc identifies a supervised child process for the report's children section
// (featurehost children - pid matched against the same top-process CPU sampling pass).
type ChildProc struct {
	Name     string
	PID      int
	Ready    bool
	Restarts int
	LastErr  string
}

// Monitor owns the sample ring. New + Run once at app start; Report any time.
type Monitor struct {
	startAt time.Time
	ps      procstat.Sampler
	sys     sysProbe // incremental system CPU/mem sampler (Windows; no-op elsewhere)

	mu         sync.Mutex
	ring       [ringCap]Sample
	n, next    int
	prevPauses []uint64  // last-seen pause-histogram counts (per-sample max-bucket delta)
	lastSeen   time.Time // last Snapshot() read (drives the observed/idle sample rate)

	rms []metrics.Sample // reused per-tick metrics.Read buffer (Run goroutine only)

	children func() []ChildProc // optional supervised-children lister (SetChildren)
}

// New builds a monitor (call Run to start sampling).
func New() *Monitor { return &Monitor{startAt: time.Now()} }

// SetChildren wires the supervised-child lister for the report's children section.
func (m *Monitor) SetChildren(fn func() []ChildProc) {
	m.mu.Lock()
	m.children = fn
	m.mu.Unlock()
}

// Run samples until ctx is done: 1 Hz while observed (a Snapshot read within
// observedFor), sampleIdle otherwise.
func (m *Monitor) Run(ctx context.Context) {
	interval := sampleEvery
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.add(m.sample())
			want := sampleIdle
			m.mu.Lock()
			if time.Since(m.lastSeen) < observedFor {
				want = sampleEvery
			}
			m.mu.Unlock()
			if want != interval {
				interval = want
				t.Reset(interval)
			}
		}
	}
}

// sample reads one snapshot (procstat CPU/RSS + runtime/metrics).
func (m *Monitor) sample() Sample {
	s := Sample{T: time.Now()}
	m.mu.Lock()
	if cpu, rss, ok := m.ps.CPURSS(); ok {
		s.CPUPct, s.RSSMB = cpu, rss
	}
	if cpu, used, total, ok := m.sys.tick(); ok {
		s.SysOK, s.SysCPUPct, s.SysMemUsedMB, s.SysMemTotalMB = true, cpu, used, total
	}
	prev := m.prevPauses
	m.mu.Unlock()

	if m.rms == nil {
		m.rms = []metrics.Sample{
			{Name: mGoroutines}, {Name: mHeapLive}, {Name: mRTTotal},
			{Name: mHeapObjs}, {Name: mGCCycles}, {Name: mGCPauses},
		}
	}
	rms := m.rms
	metrics.Read(rms)
	for _, r := range rms {
		switch r.Name {
		case mGoroutines:
			if r.Value.Kind() == metrics.KindUint64 {
				s.Goroutines = int(r.Value.Uint64())
			}
		case mHeapLive:
			if r.Value.Kind() == metrics.KindUint64 {
				s.HeapMB = float64(r.Value.Uint64()) / (1024 * 1024)
			}
		case mRTTotal:
			if r.Value.Kind() == metrics.KindUint64 {
				s.RTTotalMB = float64(r.Value.Uint64()) / (1024 * 1024)
			}
		case mHeapObjs:
			if r.Value.Kind() == metrics.KindUint64 {
				s.HeapObjects = r.Value.Uint64()
			}
		case mGCCycles:
			if r.Value.Kind() == metrics.KindUint64 {
				s.GCCycles = r.Value.Uint64()
			}
		case mGCPauses:
			if r.Value.Kind() == metrics.KindFloat64Histogram {
				h := r.Value.Float64Histogram()
				s.GCPauseMs = histSumMs(h)
				s.GCMaxPauseMs = histMaxBucketMs(h, prev)
				m.mu.Lock()
				m.prevPauses = append(m.prevPauses[:0], h.Counts...)
				m.mu.Unlock()
			}
		}
	}
	return s
}

// histSumMs approximates the histogram's cumulative pause total via bucket midpoints.
func histSumMs(h *metrics.Float64Histogram) float64 {
	var sum float64
	for i, c := range h.Counts {
		if c == 0 {
			continue
		}
		sum += float64(c) * bucketMidSec(h, i)
	}
	return sum * 1000
}

// histMaxBucketMs is the upper bound (ms) of the highest bucket whose count grew vs prev.
func histMaxBucketMs(h *metrics.Float64Histogram, prev []uint64) float64 {
	for i := len(h.Counts) - 1; i >= 0; i-- {
		var p uint64
		if i < len(prev) {
			p = prev[i]
		}
		if h.Counts[i] > p {
			return bucketUpperSec(h, i) * 1000
		}
	}
	return 0
}

// bucketMidSec is bucket i's midpoint, falling back to its finite edge at ±Inf bounds.
func bucketMidSec(h *metrics.Float64Histogram, i int) float64 {
	lo, hi := h.Buckets[i], h.Buckets[i+1]
	switch {
	case math.IsInf(lo, -1):
		return hi
	case math.IsInf(hi, 1):
		return lo
	default:
		return (lo + hi) / 2
	}
}

// bucketUpperSec is bucket i's upper bound, finite-clamped.
func bucketUpperSec(h *metrics.Float64Histogram, i int) float64 {
	if hi := h.Buckets[i+1]; !math.IsInf(hi, 1) {
		return hi
	}
	return h.Buckets[i]
}

// add appends one sample to the ring (oldest overwritten when full).
func (m *Monitor) add(s Sample) {
	m.mu.Lock()
	m.ring[m.next] = s
	m.next = (m.next + 1) % ringCap
	if m.n < ringCap {
		m.n++
	}
	m.mu.Unlock()
}

// Snapshot returns the retained samples oldest→newest (feeds the UI perf card).
// Reading marks the monitor observed → Run switches to the 1 Hz rate.
func (m *Monitor) Snapshot() []Sample {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastSeen = time.Now()
	out := make([]Sample, 0, m.n)
	start := 0
	if m.n == ringCap {
		start = m.next
	}
	for i := 0; i < m.n; i++ {
		out = append(out, m.ring[(start+i)%ringCap])
	}
	return out
}
