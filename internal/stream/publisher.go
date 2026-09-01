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
	"math/rand/v2"
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
	// pendingMax caps the merged-update queue (each event is a small state map, ~1KB max ->
	// ~0.5MB worst case). Policy: drop-OLDEST - desktop is source of truth, so the freshest
	// state must survive a server-error backoff window.
	pendingMax = 500
)

// Retry discipline (vars: shrunk in tests). A 401 means the publish token expired mid-set;
// a 429 means we ARE the load - both were previously retried at flush cadence (2/s) for
// the rest of the set (observed: 40min of self-inflicted 401/429 before a crash).
var (
	retryBackoffBase = time.Second
	retryBackoffMax  = 2 * time.Minute
	reacquireMin     = 30 * time.Second // min spacing between 401-recovery attempts (refresh, then re-acquire)
)

// Proactive publish-token refresh. The token has a short server TTL; refreshing
// in place (POST /streams/{id}/token-refresh) keeps stream_id, so one continuous
// set is NOT fragmented into many by a mid-set re-create. Fire at 70-80% of the
// remaining lifetime (jittered) so the swap lands before expiry.
const (
	refreshFracMin = 0.70
	refreshFracMax = 0.80
	// refreshNever effectively disables the timer until it is armed from pubExp.
	refreshNever = time.Duration(1 << 62)
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
	args                               StartArgs // kept for publish-token re-acquire (user token in memory only)
	pending                            []api.IngestEvent
	seq                                uint64
	lastFlushAt, lastFlushErr, lastErr string
	lastFlushOK                        bool
	backoff                            time.Duration // current 429/re-acquire pause; 0 = none
	retryAfter                         time.Time     // flush/heartbeat suppressed until then
	lastReacq                          time.Time     // last CreateStream re-acquire attempt
	cancel                             context.CancelFunc
	wg                                 sync.WaitGroup
	hbGate                             logbus.Gate // heartbeat-failure log gate (30s cadence)
	flushGate                          logbus.Gate // ingest-failure log gate (2/s cadence)
	refreshGate                        logbus.Gate // token-refresh-failure log gate

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

	resp, err := p.api.CreateStream(ctx, args.UserToken, createReq(args))
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
	p.args = args
	p.pending = nil
	p.seq = 0
	p.lastFlushAt, p.lastFlushErr, p.lastErr = "", "", ""
	p.lastFlushOK = true
	p.backoff, p.retryAfter, p.lastReacq = 0, time.Time{}, time.Time{}
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
	// Proactive token refresh: armed from pubExp (~70-80% of TTL), re-armed after
	// each fire. Swaps the token in place BEFORE it expires - keeps stream_id.
	refresh := time.NewTimer(refreshNever)
	defer refresh.Stop()
	p.armRefresh(refresh)

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
		case <-refresh.C:
			_ = p.refreshToken(ctx)
			p.armRefresh(refresh)
		}
	}
}

// armRefresh (re)schedules the proactive refresh timer from the stored pubExp.
// Leaves the timer un-armed when the delay is 0 (no/expired/unparseable expiry) -
// the 401-grace recovery path is the safety net then.
func (p *Publisher) armRefresh(t *time.Timer) {
	if d := p.nextRefreshDelay(); d > 0 {
		t.Reset(d)
	}
}

// nextRefreshDelay computes the proactive-refresh delay from the current pubExp.
// 0 when not live or the expiry is unusable.
func (p *Publisher) nextRefreshDelay() time.Duration {
	p.mu.Lock()
	exp, live := p.pubExp, p.live
	p.mu.Unlock()
	if !live {
		return 0
	}
	return refreshDelay(exp, time.Now())
}

