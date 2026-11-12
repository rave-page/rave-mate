package medialink

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// udpconn.go is the §2.5 UDP datagram media connection (Unit 2). It is a drop-in frameConn (same
// WriteFrame/ReadFrame/Close surface as the TCP *Conn) that rides a single net.PacketConn to one
// peer, so the router's send/recv loops use it unchanged. On top of the Unit-1 datagram codec it
// adds: a token-bucket SEND PACER (no keyframe microburst), transport-level FRAGMENT NACK +
// retransmit (bounded ring, budgeted, deadline-abandoned), DEADLINE DROPS of frames that cannot
// reassemble in time (the router then sees the frame gap and PLIs), and KEEPALIVE/LIVENESS so a
// dead route is detected in ≤ Liveness and torn down with no leaked goroutines. Every buffer is
// bounded with an explicit cap + drop policy (CLAUDE.md data-path rule).

// frameConn is the transport seam the router uses: both the TCP *Conn and the UDP *udpConn satisfy
// it, so a route is transport-agnostic above this line.
type frameConn interface {
	WriteFrame(*Frame) error
	ReadFrame() (*Frame, error)
	Close() error
}

var (
	_ frameConn = (*Conn)(nil)
	_ frameConn = (*udpConn)(nil)
)

// errUDPDead is returned by ReadFrame when the media plane goes silent past the liveness timeout (a
// UDP blackout). The router uses it as the §-fallback trigger to renegotiate onto TCP (Unit 3).
var errUDPDead = errors.New("medialink: udp route blackout (no datagrams within liveness window)")

// errUDPEnded is returned by ReadFrame when the peer sent an explicit clean end-of-stream (dgTypeBye):
// the remote source ended (EOF) or its route was stopped. Distinct from errUDPDead so the receiver
// reports "remote source ended" immediately instead of waiting out the liveness window and logging a
// (misleading) blackout. A stalled/crashed sender that cannot send a bye still surfaces as errUDPDead.
var errUDPEnded = errors.New("medialink: remote source ended (clean end-of-stream)")

var errConnClosed = errors.New("medialink: conn closed")

// packetConn is the subset of net.PacketConn the udpConn needs (so the in-process impairment shim
// can stand in for a real socket in tests).
type packetConn interface {
	ReadFrom(p []byte) (int, net.Addr, error)
	WriteTo(p []byte, addr net.Addr) (int, error)
	SetReadDeadline(t time.Time) error
	Close() error
}

// udpConfig tunes a udpConn. Zero fields take the defaults below.
type udpConfig struct {
	Token         uint32        // per-route token (derived from MediaSecret + route salt; brief req 5)
	Initiator     bool          // AEAD/role direction
	Master        []byte        // peerlink media secret; required when Encrypt
	Salt          []byte        // per-route key salt (routeKeySalt); makes keys/nonce-prefix per-route
	Encrypt       bool          // true = per-datagram AEAD; false = cleartext (token-gated only)
	Clock         ClockSource   // media clock for deadlines + timestamps (required)
	BodyMax       int           // fragment body size (default dgDefaultPayload)
	FrameDeadline time.Duration // incomplete-reassembly drop + retransmit abandon (default 200ms)
	Keepalive     time.Duration // ping cadence (default 500ms)
	Liveness      time.Duration // no-authenticated-datagram timeout → errUDPDead (default 2s)
	StartupGrace  time.Duration // liveness window until first real traffic establishes the route (default 15s)
	PacingRate    float64       // initial pacer rate, bytes/s (0 = unpaced)
	Log           func(string, map[string]any)
}

