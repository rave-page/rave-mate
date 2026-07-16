package crewlink

// node.go - the crew NODE side: uplinks this machine's decoded mocap packets to every master
// present in the event's relay room, stamped in the MASTER'S clock domain (contract §5).
//
//   packet sink → bounded queue (drop-newest) → uplink goroutine → directed pose frames
//   sync initiator: burst 8 probes 250ms apart on join, then every 2s, disciplining a
//   medialink.SoftwareClock against the elected sync master (NTP-style offset/RTT filter -
//   the router.go pattern, no SNTP client).
//
// Supervision mirrors bridge/manager.go: join → stream → on break, back off 1s doubling to a
// 30s cap and re-join. Loss is fine (poses are idempotent state); revocation surfaces as a
// heartbeat/send 404 (ErrSessionGone) or a kick, both of which end the session.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mocapnode"
)

// Sync cadence (contract §5): burst on join, then steady.
const (
	defaultBurstProbes = 8
	defaultBurstEvery  = 250 * time.Millisecond
	defaultSteadyEvery = 2 * time.Second
	defaultQueueCap    = 8 // uplink backlog; one frame supersedes the last, so keep it tight
)

// errKicked ends a session that was kicked (ctrl kick / presence kick). The supervisor
// re-joins; a genuinely revoked member then 404s at join and keeps backing off.
var errKicked = errors.New("crewlink: kicked from the room")

// reconnectLogEvery caps reconnect-warning spam: one message per failure KIND per interval
// (the bridge/manager.go logbus.Gate pattern); a kind change re-emits immediately.
const reconnectLogEvery = 10 * time.Minute

// gateKey buckets session errors into stable keys so the reconnect log gate re-emits on a
// failure-kind TRANSITION, not on cosmetic message differences.
func gateKey(err error) string {
	switch {
	case errors.Is(err, ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, errKicked):
		return "kicked"
	case errors.Is(err, ErrSessionGone):
		return "session-gone"
	default:
		return "error"
	}
}

// NodeConfig wires a Node. Client + EventID are mandatory.
type NodeConfig struct {
	Client  *Client
	EventID string
	Tier    string // informational rig tier; "" = "panel"
	Label   string

	// Test seams (0 = contract defaults).
	QueueCap    int
	BurstProbes int
	BurstEvery  time.Duration
	SteadyEvery time.Duration

	Logf  func(format string, args ...any) // optional informational log sink; never logs payloads
	Warnf func(format string, args ...any) // optional anomaly log sink; nil = Logf
}

// Node runs the node link. Run once; Enqueue is safe from any goroutine at any time.
type Node struct {
	cfg   NodeConfig
	clock *medialink.SoftwareClock
	queue chan mocapnode.Packet
	seq   atomic.Int64 // per-process send seq (server relays it verbatim)

	mu       sync.Mutex
	sid      string
	masters  map[string]Member
	syncSID  string        // elected sync master (discipline source)
	lastSync string        // previous discipline source (detects domain changes across elections)
	reburst  chan struct{} // signals the sync loop to re-burst (new/changed target)
	kicked   bool
	joins    uint64 // sessions established (observability)
	sent     uint64 // pose frames uplinked (per-master sends counted once per frame)
	dropped  uint64 // queue drop-newest + no-master drops
	sendErrs uint64
	lastErr  string
}

// NewNode builds a Node.
func NewNode(cfg NodeConfig) *Node {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Warnf == nil {
		cfg.Warnf = cfg.Logf
	}
	if cfg.Tier == "" {
		cfg.Tier = "panel"
	}
	if cfg.QueueCap <= 0 {
		cfg.QueueCap = defaultQueueCap
	}
	if cfg.BurstProbes <= 0 {
		cfg.BurstProbes = defaultBurstProbes
	}
	if cfg.BurstEvery <= 0 {
		cfg.BurstEvery = defaultBurstEvery
	}
	if cfg.SteadyEvery <= 0 {
		cfg.SteadyEvery = defaultSteadyEvery
	}
	return &Node{
		cfg:     cfg,
		clock:   medialink.NewSoftwareClock(),
		queue:   make(chan mocapnode.Packet, cfg.QueueCap),
		masters: map[string]Member{},
		reburst: make(chan struct{}, 1),
	}
}

// Enqueue accepts one decoded packet for uplink. Non-blocking: a full queue drops the NEWEST
// packet (the backlog already supersedes it by the time it would send).
func (n *Node) Enqueue(pkt mocapnode.Packet) {
	select {
	case n.queue <- pkt:
	default:
		n.mu.Lock()
		n.dropped++
		n.mu.Unlock()
	}
}

