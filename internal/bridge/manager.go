package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"rave.page/mate/internal/authz"
	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
)

// Manager owns this instance's presence on the account bridge: one registered session, one SSE
// downstream, the signal-plane rendezvous, and the live Conns to other devices.
//
// It knows nothing about crypto or trust. It brings two devices into contact and hands the
// resulting Conn to the layer that does: peerlink (which authenticates, encrypts, and gates)
// and then either the peerlink Link or the studio channel inside that tunnel.
type Manager struct {
	client *Client
	log    *logbus.Bus

	nodeID   string
	display  string
	caps     func() []string // evaluated at (re)register - capabilities follow the feature toggles
	tunnel   Tunnel
	onChange func() // UI repaint hook

	mu       sync.Mutex
	sid      string
	sessions map[string]Session          // account devices by sid (presence)
	conns    map[string]*Conn            // live conns by PEER sid
	dialing  map[string]bool             // peer sids we have an in-flight dial to
	dialWait map[string]chan signalFrame // in-flight dials awaiting a dial-ok/dial-no
	running  bool
	lastErr  string
}

// Tunnel is what the bridge hands a fresh Conn to. Implemented by the app wiring over
// peerlink + studio - kept as a seam so this package never imports either.
type Tunnel interface {
	// ServePeer runs the peerlink AKE + gate over conn and joins it to the peer link.
	// initiator = we dialled out.
	ServePeer(ctx context.Context, conn *Conn, initiator bool, peerName string)
	// ServeStudio runs the same secure tunnel, then serves the Local Studio protocol inside it.
	// Always the responder: the browser dials us.
	ServeStudio(ctx context.Context, conn *Conn)
	// StudioEnabled reports whether the Local Studio bridge feature is on. Off → we refuse
	// studio dials rather than quietly accepting them.
	StudioEnabled() bool
}

// Protocols carried inside a bridge tunnel, named on the signal plane.
const (
	ProtoPeerlink = "peerlink"
	ProtoStudio   = "studio"
)

// Signal-plane frames. The RELAY CAN READ THESE - they are rendezvous metadata only ("device A
// wants to talk to device B with protocol X"), never secrets. Everything of substance happens
// after, inside the AEAD tunnel.
const signalVersion = 1

type signalFrame struct {
	T      string `json:"t"`
	V      int    `json:"v"`
	Proto  string `json:"proto,omitempty"`
	Reason string `json:"reason,omitempty"`
}

const (
	sigDial   = "dial"
	sigDialOK = "dial-ok"
	sigDialNo = "dial-no"
)

// NewManager builds the bridge orchestrator. caps is evaluated at every (re)register so the
// advertised capabilities track the live feature toggles.
func NewManager(c *Client, log *logbus.Bus, nodeID, display string, caps func() []string, t Tunnel) *Manager {
	return &Manager{
		client: c, log: log, nodeID: nodeID, display: display, caps: caps, tunnel: t,
		sessions: map[string]Session{}, conns: map[string]*Conn{}, dialing: map[string]bool{},
		dialWait: map[string]chan signalFrame{},
	}
}

// SetOnChange installs a UI repaint hook (presence/session changes).
func (m *Manager) SetOnChange(fn func()) { m.mu.Lock(); m.onChange = fn; m.mu.Unlock() }

// Start registers and keeps the bridge session alive until ctx ends. Non-blocking.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil
	}
	m.running = true
	m.mu.Unlock()
	debuglog.Go(m.log, logTag, func() { m.run(ctx) })
	return nil
}

// Stop deregisters and drops every conn.
func (m *Manager) Stop() {
	m.mu.Lock()
	sid, running := m.sid, m.running
	conns := make([]*Conn, 0, len(m.conns))
	for _, c := range m.conns {
		conns = append(conns, c)
	}
	m.running, m.sid, m.conns = false, "", map[string]*Conn{}
	m.mu.Unlock()
	if !running {
		return
	}
	for _, c := range conns {
		c.Close()
	}
	if sid != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.client.Deregister(ctx, sid); err != nil {
			m.log.Info(logTag, "deregister failed (session will TTL out)", map[string]any{"error": err.Error()})
		}
	}
	m.fireChange()
}