const (
	udpDefFrameDeadline = 200 * time.Millisecond
	udpDefKeepalive     = 500 * time.Millisecond
	udpDefLiveness      = 2 * time.Second
	// udpDefStartupGrace is the liveness window BEFORE the route is proven established end-to-end (the
	// first authenticated media or peer keepalive-ping arrives). A cold sender needs ~1.6-2s to open
	// PipeWire capture + the VA encoder before it emits anything, and a cold receiver can take seconds
	// to open a DXVA decoder under VRAM pressure; the tight udpDefLiveness window would kill the route
	// mid-startup (the first frame once landed in the same ms the route was declared dead). The normal
	// window applies only AFTER first real traffic.
	udpDefStartupGrace  = 15 * time.Second
	udpMaintTick        = 5 * time.Millisecond   // NACK-scan + sweep cadence
	udpRebindMin        = time.Second            // min interval between authenticated remote rebinds
	owdBucketNs         = int64(2 * time.Second) // one-way-delay baseline window bucket (queuing-delay min)
	udpNackStale        = 3 * time.Millisecond   // sub-ms-reorder floor before a proven hole is NACKed
	udpNackRetry        = 8 * time.Millisecond   // min gap between re-NACKs of the same frame
	udpNackDeadlineLead = 30 * time.Millisecond  // last-chance NACK this long before the reassembly deadline
	udpRecvQueue        = 128                    // completed-frame handoff queue (newest-wins on overflow)
	udpRingFrames       = 2048                   // retransmit ring datagram cap
	udpRingBytes        = 4 << 20                // retransmit ring byte cap
	udpRetransFrac      = 0.25                   // retransmit bytes ≤ this fraction of the send rate
	udpReadBufBytes     = 1 << 20                // SO_RCVBUF/SO_SNDBUF best-effort target
)

// UDPStats is a UDP-transport telemetry snapshot (§7 + the Unit-4 congestion controller). Crosses
// the featurehost telemetry wire (JSON), so fields are tagged + exported.
type UDPStats struct {
	Encrypted     bool    `json:"encrypted,omitempty"`
	LossPct       float64 `json:"lossPct,omitempty"`     // datagram loss over the last interval (congestion input)
	RTTns         int64   `json:"rttNs,omitempty"`       // smoothed round-trip (keepalive ping/pong)
	OwdJitterNs   int64   `json:"owdJitterNs,omitempty"` // one-way-delay jitter estimate (datagram arrival)
	OwdNs         int64   `json:"owdNs,omitempty"`       // smoothed signed one-way delay (delay-gradient input)
	NacksSent     uint64  `json:"nacksSent,omitempty"`
	NacksRecv     uint64  `json:"nacksRecv,omitempty"`
	Retransmits   uint64  `json:"retransmits,omitempty"`   // datagrams re-sent on a fragment NACK
	RetransBytes  uint64  `json:"retransBytes,omitempty"`  //
	ReasmDrops    uint64  `json:"reasmDrops,omitempty"`    // frames dropped at reassembly (cap/deadline)
	PacerDepth    int     `json:"pacerDepth,omitempty"`    // datagrams queued in the pacer
	PacerDrops    uint64  `json:"pacerDrops,omitempty"`    // datagrams dropped on pacer overflow (keyframe-aware)
	RejectedDgram uint64  `json:"rejectedDgram,omitempty"` // mode/token/open failures (never update liveness/bind)
	DupDgram      uint64  `json:"dupDgram,omitempty"`      // replay-window duplicates
	Rebinds       uint64  `json:"rebinds,omitempty"`       // authenticated remote rebinds (NAT); 0 in the steady state
}

// udpConn is one peer-to-peer UDP media route. Safe for concurrent WriteFrame callers (router runs
// media + several meta writers); one recv goroutine owns the receive state.
type udpConn struct {
	pc         packetConn
	remote     atomic.Value // net.Addr send target; bound/rebound ONLY from an authenticated datagram
	lastRebind atomic.Int64 // media-clock ns of the last remote rebind (rate limit)
	clock      ClockSource
	cfg        udpConfig
	crypto     *dgCrypto // nil = cleartext route
	token      uint32
	deadline   time.Duration
	logf       func(string, map[string]any)

	// send side (guarded by encMu: encoder seq + retransmit ring are per-direction state).
	encMu sync.Mutex
	enc   *dgEncoder
	ring  *dgRing
	pacer *sendPacer
	retr  *sendPacer // retransmit budget bucket (frac of the send rate)

	sendCh chan []dgram // paced media groups
	ctrlCh chan []byte  // pings/pongs/NACKs (minimal pacing)
	recvCh chan *Frame  // completed frames → ReadFrame

	// recv side (recvLoop goroutine only, except atomics).
	reasm   *reassembler
	seqTrk  *replayWin
	lastRTx map[uint16]int64 // per-stream last transit (OWD jitter)
	// owd queuing-delay baseline (recvLoop-owned, no lock): windowed-min transit over 2 buckets.
	owdBucketMin int64
	owdBucketAt  int64
	owdHasBucket bool
	owdPrevMin   int64
	owdHasPrev   bool

	lastRecvNs  atomic.Int64 // media-clock ns of the last AUTHENTICATED datagram (liveness)
	established atomic.Bool  // route proven live (first authenticated media or peer keepalive-ping); gates the liveness window
	rttNs       atomic.Int64
	owdJitter   atomic.Int64 // |transit delta| EWMA (telemetry)
	owdNs       atomic.Int64 // smoothed QUEUING delay above baseline, ns (offset-free; cc gradient input)

	st udpStatsMu

	closeOnce sync.Once
	done      chan struct{}
	deadErr   atomic.Pointer[error]
	wg        sync.WaitGroup

	reachOnce   sync.Once
	reachableCh chan struct{} // closed on the first authenticated datagram (reachability handshake)
}

