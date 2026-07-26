package gpuwatch

import (
	"testing"
	"time"
)

// The tracker baselines on the FIRST scan (even at 0 = empty window) and never fires on
// pre-existing records; only ids above the baseline fire, each exactly once.
func TestTDRTrackerBaselineAndFire(t *testing.T) {
	var tr tdrTracker
	if tr.observe(500) {
		t.Fatal("fired on the baseline scan")
	}
	if tr.observe(500) {
		t.Fatal("fired on an already-seen tip")
	}
	if !tr.observe(501) {
		t.Fatal("did not fire on a new record")
	}
	if tr.observe(501) {
		t.Fatal("fired twice for the same record")
	}
	if !tr.observe(600) {
		t.Fatal("did not fire on the next new record")
	}
}

// An empty time window (newest==0) baselines quietly and stays quiet until a real record appears -
// the old code's `id <= high` break could never trigger at high==0, walking the whole log instead.
func TestTDRTrackerEmptyWindowBaseline(t *testing.T) {
	var tr tdrTracker
	if tr.observe(0) {
		t.Fatal("fired on an empty baseline window")
	}
	if tr.observe(0) {
		t.Fatal("fired on a still-empty window")
	}
	if !tr.observe(42) {
		t.Fatal("did not fire when the first real record appeared")
	}
	// Window rotates the record out again (time-bounded query) - must stay quiet, not re-baseline.
	if tr.observe(0) {
		t.Fatal("fired when the window emptied")
	}
	if tr.observe(42) {
		t.Fatal("re-fired an already-seen record after the window emptied")
	}
}

// TDR polling gets its own (much wider) default cadence than the hung-window poll.
func TestTDRPollDefault(t *testing.T) {
	o := Options{OnFault: func(Fault) {}}
	o.applyDefaults()
	if o.Poll != 2*time.Second {
		t.Fatalf("Poll=%v, want 2s", o.Poll)
	}
	if o.TDRPoll != 30*time.Second {
		t.Fatalf("TDRPoll=%v, want 30s", o.TDRPoll)
	}
}
