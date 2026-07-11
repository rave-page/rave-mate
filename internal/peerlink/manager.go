package peerlink

import (
	"context"
	"crypto/ed25519"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/discovery"
	"rave.page/mate/internal/identity"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/peers"
	"rave.page/mate/internal/wirecrypto"
)

const logTag = "peerlink"

// portRange is the LAN-facing listener range. Kept clear of studio's loopback (47615-47619)
// AND the single-instance control socket (127.0.0.1:47620, see internal/app/instance.go) -
// binding 0.0.0.0:47620 would collide with that.
var portRange = []int{47631, 47632, 47633, 47634, 47635}

// Status is a connection's lifecycle phase.
type Status string

const (
	StatusConnecting Status = "connecting"
	StatusAwaitSAS   Status = "awaiting-sas" // SAS shown; waiting for both users to confirm
	StatusConnected  Status = "connected"
	StatusClosed     Status = "closed"
)

// ConnInfo is a UI-facing snapshot of one peer connection.
type ConnInfo struct {
	NodeID   string
	Nickname string
	Address  string
	Status   Status
	SAS      string // populated while StatusAwaitSAS
	Trusted  bool
	Inbound  bool
}

// SASRequest is raised to the UI when a pairing needs the user to compare the 6-digit code.
type SASRequest struct {
	NodeID   string
	Nickname string
	SAS      string
}

type connState struct {
	link    *Link
	res     *Result
	info    ConnInfo
	ctx     context.Context
	cancel  context.CancelFunc
	localOK bool
	remOK   bool
}

// Manager owns the LAN listener, outbound dials, and the remembered-peer trust decisions.
type Manager struct {
	id    *identity.Identity
	store *peers.Store
	log   *logbus.Bus

	mu        sync.Mutex
	conns     map[string]*connState   // by peer node id
	dialGates map[string]*logbus.Gate // by peer node id: don't re-warn every re-discovery of an unreachable peer
	port      int
	httpSrv   *http.Server
	ln        net.Listener

	onSAS   []func(SASRequest)
	onState []func()
	onData  func(peerNodeID, channel string, payload []byte)

	ctx context.Context
}

// New builds the manager. id is this node's identity; store remembers paired peers.
func New(id *identity.Identity, store *peers.Store, log *logbus.Bus) *Manager {
	return &Manager{id: id, store: store, log: log, conns: map[string]*connState{}, dialGates: map[string]*logbus.Gate{}}
}

// AddListener registers peer-event hooks (additive - both the Fyne UI and the studio peer
// gateway listen): onSAS raises a pairing-code prompt; onState fires when any connection's
// state changes. Either may be nil. Listeners live for the app's lifetime (no removal).
func (m *Manager) AddListener(onSAS func(SASRequest), onState func()) {
	m.mu.Lock()
	if onSAS != nil {
		m.onSAS = append(m.onSAS, onSAS)
	}
	if onState != nil {
		m.onState = append(m.onState, onState)
	}
	m.mu.Unlock()
}

// SetDataHandler wires the app-level data-channel sink: fn receives (peerNodeID, channel,
// payload) for every data frame from a CONNECTED peer. Nil disables delivery.
func (m *Manager) SetDataHandler(fn func(peerNodeID, channel string, payload []byte)) {
	m.mu.Lock()
	m.onData = fn
	m.mu.Unlock()
}

// Port returns the bound LAN listener port (advertised via discovery). 0 if not started.
func (m *Manager) Port() int { m.mu.Lock(); defer m.mu.Unlock(); return m.port }

// Start binds the LAN listener and serves peer connections until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) error {
	m.ctx = ctx
	ln, port, err := listenRange()
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.ln, m.port = ln, port
	m.httpSrv = &http.Server{Handler: http.HandlerFunc(m.handleHTTP), ReadHeaderTimeout: 5 * time.Second}
	srv := m.httpSrv
	m.mu.Unlock()

	debuglog.Go(m.log, logTag, func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			m.log.Warn(logTag, "listener stopped", map[string]any{"error": err.Error()})
		}
	})
	m.log.Info(logTag, "listening", map[string]any{"port": port, "node": m.id.NodeID})
	return nil
}

