package session

import (
	"fmt"
	"sync"
	"time"
)

// held is the current winning reading for one (scope, field).
type held struct {
	value  any
	source string
	ts     time.Time
	conf   float64
}

// Merger fuses multi-source Observations into one UnifiedState by per-field source
// priority + freshness (priority.go), and fans merged Updates out to subscribers. It is
// the single source of truth: sources push, the merger owns state, sinks read.
type Merger struct {
	clock func() time.Time // injectable for tests

	mu       sync.RWMutex
	fields   map[string]map[string]held // scopeKey → field → winner
	redact   RedactFunc                 // ID-mark redaction at the output boundaries (redact.go); nil = off
	stateVer uint64                     // bumped (under mu) whenever m.fields changes; keys the Snapshot cache

	// Snapshot raw-state memo: the flattened UnifiedState (UNREDACTED) built from m.fields,
	// reused while stateVer is unchanged so the per-tick rebuild (all scopes deep-copied) runs
	// once per real change, not once per consumer/tick. Redaction is NOT cached - it's applied
	// fresh on every Snapshot read (the ID-mark set mutates at runtime independent of Apply, so a
	// cached redacted view could leak). cacheMu guards these; it's a leaf lock (taken under mu.RLock).
	cacheMu   sync.Mutex
	cachedRaw *UnifiedState
	cachedVer uint64

	// cumulative merge counters (Stats - perfmon probe)
	nApply, nFieldsIn, nFieldsWon, nEmit uint64

	subMu   sync.Mutex
	subs    map[int]chan Update
	nextSub int
}

// NewMerger constructs an empty merger.
func NewMerger() *Merger {
	return &Merger{
		clock:  time.Now,
		fields: map[string]map[string]held{},
		subs:   map[int]chan Update{},
	}
}

// Apply fuses an Observation and emits a merged Update for the fields that won. Loaded
// resets the scope first (deck.loaded = full replacement).
func (m *Merger) Apply(o Observation) {
	if o.TS.IsZero() {
		o.TS = m.clock()
	}
	key := o.Scope.Key()

	m.mu.Lock()
	m.nApply++
	scope := m.fields[key]
	if scope == nil {
		scope = map[string]held{}
		m.fields[key] = scope
	}
	if o.Loaded {
		clear(scope) // boundary: drop stale winners before applying the fresh load
	}
	accepted := make(map[string]any, len(o.Fields))
	for f, v := range o.Fields {
		if v == nil {
			continue
		}
		m.nFieldsIn++
		cur, ok := scope[f]
		if !ok || m.wins(o.Source, o.Confidence, o.TS, f, cur) {
			scope[f] = held{value: v, source: o.Source, ts: o.TS, conf: o.Confidence}
			accepted[f] = v
		}
	}
	m.nFieldsWon += uint64(len(accepted))
	if len(accepted) > 0 || o.Loaded {
		m.nEmit++
		m.stateVer++ // m.fields changed (a field won, or a load cleared/reset the scope) - invalidate the Snapshot memo
	}
	m.mu.Unlock()

	if len(accepted) == 0 && !o.Loaded {
		return // nothing won, no boundary - no observable change
	}
	m.emit(Update{Type: updateType(o.Scope, o.Loaded), Scope: o.Scope, State: accepted, TS: o.TS})
}

// wins decides if an incoming reading beats the current holder for field f. Holder ages
// out via ttl (anyone fresher takes over); otherwise strictly-higher priority wins, then
// higher confidence, then the newer timestamp.
func (m *Merger) wins(src string, conf float64, ts time.Time, f string, cur held) bool {
	if src == cur.source {
		return true // same source always refreshes its own field
	}
	if m.clock().Sub(cur.ts) > ttl(f) {
		return true // holder aged out - fall back to the next-best source
	}
	if ri, rc := rank(src, f), rank(cur.source, f); ri != rc {
		return ri < rc
	}
	if conf != cur.conf {
		return conf > cur.conf
	}
	return ts.After(cur.ts)
}

