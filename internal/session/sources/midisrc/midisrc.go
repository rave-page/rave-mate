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
	// loopbackProbeEvery paces the loopback health check; probes fire only in silent windows.
	loopbackProbeEvery = 15 * time.Second

	// Failed-port retry pacing. Endless midiInOpen retries against a port another client
	// holds (MMSYSERR_ALLOCATED) keep re-entering wdmaud/audiosrv - the kernel contention
	// pattern implicated in a host bugcheck. Backoff 4s→60s cap, hard ceiling after
	// retryMaxAttempts cycles; past the ceiling only a device-list change (cheap user-mode
	// enum, no opens) or a source restart re-arms attempts.
	retryBaseDelay   = 4 * time.Second
	retryMaxDelay    = 60 * time.Second
	retryMaxAttempts = 8
)

// retryWatchEvery paces the post-ceiling device-list watch (var: shrunk in tests).
var retryWatchEvery = 30 * time.Second

// listInputPorts is the device-list probe used by the post-ceiling watch (seam for tests).
var listInputPorts = func() []string {
	ps, _ := midi.Ports()
	return ps
}

// retrySchedule is the pure backoff/ceiling state machine for failed-port reopen attempts.
type retrySchedule struct{ attempts int }

// next returns the delay before the next attempt; ok=false once the ceiling is reached.
func (r *retrySchedule) next() (time.Duration, bool) {
	if r.attempts >= retryMaxAttempts {
		return 0, false
	}
	d := retryBaseDelay << r.attempts
	if d > retryMaxDelay || d <= 0 { // <=0 guards shift overflow
		d = retryMaxDelay
	}
	r.attempts++
	return d, true
}

// reset re-arms the schedule (device list changed / a port recovered).
func (r *retrySchedule) reset() { r.attempts = 0 }

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
	mon         *logbus.Bus                       // raw-message monitor (MIDI debugger tab); nil = off
	forward     func(port string, m midi.Message) // tap: peer-link MIDI bridge + keybind dispatch; nil = off
	injectCh    chan midi.Message                 // peer-bridged MIDI fed into the local decoders + DJ bridge
	denonPort   string
	customPort  string
	controllers []ControllerSpec
	bridge      BridgeSpec

	// Opened in Start (single goroutine, before pumps launch), read by pumps/injectPump.
	outs      map[string]midi.OutPort
	bridgeOut midi.OutPort

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
	mutedIn  []string // software-loopback ports that deliver NOTHING (see loopbackWatch)
	onPorts  func(open, failed, muted []string)

	// Loopback health: per-port received-message counters, read by loopbackWatch.
	rxMu sync.Mutex
	rxN  map[string]uint64
}

// New constructs the MIDI source. Empty port name = that decoder is disabled. Learned
// controllers + the DJ bridge are added via SetControllers / SetBridge before Start.
func New(log *logbus.Bus, denonPort, customPort string) *Source {
	return &Source{log: log, denonPort: denonPort, customPort: customPort,
		injectCh: make(chan midi.Message, 64), rxN: map[string]uint64{}}
}

// SetMonitor attaches a raw-MIDI monitor bus (the MIDI debugger view subscribes to it).
func (s *Source) SetMonitor(mon *logbus.Bus) { s.mon = mon }

// SetForwarder taps every non-system local MIDI message (with its source port name) for the
// peer-link bridge + the keybind dispatcher. Call before Start (set-once); nil disables.
func (s *Source) SetForwarder(fn func(port string, m midi.Message)) { s.forward = fn }

// SetControllers sets the native-learn controllers. Call before Start.
func (s *Source) SetControllers(cs []ControllerSpec) { s.controllers = cs }

// SetBridge sets the two-port DJ router. Call before Start.
func (s *Source) SetBridge(b BridgeSpec) { s.bridge = b }

// SetOnPorts registers a callback fired after each Start build (and on loopback-health flips)
// with the opened + failed + muted INPUT port names, so the host can report which controllers
// are actually being read.
func (s *Source) SetOnPorts(fn func(open, failed, muted []string)) { s.onPorts = fn }

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

// MutedInputPorts returns open software-loopback ports that currently deliver NOTHING - even
// our own probe injected into their out side never arrives (classic: LoopBe1 anti-feedback
// mute). The port opens fine, so only the loopbackWatch echo test catches this state.
func (s *Source) MutedInputPorts() []string {
	s.portMu.Lock()
	defer s.portMu.Unlock()
	return append([]string(nil), s.mutedIn...)
}

// rxCount returns the number of messages received on port since Start.
func (s *Source) rxCount(port string) uint64 {
	s.rxMu.Lock()
	defer s.rxMu.Unlock()
	return s.rxN[port]
}