// dgram pairs a datagram's bytes with its priority class (keyframe groups survive pacer overflow).
type dgram struct {
	buf      []byte
	keyframe bool
	frameSeq uint32
	stream   uint16
}

type udpStatsMu struct {
	mu                        sync.Mutex
	nacksSent, nacksRecv      uint64
	retransmits, retransBytes uint64
	reasmDrops                uint64
	pacerDrops                uint64
	rejected, dup             uint64
	rebinds                   uint64
}

// newUDPConn builds + starts a UDP media connection over pc toward remote. remote may be nil (the
// answerer learns it from the first AUTHENTICATED datagram); a non-nil remote (the dialer) is still
// only the send target until the first authenticated datagram confirms it.
func newUDPConn(pc packetConn, remote net.Addr, cfg udpConfig) (*udpConn, error) {
	if cfg.Clock == nil {
		return nil, errors.New("medialink: udpConn needs a clock")
	}
	deadline := cfg.FrameDeadline
	if deadline <= 0 {
		deadline = udpDefFrameDeadline
	}
	var crypto *dgCrypto
	if cfg.Encrypt {
		c, err := newDgCrypto(cfg.Master, cfg.Salt, cfg.Initiator)
		if err != nil {
			return nil, err
		}
		crypto = c
	}
	now := func() time.Time { return time.Unix(0, cfg.Clock.Now()) }
	u := &udpConn{
		pc: pc, clock: cfg.Clock, cfg: cfg, crypto: crypto, token: cfg.Token,
		deadline: deadline, logf: cfg.Log,
		enc:         newDgEncoder(cfg.Token, crypto, cfg.BodyMax),
		ring:        newDgRing(udpRingFrames, udpRingBytes),
		pacer:       newSendPacer(cfg.PacingRate, now),
		retr:        newSendPacer(cfg.PacingRate*udpRetransFrac, now),
		sendCh:      make(chan []dgram, 64),
		ctrlCh:      make(chan []byte, 64),
		recvCh:      make(chan *Frame, udpRecvQueue),
		reasm:       newReassembler(64, 32<<20),
		seqTrk:      newReplayWin(),
		lastRTx:     map[uint16]int64{},
		done:        make(chan struct{}),
		reachableCh: make(chan struct{}),
	}
	if remote != nil {
		u.remote.Store(remote) // dialer's tentative send target (confirmed on the first authenticated datagram)
	}
	u.lastRecvNs.Store(cfg.Clock.Now())
	bestEffortUDPBuffers(pc)
	u.wg.Add(3)
	go u.sendLoop()
	go u.recvLoop()
	go u.maintLoop()
	return u, nil
}

// SetPacingRate updates the pacer + retransmit budget live (Unit 4 congestion controller output).
func (u *udpConn) SetPacingRate(bytesPerSec float64) {
	u.pacer.setRate(bytesPerSec)
	u.retr.setRate(bytesPerSec * udpRetransFrac)
}