// refreshDelay returns how long to wait before proactively refreshing, given the
// token's absolute expiry (RFC3339): ~70-80% of the remaining lifetime, jittered.
// 0 when the expiry is empty/unparseable/already-expired (caller skips; the
// 401-grace path covers expiry). Floors at 1s so a tiny TTL never busy-loops.
func refreshDelay(expiresAt string, now time.Time) time.Duration {
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return 0
	}
	life := exp.Sub(now)
	if life <= 0 {
		return 0
	}
	frac := refreshFracMin + rand.Float64()*(refreshFracMax-refreshFracMin)
	d := time.Duration(float64(life) * frac)
	if d < time.Second {
		d = time.Second
	}
	return d
}

// refreshToken mints a fresh publish token for the SAME stream (proactively from
// the run-loop timer, or reactively from a 401) and swaps it in place - stream_id
// unchanged. Returns nil on success, else the error (for the 401 fallback).
func (p *Publisher) refreshToken(ctx context.Context) error {
	p.mu.Lock()
	if !p.live {
		p.mu.Unlock()
		return nil
	}
	id, token := p.streamID, p.pubToken
	p.mu.Unlock()
	if id == "" {
		return nil
	}
	resp, err := p.api.RefreshPublishToken(ctx, id, token)
	if err != nil {
		p.mu.Lock()
		p.lastErr = "publish token refresh failed: " + err.Error()
		p.mu.Unlock()
		if n, ok := p.refreshGate.Should(err.Error(), 5*time.Minute); ok {
			f := map[string]any{"error": err.Error(), "streamId": id}
			if n > 0 {
				f["suppressed"] = n
			}
			p.log.Warn(source, "publish token refresh failed", f)
		}
		return err
	}
	p.mu.Lock()
	if !p.live { // ended while refreshing
		p.mu.Unlock()
		return nil
	}
	p.pubToken, p.pubExp = resp.PublishToken, resp.PublishTokenExpiresAt // stream_id unchanged
	p.lastErr = ""
	p.mu.Unlock()
	p.refreshGate.Reset()
	p.log.Info(source, "publish token refreshed", map[string]any{"streamId": id, "expires": resp.PublishTokenExpiresAt})
	p.broadcast()
	return nil
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
	if len(p.pending) >= pendingMax { // cap: drop-oldest (see pendingMax)
		copy(p.pending, p.pending[1:])
		p.pending = p.pending[:pendingMax-1]
	}
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
	if !p.live || p.streamID == "" || len(p.pending) == 0 || time.Now().Before(p.retryAfter) {
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
		p.mu.Unlock()
		p.onPublishError(ctx, err, "ingest")
		p.broadcast()
		return
	}
	p.lastFlushOK = true
	p.lastFlushErr = ""
	p.backoff, p.retryAfter = 0, time.Time{}
	p.mu.Unlock()
	p.flushGate.Reset()
	p.broadcast()
}

func (p *Publisher) heartbeat(ctx context.Context) {
	p.mu.Lock()
	id, token, live := p.streamID, p.pubToken, p.live
	paused := time.Now().Before(p.retryAfter)
	p.mu.Unlock()
	if !live || id == "" || paused {
		return
	}
	if err := p.api.Heartbeat(ctx, id, token); err != nil {
		// 30s cadence: a down backend would warn every beat for the whole set.
		if n, ok := p.hbGate.Should(err.Error(), 5*time.Minute); ok {
			f := map[string]any{"error": err.Error()}
			if n > 0 {
				f["suppressed"] = n
			}
			p.log.Warn(source, "heartbeat failed", f)
		}
		p.onPublishError(ctx, err, "heartbeat")
		return
	}
	p.hbGate.Reset()
}

// onPublishError applies the retry discipline after a failed publish-token call:
// 401 = token expired mid-set → recover in place (refresh, then re-create as a
// last resort); 429 (and any other status/transport error) → exponential backoff
// so the flush/heartbeat tickers stop re-hitting the server at full cadence.
// Success elsewhere clears the backoff.
func (p *Publisher) onPublishError(ctx context.Context, err error, op string) {
	code := api.StatusCode(err)
	if code == 401 {
		p.recover401(ctx)
		return
	}
	p.mu.Lock()
	p.bumpBackoffLocked()
	pause := p.backoff
	p.mu.Unlock()
	if n, ok := p.flushGate.Should(err.Error(), 5*time.Minute); ok {
		f := map[string]any{"op": op, "error": err.Error(), "pause": pause.String()}
		if code != 0 {
			f["status"] = code
		}
		if n > 0 {
			f["suppressed"] = n
		}
		p.log.Warn(source, "publish failed - backing off", f)
	}
}