// setMuted flips a port's muted state + refires onPorts so hosts mirror it live.
func (s *Source) setMuted(port string, muted bool) {
	s.portMu.Lock()
	kept := s.mutedIn[:0]
	for _, p := range s.mutedIn {
		if p != port {
			kept = append(kept, p)
		}
	}
	s.mutedIn = kept
	if muted {
		s.mutedIn = append(s.mutedIn, port)
	}
	open := append([]string(nil), s.openIn...)
	failed := append([]string(nil), s.failedIn...)
	mutedL := append([]string(nil), s.mutedIn...)
	s.portMu.Unlock()
	if s.onPorts != nil {
		s.onPorts(open, failed, mutedL)
	}
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
	thruOut  midi.OutPort // forward raw input here (THRU: controller → DJ app); nil = off
}

// Start opens the port(s) + output(s) and pumps messages into the decoders until ctx is
// cancelled.
func (s *Source) Start(ctx context.Context, emit func(session.Observation)) error {
	s.outs = map[string]midi.OutPort{}
	openOut := func(name string) midi.OutPort {
		if name == "" {
			return nil
		}
		if o, ok := s.outs[name]; ok {
			return o
		}
		// Built-in one-way virtual port (ravemidi driver, else teVirtualMIDI): DJ apps see
		// an INPUT-only "rave-mate" port with no output endpoint, so their automatic LED
		// echo can't loop back (rekordbox mirrors every indicator's MIDI IN code to OUT).
		if name == midi.VirtualDJSentinel {
			v, err := midi.OpenOneWayOut(midi.VirtualDJPortName)
			if err != nil {
				s.log.Warn(srcLog, "one-way virtual port failed", map[string]any{"error": err.Error()})
				return nil
			}
			s.outs[name] = v
			s.log.Info(srcLog, "MIDI-out open (one-way virtual)", map[string]any{"port": v.PortName()})
			return v
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
		if b.thruOut != nil {
			to := b.thruOut
			input.SetThru(func(m midi.Message) { to.Send(m.Status, m.Data1, m.Data2) })
		}
		wg.Add(1)
		debuglog.Go(s.log, srcLog, func() { defer wg.Done(); s.pump(ctx, binding, input, emit) })
	}
	s.portMu.Lock()
	s.openIn, s.failedIn, s.mutedIn = opened, failed, nil
	s.portMu.Unlock()
	if s.onPorts != nil {
		s.onPorts(opened, failed, nil)
	}
	for _, name := range opened {
		s.watchIfLoopback(ctx, name, &wg)
	}

	// Auto-retry ports that were held by another app (winmm single-client): when that app
	// releases the port (e.g. OBS/Rekordbox closes its MIDI mapping), we pick it up without a
	// manual toggle. Only INPUT ports; reuses the already-opened THRU output on the binding.
	if len(failed) > 0 {
		wg.Add(1)
		debuglog.Go(s.log, srcLog, func() { defer wg.Done(); s.retryFailed(ctx, bindings, failed, emit, &wg) })
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

// retryFailed re-attempts each still-failed input port until ctx ends, backoff-paced with a
// hard ceiling (see retrySchedule): NOT a fixed-rate forever loop, so a port held by another
// app (Serato et al) stops being hammered through the kernel MIDI stack. Past the ceiling it
// logs ONCE and re-arms only when the winmm device list changes or the source restarts. On
// success it launches the port's pump and flips its status to open (fires onPorts so the UI
// can show "reading"). Its own wg count is held for the goroutine's life, so pumps it adds are
// always awaited by Start's wg.Wait().
func (s *Source) retryFailed(ctx context.Context, bindings map[string]*portBinding, failed []string, emit func(session.Observation), wg *sync.WaitGroup) {
	pending := make(map[string]bool, len(failed))
	for _, f := range failed {
		pending[f] = true
	}
	var sched retrySchedule
	for {
		delay, ok := sched.next()
		if !ok {
			// Ceiling: one loud line, then a cheap device-list watch (enum only, no opens).
			names := make([]string, 0, len(pending))
			for n := range pending {
				names = append(names, n)
			}
			sort.Strings(names)
			s.log.Warn(srcLog, "MIDI port retry ceiling reached - pausing reopen attempts", map[string]any{
				"ports": strings.Join(names, ", "), "attempts": retryMaxAttempts,
				"hint": "attempts resume when the device list changes or MIDI is re-enabled"})
			if !s.waitDeviceChange(ctx) {
				return
			}
			sched.reset()
			s.log.Info(srcLog, "MIDI device list changed - resuming port reopen attempts", nil)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		for name := range pending {
			b := bindings[name]
			if b == nil {
				delete(pending, name)
				continue
			}
			in, err := midi.Open(name)
			if err != nil {
				continue // still held / absent - next backoff cycle
			}
			sched.reset() // contention easing - re-arm full attempts for remaining ports
			s.log.Info(srcLog, "MIDI port recovered", map[string]any{"port": in.Name, "thru": b.thruOut != nil})
			delete(pending, name)
			binding := *b
			binding.name = in.Name
			input := in
			if b.thruOut != nil {
				to := b.thruOut
				input.SetThru(func(m midi.Message) { to.Send(m.Status, m.Data1, m.Data2) })
			}
			wg.Add(1)
			debuglog.Go(s.log, srcLog, func() { defer wg.Done(); s.pump(ctx, binding, input, emit) })
			s.portMu.Lock()
			s.openIn = append(s.openIn, in.Name)
			kept := s.failedIn[:0]
			for _, f := range s.failedIn {
				if f != name {
					kept = append(kept, f)
				}
			}
			s.failedIn = kept
			open := append([]string(nil), s.openIn...)
			fl := append([]string(nil), s.failedIn...)
			muted := append([]string(nil), s.mutedIn...)
			s.portMu.Unlock()
			if s.onPorts != nil {
				s.onPorts(open, fl, muted)
			}
			s.watchIfLoopback(ctx, in.Name, wg)
		}
		if len(pending) == 0 {
			return
		}
	}
}

// waitDeviceChange blocks until the winmm input device list changes (name-set signature via
// a slow enum poll - no device opens) or ctx ends; reports false on ctx end.
func (s *Source) waitDeviceChange(ctx context.Context) bool {
	base := strings.Join(listInputPorts(), "\x00")
	t := time.NewTicker(retryWatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			if strings.Join(listInputPorts(), "\x00") != base {
				return true
			}
		}
	}
}

// isLoopbackPort reports whether a port name is a software MIDI loopback (probe-safe: no
// hardware behind it to confuse with our health probe).
func isLoopbackPort(name string) bool {
	n := strings.ToLower(name)
	return strings.Contains(n, "loopbe") || strings.Contains(n, "loopmidi")
}

// watchIfLoopback launches the loopback health watcher for a just-opened input port when it
// is a software loopback with a same-name out side.
func (s *Source) watchIfLoopback(ctx context.Context, name string, wg *sync.WaitGroup) {
	if !isLoopbackPort(name) {
		return
	}
	wg.Add(1)
	debuglog.Go(s.log, srcLog, func() { defer wg.Done(); s.loopbackWatch(ctx, name) })
}

// loopbackWatch health-checks a software loopback input (LoopBe/loopMIDI): when the port has
// been silent for a full interval, inject Active Sensing (0xFE - system-realtime, so it is
// invisible to mappings, decoders, learn and the peer tap) into the loopback's OUT side; a
// healthy loopback echoes it straight back to our input. No echo = the loopback delivers
// NOTHING - classic LoopBe1 anti-feedback mute - the state that silently kills EQ/filter
// mid-set while every port-open check stays green (a muted port still opens fine). Probes
// fire only while silent, so a live set with moving knobs is never touched.
func (s *Source) loopbackWatch(ctx context.Context, port string) {
	out, err := midi.OpenOutput(port)
	if err != nil {
		return // no same-name out side - nothing to probe with
	}
	defer out.Close()
	t := time.NewTicker(loopbackProbeEvery)
	defer t.Stop()
	last := s.rxCount(port)
	muted := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n := s.rxCount(port); n != last {
				last = n // real traffic flowed - healthy without probing
				if muted {
					muted = false
					s.setMuted(port, false)
					s.log.Info(srcLog, "loopback delivering again", map[string]any{"port": port})
				}
				continue
			}
			out.Send(0xFE, 0, 0) // Active Sensing probe
			deadline := time.Now().Add(2 * time.Second)
			echoed := false
			for time.Now().Before(deadline) {
				if s.rxCount(port) != last {
					echoed = true
					break
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(100 * time.Millisecond):
				}
			}
			last = s.rxCount(port)
			if echoed != muted {
				continue // state unchanged (echoed+not-muted, or silent+already-muted)
			}
			muted = !echoed
			s.setMuted(port, muted)
			if muted {
				s.log.Warn(srcLog, "loopback port delivers nothing (muted?)", map[string]any{
					"port": port, "hint": "LoopBe1 auto-mutes on feedback: systray icon → untick Mute"})
			} else {
				s.log.Info(srcLog, "loopback delivering again", map[string]any{"port": port})
			}
		}
	}
}

// handleLocal dispatches a locally-received port message: monitor, learn-capture, peer-forward
// tap, decoders. THRU is NOT here - it's re-emitted in the winmm input callback (Input.SetThru)
// before this runs, so the DJ app sees the control at the lowest possible latency.
func (s *Source) handleLocal(b portBinding, now time.Time, m midi.Message, emit func(session.Observation)) {
	s.rxMu.Lock()
	s.rxN[b.name]++ // loopback-health heartbeat (loopbackWatch reads it)
	s.rxMu.Unlock()
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
		s.forward(b.name, m) // tap for the peer-link MIDI bridge + keybind dispatch
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
	var gate logbus.Gate // an idle clock/keepalive device repeats the same digest forever
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
			// Gated: re-log only when the digest CHANGES or every 5 min.
			if line, ok := act.flush(); ok {
				if n, emitNow := gate.Should(line, 5*time.Minute); emitNow {
					f := map[string]any{"port": b.name, "detail": line}
					if n > 0 {
						f["repeats"] = n
					}
					s.log.Info(srcLog, "MIDI activity", f)
				}
			}
		}
	}
}
