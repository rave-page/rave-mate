// Package stream publishes a live DJ set to the rave.page ingest API. It subscribes to the
// session merger (the multi-source hub), batches merged updates (≤50 / 500ms), POSTs
// /streams/{id}/ingest, and sends a 30s heartbeat so the server reaper doesn't auto-end the
// stream. One stream at a time. Canonical field names match Traktor's wire keys, so a
// Traktor-only setup is byte-identical to the legacy direct-from-Traktor path. Port of
// electron/src/main/streamPublisher.ts. Publish token lives in memory only.
package stream

import (
	"context"
	"maps"
	"sync"
	"time"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

const (
	source         = "stream"
	flushInterval  = 500 * time.Millisecond
	flushBatchSize = 50
	heartbeatEvery = 30 * time.Second
)

// Status is the observable publisher state (drives the UI).
type Status struct {
	IsLive            bool
	StreamID          string
	StartedAt         string
	Title             string
	PendingEventCount int
	LastFlushAt       string
	LastFlushOK       bool
	LastFlushError    string
	LastError         string
}

// StartArgs configures a new stream.
type StartArgs struct {
	Title     string
	Kind      string // default "dj_set"
	Source    string // default "traktor_pro_4"
	UserToken string
	Metadata  map[string]any
}

// Publisher owns the lifecycle of one live stream.
type Publisher struct {
	log *logbus.Bus
	api *api.Client
	// subscribe yields merged session updates + an unsubscribe (decoupled from the
	// Merger so the publisher can run in a feature subprocess fed over IPC).
	subscribe func() (<-chan session.Update, func())

	mu                                 sync.Mutex
	live                               bool
	streamID                           string
	pubToken                           string
	pubExp                             string
	started                            string
	title                              string
	pending                            []api.IngestEvent
	seq                                uint64
	lastFlushAt, lastFlushErr, lastErr string
	lastFlushOK                        bool
	cancel                             context.CancelFunc
	wg                                 sync.WaitGroup

	statusMu   sync.Mutex
	statusSubs map[int]chan Status
	nextSub    int
}

// New constructs a publisher bound to an update feed (e.g. Merger.Subscribe) + API client.
func New(log *logbus.Bus, apiClient *api.Client, subscribe func() (<-chan session.Update, func())) *Publisher {
	return &Publisher{log: log, api: apiClient, subscribe: subscribe, lastFlushOK: true, statusSubs: map[int]chan Status{}}
}

// Start creates the stream and begins publishing. Idempotent: a second call while live
// returns current status without error.
func (p *Publisher) Start(ctx context.Context, args StartArgs) (Status, error) {
	p.mu.Lock()
	if p.live {
		st := p.statusLocked()
		p.mu.Unlock()
		return st, nil
	}
	p.mu.Unlock()

	kind := args.Kind
	if kind == "" {
		kind = "dj_set"
	}
	src := args.Source
	if src == "" {
		src = "traktor_pro_4"
	}
	meta := map[string]any{"software": "traktor_pro_4"}
	maps.Copy(meta, args.Metadata)

	resp, err := p.api.CreateStream(ctx, args.UserToken, api.CreateStreamReq{
		Title: args.Title, Kind: kind, Source: src, Metadata: meta,
	})
	if err != nil {
		p.mu.Lock()
		p.lastErr = err.Error()
		p.mu.Unlock()
		p.broadcast()
		return p.Status(), err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.live = true
	p.streamID = resp.StreamID
	p.pubToken = resp.PublishToken
	p.pubExp = resp.PublishTokenExpiresAt
	p.started = orNow(resp.StartedAt)
	p.title = args.Title
	p.pending = nil
	p.seq = 0
	p.lastFlushAt, p.lastFlushErr, p.lastErr = "", "", ""
	p.lastFlushOK = true
	p.cancel = cancel
	p.mu.Unlock()

	events, unsub := p.subscribe()
	p.wg.Add(1)
	debuglog.Go(p.log, source, func() { p.run(runCtx, events, unsub) })

	p.log.Info(source, "stream started", map[string]any{"streamId": resp.StreamID, "title": args.Title, "expires": resp.PublishTokenExpiresAt})
	p.broadcast()
	return p.Status(), nil
}

// run is the single owner of pending/flush/heartbeat for the active stream.
func (p *Publisher) run(ctx context.Context, events <-chan session.Update, unsub func()) {
	defer p.wg.Done()
	defer unsub()
	flush := time.NewTicker(flushInterval)
	defer flush.Stop()
	heart := time.NewTicker(heartbeatEvery)
	defer heart.Stop()

	for {
		select {
		case <-ctx.Done():
			p.flush(context.Background()) // final drain
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			p.enqueue(e)
		case <-flush.C:
			p.flush(ctx)
		case <-heart.C:
			p.heartbeat(ctx)
		}
	}
}

func (p *Publisher) enqueue(u session.Update) {
	// Only deck/channel/master updates map to ingest events; derived scopes (nowPlaying)
	// stay local to the recorder/UI.
	var deck, channel string
	switch u.Scope.Kind {
	case session.ScopeDeck:
		deck = u.Scope.ID
	case session.ScopeChannel:
		channel = u.Scope.ID
	case session.ScopeMaster:
	default:
		return
	}
	p.mu.Lock()
	p.seq++
	ev := api.IngestEvent{Type: u.Type, Deck: deck, Channel: channel, State: u.State, Seq: p.seq}
	p.pending = append(p.pending, ev)
	overflow := len(p.pending) >= flushBatchSize
	p.mu.Unlock()
	p.broadcast()
	if overflow {
		p.flush(context.Background())
	}
}

func (p *Publisher) flush(ctx context.Context) {
	p.mu.Lock()
	if !p.live || p.streamID == "" || len(p.pending) == 0 {
		p.mu.Unlock()
		return
	}
	n := min(len(p.pending), flushBatchSize)
	batch := p.pending[:n]
	p.pending = p.pending[n:]
	id, token := p.streamID, p.pubToken
	p.mu.Unlock()

	err := p.api.Ingest(ctx, id, token, batch)
	p.mu.Lock()
	p.lastFlushAt = time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		p.lastFlushOK = false
		p.lastFlushErr = err.Error()
		// Drop the failed batch (desktop is source of truth; next batch carries fresh state).
	} else {
		p.lastFlushOK = true
		p.lastFlushErr = ""
	}
	p.mu.Unlock()
	p.broadcast()
}

func (p *Publisher) heartbeat(ctx context.Context) {
	p.mu.Lock()
	id, token, live := p.streamID, p.pubToken, p.live
	p.mu.Unlock()
	if !live || id == "" {
		return
	}
	if err := p.api.Heartbeat(ctx, id, token); err != nil {
		p.log.Warn(source, "heartbeat failed", map[string]any{"error": err.Error()})
	}
}

// End stops publishing and ends the stream server-side (best effort).
func (p *Publisher) End(ctx context.Context) (Status, error) {
	p.mu.Lock()
	if !p.live {
		st := p.statusLocked()
		p.mu.Unlock()
		return st, nil
	}
	cancel := p.cancel
	id, token := p.streamID, p.pubToken
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	p.wg.Wait() // run() drains pending on ctx.Done

	if err := p.api.EndStream(ctx, id, token); err != nil {
		p.log.Warn(source, "end failed (reaper will clean up)", map[string]any{"error": err.Error()})
	}

	p.mu.Lock()
	p.live = false
	p.streamID, p.pubToken, p.pubExp, p.started, p.title = "", "", "", "", ""
	p.pending = nil
	p.seq = 0
	p.cancel = nil
	p.mu.Unlock()

	p.log.Info(source, "stream ended", map[string]any{"streamId": id})
	p.broadcast()
	return p.Status(), nil
}

// Status returns a snapshot of current publisher state.
func (p *Publisher) Status() Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.statusLocked()
}

func (p *Publisher) statusLocked() Status {
	return Status{
		IsLive:            p.live,
		StreamID:          p.streamID,
		StartedAt:         p.started,
		Title:             p.title,
		PendingEventCount: len(p.pending),
		LastFlushAt:       p.lastFlushAt,
		LastFlushOK:       p.lastFlushOK,
		LastFlushError:    p.lastFlushErr,
		LastError:         p.lastErr,
	}
}

// SubscribeStatus returns a channel of status updates + unsubscribe.
func (p *Publisher) SubscribeStatus() (<-chan Status, func()) {
	p.statusMu.Lock()
	defer p.statusMu.Unlock()
	id := p.nextSub
	p.nextSub++
	ch := make(chan Status, 16)
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

func (p *Publisher) broadcast() {
	st := p.Status()
	p.statusMu.Lock()
	chans := make([]chan Status, 0, len(p.statusSubs))
	for _, ch := range p.statusSubs {
		chans = append(chans, ch)
	}
	p.statusMu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- st:
		default:
		}
	}
}

func orNow(s string) string {
	if s != "" {
		return s
	}
	return time.Now().UTC().Format(time.RFC3339)
}
