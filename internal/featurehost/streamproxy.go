package featurehost

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
	"rave.page/mate/internal/stream"
)

// StreamProxy is the daemon-side stand-in for the subprocessed live-stream publisher:
// same Start/End/Status/SubscribeStatus surface, with merged session updates forwarded
// to the child while it's up. If the child crashes mid-stream, the server reaper ends
// the stream (heartbeats stop) and the status mirror drops to not-live.
type StreamProxy struct {
	host *Host
	mix  *session.Merger
	log  *logbus.Bus

	mu     sync.Mutex
	appCtx context.Context // bound once at wiring; child lifetime outlives a Start call
	status stream.Status

	statusMu   sync.Mutex
	statusSubs map[int]chan stream.Status
	nextSub    int
}

// StreamConfig builds the child's init params (re-read on every (re)spawn).
type StreamConfig = streamInit

// NewStreamProxy builds the proxy + its host (child spawns lazily on first Start).
func NewStreamProxy(log *logbus.Bus, mix *session.Merger, initFn func() StreamConfig) (*StreamProxy, error) {
	p := &StreamProxy{mix: mix, log: log, statusSubs: map[int]chan stream.Status{}}
	h, err := New(Options{
		Name: "stream",
		Log:  log,
		Init: func() any { return initFn() },
		OnEvent: map[string]func(json.RawMessage){
			"status": func(data json.RawMessage) {
				var st stream.Status
				if json.Unmarshal(data, &st) != nil {
					return
				}
				p.setStatus(st)
			},
		},
		OnDown: func() { p.setStatus(stream.Status{}) }, // child gone ⇒ not live
	})
	if err != nil {
		return nil, err
	}
	p.host = h
	return p, nil
}

// Host exposes the supervising host (SetNotifier, Stats).
func (p *StreamProxy) Host() *Host { return p.host }

// Bind wires the app lifetime context and starts the merged-update forwarder (idle
// until a stream is live). Call once at app wiring.
func (p *StreamProxy) Bind(ctx context.Context) {
	p.mu.Lock()
	p.appCtx = ctx
	p.mu.Unlock()
	debuglog.Go(p.log, "feature:stream", func() { p.forward(ctx) })
}

// forward taps the merger and relays updates to the child while a stream is live.
func (p *StreamProxy) forward(ctx context.Context) {
	ch, unsub := p.mix.Subscribe()
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return
		case u, ok := <-ch:
			if !ok {
				return
			}
			p.mu.Lock()
			live := p.status.IsLive
			p.mu.Unlock()
			if live && p.host.Running() {
				_ = p.host.Send("update", u)
			}
		}
	}
}

func (p *StreamProxy) setStatus(st stream.Status) {
	p.mu.Lock()
	p.status = st
	p.mu.Unlock()
	p.statusMu.Lock()
	chans := make([]chan stream.Status, 0, len(p.statusSubs))
	for _, c := range p.statusSubs {
		chans = append(chans, c)
	}
	p.statusMu.Unlock()
	for _, c := range chans {
		select {
		case c <- st:
		default:
		}
	}
}

// ensureUp lazily spawns the child (bound to the app ctx) and waits for ready.
func (p *StreamProxy) ensureUp(ctx context.Context) error {
	p.mu.Lock()
	appCtx := p.appCtx
	p.mu.Unlock()
	if appCtx == nil {
		return errors.New("stream proxy not bound")
	}
	if err := p.host.Start(appCtx); err != nil {
		return err
	}
	deadline := time.Now().Add(initTimeout)
	for !p.host.Running() {
		if time.Now().After(deadline) {
			return errors.New("stream service didn't come up")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	return nil
}

// Start creates the stream in the child and begins publishing.
func (p *StreamProxy) Start(ctx context.Context, args stream.StartArgs) (stream.Status, error) {
	if err := p.ensureUp(ctx); err != nil {
		return stream.Status{}, err
	}
	raw, err := p.host.Call(ctx, "start", args)
	if err != nil {
		return p.Status(), err
	}
	var st stream.Status
	_ = json.Unmarshal(raw, &st)
	p.setStatus(st)
	return st, nil
}

// End stops publishing and ends the stream server-side.
func (p *StreamProxy) End(ctx context.Context) (stream.Status, error) {
	if !p.host.Running() {
		return p.Status(), nil // child gone ⇒ nothing live (reaper handles the server side)
	}
	raw, err := p.host.Call(ctx, "end", nil)
	if err != nil {
		return p.Status(), err
	}
	var st stream.Status
	_ = json.Unmarshal(raw, &st)
	p.setStatus(st)
	return st, nil
}

// Status mirrors the child publisher's state; zero (not live) while the child is down.
func (p *StreamProxy) Status() stream.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.host.Running() {
		return stream.Status{}
	}
	return p.status
}

// SubscribeStatus returns a channel of status updates + unsubscribe.
func (p *StreamProxy) SubscribeStatus() (<-chan stream.Status, func()) {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	id := p.nextSub
	p.nextSub++
	ch := make(chan stream.Status, 16)
	p.statusSubs[id] = ch
	return ch, func() {
		p.statusMu.Lock()
		defer p.statusMu.Unlock()
		if c, ok := p.statusSubs[id]; ok {
			delete(p.statusSubs, id)
			close(c)
		}
	}
}
