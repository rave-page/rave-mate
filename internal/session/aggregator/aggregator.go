// Package aggregator is the orchestrator/hub of the DJ-data aggregation system. It owns
// the Merger, starts the enabled Sources (feeding observations into the merger) and Sinks
// (consuming merged state), and supports live per-source/sink toggling via Reconcile. It is
// the single "session" feature module; the merger itself stays cheap when no source runs.
package aggregator

import (
	"context"
	"fmt"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

const source = "session"

// liveTTL is how recently a source must have emitted to count as actively "receiving" data
// (vs merely running/listening). Traktor's master clock + deck updates tick well inside this.
const liveTTL = 10 * time.Second

type srcEntry struct {
	src     session.Source
	enabled func() bool
}

type snkEntry struct {
	sink    session.Sink
	enabled func() bool
}

// Aggregator wires sources → merger → sinks under one supervised lifecycle.
type Aggregator struct {
	log    *logbus.Bus
	mon    *logbus.Bus // per-observation monitor (Session monitor view); nil = off
	merger *session.Merger

	srcs []srcEntry
	snks []snkEntry

	mu       sync.Mutex
	parent   context.Context
	running  map[string]context.CancelFunc // component name → cancel
	lastSeen map[string]time.Time          // source ID → last observation time (liveness)
}

// New builds an aggregator around a merger.
func New(log *logbus.Bus, merger *session.Merger) *Aggregator {
	return &Aggregator{
		log: log, merger: merger,
		running:  map[string]context.CancelFunc{},
		lastSeen: map[string]time.Time{},
	}
}

// Merger exposes the underlying merger (for sinks driven outside the aggregator, e.g. the
// stream publisher).
func (a *Aggregator) Merger() *session.Merger { return a.merger }

// SetMonitor attaches an observation monitor bus (the Session monitor view subscribes). Every
// observation emitted by any source is logged here, before the merger fuses it.
func (a *Aggregator) SetMonitor(mon *logbus.Bus) { a.mon = mon }

// AddSource registers a source with a live-enabled predicate (reads config).
func (a *Aggregator) AddSource(src session.Source, enabled func() bool) {
	a.srcs = append(a.srcs, srcEntry{src: src, enabled: enabled})
}

// AddSink registers a sink with a live-enabled predicate.
func (a *Aggregator) AddSink(sink session.Sink, enabled func() bool) {
	a.snks = append(a.snks, snkEntry{sink: sink, enabled: enabled})
}

// Start binds the aggregator to ctx and starts every currently-enabled component.
func (a *Aggregator) Start(ctx context.Context) error {
	a.mu.Lock()
	a.parent = ctx
	a.mu.Unlock()
	a.Reconcile()
	return nil
}

// Stop cancels every running component.
func (a *Aggregator) Stop() {
	a.mu.Lock()
	a.parent = nil
	names := make([]string, 0, len(a.running))
	for n := range a.running {
		names = append(names, n)
	}
	a.mu.Unlock()
	for _, n := range names {
		a.stop(n)
	}
}

// Reconcile starts components whose feature turned on and stops those turned off. Safe to
// call repeatedly (e.g. after a settings change).
func (a *Aggregator) Reconcile() {
	a.mu.Lock()
	if a.parent == nil {
		a.mu.Unlock()
		return // not started yet
	}
	a.mu.Unlock()
	for _, e := range a.srcs {
		a.apply("src:"+e.src.ID(), e.enabled(), func(ctx context.Context) { a.runSource(ctx, e.src) })
	}
	for _, e := range a.snks {
		a.apply("sink:"+e.sink.ID(), e.enabled(), func(ctx context.Context) { a.runSink(ctx, e.sink) })
	}
}

// apply starts or stops one named component to match desired.
func (a *Aggregator) apply(name string, desired bool, run func(context.Context)) {
	a.mu.Lock()
	_, isRunning := a.running[name]
	parent := a.parent
	if desired && !isRunning && parent != nil {
		ctx, cancel := context.WithCancel(parent)
		a.running[name] = cancel
		a.mu.Unlock()
		go func() {
			defer a.clearRunning(name)
			defer a.guard("component", name) // runs before clearRunning on panic: logs + contains
			run(ctx)
		}()
		a.log.Info(source, "component started", map[string]any{"component": name})
		return
	}
	a.mu.Unlock()
	if !desired && isRunning {
		a.stop(name)
	}
}

func (a *Aggregator) stop(name string) {
	a.mu.Lock()
	cancel, ok := a.running[name]
	if ok {
		delete(a.running, name)
		if id, isSrc := strings.CutPrefix(name, "src:"); isSrc {
			delete(a.lastSeen, id) // a stopped source isn't "receiving"
		}
	}
	a.mu.Unlock()
	if ok {
		cancel()
		a.log.Info(source, "component stopped", map[string]any{"component": name})
	}
}

func (a *Aggregator) clearRunning(name string) {
	a.mu.Lock()
	delete(a.running, name)
	a.mu.Unlock()
}

// runSource runs a source under a panic guard so one source can't crash the daemon. The
// emit is wrapped to stamp the source's last-observation time, so the UI can distinguish
// "running but silent" from "actually receiving data".
func (a *Aggregator) runSource(ctx context.Context, src session.Source) {
	defer a.guard("source", src.ID())
	id := src.ID()
	emit := func(o session.Observation) {
		a.mu.Lock()
		a.lastSeen[id] = time.Now()
		a.mu.Unlock()
		if a.mon != nil {
			a.mon.Info(string(o.Source), observationSummary(o), map[string]any{"conf": o.Confidence})
		}
		a.merger.Apply(o)
	}
	if err := src.Start(ctx, emit); err != nil && ctx.Err() == nil {
		a.log.Error(source, "source error", map[string]any{"source": id, "error": err.Error()})
	}
}

// runSink runs a sink under a panic guard.
func (a *Aggregator) runSink(ctx context.Context, sink session.Sink) {
	defer a.guard("sink", sink.ID())
	if err := sink.Start(ctx, a.merger); err != nil && ctx.Err() == nil {
		a.log.Error(source, "sink error", map[string]any{"sink": sink.ID(), "error": err.Error()})
	}
}

// observationSummary renders "scope field=val field=val" for the Session monitor view.
func observationSummary(o session.Observation) string {
	keys := make([]string, 0, len(o.Fields))
	for k := range o.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	if o.Scope.Kind != "" {
		fmt.Fprintf(&b, "%s", o.Scope.Kind)
		if o.Scope.ID != "" {
			fmt.Fprintf(&b, " %s", o.Scope.ID)
		}
		b.WriteString("  ")
	}
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		v := fmt.Sprintf("%v", o.Fields[k])
		if len(v) > 32 {
			v = v[:31] + "…"
		}
		fmt.Fprintf(&b, "%s=%s", k, v)
	}
	return b.String()
}

