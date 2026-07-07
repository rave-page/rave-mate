package medialink

import "sync"

// nack.go is the §2.5 loss machinery, TCP-profile half: the receiver NACKs seq gaps (RFC 4585
// semantics on the reserved MetaNACK type) and the sender selectively retransmits from a bounded
// buffer of recently sent media frames. On the reference LAN (ordered TCP) gaps only arise from
// application-edge policy drops; the same machinery carries the P8 UDP profile unchanged. Armed
// only when BOTH ends negotiated nack (Offer/Answer reserved field) - P1 peers stay dark.

const (
	rebufMaxFrames = 512      // default retransmit-buffer frame cap
	rebufMaxBytes  = 16 << 20 // default retransmit-buffer payload cap
)

// KeyframeSource is an optional Source extension: the route requests a fresh keyframe when the
// peer sends a PLI-style NACK (§2.5). Encoder-backed video sources implement it (P4).
type KeyframeSource interface {
	Source
	RequestKeyframe()
}

// retransmitBuf keeps recently sent media frames for NACK retransmit. FIFO, bounded by frame
// count AND payload bytes (oldest evicted first). Safe for concurrent use.
type retransmitBuf struct {
	mu        sync.Mutex
	frames    []*Frame
	bytes     int
	maxFrames int
	maxBytes  int
}

// newRetransmitBuf builds a buffer with the given caps (0 = defaults).
func newRetransmitBuf(maxFrames, maxBytes int) *retransmitBuf {
	if maxFrames <= 0 {
		maxFrames = rebufMaxFrames
	}
	if maxBytes <= 0 {
		maxBytes = rebufMaxBytes
	}
	return &retransmitBuf{maxFrames: maxFrames, maxBytes: maxBytes}
}

// add records a sent media frame (call after Stream/Seq are stamped).
func (b *retransmitBuf) add(f *Frame) {
	b.mu.Lock()
	b.frames = append(b.frames, f)
	b.bytes += len(f.Payload)
	for len(b.frames) > b.maxFrames || b.bytes > b.maxBytes {
		b.bytes -= len(b.frames[0].Payload)
		b.frames[0] = nil // release for GC before reslice
		b.frames = b.frames[1:]
	}
	b.mu.Unlock()
}

// get returns the buffered frames of stream with seq in [from,to] (inclusive, wrap-aware), in
// send order. Evicted seqs are simply absent.
func (b *retransmitBuf) get(stream uint16, from, to uint32) []*Frame {
	if int32(to-from) < 0 {
		return nil // empty/inverted range (PLI-only NACK)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*Frame
	for _, f := range b.frames {
		if f.Stream == stream && int32(f.Seq-from) >= 0 && int32(to-f.Seq) >= 0 {
			out = append(out, f)
		}
	}
	return out
}
