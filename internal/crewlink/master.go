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

	"rave.page/mate/internal/mocapnode"
)

// Ingest clamp window (contract §5).
const (
	clampPast   = 2 * time.Second
	clampFuture = 250 * time.Millisecond
)

// MasterConfig wires a MasterLink. Client, EventID and Inject are mandatory.
type MasterConfig struct {
	Client  *Client
	EventID string
	Label   string

	// Inject feeds one clamped packet into the persistent master. false = dropped (e.g. the
	// mocap module is off); counted in health, never fatal.
	Inject func(pkt mocapnode.Packet) bool

	Now  func() time.Time                 // clock seam for tests; nil = time.Now
	Logf func(format string, args ...any) // optional log sink; never logs payloads
}

// MasterLink runs the master link. Run once.
type MasterLink struct {
	cfg MasterConfig
	seq atomic.Int64

	mu       sync.Mutex
	sid      string
	nodes    map[string]Member
	kicked   bool
	joins    uint64
	frames   uint64 // pose frames received
	injected uint64 // accepted into the store
	clamped  uint64 // capturedAt outside the clamp window
	dropped  uint64 // inject refused (mocap module off) or undecodable
	lastErr  string
}

// NewMaster builds a MasterLink.
func NewMaster(cfg MasterConfig) *MasterLink {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
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
	for ctx.Err() == nil {
		err := m.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			m.setErr(err.Error())
			m.cfg.Logf("crewlink: master session ended; reconnecting (%v)", err)
		} else {
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
	m.mu.Lock()
	m.sid, m.kicked, m.lastErr = res.SID, false, ""
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
		m.nodes = map[string]Member{}
		m.mu.Unlock()
		if sid != "" {
			lctx, lcancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = m.cfg.Client.Leave(lctx, sid)
			lcancel()
		}
	}()

	go m.guard(func() { m.heartbeat(sctx, res.SID, cancel) })

	err = m.cfg.Client.Stream(sctx, res.SID, StreamHandlers{
		OnRelay:    func(from string, _ int64, payload []byte) { m.onRelay(sctx, from, payload) },
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

// onRelay handles a downstream frame: pose → clamp+inject; sync ping → pong. T2 (receive
// stamp) is taken first so queueing inside this process doesn't inflate the nodes' RTT split.
func (m *MasterLink) onRelay(ctx context.Context, from string, payload []byte) {
	t2 := m.cfg.Now().UnixNano()
	switch frameType(payload) {
	case FrameTypePose:
		m.ingestPose(payload)
	case FrameTypeSync:
		var sf SyncFrame
		if json.Unmarshal(payload, &sf) != nil || sf.IsPong() {
			return
		}
		pong := SyncFrame{T: FrameTypeSync, ID: sf.ID, T1: sf.T1, T2: t2, T3: m.cfg.Now().UnixNano()}
		b, err := json.Marshal(pong)
		if err != nil {
			return
		}
		m.mu.Lock()
		sid := m.sid
		m.mu.Unlock()
		if sid == "" {
			return
		}
		_ = m.cfg.Client.Send(ctx, sid, from, m.seq.Add(1), b) // loss-tolerant: the node re-probes
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
			m.cfg.Logf("crewlink: master session dropped by server (%s)", p.Type)
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
			m.cfg.Logf("crewlink: master goroutine panicked: %v", r)
		}
	}()
	fn()
}

// MasterStatus is the master link's live snapshot.
type MasterStatus struct {
	SID      string
	Nodes    int
	Joins    uint64
	Frames   uint64 // pose frames received
	Injected uint64 // accepted into the store
	Clamped  uint64 // capturedAt outside [now−2s, now+250ms]
	Dropped  uint64 // undecodable or inject refused
	LastErr  string
}

// Status snapshots the master link.
func (m *MasterLink) Status() MasterStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return MasterStatus{
		SID: m.sid, Nodes: len(m.nodes), Joins: m.joins,
		Frames: m.frames, Injected: m.injected, Clamped: m.clamped, Dropped: m.dropped,
		LastErr: m.lastErr,
	}
}
