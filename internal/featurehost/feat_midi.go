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

func init() { Register("midi", func() Feature { return &midiFeature{} }) }

// midiInit is the init wire config for the midi feature. Controllers + Bridge added at config
// v27 (native MIDI-learn + two-port DJ router); absent = old two-decoder behaviour.
type midiInit struct {
	DenonPort   string               `json:"denonPort"`
	CustomPort  string               `json:"customPort"`
	Controllers []MidiControllerInit `json:"controllers,omitempty"`
	Bridge      MidiBridgeInit       `json:"bridge,omitempty"`
}

// MidiControllerInit is one native-learn controller on the init wire (app.go maps config to it).
type MidiControllerInit struct {
	Name     string            `json:"name"`
	Port     string            `json:"port"`
	ThruPort string            `json:"thruPort,omitempty"`
	Bindings []MidiBindingInit `json:"bindings,omitempty"`
}

// MidiBindingInit is one learned binding on the init wire.
type MidiBindingInit struct {
	Control string `json:"control"`
	Channel int    `json:"channel"`
	Status  byte   `json:"status"`
	Data1   byte   `json:"data1"`
	Invert  bool   `json:"invert,omitempty"`
}

// MidiBridgeInit is the two-port DJ router on the init wire.
type MidiBridgeInit struct {
	Enabled    bool   `json:"enabled"`
	ToDJPort   string `json:"toDjPort,omitempty"`
	FromDJPort string `json:"fromDjPort,omitempty"`
}

// midiMsg is one raw MIDI message on the wire (peer-bridge tap + inject). P = source input
// port (tap only; empty on inject) so the daemon's keybind dispatcher can device-match.
type midiMsg struct {
	S  byte   `json:"s"`
	D1 byte   `json:"d1"`
	D2 byte   `json:"d2"`
	P  string `json:"p,omitempty"`
}

// learnReq/learnRes carry a native MIDI-learn capture across the stdio boundary.
type learnReq struct {
	Port      string `json:"port"`
	TimeoutMs int    `json:"timeoutMs"`
}
type learnRes struct {
	Port   string `json:"port"`
	Status byte   `json:"status"`
	Data1  byte   `json:"data1"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"` // e.g. "port-not-open" (in use by another app / missing)
}

// portsEvent reports which controller INPUT ports opened vs failed (child → daemon), so the UI
// can flag a port that's held exclusively by another app.
type portsEvent struct {
	Open   []string `json:"open"`
	Failed []string `json:"failed"`
}

func toControllerSpecs(in []MidiControllerInit) []midisrc.ControllerSpec {
	out := make([]midisrc.ControllerSpec, 0, len(in))
	for _, c := range in {
		bs := make([]midisrc.LearnedBinding, 0, len(c.Bindings))
		for _, b := range c.Bindings {
			bs = append(bs, midisrc.LearnedBinding{Control: b.Control, Channel: b.Channel, Status: b.Status, Data1: b.Data1, Invert: b.Invert})
		}
		out = append(out, midisrc.ControllerSpec{Name: c.Name, Port: c.Port, ThruPort: c.ThruPort, Bindings: bs})
	}
	return out
}

// midiFeature hosts the winmm MIDI driver + decoders in the child: a callback-thread fault
// kills only the child. The source is rebuilt in-child on "configure" (new learned bindings /
// controllers / bridge) so mappings apply live without respawning the process. Emits "obs",
// "mon" (raw-message monitor), "midi" (peer forward tap); handles "inject" (peer MIDI into the
// decoders + DJ bridge), "learn"/"learn-cancel" (native MIDI-learn capture), "configure".
type midiFeature struct {
	rt  *Runtime
	mon *logbus.Bus

	mu        sync.Mutex
	cfg       midiInit
	src       *midisrc.Source
	srcCancel context.CancelFunc
}

