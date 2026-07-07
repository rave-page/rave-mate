package featurehost

import (
	"context"
	"encoding/json"
	"sync"

	"rave.page/mate/internal/icecast"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/session/sources/icecastsrc"
)

// IcecastProxy is the daemon-side stand-in for the subprocessed set-capture receiver:
// a session.Source (now-playing metadata) + the Snapshot/SubscribeCapture surface the
// in-proc *icecast.Receiver provided (settings card, linkSetCaptures).
type IcecastProxy struct {
	host *Host

	mu     sync.Mutex
	status icecast.Status
	obs    chan session.Observation

	capMu   sync.Mutex
	capSubs map[int]chan icecast.Capture
	nextSub int
}

// NewIcecastProxy builds the proxy + its host. init is re-evaluated per (re)spawn, so a
// settings edit takes effect on module restart (toggle off/on - same UX as before).
func NewIcecastProxy(log *logbus.Bus, initFn func() icecast.Config) (*IcecastProxy, error) {
	p := &IcecastProxy{obs: make(chan session.Observation, 64), capSubs: map[int]chan icecast.Capture{}}
	h, err := New(Options{
		Name: "icecast",
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
			"state": func(data json.RawMessage) {
				var st icecast.Status
				if json.Unmarshal(data, &st) != nil {
					return
				}
				p.mu.Lock()
				p.status = st
				p.mu.Unlock()
			},
			"capture": func(data json.RawMessage) {
				var c icecast.Capture
				if json.Unmarshal(data, &c) != nil {
					return
				}
				p.capMu.Lock()
				subs := make([]chan icecast.Capture, 0, len(p.capSubs))
				for _, ch := range p.capSubs {
					subs = append(subs, ch)
				}
				p.capMu.Unlock()
				for _, ch := range subs {
					select {
					case ch <- c:
					default: // drop on overflow (captures are rare; parity with in-proc fan-out)
					}
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
func (p *IcecastProxy) Host() *Host { return p.host }

// Snapshot mirrors the child receiver's status; Listening is false while the child is down.
func (p *IcecastProxy) Snapshot() icecast.Status {
	p.mu.Lock()
	st := p.status
	p.mu.Unlock()
	if !p.host.Running() {
		st.Listening = false
		st.Connected = false
	}
	return st
}

// SubscribeCapture mirrors the child's capture start/end events (buffered; drops on overflow).
func (p *IcecastProxy) SubscribeCapture() (<-chan icecast.Capture, func()) {
	p.capMu.Lock()
	defer p.capMu.Unlock()
	id := p.nextSub
	p.nextSub++
	ch := make(chan icecast.Capture, 16)
	p.capSubs[id] = ch
	return ch, func() {
		p.capMu.Lock()
		defer p.capMu.Unlock()
		if c, ok := p.capSubs[id]; ok {
			delete(p.capSubs, id)
			close(c)
		}
	}
}

// ── session.Source ───────────────────────────────────────────────────────────

// ID implements session.Source.
func (p *IcecastProxy) ID() string { return session.SourceIcecast }

// Capabilities implements session.Source (static - borrowed from the in-proc adapter).
func (p *IcecastProxy) Capabilities() []session.Capability {
	return icecastsrc.New(nil).Capabilities()
}

// Start implements session.Source: pumps child observations until ctx is cancelled.
// The host lifecycle is owned by the "setcapture" module, not this pump.
func (p *IcecastProxy) Start(ctx context.Context, emit func(session.Observation)) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case o := <-p.obs:
			emit(o)
		}
	}
}