// run is the supervision loop: register → stream → on break, back off and re-register. The
// contract gives no replay on reconnect, so presence is re-listed every time.
func (m *Manager) run(ctx context.Context) {
	backoff := time.Second
	// Log gate: a signed-out user would otherwise spam a warning every retry forever.
	var authGate logbus.Gate

	for ctx.Err() == nil {
		err := m.session(ctx)
		if ctx.Err() != nil {
			return
		}
		switch {
		case errors.Is(err, ErrUnauthorized):
			if n, ok := authGate.Should("signed-out", 10*time.Minute); ok {
				f := map[string]any{}
				if n > 0 {
					f["suppressed"] = n
				}
				m.log.Info(logTag, "not signed in; the account bridge is idle", f)
			}
			m.setErr("not signed in")
			backoff = 30 * time.Second // no point hammering; OnChange will kick us on sign-in
		case err != nil:
			authGate.Reset()
			m.log.Warn(logTag, "bridge session ended; reconnecting", map[string]any{"error": err.Error()})
			m.setErr(err.Error())
		default:
			authGate.Reset()
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

// session registers, opens the stream, and serves it until it breaks.
func (m *Manager) session(ctx context.Context) error {
	sess, err := m.client.Register(ctx, m.nodeID, m.display, m.caps())
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.sid = sess.SID
	m.lastErr = ""
	m.mu.Unlock()
	m.log.Info(logTag, "registered on the account bridge", map[string]any{
		"sid": sess.SID, "caps": sess.Capabilities,
	})
	m.refreshPresence(ctx)
	m.fireChange()

	defer func() {
		m.mu.Lock()
		m.sid = ""
		m.mu.Unlock()
		m.fireChange()
	}()

	// Heartbeat is belt-and-braces: the open stream refreshes our TTL every 15s server-side.
	// It also detects a session the server has forgotten (404 → re-register).
	hctx, hcancel := context.WithCancel(ctx)
	defer hcancel()
	debuglog.Go(m.log, logTag, func() { m.heartbeat(hctx, sess.SID) })

	return m.client.Stream(ctx, sess.SID, StreamHandlers{
		OnFrame:    func(f Frame) { m.onFrame(ctx, f) },
		OnPresence: func(p PresenceEvent) { m.onPresence(p) },
	})
}

func (m *Manager) heartbeat(ctx context.Context, sid string) {
	t := time.NewTicker(HeartbeatEach)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := m.client.Heartbeat(hctx, sid)
			cancel()
			if errors.Is(err, ErrSessionGone) {
				m.log.Info(logTag, "session expired server-side; re-registering", nil)
				return // the stream will break too; run() re-registers
			}
		}
	}
}

// refreshPresence re-lists the account's devices. The contract ignores Last-Event-ID and
// replays nothing on reconnect, so this is the ONLY way to resync after a stream drop.
func (m *Manager) refreshPresence(ctx context.Context) {
	list, err := m.client.ListSessions(ctx)
	if err != nil {
		return
	}
	m.mu.Lock()
	m.sessions = make(map[string]Session, len(list))
	for _, s := range list {
		if s.SID != m.sid {
			m.sessions[s.SID] = s
		}
	}
	m.mu.Unlock()
	m.fireChange()
}

func (m *Manager) onPresence(p PresenceEvent) {
	m.mu.Lock()
	if p.Session.SID == m.sid {
		m.mu.Unlock()
		return // ourselves
	}
	switch p.Event {
	case "online":
		m.sessions[p.Session.SID] = p.Session
	case "offline":
		delete(m.sessions, p.Session.SID)
		if c := m.conns[p.Session.SID]; c != nil {
			delete(m.conns, p.Session.SID)
			m.mu.Unlock()
			c.Close()
			m.fireChange()
			return
		}
	}
	m.mu.Unlock()
	m.fireChange()
}

// onFrame demuxes the downstream: relay → the peer's Conn; signal → rendezvous.
func (m *Manager) onFrame(ctx context.Context, f Frame) {
	switch f.Kind {
	case KindRelay:
		m.mu.Lock()
		c := m.conns[f.SID]
		m.mu.Unlock()
		if c != nil {
			c.deliver(f.Payload) // non-blocking; drops on a full queue (ARQ retransmits)
		}
	case KindSignal:
		m.onSignal(ctx, f)
	}
}

func (m *Manager) onSignal(ctx context.Context, f Frame) {
	var sf signalFrame
	if json.Unmarshal(f.Payload, &sf) != nil || sf.V != signalVersion {
		return
	}
	switch sf.T {
	case sigDial:
		m.onDial(ctx, f.SID, sf)
	case sigDialOK:
		// Our outbound dial was accepted; the dial goroutine is waiting on this.
		m.mu.Lock()
		ch := m.dialWait[f.SID]
		m.mu.Unlock()
		if ch != nil {
			select {
			case ch <- sf:
			default:
			}
		}
	case sigDialNo:
		m.mu.Lock()
		ch := m.dialWait[f.SID]
		m.mu.Unlock()
		if ch != nil {
			select {
			case ch <- sf:
			default:
			}
		}
	}
}

// onDial answers an inbound rendezvous. We accept the peer for relay, reply, and serve the
// requested protocol as the RESPONDER.
func (m *Manager) onDial(ctx context.Context, peerSID string, sf signalFrame) {
	m.mu.Lock()
	sid := m.sid
	_, busy := m.conns[peerSID]
	peer, known := m.sessions[peerSID]
	m.mu.Unlock()
	if sid == "" || busy {
		return
	}
	if !known {
		// Presence hasn't caught up; the sid is still account-scoped by the server, so trust it.
		peer = Session{SID: peerSID}
	}

	if sf.Proto == ProtoStudio && !m.tunnel.StudioEnabled() {
		m.replySignal(ctx, peerSID, signalFrame{T: sigDialNo, V: signalVersion, Reason: "studio bridge disabled"})
		return
	}
	if sf.Proto != ProtoStudio && sf.Proto != ProtoPeerlink {
		m.replySignal(ctx, peerSID, signalFrame{T: sigDialNo, V: signalVersion, Reason: "unknown protocol"})
		return
	}

	// Accept the peer for RELAY frames. Until BOTH sides have done this the data plane 403s.
	if err := m.client.Accept(ctx, sid, peerSID); err != nil {
		m.log.Warn(logTag, "accept failed", map[string]any{"peer_sid": peerSID, "error": err.Error()})
		return
	}
	if err := m.replySignal(ctx, peerSID, signalFrame{T: sigDialOK, V: signalVersion, Proto: sf.Proto}); err != nil {
		return
	}

	conn := m.newConn(ctx, peerSID)
	if conn == nil {
		return
	}
	m.log.Info(logTag, "inbound bridge dial", map[string]any{"peer_sid": peerSID, "proto": sf.Proto})
	debuglog.Go(m.log, logTag, func() {
		defer m.dropConn(peerSID)
		if sf.Proto == ProtoStudio {
			m.tunnel.ServeStudio(ctx, conn)
			return
		}
		m.tunnel.ServePeer(ctx, conn, false, peer.DisplayName)
	})
}

// Dial reaches another device on the account. Blocking; run it off the UI thread.
func (m *Manager) Dial(ctx context.Context, peerSID, proto string) error {
	m.mu.Lock()
	sid := m.sid
	if sid == "" {
		m.mu.Unlock()
		return errors.New("bridge: not registered")
	}
	if _, busy := m.conns[peerSID]; busy {
		m.mu.Unlock()
		return nil // already linked
	}
	if m.dialing[peerSID] {
		m.mu.Unlock()
		return nil
	}
	m.dialing[peerSID] = true
	wait := make(chan signalFrame, 1)
	m.dialWait[peerSID] = wait
	peer := m.sessions[peerSID]
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.dialing, peerSID)
		delete(m.dialWait, peerSID)
		m.mu.Unlock()
	}()

	// Accept the peer ourselves first, so the moment they accept us the data plane is open.
	if err := m.client.Accept(ctx, sid, peerSID); err != nil {
		return err
	}
	if err := m.replySignal(ctx, peerSID, signalFrame{T: sigDial, V: signalVersion, Proto: proto}); err != nil {
		return err
	}

	// Signal frames are fire-and-forget too. Retry the dial a few times before giving up.
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	retry := time.NewTicker(3 * time.Second)
	defer retry.Stop()
	for {
		select {
		case <-dctx.Done():
			return errors.New("bridge: peer did not answer the dial")
		case <-retry.C:
			_ = m.replySignal(ctx, peerSID, signalFrame{T: sigDial, V: signalVersion, Proto: proto})
		case sf := <-wait:
			if sf.T == sigDialNo {
				return errors.New("bridge: peer refused: " + sf.Reason)
			}
			conn := m.newConn(ctx, peerSID)
			if conn == nil {
				return errors.New("bridge: not registered")
			}
			m.log.Info(logTag, "outbound bridge dial accepted", map[string]any{
				"peer_sid": peerSID, "proto": proto,
			})
			debuglog.Go(m.log, logTag, func() {
				defer m.dropConn(peerSID)
				m.tunnel.ServePeer(ctx, conn, true, peer.DisplayName)
			})
			return nil
		}
	}
}

