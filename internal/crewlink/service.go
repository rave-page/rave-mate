package crewlink

// service.go - the "crew" feature module (registered like mocap in internal/app). cfg is
// re-read on each (re)start; settings edits auto-restart the module (webui settingModule).
//
//	role=node   → mocap.Service sink = local Inject (overlay keeps working) + node uplink
//	role=master → crewlink ingest feeds the ONE persistent mocapmaster.Master via Inject
//
// ISOLATION: in-proc, network-only supervisor (bridge-style). No subprocesses, no cgo.

import (
	"context"
	"fmt"
	"sync"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/mocap"
	"rave.page/mate/internal/mocapnode"
)

const source = "crew"

// Service is the crew module. Start/ctx follows the module.Manager contract (non-blocking).
type Service struct {
	log    *logbus.Bus
	cfgFn  func() config.CrewFeature
	base   string // API root (cfg.APIBaseURL)
	tokens TokenSource
	mocap  *mocap.Service // sink/inject seam; may be nil (tests)

	mu      sync.Mutex
	running bool
	role    string
	eventID string
	node    *Node
	master  *MasterLink
	lastErr string
}

// New builds the service. tokens is the account TokenSource (shared/auth.Manager).
func New(log *logbus.Bus, cfgFn func() config.CrewFeature, base string, tokens TokenSource, mocapSvc *mocap.Service) *Service {
	return &Service{log: log, cfgFn: cfgFn, base: base, tokens: tokens, mocap: mocapSvc}
}

// Start launches the supervised relay link bound to ctx. Fails only on invalid config;
// network trouble degrades into the 1s→30s re-join loop.
func (s *Service) Start(ctx context.Context) error {
	cfg := s.cfgFn()
	eventID := cfg.ResolvedEventID()
	if eventID == "" {
		return fmt.Errorf("crew: event id required (Settings -> Capture crew)")
	}
	logf := func(format string, args ...any) {
		s.log.Warn(source, fmt.Sprintf(format, args...), nil)
	}
	client := NewClient(s.base, s.tokens)
	role := cfg.ResolvedRole()

	s.mu.Lock()
	s.running, s.role, s.eventID, s.lastErr = true, role, eventID, ""
	s.node, s.master = nil, nil
	s.mu.Unlock()

	switch role {
	case RoleMaster:
		ml := NewMaster(MasterConfig{
			Client: client, EventID: eventID, Label: cfg.ResolvedLabel(),
			Inject: s.inject, Logf: logf,
		})
		s.mu.Lock()
		s.master = ml
		s.mu.Unlock()
		debuglog.Go(s.log, source, func() { ml.Run(ctx) })
	default:
		n := NewNode(NodeConfig{
			Client: client, EventID: eventID, Label: cfg.ResolvedLabel(), Logf: logf,
		})
		s.mu.Lock()
		s.node = n
		s.mu.Unlock()
		if s.mocap != nil {
			// Captured packets: keep the local overlay (Inject) AND uplink to the crew masters.
			s.mocap.SetSink(func(pkt mocapnode.Packet) {
				_ = s.mocap.Inject(pkt)
				n.Enqueue(pkt)
			})
		}
		debuglog.Go(s.log, source, func() { n.Run(ctx) })
	}
	s.log.Info(source, "crew relay up", map[string]any{"role": role, "event": eventID})

	debuglog.Go(s.log, source, func() {
		<-ctx.Done()
		if s.mocap != nil {
			s.mocap.SetSink(nil) // restore direct local routing
		}
		s.mu.Lock()
		s.running, s.node, s.master = false, nil, nil
		s.mu.Unlock()
	})
	return nil
}

// inject feeds a remote packet into the local persistent master; false while mocap is off.
func (s *Service) inject(pkt mocapnode.Packet) bool {
	if s.mocap == nil {
		return false
	}
	return s.mocap.Inject(pkt)
}

// Status is the live snapshot for the settings card + ctl crew-status.
type Status struct {
	Running  bool
	Role     string // "node" | "master"
	EventID  string
	SID      string // "" = connecting
	Members  int    // masters present (node) / nodes present (master)
	Frames   uint64 // pose frames uplinked (node) / injected (master)
	Dropped  uint64 // queue/no-master drops (node) / clamped+dropped (master)
	Locked   bool   // node clock discipline lock (always true for master - own domain)
	OffsetNs int64  // node applied clock slew
	LastErr  string
}

// Status returns the live snapshot.
func (s *Service) Status() Status {
	s.mu.Lock()
	st := Status{Running: s.running, Role: s.role, EventID: s.eventID, LastErr: s.lastErr}
	node, master := s.node, s.master
	s.mu.Unlock()
	switch {
	case node != nil:
		ns := node.Status()
		st.SID, st.Members, st.Frames = ns.SID, ns.Masters, ns.Sent
		st.Dropped = ns.Dropped + ns.SendErrs
		st.Locked, st.OffsetNs = ns.Locked, ns.OffsetNs
		if st.LastErr == "" {
			st.LastErr = ns.LastErr
		}
	case master != nil:
		ms := master.Status()
		st.SID, st.Members, st.Frames = ms.SID, ms.Nodes, ms.Injected
		st.Dropped = ms.Clamped + ms.Dropped
		st.Locked = true
		if st.LastErr == "" {
			st.LastErr = ms.LastErr
		}
	}
	return st
}

// StatusText renders the snapshot as the multi-line ctl reply.
func (s *Service) StatusText() string {
	st := s.Status()
	if !st.Running {
		return "crew off (enable it in Settings -> Capture crew)"
	}
	out := fmt.Sprintf("crew running - role %s, event %s\n", st.Role, st.EventID)
	switch {
	case st.LastErr != "":
		out += "status: ERROR " + st.LastErr + "\n"
	case st.SID == "":
		out += "status: connecting\n"
	case st.Role == RoleMaster:
		out += fmt.Sprintf("status: %d frame(s) ingested, %d rejected, %d node(s) present\n",
			st.Frames, st.Dropped, st.Members)
	default:
		lock := "unlocked"
		if st.Locked {
			lock = "locked"
		}
		out += fmt.Sprintf("status: %d frame(s) uplinked, %d dropped, %d master(s) present, clock %s\n",
			st.Frames, st.Dropped, st.Members, lock)
	}
	return out
}
