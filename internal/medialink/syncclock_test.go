package medialink

import (
	"testing"
	"time"
)

// TestOffsetEstimatorFilter: min-RTT sample wins; RTT > 2× window-min disqualified; dispersion
// spans qualifying samples only.
func TestOffsetEstimatorFilter(t *testing.T) {
	var e OffsetEstimator
	now := time.Now()
	if _, ok := e.Estimate(now); ok {
		t.Fatal("empty estimator must not estimate")
	}
	e.Add(100_000, 1_000_000, now) // qualifying (min RTT)
	e.Add(150_000, 1_800_000, now) // qualifying (≤ 2× min)
	e.Add(999_999, 5_000_000, now) // queueing-delayed: disqualified
	est, ok := e.Estimate(now)
	if !ok {
		t.Fatal("estimate missing")
	}
	if est.OffsetNs != 100_000 || est.RTTNs != 1_000_000 {
		t.Fatalf("min-RTT sample must win: %+v", est)
	}
	if est.DispersionNs != 50_000 {
		t.Fatalf("dispersion = %d, want 50000 (disqualified sample excluded)", est.DispersionNs)
	}
	if est.Samples != 3 {
		t.Fatalf("samples = %d, want 3", est.Samples)
	}
	if est.Locked {
		t.Fatal("2 qualifying samples must not lock")
	}
	e.Add(110_000, 1_200_000, now)
	if est, _ := e.Estimate(now); !est.Locked {
		t.Fatalf("3 qualifying fresh samples must lock: %+v", est)
	}
	// Negative RTT (clock stepped mid-probe) is rejected outright.
	e.Add(0, -1, now)
	if est, _ := e.Estimate(now); est.Samples != 4 {
		t.Fatalf("negative-RTT sample must be dropped: %+v", est)
	}
}

// TestOffsetEstimatorAging: samples beyond syncMaxAge fall out; stale lock degrades to holdover.
func TestOffsetEstimatorAging(t *testing.T) {
	var e OffsetEstimator
	base := time.Now()
	for i := 0; i < 4; i++ {
		e.Add(int64(1000+i), 500, base)
	}
	if est, _ := e.Estimate(base.Add(time.Second)); !est.Locked {
		t.Fatalf("fresh window must lock: %+v", est)
	}
	// Past syncStaleAfter the lock drops (holdover) even though samples still age in.
	stale := base.Add(syncStaleAfter + time.Second)
	est, ok := e.Estimate(stale)
	if !ok || est.Locked {
		t.Fatalf("stale samples must unlock: ok=%v %+v", ok, est)
	}
	// Past syncMaxAge everything is gone.
	if _, ok := e.Estimate(base.Add(syncMaxAge + time.Second)); ok {
		t.Fatal("aged-out window must not estimate")
	}
}

// TestSoftwareClockDiscipline: residual samples slew Now(); post-slew zero residuals keep the
// offset stable (no feedback double-correction).
func TestSoftwareClockDiscipline(t *testing.T) {
	c := NewSoftwareClock()
	if q := c.Quality(); q.Tier != TierSoftware || q.Locked || q.OffsetNs != 0 {
		t.Fatalf("initial quality = %+v", q)
	}
	var _ DisciplinedClock = c // seam holds
	var _ ClockSource = c

	before := c.Now()
	const slew = int64(500_000_000) // remote is +500 ms
	for i := 0; i < 3; i++ {
		// A real probe measures the residual against the already-disciplined clock.
		residual := slew - c.Quality().OffsetNs
		c.AddSample(residual, 1_000_000)
	}
	q := c.Quality()
	if !q.Locked {
		t.Fatalf("3 samples must lock: %+v", q)
	}
	if q.OffsetNs != slew {
		t.Fatalf("offset = %d, want %d", q.OffsetNs, slew)
	}
	if after := c.Now(); after-before < slew {
		t.Fatalf("Now() did not slew: %d → %d", before, after)
	}

	// Disciplined clock now measures ~zero residual - offset must hold, not double.
	c.AddSample(0, 500_000) // better RTT: becomes the winning sample
	if q := c.Quality(); q.OffsetNs != slew {
		t.Fatalf("offset drifted on zero residual: %d, want %d", q.OffsetNs, slew)
	}
}
