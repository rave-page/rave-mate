//go:build windows && cgo

package mfenc

// procnet: attach a NETWORK source to a decode session (child proto v5, native/zigenc/src/netrx.zig).
// Go hands the child a duplicate of the route's bound UDP socket (WSADuplicateSocketW into the child's
// pid) plus the per-route keys; from then on the per-packet/per-frame path - socket RX, reassembly,
// NACK, adaptive playout, decode, publish - never touches the Go runtime. Go reads the child's
// telemetry block (shm, seqlock) and forwards receiver->sender meta through the child ("meta" op), so
// every control datagram is sealed on ONE seq/nonce counter.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	protoVerNetRX = 5   // minimum child hello.ver for null_dec/net_blk/netrx/meta
	netBlkSize    = 512 // telemetry block after the inbound ring (netrx.zig blk_size)
	netAttachWait = 3 * time.Second
	netMetaQueue  = 32 // inbound sender meta buffered for the route (drop-newest when full)
	// netSeqMargin: a re-attach (decoder recycle / child restart) continues ABOVE the last published
	// control seq by this much - the child writes the block every 250 ms, and an AES-GCM nonce must
	// never repeat. Far below the per-key seq budget.
	netSeqMargin = 1 << 20
	netMetaMax   = 256 // = zigmedia receiver mailbox slot
)

// Telemetry block slots (u64 each; f64 as bits). Mirrors netrx.zig Slot - change BOTH ends together.
const (
	nsSeqlock = iota
	nsDatagrams
	nsDelivered
	nsPublished
	nsNacksSent
	nsNackRecovered
	nsNackExpired
	nsPLIsSent
	nsReasmDrops
	nsSlotDrops
	nsMetasSent
	nsLossPct
	nsOwdNs
	nsOwdJitterNs
	nsRTTns
	nsPlayoutMs
	nsTargetMs
	nsMaxMs
	nsMisses
	nsMisses10m
	nsRepeats
	nsRepeats10m
	nsRebases
	nsGapDrops
	nsGrows
	nsDecays
	nsPubCount
	nsPubP50
	nsPubP99
	nsPubMax
	nsPubOver25
	nsPubOver50
	nsPubOver100
	nsLastRxNs
	nsCtlSeq
	nsKeyWaits
	nsRebinds
	nsMediaBytes
	nsKeyframes
	nsRelP99
	nsRelMax
	nsRelOver25
	nsMetaInDrops
	nsBuffered
	nsDecErrors
	nsNowNs
	nsPubMean
	nsBye
	nsState
	nsRxErrors
	nsCount
)

// Err is non-nil once the session failed for good (destination pinned, child crash limit, re-attach
// failure): the route ends and re-establishes.
func (d *ProcDecSession) Err() error { return d.failed() }

// NetCapable reports whether this session can take a network source (child v5 + telemetry block).
func (d *ProcDecSession) NetCapable() bool { return d.netBlk }

// AttachNet hands the route's socket + keys to the child. On success the session owns p.Socket. On
// error the caller keeps it and its Go receive path (nothing was started in the child).
func (d *ProcDecSession) AttachNet(p NetRXParams) error {
	if !d.netBlk {
		return ErrNetRXUnsupported
	}
	if d.closed.Load() {
		return errors.New("mfenc: decode session closed")
	}
	if p.Socket == 0 || p.Socket == uintptr(windows.InvalidHandle) {
		return errors.New("mfenc: netrx: no socket")
	}
	d.netMu.Lock()
	defer d.netMu.Unlock()
	if d.netP != nil {
		return errors.New("mfenc: netrx: already attached")
	}
	if err := d.attachLocked(&p); err != nil {
		return err
	}
	cp := p
	d.netP = &cp
	return nil
}

// reattachNet re-places an attached network source after the child session was reopened (decoder
// recycle or child restart): fresh duplicate, control seq continued ABOVE the last published one.
func (d *ProcDecSession) reattachNet() {
	d.netMu.Lock()
	defer d.netMu.Unlock()
	if d.netP == nil || d.closed.Load() {
		return
	}
	if s, ok := d.readNetBlock(); ok && s.CtlSeq+netSeqMargin > d.netP.InitialSeq {
		d.netP.InitialSeq = s.CtlSeq + netSeqMargin
	} else {
		d.netP.InitialSeq += netSeqMargin
	}
	if err := d.attachLocked(d.netP); err != nil {
		d.fail("mfenc: native receive re-attach failed: " + err.Error())
		return
	}
	Infof("mfenc: native receive re-attached on %q (control seq %d)", d.dstName, d.netP.InitialSeq)
}

