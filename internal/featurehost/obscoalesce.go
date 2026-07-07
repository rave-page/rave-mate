package featurehost

import (
	"maps"
	"reflect"
	"sync"
	"time"

	"rave.page/mate/internal/session"
)

// obsCoalesceInterval caps child→daemon "obs" frames at ~10 Hz per scope. Safe: the merger
// is latest-wins per field (TTLs ≥5s) and overlay/PNG renderers interpolate elapsedTime
// from its merge timestamp, so sub-100ms tick granularity buys nothing daemon-side.
const obsCoalesceInterval = 100 * time.Millisecond

// continuousObsFields are the high-rate numeric fields safe to coalesce latest-state-wins.
// Anything else (track text, path, isPlaying, cue, unknown fields) is discrete: a value
// change forwards immediately.
var continuousObsFields = map[string]bool{
	session.FieldElapsedTime: true,
	session.FieldFader:       true,
	session.FieldEQHigh:      true,
	session.FieldEQMid:       true,
	session.FieldEQLow:       true,
	session.FieldFilter:      true,
	session.FieldBPM:         true,
	session.FieldPhase:       true,
}

// obsCoalescer rate-limits a child's Observation emissions to one frame per scope per
// interval (latest-state-wins merge of the buffered fields), so per-tick deck updates
// (Traktor POSTs elapsed many times/sec/deck, full deck state each) don't flood the
// daemon's stdout reader with JSON frames. Discrete transitions bypass the limiter:
// a Loaded boundary, or any non-continuous field changing value, flushes immediately.
// Leading edge is immediate too - the first observation after a quiet interval forwards
// without delay.
type obsCoalescer struct {
	emit     func(session.Observation)
	interval time.Duration
	now      func() time.Time // injectable for tests

	mu       sync.Mutex
	scopes   map[string]*obsScopeState
	timerSet bool
}

type obsScopeState struct {
	lastEmit time.Time
	pending  *session.Observation
	last     map[string]any // last forwarded discrete values (change detection)
}

// newObsCoalescer wraps emit with per-scope ≤1/interval coalescing.
func newObsCoalescer(interval time.Duration, emit func(session.Observation)) *obsCoalescer {
	return &obsCoalescer{emit: emit, interval: interval, now: time.Now, scopes: map[string]*obsScopeState{}}
}

// Add ingests one observation: forward now (discrete change / leading edge) or buffer it.
// emit runs under the lock - frame order on the wire matches merge order.
func (c *obsCoalescer) Add(o session.Observation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := o.Scope.Key()
	sc := c.scopes[key]
	if sc == nil {
		sc = &obsScopeState{last: map[string]any{}}
		c.scopes[key] = sc
	}
	now := c.now()

	switch {
	case o.Loaded:
		// Boundary resets the scope - pre-load buffered state is obsolete, don't merge it.
		sc.pending = nil
		sc.last = map[string]any{}
	case sc.discreteChanged(o.Fields):
		if sc.pending != nil {
			o = mergeObs(*sc.pending, o)
			sc.pending = nil
		}
	case sc.pending != nil || now.Sub(sc.lastEmit) < c.interval:
		// Continuous-only within the window → buffer latest-wins, trailing flush emits it.
		if sc.pending == nil {
			p := o
			p.Fields = maps.Clone(o.Fields) // own the map; sources may reuse theirs
			sc.pending = &p
		} else {
			maps.Copy(sc.pending.Fields, o.Fields)
			sc.pending.TS = o.TS
		}
		c.armFlushLocked()
		return
	}
	sc.noteDiscrete(o.Fields)
	sc.lastEmit = now
	c.emit(o)
}

// armFlushLocked schedules the trailing-edge flush once per window.
func (c *obsCoalescer) armFlushLocked() {
	if c.timerSet {
		return
	}
	c.timerSet = true
	time.AfterFunc(c.interval, c.flush)
}

// flush emits every buffered scope whose window elapsed; re-arms while any remain.
func (c *obsCoalescer) flush() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timerSet = false
	now := c.now()
	rearm := false
	for _, sc := range c.scopes {
		if sc.pending == nil {
			continue
		}
		if now.Sub(sc.lastEmit) < c.interval {
			rearm = true // recently force-flushed by a discrete change - next window
			continue
		}
		o := *sc.pending
		sc.pending = nil
		sc.noteDiscrete(o.Fields)
		sc.lastEmit = now
		c.emit(o)
	}
	if rearm {
		c.armFlushLocked()
	}
}

// discreteChanged reports whether any non-continuous field differs from its last
// forwarded value (Traktor sends full scope state per tick, so presence alone means
// nothing - only a value change is a transition).
func (s *obsScopeState) discreteChanged(fields map[string]any) bool {
	for k, v := range fields {
		if continuousObsFields[k] {
			continue
		}
		prev, ok := s.last[k]
		if !ok || !reflect.DeepEqual(prev, v) {
			return true
		}
	}
	return false
}

// noteDiscrete records the discrete values just forwarded.
func (s *obsScopeState) noteDiscrete(fields map[string]any) {
	for k, v := range fields {
		if !continuousObsFields[k] {
			s.last[k] = v
		}
	}
}

// mergeObs overlays b's fields on a's buffered ones (b wins; b's identity/TS kept).
func mergeObs(a, b session.Observation) session.Observation {
	out := b
	out.Fields = make(map[string]any, len(a.Fields)+len(b.Fields))
	maps.Copy(out.Fields, a.Fields)
	maps.Copy(out.Fields, b.Fields)
	return out
}
