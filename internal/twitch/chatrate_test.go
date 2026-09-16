package twitch

import (
	"testing"
	"time"
)

func TestChatRateWindow(t *testing.T) {
	var r ChatRate
	t0 := time.Unix(1_700_000_000, 0)
	for range 3 {
		r.Add(t0)
	}
	r.Add(t0.Add(30 * time.Second))
	r.Add(t0.Add(30 * time.Second))
	if got := r.PerMinute(t0.Add(30 * time.Second)); got != 5 {
		t.Fatalf("at +30s: %d, want 5", got)
	}
	if got := r.PerMinute(t0.Add(61 * time.Second)); got != 2 {
		t.Fatalf("at +61s the t0 bucket must have aged out: %d, want 2", got)
	}
	if got := r.PerMinute(t0.Add(120 * time.Second)); got != 0 {
		t.Fatalf("at +120s: %d, want 0", got)
	}
	// Slot reuse: a message exactly one minute after t0 lands in t0's slot and must not inherit its 3.
	r.Add(t0.Add(60 * time.Second))
	if got := r.PerMinute(t0.Add(60 * time.Second)); got != 3 {
		t.Fatalf("after slot reuse: %d, want 3 (2 at +30s + 1 at +60s)", got)
	}
}
