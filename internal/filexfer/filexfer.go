// Package filexfer transfers files + directories between SAS-paired rave-mate instances.
//
// Transport decision: a dedicated AEAD TCP stream per transfer (ports 47651-47655), NOT
// peerlink control frames. Peerlink wraps every payload as a MAC'd canonical-JSON string -
// fine for control, hostile to multi-GB payloads (base64 + JSON + per-frame MAC on a WS text
// channel). This mirrors the medialink precedent exactly: negotiation (offer/answer) rides
// the existing eventbus control plane, the bulk data rides its own binary listener whose
// AES-256-GCM keys are HKDF-derived from the peerlink handshake (peerlink.Manager.FileSecret,
// domain-separated from MediaSecret). A separate listener is chosen over a new medialink
// stream kind so medialink's frozen v1 media wire stays untouched. Unlike medialink, each
// transfer salts its HKDF with the transfer id, so parallel transfers between the same pair
// never share AEAD keys (no cross-conn nonce reuse).
//
// Flow (sender owns the listener; receiver pulls, which makes resume natural):
//
//	sender                                          receiver
//	  file.offer{id,name,files,bytes,addr} ──bus──▶  policy: enabled? auto|ask
//	           ◀──bus── file.answer{id,accept}
//	  accept ◀──tcp──── dial addr + preamble(id), AEAD conn (initiator=receiver)
//	  manifest ────────▶
//	           ◀──────── have{i} (final file already present) / get{i,offset}
//	  chunks(≤1MiB)… filedone{i,sha256} ─────▶ verify → rename .part → final
//	           ◀──────── done            (or err/cancel any time)
//
// Resume: the receiver writes each file as <dest>.part; on reconnect it hashes the existing
// .part prefix and negotiates the offset via get{i,offset}, so only missing bytes cross the
// wire. Completed files (final exists, size matches) are skipped via have{i}. The sender
// re-offers stalled transfers on peer reconnect + a retry timer; the receiver recognises the
// transfer id and resumes without re-asking the user.
package filexfer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Bus is the control-plane pub/sub negotiation rides (the eventbus; same shape as
// medialink.Bus, adapted by the app).
type Bus interface {
	Publish(topic string, data json.RawMessage)
	Subscribe(topic string, fn func(ev Event)) func()
}

// Event is one bus delivery: Origin = producer node id, Local = produced on this node.
type Event struct {
	Origin string
	Local  bool
	Data   json.RawMessage
}

// SecretProvider yields the per-peer filexfer AEAD master (peerlink.Manager.FileSecret).
// Second return false when there's no live, connected link to the peer.
type SecretProvider interface {
	FileSecret(nodeID string) ([]byte, bool)
}

// Logger is the optional log sink (satisfied by *logbus.Bus). Nil = silent.
type Logger interface {
	Info(tag, msg string, fields map[string]any)
	Warn(tag, msg string, fields map[string]any)
}

const logTag = "filexfer"

// filePortRange is the transfer listener's LAN port range. Distinct from studio
// (47615-47619), ctl (47620), peerlink (47631-47635), and medialink (47641-47645).
var filePortRange = []int{47651, 47652, 47653, 47654, 47655}

// Policy is the receiver-side acceptance policy (live config read via Options.Policy).
type Policy struct {
	Enabled    bool
	Dir        string // destination root for received files
	AutoAccept bool   // false = ask (pending + UI Accept)
}

// State is a transfer's lifecycle phase.
type State string

const (
	StatePending  State = "pending" // receiver: awaiting local accept decision
	StateWaiting  State = "waiting" // sender: offered, awaiting answer/dial
	StateActive   State = "active"
	StateStalled  State = "stalled" // interrupted; retried on peer reconnect
	StateDone     State = "done"
	StateError    State = "error"
	StateCanceled State = "canceled"
)

// Terminal reports whether the state is final.
func (s State) Terminal() bool {
	return s == StateDone || s == StateError || s == StateCanceled
}

// Transfer is one queued/running transfer's UI-facing snapshot.
type Transfer struct {
	ID    string
	Peer  string // peer node id
	Send  bool   // true = outgoing
	Name  string // display name (base of the sent path)
	Files int
	Bytes int64 // total payload bytes
	Done  int64 // bytes transferred (+ skipped as already present)
	Rate  float64
	State State
	Error string
	Path  string // send: source root; recv: destination root dir
	At    time.Time
}

// Options configures a Manager. Self, Bus, Secrets, Policy are required.
type Options struct {
	Self       string
	Bus        Bus
	Secrets    SecretProvider
	Policy     func() Policy // live receiver policy (config read)
	Log        Logger        // optional
	AdvertHost string        // host placed in Offer.Addr; default: autodetected LAN IPv4
	Ports      []int         // listener candidates; default filePortRange; []int{0} = ephemeral (tests)
}

// xfer is the manager-internal transfer state (Transfer snapshot + protocol context).
type xfer struct {
	Transfer
	files     []FileEntry
	addr      string             // recv: sender addr to dial
	accepted  bool               // recv: user/policy said yes (re-offers resume silently)
	cancelReq bool               // local cancel requested (conn error → canceled, not stalled)
	retries   int                // send: re-offer attempts since last progress
	cancel    context.CancelFunc // active session cancel; nil when idle
	answerT   *time.Timer        // send: offer-answer timeout
	retryT    *time.Timer        // send: stalled re-offer timer
	prevDone  int64              // rate window
	prevAt    time.Time
}

// Manager owns the transfer listener, negotiation, and queue. Both roles (send + receive)
// live here. Create with New, then Start. Safe for concurrent use.
type Manager struct {
	self    string
	bus     Bus
	secrets SecretProvider
	policy  func() Policy
	log     Logger
	host    string
	ports   []int

	notify func(title, body string) // optional UI toast seam

	retryAfter time.Duration // stalled re-offer delay (test-tunable)
	answerWait time.Duration // offer-answer timeout (test-tunable)
	maxRetries int           // re-offer budget until progress

	mu     sync.Mutex
	ln     net.Listener
	addr   string
	xfers  map[string]*xfer
	order  []string // insertion order for Transfers()
	subs   map[int]func(Transfer)
	subSeq int
	unsub  []func()
	ctx    context.Context
	cancel context.CancelFunc
}

// New builds a Manager (does not bind - call Start).
func New(opts Options) *Manager {
	host := opts.AdvertHost
	if host == "" {
		host = localIPv4()
	}
	ports := opts.Ports
	if ports == nil {
		ports = filePortRange
	}
	return &Manager{
		self: opts.Self, bus: opts.Bus, secrets: opts.Secrets, policy: opts.Policy,
		log: opts.Log, host: host, ports: ports,
		retryAfter: 10 * time.Second, answerWait: 30 * time.Second, maxRetries: 30,
		xfers: map[string]*xfer{}, subs: map[int]func(Transfer){},
	}
}

// SetNotify attaches the desktop-toast seam (incoming pending transfers).
func (m *Manager) SetNotify(fn func(title, body string)) {
	m.mu.Lock()
	m.notify = fn
	m.mu.Unlock()
}

// Start binds the transfer listener and subscribes the negotiation topics.
func (m *Manager) Start(ctx context.Context) error {
	ln, err := listenRange(m.ports)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.ln = ln
	m.ctx, m.cancel = cctx, cancel
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	m.addr = net.JoinHostPort(m.host, portStr)
	m.unsub = []func(){
		m.bus.Subscribe(TopicOffer, m.onOffer),
		m.bus.Subscribe(TopicAnswer, m.onAnswer),
	}
	m.mu.Unlock()
	go m.acceptLoop(cctx, ln)
	m.infof("file listener", map[string]any{"addr": m.addr})
	return nil
}

// Stop closes the listener, cancels active sessions, and unsubscribes. Non-terminal
// transfers stall (resumable on the next Start).
func (m *Manager) Stop() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	ln := m.ln
	unsub := m.unsub
	m.ln, m.unsub = nil, nil
	var cancels []context.CancelFunc
	for _, x := range m.xfers {
		if x.cancel != nil {
			cancels = append(cancels, x.cancel)
		}
		stopTimersLocked(x)
	}
	m.mu.Unlock()
	for _, u := range unsub {
		u()
	}
	for _, c := range cancels {
		c()
	}
	if ln != nil {
		_ = ln.Close()
	}
}