// WriteFrame encodes f into datagrams, records them in the retransmit ring, and hands the group to
// the pacer. Media groups that overflow the bounded pacer queue are dropped keyframe-aware (never
// accumulate); meta/keyframe groups are protected. Safe for concurrent callers.
func (u *udpConn) WriteFrame(f *Frame) error {
	if u.isDead() {
		return u.err()
	}
	u.encMu.Lock()
	dgs, _, _, err := u.enc.encodeFrame(nil, f)
	if err != nil {
		u.encMu.Unlock()
		return err
	}
	deadlineNs := u.clock.Now() + int64(u.deadline)
	key := f.Keyframe() || f.Stream == metaStream // protected from pacer-overflow drops
	group := make([]dgram, 0, len(dgs))
	for i, b := range dgs {
		u.ring.add(dgKey(f.Stream, f.Seq, uint16(i)), b, deadlineNs)
		group = append(group, dgram{buf: b, keyframe: key, frameSeq: f.Seq, stream: f.Stream})
	}
	u.encMu.Unlock()
	select {
	case u.sendCh <- group:
		return nil
	case <-u.done:
		return u.err()
	default:
		// Pacer queue full: drop THIS group unless it is protected (keyframe/meta); a protected
		// group blocks briefly rather than being dropped (bounded by done).
		if !key {
			u.st.mu.Lock()
			u.st.pacerDrops += uint64(len(group))
			u.st.mu.Unlock()
			return nil
		}
		select {
		case u.sendCh <- group:
			return nil
		case <-u.done:
			return u.err()
		}
	}
}

// ReadFrame returns the next reassembled frame, errUDPDead on a liveness blackout, or a close error.
func (u *udpConn) ReadFrame() (*Frame, error) {
	select {
	case f := <-u.recvCh:
		if f == nil {
			return nil, u.err()
		}
		return f, nil
	case <-u.done:
		// Drain any frame already queued before reporting the end.
		select {
		case f := <-u.recvCh:
			if f != nil {
				return f, nil
			}
		default:
		}
		return nil, u.err()
	}
}

// Close tears the conn down: stop goroutines, close the socket, unblock ReadFrame. Idempotent.
func (u *udpConn) Close() error {
	u.closeOnce.Do(func() {
		u.setErr(errConnClosed)
		close(u.done)
		_ = u.pc.Close() // unblocks recvLoop's ReadFrom
	})
	u.wg.Wait()
	return nil
}

// waitReachable blocks until the first authenticated datagram arrives (the §-reachability handshake:
// the peer answered our keepalive ping), the conn dies, or timeout. Returns whether the peer is
// reachable - the dialer's UDP-vs-TCP decision (brief req 4).
func (u *udpConn) waitReachable(timeout time.Duration) bool {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-u.reachableCh:
		return true
	case <-u.done:
		return false
	case <-t.C:
		return false
	}
}

func (u *udpConn) isDead() bool { return u.deadErr.Load() != nil }

func (u *udpConn) setErr(err error) { u.deadErr.CompareAndSwap(nil, &err) }

func (u *udpConn) err() error {
	if p := u.deadErr.Load(); p != nil {
		return *p
	}
	return errConnClosed
}

// Stats snapshots the UDP transport counters (§7 + congestion controller input).
func (u *udpConn) Stats() UDPStats {
	u.st.mu.Lock()
	s := UDPStats{
		NacksSent: u.st.nacksSent, NacksRecv: u.st.nacksRecv,
		Retransmits: u.st.retransmits, RetransBytes: u.st.retransBytes,
		ReasmDrops: u.st.reasmDrops, PacerDrops: u.st.pacerDrops,
		RejectedDgram: u.st.rejected, DupDgram: u.st.dup, Rebinds: u.st.rebinds,
	}
	u.st.mu.Unlock()
	s.Encrypted = u.crypto != nil
	s.RTTns = u.rttNs.Load()
	s.OwdJitterNs = u.owdJitter.Load()
	s.OwdNs = u.owdNs.Load()
	s.LossPct = u.seqTrk.lossFraction() * 100
	s.PacerDepth = len(u.sendCh)
	return s
}

// ── send ──────────────────────────────────────────────────────────────────────

func (u *udpConn) sendLoop() {
	defer u.wg.Done()
	for {
		select {
		case <-u.done:
			return
		case b := <-u.ctrlCh: // control (ping/pong/NACK): minimal pacing, high priority
			u.paceWrite(b)
		case g := <-u.sendCh:
			for _, d := range g {
				u.paceWrite(d.buf)
			}
		}
	}
}

