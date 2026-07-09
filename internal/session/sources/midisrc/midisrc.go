// Package midisrc is the MIDI-controller session source. It reads MIDI input (via
// internal/midi) and runs decoders: the Denon HC4500 stock-mapping decoder (decks A/B track
// text), our custom RavePage-State.tsi decoder (per-deck/channel transport + mixer state), and
// N learned decoders (native MIDI-learn: one per physical controller, each with its own port +
// learned bindings, all feeding the shared deck/channel model). Optional per-controller THRU
// re-emits raw input to a MIDI-OUT (a loopMIDI cable the DJ app reads) so rave-mate can read a
// controller AND the DJ app still gets it on single-client Windows MIDI. An optional two-port
// bridge routes peer control out to the DJ app and reads the DJ app's own output back in.
// See docs/MIDI_MAPPING.md.
package midisrc

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
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

// ControllerSpec is one native-learn controller: an input port, its learned bindings, and an
// optional THRU MIDI-OUT (loopMIDI cable the DJ app reads).
type ControllerSpec struct {
	Name     string
	Port     string
	ThruPort string
	Bindings []LearnedBinding
}

// BridgeSpec is the two-port loopMIDI DJ router. ToDJPort = MIDI-OUT the DJ app reads (peer
// control lands here); FromDJPort = MIDI-IN the DJ app writes (its own indicator/VU output).
type BridgeSpec struct {
	Enabled    bool
	ToDJPort   string
	FromDJPort string
}

// Source opens the configured MIDI input port(s) and runs the decoders.
type Source struct {
	log         *logbus.Bus
	mon         *logbus.Bus          // raw-message monitor (MIDI debugger tab); nil = off
	forward     func(m midi.Message) // tap for the peer-link MIDI bridge; nil = off
	injectCh    chan midi.Message    // peer-bridged MIDI fed into the local decoders + DJ bridge
	denonPort   string
	customPort  string
	controllers []ControllerSpec
	bridge      BridgeSpec

	// Opened in Start (single goroutine, before pumps launch), read by pumps/injectPump.
	outs      map[string]*midi.Output
	bridgeOut *midi.Output

	// Native MIDI-learn one-shot capture. Armed by ArmLearn; the next active-edge message on
	// the target port (or any, if learnPort=="") fires the callback and disarms.
	learnMu   sync.Mutex
	learnPort string
	learnCB   func(port string, status, data1 byte)

	// Port-open outcome of the last Start build (so the UI can show which controller ports
	// actually opened vs failed - e.g. a hardware input already held by another app).
	portMu   sync.Mutex
	openIn   []string // successfully-opened INPUT port display names
	failedIn []string // input port names that failed to open (allocated/missing)
	onPorts  func(open, failed []string)
}

// New constructs the MIDI source. Empty port name = that decoder is disabled. Learned
// controllers + the DJ bridge are added via SetControllers / SetBridge before Start.
func New(log *logbus.Bus, denonPort, customPort string) *Source {
	return &Source{log: log, denonPort: denonPort, customPort: customPort, injectCh: make(chan midi.Message, 64)}
}

// SetMonitor attaches a raw-MIDI monitor bus (the MIDI debugger view subscribes to it).
func (s *Source) SetMonitor(mon *logbus.Bus) { s.mon = mon }

// SetForwarder taps every non-system local MIDI message for the peer-link bridge. Call before
// Start (set-once); nil disables.
func (s *Source) SetForwarder(fn func(m midi.Message)) { s.forward = fn }

// SetControllers sets the native-learn controllers. Call before Start.
func (s *Source) SetControllers(cs []ControllerSpec) { s.controllers = cs }

// SetBridge sets the two-port DJ router. Call before Start.
func (s *Source) SetBridge(b BridgeSpec) { s.bridge = b }

// SetOnPorts registers a callback fired after each Start build with the opened + failed INPUT
// port names, so the host can report which controllers are actually being read.
func (s *Source) SetOnPorts(fn func(open, failed []string)) { s.onPorts = fn }

// OpenInputPorts returns the INPUT ports opened by the last build.
func (s *Source) OpenInputPorts() []string {
	s.portMu.Lock()
	defer s.portMu.Unlock()
	return append([]string(nil), s.openIn...)
}

// FailedInputPorts returns the INPUT ports that failed to open (allocated by another app / gone).
func (s *Source) FailedInputPorts() []string {
	s.portMu.Lock()
	defer s.portMu.Unlock()
	return append([]string(nil), s.failedIn...)
}

// PortOpen reports whether an opened input port name contains substr (case-insensitive).
func (s *Source) PortOpen(substr string) bool {
	if substr == "" {
		s.portMu.Lock()
		defer s.portMu.Unlock()
		return len(s.openIn) > 0
	}
	want := strings.ToLower(substr)
	s.portMu.Lock()
	defer s.portMu.Unlock()
	for _, p := range s.openIn {
		if strings.Contains(strings.ToLower(p), want) {
			return true
		}
	}
	return false
}

