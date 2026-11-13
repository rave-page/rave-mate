package mfenc

import (
	"errors"
	"time"
)

// netrx.go: the cross-platform surface of a decode session's NETWORK source (child proto v5,
// native/zigenc/src/netrx.zig). The parent negotiates + handshakes, then hands the bound UDP socket
// and the per-route keys to the child; the zigmedia receiver runs socket RX -> playout -> decode ->
// publish inside the child. Implementation: procnet_windows.go; stubs elsewhere.

// ErrNetRXUnsupported: this session/child cannot take a network source (old child, no telemetry
// block, platform). The caller keeps the Go receive path.
var ErrNetRXUnsupported = errors.New("mfenc: native network receive unsupported")

// NetRXParams is one attach. Socket is a socket handle owned by THIS process that the session
// duplicates into the child on every (re)attach; on success the session owns it (closed by Close).
type NetRXParams struct {
	Socket        uintptr // OS socket handle (Windows SOCKET) - parked, never read by Go
	Peer          string  // sender "a.b.c.d:port" ("" = learn from the first datagram); IPv4 only
	Encrypt       bool
	Token         uint32
	OpenKey       [32]byte // opens the sender's media (receiver recv direction)
	OpenPrefix    uint32
	SendKey       [32]byte // seals OUR control (NACK/PLI/ping/meta)
	SendPrefix    uint32
	BodyMax       int
	FPS           float64
	Preset        string // playout preset name (low-latency | balanced | smooth)
	Stream        uint16
	InitialSeq    uint64 // first control datagram seq (must lie ABOVE every seq this socket already sent)
	MaxFrameBytes int
}

// NetRXStats is the child's receive telemetry (shm block, ~4 Hz).
type NetRXStats struct {
	Datagrams, Delivered, Published         uint64
	NacksSent, NackRecovered, NackExpired   uint64
	PLIsSent, ReasmDrops, SlotDrops         uint64
	MetasSent                               uint64
	LossPct                                 float64
	OwdNs, OwdJitterNs, RTTns               int64
	PlayoutMs, TargetMs, MaxMs              float64
	Misses, Misses10m, Repeats, Repeats10m  uint64
	Rebases, GapDrops, Grows, Decays        uint64
	PubCount                                uint64
	PubP50Ms, PubP99Ms, PubMaxMs, PubMeanMs float64
	PubOver25, PubOver50, PubOver100        uint64
	RelP99Ms, RelMaxMs                      float64 // release (playout) intervals, before decode
	RelOver25                               uint64
	CtlSeq                                  uint64 // next control seq the receiver will use
	KeyWaits, Rebinds                       uint64
	MediaBytes, Keyframes                   uint64
	MetaInDrops, Buffered, DecErrors        uint64
	RxErrors                                uint64        // socket receive errors (a deaf socket shows here)
	Bye                                     bool          // the sender ended the route cleanly
	SinceRx                                 time.Duration // age of the last authenticated datagram (-1 = none yet)
	Running                                 bool          // a receiver is attached right now
}

// NetMeta is one sender->receiver meta frame (sync pong, report) forwarded by the child. Age is how
// long ago (child monotonic clock) it arrived off the socket, so the caller can place T4 exactly.
type NetMeta struct {
	JSON []byte
	Age  time.Duration
}
