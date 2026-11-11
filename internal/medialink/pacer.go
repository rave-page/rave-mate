package medialink

import (
	"sync"
	"time"
)

// pacer.go is the §2.5 send pacer (Unit 2): a token-bucket that spreads a frame's datagrams over
// the wire instead of bursting a 4K keyframe's hundreds of datagrams into the NIC/switch at once
// (switch egress buffers are shallow on consumer gear - a microburst is self-inflicted loss). The
// bucket refills at a live rate (the congestion controller's target × a pacing gain) with a small
// burst allowance, so on a clean LAN at target rate it never throttles and adds no latency.
//
// Pure + time-injectable: reserve() returns how long to wait before sending n bytes and is the only
// method the send goroutine calls. rate 0 = unpaced (reserve returns 0).

type sendPacer struct {
	mu     sync.Mutex
	rate   float64 // bytes/sec (0 = unpaced)
	burst  float64 // bucket capacity (bytes)
	tokens float64
	last   time.Time
	now    func() time.Time
}

// pacerMinBurst is the floor on the bucket so a single MTU datagram always fits (never a deadlock).
const pacerMinBurst = 2 * dgMaxPayload

func newSendPacer(bytesPerSec float64, now func() time.Time) *sendPacer {
	if now == nil {
		now = time.Now
	}
	p := &sendPacer{now: now, last: now()}
	p.setRate(bytesPerSec)
	p.tokens = p.burst
	return p
}

// setRate updates the refill rate live (bytes/sec; ≤0 = unpaced). Burst = ~5 ms of rate, floored at
// pacerMinBurst so a full-size datagram always fits.
func (p *sendPacer) setRate(bytesPerSec float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rate = bytesPerSec
	if bytesPerSec <= 0 {
		p.burst = 0
		return
	}
	b := bytesPerSec * 0.005
	if b < pacerMinBurst {
		b = pacerMinBurst
	}
	p.burst = b
	if p.tokens > p.burst {
		p.tokens = p.burst
	}
}

// reserve accrues tokens since the last call, spends n, and returns how long the caller must wait
// before sending (0 when tokens already cover n). It allows the balance to go negative by at most
// one send so a datagram larger than the burst still eventually goes (paced by the deficit).
func (p *sendPacer) reserve(n int) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rate <= 0 {
		return 0
	}
	now := p.now()
	if elapsed := now.Sub(p.last).Seconds(); elapsed > 0 {
		p.tokens += elapsed * p.rate
		if p.tokens > p.burst {
			p.tokens = p.burst
		}
		p.last = now
	}
	var wait time.Duration
	if p.tokens < float64(n) {
		deficit := float64(n) - p.tokens
		wait = time.Duration(deficit / p.rate * float64(time.Second))
	}
	p.tokens -= float64(n)
	return wait
}

// tryReserve spends n tokens only if the bucket covers them, never going negative; returns whether
// it did. Used for the retransmit BUDGET (abandon rather than queue when the budget is spent).
// rate ≤ 0 means "unbudgeted" → always allow.
func (p *sendPacer) tryReserve(n int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.rate <= 0 {
		return true
	}
	now := p.now()
	if elapsed := now.Sub(p.last).Seconds(); elapsed > 0 {
		p.tokens += elapsed * p.rate
		if p.tokens > p.burst {
			p.tokens = p.burst
		}
		p.last = now
	}
	if p.tokens < float64(n) {
		return false
	}
	p.tokens -= float64(n)
	return true
}
