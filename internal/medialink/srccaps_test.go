package medialink

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// hintConn captures the MetaRate hints a governor emits (frameConn seam).
type hintConn struct {
	mu    sync.Mutex
	hints []RateHint
}

func (c *hintConn) WriteFrame(f *Frame) error {
	h, err := DecodeRate(f)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.hints = append(c.hints, h)
	c.mu.Unlock()
	return nil
}
func (c *hintConn) ReadFrame() (*Frame, error) { return nil, errors.New("write-only") }
func (c *hintConn) Close() error               { return nil }
func (c *hintConn) last() (RateHint, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.hints) == 0 {
		return RateHint{}, 0
	}
	return c.hints[len(c.hints)-1], len(c.hints)
}

// liveCaps is a mutable per-source cap reader (what the media child's override map is).
type liveCaps struct {
	mu   sync.Mutex
	k, f int
}

func (l *liveCaps) get() (int, int) { l.mu.Lock(); defer l.mu.Unlock(); return l.k, l.f }
func (l *liveCaps) set(k, f int)    { l.mu.Lock(); l.k, l.f = k, f; l.mu.Unlock() }

func waitHint(t *testing.T, c *hintConn, want func(RateHint) bool, what string) RateHint {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h, n := c.last(); n > 0 && want(h) {
			return h
		}
		time.Sleep(5 * time.Millisecond)
	}
	h, n := c.last()
	t.Fatalf("%s: last hint %+v (n=%d)", what, h, n)
	return RateHint{}
}

// TCP-route governor (rateGovernor) with the VRAM governor OFF: per-source caps are still enforced -
// emitted immediately at route-up, changed live, and lifting them restores the base once.
func TestRateGovernorHonoursLiveSourceCaps(t *testing.T) {
	rm := &RouteManager{reportEvery: 10 * time.Millisecond, clock: NewMonotonicClock(),
		headroom: func() (uint64, bool) { return 0, false }, governorOn: func() bool { return false }}
	conn := &hintConn{}
	rio := &routeIO{conn: conn, st: newRouteStat("s", "B", 1, "recv")}
	caps := &liveCaps{k: 8000, f: 30}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rm.rateGovernor(ctx, rio, 1, func(int) int { return 20000 }, 60, nil, caps.get)

	h := waitHint(t, conn, func(h RateHint) bool { return h.MaxBitrateKbps == 8000 && h.MaxFPS == 30 }, "initial cap")
	if h.Type != MetaRate {
		t.Fatalf("hint type %q", h.Type)
	}
	if s := rio.st.snapshot(); s.CapKbps != 8000 || s.CapFPS != 30 || s.BudgetReason != "source-cap (user)" {
		t.Fatalf("telemetry caps %d/%d reason %q", s.CapKbps, s.CapFPS, s.BudgetReason)
	}
	caps.set(12000, 0) // live change on the running route
	waitHint(t, conn, func(h RateHint) bool { return h.MaxBitrateKbps == 12000 && h.MaxFPS == 60 }, "live raise")
	caps.set(0, 0) // lift: base restored once, then silence (fail-open as before)
	waitHint(t, conn, func(h RateHint) bool { return h.MaxBitrateKbps == 20000 && h.MaxFPS == 60 }, "restore")
	_, n := conn.last()
	time.Sleep(60 * time.Millisecond)
	if _, n2 := conn.last(); n2 != n {
		t.Fatalf("uncapped governor-off route kept emitting: %d -> %d hints", n, n2)
	}
}

// With the VRAM governor ON and trimming, the per-source cap is min()-combined: the more restrictive
// of ladder and cap binds on each axis.
func TestRateGovernorCapsCombineWithLadder(t *testing.T) {
	free := uint64(700) // floor 1024 → tier 2 (bitrate 35%, fps /2)
	rm := &RouteManager{reportEvery: 10 * time.Millisecond, clock: NewMonotonicClock(), govFloorMB: 1024,
		headroom: func() (uint64, bool) { return free, true }}
	conn := &hintConn{}
	rio := &routeIO{conn: conn, st: newRouteStat("s", "B", 1, "recv")}
	caps := &liveCaps{k: 5000, f: 50}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rm.rateGovernor(ctx, rio, 1, func(int) int { return 20000 }, 60, nil, caps.get)
	// ladder: 20000*35% = 7000 kbps, 60/2 = 30 fps; cap 5000/50 → 5000 kbps, 30 fps
	waitHint(t, conn, func(h RateHint) bool { return h.MaxBitrateKbps == 5000 && h.MaxFPS == 30 }, "min(ladder, cap)")
}

// UDP-route governor (congestGovernor): the cap bounds the controller's ceiling live.
func TestCongestGovernorHonoursLiveSourceCaps(t *testing.T) {
	rm := &RouteManager{reportEvery: 10 * time.Millisecond, clock: NewMonotonicClock()}
	conn := &hintConn{}
	rio := &routeIO{conn: conn, st: newRouteStat("s", "B", 1, "recv")}
	caps := &liveCaps{k: 6000, f: 25}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rm.congestGovernor(ctx, rio, fakeStats{}, 1, func(int) int { return 20000 }, 60, nil, caps.get)
	waitHint(t, conn, func(h RateHint) bool { return h.MaxBitrateKbps == 6000 && h.MaxFPS == 25 }, "initial cap")
	caps.set(0, 0)
	waitHint(t, conn, func(h RateHint) bool { return h.MaxBitrateKbps > 6000 && h.MaxFPS == 0 }, "cap lifted (probe up, fps uncapped)")
}

type fakeStats struct{}

func (fakeStats) Stats() UDPStats { return UDPStats{} }