// paceWrite waits for the pacer then writes one datagram. A pacing wait is interruptible by Close.
func (u *udpConn) paceWrite(b []byte) {
	if wait := u.pacer.reserve(len(b)); wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-u.done:
			t.Stop()
			return
		case <-t.C:
		}
	}
	r := u.remoteAddr()
	if r == nil {
		return // no authenticated remote yet (answerer pre-handshake): never send to an unverified addr
	}
	_, _ = u.pc.WriteTo(b, r)
}

// remoteAddr loads the current authenticated send target (nil if not yet bound).
func (u *udpConn) remoteAddr() net.Addr {
	if v := u.remote.Load(); v != nil {
		return v.(net.Addr)
	}
	return nil
}

// sendBye tells the peer this side is ending the route cleanly (source EOF / route stop) so the
// receiver ends with errUDPEnded immediately instead of waiting out the liveness window and reporting
// a blackout. Best-effort and SYNCHRONOUS (the conn is about to close, so it bypasses the pacer and
// the sendLoop, which may already be torn down). An old peer ignores the unknown datagram type, so a
// mixed-version pair simply keeps the old 2 s blackout behaviour (no regression).
func (u *udpConn) sendBye() {
	r := u.remoteAddr()
	if r == nil {
		return // no authenticated remote: nothing to notify
	}
	b := u.buildControl(dgTypeBye, nil)
	if b == nil {
		return // encoder seq exhausted
	}
	_, _ = u.pc.WriteTo(b, r)
}

// sendControl queues a control datagram (never dropped silently; bounded channel).
func (u *udpConn) sendControlDatagram(b []byte) {
	select {
	case u.ctrlCh <- b:
	case <-u.done:
	default: // control backlog (unreachable in practice): drop the oldest, keep the newest
		select {
		case <-u.ctrlCh:
		default:
		}
		select {
		case u.ctrlCh <- b:
		default:
		}
	}
}

// ── receive ─────────────────────────────────────────────────────────────────────

func (u *udpConn) recvLoop() {
	defer u.wg.Done()
	buf := make([]byte, dgMaxPayload+dgHeaderLen+64)
	for {
		n, src, err := u.pc.ReadFrom(buf)
		if err != nil {
			select {
			case <-u.done:
				return // closed
			default:
			}
			// A read error on a live conn is treated as a transient (deadline) read; loop. A
			// permanent error surfaces via the liveness timeout instead of a hard kill here.
			continue
		}
		u.handleDatagram(buf[:n], src)
	}
}

func (u *udpConn) handleDatagram(dg []byte, src net.Addr) {
	h, off, err := parseDatagramHeader(dg)
	if err != nil {
		u.bump(&u.st.rejected)
		return // malformed: never counts as liveness
	}
	chunk, err := decodeBody(h, dg[off:], u.token, u.crypto, dg[:off])
	if err != nil {
		u.bump(&u.st.rejected) // mode/token/open failure: NOT alive, NOT a bind trigger (flood-safe)
		return
	}
	// Replay/sequence check BEFORE any rebind or liveness refresh: a REPLAYED authenticated datagram
	// (captured + re-sent from another address) must change nothing - otherwise it could redirect the
	// send target (stream hijack) or keep a dead route alive, on AEAD routes too, since AEAD does not
	// prevent replay. Only a STRICTLY-NEWEST datagram (stNew - a replay can't forge one) may rebind
	// the remote, refresh liveness, and signal reachability. A gap-filling retransmit (stFill) is
	// delivered for recovery but grants none of that leverage.
	st := u.seqTrk.accept(h.Seq)
	switch st {
	case stDup, stTooOld:
		u.bump(&u.st.dup)
		return // replay / duplicate / too-old: no rebind, no liveness, no reach, no processing
	case stNew:
		u.bindRemote(src)                               // rate-limited inside; authenticated + newest only
		u.lastRecvNs.Store(u.clock.Now())               // liveness: a genuinely new datagram arrived
		u.reachOnce.Do(func() { close(u.reachableCh) }) // reachability handshake
	}
	// Establish the route on the first real traffic: a media datagram, or a peer keepalive PING (proves
	// the peer's maintLoop is up and emitting, not just a one-shot reachability pong). After this the
	// tight liveness window applies; before it the startup grace tolerates a cold sender/receiver.
	if h.Typ == dgTypeMedia || h.Typ == dgTypePing {
		u.established.Store(true)
	}
	switch h.Typ {
	case dgTypeMedia:
		u.onMedia(h, chunk)
	case dgTypePing:
		u.sendControlDatagram(u.buildControl(dgTypePong, chunk)) // echo the probe payload
	case dgTypePong:
		u.onPong(chunk)
	case dgTypeNack:
		u.onNack(chunk)
	case dgTypeBye:
		// Clean end-of-stream: end the route now with errUDPEnded instead of a liveness blackout. Only a
		// STRICTLY-NEWEST (stNew) bye gets this leverage - the real end-of-stream is always the newest
		// datagram, and gating on stNew means a replayed or reordered (stFill) bye can never tear down a
		// live or re-established route (same replay-hardening rule as rebind/liveness above).
		if st == stNew {
			u.setErr(errUDPEnded)
			u.closeOnce.Do(func() {
				close(u.done)
				_ = u.pc.Close()
			})
		}
	}
}

