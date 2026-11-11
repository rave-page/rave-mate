package medialink

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"encoding/binary"
	"errors"

	"rave.page/mate/internal/wirecrypto"
)

// udpframe.go is the §2.5 UDP datagram profile's wire + codec (Unit 1). It carries the SAME Frame
// (frame.go) across one or more MTU-safe datagrams so a 4K keyframe is fragmented in user space -
// never relying on IP fragmentation, which drops the whole datagram if any fragment is lost.
//
// Datagram header (fixed 24 B, big-endian, clear even in AEAD mode - it is the AEAD AAD). It is the
// §2.5 spec `[8B seq][2B stream][2B frag-idx][2B frag-count][AEAD]` plus four documented additions
// (see .devnotes/UDP_TRANSPORT.md): a version/type byte and a flags byte (keepalive rides the same
// socket, the wire is versioned, the AEAD bit is self-describing), a 4 B route token (off-path
// injection is rejected at zero crypto cost - brief req 5), and a 4 B FrameSeq (the §2.5 8 B seq is
// the per-DATAGRAM number used for the nonce, replay window and loss %; reassembly and per-frame
// NACK need the FRAME id, which datagram-seq ranges cannot carry robustly under loss/retransmit).
//
//	off size field
//	 0   1  Ver<<4 | Type   (Ver=1; Type: 0=media/meta fragment, 1=ping, 2=pong)
//	 1   1  Flags           (bit0 = AEAD body)
//	 2   4  Token           (per-route, constant-time compared; drops off-path/cross-route datagrams)
//	 6   8  Seq             (per-direction datagram seq: AEAD nonce input + loss/replay/jitter unit)
//	14   2  Stream          (media stream id; 0 = meta)
//	16   4  FrameSeq        (Frame.Seq this fragment reassembles into)
//	20   2  FragIdx         (fragment index within the frame)
//	22   2  FragCount       (total fragments for the frame; 1 = unfragmented)
const dgHeaderLen = 24

const (
	dgVersion   = 1
	dgTypeMedia = 0 // a media/meta Frame fragment
	dgTypePing  = 1 // keepalive / reachability probe (cleartext, token-gated)
	dgTypePong  = 2 // keepalive / reachability reply
)

const dgFlagAEAD = 1 << 0 // body is AES-256-GCM sealed (AAD = the 24 B header)

const (
	// dgDefaultPayload is the default max datagram body (fragment) size - fits a 1500 B Ethernet
	// MTU after IP(20)+UDP(8)+our header(24)+GCM tag(16). Configurable (Options), capped at
	// dgMaxPayload; NEVER sized to force IP fragmentation.
	dgDefaultPayload = 1200
	dgMaxPayload     = 8900 // jumbo-frame ceiling; a configured value above this is clamped

	// dgMaxFrags bounds fragments per frame (hostile FragCount guard): 8192 × dgMaxPayload ≈ 70 MiB
	// hard ceiling, but a reassembly is dropped far earlier by the byte cap below. Encoded 4K
	// keyframes are ~400 frags at 1200 B; raw media never rides UDP at a size that reaches this.
	dgMaxFrags = 8192
)

var (
	errDgShort    = errors.New("medialink: short datagram")
	errDgVersion  = errors.New("medialink: bad datagram version")
	errDgFrag     = errors.New("medialink: bad fragment index/count")
	errDgToken    = errors.New("medialink: datagram token mismatch")
	errDgBig      = errors.New("medialink: frame too large for the UDP profile")
	errReasmLimit = errors.New("medialink: reassembly size cap")
)

// dgHeader is the parsed outer datagram header.
type dgHeader struct {
	Typ       uint8
	Flags     uint8
	Token     uint32
	Seq       uint64
	Stream    uint16
	FrameSeq  uint32
	FragIdx   uint16
	FragCount uint16
}

func (h dgHeader) aead() bool { return h.Flags&dgFlagAEAD != 0 }

