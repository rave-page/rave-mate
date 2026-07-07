package medialink

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// tcplane.go is the §4 timecode plane (P3): TC master election + ST 12M generation off the synced
// media clock. One node is TC master; its house frame clock is authoritative. Election: user-pinned
// master wins; else deterministic lowest-NodeID among clock-capable nodes (Caps.Clock adverts -
// BMCA is overkill for a small mesh, ties impossible on unique NodeIDs). The master publishes its
// TC anchor on TopicTC (~4 Hz + on jam/start/stop); slaves derive TC from their own (disciplined)
// media clock against that anchor. Master silence > tcStale → slaves freewheel on the last anchor
// (holdover, flagged) and re-elect. See MEDIALINK_DESIGN.md §4 + §12.

const (
	tcAnnounceEvery = 250 * time.Millisecond // §4 media.tc cadence (~4 Hz)
	tcStale         = 5 * time.Second        // master silence → holdover + re-election (§4)
)

// TCRole is a node's timecode-plane role.
type TCRole string

const (
	TCRoleMaster TCRole = "master" // this node generates + announces house TC
	TCRoleSlave  TCRole = "slave"  // following a paired instance's announces
)

// TCAnnounce is the master's house-clock state on TopicTC (§4 media.tc): the anchor pair
// (Frame, Anchor) lets a slave compute frame-now = Frame + (mediaclock − Anchor)·fps. Golden-
// pinned; additive changes only (P1/P2 peers never subscribe the topic - bus-compat by omission).
type TCAnnounce struct {
	Node    string `json:"node"`           // master node id
	Running bool   `json:"running"`        // house clock advancing (false = parked at Frame)
	Rate    int    `json:"rate"`           // nominal fps (24/25/30)
	Drop    bool   `json:"drop,omitempty"` // drop-frame counting
	Frame   int64  `json:"frame"`          // absolute ST 12M frame index at Anchor
	Anchor  int64  `json:"anchor_ns"`      // master media-clock ns at Frame
}

// TCSource samples the local house clock (the master-side generator seam).
type TCSource func() (frame int64, rate Rate, running bool)

// TCStatus is the plane's telemetry snapshot (§7; drives the Peers → Media panel + ctl status).
type TCStatus struct {
	Role     TCRole
	Master   string // elected master node id (== self when Role == master)
	Running  bool
	TC       Timecode
	Rate     Rate
	Holdover bool      // master announces went stale - freewheeling on the last anchor
	LastAt   time.Time // last accepted announce (slave side; zero = none yet)
}

// TCPlaneOptions configures a TCPlane. Self, Bus, Clock are required.
type TCPlaneOptions struct {
	Self  string
	Bus   Bus
	Clock ClockSource
	Log   Logger // optional

	// Master pins the TC master (D6: user-pinned wins; "" = auto lowest-NodeID). A pinned node
	// that was never seen clock-capable falls back to auto.
	Master string

	// OnMaster fires on every election change with the elected node id (the app retargets
	// clock-sync discipline: elected TC master = sync master, §2.3/D6). Called off-lock.
	OnMaster func(node string)
}

// TCPlane owns this node's timecode-plane role: candidate tracking, election, master announce
// loop, and slave follow/holdover state. Create with NewTCPlane, attach a source, then Start.
type TCPlane struct {
	self  string
	bus   Bus
	clock ClockSource
	log   Logger
	pin   string

	announceEvery time.Duration // test-tunable
	staleAfter    time.Duration // test-tunable

	onMaster func(string)

	mu      sync.Mutex
	source  TCSource
	chase   func(frame int64, rate Rate) // slave-side follower (timecode.Service jam glue)
	cands   map[string]bool              // clock-capable peers (Caps.Clock adverts / announcers)
	elected string
	ann     TCAnnounce // last accepted announce (freewheel anchor during holdover)
	annAt   time.Time
	annLoc  int64 // local media-clock ns at announce receipt (anchor when domains aren't shared)
	haveAnn bool
	unsub   []func()
	started bool
}