func (u *udpConn) onMedia(h dgHeader, chunk []byte) {
	now := u.clock.Now()
	wire, done := u.reasm.accept(h, chunk, now)
	if !done {
		return
	}
	f, err := parseFrame(wire)
	if err != nil {
		return // a completed-but-corrupt frame (only reachable on a clear route): drop
	}
	// One-way-delay jitter (congestion input): |transit - lastTransit| EWMA, per stream. A
	// constant clock-domain offset cancels in the difference, so it is valid even unsynced.
	transit := now - f.PTS
	if last, ok := u.lastRTx[f.Stream]; ok {
		d := transit - last
		if d < 0 {
			d = -d
		}
		j := u.owdJitter.Load()
		j += (d - j) / 16
		u.owdJitter.Store(j)
	}
	u.lastRTx[f.Stream] = transit
	u.updateOwd(transit, now)
	u.deliver(f)
}

// updateOwd maintains the one-way-delay signal as QUEUING DELAY above a windowed-min baseline
// (transit − recent-min-transit), so it is offset-free (independent of absolute clock agreement) and
// never negative - the fix for the "-39 ms owd" display (item 4). Its trend is the congestion
// controller's delay-gradient input; a constant clock offset cancels in both the baseline and the
// gradient, so the detector does not depend on clock agreement. recvLoop-owned (no lock); only the
// published owdNs is atomic.
func (u *udpConn) updateOwd(transit, now int64) {
	switch {
	case !u.owdHasBucket:
		u.owdBucketMin, u.owdBucketAt, u.owdHasBucket = transit, now, true
	case now-u.owdBucketAt >= owdBucketNs:
		u.owdPrevMin, u.owdHasPrev = u.owdBucketMin, true
		u.owdBucketMin, u.owdBucketAt = transit, now
	case transit < u.owdBucketMin:
		u.owdBucketMin = transit
	}
	base := u.owdBucketMin
	if u.owdHasPrev && u.owdPrevMin < base {
		base = u.owdPrevMin
	}
	q := transit - base
	if q < 0 {
		q = 0
	}
	o := u.owdNs.Load()
	if o == 0 {
		o = q
	} else {
		o += (q - o) / 8
	}
	u.owdNs.Store(o)
}

// deliver hands a completed frame to ReadFrame, newest-wins on overflow (bounded latency).
func (u *udpConn) deliver(f *Frame) {
	select {
	case u.recvCh <- f:
	default:
		select {
		case <-u.recvCh: // drop oldest
			u.bump(&u.st.reasmDrops)
		default:
		}
		select {
		case u.recvCh <- f:
		default:
		}
	}
}

func (u *udpConn) onPong(payload []byte) {
	if len(payload) < 8 {
		return
	}
	sentNs := int64(beU64(payload))
	rtt := u.clock.Now() - sentNs
	if rtt < 0 {
		return
	}
	prev := u.rttNs.Load()
	if prev == 0 {
		u.rttNs.Store(rtt)
	} else {
		u.rttNs.Store(prev + (rtt-prev)/8) // smoothed
	}
}

