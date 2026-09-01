package bridge

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/wirecrypto"
)

// Conn is a reliable, ordered, optionally-encrypted message channel between two bridge
// sessions, riding the relay's fire-and-forget frames.
//
// It satisfies peerlink.Conn (Send/Recv/Close) and authz.Channel, so everything already built
// on the LAN peer link - remote control, the RemoteUI Library mirror - runs over it unchanged.
//
// Three jobs the relay does NOT do for us:
//
//  1. DELIVERY. A 202 means "published to Redis pub/sub", not "arrived". Conn sequences every
//     chunk, cumulatively acks, and retransmits on an RTO until acked. Loss costs latency,
//     never correctness.
//  2. SIZE. Relay frames cap at 256 KiB; peerlink frames run to 32 MiB. Conn fragments a
//     message into chunks and reassembles in order.
//  3. SECRECY. peerlink's data plane is plaintext + HMAC (a considered LAN tradeoff). Across a
//     third-party relay that would hand the operator every remote-control command and the whole
//     RemoteUI stream. After the AKE, Upgrade() switches the transport to AES-256-GCM with
//     per-direction keys, so the relay sees ciphertext only.
//
// Buffer inventory (repo hard rule - every queue bounded, with a drop policy):
//
//	sendWindow  ≤ maxWindowChunks (32) AND maxWindowBytes (8 MiB) → Send BACKPRESSURES
//	inbound     ≤ inboundChunks (128) chunks                      → DROP-NEWEST (ARQ retransmits)
//	reassembly  ≤ MaxMessage (8 MiB)                              → protocol error, close
//	delivered   ≤ deliveredMsgs (32) messages                     → blocks the reassembler,
//	                                                                which backpressures via ARQ
type Conn struct {
	sid     string // ours
	peerSID string
	send    frameSender
	log     *logbus.Bus

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	sendSeq  uint64
	window   map[uint64]*pending // unacked chunks, by seq
	windowB  int                 // bytes currently in the window
	sendCond *sync.Cond          // signalled when the window drains
	cerr     error               // why the conn closed; surfaced by Recv

	rmu        sync.Mutex
	recvNext   uint64              // next in-order chunk seq we expect
	reorder    map[uint64][]byte   // out-of-order chunks held until the gap fills
	reorderFin map[uint64]struct{} // which held chunks carried fin
	reorderB   int
	assembly   []byte // reassembled message-in-progress
	ackedThru  uint64 // highest contiguous seq received (+1 == recvNext)
	needAck    bool

	inbound   chan chunk
	delivered chan []byte

	// AEAD state. Nil until Upgrade. The two directions are keyed independently so the two ends
	// never share a (key, nonce) pair - the classic GCM catastrophe. Each Sealer owns its own
	// monotonic counter nonce (wirecrypto.Sealer).
	cmu        sync.Mutex
	sealSealer *wirecrypto.Sealer
	openSealer *wirecrypto.Sealer
	upgraded   bool
	closeOnce  sync.Once
	closed     chan struct{}
}

// frameSender publishes one relay frame. Injected so tests can drive a Conn without HTTP.
type frameSender interface {
	Send(ctx context.Context, sid, toSID string, seq int64, kind string, payload []byte) error
}

const (
	// chunkBody leaves room for the header inside the relay's 256 KiB decoded-payload cap.
	chunkBody = 192 << 10
	// MaxMessage bounds one logical message (matches studio's maxPayload). peerlink's 32 MiB
	// ceiling is a LAN number; pushing that through a relay is abuse, so we refuse it loudly.
	MaxMessage = 8 << 20

	maxWindowChunks = 32
	maxWindowBytes  = 8 << 20
	inboundChunks   = 128
	deliveredMsgs   = 32

	rtoInitial = 600 * time.Millisecond
	rtoMax     = 8 * time.Second
	maxRetries = 10
	ackDelay   = 150 * time.Millisecond // standalone-ack timer when we have nothing to piggyback on
)

// hdrLen: ver(1) type(1) seq(8) ack(8) flags(1).
const hdrLen = 19

const (
	chunkVersion = 1
	typeData     = 0
	typeAck      = 1
	flagFin      = 1 << 0 // last chunk of a logical message
)

var (
	errProtocol  = errors.New("bridge: protocol error")
	errConnClose = errors.New("bridge: connection closed")
	// ErrTooBig - a message beyond MaxMessage was offered to Send.
	ErrTooBig = errors.New("bridge: message exceeds the relay message cap")
)

type pending struct {
	payload []byte
	sentAt  time.Time
	rto     time.Duration
	tries   int
}

