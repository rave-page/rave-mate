package featurehost

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/midi"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/session/sources/midisrc"
)

// MidiProxy is the daemon-side stand-in for the subprocessed MIDI source. Its host
// lifecycle rides the session.Source slot: Start spawns the child, ctx cancel reaps it -
// so the existing MIDI.Enabled gate + aggregator Reconcile() drive the subprocess.
type MidiProxy struct {
	host   *Host
	initFn func() MidiConfig
	obs    chan session.Observation

	mu          sync.Mutex
	forward     func(port string, m midi.Message)
	openPorts   []string // last-reported opened INPUT ports (from the child's "ports" event)
	failedPorts []string // last-reported input ports that failed to open (allocated/missing)
	mutedPorts  []string // loopback ports delivering nothing (LoopBe mute - see midisrc.loopbackWatch)
}

// MidiConfig builds the child's init params (re-read live on every restart).
type MidiConfig = midiInit

// NewMidiProxy builds the proxy + its host. mon receives the child's raw-MIDI monitor
// lines (the MIDI debugger bus).
func NewMidiProxy(log, mon *logbus.Bus, initFn func() MidiConfig) (*MidiProxy, error) {
	p := &MidiProxy{initFn: initFn, obs: make(chan session.Observation, 256)}
	h, err := New(Options{
		Name: "midi",
		Log:  log,
		Init: func() any { return initFn() },
		OnEvent: map[string]func(json.RawMessage){
			"obs": func(data json.RawMessage) {
				var o session.Observation
				if json.Unmarshal(data, &o) != nil {
					return
				}
				select {
				case p.obs <- o:
				default:
				}
			},
			"mon": func(data json.RawMessage) {
				var le logEvent
				if json.Unmarshal(data, &le) == nil && mon != nil {
					mon.Log(logbus.Level(le.Level), le.Source, le.Msg, le.Fields)
				}
			},
			"midi": func(data json.RawMessage) {
				var m midiMsg
				if json.Unmarshal(data, &m) != nil {
					return
				}
				p.mu.Lock()
				fn := p.forward
				p.mu.Unlock()
				if fn != nil {
					fn(m.P, midi.Message{Status: m.S, Data1: m.D1, Data2: m.D2})
				}
			},
			"ports": func(data json.RawMessage) {
				var ev portsEvent
				if json.Unmarshal(data, &ev) != nil {
					return
				}
				p.mu.Lock()
				p.openPorts, p.failedPorts, p.mutedPorts = ev.Open, ev.Failed, ev.Muted
				p.mu.Unlock()
			},
		},
	})
	if err != nil {
		return nil, err
	}
	p.host = h
	return p, nil
}

// Host exposes the supervising host (SetNotifier, Stats).
func (p *MidiProxy) Host() *Host { return p.host }

// SetForwarder taps every non-system MIDI message from the child, with its source input port
// name (peer-link bridge + keybind dispatch).
func (p *MidiProxy) SetForwarder(fn func(port string, m midi.Message)) {
	p.mu.Lock()
	p.forward = fn
	p.mu.Unlock()
}

// Inject feeds a peer-bridged MIDI message into the child's decoders (best-effort). When the
// DJ bridge is on the child also writes it out to ToDJPort (a paired instance driving the DJ rig).
func (p *MidiProxy) Inject(m midi.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = p.host.Call(ctx, "inject", midiMsg{S: m.Status, D1: m.Data1, D2: m.Data2})
}

// Reconfigure re-reads the init config (fresh Controllers/Bridge/ports) and pushes it to the
// running child, which rebuilds its source in place - applies a new learned mapping live, no
// respawn. No-op if the child is down (the next spawn reads the fresh config anyway).
func (p *MidiProxy) Reconfigure() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _ = p.host.Call(ctx, "configure", p.initFn())
}

// ArmLearn arms a one-shot native MIDI-learn capture on port (substring; "" = any open port).
// cb fires with the captured status+data1 (ok=true), or ok=false with a reason ("port-not-open"
// = held by another app / gone; "" = timeout/error). Async - the child blocks until an
// active-edge message arrives, the port is found closed, or timeout elapses.
func (p *MidiProxy) ArmLearn(port string, timeout time.Duration, cb func(port string, status, data1 byte, ok bool, reason string)) {
	ms := int(timeout / time.Millisecond)
	if ms <= 0 {
		ms = 15000
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(ms)*time.Millisecond+3*time.Second)
		defer cancel()
		raw, err := p.host.Call(ctx, "learn", learnReq{Port: port, TimeoutMs: ms})
		if err != nil {
			cb("", 0, 0, false, "")
			return
		}
		var r learnRes
		if json.Unmarshal(raw, &r) != nil {
			cb("", 0, 0, false, "")
			return
		}
		cb(r.Port, r.Status, r.Data1, r.OK, r.Reason)
	}()
}

// OpenInputPorts returns the controller INPUT ports the child currently has open.
func (p *MidiProxy) OpenInputPorts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.openPorts...)
}

// FailedInputPorts returns the controller INPUT ports that failed to open (held by another app,
// or missing). These are the ports to warn the user about.
func (p *MidiProxy) FailedInputPorts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.failedPorts...)
}

// MutedInputPorts returns open loopback ports that deliver nothing - even the child's own
// probe never echoes back (LoopBe1 anti-feedback mute). EQ/filter are silently dead on these.
func (p *MidiProxy) MutedInputPorts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.mutedPorts...)
}

// CancelLearn disarms a pending capture in the child.
func (p *MidiProxy) CancelLearn() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = p.host.Call(ctx, "learn-cancel", nil)
}

// InputPorts lists available MIDI input ports (for the learn UI). Enumeration is a cheap winmm
// call (no callback/streaming), so it runs in-daemon like the emitter's OutPorts.
func (p *MidiProxy) InputPorts() []string {
	ports, err := midi.Ports()
	if err != nil {
		return nil
	}
	return ports
}

// ── session.Source ───────────────────────────────────────────────────────────

// ID implements session.Source.
func (p *MidiProxy) ID() string { return session.SourceMIDI }

// Capabilities implements session.Source (same port-derived logic as the in-proc source),
// including any learned controllers so the aggregator advertises their deck/channel fields.
func (p *MidiProxy) Capabilities() []session.Capability {
	c := p.initFn()
	src := midisrc.New(nil, c.DenonPort, c.CustomPort)
	src.SetControllers(toControllerSpecs(c.Controllers))
	return src.Capabilities()
}

// Start implements session.Source: spawns the child for the duration of the source slot
// (the aggregator's enable-gate + Reconcile() start/stop it), pumping observations.
func (p *MidiProxy) Start(ctx context.Context, emit func(session.Observation)) error {
	if err := p.host.Start(ctx); err != nil {
		return err
	}
	defer p.host.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case o := <-p.obs:
			emit(o)
		}
	}
}