// started reports whether Start has run (listener bound).
func (m *Manager) started() bool { m.mu.Lock(); defer m.mu.Unlock(); return m.ln != nil }

// ── FileXfer surface (Services-level API) ─────────────────────────────────────

// SendToPeer queues a file or directory (recursive) for transfer to a paired instance and
// returns the transfer id. The peer must be reachable when the transfer runs; offers are
// retried while it isn't.
func (m *Manager) SendToPeer(nodeID, path string) (string, error) {
	if !m.started() {
		return "", errors.New("file transfer is off - enable it in the Peers tab")
	}
	if nodeID == "" {
		return "", errors.New("no peer selected")
	}
	name, files, total, err := BuildManifest(path)
	if err != nil {
		return "", err
	}
	x := &xfer{
		Transfer: Transfer{ID: newID(), Peer: nodeID, Send: true, Name: name,
			Files: len(files), Bytes: total, State: StateWaiting, Path: filepath.Clean(path), At: time.Now()},
		files: files,
	}
	m.mu.Lock()
	m.xfers[x.ID] = x
	m.order = append(m.order, x.ID)
	m.mu.Unlock()
	m.publishOffer(x)
	m.emit(x.ID)
	return x.ID, nil
}

// Transfers snapshots every known transfer, newest first.
func (m *Manager) Transfers() []Transfer {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Transfer, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		if x := m.xfers[m.order[i]]; x != nil {
			out = append(out, x.snapshot())
		}
	}
	return out
}

