package webui

import (
	"fmt"
	"sync"
)

// Latest-wins render mailbox for pointer-drag surfaces. actpos 'move' events arrive
// faster than a render+eval round-trip; running each render on the serial actWorker
// queues a repaint per event and lags the whole acts lane (same failure mpMoveCoalesce
// fixed for the player waveform). renderCoalesce collapses pending renders per key
// (cap 1, newest wins) with ONE goroutine in flight; fn reads current state at render
// time, so a late entry repaints the latest state - never a stale one.
var (
	rmMu   sync.Mutex
	rmPend = map[string]func(){}
	rmBusy = map[string]bool{}
)

// renderCoalesce schedules fn under key; a pending schedule is replaced (newest wins).
// Keys are namespaced per *UI: a headless remote session and the window UI may use the
// same fragment keys and must never collapse into each other's mailbox slot.
func (u *UI) renderCoalesce(key string, fn func()) {
	key = fmt.Sprintf("%p\x00%s", u, key)
	rmMu.Lock()
	rmPend[key] = fn
	if rmBusy[key] {
		rmMu.Unlock()
		return
	}
	rmBusy[key] = true
	rmMu.Unlock()
	u.bg(func() {
		for {
			rmMu.Lock()
			next, ok := rmPend[key]
			delete(rmPend, key)
			if !ok {
				rmBusy[key] = false
				rmMu.Unlock()
				return
			}
			rmMu.Unlock()
			next()
		}
	})
}
