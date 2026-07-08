package governor

import (
	"context"
	"sync"
	"testing"
	"time"
)

// reset returns the singleton to its default (focused, nothing streaming) between tests.
func reset() {
	g.mu.Lock()
	g.focused, g.minimized, g.sizeMove, g.streaming = true, false, false, false
	g.deferred = map[string]func(){}
	g.prioSet = false
	g.mu.Unlock()
}

func TestUIAnimGating(t *testing.T) {
	reset()
	if !UIAnimAllowed() {
		t.Fatal("default (focused, not streaming) should allow UI ticks")
	}
	for _, tc := range []struct {
		name string
		set  func()
	}{
		{"unfocused", func() { SetFocused(false) }},
		{"minimized", func() { SetMinimized(true) }},
		{"sizemove", func() { SetSizeMove(true) }},
		{"streaming", func() { SetStreaming(true) }},
	} {
		reset()
		tc.set()
		if UIAnimAllowed() {
			t.Fatalf("%s: UI ticks should be paused", tc.name)
		}
	}
}

func TestBackgroundGating(t *testing.T) {
	reset()
	if !BackgroundAllowed() {
		t.Fatal("background work allowed when not streaming")
	}
	SetStreaming(true)
	if BackgroundAllowed() {
		t.Fatal("background work must be blocked while streaming")
	}
	// Size-move (window drag/resize) must also block background work - a heavy in-proc sweep then
	// starves the software WebView2 compositor and the window trails the cursor.
	reset()
	SetSizeMove(true)
	if BackgroundAllowed() {
		t.Fatal("background work must be blocked while dragging/resizing")
	}
	// Unfocused/minimized alone must NOT block background work (only streaming + size-move do).
	reset()
	SetFocused(false)
	if !BackgroundAllowed() {
		t.Fatal("unfocused should not block background work")
	}
	reset()
	SetMinimized(true)
	if !BackgroundAllowed() {
		t.Fatal("minimized should not block background work")
	}
}

// TestPriorityFollowsSizeMove exercises apply(): a drag drops the process to below-normal; an idle
// focused non-streaming window is normal priority.
func TestPriorityFollowsSizeMove(t *testing.T) {
	reset()
	SetSizeMove(true) // transition -> apply() runs
	g.mu.Lock()
	below, set := g.lastPrio, g.prioSet
	g.mu.Unlock()
	if !set || !below {
		t.Fatal("size-move should drop the process to below-normal")
	}
	SetSizeMove(false) // back to focused + idle + not streaming
	g.mu.Lock()
	below = g.lastPrio
	g.mu.Unlock()
	if below {
		t.Fatal("focused + idle + not streaming should be normal priority")
	}

	// Streaming still forces below-normal even when focused + idle.
	reset()
	SetStreaming(true)
	g.mu.Lock()
	below = g.lastPrio
	g.mu.Unlock()
	if !below {
		t.Fatal("streaming should drop the process to below-normal")
	}
	SetStreaming(false)
}

// TestWaitWhileBusy_ReturnsWhenAllowed: not busy -> returns immediately; ctx cancel unblocks.
func TestWaitWhileBusy_ReturnsWhenAllowed(t *testing.T) {
	reset()
	done := make(chan struct{})
	go func() { WaitWhileBusy(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WaitWhileBusy should return at once when work is allowed")
	}

	reset()
	SetStreaming(true) // busy -> WaitWhileBusy parks until ctx cancels
	ctx, cancel := context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() { WaitWhileBusy(ctx); close(done) }()
	select {
	case <-done:
		t.Fatal("WaitWhileBusy must block while streaming")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WaitWhileBusy should return when ctx is cancelled")
	}
	SetStreaming(false)
}

func TestWhenBackgroundAllowed_RunsImmediatelyWhenIdle(t *testing.T) {
	reset()
	ran := false
	WhenBackgroundAllowed("k", func() { ran = true })
	if !ran {
		t.Fatal("should run immediately when not streaming")
	}
}

func TestWhenBackgroundAllowed_DefersAndResumes(t *testing.T) {
	reset()
	SetStreaming(true)
	var mu sync.Mutex
	runs := 0
	done := make(chan struct{}, 1)
	WhenBackgroundAllowed("k", func() {
		mu.Lock()
		runs++
		mu.Unlock()
		done <- struct{}{}
	})
	mu.Lock()
	if runs != 0 {
		mu.Unlock()
		t.Fatal("must not run while streaming")
	}
	mu.Unlock()
	SetStreaming(false) // stream ends -> parked work released (on a goroutine)
	<-done
	mu.Lock()
	defer mu.Unlock()
	if runs != 1 {
		t.Fatalf("deferred work should run once on resume, got %d", runs)
	}
}

func TestWhenBackgroundAllowed_DedupByKey(t *testing.T) {
	reset()
	SetStreaming(true)
	first, second := false, false
	WhenBackgroundAllowed("k", func() { first = true })
	WhenBackgroundAllowed("k", func() { second = true }) // same key overwrites
	g.mu.Lock()
	n := len(g.deferred)
	g.mu.Unlock()
	if n != 1 {
		t.Fatalf("same key should dedup to 1 pending fn, got %d", n)
	}
	_ = first
	_ = second
}
