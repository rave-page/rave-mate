//go:build linux && zignative

package medialink

// nativesender_cgo_linux.go is a THIN cgo handle to the Zig rm_tx_* sender (native/zigmedia, C ABI
// include/ravemedia.h), used ONLY by the Linux interop test (native sender -> a real Go udpConn). It
// is NOT the production attach path - that is the encode host's shim (internal/pwcapture, branch
// feat/pw-4k). This exists to prove the ABI contract end to end: that a native sender CONTINUING the
// Go conn's datagram seq space rebinds a real udpConn and that the reverse (seq 0 vs an advanced
// window) is rejected. Built only under `-tags zignative` on Linux; a plain build never compiles it,
// so it needs no libravemedia.a. Build the lib first:
//   cd native/zigmedia && zig build -Dtarget=x86_64-linux-gnu --release=fast
// then run in the Linux build container (Go+gcc+CGO, glibc):
//   go test -tags zignative -run NativeSender ./internal/medialink/

/*
#cgo CFLAGS: -I${SRCDIR}/../../native/zigmedia/include
#cgo LDFLAGS: ${SRCDIR}/../../native/zigmedia/zig-out/lib/libravemedia.a -lpthread -lm
#include <stdlib.h>
#include "ravemedia.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// cgoNativeSender wraps one running rm_sender. close() is idempotent.
type cgoNativeSender struct {
	s *C.rm_sender
}

// nativeABIVersion returns the linked lib's ABI version (sanity that the header + lib agree).
func nativeABIVersion() uint32 { return uint32(C.rm_abi_version()) }

// nativeWireSelftest runs the lib's built-in wire golden check (0 = pass).
func nativeWireSelftest() int { return int(C.rm_wire_selftest()) }

// openCgoNativeSender builds rm_open_params from Go-derived material and opens a UDP sender toward
// peerAddr (sockaddr_in bytes). The native sender is the answerer/responder (initiator=0); keys are
// derived by the caller (deriveRouteKeysRaw(master,salt,false)). initialSeq continues the Go conn's
// datagram seq space so the first datagram is strictly-newest at the receiver.
func openCgoNativeSender(peerAddr []byte, token uint32, encrypt bool, sendKey [32]byte, sendPre uint32,
	recvKey [32]byte, recvPre uint32, codec uint8, stream uint16, bodyMax int, initialSeq uint64,
	rateBytesPerSec float64) (*cgoNativeSender, error) {
	return openCgoNativeSenderClock(peerAddr, token, encrypt, sendKey, sendPre, recvKey, recvPre, codec, stream,
		bodyMax, initialSeq, rateBytesPerSec, 0)
}

// openCgoNativeSenderClock is openCgoNativeSender with media_clock_ns (PTS domain; 0 = sender-local mono).
func openCgoNativeSenderClock(peerAddr []byte, token uint32, encrypt bool, sendKey [32]byte, sendPre uint32,
	recvKey [32]byte, recvPre uint32, codec uint8, stream uint16, bodyMax int, initialSeq uint64,
	rateBytesPerSec float64, mediaClockNs int64) (*cgoNativeSender, error) {

	var p C.rm_open_params
	p.abi_version = C.uint32_t(C.RM_ABI_VERSION)
	p.struct_size = C.uint32_t(unsafe.Sizeof(p))
	p.transport = C.RM_TRANSPORT_UDP
	p.codec = C.rm_codec(codec)
	p.stream = C.uint16_t(stream)
	p.body_max = C.int32_t(bodyMax)
	p.socket_fd = -1 // native opens its own socket toward peer_addr

	// peer_addr must be valid only for the (synchronous) connect inside rm_tx_open.
	caddr := C.CBytes(peerAddr)
	defer C.free(caddr)
	p.peer_addr = (*C.uint8_t)(caddr)
	p.peer_addr_len = C.int32_t(len(peerAddr))

	p.keys.token = C.uint32_t(token)
	if encrypt {
		p.keys.encrypt = 1
	}
	p.keys.initiator = 0 // answerer/responder
	p.keys.send_nonce_prefix = C.uint32_t(sendPre)
	p.keys.recv_nonce_prefix = C.uint32_t(recvPre)
	for i := 0; i < 32; i++ {
		p.keys.send_key[i] = C.uint8_t(sendKey[i])
		p.keys.recv_key[i] = C.uint8_t(recvKey[i])
	}
	p.initial_rate_bytes_per_sec = C.double(rateBytesPerSec)
	p.initial_seq = C.uint64_t(initialSeq)
	p.fps = 60
	p.startup_grace_ns = 0 // native default (15s)
	p.media_clock_ns = C.int64_t(mediaClockNs)

	var out *C.rm_sender
	if rc := C.rm_tx_open(&p, nil, nil, &out); rc != C.RM_OK {
		return nil, fmt.Errorf("rm_tx_open: rc=%d", int(rc))
	}
	return &cgoNativeSender{s: out}, nil
}

// openCgoNativeSenderTCP opens a TCP native sender on an already-connected socket (caller keeps
// ownership of fd and closes it after close()).
func openCgoNativeSenderTCP(fd int, encrypt bool, sendKey, recvKey [32]byte, codec uint8, stream uint16, mediaClockNs int64) (*cgoNativeSender, error) {
	var p C.rm_open_params
	p.abi_version = C.uint32_t(C.RM_ABI_VERSION)
	p.struct_size = C.uint32_t(unsafe.Sizeof(p))
	p.transport = C.RM_TRANSPORT_TCP
	p.codec = C.rm_codec(codec)
	p.stream = C.uint16_t(stream)
	p.socket_fd = C.int64_t(fd)
	if encrypt {
		p.keys.encrypt = 1
	}
	for i := 0; i < 32; i++ {
		p.keys.send_key[i] = C.uint8_t(sendKey[i])
		p.keys.recv_key[i] = C.uint8_t(recvKey[i])
	}
	p.fps = 60
	p.media_clock_ns = C.int64_t(mediaClockNs)
	var out *C.rm_sender
	if rc := C.rm_tx_open(&p, nil, nil, &out); rc != C.RM_OK {
		return nil, fmt.Errorf("rm_tx_open(tcp): rc=%d", int(rc))
	}
	return &cgoNativeSender{s: out}, nil
}

// submitAU hands one encoded access unit to the native sender (non-blocking; copied synchronously).
func (n *cgoNativeSender) submitAU(au []byte, ptsNs int64, keyframe bool) {
	var ptr *C.uint8_t
	if len(au) > 0 {
		ptr = (*C.uint8_t)(unsafe.Pointer(&au[0]))
	}
	var flags C.uint32_t
	if keyframe {
		flags = C.RM_FLAG_KEYFRAME
	}
	C.rm_tx_submit_au(n.s, ptr, C.size_t(len(au)), C.int64_t(ptsNs), flags)
}

// fullStats snapshots every rm_tx_stats counter.
func (n *cgoNativeSender) fullStats() NativeTXStats {
	var st C.rm_tx_stats
	C.rm_tx_stats_get(n.s, &st)
	return NativeTXStats{AUsSubmitted: uint64(st.aus_submitted), AUsSent: uint64(st.aus_sent),
		AUsDropped: uint64(st.aus_dropped), DatagramsSent: uint64(st.datagrams_sent), BytesSent: uint64(st.bytes_sent),
		Retransmits: uint64(st.retransmits), Seq: uint64(st.seq), QueueDepth: int(st.queue_depth),
		Dead: st.dead != 0, NacksRecv: uint64(st.nacks_recv)}
}

// rateHint returns the last receiver MetaRate the native sender received (gen 0 = none).
func (n *cgoNativeSender) rateHint() (RateHint, uint64) {
	var h C.rm_rate_hint
	C.rm_tx_rate_hint(n.s, &h)
	return RateHint{Type: MetaRate, MaxBitrateKbps: int(h.max_bitrate_kbps), MaxFPS: int(h.max_fps),
		MaxHeight: int(h.max_height), QualityTarget: int(h.quality)}, uint64(h.gen)
}

// stats snapshots the lock-free counters.
func (n *cgoNativeSender) stats() (sent, retransmits, seq uint64, dead bool) {
	var st C.rm_tx_stats
	C.rm_tx_stats_get(n.s, &st)
	return uint64(st.aus_sent), uint64(st.retransmits), uint64(st.seq), st.dead != 0
}

// close sends a clean bye and frees the sender. Idempotent.
func (n *cgoNativeSender) close() {
	if n.s != nil {
		C.rm_tx_close(n.s)
		n.s = nil
	}
}
