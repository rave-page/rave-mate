package medialink

import (
	"testing"
	"time"
)

// fakeClock drives the pacer deterministically.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }

func TestPacerUnpaced(t *testing.T) {
	p := newSendPacer(0, time.Now)
	for i := 0; i < 100; i++ {
		if w := p.reserve(1500); w != 0 {
			t.Fatalf("unpaced pacer returned wait %v", w)
		}
	}
}

func TestPacerThrottlesBurst(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	// 1 MB/s. Burst = 5ms*rate = 5000 B, floored at pacerMinBurst (2*dgMaxPayload=17800). So burst
	// = 17800 B.
	p := newSendPacer(1<<20, clk.now)
	// Drain the burst: the first reserves cost no wait until the bucket empties.
	var firstWait time.Duration
	for i := 0; i < 20; i++ {
		firstWait = p.reserve(1200)
		if firstWait > 0 {
			break
		}
	}
	if firstWait == 0 {
		t.Fatal("pacer never throttled after draining the burst")
	}
	// After advancing time, tokens refill and a send is immediately allowed again.
	clk.add(50 * time.Millisecond)
	if w := p.reserve(1200); w != 0 {
		t.Fatalf("pacer did not refill after idle: wait %v", w)
	}
}

func TestPacerRateApprox(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	const rate = 10 << 20 // 10 MB/s
	p := newSendPacer(rate, clk.now)
	// Send 10 MB in 1200-byte datagrams, advancing the clock by each returned wait. Elapsed should
	// be ≈ 1 s (within the burst slack).
	const total = 10 << 20
	sent := 0
	for sent < total {
		w := p.reserve(1200)
		clk.add(w)
		sent += 1200
	}
	elapsed := clk.t.Sub(time.Unix(0, 0)).Seconds()
	if elapsed < 0.9 || elapsed > 1.1 {
		t.Fatalf("paced %d bytes in %.3fs, want ≈1s at 10MB/s", total, elapsed)
	}
}

// TestPacerFrameWithinInterval (item 2): at the derived pacing rate (a MULTIPLE of the budget), a
// keyframe-sized burst finishes well inside one 60fps frame interval - not stretched over several as
// pacing at the bare target bitrate would.
func TestPacerFrameWithinInterval(t *testing.T) {
	const interval = 16_666_667 // ns, 60 fps
	const keyframe = 150 << 10  // a ~20 Mbps keyframe
	clk := &fakeClock{t: time.Unix(0, 0)}
	p := newSendPacer(pacingRateFor(20000), clk.now) // multiple of 20 Mbps
	var total time.Duration
	for sent := 0; sent < keyframe; sent += 1200 {
		w := p.reserve(1200)
		clk.add(w)
		total += w
	}
	if total > time.Duration(interval) {
		t.Fatalf("keyframe paced over %v > one 60fps interval", total)
	}
	// The bare target would NOT fit (proves the gain matters): 150 KB at 20 Mbps ≈ 60 ms.
	clk2 := &fakeClock{t: time.Unix(0, 0)}
	bare := newSendPacer(20000*1000/8, clk2.now)
	var bareTotal time.Duration
	for sent := 0; sent < keyframe; sent += 1200 {
		w := bare.reserve(1200)
		clk2.add(w)
		bareTotal += w
	}
	if bareTotal <= time.Duration(interval) {
		t.Fatal("bare-target pacing unexpectedly fit in one interval - test is not proving the gain")
	}
	if pacingRateFor(0) < pacerMinRateBytes {
		t.Fatal("unknown-budget pacing rate below the floor")
	}
}

func TestPacerRetransmitBudget(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	p := newSendPacer(1<<20, clk.now) // 1 MB/s budget bucket
	// Spend the whole burst, then further tryReserves fail until time passes.
	ok := 0
	for i := 0; i < 1000; i++ {
		if p.tryReserve(1200) {
			ok++
		} else {
			break
		}
	}
	if ok == 0 {
		t.Fatal("budget bucket allowed nothing")
	}
	if p.tryReserve(1200) {
		t.Fatal("budget bucket allowed a spend past its burst without refill")
	}
	clk.add(100 * time.Millisecond) // refills ~100 KB
	if !p.tryReserve(1200) {
		t.Fatal("budget bucket did not refill over time")
	}
}