func (u *udpConn) onNack(payload []byte) {
	items := parseNackPayload(payload)
	if len(items) == 0 {
		return
	}
	u.st.mu.Lock()
	u.st.nacksRecv++
	u.st.mu.Unlock()
	for _, it := range items {
		b, ok := u.ring.get(dgKey(it.Stream, it.FrameSeq, it.FragIdx))
		if !ok {
			continue // evicted / past deadline: abandon (receiver resyncs via frame NACK/PLI)
		}
		if !u.retr.tryReserve(len(b)) {
			continue // retransmit budget exhausted (≤ frac of send rate): abandon
		}
		u.sendControlDatagram(b) // resend the exact datagram (same seq → receiver dedups replays)
		u.st.mu.Lock()
		u.st.retransmits++
		u.st.retransBytes += uint64(len(b))
		u.st.mu.Unlock()
	}
}

// ── keepalive / NACK scan / sweeps ──────────────────────────────────────────────

func (u *udpConn) maintLoop() {
	defer u.wg.Done()
	t := time.NewTicker(udpMaintTick)
	defer t.Stop()
	keepalive := u.cfg.Keepalive
	if keepalive <= 0 {
		keepalive = udpDefKeepalive
	}
	liveness := u.cfg.Liveness
	if liveness <= 0 {
		liveness = udpDefLiveness
	}
	startup := u.cfg.StartupGrace
	if startup <= 0 {
		startup = udpDefStartupGrace
	}
	if startup < liveness {
		startup = liveness // never stricter than the steady window
	}
	var lastPing int64
	for {
		select {
		case <-u.done:
			return
		case <-t.C:
		}
		now := u.clock.Now()
		// Liveness: no authenticated datagram within the window → blackout, tear the conn down. Until the
		// route is established end-to-end (first media / peer ping) the generous startup grace applies, so
		// a cold sender's encoder-open (or a cold receiver's decoder-open under VRAM pressure) is not
		// mistaken for a dead path.
		window := liveness
		if !u.established.Load() {
			window = startup
		}
		if now-u.lastRecvNs.Load() > int64(window) {
			u.setErr(errUDPDead)
			u.closeOnce.Do(func() {
				close(u.done)
				_ = u.pc.Close()
			})
			return
		}
		// Keepalive ping (carries our clock ns for RTT).
		if now-lastPing >= int64(keepalive) {
			lastPing = now
			var ts [8]byte
			putBeU64(ts[:], uint64(now))
			u.sendControlDatagram(u.buildControl(dgTypePing, ts[:]))
		}
		// Deadline drops: incomplete frames past their playout deadline (the router sees the gap
		// and PLIs inter-coded streams). Drop-expired-incomplete.
		if dropped := u.reasm.sweepExpired(now - int64(u.deadline)); len(dropped) > 0 {
			u.st.mu.Lock()
			u.st.reasmDrops += uint64(len(dropped))
			u.st.mu.Unlock()
		}
		// Fragment NACK: frames incomplete past udpNackStale get their missing fragments requested
		// (rate-limited per frame). Bounded to one NACK datagram (dgNackMax items).
		if items := u.scanNack(now); len(items) > 0 {
			u.sendControlDatagram(u.buildControl(dgTypeNack, appendNackPayload(nil, items)))
			u.st.mu.Lock()
			u.st.nacksSent++
			u.st.mu.Unlock()
		}
		u.ring.gc(now)
	}
}

// scanNack collects missing fragments of stale-incomplete frames (recvLoop state; called only from
// maintLoop, which does not race recvLoop... except both touch reasm). Guard reasm with the recv
// goroutine: maintLoop and recvLoop both run, so reasm needs its own lock.
// scanNack collects missing fragments WORTH requesting. A hole is only requested once it is PROVEN
// missing, not merely in-flight: either a LATER frame on the same stream has started arriving (the
// sender transmits a frame fully before the next, so an incomplete OVERTAKEN frame really lost
// fragments), or the frame is close to its playout deadline (last-chance recovery before the
// reassembler drops it). The frame currently still arriving - the newest, unpaced-bursted or paced
// over a few ms, or briefly reordered within itself - is never NACKed. This kills the spurious NACKs
// seen at 0% loss (item 1). A small stale floor absorbs sub-ms LAN reorder; per-frame rate-limited.
func (u *udpConn) scanNack(now int64) []nackItem {
	u.reasm.mu.Lock()
	defer u.reasm.mu.Unlock()
	deadlineCut := now - int64(u.deadline) + int64(udpNackDeadlineLead) // request before the sweep drops it
	var out []nackItem
	for _, key := range u.reasm.order {
		e := u.reasm.entries[key]
		if e == nil {
			continue
		}
		stream := uint16(key >> 32)
		fseq := uint32(key)
		overtaken := int32(u.reasm.maxFrameSeq[stream]-fseq) > 0
		deadlineNear := e.addedAt <= deadlineCut
		if !overtaken && !deadlineNear {
			continue // still the newest in-flight frame + time left - its fragments are arriving, not lost
		}
		if now-e.addedAt < int64(udpNackStale) {
			continue // sub-ms reorder tolerance even once overtaken
		}
		if e.lastNack != 0 && now-e.lastNack < int64(udpNackRetry) {
			continue
		}
		for i, fr := range e.frags {
			if fr == nil {
				out = append(out, nackItem{Stream: stream, FrameSeq: fseq, FragIdx: uint16(i)})
				if len(out) >= dgNackMax {
					e.lastNack = now
					return out
				}
			}
		}
		e.lastNack = now
	}
	return out
}

