package twitch

import "sync"

// idWindowSize bounds IDWindow: the last N chat message ids. Twitch chat on one channel runs at
// most a few hundred lines a minute, so 1024 covers minutes of overlap between two sessions.
const idWindowSize = 1024

// IDWindow remembers the last idWindowSize ids (ring + set, fixed memory) so a chat message that
// reaches this instance twice is shown, logged and counted ONCE. The double happens when two paired
// instances are both signed in to the same Twitch channel: each child receives every line from
// EventSub and publishes it on the mesh bus under its own origin, and the bus can only dedupe
// re-deliveries of the SAME origin frame, not two origins reporting one message. The zero value
// is ready to use.
type IDWindow struct {
	mu   sync.Mutex
	ring [idWindowSize]string
	next int
	set  map[string]struct{}
}

// Seen reports whether id was recorded before, recording it if not. An empty id is never a
// duplicate (alerts carry no id and are not deduplicated here).
func (w *IDWindow) Seen(id string) bool {
	if id == "" {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.set == nil {
		w.set = make(map[string]struct{}, idWindowSize)
	}
	if _, dup := w.set[id]; dup {
		return true
	}
	if old := w.ring[w.next]; old != "" {
		delete(w.set, old) // evict the oldest so the set stays bounded with the ring
	}
	w.ring[w.next] = id
	w.set[id] = struct{}{}
	w.next = (w.next + 1) % idWindowSize
	return false
}