// Run supervises the link until ctx ends: join → stream → back off (1s doubling to 30s) →
// re-join. Blocking; run on its own goroutine.
func (n *Node) Run(ctx context.Context) {
	backoff := time.Second
	var gate logbus.Gate // reconnect spam: one warn per failure kind per reconnectLogEvery
	for ctx.Err() == nil {
		err := n.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			n.setErr(err.Error())
			if sup, ok := gate.Should(gateKey(err), reconnectLogEvery); ok {
				if sup > 0 {
					n.cfg.Warnf("crewlink: node session ended; reconnecting (%v) [%d repeats suppressed]", err, sup)
				} else {
					n.cfg.Warnf("crewlink: node session ended; reconnecting (%v)", err)
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

// session joins the room, runs heartbeat/sync/uplink, and serves the stream until it breaks.
func (n *Node) session(ctx context.Context) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()

	res, err := n.cfg.Client.Join(sctx, n.cfg.EventID, RoleNode, n.cfg.Tier, n.cfg.Label)
	if err != nil {
		return err
	}
	n.mu.Lock()
	n.sid, n.kicked, n.lastErr = res.SID, false, ""
	n.joins++
	n.masters = map[string]Member{}
	for _, m := range res.Members {
		if m.Role == RoleMaster && m.SID != res.SID {
			n.masters[m.SID] = m
		}
	}
	n.electSyncLocked()
	masters := len(n.masters)
	n.mu.Unlock()
	n.cfg.Logf("crewlink: node joined room (masters=%d)", masters)

	defer func() {
		n.mu.Lock()
		sid := n.sid
		n.sid, n.syncSID = "", ""
		n.masters = map[string]Member{}
		n.mu.Unlock()
		if sid != "" { // best-effort; a dead session 404s harmlessly
			lctx, lcancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = n.cfg.Client.Leave(lctx, sid)
			lcancel()
		}
	}()

	go n.guard(func() { n.heartbeat(sctx, res.SID, cancel) })
	go n.guard(func() { n.syncLoop(sctx, res.SID) })
	go n.guard(func() { n.uplink(sctx, res.SID, cancel) })

	err = n.cfg.Client.Stream(sctx, res.SID, StreamHandlers{
		OnRelay:    func(from string, _ int64, payload []byte) { n.onRelay(from, payload, cancel) },
		OnPresence: func(p Presence) { n.onPresence(p, res.SID, cancel) },
	})
	n.mu.Lock()
	kicked := n.kicked
	n.mu.Unlock()
	if kicked {
		return errKicked
	}
	if ctx.Err() == nil && sctx.Err() != nil {
		return ErrSessionGone // a sub-loop cancelled us (heartbeat 404 / send session-gone)
	}
	return err
}

// heartbeat refreshes the session TTL every 25s; it is also the revocation surface - a 404
// means the crew-check failed (revoked) or the session TTLed out → end the session.
func (n *Node) heartbeat(ctx context.Context, sid string, cancel context.CancelFunc) {
	t := time.NewTicker(HeartbeatEach)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			hctx, hcancel := context.WithTimeout(ctx, 10*time.Second)
			err := n.cfg.Client.Heartbeat(hctx, sid)
			hcancel()
			if errors.Is(err, ErrSessionGone) || errors.Is(err, ErrUnauthorized) {
				n.cfg.Logf("crewlink: node heartbeat rejected - session over")
				cancel()
				return
			}
		}
	}
}

// uplink drains the packet queue into directed pose frames, one per master present.
func (n *Node) uplink(ctx context.Context, sid string, cancel context.CancelFunc) {
	for {
		select {
		case <-ctx.Done():
			return
		case pkt := <-n.queue:
			n.mu.Lock()
			targets := make([]string, 0, len(n.masters))
			for msid := range n.masters {
				targets = append(targets, msid)
			}
			n.mu.Unlock()
			if len(targets) == 0 {
				n.mu.Lock()
				n.dropped++
				n.mu.Unlock()
				continue
			}
			frame := PoseFromPacket(pkt, n.stampNs(pkt))
			b, err := json.Marshal(frame)
			if err != nil {
				continue
			}
			for _, to := range targets {
				err := n.cfg.Client.Send(ctx, sid, to, n.seq.Add(1), b)
				switch {
				case err == nil:
				case errors.Is(err, ErrSessionGone), errors.Is(err, ErrUnauthorized):
					cancel()
					return
				default:
					// Rate limit / unknown peer / transient: poses tolerate loss - count and go on.
					n.mu.Lock()
					n.sendErrs++
					n.mu.Unlock()
				}
			}
			n.mu.Lock()
			n.sent++
			n.mu.Unlock()
		}
	}
}

// stampNs converts a packet's local capture time into the master clock domain: the
// disciplined clock's now minus the capture→send age (contract §5).
func (n *Node) stampNs(pkt mocapnode.Packet) int64 {
	return n.clock.Now() - time.Since(pkt.CapturedAt).Nanoseconds()
}

// syncLoop probes the elected sync master: a burst on (re)join/target change, then steady
// cadence. Pongs are handled by onRelay → the clock filter.
func (n *Node) syncLoop(ctx context.Context, sid string) {
	var id uint32
	probe := func() {
		n.mu.Lock()
		target := n.syncSID
		n.mu.Unlock()
		if target == "" {
			return
		}
		id++
		ping := SyncFrame{T: FrameTypeSync, ID: id, T1: n.clock.Now()}
		b, err := json.Marshal(ping)
		if err != nil {
			return
		}
		_ = n.cfg.Client.Send(ctx, sid, target, n.seq.Add(1), b) // loss-tolerant
	}
	burst := func() {
		for i := 0; i < n.cfg.BurstProbes; i++ {
			probe()
			select {
			case <-ctx.Done():
				return
			case <-time.After(n.cfg.BurstEvery):
			}
		}
	}
	burst()
	t := time.NewTicker(n.cfg.SteadyEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-n.reburst:
			burst()
		case <-t.C:
			probe()
		}
	}
}

