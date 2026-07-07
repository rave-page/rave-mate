package automation

import "sync"

// eventBus fans run events out to studio subscribers (one set, keyed by seq for unsub).
// Mirrors the Electron studioEventListeners broadcast.
type eventBus struct {
	mu   sync.Mutex
	seq  int
	subs map[int]func(RunEvent)
}

func newEventBus() *eventBus { return &eventBus{subs: map[int]func(RunEvent){}} }

// on registers fn and returns an unsubscribe func.
func (b *eventBus) on(fn func(RunEvent)) func() {
	b.mu.Lock()
	id := b.seq
	b.seq++
	b.subs[id] = fn
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}
}

// emit delivers ev to every subscriber (snapshot under lock; call outside).
func (b *eventBus) emit(ev RunEvent) {
	b.mu.Lock()
	fns := make([]func(RunEvent), 0, len(b.subs))
	for _, fn := range b.subs {
		fns = append(fns, fn)
	}
	b.mu.Unlock()
	for _, fn := range fns {
		fn(ev)
	}
}