type chunk struct {
	seq  uint64
	ack  uint64
	typ  byte
	fin  bool
	body []byte
}

// newConn builds a Conn. The caller pumps relay frames in via deliver().
func newConn(ctx context.Context, sid, peerSID string, send frameSender, log *logbus.Bus) *Conn {
	cctx, cancel := context.WithCancel(ctx)
	c := &Conn{
		sid: sid, peerSID: peerSID, send: send, log: log,
		ctx: cctx, cancel: cancel,
		window:    map[uint64]*pending{},
		reorder:   map[uint64][]byte{},
		inbound:   make(chan chunk, inboundChunks),
		delivered: make(chan []byte, deliveredMsgs),
		closed:    make(chan struct{}),
	}
	c.sendCond = sync.NewCond(&c.mu)
	go c.reassembler()
	go c.retransmitter()
	return c
}

// PeerSID is the far session id.
func (c *Conn) PeerSID() string { return c.peerSID }

// ── AEAD upgrade ─────────────────────────────────────────────────────────────

// Upgrade switches the transport to AES-256-GCM, keyed from the handshake secret. Both ends
// call it at the same point in the stream (immediately after the AKE completes), so they stay
// in lockstep; every message after this is ciphertext to the relay.
//
// initiator selects which direction key is ours, so the two ends never seal with the same key.
func (c *Conn) Upgrade(master []byte, initiator bool) error {
	send, recv, err := wirecrypto.NewDuplexSealer(master, nil, initiator, "rave-bridge-i2r-v1", "rave-bridge-r2i-v1")
	if err != nil {
		return err
	}
	c.cmu.Lock()
	c.sealSealer, c.openSealer, c.upgraded = send, recv, true
	c.cmu.Unlock()
	return nil
}

// seal encrypts one logical message. Nonce = the per-direction message counter (inside the
// Sealer), so it can never repeat under a key that is itself fresh per connection.
func (c *Conn) seal(msg []byte) ([]byte, error) {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	if !c.upgraded {
		return msg, nil
	}
	return c.sealSealer.Seal(nil, msg), nil
}

// open decrypts one logical message. Messages arrive in order (the ARQ guarantees it), so the
// receive counter tracks the send counter exactly.
func (c *Conn) open(ct []byte) ([]byte, error) {
	c.cmu.Lock()
	defer c.cmu.Unlock()
	if !c.upgraded {
		return ct, nil
	}
	pt, err := c.openSealer.Open(nil, ct)
	if err != nil {
		// A forged or reordered ciphertext. Never recoverable - the counter would desync.
		return nil, fmt.Errorf("bridge: decrypt: %w", err)
	}
	return pt, nil
}

// ── send path ────────────────────────────────────────────────────────────────

// Send transmits one logical message reliably and in order. Blocks while the send window is
// full (backpressure - never silent loss on the control plane) until the peer acks, ctx ends,
// or the conn closes.
func (c *Conn) Send(ctx context.Context, msg []byte) error {
	if len(msg) > MaxMessage {
		return ErrTooBig
	}
	ct, err := c.seal(msg)
	if err != nil {
		return err
	}
	// Fragment. A zero-length message still emits one (fin) chunk so the peer sees it.
	for off := 0; ; off += chunkBody {
		end := min(off+chunkBody, len(ct))
		fin := end >= len(ct)
		if err := c.sendChunk(ctx, ct[off:end], fin); err != nil {
			return err
		}
		if fin {
			return nil
		}
	}
}

// sendChunk enqueues one chunk into the window (blocking if full) and publishes it.
func (c *Conn) sendChunk(ctx context.Context, body []byte, fin bool) error {
	c.mu.Lock()
	// Backpressure: wait for window room. Cond.Wait needs a waker on ctx/close, so we poll the
	// closed channel through a broadcast from Close/retransmitter.
	for len(c.window) >= maxWindowChunks || c.windowB+len(body) > maxWindowBytes {
		if c.isClosed() {
			c.mu.Unlock()
			return errConnClose
		}
		if ctx.Err() != nil {
			c.mu.Unlock()
			return ctx.Err()
		}
		c.sendCond.Wait()
	}
	if c.isClosed() {
		c.mu.Unlock()
		return errConnClose
	}
	seq := c.sendSeq
	c.sendSeq++
	c.rmu.Lock()
	ack := c.ackedThru
	c.rmu.Unlock()

	frame := encodeChunk(typeData, seq, ack, fin, body)
	c.window[seq] = &pending{payload: frame, sentAt: time.Now(), rto: rtoInitial}
	c.windowB += len(frame)
	c.mu.Unlock()

	c.clearNeedAck()
	return c.publish(ctx, seq, frame)
}

