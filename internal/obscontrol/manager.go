package obscontrol

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mediasync"
	"rave.page/mate/internal/obs"
)

var (
	errNoLocalOBS    = errors.New("obscontrol: no local OBS connection")
	errUnknownAction = errors.New("obscontrol: unknown action")
)

const (
	logTag    = "obscontrol"
	pollEvery = time.Second
	staleAge  = 6 * time.Second // peers that stop publishing are dropped from snapshots
)

// OBS is the local OBS bridge surface the manager drives (satisfied by *featurehost.ObsProxy). Nil on
// an instance with no local OBS - that instance only renders peers' status + routes commands.
type OBS interface {
	Connected() bool
	StartStream(ctx context.Context) error
	StopStream(ctx context.Context) error
	ToggleStream(ctx context.Context) (bool, error)
	StartRecord(ctx context.Context) error
	StopRecord(ctx context.Context) error
	ToggleRecord(ctx context.Context) (bool, error)
	ToggleRecordPause(ctx context.Context) (bool, error)
	ToggleMute(ctx context.Context, input string) (bool, error)
	GetStreamStatus(ctx context.Context) (obs.StreamStatus, error)
	GetRecordStatus(ctx context.Context) (obs.RecordStatus, error)
}

type entry struct {
	Instance
	seen time.Time
}

// srcState tracks one OBS source this node owns (its local OBS + any direct LAN remotes): the OBS
// surface, bitrate-from-byte-delta state, and last-published connectivity.
type srcState struct {
	id, label    string
	obs          OBS
	lastBytes    int64
	lastAt       time.Time
	wasConnected bool
}

// Manager polls every OBS source this node owns (local + direct LAN remotes), broadcasts each one's
// Status, serves directed commands, and keeps a snapshot of all sources seen on the bus.
type Manager struct {
	log     *logbus.Bus
	bus     *eventbus.Bus
	local   OBS             // local OBS bridge (nil = none)
	localID string          // source id for the local OBS (= node id)
	label   string          // local human label (hostname)
	self    string          // local node id
	remotes func() []Remote // direct LAN remotes (re-read each poll; nil = none)

	mu      sync.Mutex
	srcs    map[string]*srcState  // source id → owned source (local + active remotes)
	directs map[string]*directOBS // remote id → direct client (for reconcile/close)
	insts   map[string]entry      // source id → Status seen on the bus (own echo + peers)

	// media-sync tier (chase OBS media sources to the house clock)
	syncClock *mediasync.WallClock         // house clock (v1: wall-clock, "start sync now")
	syncCfg   func() SyncConfig            // live sync config (nil = disabled)
	syncMu    sync.Mutex                   // guards chasers + syncGates
	chasers   map[string]*mediasync.Chaser // "endpoint\x00input" → chaser
	syncGates map[string]*logbus.Gate      // per-chaser "sync tick" failure log gate
}

// New builds the manager. local may be nil (render/route only). bus may be nil (local-only). remotes
// supplies direct LAN OBS endpoints (re-read each poll); nil = none.
func New(log *logbus.Bus, bus *eventbus.Bus, local OBS, label, selfNodeID string, remotes func() []Remote) *Manager {
	return &Manager{
		log: log, bus: bus, local: local, localID: selfNodeID, label: label, self: selfNodeID, remotes: remotes,
		srcs:      map[string]*srcState{},
		directs:   map[string]*directOBS{},
		insts:     map[string]entry{},
		syncClock: mediasync.NewWallClock(),
		chasers:   map[string]*mediasync.Chaser{},
		syncGates: map[string]*logbus.Gate{},
	}
}

// Start runs the supervise loop until ctx is cancelled: subscribe to status/cmd, poll local OBS,
// broadcast status. Implements module.Service.Start.
func (m *Manager) Start(ctx context.Context) error {
	if m.bus != nil {
		m.bus.Subscribe(TopicStatus, m.onStatus)
		m.bus.Subscribe(TopicCmd, func(e eventbus.Event) { m.onCmd(ctx, e) })
	}
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	st := time.NewTicker(syncEvery)
	defer st.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			m.pollAll(ctx)
		case <-st.C:
			m.tickSync(ctx)
		}
	}
}

