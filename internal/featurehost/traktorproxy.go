package featurehost

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/session/sources/traktorsrc"
)

// TraktorProxy is the daemon-side stand-in for the subprocessed Traktor listener: a
// session.Source fed by the child's "obs" events, plus the UI surface the in-proc
// *traktor.Server used to provide (Listening/SetLogging). Crash-safe by construction -
// the child dying just flips Listening false until the host restarts it.
type TraktorProxy struct {
	host *Host

	mu        sync.Mutex
	listening bool
	obs       chan session.Observation
}

// TraktorConfig builds the child's init params (re-read live on every restart).
type TraktorConfig = traktorInit

// NewTraktorProxy builds the proxy + its host. mon receives the child's ingest monitor
// lines (the Traktor tab bus). init is re-evaluated per (re)spawn.
func NewTraktorProxy(log, mon *logbus.Bus, initFn func() TraktorConfig) (*TraktorProxy, error) {
	p := &TraktorProxy{obs: make(chan session.Observation, 256)}
	h, err := New(Options{
		Name: "traktor",
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
				default: // drop on overflow - parity with in-proc subscriber buffers
				}
			},
			"state": func(data json.RawMessage) {
				var st struct {
					Listening bool `json:"listening"`
				}
				if json.Unmarshal(data, &st) != nil {
					return
				}
				p.mu.Lock()
				p.listening = st.Listening
				p.mu.Unlock()
			},
			"mon": func(data json.RawMessage) {
				var le logEvent
				if json.Unmarshal(data, &le) == nil && mon != nil {
					mon.Log(logbus.Level(le.Level), le.Source, le.Msg, le.Fields)
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

// Host exposes the supervising host (module Start/Stop, SetNotifier, Stats).
func (p *TraktorProxy) Host() *Host { return p.host }

// Listening mirrors the child listener's bind state; false while the child is down.
func (p *TraktorProxy) Listening() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.host.Running() && p.listening
}

// SetLogging toggles raw-payload logging in the child (best-effort; config persists the
// desired state and a restart re-applies it via init).
func (p *TraktorProxy) SetLogging(on bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = p.host.Call(ctx, "setLogging", map[string]bool{"on": on})
}

// ── session.Source ───────────────────────────────────────────────────────────

// ID implements session.Source.
func (p *TraktorProxy) ID() string { return session.SourceTraktor }

// Capabilities implements session.Source (static - borrowed from the in-proc adapter).
func (p *TraktorProxy) Capabilities() []session.Capability {
	return traktorsrc.New(nil).Capabilities()
}

// Start implements session.Source: pumps child observations into the merger until ctx
// is cancelled. The host's lifecycle is owned by the "traktor" module, not this pump.
func (p *TraktorProxy) Start(ctx context.Context, emit func(session.Observation)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case o := <-p.obs:
			emit(o)
		}
	}
}
