package videoshare

import (
	"testing"
	"time"
)

// TestFPSGateUncapped: no cap = every tick passes (the readback must not be paced away).
func TestFPSGateUncapped(t *testing.T) {
	var g fpsGate
	now := int64(1)
	for i := 0; i < 100; i++ {
		if !g.allow(now) {
			t.Fatalf("uncapped gate blocked tick %d", i)
		}
		now += int64(time.Millisecond)
	}
	g.setFPS(-1)
	if !g.allow(now) {
		t.Fatal("fps<0 must mean uncapped")
	}
}

// TestFPSGateCapCountsPerSecond: a 30 cap polled at 250 Hz for one second lets exactly 30 through.
func TestFPSGateCapCountsPerSecond(t *testing.T) {
	var g fpsGate
	g.setFPS(30)
	now := int64(time.Second) // non-zero epoch
	allowed := 0
	for i := 0; i < 250; i++ { // 250 polls × 4 ms = 1 s
		if g.allow(now) {
			allowed++
		}
		now += 4 * int64(time.Millisecond)
	}
	if allowed != 30 {
		t.Fatalf("30 fps cap over 1 s of 250 Hz polling allowed %d, want 30", allowed)
	}
}

// TestFPSGateNoDrift: the slot advances by exactly one gap, so a 60 cap polled at 4 ms does not
// degrade to ~50 (the trap of re-anchoring on every fire).
func TestFPSGateNoDrift(t *testing.T) {
	var g fpsGate
	g.setFPS(60)
	now := int64(time.Second)
	allowed := 0
	for i := 0; i < 2500; i++ { // 10 s
		if g.allow(now) {
			allowed++
		}
		now += 4 * int64(time.Millisecond)
	}
	if allowed < 595 || allowed > 601 {
		t.Fatalf("60 fps cap over 10 s allowed %d, want ~600", allowed)
	}
}

// TestFPSGateIdleReanchor: after a quiet stretch the gate fires once and re-anchors instead of
// releasing a burst of catch-up frames.
func TestFPSGateIdleReanchor(t *testing.T) {
	var g fpsGate
	g.setFPS(10) // 100 ms gap
	now := int64(time.Second)
	if !g.allow(now) {
		t.Fatal("first tick must pass")
	}
	now += int64(5 * time.Second) // sender was quiet
	if !g.allow(now) {
		t.Fatal("tick after idle must pass")
	}
	if g.allow(now) {
		t.Fatal("second tick at the same instant must be blocked (no burst catch-up)")
	}
	now += int64(99 * time.Millisecond)
	if g.allow(now) {
		t.Fatal("tick inside the gap must be blocked")
	}
	now += int64(2 * time.Millisecond)
	if !g.allow(now) {
		t.Fatal("tick past the gap must pass")
	}
}

// TestFPSGateLiveChange: raising the cap takes effect without recreating the gate (shared capture
// re-rates when a faster route attaches).
func TestFPSGateLiveChange(t *testing.T) {
	var g fpsGate
	g.setFPS(10)
	now := int64(time.Second)
	if !g.allow(now) {
		t.Fatal("first tick")
	}
	now += int64(20 * time.Millisecond)
	if g.allow(now) {
		t.Fatal("20 ms into a 100 ms gap must block")
	}
	g.setFPS(0) // uncapped
	if !g.allow(now) {
		t.Fatal("uncapping must release immediately")
	}
	g.setFPS(50)
	if got := g.fps(); got != 50 {
		t.Fatalf("fps() = %v, want 50", got)
	}
}
