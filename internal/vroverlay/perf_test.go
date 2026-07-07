package vroverlay

import (
	"strings"
	"testing"
	"time"

	"rave.page/mate/internal/config"
)

func TestLoopStatMath(t *testing.T) {
	s := loopStat{budgetMs: 11}
	for i := 0; i < 99; i++ {
		s.observe(400 * time.Microsecond) // 0.4ms → bucket ≤0.5
	}
	s.observe(30 * time.Millisecond) // one outlier over budget

	s.mu.Lock()
	maxMs, over, n, p99 := s.maxMs, s.over, s.n, s.p99Ms()
	s.mu.Unlock()
	if n != 100 || over != 1 {
		t.Fatalf("n=%d over=%d, want 100/1", n, over)
	}
	if maxMs < 29 || maxMs > 31 {
		t.Fatalf("max=%v want ~30", maxMs)
	}
	// 99% of samples fit the ≤0.5ms bucket → p99 is its bound.
	if p99 != 0.5 {
		t.Fatalf("p99=%v want 0.5", p99)
	}
	out := s.String()
	for _, want := range []string{"n=100", "over-budget=1", "(>11ms)", "p99≤0.5ms"} {
		if !strings.Contains(out, want) {
			t.Fatalf("String missing %q: %s", want, out)
		}
	}
}

func TestLoopStatEmptyAndOverflowBucket(t *testing.T) {
	var s loopStat
	if s.String() != "n=0" {
		t.Fatalf("empty=%q", s.String())
	}
	s.observe(500 * time.Millisecond) // beyond the last bucket → overflow, p99 = max
	s.mu.Lock()
	p99, maxMs := s.p99Ms(), s.maxMs
	s.mu.Unlock()
	if p99 != maxMs {
		t.Fatalf("overflow p99=%v max=%v", p99, maxMs)
	}
}

func TestLoopStatEWMAConverges(t *testing.T) {
	var s loopStat
	for i := 0; i < 500; i++ {
		s.observe(2 * time.Millisecond)
	}
	s.mu.Lock()
	ewma := s.ewmaMs
	s.mu.Unlock()
	if ewma < 1.9 || ewma > 2.1 {
		t.Fatalf("ewma=%v want ~2", ewma)
	}
}

// PerfProbe must render on a stub (non-vr) manager without VR connected.
func TestPerfProbeStub(t *testing.T) {
	m := New(nil, nil, NewRuntime(), func() config.VROverlayFeature { return config.VROverlayFeature{} }, nil)
	out := m.PerfProbe()
	for _, want := range []string{"connected=false", "render tick", "input loop", "pointer cast", "tex uploads"} {
		if !strings.Contains(out, want) {
			t.Fatalf("probe missing %q: %s", want, out)
		}
	}
}
