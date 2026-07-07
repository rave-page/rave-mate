package featurehost

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"rave.page/mate/internal/abletonlink"
)

func init() { Register("abletonlink", func() Feature { return &abletonLinkFeature{} }) }

// abletonLinkInit configures the Link child (re-read on every respawn).
type abletonLinkInit struct {
	Enabled       bool `json:"enabled"`
	Quantum       int  `json:"quantum"`       // phrase beats (8/16/32); 0 = 16
	StartStopSync bool `json:"startStopSync"` // share transport with peers
}

// linkBridge is the daemon→child tempo/phase bridge frame ("bridge" event): the fused DJ
// master state. Applied only when Drive is set (this node owns the Link tempo). Phase is the
// master's fractional beat position (0..1); HasPhase gates the phase realign.
type linkBridge struct {
	Drive      bool    `json:"drive"`
	Tempo      float64 `json:"tempo"` // master BPM (>0 to apply)
	Phase      float64 `json:"phase"` // fractional beat, 0..1
	HasPhase   bool    `json:"hasPhase"`
	Playing    bool    `json:"playing"`
	HasPlaying bool    `json:"hasPlaying"`
}

// abletonLinkFeature hosts the real Link session in the child. The cgo backend
// (abletonlink.NewLink) only exists in `-tags abletonlink` builds; otherwise NewLink returns
// ErrUnavailable and the child runs with a Stub (reports unavailable, everything inert) so the
// daemon + UI stay functional. Samples state ~10 Hz → "state" events; applies the daemon's
// tempo/phase bridge frames when it owns the tempo.
type abletonLinkFeature struct {
	rt   *Runtime
	cfg  abletonLinkInit
	sess abletonlink.Session
}

func (f *abletonLinkFeature) Init(params json.RawMessage, rt *Runtime) error {
	if err := json.Unmarshal(params, &f.cfg); err != nil {
		return err
	}
	f.rt = rt
	q := float64(f.cfg.Quantum)
	sess, err := abletonlink.NewLink(q)
	if err != nil {
		if !errors.Is(err, abletonlink.ErrUnavailable) {
			return err
		}
		rt.Log.Info("abletonlink", "Link backend unavailable (build without -tags abletonlink) - feature inert", nil)
		f.sess = abletonlink.NewStub()
		return nil
	}
	f.sess = sess
	f.sess.SetQuantum(q)
	f.sess.SetStartStopSyncEnabled(f.cfg.StartStopSync)
	if f.cfg.Enabled {
		f.sess.SetEnabled(true)
	}
	rt.Log.Info("abletonlink", "Link backend ready", map[string]any{"quantum": f.cfg.Quantum, "enabled": f.cfg.Enabled})
	return nil
}

// Start samples the session ~10 Hz, emitting "state" on change (or at a 1 Hz keepalive so the
// UI phrase bar stays live even at a steady phase). Exits on ctx cancel.
func (f *abletonLinkFeature) Start(ctx context.Context) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	var last abletonlink.State
	var lastEmit time.Time
	for {
		select {
		case <-ctx.Done():
			if f.sess != nil {
				_ = f.sess.Close()
			}
			return nil
		case now := <-tick.C:
			f.rt.Beat()
			st := f.sess.State(now)
			if stateChanged(last, st) || now.Sub(lastEmit) >= time.Second {
				f.rt.Emit("state", st)
				last, lastEmit = st, now
			}
		}
	}
}

// stateChanged reports whether the UI-relevant fields moved enough to re-emit.
func stateChanged(a, b abletonlink.State) bool {
	if a.Available != b.Available || a.Enabled != b.Enabled || a.Playing != b.Playing || a.Peers != b.Peers {
		return true
	}
	if math.Abs(a.Tempo-b.Tempo) > 0.01 || math.Abs(a.Quantum-b.Quantum) > 0.01 {
		return true
	}
	return math.Abs(a.Phase-b.Phase) > 0.02
}

// HandleEvent applies daemon→child bridge frames (parent→child, no response).
func (f *abletonLinkFeature) HandleEvent(event string, data json.RawMessage) {
	if event != "bridge" || f.sess == nil || !f.sess.Available() {
		return
	}
	var b linkBridge
	if json.Unmarshal(data, &b) != nil || !b.Drive {
		return
	}
	now := time.Now()
	if b.Tempo > 0 {
		f.sess.SetTempo(b.Tempo, now)
	}
	if b.HasPhase {
		f.alignPhase(b.Phase, now)
	}
	if b.HasPlaying && f.cfg.StartStopSync {
		f.sess.SetPlaying(b.Playing, now)
	}
}

// alignPhase nudges the Link timeline so the current beat's fractional position matches the DJ
// master's beat phase (0..1). Picks the beat-aligned target nearest the current Link beat to
// avoid a full-beat jump, then RequestBeat (gentle, no tempo change).
func (f *abletonLinkFeature) alignPhase(phase float64, now time.Time) {
	if phase < 0 {
		phase = 0
	}
	if phase >= 1 {
		phase -= math.Floor(phase)
	}
	cur := f.sess.State(now).Beat
	target := math.Floor(cur) + phase
	if target < cur-0.5 {
		target += 1
	} else if target > cur+0.5 {
		target -= 1
	}
	f.sess.RequestBeat(target, now)
}

// ── control RPC (daemon proxy → child) ──

var errLinkClosed = errors.New("abletonlink: session closed")

func (f *abletonLinkFeature) Handle(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if f.sess == nil {
		return nil, errLinkClosed
	}
	switch method {
	case "setEnabled":
		var r struct {
			On bool `json:"on"`
		}
		if err := json.Unmarshal(params, &r); err != nil {
			return nil, err
		}
		f.cfg.Enabled = r.On
		f.sess.SetEnabled(r.On)
		return nil, nil
	case "setQuantum":
		var r struct {
			Quantum int `json:"quantum"`
		}
		if err := json.Unmarshal(params, &r); err != nil {
			return nil, err
		}
		f.cfg.Quantum = r.Quantum
		f.sess.SetQuantum(float64(r.Quantum))
		return nil, nil
	case "setStartStopSync":
		var r struct {
			On bool `json:"on"`
		}
		if err := json.Unmarshal(params, &r); err != nil {
			return nil, err
		}
		f.cfg.StartStopSync = r.On
		f.sess.SetStartStopSyncEnabled(r.On)
		return nil, nil
	case "setTempo":
		var r struct {
			BPM float64 `json:"bpm"`
		}
		if err := json.Unmarshal(params, &r); err != nil {
			return nil, err
		}
		f.sess.SetTempo(r.BPM, time.Now())
		return nil, nil
	case "resync":
		// Hard phrase realign: map beat 0 (phrase start) to now.
		f.sess.ForceBeat(0, time.Now())
		return nil, nil
	case "setPlaying":
		var r struct {
			Playing bool `json:"playing"`
		}
		if err := json.Unmarshal(params, &r); err != nil {
			return nil, err
		}
		f.sess.SetPlaying(r.Playing, time.Now())
		return nil, nil
	case "getState":
		return json.Marshal(f.sess.State(time.Now()))
	default:
		return nil, errUnknownMethod(method)
	}
}
