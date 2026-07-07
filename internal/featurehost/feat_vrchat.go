package featurehost

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"rave.page/mate/internal/vrchat"
)

func init() { Register("vrchat", func() Feature { return &vrchatFeature{} }) }

// vrchatInit seeds the child with the current session token (may be empty -
// the child idles until an "auth" event delivers one).
type vrchatInit struct {
	AuthToken string `json:"authToken"`
}

// vrchatAuthEvent is the parent→child token update (login / logout / refresh).
type vrchatAuthEvent struct {
	AuthToken string `json:"authToken"`
}

// vrchatPipeState mirrors pipeline connectivity to the daemon ("state" events).
type vrchatPipeState struct {
	Connected bool   `json:"connected"`
	LastError string `json:"lastError,omitempty"`
}

// vrchatReconnectDelay paces pipeline redials - VRChat rate-limits aggressively.
var vrchatReconnectDelay = 15 * time.Second

// vrchatFeature hosts the VRChat pipeline WS in the child: connect with the
// session token, forward realtime events ("pipe") + connectivity ("state") to
// the daemon, redial on drop, and hot-swap the token on "auth" events.
type vrchatFeature struct {
	rt *Runtime

	mu    sync.Mutex
	token string
	kick  chan struct{} // closed+replaced on token change

	last vrchatPipeState
}

func (f *vrchatFeature) Init(params json.RawMessage, rt *Runtime) error {
	var cfg vrchatInit
	if err := json.Unmarshal(params, &cfg); err != nil {
		return err
	}
	f.rt = rt
	f.token = cfg.AuthToken
	f.kick = make(chan struct{})
	return nil
}

// HandleEvent consumes parent→child "auth" token updates.
func (f *vrchatFeature) HandleEvent(event string, data json.RawMessage) {
	if event != "auth" {
		return
	}
	var ev vrchatAuthEvent
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	f.mu.Lock()
	f.token = ev.AuthToken
	close(f.kick) // wake the session loop
	f.kick = make(chan struct{})
	f.mu.Unlock()
}

func (f *vrchatFeature) snapshot() (token string, kick chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token, f.kick
}

// Start loops connect → stream → reconnect until ctx is done. No token = idle
// until one arrives; a rejected/dropped connection backs off before redialing.
func (f *vrchatFeature) Start(ctx context.Context) error {
	for {
		token, kick := f.snapshot()
		if token == "" {
			f.emitState(vrchatPipeState{})
			select {
			case <-ctx.Done():
				return nil
			case <-kick:
				continue
			}
		}
		err := f.session(ctx, token, kick)
		st := vrchatPipeState{}
		if err != nil {
			st.LastError = err.Error()
			f.rt.Log.Debug("vrchat", "pipeline session ended", map[string]any{"error": err.Error()})
		}
		f.emitState(st)
		select {
		case <-ctx.Done():
			return nil
		case <-kick: // token changed: redial immediately
		case <-time.After(vrchatReconnectDelay):
		}
	}
}

// session runs one connected pipeline lifetime, forwarding events to the daemon.
func (f *vrchatFeature) session(ctx context.Context, token string, kick chan struct{}) error {
	p, err := vrchat.DialPipeline(ctx, token)
	if err != nil {
		return err
	}
	defer p.Close()
	f.rt.Log.Info("vrchat", "pipeline connected", nil)
	f.emitState(vrchatPipeState{Connected: true})
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-kick:
			return nil // token changed; caller redials
		case ev, ok := <-p.Events():
			if !ok {
				return p.Err()
			}
			f.rt.Emit("pipe", ev)
		}
	}
}

// emitState pushes "state" on change only.
func (f *vrchatFeature) emitState(st vrchatPipeState) {
	if st == f.last {
		return
	}
	f.last = st
	f.rt.Emit("state", st)
}

func (f *vrchatFeature) Handle(_ context.Context, method string, _ json.RawMessage) (json.RawMessage, error) {
	return nil, errUnknownMethod(method)
}