type netrxCmd struct {
	Op      string  `json:"op"`
	SID     uint32  `json:"sid"`
	PInfo   string  `json:"pinfo"`
	Peer    string  `json:"peer,omitempty"`
	Enc     bool    `json:"enc"`
	Token   uint32  `json:"token"`
	OKey    string  `json:"okey"`
	OPfx    uint32  `json:"opfx"`
	SKey    string  `json:"skey"`
	SPfx    uint32  `json:"spfx"`
	BodyMax int     `json:"body_max,omitempty"`
	RFPS    float64 `json:"rfps,omitempty"`
	Preset  string  `json:"preset,omitempty"`
	Stream  uint16  `json:"stream"`
	ISeq    uint64  `json:"iseq"`
	MaxFB   int     `json:"maxfb,omitempty"`
}

// attachLocked duplicates the socket into the CURRENT child incarnation and waits for its verdict.
// Caller holds d.netMu.
func (d *ProcDecSession) attachLocked(p *NetRXParams) error {
	c := d.child
	c.mu.Lock()
	if c.dead || c.cmd == nil || c.cmd.Process == nil {
		c.mu.Unlock()
		return errors.New("mfenc: encoder child down")
	}
	pid := uint32(c.cmd.Process.Pid)
	ch := make(chan openedEv, 1)
	c.netWait[d.sid] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.netWait[d.sid] == ch {
			delete(c.netWait, d.sid)
		}
		c.mu.Unlock()
	}()
	var info windows.WSAProtocolInfo
	if err := windows.WSADuplicateSocket(windows.Handle(p.Socket), pid, &info); err != nil {
		return fmt.Errorf("mfenc: WSADuplicateSocket: %w", err)
	}
	raw := unsafe.Slice((*byte)(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	cmd := netrxCmd{Op: "netrx", SID: d.sid, PInfo: hex.EncodeToString(raw), Peer: p.Peer, Enc: p.Encrypt,
		Token: p.Token, OKey: hex.EncodeToString(p.OpenKey[:]), OPfx: p.OpenPrefix,
		SKey: hex.EncodeToString(p.SendKey[:]), SPfx: p.SendPrefix, BodyMax: p.BodyMax, RFPS: p.FPS,
		Preset: p.Preset, Stream: p.Stream, ISeq: p.InitialSeq, MaxFB: p.MaxFrameBytes}
	if err := c.send(cmd); err != nil {
		return err
	}
	select {
	case ev := <-ch:
		if !ev.OK {
			return fmt.Errorf("mfenc: netrx refused: %s", ev.Err)
		}
		return nil
	case <-time.After(netAttachWait):
		return errors.New("mfenc: netrx attach timeout")
	}
}

// SendMeta queues one receiver->sender meta frame (JSON <= 256 B) on the child's control seq/nonce
// space. Never write control on Go's own copy of the socket once attached (AES-GCM nonce reuse).
func (d *ProcDecSession) SendMeta(js []byte) error {
	if len(js) == 0 || len(js) > netMetaMax {
		return fmt.Errorf("mfenc: meta size %d (1..%d)", len(js), netMetaMax)
	}
	d.netMu.Lock()
	attached := d.netP != nil
	d.netMu.Unlock()
	if !attached {
		return ErrNetRXUnsupported
	}
	return d.child.send(map[string]any{"op": "meta", "sid": d.sid, "m": string(js)})
}

// NetMeta delivers the sender's meta frames (sync pongs, reports). Bounded; never closed.
func (d *ProcDecSession) NetMeta() <-chan NetMeta { return d.netMeta }

// onNetMeta: readEvents goroutine. Drop-newest when the route is not draining (bounded).
func (d *ProcDecSession) onNetMeta(ev openedEv) {
	age := time.Duration(0)
	if now := childNowNs(); now > ev.At && ev.At > 0 {
		age = time.Duration(now - ev.At)
	}
	select {
	case d.netMeta <- NetMeta{JSON: []byte(ev.M), Age: age}:
	default:
	}
}

// NetStats reads the child's telemetry block. ok=false: not attached / no block.
func (d *ProcDecSession) NetStats() (NetRXStats, bool) {
	d.netMu.Lock()
	attached := d.netP != nil
	d.netMu.Unlock()
	if !attached {
		return NetRXStats{}, false
	}
	return d.readNetBlock()
}

func (d *ProcDecSession) netSlot(i int) *uint64 { return d.shm.u64(d.netOff + uintptr(i)*8) }

// readNetBlock takes a consistent snapshot (seqlock: retry while the child is mid-write).
func (d *ProcDecSession) readNetBlock() (NetRXStats, bool) {
	if !d.netBlk {
		return NetRXStats{}, false
	}
	var v [nsCount]uint64
	for try := 0; try < 8; try++ {
		s0 := atomic.LoadUint64(d.netSlot(nsSeqlock))
		if s0&1 != 0 {
			continue
		}
		for i := 1; i < nsCount; i++ {
			v[i] = atomic.LoadUint64(d.netSlot(i))
		}
		if atomic.LoadUint64(d.netSlot(nsSeqlock)) == s0 {
			break
		}
	}
	f := func(i int) float64 { return math.Float64frombits(v[i]) }
	s := NetRXStats{
		Datagrams: v[nsDatagrams], Delivered: v[nsDelivered], Published: v[nsPublished],
		NacksSent: v[nsNacksSent], NackRecovered: v[nsNackRecovered], NackExpired: v[nsNackExpired],
		PLIsSent: v[nsPLIsSent], ReasmDrops: v[nsReasmDrops], SlotDrops: v[nsSlotDrops], MetasSent: v[nsMetasSent],
		LossPct: f(nsLossPct), OwdNs: int64(v[nsOwdNs]), OwdJitterNs: int64(v[nsOwdJitterNs]), RTTns: int64(v[nsRTTns]),
		PlayoutMs: f(nsPlayoutMs), TargetMs: f(nsTargetMs), MaxMs: f(nsMaxMs),
		Misses: v[nsMisses], Misses10m: v[nsMisses10m], Repeats: v[nsRepeats], Repeats10m: v[nsRepeats10m],
		Rebases: v[nsRebases], GapDrops: v[nsGapDrops], Grows: v[nsGrows], Decays: v[nsDecays],
		PubCount: v[nsPubCount], PubP50Ms: f(nsPubP50), PubP99Ms: f(nsPubP99), PubMaxMs: f(nsPubMax), PubMeanMs: f(nsPubMean),
		PubOver25: v[nsPubOver25], PubOver50: v[nsPubOver50], PubOver100: v[nsPubOver100],
		RelP99Ms: f(nsRelP99), RelMaxMs: f(nsRelMax), RelOver25: v[nsRelOver25],
		CtlSeq: v[nsCtlSeq], KeyWaits: v[nsKeyWaits], Rebinds: v[nsRebinds],
		MediaBytes: v[nsMediaBytes], Keyframes: v[nsKeyframes],
		MetaInDrops: v[nsMetaInDrops], Buffered: v[nsBuffered], DecErrors: v[nsDecErrors], RxErrors: v[nsRxErrors],
		Bye: v[nsBye] != 0, Running: v[nsState] == 1, SinceRx: -1,
	}
	if last := int64(v[nsLastRxNs]); last > 0 {
		if now := childNowNs(); now >= last {
			s.SinceRx = time.Duration(now - last)
		}
	}
	return s, true
}

// closeNet releases the parked socket after the child session is gone.
func (d *ProcDecSession) closeNet() {
	d.netMu.Lock()
	p := d.netP
	d.netP = nil
	d.netMu.Unlock()
	if p != nil {
		_ = windows.Closesocket(windows.Handle(p.Socket))
	}
}

// netDelivered is the frozen-destination oracle's "AUs arrived" input on a network session (Go appends
// nothing to the ring there).
func (d *ProcDecSession) netDelivered() (uint64, bool) {
	d.netMu.Lock()
	attached := d.netP != nil
	d.netMu.Unlock()
	if !attached {
		return 0, false
	}
	return atomic.LoadUint64(d.netSlot(nsDelivered)), true
}