// NewTCPlane builds a timecode plane (does not subscribe - call Start).
func NewTCPlane(opts TCPlaneOptions) *TCPlane {
	return &TCPlane{
		self: opts.Self, bus: opts.Bus, clock: opts.Clock, log: opts.Log, pin: opts.Master,
		onMaster: opts.OnMaster, announceEvery: tcAnnounceEvery, staleAfter: tcStale,
		cands: map[string]bool{}, elected: opts.Self,
	}
}

// SetLocalSource attaches the house-clock sampler the master role announces from.
func (p *TCPlane) SetLocalSource(fn TCSource) { p.mu.Lock(); p.source = fn; p.mu.Unlock() }

// SetChase attaches the slave-side follower: called (off-lock) with the master's current frame
// position on every accepted running announce; the consumer jams its local clock on divergence.
func (p *TCPlane) SetChase(fn func(frame int64, rate Rate)) {
	p.mu.Lock()
	p.chase = fn
	p.mu.Unlock()
}

// Start subscribes the negotiation topics and runs the announce/election loop until ctx ends.
func (p *TCPlane) Start(ctx context.Context) {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.unsub = []func(){
		p.bus.Subscribe(TopicAdvert, p.onAdvert),
		p.bus.Subscribe(TopicTC, p.onTC),
	}
	every := p.announceEvery
	p.mu.Unlock()

	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				p.tick()
			}
		}
	}()
}

// Stop unsubscribes (the loop exits with its context).
func (p *TCPlane) Stop() {
	p.mu.Lock()
	unsub := p.unsub
	p.unsub, p.started = nil, false
	p.mu.Unlock()
	for _, u := range unsub {
		u()
	}
}

// PeerGone drops a departed peer from the candidate set (hook it to the peer-link state listener,
// like RouteManager.Advertise). A departed master re-elects immediately instead of after tcStale.
func (p *TCPlane) PeerGone(nodeID string) {
	p.mu.Lock()
	delete(p.cands, nodeID)
	changed := p.electLocked()
	p.mu.Unlock()
	p.notify(changed)
}

// tick is the announce/holdover heartbeat: masters announce; slaves depose a stale master.
func (p *TCPlane) tick() {
	p.mu.Lock()
	if p.elected == p.self {
		p.mu.Unlock()
		p.AnnounceNow()
		return
	}
	var changed bool
	if p.haveAnn && p.ann.Node == p.elected && time.Since(p.annAt) > p.staleAfter {
		// Master silent past holdover: drop it and re-elect (§4). The last anchor keeps
		// freewheeling Now() until a new master announces (or we take over).
		delete(p.cands, p.elected)
		changed = p.electLocked()
	}
	p.mu.Unlock()
	p.notify(changed)
}

// AnnounceNow publishes the local house-clock anchor if this node is the elected master (call on
// jam/start/stop for the §4 "on-jump" announce; a slave call is a no-op).
func (p *TCPlane) AnnounceNow() {
	p.mu.Lock()
	src := p.source
	if p.elected != p.self || src == nil {
		p.mu.Unlock()
		return
	}
	p.mu.Unlock()
	frame, rate, running := src()
	ann := TCAnnounce{Node: p.self, Running: running, Rate: rate.Nominal, Drop: rate.Drop,
		Frame: frame, Anchor: p.clock.Now()}
	if raw, err := json.Marshal(ann); err == nil {
		p.bus.Publish(TopicTC, raw)
	}
}

// onAdvert tracks clock-capable peers (Caps.Clock) as election candidates.
func (p *TCPlane) onAdvert(ev Event) {
	if ev.Local {
		return
	}
	var ad Advert
	if json.Unmarshal(ev.Data, &ad) != nil || ad.Caps == nil || !ad.Caps.Clock {
		return
	}
	node := ad.Node
	if node == "" {
		node = ev.Origin
	}
	if node == p.self {
		return
	}
	p.mu.Lock()
	p.cands[node] = true
	changed := p.electLocked()
	p.mu.Unlock()
	p.notify(changed)
}

