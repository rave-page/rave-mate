package ratewin

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

// TestClumpedVolumeIsNotUnderreported is the non-vacuity arm for defect 2 (a 500 ms window).
// Frames arrive at a constant cadence while the bytes CLUMP at a longer period - the field shape
// that displayed 0.1 Mbps on a healthy ~0.8 Mbps route. Every reading must stay within 2x of the
// true mean; with Span cut to 500 ms the same feed reports ~1/8 of truth.
func TestClumpedVolumeIsNotUnderreported(t *testing.T) {
	const (
		fps       = 40
		interB    = 300        // small inter-frame
		clumpB    = 120 << 10  // keyframe-sized payload
		clumpEver = 1300       // ms between clumps
		runMs     = 12000      // long enough for several clumps
		frameEver = 1000 / fps // ms
		trueMean  = float64(fps*interB) + float64(clumpB)*1000/clumpEver
	)
	var r Ring
	var readings []float64
	lastClump := 0
	for ms := 0; ms <= runMs; ms += frameEver {
		now := t0.Add(time.Duration(ms) * time.Millisecond)
		add := uint64(interB)
		if ms-lastClump >= clumpEver {
			add += clumpB
			lastClump = ms
		}
		r.Add(add, 1, now)
		if ms >= 6000 && ms%500 == 0 { // sample after the window is fully primed
			bps, _ := r.Rate(now)
			readings = append(readings, bps)
		}
	}
	if len(readings) < 8 {
		t.Fatalf("only %d readings - the harness is not exercising the window", len(readings))
	}
	lo := trueMean / 2
	for i, v := range readings {
		if v < lo {
			t.Errorf("reading %d = %.0f B/s, under half the true mean %.0f - a clumped-byte window is too short",
				i, v, trueMean)
		}
	}
}

// TestReadsDoNotMoveTheWindow is the non-vacuity arm for defect 1 (the window closed on read).
// A greedy 1 ms poller and a lazy 5 s poller must agree, and the greedy one must not leave the
// lazy one measuring a 1 ms span. The old shape (anchor refreshed on read) made the lazy reader
// report whatever the greedy one had just left behind.
func TestReadsDoNotMoveTheWindow(t *testing.T) {
	var r Ring
	const perFrame = 5000
	// 60 fps for 6 s, with a greedy reader polling every millisecond throughout.
	var greedyLast float64
	for ms := 0; ms <= 6000; ms++ {
		now := t0.Add(time.Duration(ms) * time.Millisecond)
		if ms%16 == 0 {
			r.Add(perFrame, 1, now)
		}
		greedyLast, _ = r.Rate(now)
	}
	now := t0.Add(6000 * time.Millisecond)
	lazyBps, lazyFps := r.Rate(now)
	if lazyBps != greedyLast {
		t.Fatalf("two readers disagree: greedy %.0f vs lazy %.0f B/s - Rate is not a pure read", greedyLast, lazyBps)
	}
	wantFps := 1000.0 / 16
	if math.Abs(lazyFps-wantFps) > wantFps*0.15 {
		t.Fatalf("fps %.1f, want ~%.1f - a reader truncated the window", lazyFps, wantFps)
	}
	if want := float64(perFrame); math.Abs(r.PerEvent(now)-want) > want*0.15 {
		t.Fatalf("bytes/frame %.0f, want ~%.0f", r.PerEvent(now), want)
	}
}

// TestStoppedCounterDecaysToZero: a route that died must not keep advertising its last rate.
func TestStoppedCounterDecaysToZero(t *testing.T) {
	var r Ring
	for ms := 0; ms <= 4000; ms += 16 {
		r.Add(5000, 1, t0.Add(time.Duration(ms)*time.Millisecond))
	}
	live := t0.Add(4000 * time.Millisecond)
	if bps, _ := r.Rate(live); bps <= 0 {
		t.Fatal("a live feed reports no rate")
	}
	dead := live.Add(Stale + time.Second)
	if bps, fps := r.Rate(dead); bps != 0 || fps != 0 {
		t.Fatalf("a stopped feed still reports %.0f B/s %.1f fps", bps, fps)
	}
}

// TestObserveIsPhaseIndependent covers the reader-driven consumers (CapFPS/DecFPS/EncBusyMs read a
// cumulative counter out of the child's SHM header). Two pollers at different phases must both see
// the real rate, not each other's leftovers.
func TestObserveIsPhaseIndependent(t *testing.T) {
	var r Ring
	var n uint64
	var slowReadings []float64
	for ms := 0; ms <= 12000; ms += 10 {
		now := t0.Add(time.Duration(ms) * time.Millisecond)
		if ms%20 == 0 {
			n++ // 50 fps
		}
		r.Observe(0, n, now) // fast poller, 100 Hz
		if ms%5000 == 0 && ms > 0 {
			_, fps := r.Rate(now) // slow poller, 0.2 Hz
			slowReadings = append(slowReadings, fps)
		}
	}
	if len(slowReadings) == 0 {
		t.Fatal("no slow readings")
	}
	want := 1000.0 / 20
	for i, v := range slowReadings {
		if math.Abs(v-want) > want*0.15 {
			t.Errorf("slow reading %d = %.1f fps, want ~%.1f - the fast poller moved the window", i, v, want)
		}
	}
}

// TestObserveReanchorsOnCounterReset: the child restarts and its SHM counters go back to 0. A
// delta against the old anchor would be a 2^64-sized negative, i.e. a garbage spike.
func TestObserveReanchorsOnCounterReset(t *testing.T) {
	var r Ring
	for ms := 0; ms <= 4000; ms += 100 {
		r.Observe(uint64(ms)*100, uint64(ms/16), t0.Add(time.Duration(ms)*time.Millisecond))
	}
	base := t0.Add(4000 * time.Millisecond)
	r.Observe(0, 0, base) // child respawned
	if bps, fps := r.Rate(base.Add(2 * time.Second)); bps != 0 || fps != 0 {
		t.Fatalf("after a counter reset the window reports %.0f B/s %.1f fps", bps, fps)
	}
	for ms := 0; ms <= 4000; ms += 16 {
		r.Observe(uint64(ms/16)*4000, uint64(ms/16), base.Add(time.Duration(ms)*time.Millisecond))
	}
	end := base.Add(4000 * time.Millisecond)
	if _, fps := r.Rate(end); fps < 50 || fps > 70 {
		t.Fatalf("the restarted child reports %.1f fps, want ~62.5", fps)
	}
}

// TestRingIsBounded: the sample ring must not grow with traffic (CLAUDE.md bound rule).
func TestRingIsBounded(t *testing.T) {
	var r Ring
	for ms := 0; ms < 600000; ms += 5 { // 10 minutes at 200 Hz
		r.Add(1000, 1, t0.Add(time.Duration(ms)*time.Millisecond))
	}
	r.mu.Lock()
	n := len(r.ring)
	r.mu.Unlock()
	if n > slots {
		t.Fatalf("ring holds %d samples, cap is %d", n, slots)
	}
}

// TestShortWindowReportsNothing: below MinSpan the estimate is noise, and a noise reading on a
// media panel is worse than a blank.
func TestShortWindowReportsNothing(t *testing.T) {
	var r Ring
	r.Add(100000, 10, t0)
	if bps, fps := r.Rate(t0.Add(100 * time.Millisecond)); bps != 0 || fps != 0 {
		t.Fatalf("a 100 ms window reported %.0f B/s %.1f fps", bps, fps)
	}
}
