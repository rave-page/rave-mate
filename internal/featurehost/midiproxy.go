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

	mu      sync.Mutex
	forward func(m midi.Message)
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
					fn(midi.Message{Status: m.S, Data1: m.D1, Data2: m.D2})
				}
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

// SetForwarder taps every non-system MIDI message from the child (peer-link bridge).
func (p *MidiProxy) SetForwarder(fn func(m midi.Message)) {
	p.mu.Lock()
	p.forward = fn
	p.mu.Unlock()
}

// Inject feeds a peer-bridged MIDI message into the child's decoders (best-effort).
func (p *MidiProxy) Inject(m midi.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = p.host.Call(ctx, "inject", midiMsg{S: m.Status, D1: m.Data1, D2: m.Data2})
}

// ── session.Source ───────────────────────────────────────────────────────────

// ID implements session.Source.
func (p *MidiProxy) ID() string { return session.SourceMIDI }

// Capabilities implements session.Source (same port-derived logic as the in-proc source).
func (p *MidiProxy) Capabilities() []session.Capability {
	c := p.initFn()
	return midisrc.New(nil, c.DenonPort, c.CustomPort).Capabilities()
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