// Statuses returns a snapshot of all known instances' OBS status (local first, then by label), with
// stale peers (no update within staleAge) pruned.
func (m *Manager) Statuses() []Instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	var out []Instance
	for node, e := range m.insts {
		if !e.Local && now.Sub(e.seen) > staleAge {
			delete(m.insts, node)
			continue
		}
		out = append(out, e.Instance)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Local != out[j].Local {
			return out[i].Local
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// Command issues a command: over the bus when wired (the targeted source's owner executes), else
// directly against the local OBS.
func (m *Manager) Command(ctx context.Context, cmd Cmd) error {
	if m.bus == nil {
		m.reconcileSources()
		target := cmd.Target
		if target == "" {
			target = m.localID
		}
		m.mu.Lock()
		s := m.srcs[target]
		m.mu.Unlock()
		if s == nil || s.obs == nil {
			return errNoLocalOBS
		}
		return exec(ctx, s.obs, cmd)
	}
	raw, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	m.bus.Publish(TopicCmd, raw)
	return nil
}

// onStatus records a source's status broadcast (keyed by source id, not node).
func (m *Manager) onStatus(e eventbus.Event) {
	var st Status
	if json.Unmarshal(e.Data, &st) != nil || st.ID == "" {
		return
	}
	m.mu.Lock()
	m.insts[st.ID] = entry{Instance: Instance{Node: e.Origin, Local: e.Local, Status: st}, seen: time.Now()}
	m.mu.Unlock()
}

// onCmd executes a command if it targets a source this node owns (Target==source id, or "" → the
// local OBS).
func (m *Manager) onCmd(ctx context.Context, e eventbus.Event) {
	var cmd Cmd
	if json.Unmarshal(e.Data, &cmd) != nil {
		return
	}
	m.mu.Lock()
	target := cmd.Target
	if target == "" {
		target = m.localID
	}
	s := m.srcs[target]
	m.mu.Unlock()
	if s == nil || s.obs == nil {
		return
	}
	go func() { // don't block the bus fanout goroutine on an obs-websocket round-trip
		if err := exec(ctx, s.obs, cmd); err != nil {
			m.log.Warn(logTag, "command failed", map[string]any{"action": cmd.Action, "id": target, "error": err.Error()})
		}
	}()
}

// exec runs one action against an OBS source.
func exec(ctx context.Context, o OBS, cmd Cmd) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	switch cmd.Action {
	case ActMicToggle:
		_, err := o.ToggleMute(cctx, cmd.Arg)
		return err
	case ActStreamStart:
		return o.StartStream(cctx)
	case ActStreamStop:
		return o.StopStream(cctx)
	case ActStreamToggle:
		_, err := o.ToggleStream(cctx)
		return err
	case ActRecordStart:
		return o.StartRecord(cctx)
	case ActRecordStop:
		return o.StopRecord(cctx)
	case ActRecordToggle:
		_, err := o.ToggleRecord(cctx)
		return err
	case ActRecordPause:
		_, err := o.ToggleRecordPause(cctx)
		return err
	}
	return errUnknownAction
}

// pollAll reconciles the owned sources (local + direct LAN remotes) and polls + broadcasts each.
func (m *Manager) pollAll(ctx context.Context) {
	m.reconcileSources()

	m.mu.Lock()
	srcs := make([]*srcState, 0, len(m.srcs))
	for _, s := range m.srcs {
		srcs = append(srcs, s)
	}
	m.mu.Unlock()

	anyConnected := false
	for _, s := range srcs {
		if m.pollSource(ctx, s) {
			anyConnected = true
		}
	}
	if m.bus != nil {
		if anyConnected {
			m.bus.AddCap(CapOBS)
		} else {
			m.bus.RemoveCap(CapOBS)
		}
	}
}

// reconcileSources rebuilds the owned-source set from the local OBS + the configured remotes (adds
// new direct clients, closes + drops removed ones).
func (m *Manager) reconcileSources() {
	want := map[string]*srcState{}
	if m.local != nil {
		want[m.localID] = m.srcOrNew(m.localID, m.label, m.local)
	}
	var remotes []Remote
	if m.remotes != nil {
		remotes = m.remotes()
	}
	live := map[string]bool{}
	for _, r := range remotes {
		if r.ID == "" || r.Host == "" {
			continue
		}
		live[r.ID] = true
		d, ok := m.directs[r.ID]
		if !ok {
			d = newDirectOBS(r.Host, r.Port, r.Password)
			m.directs[r.ID] = d
		}
		want[r.ID] = m.srcOrNew(r.ID, r.Label, d)
	}
	// Close direct clients no longer configured.
	for id, d := range m.directs {
		if !live[id] {
			d.close()
			delete(m.directs, id)
		}
	}
	m.mu.Lock()
	m.srcs = want
	m.mu.Unlock()
}

// srcOrNew preserves a source's bitrate state across reconciles.
func (m *Manager) srcOrNew(id, label string, o OBS) *srcState {
	if s, ok := m.srcs[id]; ok {
		s.label, s.obs = label, o
		return s
	}
	return &srcState{id: id, label: label, obs: o}
}

// ensureConnector marks owned sources whose (re)connect is DRIVEN by the poll (directOBS,
// which dials lazily + throttled). The featurehost local proxy reconnects itself in the
// child, so a cheap Connected()=false skips its status request entirely - no per-second
// IPC round-trip while OBS is closed or the feature is off.
type ensureConnector interface {
	ensureConnected(ctx context.Context) bool
}

// pollSource samples one source and broadcasts its status (bitrate from the byte delta). Returns
// whether it's connected.
func (m *Manager) pollSource(ctx context.Context, s *srcState) bool {
	if !s.obs.Connected() {
		ec, ok := s.obs.(ensureConnector)
		if !ok || !ec.ensureConnected(ctx) {
			if s.wasConnected {
				m.publish(Status{ID: s.id, Label: s.label})
				s.wasConnected = false
			}
			s.lastBytes, s.lastAt = 0, time.Time{}
			return false
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ss, serr := s.obs.GetStreamStatus(cctx)
	if serr != nil {
		if s.wasConnected {
			m.publish(Status{ID: s.id, Label: s.label})
			s.wasConnected = false
		}
		s.lastBytes, s.lastAt = 0, time.Time{}
		return false
	}
	rs, _ := s.obs.GetRecordStatus(cctx)
	s.wasConnected = true

	now := time.Now()
	kbps := 0
	if ss.Active && s.lastBytes > 0 && !s.lastAt.IsZero() {
		if dt := now.Sub(s.lastAt).Seconds(); dt > 0 {
			kbps = int(float64(ss.Bytes-s.lastBytes) * 8 / 1000 / dt)
		}
	}
	if ss.Active {
		s.lastBytes, s.lastAt = ss.Bytes, now
	} else {
		s.lastBytes, s.lastAt = 0, time.Time{}
	}
	m.publish(Status{
		ID:           s.id,
		Label:        s.label,
		Connected:    true,
		Streaming:    ss.Active,
		Recording:    rs.Active,
		Reconnecting: ss.Reconnecting,
		StreamSec:    ss.Duration.Seconds(),
		RecSec:       rs.Duration.Seconds(),
		BitrateKbps:  kbps,
		Congestion:   ss.Congestion,
		Skipped:      ss.Skipped,
		Total:        ss.Total,
	})
	return true
}

// publish broadcasts a source's status (also fans out locally → updates our own snapshot).
func (m *Manager) publish(st Status) {
	if m.bus == nil {
		m.mu.Lock()
		m.insts[st.ID] = entry{Instance: Instance{Node: m.self, Local: true, Status: st}, seen: time.Now()}
		m.mu.Unlock()
		return
	}
	if raw, err := json.Marshal(st); err == nil {
		m.bus.Publish(TopicStatus, raw)
	}
}