// ArmLearn arms a one-shot capture: the next active-edge message (Note-On, or CC with a
// nonzero value) on port (substring-matched against the opened port name; "" = any port) fires
// cb with the learned status+data1, then disarms. Re-arming replaces a pending capture.
func (s *Source) ArmLearn(port string, cb func(port string, status, data1 byte)) {
	s.learnMu.Lock()
	s.learnPort = port
	s.learnCB = cb
	s.learnMu.Unlock()
}

// CancelLearn disarms a pending capture.
func (s *Source) CancelLearn() {
	s.learnMu.Lock()
	s.learnCB = nil
	s.learnMu.Unlock()
}

// takeLearn returns + disarms the capture callback if this message is a learnable active edge
// on the armed port, else nil. Kept short so the pump stays hot.
func (s *Source) takeLearn(m midi.Message, port string) func(string, byte, byte) {
	if m.IsSystem() {
		return nil
	}
	if !(m.IsNoteOn() || (m.IsCC() && m.Value() > 0)) {
		return nil // capture the press/turn edge, not release/zero
	}
	s.learnMu.Lock()
	defer s.learnMu.Unlock()
	if s.learnCB == nil {
		return nil
	}
	if s.learnPort != "" && !strings.Contains(strings.ToLower(port), strings.ToLower(s.learnPort)) {
		return nil
	}
	cb := s.learnCB
	s.learnCB = nil
	return cb
}

// Inject feeds a peer-bridged MIDI message into the local decoders (so a linked instance's
// controller drives this session) and, when the DJ bridge is on, out to the DJ app. Non-blocking
// - drops if the buffer is full. Processed by the inject pump, so decoder state stays isolated.
func (s *Source) Inject(m midi.Message) {
	select {
	case s.injectCh <- m:
	default:
	}
}

// ID implements session.Source. "midi" is the aggregator liveness key; the per-decoder
// provenance tags (midi.custom/midi.denon) live on the observations, not here.
func (s *Source) ID() string { return session.SourceMIDI }

// Capabilities implements session.Source: Denon gives A/B text; the custom map + any learned
// controllers give per-deck transport + per-channel mixer state.
func (s *Source) Capabilities() []session.Capability {
	var caps []session.Capability
	if s.denonPort != "" {
		caps = append(caps, session.Capability{Scope: session.ScopeDeck, IDs: []string{"A", "B"},
			Fields: []string{session.FieldTitle, session.FieldArtist}})
	}
	if s.customPort != "" || len(s.controllers) > 0 {
		caps = append(caps,
			session.Capability{Scope: session.ScopeDeck, IDs: []string{"A", "B", "C", "D"}, Fields: []string{session.FieldIsPlaying}},
			session.Capability{Scope: session.ScopeChannel, IDs: []string{"1", "2", "3", "4"},
				Fields: []string{session.FieldFader, session.FieldEQHigh, session.FieldEQMid, session.FieldEQLow, session.FieldFilter, session.FieldTrim, session.FieldCue}})
	}
	return caps
}

// portBinding is one opened port plus the decoders that consume it and an optional THRU output.
type portBinding struct {
	name     string
	decoders []decoder
	thruOut  *midi.Output // forward raw input here (THRU: controller → DJ app); nil = off
}

