package logbus

import (
	"sync"
	"time"
)

// Gate suppresses repeat logs from retry/poll loops. A site calls Should(key)
// each time it would log; it returns true on a key CHANGE (state transition,
// including recovery) or when refresh elapsed since the last emit. Suppressed
// repeats are counted and reported via the second return on the next emit.
// Zero value is ready; safe for concurrent use.
type Gate struct {
	mu     sync.Mutex
	key    string
	lastAt time.Time
	hidden int
	clock  func() time.Time // test seam; nil = time.Now
}

// Should reports whether a log with key should be emitted now. refresh <= 0
// means transition-only (repeat keys never re-emit). suppressed = repeats
// swallowed since the previous emit (add to log fields when > 0).
func (g *Gate) Should(key string, refresh time.Duration) (suppressed int, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if g.clock != nil {
		now = g.clock()
	}
	if key == g.key && !g.lastAt.IsZero() && (refresh <= 0 || now.Sub(g.lastAt) < refresh) {
		g.hidden++
		return 0, false
	}
	suppressed = g.hidden
	g.key, g.lastAt, g.hidden = key, now, 0
	return suppressed, true
}

// Reset clears state so the next Should emits (use after recovery when the
// success path doesn't log).
func (g *Gate) Reset() {
	g.mu.Lock()
	g.key, g.lastAt, g.hidden = "", time.Time{}, 0
	g.mu.Unlock()
}