// appendHeader writes h's 24 wire bytes to dst and returns the grown slice.
func (h dgHeader) appendHeader(dst []byte) []byte {
	var b [dgHeaderLen]byte
	b[0] = byte(dgVersion<<4) | (h.Typ & 0x0f)
	b[1] = h.Flags
	binary.BigEndian.PutUint32(b[2:], h.Token)
	binary.BigEndian.PutUint64(b[6:], h.Seq)
	binary.BigEndian.PutUint16(b[14:], h.Stream)
	binary.BigEndian.PutUint32(b[16:], h.FrameSeq)
	binary.BigEndian.PutUint16(b[20:], h.FragIdx)
	binary.BigEndian.PutUint16(b[22:], h.FragCount)
	return append(dst, b[:]...)
}

// parseDatagramHeader validates + decodes the outer header. Every field from the wire is treated as
// hostile: version is checked, FragIdx < FragCount, FragCount in [1,dgMaxFrags]. Returns the body
// offset (dgHeaderLen) so the caller can slice the (sealed or clear) body.
func parseDatagramHeader(buf []byte) (dgHeader, int, error) {
	if len(buf) < dgHeaderLen {
		return dgHeader{}, 0, errDgShort
	}
	if buf[0]>>4 != dgVersion {
		return dgHeader{}, 0, errDgVersion
	}
	h := dgHeader{
		Typ:       buf[0] & 0x0f,
		Flags:     buf[1],
		Token:     binary.BigEndian.Uint32(buf[2:]),
		Seq:       binary.BigEndian.Uint64(buf[6:]),
		Stream:    binary.BigEndian.Uint16(buf[14:]),
		FrameSeq:  binary.BigEndian.Uint32(buf[16:]),
		FragIdx:   binary.BigEndian.Uint16(buf[20:]),
		FragCount: binary.BigEndian.Uint16(buf[22:]),
	}
	if h.Typ == dgTypeMedia {
		if h.FragCount == 0 || h.FragCount > dgMaxFrags || h.FragIdx >= h.FragCount {
			return dgHeader{}, 0, errDgFrag
		}
	}
	return h, dgHeaderLen, nil
}