func (f *midiFeature) Init(params json.RawMessage, rt *Runtime) error {
	var p midiInit
	if err := json.Unmarshal(params, &p); err != nil {
		return err
	}
	f.rt = rt
	f.mon = newChildMonitor(rt, "mon")
	f.cfg = p
	return nil
}

// build constructs a source for the current config (called on Start + every reconfigure).
func (f *midiFeature) build(cfg midiInit) *midisrc.Source {
	src := midisrc.New(f.rt.Log, cfg.DenonPort, cfg.CustomPort)
	src.SetMonitor(f.mon)
	src.SetForwarder(func(port string, m midi.Message) {
		f.rt.Emit("midi", midiMsg{S: m.Status, D1: m.Data1, D2: m.Data2, P: port})
	})
	src.SetControllers(toControllerSpecs(cfg.Controllers))
	src.SetBridge(midisrc.BridgeSpec{Enabled: cfg.Bridge.Enabled, ToDJPort: cfg.Bridge.ToDJPort, FromDJPort: cfg.Bridge.FromDJPort})
	src.SetOnPorts(func(open, failed []string) {
		f.rt.Emit("ports", portsEvent{Open: open, Failed: failed})
	})
	return src
}

// Start runs the source, rebuilding it whenever reconfigure cancels the current run. Each run
// blocks on the source (which blocks on its own ctx even with no ports), so the loop only spins
// on an explicit reconfigure or parent-ctx cancel - never a busy loop.
func (f *midiFeature) Start(ctx context.Context) error {
	for ctx.Err() == nil {
		f.mu.Lock()
		src := f.build(f.cfg)
		sctx, cancel := context.WithCancel(ctx)
		f.src = src
		f.srcCancel = cancel
		f.mu.Unlock()

		// Coalesced: CC twiddles (fader/EQ) tick many times/sec; discrete track text passes through.
		co := newObsCoalescer(obsCoalesceInterval, func(o session.Observation) { f.rt.Emit("obs", o) })
		_ = src.Start(sctx, co.Add)
		cancel()
	}
	return nil
}

// reconfigure swaps the live config + cancels the current source run so Start rebuilds it.
func (f *midiFeature) reconfigure(newCfg midiInit) {
	f.mu.Lock()
	f.cfg = newCfg
	c := f.srcCancel
	f.mu.Unlock()
	if c != nil {
		c()
	}
}

func (f *midiFeature) current() *midisrc.Source {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.src
}

func (f *midiFeature) Handle(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "inject":
		var m midiMsg
		if err := json.Unmarshal(params, &m); err != nil {
			return nil, err
		}
		if src := f.current(); src != nil {
			src.Inject(midi.Message{Status: m.S, Data1: m.D1, Data2: m.D2})
		}
		return nil, nil
	case "configure":
		var p midiInit
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		f.reconfigure(p)
		return nil, nil
	case "learn":
		var p learnReq
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		src := f.current()
		if src == nil {
			return json.Marshal(learnRes{})
		}
		if !src.PortOpen(p.Port) {
			// The port never opened (held by another app, or gone) - fail fast so the UI can
			// explain, instead of arming a capture that would silently time out.
			return json.Marshal(learnRes{Reason: "port-not-open"})
		}
		resCh := make(chan learnRes, 1)
		src.ArmLearn(p.Port, func(port string, status, data1 byte) {
			select {
			case resCh <- learnRes{Port: port, Status: status, Data1: data1, OK: true}:
			default:
			}
		})
		to := time.Duration(p.TimeoutMs) * time.Millisecond
		if to <= 0 {
			to = 15 * time.Second
		}
		timer := time.NewTimer(to)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			src.CancelLearn()
			return json.Marshal(learnRes{})
		case <-timer.C:
			src.CancelLearn()
			return json.Marshal(learnRes{})
		case r := <-resCh:
			return json.Marshal(r)
		}
	case "learn-cancel":
		if src := f.current(); src != nil {
			src.CancelLearn()
		}
		return nil, nil
	}
	return nil, errUnknownMethod(method)
}