// publish pushes a frame at the relay. A not-yet-accepted peer (403) or a rate limit (429) is
// TRANSIENT: the retransmitter will try again, so we swallow them here rather than kill the
// connection.
func (c *Conn) publish(ctx context.Context, seq uint64, frame []byte) error {
	err := c.send.Send(ctx, c.sid, c.peerSID, int64(seq), KindRelay, frame)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotAccepted), errors.Is(err, ErrRateLimited):
		return nil // the ARQ owns recovery
	case errors.Is(err, ErrSessionGone), errors.Is(err, ErrUnauthorized):
		c.closeWith(err)
		return err
	default:
		// Network blip: leave it in the window; the retransmitter retries.
		return nil
	}
}

// retransmitter re-publishes unacked chunks past their RTO and fires standalone acks. One
// goroutine per conn; exits on close.
func (c *Conn) retransmitter() {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-c.closed:
			return
		case <-c.ctx.Done():
			c.closeWith(c.ctx.Err())
			return
		case <-t.C:
			c.sweep()
			c.maybeStandaloneAck()
		}
	}
}

// sweep retransmits everything past its RTO, backing off each time. A chunk that survives
// maxRetries means the peer is gone - fail the connection rather than retry forever.
func (c *Conn) sweep() {
	now := time.Now()
	type due struct {
		seq   uint64
		frame []byte
	}
	var out []due

	c.mu.Lock()
	for seq, p := range c.window {
		if now.Sub(p.sentAt) < p.rto {
			continue
		}
		p.tries++
		if p.tries > maxRetries {
			c.mu.Unlock()
			c.log.Warn(logTag, "peer unreachable; giving up on the link", map[string]any{
				"peer_sid": c.peerSID, "seq": seq, "tries": p.tries,
			})
			c.closeWith(errors.New("bridge: peer unreachable (retransmit limit)"))
			return
		}
		p.sentAt = now
		p.rto = min(time.Duration(float64(p.rto)*1.6), rtoMax)
		out = append(out, due{seq, p.payload})
	}
	c.mu.Unlock()

	for _, d := range out {
		_ = c.publish(c.ctx, d.seq, d.frame)
	}
}

// maybeStandaloneAck sends a bare ack when we owe one and have no data to piggyback it on.
func (c *Conn) maybeStandaloneAck() {
	c.rmu.Lock()
	owe, ack := c.needAck, c.ackedThru
	c.rmu.Unlock()
	if !owe {
		return
	}
	c.clearNeedAck()
	frame := encodeChunk(typeAck, 0, ack, false, nil)
	// Acks ride the relay plane too; a lost ack just means a retransmit.
	_ = c.send.Send(c.ctx, c.sid, c.peerSID, 0, KindRelay, frame)
}

func (c *Conn) clearNeedAck() {
	c.rmu.Lock()
	c.needAck = false
	c.rmu.Unlock()
}

// ── receive path ─────────────────────────────────────────────────────────────

// deliver hands a relay frame from the SSE demux to this conn. NON-BLOCKING: the SSE reader is
// shared by every conn, so it must never stall here. A full inbound queue DROPS the chunk - the
// peer's ARQ retransmits it, costing latency, never correctness.
func (c *Conn) deliver(payload []byte) {
	ch, err := decodeChunk(payload)
	if err != nil {
		return // malformed; the AEAD already rejects forgeries, this is just noise
	}
	select {
	case c.inbound <- ch:
	default:
		c.log.Warn(logTag, "inbound queue full; dropping chunk (peer will retransmit)",
			map[string]any{"peer_sid": c.peerSID, "cap": inboundChunks})
	}
}

// reassembler drains inbound chunks, applies acks, reorders, reassembles messages, and
// publishes them to delivered. One goroutine per conn.
func (c *Conn) reassembler() {
	for {
		select {
		case <-c.closed:
			return
		case <-c.ctx.Done():
			return
		case ch := <-c.inbound:
			c.applyAck(ch.ack)
			if ch.typ == typeAck {
				continue
			}
			c.ingest(ch)
		}
	}
}

// applyAck retires every chunk the peer has cumulatively acknowledged and wakes any Send
// blocked on window space.
func (c *Conn) applyAck(ack uint64) {
	c.mu.Lock()
	for seq, p := range c.window {
		// ack is "highest contiguous seq received, +1" - i.e. everything below it is in.
		if seq < ack {
			c.windowB -= len(p.payload)
			delete(c.window, seq)
		}
	}
	c.mu.Unlock()
	c.sendCond.Broadcast()
}

