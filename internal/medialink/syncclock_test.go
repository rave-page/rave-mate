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

// TestOffsetEstimatorSubMicroFloor: on a loopback/same-host peer the min RTT is scheduling
// noise, not a path floor. Without syncMinRTTFloor a 300ns minimum disqualifies every other
// sample as "queueing-delayed" and the clock never locks (observed as a CI flake in
// crewlink's TestClockRestampUnderSkew).
func TestOffsetEstimatorSubMicroFloor(t *testing.T) {
	var e OffsetEstimator
	now := time.Now()
	e.Add(100_000, 300, now)     // 300ns "min"
	e.Add(101_000, 40_000, now)  // 40µs - 100× the min, still far under the floor
	e.Add(102_000, 120_000, now) // 120µs
	est, ok := e.Estimate(now)
	if !ok {
		t.Fatal("estimate missing")
	}
	if !est.Locked {
		t.Fatalf("sub-floor jitter must not disqualify: %+v", est)
	}
	// Above the floor the 2×-min rule still bites.
	var e2 OffsetEstimator
	e2.Add(100_000, 1_000_000, now)
	e2.Add(101_000, 5_000_000, now)
	e2.Add(102_000, 6_000_000, now)
	if est2, _ := e2.Estimate(now); est2.Locked {
		t.Fatalf("queueing-delayed samples must still be disqualified: %+v", est2)
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

// TestSoftwareClockResync: Resync empties the window (lock drops, slew HOLDS - no step), and
// fresh samples re-discipline to a new peer's domain without the old peer's min-RTT samples
// pinning the estimate.
func TestSoftwareClockResync(t *testing.T) {
	c := NewSoftwareClock()
	const oldDomain = int64(500_000_000) // peer A: +500 ms
	for i := 0; i < 3; i++ {
		c.AddSample(oldDomain-c.Quality().OffsetNs, 1_000_000)
	}
	if q := c.Quality(); !q.Locked || q.OffsetNs != oldDomain {
		t.Fatalf("pre-resync quality = %+v", q)
	}

	c.Resync()
	if q := c.Quality(); q.Locked {
		t.Fatal("lock must drop on resync")
	}
	if q := c.Quality(); q.OffsetNs != oldDomain {
		t.Fatalf("resync stepped the applied slew: %d, want %d (holdover)", q.OffsetNs, oldDomain)
	}

	// New peer B: -300 ms. Old A samples had better RTT; they must NOT survive the resync.
	const newDomain = int64(-300_000_000)
	for i := 0; i < 3; i++ {
		c.AddSample(newDomain-c.Quality().OffsetNs, 2_000_000)
	}
	q := c.Quality()
	if !q.Locked || q.OffsetNs != newDomain {
		t.Fatalf("post-resync quality = %+v, want locked at %d", q, newDomain)
	}
}