// onRelay handles a downstream frame: sync pongs discipline the clock; ctrl kick ends the
// session. T4 is stamped first - it anchors the RTT measurement.
func (n *Node) onRelay(from string, payload []byte, cancel context.CancelFunc) {
	t4 := n.clock.Now()
	switch frameType(payload) {
	case FrameTypeSync:
		var sf SyncFrame
		if json.Unmarshal(payload, &sf) != nil || !sf.IsPong() {
			return
		}
		n.mu.Lock()
		target := n.syncSID
		n.mu.Unlock()
		if from != target {
			return // telemetry-grade only; discipline is pinned to the elected master
		}
		offset := ((sf.T2 - sf.T1) + (sf.T3 - t4)) / 2
		rtt := (t4 - sf.T1) - (sf.T3 - sf.T2)
		if rtt < 0 {
			return // clock stepped mid-probe; discard
		}
		n.clock.AddSample(offset, rtt)
	case FrameTypeCtrl:
		var cf CtrlFrame
		if json.Unmarshal(payload, &cf) != nil {
			return
		}
		if cf.Op == CtrlOpKick {
			n.cfg.Warnf("crewlink: node kicked by master ctrl")
			n.mu.Lock()
			n.kicked = true
			n.mu.Unlock()
			cancel()
		}
		// ctrl config is informational for a node (capture geometry is local config).
	}
}

// onPresence tracks the room's masters and reacts to our own kick.
func (n *Node) onPresence(p Presence, ownSID string, cancel context.CancelFunc) {
	if p.SID == ownSID {
		if p.Type == "kick" || p.Type == "leave" {
			logf := n.cfg.Logf
			if p.Type == "kick" {
				logf = n.cfg.Warnf // deliberate removal = anomaly; leave is routine
			}
			logf("crewlink: node session dropped by server (%s)", p.Type)
			n.mu.Lock()
			n.kicked = p.Type == "kick"
			n.mu.Unlock()
			cancel()
		}
		return
	}
	if p.Role != RoleMaster {
		return
	}
	n.mu.Lock()
	switch p.Type {
	case "join":
		n.masters[p.SID] = Member{SID: p.SID, UserID: p.UserID, Role: p.Role, Tier: p.Tier, Label: p.Label}
	case "leave", "kick":
		delete(n.masters, p.SID)
	}
	n.electSyncLocked()
	n.mu.Unlock()
}

// electSyncLocked keeps the current sync master while present; otherwise picks any master and
// signals a re-burst (fresh discipline against the new clock domain). Callers hold n.mu.
func (n *Node) electSyncLocked() {
	if _, ok := n.masters[n.syncSID]; ok && n.syncSID != "" {
		return
	}
	n.syncSID = ""
	for sid := range n.masters {
		n.syncSID = sid
		break
	}
	if n.syncSID == "" {
		return
	}
	if n.lastSync != "" && n.lastSync != n.syncSID {
		// Master failover: the old master's min-RTT samples would pin the clock to its
		// domain for up to 60s. Fresh estimator; same SoftwareClock → slew, not step.
		n.clock.Resync()
	}
	n.lastSync = n.syncSID
	select {
	case n.reburst <- struct{}{}:
	default:
	}
}

func (n *Node) setErr(msg string) {
	n.mu.Lock()
	n.lastErr = msg
	n.mu.Unlock()
}

// guard runs fn with a panic trap (a relay hiccup must never kill the daemon).
func (n *Node) guard(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			n.cfg.Warnf("crewlink: node goroutine panicked: %v", r)
		}
	}()
	fn()
}

// NodeStatus is the node link's live snapshot.
type NodeStatus struct {
	SID      string
	Masters  int
	Joins    uint64
	Sent     uint64 // pose frames uplinked
	Dropped  uint64 // queue drop-newest + no-master drops
	SendErrs uint64
	Locked   bool  // clock discipline lock
	OffsetNs int64 // applied clock slew
	LastErr  string
}

// Status snapshots the node link.
func (n *Node) Status() NodeStatus {
	n.mu.Lock()
	st := NodeStatus{
		SID: n.sid, Masters: len(n.masters), Joins: n.joins,
		Sent: n.sent, Dropped: n.dropped, SendErrs: n.sendErrs, LastErr: n.lastErr,
	}
	n.mu.Unlock()
	q := n.clock.Quality()
	st.Locked, st.OffsetNs = q.Locked, q.OffsetNs
	return st
}