// Start opens the port(s) + output(s) and pumps messages into the decoders until ctx is
// cancelled.
func (s *Source) Start(ctx context.Context, emit func(session.Observation)) error {
	s.outs = map[string]*midi.Output{}
	openOut := func(name string) *midi.Output {
		if name == "" {
			return nil
		}
		if o, ok := s.outs[name]; ok {
			return o
		}
		o, err := midi.OpenOutput(name)
		if err != nil {
			s.log.Warn(srcLog, "open MIDI-out failed", map[string]any{"port": name, "error": err.Error()})
			return nil
		}
		s.outs[name] = o
		s.log.Info(srcLog, "MIDI-out open", map[string]any{"port": o.Name})
		return o
	}

	// One binding per input port; stack decoders that share a port. injectDecs = the stateless
	// decoders the inject pump feeds (custom + learned; denon is stateful/text-only, excluded).
	order := []string{}
	bindings := map[string]*portBinding{}
	var injectDecs []decoder
	get := func(port string) *portBinding {
		b := bindings[port]
		if b == nil {
			b = &portBinding{name: port}
			bindings[port] = b
			order = append(order, port)
		}
		return b
	}
	if s.customPort != "" {
		cd := customDecoder{}
		b := get(s.customPort)
		b.decoders = append(b.decoders, cd)
		injectDecs = append(injectDecs, cd)
	}
	if s.denonPort != "" {
		get(s.denonPort).decoders = append(get(s.denonPort).decoders, newDenonDecoder())
	}
	for _, c := range s.controllers {
		if c.Port == "" {
			continue
		}
		ld := &learnedDecoder{name: c.Name, bindings: c.Bindings}
		b := get(c.Port)
		b.decoders = append(b.decoders, ld)
		if c.ThruPort != "" {
			b.thruOut = openOut(c.ThruPort)
		}
		injectDecs = append(injectDecs, ld)
	}
	if s.bridge.Enabled {
		s.bridgeOut = openOut(s.bridge.ToDJPort)
		if s.bridge.FromDJPort != "" {
			b := get(s.bridge.FromDJPort)
			b.decoders = append(b.decoders, customDecoder{})
		}
	}

	if len(bindings) == 0 && !s.bridge.Enabled {
		s.log.Warn(srcLog, "no MIDI ports configured; source idle", nil)
		<-ctx.Done()
		return nil
	}

	// Inject pump: peer MIDI → drive the DJ (bridge out) + mirror into the stateless decoders.
	var wg sync.WaitGroup
	wg.Add(1)
	debuglog.Go(s.log, srcLog, func() { defer wg.Done(); s.injectPump(ctx, injectDecs, emit) })

	var opened, failed []string
	for _, name := range order {
		b := bindings[name]
		in, err := midi.Open(name)
		if err != nil {
			// mmresult=7 (MMSYSERR_ALLOCATED) = another app already holds this input (winmm
			// MIDI-IN is single-client). Record it so the UI can say so instead of failing silent.
			s.log.Warn(srcLog, "open port failed", map[string]any{"port": name, "error": err.Error()})
			failed = append(failed, name)
			continue
		}
		s.log.Info(srcLog, "MIDI port open", map[string]any{"port": in.Name, "decoders": len(b.decoders), "thru": b.thruOut != nil})
		opened = append(opened, in.Name)
		binding := *b
		binding.name = in.Name
		input := in
		wg.Add(1)
		debuglog.Go(s.log, srcLog, func() { defer wg.Done(); s.pump(ctx, binding, input, emit) })
	}
	s.portMu.Lock()
	s.openIn, s.failedIn = opened, failed
	s.portMu.Unlock()
	if s.onPorts != nil {
		s.onPorts(opened, failed)
	}

	<-ctx.Done()
	// Wait for pumps to close their inputs BEFORE returning, so a reconfigure rebuild doesn't
	// race the old pump for an exclusive hardware port (would spuriously fail to reopen).
	wg.Wait()
	for _, o := range s.outs {
		o.Close()
	}
	return nil
}

// handleLocal dispatches a locally-received port message: THRU, monitor, learn-capture,
// peer-forward tap, decoders.
func (s *Source) handleLocal(b portBinding, now time.Time, m midi.Message, emit func(session.Observation)) {
	if b.thruOut != nil {
		b.thruOut.Send(m.Status, m.Data1, m.Data2) // THRU: controller → DJ app (built-in split)
	}
	if s.mon != nil {
		// Every raw message - even ones no decoder maps - so the debugger shows exactly
		// what the controller emits (the key signal when a mapping is missing/misrouted).
		s.mon.Info(b.name, m.Describe(), map[string]any{
			"status": m.Status, "d1": m.Data1, "d2": m.Data2,
		})
	}
	if cb := s.takeLearn(m, b.name); cb != nil {
		cb(b.name, m.Status, m.Data1)
	}
	if s.forward != nil && !m.IsSystem() {
		s.forward(m) // tap for the peer-link MIDI bridge
	}
	for _, d := range b.decoders {
		d.handle(now, m, emit)
	}
}

// injectPump drains peer-bridged MIDI: writes it to the DJ bridge out (so a paired instance can
// drive this DJ rig) and mirrors it into the stateless decoders. NEVER touches the forward tap
// (bridged-in MIDI must not echo back onto the peer link - mesh-loop safety).
func (s *Source) injectPump(ctx context.Context, decs []decoder, emit func(session.Observation)) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-s.injectCh:
			s.applyInjected(decs, m, emit)
		}
	}
}

// applyInjected handles one peer-bridged message: DJ bridge out + decoder mirror. It NEVER
// calls the forward tap (s.forward) - that omission is the structural mesh-loop guarantee.
func (s *Source) applyInjected(decs []decoder, m midi.Message, emit func(session.Observation)) {
	if s.bridge.Enabled && s.bridgeOut != nil {
		s.bridgeOut.Send(m.Status, m.Data1, m.Data2)
	}
	if s.mon != nil {
		s.mon.Info("peer", "⇆ peer "+m.Describe(), map[string]any{
			"status": m.Status, "d1": m.Data1, "d2": m.Data2, "peer": true,
		})
	}
	now := time.Now()
	for _, d := range decs {
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
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-msgs:
			act.add(m)
			s.handleLocal(b, time.Now(), m, emit)
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
