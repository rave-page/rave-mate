package governor

import (
	"sync"
	"testing"
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
	// Unfocused/minimized alone must NOT block background work (only streaming does).
	reset()
	SetFocused(false)
	if !BackgroundAllowed() {
		t.Fatal("unfocused should not block background work")
	}
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
