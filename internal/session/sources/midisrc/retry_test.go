package midisrc

import (
	"context"
	"testing"
	"time"
)

// TestRetrySchedule proves the backoff progression (4→8→16→32→60s cap) and the hard
// ceiling after retryMaxAttempts - the guard against endless midiInOpen kernel churn.
func TestRetrySchedule(t *testing.T) {
	var r retrySchedule
	want := []time.Duration{
		4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second,
		60 * time.Second, 60 * time.Second, 60 * time.Second, 60 * time.Second,
	}
	if len(want) != retryMaxAttempts {
		t.Fatalf("test out of sync: %d expectations vs retryMaxAttempts=%d", len(want), retryMaxAttempts)
	}
	for i, w := range want {
		d, ok := r.next()
		if !ok {
			t.Fatalf("attempt %d: exhausted early", i)
		}
		if d != w {
			t.Errorf("attempt %d: delay %v, want %v", i, d, w)
		}
	}
	if _, ok := r.next(); ok {
		t.Error("ceiling not enforced after retryMaxAttempts")
	}
	if _, ok := r.next(); ok {
		t.Error("ceiling must hold on repeated calls")
	}
	r.reset()
	if d, ok := r.next(); !ok || d != retryBaseDelay {
		t.Errorf("after reset: got (%v,%v), want (%v,true)", d, ok, retryBaseDelay)
	}
}

// TestWaitDeviceChange proves the post-ceiling watch re-arms ONLY on a device-list change
// and exits on ctx cancel.
func TestWaitDeviceChange(t *testing.T) {
	orig := listInputPorts
	defer func() { listInputPorts = orig }()

	// unchanged list + ctx cancel → false
	listInputPorts = func() []string { return []string{"A", "B"} }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := New(nil, "", "")
	if s.waitDeviceChange(ctx) {
		t.Error("ctx cancel must report false")
	}

	// list changes after baseline → true
	origWatch := retryWatchEvery
	retryWatchEvery = 5 * time.Millisecond
	defer func() { retryWatchEvery = origWatch }()
	calls := 0
	listInputPorts = func() []string {
		calls++
		if calls > 1 {
			return []string{"A", "B", "NEW"}
		}
		return []string{"A", "B"}
	}
	done := make(chan bool, 1)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	go func() { done <- s.waitDeviceChange(ctx2) }()
	select {
	case got := <-done:
		if !got {
			t.Error("device-list change must report true")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waitDeviceChange did not observe the change")
	}
}
