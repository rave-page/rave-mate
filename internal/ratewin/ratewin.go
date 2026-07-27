// Package ratewin is the sliding-window rate estimator the media plane's live stats use.
//
// It exists because the same two defects were written four times over, in
// mediapipe.rate (OutFPS), mfenc.counterRate (CapFPS/DecFPS), mfenc.busyMean (EncBusyMs/
// DecBusyMs) and medialink.routeStat.rateBps - and only the last one got fixed:
//
//  1. The window closed ON READ. The reported value therefore depended on WHO polled and how
//     often: two pollers at different phases each got a window the other had just truncated, and
//     a 10 s telemetry line reported whatever span an unrelated 1 Hz reader happened to leave
//     behind. A window a reader can move is not a measurement.
//  2. The window was 500 ms. Media bytes CLUMP - small inter-frames continuously plus a
//     keyframe-sized payload every few seconds - so a short window lands between clumps and
//     reports the trickle alone. In the field a healthy ~0.9 Mbps route displayed 0.1 Mbps in
//     five readings out of six, and 0.1 Mbps is the number that means BLACK FRAMES to an
//     operator: a working route showing the reading for catastrophically broken.
//
// The fix, both defects at once: a ring of (t, cumulative A, cumulative B) samples appended at a
// fixed granularity, and a PURE read that derives every numerator and the divisor from the SAME
// two endpoints. Appends are idempotent within a granule, so a fast reader cannot shrink a slow
// reader's window - it just gets the same answer. Two counters ride one ring on purpose: bytes and
// frames measured over different spans is how "bytes per frame" becomes a fiction.
package ratewin

import (
	"sync"
	"time"
)

const (
	// Span is the sliding window length. Long enough to span a keyframe clump; the cost is that a
	// genuine rate change takes up to Span to be fully reflected, which is the right trade for a
	// panel whose job is "healthy or broken at a glance".
	Span = 4 * time.Second
	// SampleEvery is the ring granularity: one sample per 250 ms however often Observe is called.
	SampleEvery = 250 * time.Millisecond
	// MinSpan is the shortest window that is worth reporting - below it the estimate is noise.
	MinSpan = time.Second
	// Stale is how long after the counters last ADVANCED the rate stops being reported. A route
	// that STOPPED must not keep advertising the rate it had when it died: that is the
	// "healthy counters over a dead stream" shape the content oracle exists to catch.
	Stale = 3 * time.Second
)

// slots caps the ring: Span/SampleEvery + 2 samples (17 × 40 B ≈ 680 B per instance).
// Policy: drop-oldest - the window slides, it never accumulates with traffic.
const slots = int(Span/SampleEvery) + 2

// sample is one point of the window: wall time plus both cumulative counters at it.
type sample struct {
	t    time.Time
	a, b uint64
}

// Ring is a sliding window over two monotone cumulative counters. A is the "volume" counter
// (bytes, busy-nanoseconds) and B the "event" counter (frames, AUs); either may be unused. Safe
// for concurrent use.
type Ring struct {
	mu       sync.Mutex
	a, b     uint64    // latest cumulative values
	advanced time.Time // when they last CHANGED (staleness oracle, not when Observe was called)
	ring     []sample  // bounded at slots, drop-oldest
}

// Add accumulates deltas onto the counters and samples the window. For a producer that owns the
// counting (call it on the producing goroutine - that is what makes the boundaries reader-proof).
func (r *Ring) Add(da, db uint64, now time.Time) {
	r.mu.Lock()
	r.a, r.b = r.a+da, r.b+db
	if da != 0 || db != 0 {
		r.advanced = now
	}
	r.appendLocked(now)
	r.mu.Unlock()
}

// Observe records ABSOLUTE cumulative values read from elsewhere (a shared-memory header the child
// writes) and samples the window. Non-monotone input is ignored rather than wrapped into a
// negative rate: a child restart resets its counters, and a spike of 2^64/span is not a datum.
func (r *Ring) Observe(a, b uint64, now time.Time) {
	r.mu.Lock()
	if a < r.a || b < r.b { // counters went backwards: the source restarted - re-anchor
		r.a, r.b, r.advanced, r.ring = a, b, now, r.ring[:0]
		r.appendLocked(now)
		r.mu.Unlock()
		return
	}
	if a != r.a || b != r.b {
		r.advanced = now
	}
	r.a, r.b = a, b
	r.appendLocked(now)
	r.mu.Unlock()
}

// appendLocked adds a sample when the granule is up and slides the window. Caller holds mu.
func (r *Ring) appendLocked(now time.Time) {
	if k := len(r.ring); k == 0 || now.Sub(r.ring[k-1].t) >= SampleEvery {
		if len(r.ring) == slots { // drop-oldest; the ring never grows with traffic
			copy(r.ring, r.ring[1:])
			r.ring = r.ring[:slots-1]
		}
		r.ring = append(r.ring, sample{t: now, a: r.a, b: r.b})
	}
	// Keep exactly one sample older than Span so the window stays >= Span wide until it must
	// shrink, never wider.
	for len(r.ring) > 1 && now.Sub(r.ring[1].t) >= Span {
		copy(r.ring, r.ring[1:])
		r.ring = r.ring[:len(r.ring)-1]
	}
}

// Rate evaluates the window at now: A/s and B/s over the SAME endpoints, so the pair can be
// divided into a per-event volume without inventing a span. Pure read - it never mutates the
// window, so any number of pollers at any phase see the same measurement.
func (r *Ring) Rate(now time.Time) (aPerSec, bPerSec float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.ring) == 0 {
		return 0, 0
	}
	if !r.advanced.IsZero() && now.Sub(r.advanced) > Stale {
		return 0, 0 // stopped: report nothing rather than the last live value
	}
	o := r.ring[0]
	span := now.Sub(o.t).Seconds()
	if span < MinSpan.Seconds() {
		return 0, 0
	}
	return float64(r.a-o.a) / span, float64(r.b-o.b) / span
}

// PerEvent is A per B over the window (bytes per frame, busy-ns per frame): 0 when no events fell
// inside it. Derived from Rate so both terms share one span by construction - the whole reason the
// two counters live on one ring.
func (r *Ring) PerEvent(now time.Time) float64 {
	a, b := r.Rate(now)
	if b <= 0 {
		return 0
	}
	return a / b
}

// Totals returns the latest cumulative counters (lifetime, not windowed).
func (r *Ring) Totals() (a, b uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.a, r.b
}
