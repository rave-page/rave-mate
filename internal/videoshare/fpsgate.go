package videoshare

import (
	"sync/atomic"
	"time"
)

// fpsgate.go - the capture-rate cap, applied BEFORE the GPU→CPU readback. A VJ source rendering
// at 120+ fps used to pay a full ReceiveImage copy (8 MB at 1080p, 33 MB at 4K) for every frame
// and only then hit the route's fps cap - the readback bandwidth was spent on frames nobody sent.
// The gate lives in the poll loop instead, so an over-budget tick costs one channel wakeup.
// Live-settable: one shared capture serving routes with different caps runs at the HIGHEST
// requested rate and each route drops down to its own rate downstream.

// fpsGate paces work to at most N per second. fps <= 0 = uncapped. One caller (the poll loop)
// calls allow; setFPS may come from any goroutine.
type fpsGate struct {
	gap   atomic.Int64 // min ns between allowed ticks (0 = uncapped)
	armed atomic.Bool
	next  atomic.Int64 // earliest ns the next tick may run
}

// setFPS installs the cap (<= 0 = uncapped).
func (g *fpsGate) setFPS(fps float64) {
	if fps <= 0 {
		g.gap.Store(0)
		return
	}
	gap := int64(float64(time.Second) / fps)
	if gap < 1 {
		gap = 1
	}
	g.gap.Store(gap)
}

// fps returns the installed cap (0 = uncapped).
func (g *fpsGate) fps() float64 {
	gap := g.gap.Load()
	if gap <= 0 {
		return 0
	}
	return float64(time.Second) / float64(gap)
}

// allow reports whether a tick may run at nowNS, arming the next slot when it may. The slot
// advances by exactly one gap from the previous slot while we keep up (no drift, so a 60 cap
// really delivers 60 and not 55), and re-anchors to now after an idle stretch (no burst
// catch-up after a quiet sender).
func (g *fpsGate) allow(nowNS int64) bool {
	gap := g.gap.Load()
	if gap <= 0 {
		return true
	}
	if !g.armed.Load() {
		g.armed.Store(true)
		g.next.Store(nowNS + gap)
		return true
	}
	next := g.next.Load()
	if nowNS < next {
		return false
	}
	slot := next + gap
	if slot <= nowNS { // idle longer than one gap - re-anchor instead of firing a burst
		slot = nowNS + gap
	}
	g.next.Store(slot)
	return true
}