// ingest places a data chunk in order and completes messages on the fin flag.
func (c *Conn) ingest(ch chunk) {
	c.rmu.Lock()
	if ch.seq < c.recvNext {
		// Duplicate (the peer retransmitted before our ack landed). Re-ack and drop.
		c.needAck = true
		c.rmu.Unlock()
		return
	}
	if ch.seq > c.recvNext {
		// Out of order: hold it, bounded. A peer that floods us with far-future seqs must not
		// grow this map without limit - past the cap we drop and let the ARQ sort it out.
		if len(c.reorder) < inboundChunks && c.reorderB+len(ch.body) <= MaxMessage {
			c.reorder[ch.seq] = ch.body
			c.reorderB += len(ch.body)
			if ch.fin {
				c.finSeqs()[ch.seq] = struct{}{}
			}
		}
		c.needAck = true
		c.rmu.Unlock()
		return
	}

	// In order. Absorb this chunk and any contiguous run now unblocked.
	var complete [][]byte
	for {
		if err := c.absorb(ch.body, ch.fin, &complete); err != nil {
			c.rmu.Unlock()
			c.closeWith(err)
			return
		}
		c.recvNext++
		c.ackedThru = c.recvNext
		body, ok := c.reorder[c.recvNext]
		if !ok {
			break
		}
		delete(c.reorder, c.recvNext)
		c.reorderB -= len(body)
		_, fin := c.finSeqs()[c.recvNext]
		delete(c.finSeqs(), c.recvNext)
		ch = chunk{seq: c.recvNext, body: body, fin: fin}
	}
	c.needAck = true
	c.rmu.Unlock()

	for _, msg := range complete {
		pt, err := c.open(msg)
		if err != nil {
			c.closeWith(err)
			return
		}
		select {
		case c.delivered <- pt:
		case <-c.closed:
			return
		}
	}
}

// absorb appends a chunk to the message under assembly and, on fin, cuts a complete message.
func (c *Conn) absorb(body []byte, fin bool, out *[][]byte) error {
	if len(c.assembly)+len(body) > MaxMessage {
		return fmt.Errorf("%w: message exceeds %d bytes", errProtocol, MaxMessage)
	}
	c.assembly = append(c.assembly, body...)
	if fin {
		msg := c.assembly
		c.assembly = nil
		*out = append(*out, msg)
	}
	return nil
}

// finSeqs lazily allocates the out-of-order fin-flag set.
func (c *Conn) finSeqs() map[uint64]struct{} {
	if c.reorderFin == nil {
		c.reorderFin = map[uint64]struct{}{}
	}
	return c.reorderFin
}

// Recv returns the next complete message. Blocks until one arrives, ctx ends, or the conn
// closes.
func (c *Conn) Recv(ctx context.Context) ([]byte, error) {
	select {
	case msg := <-c.delivered:
		return msg, nil
	case <-c.closed:
		return nil, c.closeErr()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ── lifecycle ────────────────────────────────────────────────────────────────

// Close tears the conn down. Idempotent (peerlink calls it on every error path).
func (c *Conn) Close() { c.closeWith(nil) }

func (c *Conn) closeWith(err error) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.cerr = err
		c.mu.Unlock()
		close(c.closed)
		c.cancel()
		c.sendCond.Broadcast() // wake anyone blocked on window space
		if err != nil && !errors.Is(err, context.Canceled) {
			c.log.Info(logTag, "bridge conn closed", map[string]any{
				"peer_sid": c.peerSID, "reason": err.Error(),
			})
		}
	})
}

func (c *Conn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *Conn) closeErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cerr != nil {
		return c.cerr
	}
	return errConnClose
}

// ── wire codec ───────────────────────────────────────────────────────────────

// encodeChunk builds [ver][type][seq][ack][flags][body].
func encodeChunk(typ byte, seq, ack uint64, fin bool, body []byte) []byte {
	out := make([]byte, hdrLen+len(body))
	out[0] = chunkVersion
	out[1] = typ
	binary.BigEndian.PutUint64(out[2:10], seq)
	binary.BigEndian.PutUint64(out[10:18], ack)
	if fin {
		out[18] |= flagFin
	}
	copy(out[hdrLen:], body)
	return out
}

func decodeChunk(b []byte) (chunk, error) {
	if len(b) < hdrLen || b[0] != chunkVersion {
		return chunk{}, errProtocol
	}
	return chunk{
		typ:  b[1],
		seq:  binary.BigEndian.Uint64(b[2:10]),
		ack:  binary.BigEndian.Uint64(b[10:18]),
		fin:  b[18]&flagFin != 0,
		body: b[hdrLen:],
	}, nil
}
