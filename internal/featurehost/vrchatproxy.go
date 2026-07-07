package featurehost

import (
	"encoding/json"
	"sync"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/vrchat"
)

// VrchatStatus mirrors the child pipeline's connectivity for the UI.
type VrchatStatus struct {
	Connected bool
	LastError string
}

// VrchatProxy is the daemon-side stand-in for the subprocessed VRChat pipeline:
// status mirror + realtime event fan-out + token push on login/logout.
type VrchatProxy struct {
	host *Host

	mu     sync.Mutex
	status VrchatStatus

	evMu    sync.Mutex
	evSubs  map[int]chan vrchat.PipelineEvent
	nextSub int
}

// NewVrchatProxy builds the proxy + its host. token is re-read per (re)spawn so a
// restart picks up the current session.
func NewVrchatProxy(log *logbus.Bus, token func() string) (*VrchatProxy, error) {
	p := &VrchatProxy{evSubs: map[int]chan vrchat.PipelineEvent{}}
	h, err := New(Options{
		Name: "vrchat",
		Log:  log,
		Init: func() any { return vrchatInit{AuthToken: token()} },
		OnEvent: map[string]func(json.RawMessage){
			"state": func(data json.RawMessage) {
				var st vrchatPipeState
				if json.Unmarshal(data, &st) != nil {
					return
				}
				p.mu.Lock()
				p.status = VrchatStatus(st)
				p.mu.Unlock()
			},
			"pipe": func(data json.RawMessage) {
				var ev vrchat.PipelineEvent
				if json.Unmarshal(data, &ev) != nil {
					return
				}
				p.evMu.Lock()
				subs := make([]chan vrchat.PipelineEvent, 0, len(p.evSubs))
				for _, ch := range p.evSubs {
					subs = append(subs, ch)
				}
				p.evMu.Unlock()
				for _, ch := range subs {
					select {
					case ch <- ev:
					default: // slow consumer: drop, realtime data only
					}
				}
			},
		},
		OnDown: func() {
			p.mu.Lock()
			p.status = VrchatStatus{}
			p.mu.Unlock()
		},
	})
	if err != nil {
		return nil, err
	}
	p.host = h
	return p, nil
}

// Host exposes the supervising host (module Start/Stop, Stats).
func (p *VrchatProxy) Host() *Host { return p.host }

// Status mirrors pipeline connectivity; zero while the child is down.
func (p *VrchatProxy) Status() VrchatStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.host.Running() {
		return VrchatStatus{}
	}
	return p.status
}

// SetAuth pushes a session-token change to the running child ("" = logged out).
func (p *VrchatProxy) SetAuth(token string) {
	_ = p.host.Send("auth", vrchatAuthEvent{AuthToken: token})
}

// SubscribeEvents streams realtime pipeline events (buffered; drops on overflow).
func (p *VrchatProxy) SubscribeEvents() (<-chan vrchat.PipelineEvent, func()) {
	p.evMu.Lock()
	defer p.evMu.Unlock()
	id := p.nextSub
	p.nextSub++
	ch := make(chan vrchat.PipelineEvent, 32)
	p.evSubs[id] = ch
	return ch, func() {
		p.evMu.Lock()
		defer p.evMu.Unlock()
		if c, ok := p.evSubs[id]; ok {
			delete(p.evSubs, id)
			close(c)
		}
	}
}