// newConn registers a Conn for a peer sid. Nil if we lost our session in the meantime.
func (m *Manager) newConn(ctx context.Context, peerSID string) *Conn {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sid == "" {
		return nil
	}
	c := newConn(ctx, m.sid, peerSID, m.client, m.log)
	m.conns[peerSID] = c
	return c
}

func (m *Manager) dropConn(peerSID string) {
	m.mu.Lock()
	c := m.conns[peerSID]
	delete(m.conns, peerSID)
	m.mu.Unlock()
	if c != nil {
		c.Close()
	}
	m.fireChange()
}

// replySignal publishes one signal frame.
func (m *Manager) replySignal(ctx context.Context, toSID string, sf signalFrame) error {
	m.mu.Lock()
	sid := m.sid
	m.mu.Unlock()
	if sid == "" {
		return errors.New("bridge: not registered")
	}
	b, err := json.Marshal(sf)
	if err != nil {
		return err
	}
	return m.client.Send(ctx, sid, toSID, 0, KindSignal, b)
}

// ── UI surface ───────────────────────────────────────────────────────────────

// Device is one other rave-mate/browser session on the account, for the UI.
type Device struct {
	SID          string
	NodeID       string
	DisplayName  string
	Capabilities []string
	Linked       bool // we have a live tunnel to it
}