// Stop closes the listener and every live connection.
func (m *Manager) Stop() {
	m.mu.Lock()
	srv := m.httpSrv
	conns := make([]*connState, 0, len(m.conns))
	for _, c := range m.conns {
		conns = append(conns, c)
	}
	m.httpSrv = nil
	m.mu.Unlock()
	for _, c := range conns {
		c.cancel()
		c.link.Close()
	}
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

func listenRange() (net.Listener, int, error) {
	var lastErr error
	host := bindHost()
	for _, p := range listenPorts() {
		ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err == nil {
			return ln, p, nil
		}
		lastErr = err
	}
	return nil, 0, lastErr
}

// listenPorts returns the listener port candidates. RAVE_MATE_PEER_PORTS (comma-separated)
// overrides portRange - isolated rigs sharing a host with a real instance must not race it
// for 47631-47635.
func listenPorts() []int {
	v := strings.TrimSpace(os.Getenv("RAVE_MATE_PEER_PORTS"))
	if v == "" {
		return portRange
	}
	var out []int
	for _, f := range strings.Split(v, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(f)); err == nil && n > 0 && n < 65536 {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return portRange
	}
	return out
}

// bindHost is the listener bind address. RAVE_MATE_PEER_BIND overrides the default
// all-interfaces bind - e.g. 127.0.0.1 for same-host rigs (no LAN exposure, no firewall
// prompt) or one NIC's address on multi-homed boxes.
func bindHost() string {
	if v := strings.TrimSpace(os.Getenv("RAVE_MATE_PEER_BIND")); v != "" {
		return v
	}
	return "0.0.0.0"
}

// BindIsLoopback reports a loopback-only listener bind (RAVE_MATE_PEER_BIND). The app skips
// mDNS then: a loopback listener isn't LAN-reachable and discovery only advertises LAN IPs.
func BindIsLoopback() bool {
	ip := net.ParseIP(bindHost())
	return ip != nil && ip.IsLoopback()
}

// SeedAddrs returns static peer addresses ("host:port", comma-separated) from
// RAVE_MATE_PEER_SEED - direct dial for same-host rigs and multicast-less networks.
func SeedAddrs() []string {
	var out []string
	for _, a := range strings.Split(os.Getenv("RAVE_MATE_PEER_SEED"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// ConnectAddr dials a peer at a literal address (seed / manual connect - no discovery
// record). No-op while any connection already uses addr; trust + SAS flow are identical to
// a discovered dial.
func (m *Manager) ConnectAddr(addr string) {
	if addr == "" {
		return
	}
	m.mu.Lock()
	for _, c := range m.conns {
		if c.info.Address == addr {
			m.mu.Unlock()
			return
		}
	}
	m.mu.Unlock()
	debuglog.Go(m.log, logTag, func() {
		dctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
		defer cancel()
		ws, _, err := websocket.Dial(dctx, "ws://"+addr+"/", nil)
		if err != nil {
			if n, ok := m.dialGate("addr:"+addr).Should(err.Error(), 10*time.Minute); ok {
				f := map[string]any{"addr": addr, "error": err.Error()}
				if n > 0 {
					f["suppressed"] = n
				}
				m.log.Warn(logTag, "seed dial failed", f)
			}
			return
		}
		m.dialGate("addr:" + addr).Reset()
		m.runHandshake(newWSConn(ws), roleInitiator, "", addr)
	})
}

// ── inbound ──────────────────────────────────────────────────────────────────

func (m *Manager) handleHTTP(w http.ResponseWriter, r *http.Request) {
	// InsecureSkipVerify skips coder/websocket's browser *Origin* same-origin check - NOT
	// TLS (this is a plaintext LAN ws:// listener; peers are native, not browsers, and send
	// no Origin). Authenticity comes entirely from the Ed25519 mutual-auth handshake below,
	// not from the transport.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	debuglog.Go(m.log, logTag, func() { m.runHandshake(newWSConn(c), roleResponder, "", hostOf(r.RemoteAddr)) })
}

// ── outbound ─────────────────────────────────────────────────────────────────

// Connect dials a discovered peer and runs the handshake. A trusted peer connects
// silently; an unknown peer raises an SAS prompt (the pairing flow).
func (m *Manager) Connect(p discovery.Peer) {
	if p.NodeID == m.id.NodeID || p.Port == 0 {
		return
	}
	m.mu.Lock()
	_, busy := m.conns[p.NodeID]
	m.mu.Unlock()
	if busy {
		return // already connected/connecting
	}
	addr := net.JoinHostPort(p.Address.String(), strconv.Itoa(p.Port))
	debuglog.Go(m.log, logTag, func() {
		dctx, cancel := context.WithTimeout(m.ctx, 8*time.Second)
		defer cancel()
		ws, _, err := websocket.Dial(dctx, "ws://"+addr+"/", nil)
		if err != nil {
			// Re-warned on every discovery re-advert of an unreachable peer otherwise.
			if n, ok := m.dialGate(p.NodeID).Should(err.Error(), 10*time.Minute); ok {
				f := map[string]any{"peer": p.NodeID, "error": err.Error()}
				if n > 0 {
					f["suppressed"] = n
				}
				m.log.Warn(logTag, "dial failed", f)
			}
			return
		}
		m.dialGate(p.NodeID).Reset()
		m.runHandshake(newWSConn(ws), roleInitiator, p.Name, addr)
	})
}

// dialGate returns (lazily creating) the per-peer dial-failure log gate.
func (m *Manager) dialGate(nodeID string) *logbus.Gate {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, ok := m.dialGates[nodeID]
	if !ok {
		g = &logbus.Gate{}
		m.dialGates[nodeID] = g
	}
	return g
}

// ── handshake → connection ───────────────────────────────────────────────────

func (m *Manager) runHandshake(conn Conn, r role, nickname, addr string) {
	hctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	res, err := doHandshake(hctx, conn, r, m.id, m.trustLookup)
	cancel()
	if err != nil {
		m.log.Warn(logTag, "handshake failed", map[string]any{"error": err.Error(), "addr": addr})
		conn.Close()
		return
	}

	cctx, ccancel := context.WithCancel(m.ctx)
	cs := &connState{
		link: newLink(conn, res), res: res, ctx: cctx, cancel: ccancel,
		info: ConnInfo{NodeID: res.PeerNodeID, Nickname: nickname, Address: addr, Trusted: res.Trusted, Inbound: r == roleResponder},
	}
	if known, ok := m.store.Get(res.PeerNodeID); ok && cs.info.Nickname == "" {
		cs.info.Nickname = known.Nickname
	}
	cs.link.onClose = func(error) { m.dropConn(res.PeerNodeID) }
	cs.link.onFrame = func(t string, mp map[string]any) { m.onFrame(cs, t, mp) }

	// Register (replace any stale entry for this peer).
	m.mu.Lock()
	if old, ok := m.conns[res.PeerNodeID]; ok {
		old.cancel()
		old.link.Close()
	}
	m.conns[res.PeerNodeID] = cs
	m.mu.Unlock()

	debuglog.Go(m.log, logTag, func() { cs.link.readLoop(cctx) })

	if res.Trusted {
		m.openConnected(cs)
		return
	}
	// Pairing: show the SAS on both ends; wait for mutual confirmation.
	cs.info.Status, cs.info.SAS = StatusAwaitSAS, res.SAS
	m.fireState()
	m.fireSAS(SASRequest{NodeID: res.PeerNodeID, Nickname: cs.info.Nickname, SAS: res.SAS})
}

// ConfirmSAS records the local user's verdict on a pairing code and tells the peer.
func (m *Manager) ConfirmSAS(nodeID string, accept bool) {
	m.mu.Lock()
	cs := m.conns[nodeID]
	m.mu.Unlock()
	if cs == nil {
		return
	}
	sctx, cancel := context.WithTimeout(cs.ctx, 5*time.Second)
	defer cancel()
	if !accept {
		_ = cs.link.send(sctx, frameReject, nil)
		cs.cancel()
		cs.link.Close()
		return
	}
	_ = cs.link.send(sctx, frameConfirm, nil)
	cs.localOK = true
	m.maybeFinalize(cs)
}

func (m *Manager) onFrame(cs *connState, t string, mp map[string]any) {
	switch t {
	case frameConfirm:
		cs.remOK = true
		m.maybeFinalize(cs)
	case frameReject:
		m.log.Info(logTag, "peer rejected pairing", map[string]any{"peer": cs.res.PeerNodeID})
		cs.cancel()
		cs.link.Close()
	case framePing:
		// Echo the sender's timestamp + our clock so the pong doubles as an RTT/offset probe.
		extra := map[string]any{"pt": time.Now().UnixMicro()}
		if pts, ok := numField(mp, "pts"); ok {
			extra["pts"] = pts
		}
		sctx, cancel := context.WithTimeout(cs.ctx, 5*time.Second)
		_ = cs.link.send(sctx, framePong, extra)
		cancel()
	case framePong:
		cs.link.notePong(mp)
	case frameData:
		m.dispatchData(cs, mp)
	}
}

// dispatchData routes an app-level data frame to the registered handler. Ignored unless the
// connection is fully open (no app data during pairing) and a handler is set.
func (m *Manager) dispatchData(cs *connState, mp map[string]any) {
	m.mu.Lock()
	fn := m.onData
	open := cs.info.Status == StatusConnected
	m.mu.Unlock()
	if fn == nil || !open {
		return
	}
	ch, _ := mp["ch"].(string)
	data, _ := mp["data"].(string)
	if ch == "" {
		return
	}
	fn(cs.res.PeerNodeID, ch, []byte(data))
}

// SendTo sends an app-level data frame to one connected, trusted peer. No-op (returns nil) if
// the peer isn't currently connected.
func (m *Manager) SendTo(nodeID, channel string, payload []byte) error {
	m.mu.Lock()
	cs := m.conns[nodeID]
	open := cs != nil && cs.info.Status == StatusConnected
	m.mu.Unlock()
	if !open {
		return nil
	}
	sctx, cancel := context.WithTimeout(cs.ctx, 5*time.Second)
	defer cancel()
	return cs.link.SendData(sctx, channel, payload)
}

// Broadcast sends an app-level data frame to every connected peer (best-effort).
func (m *Manager) Broadcast(channel string, payload []byte) {
	m.mu.Lock()
	targets := make([]*connState, 0, len(m.conns))
	for _, c := range m.conns {
		if c.info.Status == StatusConnected {
			targets = append(targets, c)
		}
	}
	m.mu.Unlock()
	for _, c := range targets {
		sctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
		_ = c.link.SendData(sctx, channel, payload)
		cancel()
	}
}

// maybeFinalize promotes a pairing to trusted+connected once both sides confirmed the SAS.
func (m *Manager) maybeFinalize(cs *connState) {
	if !cs.localOK || !cs.remOK {
		return
	}
	now := time.Now().UTC()
	_ = m.store.Save(peers.Peer{
		NodeID: cs.res.PeerNodeID, IdentityPub: cs.res.PeerIdentityPub, Nickname: cs.info.Nickname,
		LastAddress: cs.info.Address, LastSeen: now, PairedAt: now, Trusted: true,
	})
	cs.info.Trusted = true
	m.log.Info(logTag, "peer paired", map[string]any{"peer": cs.res.PeerNodeID})
	m.openConnected(cs)
}

func (m *Manager) openConnected(cs *connState) {
	cs.info.Status, cs.info.SAS = StatusConnected, ""
	// Refresh last-seen/address on the remembered peer.
	if known, ok := m.store.Get(cs.res.PeerNodeID); ok {
		known.LastSeen = time.Now().UTC()
		known.LastAddress = cs.info.Address
		_ = m.store.Save(known)
	}
	debuglog.Go(m.log, logTag, func() { cs.link.keepalive(cs.ctx) })
	m.fireState()
}

func (m *Manager) dropConn(nodeID string) {
	m.mu.Lock()
	delete(m.conns, nodeID)
	m.mu.Unlock()
	m.fireState()
}

// ── auto-reconnect ───────────────────────────────────────────────────────────

// OnDiscovered is wired to discovery: dial any trusted, rediscovered peer not yet connected.
func (m *Manager) OnDiscovered(found []discovery.Peer) {
	for _, p := range found {
		if _, ok := m.store.TrustedKey(p.NodeID); ok {
			m.Connect(p)
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (m *Manager) trustLookup(nodeID string) (ed25519.PublicKey, bool) {
	return m.store.TrustedKey(nodeID)
}

// Remembered returns all trusted/paired peers (for the UI's offline list).
func (m *Manager) Remembered() []peers.Peer {
	list, _ := m.store.List()
	return list
}

// IsTrusted reports whether a node id is a remembered, trusted peer.
func (m *Manager) IsTrusted(nodeID string) bool {
	_, ok := m.store.TrustedKey(nodeID)
	return ok
}

// MediaSecret returns the purpose-bound medialink data-plane master for a currently CONNECTED
// peer (medialink transport HKDF-derives its per-direction AEAD keys from it). Second return is
// false unless the peer has a live, connected link. Per-connection → every reconnect rekeys the
// media plane. Both ends compute the identical secret (symmetric session key + transcript).
func (m *Manager) MediaSecret(nodeID string) ([]byte, bool) {
	m.mu.Lock()
	cs := m.conns[nodeID]
	m.mu.Unlock()
	if cs == nil || cs.res == nil || cs.info.Status != StatusConnected {
		return nil, false
	}
	sk, err := mediaSecret(cs.res)
	if err != nil {
		return nil, false
	}
	return sk, true
}

// PeerAddr returns the link address ("host:port") of a currently CONNECTED peer, for routing the
// media data plane toward the same interface the control link uses.
func (m *Manager) PeerAddr(nodeID string) (string, bool) {
	m.mu.Lock()
	cs := m.conns[nodeID]
	m.mu.Unlock()
	if cs == nil || cs.info.Status != StatusConnected || cs.info.Address == "" {
		return "", false
	}
	return cs.info.Address, true
}

// mediaSecret = HKDF(SessionKey, transcript, "rave-peer-media-v1", 32).
func mediaSecret(res *Result) ([]byte, error) {
	return wirecrypto.HkdfSha256(res.SessionKey, res.Transcript, []byte(mediaSecretInfo), 32)
}

// FileSecret returns the purpose-bound filexfer data-plane master for a currently CONNECTED
// peer (same contract as MediaSecret; per-connection → every reconnect rekeys).
func (m *Manager) FileSecret(nodeID string) ([]byte, bool) {
	m.mu.Lock()
	cs := m.conns[nodeID]
	m.mu.Unlock()
	if cs == nil || cs.res == nil || cs.info.Status != StatusConnected {
		return nil, false
	}
	sk, err := fileSecret(cs.res)
	if err != nil {
		return nil, false
	}
	return sk, true
}

// fileSecret = HKDF(SessionKey, transcript, "rave-peer-file-v1", 32).
func fileSecret(res *Result) ([]byte, error) {
	return wirecrypto.HkdfSha256(res.SessionKey, res.Transcript, []byte(fileSecretInfo), 32)
}

// PeerNetStat is one connected peer's cumulative traffic + latency snapshot.
type PeerNetStat struct {
	NodeID, Nickname     string
	BytesIn, BytesOut    uint64  // wire bytes for the current connection
	RTTMs, ClockOffsetMs float64 // valid when HasRTT (offset = peer clock − local clock)
	HasRTT               bool
}

// NetStats snapshots traffic/RTT for every connected peer.
func (m *Manager) NetStats() []PeerNetStat {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PeerNetStat, 0, len(m.conns))
	for _, c := range m.conns {
		if c.info.Status != StatusConnected {
			continue
		}
		in, o, rtt, off, has := c.link.Stats()
		out = append(out, PeerNetStat{
			NodeID: c.info.NodeID, Nickname: c.info.Nickname,
			BytesIn: in, BytesOut: o, RTTMs: rtt, ClockOffsetMs: off, HasRTT: has,
		})
	}
	return out
}

// Connections returns a UI snapshot of all live connections.
func (m *Manager) Connections() []ConnInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ConnInfo, 0, len(m.conns))
	for _, c := range m.conns {
		out = append(out, c.info)
	}
	return out
}

// Forget drops a remembered peer and closes any live connection to it.
func (m *Manager) Forget(nodeID string) {
	_ = m.store.Forget(nodeID)
	m.mu.Lock()
	cs := m.conns[nodeID]
	m.mu.Unlock()
	if cs != nil {
		cs.cancel()
		cs.link.Close()
	}
}

func (m *Manager) fireState() {
	m.mu.Lock()
	cbs := append([]func(){}, m.onState...)
	m.mu.Unlock()
	for _, cb := range cbs {
		cb()
	}
}

func (m *Manager) fireSAS(req SASRequest) {
	m.mu.Lock()
	cbs := append([]func(SASRequest){}, m.onSAS...)
	m.mu.Unlock()
	for _, cb := range cbs {
		cb(req)
	}
}

func hostOf(remoteAddr string) string {
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return h
	}
	return remoteAddr
}
