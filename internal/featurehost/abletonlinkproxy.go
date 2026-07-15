package featurehost

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"time"

	"rave.page/mate/internal/abletonlink"
	"rave.page/mate/internal/logbus"
)

// AbletonLinkConfig builds the child's init params (re-read on every respawn).
type AbletonLinkConfig = abletonLinkInit

// LinkBridge is the daemon→child tempo/phase bridge frame (P2). Exported for app.go's DJ→Link
// bridge loop.
type LinkBridge = linkBridge

// AbletonLinkProxy is the daemon-side stand-in for the subprocessed Link session: mirrors the
// child's session state for the UI + the media-sync house clock, forwards control RPCs, and
// pushes the fused DJ tempo/phase bridge. Satisfies mediasync.TimeSource via Position().
type AbletonLinkProxy struct {
	host *Host

	mu    sync.Mutex
	state abletonlink.State
	at    time.Time // wall time the mirror was last updated

	// desired runtime state re-pushed on every (re)spawn so a restarted child reconstructs it.
	desMu    sync.Mutex
	desEnab  bool
	haveEnab bool
}

// posMaxExtrapolate caps how far the house clock extrapolates past the last mirror sample -
// a dead/hung child stops advancing rather than running away.
const posMaxExtrapolate = 2 * time.Second

// NewAbletonLinkProxy builds the proxy + its host. init is re-evaluated per (re)spawn.
func NewAbletonLinkProxy(log *logbus.Bus, initFn func() AbletonLinkConfig) (*AbletonLinkProxy, error) {
	p := &AbletonLinkProxy{}
	h, err := New(Options{
		Name:             "abletonlink",
		Log:              log,
		HeartbeatTimeout: 5 * time.Second, // the child beats from its sample loop
		Init:             func() any { return initFn() },
		OnEvent: map[string]func(json.RawMessage){
			"state": func(data json.RawMessage) {
				var st abletonlink.State
				if json.Unmarshal(data, &st) != nil {
					return
				}
				p.mu.Lock()
				p.state = st
				p.at = time.Now()
				p.mu.Unlock()
			},
		},
		OnReady: func() {
			// Re-push a runtime enable toggle (config-driven enable comes via Init).
			p.desMu.Lock()
			enab, have := p.desEnab, p.haveEnab
			p.desMu.Unlock()
			if have {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, _ = p.host.Call(ctx, "setEnabled", map[string]any{"on": enab})
				cancel()
			}
		},
		OnDown: func() {
			p.mu.Lock()
			p.state = abletonlink.State{}
			p.at = time.Time{}
			p.mu.Unlock()
		},
	})
	if err != nil {
		return nil, err
	}
	p.host = h
	return p, nil
}

// Host exposes the supervising host (module Start/Stop, SetNotifier, Stats).
func (p *AbletonLinkProxy) Host() *Host { return p.host }

// State returns the mirrored session state (zeroed while the child is down). Available reflects
// whether a real Link backend is compiled in.
func (p *AbletonLinkProxy) State() abletonlink.State {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.host.Running() {
		return abletonlink.State{}
	}
	return p.state
}

// StateNow returns the mirror with Beat/Phase advanced from the last ~10 Hz sample to the current
// wall clock (tempo-driven), so the value reads fresh at call time. The mirror snapshot is up to
// ~100 ms stale; the UI's 30 fps phrase-bar interpolation anchors to this, so a stale phase would
// make the bar run behind and hitch forward on every re-sync. Falls back to the raw mirror when
// Link isn't running/advancing (disabled, tempo-less, or a dead/hung child past posMaxExtrapolate).
func (p *AbletonLinkProxy) StateNow() abletonlink.State {
	p.mu.Lock()
	st, at := p.state, p.at
	running := p.host.Running()
	p.mu.Unlock()
	if !running {
		return abletonlink.State{}
	}
	if !st.Available || !st.Enabled || st.Tempo <= 0 || at.IsZero() {
		return st
	}
	elapsed := time.Since(at)
	if elapsed <= 0 || elapsed > posMaxExtrapolate {
		return st // dead/stale mirror - don't run the phase away
	}
	adv := elapsed.Seconds() * st.Tempo / 60.0 // beats elapsed since the sample
	q := st.Quantum
	if q <= 0 {
		q = abletonlink.DefaultQuantum
	}
	st.Beat += adv
	ph := st.Phase + adv
	st.Phase = ph - math.Floor(ph/q)*q // wrap into [0,q)
	return st
}

// Position implements mediasync.TimeSource: Link musical time as a monotonic house clock,
// extrapolated from the last mirror sample. running=false when Link is down/disabled/tempo-less
// or the mirror is stale (dead child) so the media chaser leaves sources untouched.
func (p *AbletonLinkProxy) Position() (time.Duration, bool) {
	p.mu.Lock()
	st, at := p.state, p.at
	p.mu.Unlock()
	if !p.host.Running() || !st.Available || !st.Enabled || st.Tempo <= 0 || at.IsZero() {
		return 0, false
	}
	elapsed := time.Since(at)
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed > posMaxExtrapolate {
		return 0, false // stale mirror - treat as stopped
	}
	secPerBeat := 60.0 / st.Tempo
	beats := st.Beat + elapsed.Seconds()/secPerBeat
	return time.Duration(beats * secPerBeat * float64(time.Second)), true
}

var _ interface {
	Position() (time.Duration, bool)
} = (*AbletonLinkProxy)(nil)

// ── control (daemon → child RPC) ──

// SetEnabled joins/leaves the Link session at runtime + remembers it for re-push on respawn.
func (p *AbletonLinkProxy) SetEnabled(ctx context.Context, on bool) error {
	p.desMu.Lock()
	p.desEnab, p.haveEnab = on, true
	p.desMu.Unlock()
	_, err := p.host.Call(ctx, "setEnabled", map[string]any{"on": on})
	return err
}

// SetQuantum sets the phrase length in beats (8/16/32).
func (p *AbletonLinkProxy) SetQuantum(ctx context.Context, beats int) error {
	_, err := p.host.Call(ctx, "setQuantum", map[string]any{"quantum": beats})
	return err
}

// SetStartStopSync toggles transport (start/stop) sharing across peers.
func (p *AbletonLinkProxy) SetStartStopSync(ctx context.Context, on bool) error {
	_, err := p.host.Call(ctx, "setStartStopSync", map[string]any{"on": on})
	return err
}

// SetTempo manually sets the session tempo (used when tempo-owner=always without a DJ feed).
func (p *AbletonLinkProxy) SetTempo(ctx context.Context, bpm float64) error {
	_, err := p.host.Call(ctx, "setTempo", map[string]any{"bpm": bpm})
	return err
}

// Resync hard-realigns the phrase (maps beat 0 to now).
func (p *AbletonLinkProxy) Resync(ctx context.Context) error {
	_, err := p.host.Call(ctx, "resync", nil)
	return err
}

// SetPlaying sets the shared transport state (start/stop-sync must be enabled).
func (p *AbletonLinkProxy) SetPlaying(ctx context.Context, playing bool) error {
	_, err := p.host.Call(ctx, "setPlaying", map[string]any{"playing": playing})
	return err
}

// PushBridge sends one fused DJ tempo/phase bridge frame to the child (fire-and-forget). No-op
// error when the child is down (the next frame retries).
func (p *AbletonLinkProxy) PushBridge(b LinkBridge) error {
	return p.host.Send("bridge", b)
}