// Devices lists the account's other online sessions.
func (m *Manager) Devices() []Device {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Device, 0, len(m.sessions))
	for sid, s := range m.sessions {
		_, linked := m.conns[sid]
		out = append(out, Device{
			SID: sid, NodeID: s.NodeID, DisplayName: s.DisplayName,
			Capabilities: s.Capabilities, Linked: linked,
		})
	}
	return out
}

// State is the bridge's connection state for the UI.
type State struct {
	Registered bool
	SID        string
	Error      string
	Devices    int
	Links      int
}

// State snapshots the bridge for the UI.
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return State{
		Registered: m.sid != "", SID: m.sid, Error: m.lastErr,
		Devices: len(m.sessions), Links: len(m.conns),
	}
}

func (m *Manager) setErr(s string) { m.mu.Lock(); m.lastErr = s; m.mu.Unlock(); m.fireChange() }

func (m *Manager) fireChange() {
	m.mu.Lock()
	fn := m.onChange
	m.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// StudioConn adapts a bridge Conn to studio.Conn, whose Close carries a protocol close code.
// The relay has no close frames, so the code is logged and the tunnel simply drops.
type StudioConn struct{ *Conn }

// Close ends the studio session. code/reason are studio's (4001-4007).
func (s StudioConn) Close(code int, reason string) {
	s.Conn.log.Info(logTag, "studio session over the bridge closed", map[string]any{
		"code": code, "reason": reason, "peer_sid": s.Conn.peerSID,
	})
	s.Conn.Close()
}

// GateTransport marks a bridge Conn as a gated transport (peerlink.Gated): there is no human
// at the far end to compare an SAS, so an unknown peer goes through the TOTP gate instead.
func (c *Conn) GateTransport() authz.Transport { return authz.TransportBridge }