func (u *udpConn) buildControl(typ uint8, payload []byte) []byte {
	u.encMu.Lock()
	defer u.encMu.Unlock()
	return u.enc.control(typ, payload)
}

func (u *udpConn) bump(p *uint64) {
	u.st.mu.Lock()
	*p++
	u.st.mu.Unlock()
}

// ── helpers ───────────────────────────────────────────────────────────────────

func beU64(b []byte) uint64 {
	_ = b[7]
	return uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
}

func putBeU64(b []byte, v uint64) {
	_ = b[7]
	b[0], b[1], b[2], b[3] = byte(v>>56), byte(v>>48), byte(v>>40), byte(v>>32)
	b[4], b[5], b[6], b[7] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

// bestEffortUDPBuffers raises SO_RCVBUF/SO_SNDBUF on a real UDP socket (brief req 2). Best-effort:
// errors are ignored, a non-UDP packetConn (the test shim) is skipped.
func bestEffortUDPBuffers(pc packetConn) {
	if uc, ok := pc.(*net.UDPConn); ok {
		_ = uc.SetReadBuffer(udpReadBufBytes)
		_ = uc.SetWriteBuffer(udpReadBufBytes)
	}
}

func dgKey(stream uint16, frameSeq uint32, fragIdx uint16) uint64 {
	return uint64(stream)<<48 | uint64(frameSeq)<<16 | uint64(fragIdx)
}

const (
	// pacingGain spreads a frame's datagrams at this MULTIPLE of the negotiated bitrate - NOT the bare
	// target (which would stretch one frame over several frame intervals and add latency, item 2). At
	// 8× target the per-frame send duration is a small fraction of a frame interval, so each frame
	// finishes well inside one interval on a gigabit LAN while a 4K-keyframe microburst is still
	// smoothed instead of blasted at line rate into a switch buffer (brief req 2).
	pacingGain = 8
	// pacerMinRateBytes floors the pacing rate so a low-bitrate route still bursts fast enough; and
	// pacingRateFor returns this (≈256 Mbps, effectively unpaced on GbE) when the budget is unknown.
	pacerMinRateBytes = 32 << 20
)

// pacingRateFor derives the send-pacer rate (bytes/s) from the negotiated video bitrate.
func pacingRateFor(bitrateKbps int) float64 {
	if bitrateKbps <= 0 {
		return pacerMinRateBytes
	}
	r := float64(bitrateKbps) * 1000 / 8 * pacingGain
	if r < pacerMinRateBytes {
		r = pacerMinRateBytes
	}
	return r
}

// bindRemote sets/updates the send target from an AUTHENTICATED datagram's source. First bind is
// immediate; a later change (NAT rebind) is accepted only rate-limited (udpRebindMin) so a burst of
// authenticated-but-spoofed-source datagrams can't thrash the target. Only called after decodeBody
// succeeds, so an off-path attacker (no token/key) can never move it.
func (u *udpConn) bindRemote(src net.Addr) {
	if src == nil {
		return
	}
	cur := u.remoteAddr()
	if cur == nil {
		u.remote.Store(src)
		return
	}
	if cur.String() == src.String() {
		return
	}
	now := u.clock.Now()
	if now-u.lastRebind.Load() < int64(udpRebindMin) {
		return
	}
	u.lastRebind.Store(now)
	u.remote.Store(src)
	u.bump(&u.st.rebinds)
}