// Cancel aborts a transfer (any state; terminal is a no-op).
func (m *Manager) Cancel(id string) {
	m.mu.Lock()
	x := m.xfers[id]
	if x == nil || x.State.Terminal() {
		m.mu.Unlock()
		return
	}
	x.cancelReq = true
	stopTimersLocked(x)
	cancel := x.cancel
	pendingRecv := !x.Send && x.State == StatePending
	if cancel == nil {
		x.State, x.Error = StateCanceled, ""
	}
	m.mu.Unlock()
	if pendingRecv {
		m.publishAnswer(Answer{ID: id, Accept: false, Reason: reasonDeclined})
	}
	if cancel != nil {
		cancel() // session loop settles the state (canceled via cancelReq)
	}
	m.emit(id)
}

// Accept resolves a pending incoming transfer: ok starts the download, !ok declines.
func (m *Manager) Accept(id string, ok bool) {
	m.mu.Lock()
	x := m.xfers[id]
	if x == nil || x.Send || x.State != StatePending {
		m.mu.Unlock()
		return
	}
	if !ok {
		x.State = StateCanceled
		m.mu.Unlock()
		m.publishAnswer(Answer{ID: id, Accept: false, Reason: reasonDeclined})
		m.emit(id)
		return
	}
	x.accepted = true
	x.State = StateWaiting
	m.mu.Unlock()
	m.publishAnswer(Answer{ID: id, Accept: true})
	m.emit(id)
	go m.dialPull(id)
}

// Subscribe registers a progress/state listener (called off-lock, any goroutine).
// Returns the unsubscribe func.
func (m *Manager) Subscribe(fn func(Transfer)) func() {
	m.mu.Lock()
	m.subSeq++
	id := m.subSeq
	m.subs[id] = fn
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.subs, id)
		m.mu.Unlock()
	}
}

// Pending returns incoming transfers awaiting a local accept decision.
func (m *Manager) Pending() []Transfer {
	var out []Transfer
	for _, t := range m.Transfers() {
		if !t.Send && t.State == StatePending {
			out = append(out, t)
		}
	}
	return out
}

// PeerStateChanged retries stalled sends (hook to peerlink's state listener).
func (m *Manager) PeerStateChanged() {
	m.mu.Lock()
	var retry []*xfer
	for _, x := range m.xfers {
		if x.Send && x.State == StateStalled {
			x.retries = 0
			retry = append(retry, x)
		}
	}
	m.mu.Unlock()
	for _, x := range retry {
		m.reoffer(x.ID)
	}
}

// ── negotiation ───────────────────────────────────────────────────────────────

// publishOffer emits the offer + arms the answer timeout.
func (m *Manager) publishOffer(x *xfer) {
	m.mu.Lock()
	if m.ln == nil || x.State.Terminal() {
		m.mu.Unlock()
		return
	}
	x.State = StateWaiting
	off := Offer{ID: x.ID, Target: x.Peer, Name: x.Name, Files: x.Files, Bytes: x.Bytes, Addr: m.addr}
	stopTimersLocked(x)
	id := x.ID
	x.answerT = time.AfterFunc(m.answerWait, func() { m.stallSend(id, "no answer from the paired instance") })
	m.mu.Unlock()
	if raw, err := json.Marshal(off); err == nil {
		m.bus.Publish(TopicOffer, raw)
	}
}

// reoffer re-publishes a stalled send (resume path).
func (m *Manager) reoffer(id string) {
	m.mu.Lock()
	x := m.xfers[id]
	if x == nil || !x.Send || x.State != StateStalled {
		m.mu.Unlock()
		return
	}
	if x.retries++; x.retries > m.maxRetries {
		x.State, x.Error = StateError, "gave up retrying - the paired instance is unreachable"
		stopTimersLocked(x)
		m.mu.Unlock()
		m.emit(id)
		return
	}
	m.mu.Unlock()
	m.publishOffer(x)
	m.emit(id)
}

