package mediasync

import (
	"sync"
	"time"
)

// TimeSource is the house clock a Chaser locks media to. Position returns the current timeline
// position and whether the clock is running. v1 = WallClock; a medialink/SMPTE-driven house
// clock (LTC/ArtTimeCode) can satisfy this later without changing the chaser.
type TimeSource interface {
	Position() (pos time.Duration, running bool)
}

// WallClock is the v1 house clock: "start sync now" anchors the timeline to wall-clock time so
// Position advances in real time. Concurrency-safe.
type WallClock struct {
	mu      sync.Mutex
	running bool
	anchor  time.Time     // wall time captured at Start
	base    time.Duration // timeline position at anchor
	now     func() time.Time
}

// NewWallClock returns a stopped wall clock.
func NewWallClock() *WallClock { return &WallClock{now: time.Now} }

// Start (re)starts the clock at timeline position base (base=0 = "start sync now").
func (w *WallClock) Start(base time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.running = true
	w.base = base
	w.anchor = w.clock()
}

// StartNow starts the clock at position 0.
func (w *WallClock) StartNow() { w.Start(0) }

// Stop freezes the clock (Position reports running=false).
func (w *WallClock) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.running = false
}

// Running reports whether the clock is advancing.
func (w *WallClock) Running() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}

// Position returns base + elapsed-since-Start (or the frozen base when stopped).
func (w *WallClock) Position() (time.Duration, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.running {
		return w.base, false
	}
	return w.base + w.clock().Sub(w.anchor), true
}

func (w *WallClock) clock() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}