// tokenOK is a constant-time token compare (brief req 5: reject blind/off-path injection + cross-
// route confusion at zero crypto cost, cleartext routes included).
func tokenOK(got, want uint32) bool {
	var a, b [4]byte
	binary.BigEndian.PutUint32(a[:], got)
	binary.BigEndian.PutUint32(b[:], want)
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// ── per-datagram AEAD (SRTP-style explicit-nonce; §2.5 / RFC 3711 shape) ──────────────────────────
//
// The stream Sealer (wirecrypto) can't be used: it carries an IMPLICIT lockstep counter nonce that
// only works on an ordered, lossless stream. UDP datagrams reorder, drop and retransmit, so the
// nonce is the EXPLICIT per-direction datagram Seq (unique under a per-connection key → never
// repeats, the one GCM rule). Keys + a 4 B nonce prefix are HKDF-derived per direction. Built on
// stdlib crypto directly (no new dep, no churn in shared wirecrypto).

type dgCrypto struct {
	send, recv       cipher.AEAD
	sendPre, recvPre uint32 // per-direction nonce prefix (domain separation on top of the key)
}

const (
	dgLabelC2S = "medialink-udp c2s v1"
	dgLabelS2C = "medialink-udp s2c v1"
)

// newDgCrypto derives the per-direction GCM state from the shared peerlink media master. initiator
// seals c2s / opens s2c; the responder mirrors - so the two ends never seal under the same key.
func newDgCrypto(master []byte, initiator bool) (*dgCrypto, error) {
	a, err := wirecrypto.HkdfSha256(master, nil, []byte(dgLabelC2S), 36) // 32 key + 4 nonce prefix
	if err != nil {
		return nil, err
	}
	b, err := wirecrypto.HkdfSha256(master, nil, []byte(dgLabelS2C), 36)
	if err != nil {
		return nil, err
	}
	sk, skPre, rk, rkPre := a[:32], a[32:], b[:32], b[32:]
	if !initiator {
		sk, skPre, rk, rkPre = b[:32], b[32:], a[:32], a[32:]
	}
	sa, err := newGCM(sk)
	if err != nil {
		return nil, err
	}
	ra, err := newGCM(rk)
	if err != nil {
		return nil, err
	}
	return &dgCrypto{send: sa, recv: ra,
		sendPre: binary.BigEndian.Uint32(skPre), recvPre: binary.BigEndian.Uint32(rkPre)}, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

func dgNonce(prefix uint32, seq uint64) [12]byte {
	var n [12]byte
	binary.BigEndian.PutUint32(n[0:], prefix)
	binary.BigEndian.PutUint64(n[4:], seq)
	return n
}

// seal appends the GCM-sealed chunk to dst (which already holds the header bytes used as AAD).
func (c *dgCrypto) seal(dst, hdr, chunk []byte, seq uint64) []byte {
	n := dgNonce(c.sendPre, seq)
	return c.send.Seal(dst, n[:], chunk, hdr)
}

// open returns the plaintext chunk (appended to dst). hdr is the AAD; a forged/tampered datagram
// fails here and is dropped by the caller.
func (c *dgCrypto) open(dst, hdr, body []byte, seq uint64) ([]byte, error) {
	n := dgNonce(c.recvPre, seq)
	return c.recv.Open(dst, n[:], body, hdr)
}

// ── fragmentation ─────────────────────────────────────────────────────────────────────────────

// frameWireLen is the full marshaled size of f (header + payload) - what the fragmenter splits.
func frameWireLen(f *Frame) int { return headerLen + len(f.Payload) }

// numFrags reports how many datagrams a frame of wireLen bytes needs at the given body size.
func numFrags(wireLen, bodyMax int) int {
	if wireLen <= 0 {
		return 1
	}
	return (wireLen + bodyMax - 1) / bodyMax
}

// ── reassembly (bounded; drop-oldest + drop-expired-incomplete) ───────────────────────────────

// reasmEntry collects a frame's fragments until all FragCount arrive.
type reasmEntry struct {
	key     uint64 // stream<<32 | frameSeq
	frags   [][]byte
	have    int
	count   uint16
	bytes   int
	seq     uint64 // highest datagram seq seen for this frame (deadline/ordering aid)
	addedAt int64  // media-clock ns at first fragment (deadline sweep input)
}

func reasmKey(stream uint16, frameSeq uint32) uint64 {
	return uint64(stream)<<32 | uint64(frameSeq)
}

// reassembler holds in-flight frames, bounded by frame COUNT and total BYTES. On a cap breach the
// OLDEST incomplete frame is evicted (drop-oldest); the conn additionally sweeps entries past their
// playout deadline (sweepExpired). Not safe for concurrent use - the conn's single recv goroutine
// owns it.
type reassembler struct {
	entries   map[uint64]*reasmEntry
	order     []uint64 // insertion order for drop-oldest eviction
	bytes     int
	maxFrames int
	maxBytes  int
	// completed guards against a late/duplicate fragment resurrecting a just-finished frame: the
	// highest completed/evicted frameSeq per stream (wrap-aware signed compare).
	doneSeq map[uint16]uint32
	dropped uint64 // frames dropped at reassembly (cap breach or expiry) - telemetry
}

func newReassembler(maxFrames, maxBytes int) *reassembler {
	if maxFrames <= 0 {
		maxFrames = 64
	}
	if maxBytes <= 0 {
		maxBytes = 32 << 20
	}
	return &reassembler{entries: map[uint64]*reasmEntry{}, maxFrames: maxFrames, maxBytes: maxBytes,
		doneSeq: map[uint16]uint32{}}
}

// accept folds one media fragment in. Returns a completed frame's full wire bytes (header||payload)
// when the last fragment arrives, else nil. chunk is copied (the caller's datagram buffer is
// recycled). A fragment for an already-completed/evicted frame, or a duplicate, is ignored.
func (r *reassembler) accept(h dgHeader, chunk []byte, nowNs int64) ([]byte, bool) {
	if done, ok := r.doneSeq[h.Stream]; ok && int32(h.FrameSeq-done) <= 0 {
		return nil, false // already finished or older than the last finished frame on this stream
	}
	key := reasmKey(h.Stream, h.FrameSeq)
	e := r.entries[key]
	if e == nil {
		e = &reasmEntry{key: key, frags: make([][]byte, h.FragCount), count: h.FragCount, addedAt: nowNs}
		r.entries[key] = e
		r.order = append(r.order, key)
		r.evictIfNeeded()
		e = r.entries[key] // re-read: a pathological maxFrames<1 could have evicted it (defensive)
		if e == nil {
			return nil, false
		}
	}
	if int(h.FragCount) != len(e.frags) || h.FragIdx >= uint16(len(e.frags)) {
		return nil, false // inconsistent fragment metadata for this frame - ignore
	}
	if e.frags[h.FragIdx] != nil {
		return nil, false // duplicate fragment
	}
	cp := append([]byte(nil), chunk...)
	e.frags[h.FragIdx] = cp
	e.have++
	e.bytes += len(cp)
	r.bytes += len(cp)
	if h.Seq > e.seq {
		e.seq = h.Seq
	}
	if e.bytes > r.maxBytes { // a single oversized frame can't be allowed to sit in the table
		r.remove(key)
		r.markDone(h.Stream, h.FrameSeq)
		r.dropped++
		return nil, false
	}
	r.evictIfNeeded()
	if r.entries[key] == nil { // evicted under pressure before completing
		return nil, false
	}
	if e.have < int(e.count) {
		return nil, false
	}
	// Complete: concatenate fragments in order.
	out := make([]byte, 0, e.bytes)
	for _, fr := range e.frags {
		out = append(out, fr...)
	}
	r.remove(key)
	r.markDone(h.Stream, h.FrameSeq)
	return out, true
}

// missing returns the fragment indices not yet received for an in-flight frame (NACK input). nil if
// the frame is unknown or complete.
func (r *reassembler) missing(stream uint16, frameSeq uint32) []uint16 {
	e := r.entries[reasmKey(stream, frameSeq)]
	if e == nil {
		return nil
	}
	var out []uint16
	for i, fr := range e.frags {
		if fr == nil {
			out = append(out, uint16(i))
		}
	}
	return out
}

// sweepExpired drops incomplete frames whose first fragment arrived before cutoffNs (past their
// playout deadline). Returns the dropped (stream, frameSeq) pairs so the conn can PLI inter-coded
// streams. Drop-expired-incomplete policy (brief req 1).
func (r *reassembler) sweepExpired(cutoffNs int64) [][2]uint32 {
	if len(r.order) == 0 {
		return nil
	}
	var dropped [][2]uint32
	kept := r.order[:0]
	for _, key := range r.order {
		e := r.entries[key]
		if e == nil {
			continue
		}
		if e.addedAt < cutoffNs {
			stream := uint16(key >> 32)
			fseq := uint32(key)
			delete(r.entries, key)
			r.bytes -= e.bytes
			r.markDone(stream, fseq)
			r.dropped++
			dropped = append(dropped, [2]uint32{uint32(stream), fseq})
			continue
		}
		kept = append(kept, key)
	}
	r.order = kept
	return dropped
}

func (r *reassembler) evictIfNeeded() {
	for (len(r.entries) > r.maxFrames || r.bytes > r.maxBytes) && len(r.order) > 0 {
		oldest := r.order[0]
		r.order = r.order[1:]
		if e := r.entries[oldest]; e != nil {
			delete(r.entries, oldest)
			r.bytes -= e.bytes
			r.markDone(uint16(oldest>>32), uint32(oldest))
			r.dropped++
		}
	}
}

func (r *reassembler) remove(key uint64) {
	if e := r.entries[key]; e != nil {
		r.bytes -= e.bytes
		delete(r.entries, key)
	}
	for i, k := range r.order {
		if k == key {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

func (r *reassembler) markDone(stream uint16, frameSeq uint32) {
	if cur, ok := r.doneSeq[stream]; !ok || int32(frameSeq-cur) > 0 {
		r.doneSeq[stream] = frameSeq
	}
}

// ── encode (one Frame → N datagrams) ──────────────────────────────────────────────────────────

// dgEncoder serializes Frames into datagrams, owning the per-direction datagram Seq (nonce/loss
// unit). crypto nil = cleartext body (token-gated only). Not safe for concurrent use.
type dgEncoder struct {
	token   uint32
	crypto  *dgCrypto // nil = cleartext
	bodyMax int       // max fragment body (plaintext) bytes
	seq     uint64    // next datagram seq
}

func newDgEncoder(token uint32, crypto *dgCrypto, bodyMax int) *dgEncoder {
	if bodyMax <= 0 || bodyMax > dgMaxPayload {
		bodyMax = dgDefaultPayload
	}
	return &dgEncoder{token: token, crypto: crypto, bodyMax: bodyMax}
}

// encodeFrame marshals f and splits it into datagrams, appending each to out (reused across calls).
// It returns out plus the datagram Seq of the FIRST fragment and the fragment count, so the caller
// can map a frame to its datagram-seq span for the retransmit ring. A frame too large for the
// profile (would exceed dgMaxFrags) is rejected rather than silently split past the cap.
func (e *dgEncoder) encodeFrame(out [][]byte, f *Frame) ([][]byte, uint64, uint16, error) {
	wire := f.marshal(nil)
	count := numFrags(len(wire), e.bodyMax)
	if count > dgMaxFrags {
		return out, 0, 0, errDgBig
	}
	flags := uint8(0)
	if e.crypto != nil {
		flags = dgFlagAEAD
	}
	first := e.seq
	for i := 0; i < count; i++ {
		lo := i * e.bodyMax
		hi := lo + e.bodyMax
		if hi > len(wire) {
			hi = len(wire)
		}
		h := dgHeader{Typ: dgTypeMedia, Flags: flags, Token: e.token, Seq: e.seq,
			Stream: f.Stream, FrameSeq: f.Seq, FragIdx: uint16(i), FragCount: uint16(count)}
		dg := h.appendHeader(make([]byte, 0, dgHeaderLen+(hi-lo)+16))
		if e.crypto != nil {
			dg = e.crypto.seal(dg, dg[:dgHeaderLen], wire[lo:hi], e.seq)
		} else {
			dg = append(dg, wire[lo:hi]...)
		}
		out = append(out, dg)
		e.seq++
	}
	return out, first, uint16(count), nil
}

// control builds a cleartext ping/pong datagram (keepalive + reachability; token-gated). payload is
// an opaque echo (a probe nonce / timestamp). These ride the media socket on any route, AEAD or not.
func (e *dgEncoder) control(typ uint8, payload []byte) []byte {
	h := dgHeader{Typ: typ, Token: e.token, Seq: e.seq}
	e.seq++
	dg := h.appendHeader(make([]byte, 0, dgHeaderLen+len(payload)))
	return append(dg, payload...)
}

// decodeBody extracts a datagram's fragment plaintext: it verifies the token, then (AEAD) opens the
// body with the explicit-seq nonce, or (clear) returns it directly. crypto must be non-nil iff the
// datagram's AEAD flag is set - a mode mismatch is rejected. Returns the plaintext chunk.
func decodeBody(h dgHeader, body []byte, token uint32, crypto *dgCrypto, hdr []byte) ([]byte, error) {
	if !tokenOK(h.Token, token) {
		return nil, errDgToken
	}
	if h.aead() {
		if crypto == nil {
			return nil, errDgToken
		}
		return crypto.open(nil, hdr, body, h.Seq)
	}
	return append([]byte(nil), body...), nil
}
