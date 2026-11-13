package medialink

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// native_rx.go is the RECEIVE-side mirror of native_tx.go (owner directive 2026-11-12: realtime paths
// are native). When the route's decode sink runs in the native child and can take the network source
// (NativeRXSink), dialAndReceive hands it the route: Go negotiated + ran the UDP reachability
// handshake, derives the per-route keys/token, parks a duplicate of the bound socket and passes it on;
// the child's zigmedia receiver then owns socket RX -> reassembly -> NACK -> adaptive playout ->
// decode -> publish. The Go udpConn receive loop and the Go jitter buffer do not run. Go keeps control
// only: liveness/bye supervision, the congestion/VRAM governor (inputs from the native stats), clock
// sync, telemetry. Every receiver->sender control datagram is sealed by the child (SendMeta) - Go never
// writes on its own copy of the socket after the handover (AES-GCM nonce reuse).
//
// Anything unsupported (TCP route, legacy peer without per-route keys, IPv6, non-native decode engine,
// attach failure) keeps the Go receive path unchanged. Escape hatch: config mediaLink.recvImpl="go"
// or RAVE_MATE_NATIVE_RX=0.

// NativeRXParams is one hand-off. Socket is a PARKED duplicate of the route's bound UDP socket owned
// by this process; on a successful attach the sink owns it (on error the caller closes it).
type NativeRXParams struct {
	Socket        uintptr
	Peer          string // sender "a.b.c.d:port" (the dialer's authenticated remote)
	Encrypt       bool
	Token         uint32
	OpenKey       [32]byte // opens the sender's media (initiator: s2c)
	OpenPrefix    uint32
	SendKey       [32]byte // seals our control (initiator: c2s)
	SendPrefix    uint32
	BodyMax       int
	FPS           float64
	Preset        string // playout preset (low-latency | balanced | smooth)
	Stream        uint16
	InitialSeq    uint64 // above every seq the Go conn used or may still use (nativeRXSeqJump)
	MaxFrameBytes int    // 0 = native default (4 MiB)
}

// NativeRXStats is the native receiver's telemetry (sampled ~4 Hz off the realtime path).
type NativeRXStats struct {
	Datagrams, Delivered, Published         uint64
	NacksSent, NackRecovered, NackExpired   uint64
	PLIsSent, ReasmDrops, SlotDrops         uint64
	LossPct                                 float64
	OwdNs, OwdJitterNs, RTTns               int64
	PlayoutMs, TargetMs, MaxMs              float64
	Misses, Misses10m, Repeats, Repeats10m  uint64
	Rebases, GapDrops, Grows, Decays        uint64
	Buffered                                uint64
	PubCount                                uint64
	PubP50Ms, PubP99Ms, PubMaxMs, PubMeanMs float64
	PubOver25, PubOver50, PubOver100        uint64
	RelP99Ms, RelMaxMs                      float64
	RelOver25                               uint64
	KeyWaits, Rebinds                       uint64
	MediaBytes, Keyframes                   uint64
	DecErrors                               uint64
	RxErrors                                uint64 // socket receive errors (non-zero + no datagrams = deaf socket)
	Bye                                     bool
	SinceRx                                 time.Duration // age of the last authenticated datagram (<0 = none yet)
}

// NativeRXMeta is one sender->receiver meta frame forwarded by the native receiver; Age = time since
// it came off the socket (places sync T4 exactly).
type NativeRXMeta struct {
	JSON []byte
	Age  time.Duration
}

// NativeRX is a running native receive path. The router supervises it; it never pumps media.
type NativeRX interface {
	// SendMeta queues one receiver->sender meta frame on the native control seq/nonce counter.
	SendMeta(json []byte) error
	// Stats snapshots the native counters (ok=false: not attached / gone).
	Stats() (NativeRXStats, bool)
	// Meta delivers the sender's meta frames (sync pongs, reports). Bounded.
	Meta() <-chan NativeRXMeta
	// Err is non-nil once the native path failed for good (decode session pinned, child crash limit):
	// the route ends and the receive layer re-establishes it.
	Err() error
}