// stallSend marks a send stalled + schedules the next re-offer.
func (m *Manager) stallSend(id, why string) {
	m.mu.Lock()
	x := m.xfers[id]
	if x == nil || x.State.Terminal() || x.State == StateActive {
		m.mu.Unlock()
		return
	}
	if x.cancelReq {
		x.State = StateCanceled
		stopTimersLocked(x)
		m.mu.Unlock()
		m.emit(id)
		return
	}
	x.State, x.Error = StateStalled, why
	stopTimersLocked(x)
	x.retryT = time.AfterFunc(m.retryAfter, func() { m.reoffer(id) })
	m.mu.Unlock()
	m.emit(id)
}

// onOffer (receiver): apply policy, answer, and pull on accept.
func (m *Manager) onOffer(ev Event) {
	if ev.Local {
		return
	}
	var off Offer
	if json.Unmarshal(ev.Data, &off) != nil || off.ID == "" || off.Target != m.self {
		return
	}
	pol := m.policy()
	if !pol.Enabled {
		m.publishAnswer(Answer{ID: off.ID, Accept: false, Reason: reasonDisabled})
		return
	}
	if off.Files < 0 || off.Bytes < 0 || off.Addr == "" || !safeName(off.Name) {
		m.publishAnswer(Answer{ID: off.ID, Accept: false, Reason: "malformed offer"})
		return
	}
	m.mu.Lock()
	x := m.xfers[off.ID]
	if x != nil && (x.Send || x.Peer != ev.Origin) {
		m.mu.Unlock()
		return // id collision/spoof - ignore
	}
	fresh := x == nil
	if fresh {
		x = &xfer{Transfer: Transfer{ID: off.ID, Peer: ev.Origin, Name: off.Name,
			Files: off.Files, Bytes: off.Bytes, State: StatePending, Path: pol.Dir, At: time.Now()}}
		m.xfers[x.ID] = x
		m.order = append(m.order, x.ID)
	}
	if x.State.Terminal() {
		declined := x.State == StateCanceled
		m.mu.Unlock()
		if declined {
			m.publishAnswer(Answer{ID: off.ID, Accept: false, Reason: reasonDeclined})
		}
		return // done/error: don't resurrect
	}
	x.addr = off.Addr
	if x.State == StateActive {
		m.mu.Unlock()
		return // already pulling
	}
	resume := x.accepted // previously accepted → silent resume
	auto := pol.AutoAccept
	notify := m.notify
	if resume || auto {
		x.accepted = true
		x.State = StateWaiting
	}
	m.mu.Unlock()
	if resume || auto {
		m.publishAnswer(Answer{ID: off.ID, Accept: true})
		m.emit(off.ID)
		go m.dialPull(off.ID)
		return
	}
	m.emit(off.ID)
	if fresh && notify != nil {
		notify("Incoming file transfer",
			fmt.Sprintf("A paired instance wants to send %q (%s). Review it in the Peers tab.",
				off.Name, FmtBytes(off.Bytes)))
	}
}

// onAnswer (sender): accepted → wait for the dial; rejected → error/canceled.
func (m *Manager) onAnswer(ev Event) {
	if ev.Local {
		return
	}
	var ans Answer
	if json.Unmarshal(ev.Data, &ans) != nil || ans.ID == "" {
		return
	}
	m.mu.Lock()
	x := m.xfers[ans.ID]
	if x == nil || !x.Send || x.Peer != ev.Origin || x.State.Terminal() {
		m.mu.Unlock()
		return
	}
	if ans.Accept {
		// Keep waiting: the receiver dials next; the answer timer keeps running as the
		// dial deadline and re-arms the retry path if no connection arrives.
		m.mu.Unlock()
		return
	}
	stopTimersLocked(x)
	switch ans.Reason {
	case reasonDeclined:
		x.State, x.Error = StateCanceled, "declined by the paired instance"
	default:
		x.State, x.Error = StateError, ans.Reason
	}
	m.mu.Unlock()
	m.emit(ans.ID)
}

func (m *Manager) publishAnswer(a Answer) {
	if raw, err := json.Marshal(a); err == nil {
		m.bus.Publish(TopicAnswer, raw)
	}
}

// ── shared session state helpers ─────────────────────────────────────────────