// Stats is the perfmon probe body: cumulative merge counters + live scope/subscriber counts.
func (m *Merger) Stats() string {
	m.mu.RLock()
	applies, fin, fwon, emits, scopes := m.nApply, m.nFieldsIn, m.nFieldsWon, m.nEmit, len(m.fields)
	m.mu.RUnlock()
	m.subMu.Lock()
	subs := len(m.subs)
	m.subMu.Unlock()
	return fmt.Sprintf("applies=%d fieldsIn=%d fieldsWon=%d updatesEmitted=%d scopes=%d subscribers=%d",
		applies, fin, fwon, emits, scopes, subs)
}

// Snapshot returns the merged state across all scopes. ID-marked tracks are redacted - the
// merger's own state stays raw. The unredacted flattening is memoized on stateVer (rebuilt only
// when a field actually changed); redaction is re-applied every call so a live idmark edit takes
// effect immediately. The returned struct has fresh top-level maps but SHARES the per-scope field
// maps of the raw memo for scopes that aren't redacted - consumers are read-only (audited), so
// this is safe; do not mutate a returned scope's field map or its FieldValues.
func (m *Merger) Snapshot() UnifiedState {
	m.mu.RLock()
	ver := m.stateVer
	m.cacheMu.Lock()
	raw := m.cachedRaw
	if raw == nil || m.cachedVer != ver {
		built := m.buildRawLocked()
		raw = &built
		m.cachedRaw = raw
		m.cachedVer = ver
	}
	m.cacheMu.Unlock()
	fn := m.redact
	m.mu.RUnlock()
	return redactSnapshot(*raw, fn)
}

// buildRawLocked flattens m.fields into an UNREDACTED UnifiedState (fresh maps, safe to cache).
// Caller holds m.mu (at least RLock).
func (m *Merger) buildRawLocked() UnifiedState {
	st := UnifiedState{
		Decks:    map[string]map[string]FieldValue{},
		Channels: map[string]map[string]FieldValue{},
		Master:   map[string]FieldValue{},
	}
	for key, scope := range m.fields {
		kind, id := splitKey(key)
		fv := make(map[string]FieldValue, len(scope))
		for f, h := range scope {
			fv[f] = FieldValue{Value: h.value, Source: h.source, TS: h.ts, Confidence: h.conf}
		}
		switch kind {
		case ScopeDeck:
			st.Decks[id] = fv
		case ScopeChannel:
			st.Channels[id] = fv
		case ScopeMaster:
			st.Master = fv
		}
	}
	return st
}

// DeckFields returns one deck's merged fields (redacted, like Snapshot().Decks[deck]) with a
// single-scope copy - avoids the full-state deep copy when a consumer only needs one deck.
// ok=false when the deck has no state. Read-only; safe to retain.
func (m *Merger) DeckFields(deck string) (map[string]FieldValue, bool) {
	key := Scope{Kind: ScopeDeck, ID: deck}.Key()
	m.mu.RLock()
	defer m.mu.RUnlock()
	scope, ok := m.fields[key]
	if !ok {
		return nil, false
	}
	fv := make(map[string]FieldValue, len(scope))
	for f, h := range scope {
		fv[f] = FieldValue{Value: h.value, Source: h.source, TS: h.ts, Confidence: h.conf}
	}
	if m.redact != nil { // ScopeDeck is redactable
		fv = redactScopeShared(fv, m.redact)
	}
	return fv, true
}

// Subscribe returns a channel of future merged Updates + an unsubscribe func. Buffered;
// drops on overflow so a slow sink can't stall Apply.
func (m *Merger) Subscribe() (<-chan Update, func()) {
	m.subMu.Lock()
	defer m.subMu.Unlock()
	id := m.nextSub
	m.nextSub++
	ch := make(chan Update, 256)
	m.subs[id] = ch
	return ch, func() {
		m.subMu.Lock()
		defer m.subMu.Unlock()
		if c, ok := m.subs[id]; ok {
			delete(m.subs, id)
			close(c)
		}
	}
}

func (m *Merger) emit(u Update) {
	u = m.redactUpdate(u) // subscribers only ever see the redacted view
	m.subMu.Lock()
	chans := make([]chan Update, 0, len(m.subs))
	for _, ch := range m.subs {
		chans = append(chans, ch)
	}
	m.subMu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- u:
		default:
		}
	}
}
