package medialink

import (
	"sync/atomic"
	"time"
)

// framestages.go is STEP-0 instrumentation for the realtime-path port (owner directive 2026-11-12:
// realtime paths are native Zig/C, Go is the suspected micro-hang). It measures the INTER-FRAME
// INTERVAL at each pipeline stage so a hang is ATTRIBUTED to a stage, not assumed. Marks are made on
// the realtime goroutines and are lock-free + allocation-free (atomics only); percentiles are computed
// OFF the realtime thread at snapshot time (ctl sampling). It is pure measurement - no behaviour change
// - and stays as the acceptance instrument for the native port (assert no stage interval > 2 frame
// periods).

// monoBase anchors a process-monotonic clock for interval timing, independent of the media clock
// (which the child slews to the daemon) so clock discipline never shows up as a fake stage gap.
var monoBase = time.Now()

// monoNs returns monotonic nanoseconds since process start (time.Since uses the monotonic reading).
func monoNs() int64 { return int64(time.Since(monoBase)) }

// stageBucketsUpperMs are the inter-frame interval histogram upper bounds (ms). 1 ms resolution across
// 10-30 ms (one frame period at 30/60 fps = 33/16.67 ms) so p50/p99 are meaningful around the cadence;
// coarse below + a tail for hangs.
var stageBucketsUpperMs = [...]float64{
	4, 8,
	10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30,
	33, 40, 50, 75, 100, 150, 200, 500, 1e12,
}

// StageHist is a lock-free inter-frame-interval histogram for one pipeline stage. Mark is realtime-safe
// (single-writer per stage, atomics guard against any racing reader/writer); Snapshot is off-thread.
type StageHist struct {
	last    atomic.Int64 // monotonic ns of the previous mark (0 = none)
	count   atomic.Uint64
	sumNs   atomic.Int64
	maxNs   atomic.Int64
	over25  atomic.Uint64 // intervals > 25 ms
	over50  atomic.Uint64 // > 50 ms
	over100 atomic.Uint64 // > 100 ms
	buckets [len(stageBucketsUpperMs)]atomic.Uint64
}

// Mark records that a frame passed this stage at nowNs, folding the interval since the previous frame
// into the histogram. Allocation-free; safe to call on the frame path.
func (h *StageHist) Mark(nowNs int64) {
	prev := h.last.Swap(nowNs)
	if prev == 0 || nowNs <= prev {
		return // first frame, or a non-monotonic sample: no interval to record
	}
	d := nowNs - prev
	h.count.Add(1)
	h.sumNs.Add(d)
	for { // atomic max
		m := h.maxNs.Load()
		if d <= m || h.maxNs.CompareAndSwap(m, d) {
			break
		}
	}
	ms := float64(d) / 1e6
	if ms > 25 {
		h.over25.Add(1)
	}
	if ms > 50 {
		h.over50.Add(1)
	}
	if ms > 100 {
		h.over100.Add(1)
	}
	for i, ub := range stageBucketsUpperMs {
		if ms <= ub {
			h.buckets[i].Add(1)
			break
		}
	}
}

// StageSnapshot is one stage's interval distribution for telemetry (all ms).
type StageSnapshot struct {
	Count   uint64  `json:"n,omitempty"`
	MeanMs  float64 `json:"mean,omitempty"`
	P50Ms   float64 `json:"p50,omitempty"`
	P99Ms   float64 `json:"p99,omitempty"`
	MaxMs   float64 `json:"max,omitempty"`
	Over25  uint64  `json:"o25,omitempty"`  // intervals > 25 ms (hangs at 60 fps)
	Over50  uint64  `json:"o50,omitempty"`  // > 50 ms
	Over100 uint64  `json:"o100,omitempty"` // > 100 ms
}

// Snapshot computes the distribution off the realtime thread (bucket-approximated percentiles).
func (h *StageHist) Snapshot() StageSnapshot {
	n := h.count.Load()
	s := StageSnapshot{Count: n, MaxMs: float64(h.maxNs.Load()) / 1e6,
		Over25: h.over25.Load(), Over50: h.over50.Load(), Over100: h.over100.Load()}
	if n == 0 {
		return s
	}
	s.MeanMs = float64(h.sumNs.Load()) / 1e6 / float64(n)
	s.P50Ms = h.percentileMs(n, 0.50)
	s.P99Ms = h.percentileMs(n, 0.99)
	return s
}

// percentileMs returns the upper bound of the bucket holding the q-quantile (approx; good enough to
// localize a stage). The histogram may have advanced since count was read; clamp to the last bucket.
func (h *StageHist) percentileMs(n uint64, q float64) float64 {
	target := uint64(q * float64(n))
	var cum uint64
	for i := range stageBucketsUpperMs {
		cum += h.buckets[i].Load()
		if cum >= target {
			return stageBucketsUpperMs[i]
		}
	}
	return stageBucketsUpperMs[len(stageBucketsUpperMs)-1]
}

// FrameStages holds the per-stage interval histograms of one route. Receiver and sender each use their
// own subset (the other stays zero → omitted from telemetry).
type FrameStages struct {
	// Receiver pipeline.
	NetArrive  StageHist // first bytes of a frame readable off the socket (localizes network vs read-loop)
	RxComplete StageHist // a media frame left the transport (reassembled, authenticated)
	JBRelease  StageHist // the jitter buffer released it for playout
	DecSubmit  StageHist // handed to the decoder / publish sink
	// Sender pipeline.
	EncDone StageHist // an encoded AU became available to the send loop
	TxQueue StageHist // packetized + queued to the pacer (TCP: == the socket write return / "wire")
}

// StageStats is the telemetry snapshot of a route's stages (per RouteStat).
type StageStats struct {
	NetArrive  StageSnapshot `json:"net,omitempty"`
	RxComplete StageSnapshot `json:"rx,omitempty"`
	JBRelease  StageSnapshot `json:"jb,omitempty"`
	DecSubmit  StageSnapshot `json:"dec,omitempty"`
	EncDone    StageSnapshot `json:"enc,omitempty"`
	TxQueue    StageSnapshot `json:"tx,omitempty"`
}

// Snapshot captures all stages off the realtime thread.
func (f *FrameStages) Snapshot() *StageStats {
	if f == nil {
		return nil
	}
	return &StageStats{
		NetArrive: f.NetArrive.Snapshot(), RxComplete: f.RxComplete.Snapshot(), JBRelease: f.JBRelease.Snapshot(),
		DecSubmit: f.DecSubmit.Snapshot(), EncDone: f.EncDone.Snapshot(), TxQueue: f.TxQueue.Snapshot(),
	}
}