func (a *Aggregator) guard(kind, id string) {
	if r := recover(); r != nil {
		a.log.Error(source, kind+" panicked", map[string]any{"id": id, "panic": fmt.Sprintf("%v", r), "stack": string(debug.Stack())})
	}
}

// ── UI introspection ─────────────────────────────────────────────────────────

// SourceInfo describes a registered source's state + capabilities for the UI. Receiving is
// the honest "data is actually flowing" signal (last observation within liveTTL) - distinct
// from Running ("the source goroutine/listener is up"), which is what made the old "live"
// badge lie when Traktor was connected but sending nothing.
type SourceInfo struct {
	ID           string
	Enabled      bool
	Running      bool
	Receiving    bool
	LastSeen     time.Time
	Capabilities []session.Capability
}

// Sources returns the current state of every registered source.
func (a *Aggregator) Sources() []SourceInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	out := make([]SourceInfo, 0, len(a.srcs))
	for _, e := range a.srcs {
		id := e.src.ID()
		_, running := a.running["src:"+id]
		seen := a.lastSeen[id]
		out = append(out, SourceInfo{
			ID:           id,
			Enabled:      e.enabled(),
			Running:      running,
			Receiving:    running && !seen.IsZero() && now.Sub(seen) < liveTTL,
			LastSeen:     seen,
			Capabilities: e.src.Capabilities(),
		})
	}
	return out
}

// Snapshot returns the current merged state.
func (a *Aggregator) Snapshot() session.UnifiedState { return a.merger.Snapshot() }

// Subscribe streams merged updates (UI live refresh).
func (a *Aggregator) Subscribe() (<-chan session.Update, func()) { return a.merger.Subscribe() }