// onTC accepts the elected master's announces (an announcer is clock-capable by definition - a
// better-ranked one deposes the current belief, converging every node on the same master).
func (p *TCPlane) onTC(ev Event) {
	var ann TCAnnounce
	if ev.Local || json.Unmarshal(ev.Data, &ann) != nil || ann.Node == "" || ann.Node == p.self {
		return
	}
	p.mu.Lock()
	p.cands[ann.Node] = true
	changed := p.electLocked()
	var chase func(int64, Rate)
	var frameNow int64
	rate := Rate{Nominal: ann.Rate, Drop: ann.Drop}
	if p.elected == ann.Node {
		p.ann, p.annAt, p.annLoc, p.haveAnn = ann, time.Now(), p.clock.Now(), true
		if ann.Running && p.chase != nil && rate.Valid() {
			chase, frameNow = p.chase, p.frameAtLocked()
		}
	}
	p.mu.Unlock()
	p.notify(changed)
	if chase != nil {
		chase(frameNow, rate)
	}
}

// electLocked recomputes the master: pinned wins when clock-capable, else lowest NodeID among
// self + candidates. Returns whether the election changed. Caller holds p.mu.
func (p *TCPlane) electLocked() bool {
	best := p.self
	if p.pin != "" && (p.pin == p.self || p.cands[p.pin]) {
		best = p.pin
	} else {
		for n := range p.cands {
			if n < best {
				best = n
			}
		}
	}
	if best == p.elected {
		return false
	}
	p.elected = best
	return true
}

// notify fires OnMaster + logs after an election change (off-lock).
func (p *TCPlane) notify(changed bool) {
	if !changed {
		return
	}
	p.mu.Lock()
	m := p.elected
	p.mu.Unlock()
	if p.log != nil {
		p.log.Info(logTag, "timecode master elected", map[string]any{"master": m, "self": m == p.self})
	}
	if p.onMaster != nil {
		p.onMaster(m)
	}
}

// frameAtLocked projects the last announce to the current local media clock. Anchor domain: the
// master's Anchor when our clock is disciplined into the master's domain (§2.3 locked, non-
// monotonic tier), else the local receipt anchor (error = bus latency, ≪ ½ frame on a LAN).
// Caller holds p.mu.
func (p *TCPlane) frameAtLocked() int64 {
	base := p.annLoc
	if q := p.clock.Quality(); q.Locked && q.Tier != TierMonotonic {
		base = p.ann.Anchor
	}
	frames := p.ann.Frame
	if p.ann.Running {
		rate := Rate{Nominal: p.ann.Rate, Drop: p.ann.Drop}
		num, den := rate.exact()
		if num > 0 {
			if d := p.clock.Now() - base; d > 0 {
				frames += d * num / (int64(time.Second) * den)
			}
		}
	}
	return frames
}

// Now returns the plane's current timecode + status: the local source when master, the projected
// (or freewheeling) master anchor when slave.
func (p *TCPlane) Now() (Timecode, TCStatus) {
	p.mu.Lock()
	if p.elected == p.self {
		src := p.source
		st := TCStatus{Role: TCRoleMaster, Master: p.self}
		p.mu.Unlock()
		if src != nil {
			frame, rate, running := src()
			st.TC, st.Rate, st.Running = TimecodeFromFrames(frame, rate), rate, running
		}
		return st.TC, st
	}
	st := TCStatus{Role: TCRoleSlave, Master: p.elected, LastAt: p.annAt}
	if p.haveAnn {
		st.Rate = Rate{Nominal: p.ann.Rate, Drop: p.ann.Drop}
		st.Running = p.ann.Running
		st.TC = TimecodeFromFrames(p.frameAtLocked(), st.Rate)
		st.Holdover = time.Since(p.annAt) > p.staleAfter
	}
	p.mu.Unlock()
	return st.TC, st
}

// Status snapshots the plane state (telemetry, §7).
func (p *TCPlane) Status() TCStatus { _, st := p.Now(); return st }
