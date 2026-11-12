package medialink

import (
	"runtime"
	"testing"
)

func TestStageHistIntervals(t *testing.T) {
	var h StageHist
	const ms = int64(1e6)
	// Absolute monotonic marks (non-zero: 0 is the "no previous mark" sentinel, as real monoNs never
	// returns 0); intervals = 16, 16, 50, 100 ms.
	for _, at := range []int64{100 * ms, 116 * ms, 132 * ms, 182 * ms, 282 * ms} {
		h.Mark(at)
	}
	s := h.Snapshot()
	if s.Count != 4 {
		t.Fatalf("count=%d want 4 (first mark has no interval)", s.Count)
	}
	if s.MaxMs != 100 {
		t.Fatalf("max=%.1f want 100", s.MaxMs)
	}
	if want := (16.0 + 16 + 50 + 100) / 4; abs(s.MeanMs-want) > 0.01 {
		t.Fatalf("mean=%.3f want %.3f", s.MeanMs, want)
	}
	if s.Over25 != 2 || s.Over50 != 1 || s.Over100 != 0 { // 50,100 >25; 100 >50; none >100
		t.Fatalf("over25/50/100=%d/%d/%d want 2/1/0", s.Over25, s.Over50, s.Over100)
	}
	if s.P99Ms < 50 { // 4 samples: p99 rank is the 50ms one; max (asserted above) is the 100ms tail
		t.Fatalf("p99=%.1f unexpectedly low", s.P99Ms)
	}
}

// A rare slow frame among many fast ones shows up in the tail (p99/max), not the median - the whole
// point of the instrument (localizing a hang).
func TestStageHistTailCatchesHang(t *testing.T) {
	var h StageHist
	const ms = int64(1e6)
	at := int64(10 * ms)
	for i := 0; i < 200; i++ { // 200 frames at 16ms
		at += 16 * ms
		h.Mark(at)
	}
	at += 120 * ms // one 120ms hang
	h.Mark(at)
	s := h.Snapshot()
	if s.P50Ms > 20 {
		t.Fatalf("p50=%.1f should be the ~16ms steady interval", s.P50Ms)
	}
	if s.MaxMs < 120 || s.Over100 != 1 {
		t.Fatalf("hang not caught: max=%.1f over100=%d", s.MaxMs, s.Over100)
	}
}

func TestStageHistEmptyAndNonMonotonic(t *testing.T) {
	var h StageHist
	if s := h.Snapshot(); s.Count != 0 {
		t.Fatalf("empty hist count=%d", s.Count)
	}
	h.Mark(1000)
	h.Mark(500) // non-monotonic: ignored (no negative interval)
	if s := h.Snapshot(); s.Count != 0 {
		t.Fatalf("non-monotonic mark recorded an interval: count=%d", s.Count)
	}
}

func TestProcRuntimeSnapshot(t *testing.T) {
	p := NewProcRuntime()
	p.Snapshot() // prime the delta baseline
	runtime.GC()
	runtime.GC()
	s := p.Snapshot()
	if s.GCPause.Count == 0 {
		t.Fatal("no GC pauses recorded after two forced GCs (/gc/pauses:seconds not read)")
	}
	// Distribution must be well-formed (non-negative, p50<=p99<=max-ish within bucket approximation).
	if s.GCPause.P50Ms < 0 || s.GCPause.MaxMs < 0 {
		t.Fatalf("negative pause ms: %+v", s.GCPause)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
