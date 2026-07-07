// Package logbus is the native suite's single source of runtime truth: a bounded in-memory
// ring buffer of structured entries with live subscriber fan-out. Every service logs here;
// rave-mate's Logs tab + both apps' `ctl logs` render it. Mirrors the web repo's
// failedMediaLogger / sseDebugLogger pattern. Shared by rave-mate AND rave-app (rave-mate's
// internal/logbus is a thin alias shim over this). Transparency rule (AGENTS.md §4): outbound
// calls log a redacted summary here - never tokens/secrets.
package logbus

import (
	"sync"
	"time"
)

type Level uint8

const (
	Debug Level = iota
	Info
	Warn
	Error
)

func (l Level) String() string {
	switch l {
	case Debug:
		return "DEBUG"
	case Info:
		return "INFO"
	case Warn:
		return "WARN"
	case Error:
		return "ERROR"
	default:
		return "?"
	}
}

// Entry is one immutable log record. Fields holds redacted structured context.
type Entry struct {
	Seq    uint64
	Time   time.Time
	Level  Level
	Source string // "traktor" | "stream" | "api" | "app" | "webview" | ...
	Msg    string
	Fields map[string]any
}

// Bus is a concurrency-safe ring buffer + subscriber registry.
type Bus struct {
	mu      sync.RWMutex
	ring    []Entry
	cap     int
	start   int // index of oldest entry when full
	full    bool
	seq     uint64
	subs    map[int]chan Entry
	nextSub int
	clock   func() time.Time
}

// New returns a Bus retaining the last capacity entries.
func New(capacity int) *Bus {
	if capacity < 1 {
		capacity = 1
	}
	return &Bus{
		ring:  make([]Entry, 0, capacity),
		cap:   capacity,
		subs:  make(map[int]chan Entry),
		clock: time.Now,
	}
}

// Log appends an entry and fans it out to subscribers (non-blocking: a full
// subscriber drops the entry rather than stalling the logging goroutine).
func (b *Bus) Log(level Level, source, msg string, fields map[string]any) {
	b.mu.Lock()
	b.seq++
	e := Entry{Seq: b.seq, Time: b.clock(), Level: level, Source: source, Msg: msg, Fields: fields}
	if len(b.ring) < b.cap {
		b.ring = append(b.ring, e)
	} else {
		b.ring[b.start] = e
		b.start = (b.start + 1) % b.cap
		b.full = true
	}
	subs := make([]chan Entry, 0, len(b.subs))
	for _, ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- e:
		default: // slow consumer - drop; Snapshot() still has it
		}
	}
}

func (b *Bus) Debug(source, msg string, f map[string]any) { b.Log(Debug, source, msg, f) }
func (b *Bus) Info(source, msg string, f map[string]any)  { b.Log(Info, source, msg, f) }
func (b *Bus) Warn(source, msg string, f map[string]any)  { b.Log(Warn, source, msg, f) }
func (b *Bus) Error(source, msg string, f map[string]any) { b.Log(Error, source, msg, f) }

// Snapshot returns all retained entries oldest→newest.
func (b *Bus) Snapshot() []Entry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Entry, 0, len(b.ring))
	if b.full {
		for i := 0; i < b.cap; i++ {
			out = append(out, b.ring[(b.start+i)%b.cap])
		}
	} else {
		out = append(out, b.ring...)
	}
	return out
}

// Subscribe returns a channel of future entries plus an unsubscribe func.
// The channel is buffered; on overflow entries are dropped (see Log).
func (b *Bus) Subscribe() (<-chan Entry, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextSub
	b.nextSub++
	ch := make(chan Entry, 256)
	b.subs[id] = ch
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
}