// NativeRXSink is the optional Sink capability: a decode sink running in the native child that can
// take the route's network source.
type NativeRXSink interface {
	AttachNativeRX(NativeRXParams) (NativeRX, error)
}

// nativeRXSeqJump puts the child's first control seq far above anything the Go conn sent or can still
// send while both hold the socket during the hand-off: distinct nonces by construction.
const nativeRXSeqJump = 1 << 20

// nativeRXEnabled reports whether the native receive path may be used (escape hatch).
func (rm *RouteManager) nativeRXEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("RAVE_MATE_NATIVE_RX"))) {
	case "0", "off", "go", "false":
		return false
	}
	if rm.recvImpl != nil && strings.EqualFold(strings.TrimSpace(rm.recvImpl()), "go") {
		return false
	}
	return true
}

// tryAttachNativeRX hands a UDP route to a native decode sink. (nil,false) = keep the Go path; the Go
// udpConn is untouched in that case. On success the Go udpConn is closed (its receive loop stops; no
// bye - the route continues natively on the same socket).
func (rm *RouteManager) tryAttachNativeRX(sink Sink, udp *udpConn, peer string, salt []byte, encrypt bool, stream uint16, fps float64) (NativeRX, bool) {
	nrs, ok := sink.(NativeRXSink)
	if !ok || udp == nil || salt == nil || !rm.nativeRXEnabled() {
		return nil, false
	}
	ra, _ := udp.remoteAddr().(*net.UDPAddr)
	if ra == nil || ra.IP.To4() == nil {
		return nil, false // native receiver is IPv4-only
	}
	sec, ok := rm.secrets.MediaSecret(peer)
	if !ok {
		return nil, false
	}
	sk, skp, rk, rkp, err := deriveRouteKeysRaw(sec, salt, true) // dialer = initiator: send c2s, open s2c
	if err != nil {
		return nil, false
	}
	h, err := parkUDPSocket(udp.pc)
	if err != nil {
		rm.infof("native rx unavailable (socket not transferable) - Go receive path", map[string]any{"error": err.Error()})
		return nil, false
	}
	p := NativeRXParams{Socket: h, Peer: (&net.UDPAddr{IP: ra.IP.To4(), Port: ra.Port}).String(), Encrypt: encrypt,
		Token: udp.token, OpenKey: rk, OpenPrefix: rkp, SendKey: sk, SendPrefix: skp, BodyMax: dgDefaultPayload,
		FPS: fps, Preset: rm.playoutPreset().Name, Stream: stream, InitialSeq: udp.sendSeq() + nativeRXSeqJump}
	nrx, err := nrs.AttachNativeRX(p)
	if err != nil {
		closeParkedSocket(h)
		rm.warnf("native rx attach failed - Go receive path", map[string]any{"error": err.Error()})
		return nil, false
	}
	_ = udp.Close() // stop the Go receive loop + close Go's handle; the parked duplicate keeps the socket
	return nrx, true
}

// nativeRXConn is the route's frameConn once the native receiver owns the socket: meta writes go
// through the native control counter, reads block until the route ends.
type nativeRXConn struct {
	nrx  NativeRX
	once sync.Once
	done chan struct{}
}

func newNativeRXConn(n NativeRX) *nativeRXConn {
	return &nativeRXConn{nrx: n, done: make(chan struct{})}
}

func (c *nativeRXConn) WriteFrame(f *Frame) error {
	select {
	case <-c.done:
		return errConnClosed
	default:
	}
	if f.Kind != KindMeta || f.Stream != metaStream {
		return errors.New("medialink: native rx route carries no outbound media")
	}
	return c.nrx.SendMeta(f.Payload)
}

