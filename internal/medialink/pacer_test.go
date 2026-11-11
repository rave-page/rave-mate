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
