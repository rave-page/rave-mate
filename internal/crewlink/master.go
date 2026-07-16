package crewlink

// master.go - the crew MASTER side: joins the event's relay room as role=master, answers the
// nodes' sync pings (the clock-discipline responder - master wall clock, the same domain the
// ingest clamp and mocapnode.Packet.CapturedAt use), and ingests directed pose frames:
//
//	decode → clamp capturedAtNs to [now−2s, now+250ms] → drop+count outliers (never fatal)
//	→ rebuild mocapnode.Packet (Quats/Present nil - the store recomputes) → inject
//
// The inject seam feeds the ONE persistent mocapmaster.Master (mocap.Service.Inject);
// election stays master-side, keyed (sourceTag, sessionNonce), exactly as today.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mocapnode"
)

// Ingest clamp window (contract §5).
const (
	clampPast   = 2 * time.Second
	clampFuture = 250 * time.Millisecond
)

// pongQueueCap bounds the sync-pong reply backlog. Tiny on purpose: probes repeat every 2s
// per node, so an overflowed (dropped) pong just means the node re-probes.
const pongQueueCap = 8

// pongJob is one queued sync-ping answer: T1 echoed from the ping, T2 stamped at receive.
// T3 stamps at actual send (pongPump) so queue wait lives inside (T3−T2) and cancels out of
// the node's offset/RTT math.
type pongJob struct {
	to string
	id uint32
	t1 int64
	t2 int64
}

// MasterConfig wires a MasterLink. Client, EventID and Inject are mandatory.
type MasterConfig struct {
	Client  *Client
	EventID string
	Label   string

	// Inject feeds one clamped packet into the persistent master. false = dropped (e.g. the
	// mocap module is off); counted in health, never fatal.
	Inject func(pkt mocapnode.Packet) bool

	Now   func() time.Time                 // clock seam for tests; nil = time.Now
	Logf  func(format string, args ...any) // optional informational log sink; never logs payloads
	Warnf func(format string, args ...any) // optional anomaly log sink; nil = Logf
}

// MasterLink runs the master link. Run once.
type MasterLink struct {
	cfg MasterConfig
	seq atomic.Int64

	mu        sync.Mutex
	sid       string
	nodes     map[string]Member
	pongs     chan pongJob // per-session pong queue (replaced on each join)
	kicked    bool
	joins     uint64
	frames    uint64 // pose frames received
	injected  uint64 // accepted into the store
	clamped   uint64 // capturedAt outside the clamp window
	dropped   uint64 // inject refused (mocap module off) or undecodable
	pongDrops uint64 // sync pongs dropped on queue overflow (node re-probes)
	lastErr   string
}

// NewMaster builds a MasterLink.
func NewMaster(cfg MasterConfig) *MasterLink {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Warnf == nil {
		cfg.Warnf = cfg.Logf
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &MasterLink{cfg: cfg, nodes: map[string]Member{}}
}

// Run supervises the link until ctx ends (join → stream → 1s→30s backoff → re-join).
// Blocking; run on its own goroutine.
func (m *MasterLink) Run(ctx context.Context) {
	backoff := time.Second
	var gate logbus.Gate // reconnect spam: one warn per failure kind per reconnectLogEvery
	for ctx.Err() == nil {
		err := m.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			m.setErr(err.Error())
			if sup, ok := gate.Should(gateKey(err), reconnectLogEvery); ok {
				if sup > 0 {
					m.cfg.Warnf("crewlink: master session ended; reconnecting (%v) [%d repeats suppressed]", err, sup)
				} else {
					m.cfg.Warnf("crewlink: master session ended; reconnecting (%v)", err)
				}
			}
		} else {
			gate.Reset()
			backoff = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (m *MasterLink) session(ctx context.Context) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	res, err := m.cfg.Client.Join(sctx, m.cfg.EventID, RoleMaster, "", m.cfg.Label)
	if err != nil {
		return err
	}
	pongs := make(chan pongJob, pongQueueCap)
	m.mu.Lock()
	m.sid, m.kicked, m.lastErr = res.SID, false, ""
	m.pongs = pongs
	m.joins++
	m.nodes = map[string]Member{}
	for _, mem := range res.Members {
		if mem.Role == RoleNode {
			m.nodes[mem.SID] = mem
		}
	}
	m.mu.Unlock()
	m.cfg.Logf("crewlink: master joined room (members=%d)", len(res.Members))

	defer func() {
		m.mu.Lock()
		sid := m.sid
		m.sid = ""
		m.pongs = nil // pump is gone with sctx; late pings must not count phantom drops
		m.nodes = map[string]Member{}
		m.mu.Unlock()
		if sid != "" {
			lctx, lcancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = m.cfg.Client.Leave(lctx, sid)
			lcancel()
		}
	}()

	go m.guard(func() { m.heartbeat(sctx, res.SID, cancel) })
	go m.guard(func() { m.pongPump(sctx, res.SID, pongs) })

	err = m.cfg.Client.Stream(sctx, res.SID, StreamHandlers{
		OnRelay:    func(from string, _ int64, payload []byte) { m.onRelay(from, payload) },
		OnPresence: func(p Presence) { m.onPresence(p, res.SID, cancel) },
	})
	m.mu.Lock()
	kicked := m.kicked
	m.mu.Unlock()
	if kicked {
		return errKicked
	}
	if ctx.Err() == nil && sctx.Err() != nil {
		return ErrSessionGone
	}
	return err
}

func (m *MasterLink) heartbeat(ctx context.Context, sid string, cancel context.CancelFunc) {
	t := time.NewTicker(HeartbeatEach)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			hctx, hcancel := context.WithTimeout(ctx, 10*time.Second)
			err := m.cfg.Client.Heartbeat(hctx, sid)
			hcancel()
			if errors.Is(err, ErrSessionGone) || errors.Is(err, ErrUnauthorized) {
				m.cfg.Logf("crewlink: master heartbeat rejected - session over")
				cancel()
				return
			}
		}
	}
}

