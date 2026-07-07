// Package midisrc is the MIDI-controller session source. It reads MIDI input (via
// internal/midi) and runs two decoders: the Denon HC4500 stock-mapping decoder (decks A/B
// track text) and our custom RavePage-State.tsi decoder (per-deck/channel transport + mixer
// state). Each decoder's observations carry their own source ID so the merger ranks them
// independently. Typical setup uses a virtual MIDI port (e.g. loopMIDI) that Traktor's
// Controller Manager sends to - see docs/MIDI_MAPPING.md.
package midisrc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/session"
)

const (
	srcLog      = "midi"
	tickRate    = 100 * time.Millisecond
	summaryRate = 5 * time.Second
)

// activity accumulates a per-port message digest between summary ticks: how many messages,
// broken down by kind, which channels + CC numbers appeared, and how many were System/
// real-time (clock/active-sensing) vs actual data. The digest answers "is Traktor's
// controller-out hitting this port with usable data, or is it just clock noise?".
type activity struct {
	total, system int
	kinds         map[string]int
	chans         map[int]bool
	ccs           map[byte]bool
}

func newActivity() *activity {
	return &activity{kinds: map[string]int{}, chans: map[int]bool{}, ccs: map[byte]bool{}}
}

func (a *activity) add(m midi.Message) {
	a.total++
	a.kinds[m.KindName()]++
	if m.IsSystem() {
		a.system++
		return
	}
	a.chans[m.Channel()+1] = true
	if m.IsCC() {
		a.ccs[m.Controller()] = true
	}
}

// flush renders the digest and resets. ok=false when nothing arrived (stay quiet).
func (a *activity) flush() (string, bool) {
	if a.total == 0 {
		return "", false
	}
	line := fmt.Sprintf("%d msgs, %d data / %d system | %s", a.total, a.total-a.system, a.system, sortedCounts(a.kinds))
	if len(a.chans) > 0 {
		line += " | ch:" + joinInts(keysOf(a.chans))
	}
	if len(a.ccs) > 0 {
		line += " | cc:" + joinBytes(a.ccs)
	}
	if a.total == a.system {
		line += "  ⟵ only clock/keepalive - no usable controller data"
	}
	*a = *newActivity()
	return line, true
}

func sortedCounts(m map[string]int) string {
	type kv struct {
		k string
		v int
	}
	kvs := make([]kv, 0, len(m))
	for k, v := range m {
		kvs = append(kvs, kv{k, v})
	}
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v })
	parts := make([]string, 0, len(kvs))
	for _, e := range kvs {
		parts = append(parts, fmt.Sprintf("%s:%d", e.k, e.v))
	}
	return strings.Join(parts, " ")
}

func keysOf(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%d", x)
	}
	return strings.Join(parts, ",")
}

func joinBytes(m map[byte]bool) string {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, int(k))
	}
	sort.Ints(out)
	return joinInts(out)
}

// decoder consumes raw MIDI and emits observations. tick flushes time-based state.
type decoder interface {
	id() string
	handle(now time.Time, m midi.Message, emit func(session.Observation))
	tick(now time.Time, emit func(session.Observation))
}

// Source opens the configured MIDI input port(s) and runs the decoders.
type Source struct {
	log        *logbus.Bus
	mon        *logbus.Bus          // raw-message monitor (MIDI debugger tab); nil = off
	forward    func(m midi.Message) // tap for the peer-link MIDI bridge; nil = off
	injectCh   chan midi.Message    // peer-bridged MIDI fed into the local decoders
	denonPort  string
	customPort string
}

// New constructs the MIDI source. Empty port name = that decoder is disabled.
func New(log *logbus.Bus, denonPort, customPort string) *Source {
	return &Source{log: log, denonPort: denonPort, customPort: customPort, injectCh: make(chan midi.Message, 64)}
}

// SetMonitor attaches a raw-MIDI monitor bus (the MIDI debugger view subscribes to it).
func (s *Source) SetMonitor(mon *logbus.Bus) { s.mon = mon }

// SetForwarder taps every non-system local MIDI message for the peer-link bridge. Call before
// Start (set-once); nil disables.
func (s *Source) SetForwarder(fn func(m midi.Message)) { s.forward = fn }

// Inject feeds a peer-bridged MIDI message into the local decoders (so a linked instance's
// controller drives this session). Non-blocking - drops if the buffer is full. The message is
// processed by the custom-port pump goroutine, so decoder state stays single-threaded.
func (s *Source) Inject(m midi.Message) {
	select {
	case s.injectCh <- m:
	default:
	}
}

// ID implements session.Source. "midi" is the aggregator liveness key; the per-decoder
// provenance tags (midi.custom/midi.denon) live on the observations, not here.
func (s *Source) ID() string { return session.SourceMIDI }