// bumpBackoffLocked doubles the retry pause (base→max cap) and arms retryAfter. mu held.
func (p *Publisher) bumpBackoffLocked() {
	if p.backoff == 0 {
		p.backoff = retryBackoffBase
	} else if p.backoff < retryBackoffMax {
		p.backoff *= 2
		if p.backoff > retryBackoffMax {
			p.backoff = retryBackoffMax
		}
	}
	p.retryAfter = time.Now().Add(p.backoff)
}

// recover401 handles a mid-set publish-token expiry (401). Rate-limited to
// reacquireMin so a persistent 401 can't hammer the server. FIRST tries an
// in-place refresh (keeps stream_id; the server grace window accepts a just-
// expired token) - the set-fragmentation fix. Only if refresh fails (expired
// beyond grace, superseded, stream ended, transport, or an older backend without
// the route) does it fall back to CreateStream re-acquire (new stream_id, last
// resort). A rate-limited attempt arms the backoff instead.
func (p *Publisher) recover401(ctx context.Context) {
	p.mu.Lock()
	if !p.live {
		p.mu.Unlock()
		return
	}
	if time.Since(p.lastReacq) < reacquireMin {
		p.bumpBackoffLocked() // still 401ing right after a recovery: pause instead of hammering
		p.mu.Unlock()
		return
	}
	p.lastReacq = time.Now()
	p.mu.Unlock()

	if err := p.refreshToken(ctx); err == nil {
		p.mu.Lock()
		p.backoff, p.retryAfter = 0, time.Time{}
		p.mu.Unlock()
		p.broadcast()
		return
	}
	p.reacquire(ctx)
}

// reacquire re-runs CreateStream with the stored user token when an in-place
// refresh could not recover a 401. The server auto-ends the prior active stream
// and mints a fresh stream+token; we swap ids in place and keep publishing. The
// caller (recover401) owns the rate-limit; a failed attempt backs off.
func (p *Publisher) reacquire(ctx context.Context) {
	p.mu.Lock()
	if !p.live {
		p.mu.Unlock()
		return
	}
	args := p.args
	p.mu.Unlock()

	resp, err := p.api.CreateStream(ctx, args.UserToken, createReq(args))
	p.mu.Lock()
	if err != nil {
		p.lastErr = "publish token re-acquire failed: " + err.Error()
		p.bumpBackoffLocked()
		pause := p.backoff
		p.mu.Unlock()
		p.log.Warn(source, "publish token re-acquire failed", map[string]any{"error": err.Error(), "pause": pause.String()})
		return
	}
	if !p.live { // ended while re-acquiring: end the fresh stream, don't adopt it
		p.mu.Unlock()
		_ = p.api.EndStream(ctx, resp.StreamID, resp.PublishToken)
		return
	}
	old := p.streamID
	p.streamID, p.pubToken, p.pubExp = resp.StreamID, resp.PublishToken, resp.PublishTokenExpiresAt
	p.backoff, p.retryAfter = 0, time.Time{}
	p.mu.Unlock()
	p.log.Info(source, "publish token re-acquired", map[string]any{"oldStreamId": old, "streamId": resp.StreamID, "expires": resp.PublishTokenExpiresAt})
	p.broadcast()
}

// createReq shapes the CreateStream request from args (shared by Start + reacquire).
func createReq(args StartArgs) api.CreateStreamReq {
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
	return api.CreateStreamReq{Title: args.Title, Kind: kind, Source: src, Metadata: meta}
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
	p.args = StartArgs{} // user token leaves memory with the stream
	p.pending = nil
	p.seq = 0
	p.backoff, p.retryAfter, p.lastReacq = 0, time.Time{}, time.Time{}
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