// onRelay handles a downstream frame: pose → clamp+inject; sync ping → queued pong. T2
// (receive stamp) is taken first so queueing inside this process doesn't inflate the nodes'
// RTT split. Runs on the SSE pump goroutine (StreamHandlers must be cheap): the pong reply is
// a synchronous HTTP Send, so it goes through a bounded queue drained by pongPump - a slow
// relay must never stall pose ingest.
func (m *MasterLink) onRelay(from string, payload []byte) {
	t2 := m.cfg.Now().UnixNano()
	switch frameType(payload) {
	case FrameTypePose:
		m.ingestPose(payload)
	case FrameTypeSync:
		var sf SyncFrame
		if json.Unmarshal(payload, &sf) != nil || sf.IsPong() {
			return
		}
		m.mu.Lock()
		pongs := m.pongs
		m.mu.Unlock()
		if pongs == nil {
			return
		}
		select {
		case pongs <- pongJob{to: from, id: sf.ID, t1: sf.T1, t2: t2}:
		default:
			// Overflow drop-newest: sync probes repeat every 2s, the node just re-probes.
			m.mu.Lock()
			m.pongDrops++
			m.mu.Unlock()
		}
	}
}

// pongPump answers queued sync pings off the stream goroutine, bound to the session ctx.
// T3 stamps at actual send so the queue wait lives inside (T3−T2) and cancels out of the
// node's offset/RTT computation.
func (m *MasterLink) pongPump(ctx context.Context, sid string, jobs <-chan pongJob) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-jobs:
			pong := SyncFrame{T: FrameTypeSync, ID: j.id, T1: j.t1, T2: j.t2, T3: m.cfg.Now().UnixNano()}
			b, err := json.Marshal(pong)
			if err != nil {
				continue
			}
			_ = m.cfg.Client.Send(ctx, sid, j.to, m.seq.Add(1), b) // loss-tolerant: the node re-probes
		}
	}
}

// ingestPose decodes + clamps one pose frame and feeds it to the persistent master. Every
// reject is a counter, never an error - a rotten node degrades, the master keeps rendering.
func (m *MasterLink) ingestPose(payload []byte) {
	m.mu.Lock()
	m.frames++
	m.mu.Unlock()

	var pf PoseFrame
	if json.Unmarshal(payload, &pf) != nil {
		m.mu.Lock()
		m.dropped++
		m.mu.Unlock()
		return
	}
	now := m.cfg.Now()
	ns := pf.CapturedAtNs
	if ns < now.Add(-clampPast).UnixNano() || ns > now.Add(clampFuture).UnixNano() {
		m.mu.Lock()
		m.clamped++
		m.mu.Unlock()
		return // stale/future stamp: the node's clock isn't disciplined (yet) - drop, never fatal
	}
	if m.cfg.Inject(pf.Packet()) {
		m.mu.Lock()
		m.injected++
		m.mu.Unlock()
	} else {
		m.mu.Lock()
		m.dropped++
		m.mu.Unlock()
	}
}

func (m *MasterLink) onPresence(p Presence, ownSID string, cancel context.CancelFunc) {
	if p.SID == ownSID {
		if p.Type == "kick" || p.Type == "leave" {
			logf := m.cfg.Logf
			if p.Type == "kick" {
				logf = m.cfg.Warnf // deliberate removal = anomaly; leave is routine
			}
			logf("crewlink: master session dropped by server (%s)", p.Type)
			m.mu.Lock()
			m.kicked = p.Type == "kick"
			m.mu.Unlock()
			cancel()
		}
		return
	}
	if p.Role != RoleNode {
		return
	}
	m.mu.Lock()
	switch p.Type {
	case "join":
		m.nodes[p.SID] = Member{SID: p.SID, UserID: p.UserID, Role: p.Role, Tier: p.Tier, Label: p.Label}
	case "leave", "kick":
		delete(m.nodes, p.SID)
	}
	m.mu.Unlock()
}

func (m *MasterLink) setErr(msg string) {
	m.mu.Lock()
	m.lastErr = msg
	m.mu.Unlock()
}

func (m *MasterLink) guard(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			m.cfg.Warnf("crewlink: master goroutine panicked: %v", r)
		}
	}()
	fn()
}

// MasterStatus is the master link's live snapshot.
type MasterStatus struct {
	SID       string
	Nodes     int
	Joins     uint64
	Frames    uint64 // pose frames received
	Injected  uint64 // accepted into the store
	Clamped   uint64 // capturedAt outside [now−2s, now+250ms]
	Dropped   uint64 // undecodable or inject refused
	PongDrops uint64 // sync pongs dropped on queue overflow (the node re-probes)
	LastErr   string
}

// Status snapshots the master link.
func (m *MasterLink) Status() MasterStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return MasterStatus{
		SID: m.sid, Nodes: len(m.nodes), Joins: m.joins,
		Frames: m.frames, Injected: m.injected, Clamped: m.clamped, Dropped: m.dropped,
		PongDrops: m.pongDrops,
		LastErr:   m.lastErr,
	}
}