// beginSession moves a transfer to active and installs its cancel; returns false when the
// transfer can't run (terminal/duplicate session).
func (m *Manager) beginSession(id string, cancel context.CancelFunc) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	x := m.xfers[id]
	if x == nil || x.State.Terminal() || x.cancel != nil {
		return false
	}
	stopTimersLocked(x)
	x.cancel = cancel
	x.State, x.Error = StateActive, ""
	x.Done, x.prevDone, x.Rate, x.prevAt = 0, 0, 0, time.Now()
	return true
}

// endSession settles a finished session: err == nil → done, cancelReq → canceled, else
// stalled (sender re-offers; receiver waits for the re-offer).
func (m *Manager) endSession(id string, err error) {
	m.mu.Lock()
	x := m.xfers[id]
	if x == nil {
		m.mu.Unlock()
		return
	}
	if x.cancel != nil {
		x.cancel = nil
	}
	send := x.Send
	var why string
	switch {
	case x.State.Terminal():
	case err == nil:
		x.State, x.Error, x.Rate = StateDone, "", 0
		x.Done = x.Bytes
	case x.cancelReq:
		x.State = StateCanceled
	case errors.Is(err, errRemoteCanceled):
		x.State, x.Error = StateCanceled, err.Error()
	default:
		var pe *protoErr
		if errors.As(err, &pe) {
			x.State, x.Error = StateError, pe.msg // protocol-fatal (bad hash, bad manifest…)
		} else {
			x.State, x.Error, why = StateStalled, err.Error(), err.Error()
		}
	}
	stopTimersLocked(x)
	stalled := x.State == StateStalled
	m.mu.Unlock()
	if stalled && send {
		m.stallSend(id, why) // arms the retry timer
		return
	}
	m.emit(id)
}

// addProgress accumulates transferred bytes + updates the rate window.
func (m *Manager) addProgress(id string, n int64) {
	m.mu.Lock()
	x := m.xfers[id]
	if x == nil {
		m.mu.Unlock()
		return
	}
	x.Done += n
	now := time.Now()
	if dt := now.Sub(x.prevAt); dt >= 500*time.Millisecond {
		x.Rate = float64(x.Done-x.prevDone) / dt.Seconds()
		x.prevDone, x.prevAt = x.Done, now
	}
	m.mu.Unlock()
	m.emit(id)
}

// canceledLocally reports whether a local Cancel hit this transfer (checked between chunks).
func (m *Manager) canceledLocally(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	x := m.xfers[id]
	return x == nil || x.cancelReq
}

// emit fans the transfer snapshot out to subscribers.
func (m *Manager) emit(id string) {
	m.mu.Lock()
	x := m.xfers[id]
	if x == nil {
		m.mu.Unlock()
		return
	}
	snap := x.snapshot()
	fns := make([]func(Transfer), 0, len(m.subs))
	for _, fn := range m.subs {
		fns = append(fns, fn)
	}
	m.mu.Unlock()
	for _, fn := range fns {
		fn(snap)
	}
}

func (x *xfer) snapshot() Transfer {
	t := x.Transfer
	if t.State != StateActive {
		t.Rate = 0
	}
	return t
}

// stopTimersLocked stops the answer/retry timers. Caller holds m.mu.
func stopTimersLocked(x *xfer) {
	if x.answerT != nil {
		x.answerT.Stop()
		x.answerT = nil
	}
	if x.retryT != nil {
		x.retryT.Stop()
		x.retryT = nil
	}
}

// ── misc ─────────────────────────────────────────────────────────────────────

func (m *Manager) infof(msg string, f map[string]any) {
	if m.log != nil {
		m.log.Info(logTag, msg, f)
	}
}
func (m *Manager) warnf(msg string, f map[string]any) {
	if m.log != nil {
		m.log.Warn(logTag, msg, f)
	}
}

func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// FmtBytes renders a byte count human-readably ("3.2 MB").
func FmtBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

func listenRange(ports []int) (net.Listener, error) {
	var lastErr error
	for _, p := range ports {
		ln, err := net.Listen("tcp", "0.0.0.0:"+strconv.Itoa(p))
		if err == nil {
			return ln, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("filexfer: no listener ports configured")
	}
	return nil, lastErr
}

// localIPv4 picks a routable LAN IPv4 for Offer.Addr; falls back to loopback.
func localIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip4 := ipn.IP.To4(); ip4 != nil && !ip4.IsLoopback() {
					return ip4.String()
				}
			}
		}
	}
	return "127.0.0.1"
}
