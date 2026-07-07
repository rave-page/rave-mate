package vroverlay

import "testing"

// A run of fully-failed ticks trips dead exactly at the budget, not before.
func TestVRHealthTripsAtBudget(t *testing.T) {
	var h vrHealth
	for range vrDeadBudget - 1 {
		h.observe(3, 3)
	}
	if h.dead() {
		t.Fatalf("tripped early at %d fails (budget %d)", h.consecFail, vrDeadBudget)
	}
	h.observe(3, 3)
	if !h.dead() {
		t.Fatalf("did not trip at budget %d (consecFail=%d)", vrDeadBudget, h.consecFail)
	}
}

// A single successful call inside the window resets the dead-run (healthy session never reconnects).
func TestVRHealthPartialSuccessResets(t *testing.T) {
	var h vrHealth
	for range vrDeadBudget * 2 {
		h.observe(4, 4) // all fail
		h.observe(4, 1) // one tick with successes → reset
	}
	if h.dead() {
		t.Fatalf("healthy-with-intermittent-failure session tripped (consecFail=%d)", h.consecFail)
	}
}

// Idle ticks (no OpenVR calls) are neutral: they neither trip nor clear an in-progress dead-run.
func TestVRHealthIdleIsNeutral(t *testing.T) {
	var h vrHealth
	h.observe(2, 2) // one failed tick
	for range vrDeadBudget * 3 {
		h.observe(0, 0) // idle - nothing to render
	}
	if h.consecFail != 1 {
		t.Fatalf("idle ticks changed the dead-run: consecFail=%d, want 1", h.consecFail)
	}
	if h.dead() {
		t.Fatal("idle session mistaken for dead")
	}
}

func TestVRHealthReset(t *testing.T) {
	var h vrHealth
	for range vrDeadBudget {
		h.observe(1, 1)
	}
	if !h.dead() {
		t.Fatal("precondition: should be dead")
	}
	h.reset()
	if h.dead() || h.consecFail != 0 {
		t.Fatalf("reset failed: consecFail=%d dead=%v", h.consecFail, h.dead())
	}
}