// Capabilities implements session.Source: Denon gives A/B text; the custom map gives
// per-deck transport + per-channel mixer state.
func (s *Source) Capabilities() []session.Capability {
	var caps []session.Capability
	if s.denonPort != "" {
		caps = append(caps, session.Capability{Scope: session.ScopeDeck, IDs: []string{"A", "B"},
			Fields: []string{session.FieldTitle, session.FieldArtist}})
	}
	if s.customPort != "" {
		caps = append(caps,
			session.Capability{Scope: session.ScopeDeck, IDs: []string{"A", "B", "C", "D"}, Fields: []string{session.FieldIsPlaying}},
			session.Capability{Scope: session.ScopeChannel, IDs: []string{"1", "2", "3", "4"},
				Fields: []string{session.FieldFader, session.FieldEQHigh, session.FieldEQMid, session.FieldEQLow, session.FieldFilter, session.FieldCue}})
	}
	return caps
}

// portBinding is one opened port plus the decoders that consume it.
type portBinding struct {
	name          string
	decoders      []decoder
	acceptsInject bool // this pump drains the peer-bridge inject channel (the custom port)
}

// Start opens the port(s) and pumps messages into the decoders until ctx is cancelled.
func (s *Source) Start(ctx context.Context, emit func(session.Observation)) error {
	// Group decoders by port (Denon + custom may share one virtual port or use two).
	byPort := map[string][]decoder{}
	if s.customPort != "" {
		byPort[s.customPort] = append(byPort[s.customPort], customDecoder{})
	}
	if s.denonPort != "" {
		byPort[s.denonPort] = append(byPort[s.denonPort], newDenonDecoder())
	}
	if len(byPort) == 0 {
		s.log.Warn(srcLog, "no MIDI ports configured; source idle", nil)
		<-ctx.Done()
		return nil
	}

	for name, decs := range byPort {
		in, err := midi.Open(name)
		if err != nil {
			s.log.Warn(srcLog, "open port failed", map[string]any{"port": name, "error": err.Error()})
			continue
		}
		s.log.Info(srcLog, "MIDI port open", map[string]any{"port": in.Name, "decoders": len(decs)})
		binding := portBinding{name: in.Name, decoders: decs, acceptsInject: name == s.customPort}
		input := in
		debuglog.Go(s.log, srcLog, func() { s.pump(ctx, binding, input, emit) })
	}
	<-ctx.Done()
	return nil
}

// handleLocal dispatches a locally-received port message: monitor, peer-forward tap, decoders.
func (s *Source) handleLocal(b portBinding, now time.Time, m midi.Message, emit func(session.Observation)) {
	if s.mon != nil {
		// Every raw message - even ones no decoder maps - so the debugger shows exactly
		// what Traktor emits (the key signal when a mapping is missing/misrouted).
		s.mon.Info(b.name, m.Describe(), map[string]any{
			"status": m.Status, "d1": m.Data1, "d2": m.Data2,
		})
	}
	if s.forward != nil && !m.IsSystem() {
		s.forward(m) // tap for the peer-link MIDI bridge
	}
	for _, d := range b.decoders {
		d.handle(now, m, emit)
	}
}

// handleInjected dispatches a peer-bridged message to the decoders only - NEVER the forward
// tap, so bridged-in MIDI can't echo back onto the peer link (mesh loop safety).
func (s *Source) handleInjected(b portBinding, now time.Time, m midi.Message, emit func(session.Observation)) {
	if s.mon != nil {
		s.mon.Info(b.name, "⇆ peer "+m.Describe(), map[string]any{
			"status": m.Status, "d1": m.Data1, "d2": m.Data2, "peer": true,
		})
	}
	for _, d := range b.decoders {
		d.handle(now, m, emit)
	}
}

// pump reads a port's messages, dispatching to its decoders, and ticks them for time-based
// flushing. The port is closed on ctx cancel.
func (s *Source) pump(ctx context.Context, b portBinding, in *midi.Input, emit func(session.Observation)) {
	defer func() { _ = in.Close() }()
	msgs := in.Messages()
	tick := time.NewTicker(tickRate)
	defer tick.Stop()
	summary := time.NewTicker(summaryRate)
	defer summary.Stop()
	act := newActivity()
	// nil channel never fires → only the custom-port pump drains injected peer MIDI, keeping
	// each decoder set single-threaded.
	injectCh := s.injectCh
	if !b.acceptsInject {
		injectCh = nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-msgs:
			act.add(m)
			s.handleLocal(b, time.Now(), m, emit)
		case m := <-injectCh:
			s.handleInjected(b, time.Now(), m, emit)
		case now := <-tick.C:
			for _, d := range b.decoders {
				d.tick(now, emit)
			}
		case <-summary.C:
			// Periodic activity digest to the MAIN log (so `rave-mate ctl logs` shows whether a
			// port carries usable data or only clock/keepalive - the Denon-out diagnosis).
			if line, ok := act.flush(); ok {
				s.log.Info(srcLog, "MIDI activity", map[string]any{"port": b.name, "detail": line})
			}
		}
	}
}
