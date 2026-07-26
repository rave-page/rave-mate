package videoshare

import (
	"testing"
	"time"
)

// Clean join: every worker already stopped - returns 0 without burning the timeout.
func TestWaitAllCleanJoin(t *testing.T) {
	chans := make([]<-chan struct{}, 3)
	for i := range chans {
		ch := make(chan struct{})
		close(ch)
		chans[i] = ch
	}
	start := time.Now()
	if stuck := waitAll(chans, time.Second); stuck != 0 {
		t.Fatalf("stuck = %d, want 0", stuck)
	}
	if el := time.Since(start); el > 200*time.Millisecond {
		t.Fatalf("clean join burned %v of the timeout", el)
	}
}

// A wedged worker (never closes) must not hang Close: bounded wait, counted as stuck.
func TestWaitAllBoundedOnWedgedWorker(t *testing.T) {
	done := make(chan struct{})
	close(done)
	wedged := make(chan struct{}) // never closed - simulates a blocking driver call
	start := time.Now()
	stuck := waitAll([]<-chan struct{}{done, wedged, done}, 100*time.Millisecond)
	el := time.Since(start)
	if stuck != 1 {
		t.Fatalf("stuck = %d, want 1", stuck)
	}
	if el < 90*time.Millisecond || el > time.Second {
		t.Fatalf("join not bounded by timeout: %v", el)
	}
}

// A worker that stops within the window joins cleanly.
func TestWaitAllLateWorkerJoins(t *testing.T) {
	late := make(chan struct{})
	go func() {
		time.Sleep(30 * time.Millisecond)
		close(late)
	}()
	if stuck := waitAll([]<-chan struct{}{late}, time.Second); stuck != 0 {
		t.Fatalf("stuck = %d, want 0", stuck)
	}
}
