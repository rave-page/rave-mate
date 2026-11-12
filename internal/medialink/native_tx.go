package medialink

import (
	"encoding/binary"
	"time"

	"rave.page/mate/internal/wirecrypto"
)

// native_tx.go is the Go-side hook that lets a send route hand its per-frame send path to a NATIVE
// sender (native/zigmedia, C ABI ravemedia.h) instead of the Go pump. Owner directive 2026-11-12:
// realtime paths are native. The control plane (negotiation, per-route key/salt derivation, route
// lifecycle, rate hints, 1 Hz stats) stays in Go; the media bytes never touch the Go heap.
//
// A source (e.g. the Linux VA-encode shim) that can push its ENCODED access units straight to the
// native sender implements NativeTXSource. When it does AND the route's transport is natively
// supported AND the per-route keys exist, pumpSource calls AttachNativeTX and only SUPERVISES; the Go
// media pump (runSend) does not run. Anything unsupported (TCP until it lands, a legacy peer without a
// per-route nonce, an attach error) falls back to the Go pump unchanged.

// NativeTXParams mirrors rm_open_params (ravemedia.h) EXACTLY. Go derives the per-route material and
// hands it over; the master peerlink secret is NEVER passed. The implementer (the source's cgo shim)
// converts PeerAddr/SocketFD + keys into rm_open_params and calls rm_tx_open.
type NativeTXParams struct {
	Transport  string   // TransportUDP | TransportTCP
	Codec      Codec    // encoded payload codec (frame header)
	Stream     uint16   // negotiated media stream id
	BodyMax    int      // UDP fragment body cap (0 → native default 1200)
	PeerAddr   string   // receiver's media "host:port" (socket story: native opens its own socket toward it)
	SocketFD   int64    // >=0 = a duplicated OS socket fd the native side takes; <0 = native opens one toward PeerAddr
	Encrypt    bool     // per-datagram AEAD (false = plaintext, token-gated)
	SendKey    [32]byte // this sender's AES-256 key (answerer = s2c direction)
	SendPrefix uint32   // explicit-nonce prefix (UDP)
	RecvKey    [32]byte // authenticates the receiver's control (NACK/ping/bye)
	RecvPrefix uint32   //
	Token      uint32   // per-route demux + anti-injection token (routeToken)
	InitialSeq uint64   // first datagram seq the native sender uses (continues the Go conn's seq space so its
	//                         // first datagram is STRICTLY-NEWEST at the receiver → the receiver rebinds the remote to
	//                         // the native socket on that datagram; avoids a stale-seq collision with the handshake pong)
	BitrateKbps  int           // initial rate hint
	FPS          int           // nominal frame rate (fps rung)
	StartupGrace time.Duration // liveness grace before the route is established (cold encoder/decoder)
	MediaClockNs int64         // media clock sampled just before attach: native rebases PTS into this domain (0 = sender-local mono)
}

// NativeTXStats mirrors rm_tx_stats; sampled by the router at ~1 Hz into RouteStat.
type NativeTXStats struct {
	AUsSubmitted  uint64
	AUsSent       uint64
	AUsDropped    uint64
	DatagramsSent uint64
	BytesSent     uint64
	Retransmits   uint64
	Seq           uint64
	QueueDepth    int
	Dead          bool
	NacksRecv     uint64 // authenticated NACK datagrams received (ABI v3)
	// Source-side (encode thread) telemetry, computed natively and sampled with the counters: the Go
	// pump that marks EncDone/TxQueue does not run on a native route.
	Keyframes uint64
	Enc, Tx   StageSnapshot
}

// NativeTX is a running native send path. The Go router supervises it (does not pump media): on Done()
// it ends the route; SetRateHint → SetRate; Stats() feeds RouteStat each second.
type NativeTX interface {
	// SetRate updates the live rate hint (kbps, fps). Safe to call concurrently with the source's AU
	// submission (rm_tx_set_rate is concurrency-safe with rm_tx_submit_au).
	SetRate(kbps, fps int)
	// Stats snapshots the native sender's counters (lock-free; concurrency-safe with submit).
	Stats() NativeTXStats
	// Done is closed when the native sender ends (liveness blackout, fatal socket, seq exhaustion, or a
	// received bye). The router then ends the route.
	Done() <-chan struct{}
	// Err reports why the native sender ended (nil if still running / clean).
	Err() error
	// Close stops the native sender (sends a clean bye) and frees it. No callback fires after it returns.
	Close() error
}

// NativeTXSource is the optional Source capability. A source that implements it can hand its encoded
// AU stream to the native sender. AUs are submitted exactly as Frame.Payload carries them today
// (Annex-B with in-band parameter sets; the caller sets the keyframe flag; no separate CONFIG AU).
type NativeTXSource interface {
	AttachNativeTX(NativeTXParams) (NativeTX, error)
}

// deriveRouteKeysRaw returns the per-direction raw AES-256-GCM keys + nonce prefixes for a route,
// mirroring newDgCrypto (the initiator seals c2s/opens s2c; the responder mirrors). The native sender
// is the ANSWERER → responder (initiator=false): send = s2c, recv = c2s.
func deriveRouteKeysRaw(master, salt []byte, initiator bool) (sendKey [32]byte, sendPre uint32, recvKey [32]byte, recvPre uint32, err error) {
	a, err := wirecrypto.HkdfSha256(master, salt, []byte(dgLabelC2S), 36)
	if err != nil {
		return
	}
	b, err := wirecrypto.HkdfSha256(master, salt, []byte(dgLabelS2C), 36)
	if err != nil {
		return
	}
	c2sKey, c2sPre := a[:32], binary.BigEndian.Uint32(a[32:])
	s2cKey, s2cPre := b[:32], binary.BigEndian.Uint32(b[32:])
	if initiator {
		copy(sendKey[:], c2sKey)
		sendPre = c2sPre
		copy(recvKey[:], s2cKey)
		recvPre = s2cPre
	} else {
		copy(sendKey[:], s2cKey)
		sendPre = s2cPre
		copy(recvKey[:], c2sKey)
		recvPre = c2sPre
	}
	return
}