func (c *nativeRXConn) ReadFrame() (*Frame, error) {
	<-c.done
	return nil, errConnClosed
}

func (c *nativeRXConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return nil
}

// nativeUDPStats adapts the native counters to the congestion controller's UDPStats input.
type nativeUDPStats struct{ nrx NativeRX }

func (n nativeUDPStats) Stats() UDPStats {
	s, _ := n.nrx.Stats()
	return nativeToUDPStats(s, true)
}

func nativeToUDPStats(s NativeRXStats, enc bool) UDPStats {
	return UDPStats{Encrypted: enc, LossPct: s.LossPct, RTTns: s.RTTns, OwdJitterNs: s.OwdJitterNs, OwdNs: s.OwdNs,
		NacksSent: s.NacksSent, ReasmDrops: s.ReasmDrops, Rebinds: s.Rebinds}
}

// nativeToJitter maps the native playout onto the route's JitterStats (ctl routes "playout" line).
func nativeToJitter(s NativeRXStats, preset string) JitterStats {
	return JitterStats{Buffered: int(s.Buffered), Late: s.Misses, PolicyDrops: s.GapDrops, Grows: s.Grows,
		Decays: s.Decays, PLIsSent: s.PLIsSent, Preset: preset, PlayoutMs: s.PlayoutMs, TargetMs: s.TargetMs,
		MaxMs: s.MaxMs, Misses10m: s.Misses10m, Repeats10m: s.Repeats10m, Repeats: s.Repeats, Rebases: s.Rebases}
}

// superviseNativeRX runs the route's control loop: telemetry into RouteStat, liveness (the udpConn
// rules: startup grace until the first datagram, then the liveness window), bye, native failure, and
// the sender's meta (reports, sync pongs). Returns the route's end reason.
func (rm *RouteManager) superviseNativeRX(ctx context.Context, conn *nativeRXConn, nrx NativeRX, st *routeStat, peer string) error {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-conn.done:
			return errConnClosed
		case m := <-nrx.Meta():
			f := &Frame{Kind: KindMeta, Stream: metaStream, Payload: m.JSON}
			switch typ, err := metaType(f); {
			case err != nil:
			case typ == MetaReport:
				if r, err := DecodeReport(f); err == nil {
					st.applyRemote(r)
				}
			case typ == MetaSyncReply:
				if pong, err := DecodeSyncPong(f); err == nil {
					rm.onSyncPong(peer, pong, rm.clock.Now()-int64(m.Age))
				}
			}
		case <-t.C:
			if err := nrx.Err(); err != nil {
				return err
			}
			s, ok := nrx.Stats()
			if !ok {
				continue
			}
			st.setNativeRXStats(s)
			switch {
			case s.Bye:
				return errUDPEnded
			case s.Datagrams == 0 && time.Since(start) > udpDefStartupGrace:
				return errUDPDead
			case s.Datagrams > 0 && s.SinceRx > udpDefLiveness:
				return errUDPDead
			}
		}
	}
}

// nativeReceiverReports emits the RR-semantic report from the native counters (the Go receive loop
// that feeds receiverReport does not run on a native route).
func (rm *RouteManager) nativeReceiverReports(ctx context.Context, rio *routeIO, nrx NativeRX) {
	t := time.NewTicker(rm.reportEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s, ok := nrx.Stats()
		if !ok || s.Delivered == 0 {
			continue
		}
		r := Report{Type: MetaReport, Stream: rio.st.stream, Lost: int64(s.GapDrops), FractionLost: s.LossPct / 100,
			Jitter: float64(s.OwdJitterNs), WallNanos: time.Now().UnixNano(), PTSNanos: rm.clock.Now()}
		if rio.writeMeta(r, rm.clock.Now()) != nil {
			return
		}
	}
}

// statsSource is the congestion controller's transport-stats input (udpConn or the native receiver).
type statsSource interface{ Stats() UDPStats }
