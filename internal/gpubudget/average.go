package gpubudget

import (
	"sync"
	"time"
)

// FreeAverager is a time-window mean of free-VRAM readings. On the DJ PC free VRAM swings by hundreds
// of MB every second (Resolume), and a governor fed the raw read flapped 30<->50 Mbps every second
// (incident 2026-11-13). Bounded ring (avgRingCap samples, oldest overwritten); safe for concurrent use.
type FreeAverager struct {
	mu     sync.Mutex
	window time.Duration
	ring   [avgRingCap]freeSample
	n      int
	head   int
}

type freeSample struct {
	at   time.Time
	free uint64
}

const avgRingCap = 128

// NewFreeAverager returns an averager over window (<= 0 => 10 s).
func NewFreeAverager(window time.Duration) *FreeAverager {
	if window <= 0 {
		window = 10 * time.Second
	}
	return &FreeAverager{window: window}
}

// Add records a reading and returns the mean over the window (including it).
func (a *FreeAverager) Add(now time.Time, free uint64) uint64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ring[a.head] = freeSample{at: now, free: free}
	a.head = (a.head + 1) % avgRingCap
	if a.n < avgRingCap {
		a.n++
	}
	var sum, k uint64
	for i := 0; i < a.n; i++ {
		if s := a.ring[i]; now.Sub(s.at) <= a.window {
			sum += s.free
			k++
		}
	}
	if k == 0 {
		return free
	}
	return sum / k
}
