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
	rebufCopyMax   = 1 << 20  // largest POOLED payload copied into the window (encoded-AU class)
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

// retainOrRelease disposes of a frame the send loop just wrote: keep it in the retransmit window,
// or hand its pooled buffer back. Keyed on OWNERSHIP, not codec tier:
//
//   - no window (nack unnegotiated): release.
//   - not pooled (Release == nil): the encoder allocated this AU for us - retain as is, free.
//   - pooled + compressed AU within rebufCopyMax: COPY the (small) AU into the window, then
//     release the pooled buffer.
//   - pooled raw pixels (or an oversized AU): exempt from the window - release now. Retaining one
//     starves the capture pool, so every readback re-allocates 8 MB (1080p) / 33 MB (4K) - exactly
//     the GC churn the pool removed - and buys nothing: raw frames are intra (the receiver resyncs
//     on the very next frame) and ONE 4K frame would evict the entire 16 MB window anyway.
func (rio *routeIO) retainOrRelease(f *Frame) {
	switch {
	case rio.rebuf == nil:
	case f.Release == nil:
		rio.rebuf.add(f)
		return
	case f.Codec.CompressedVideo() && len(f.Payload) <= rebufCopyMax:
		cp := *f
		cp.Payload = append([]byte(nil), f.Payload...)
		cp.Release = nil
		rio.rebuf.add(&cp)
	}
	if f.Release != nil {
		f.Release()
	}
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
