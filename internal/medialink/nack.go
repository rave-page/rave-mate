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

// Zigmedia inc-5 re-evaluation of the raw-video carve-out (design §12.1 called it "the sharpest
// finding in the inventory: an output-visible protocol feature switched off to relieve the
// allocator", and deferred lifting it to this increment). VERDICT: it stays, because its real
// justification was never the allocator.
//
//   - The feature the design worried was off is NOT off. Every COMPRESSED route - zero-copy or
//     readback - hands its AUs here with Release == nil (the encoder child allocates each AU), so
//     they are retained verbatim and are fully retransmittable. TestZeroCopyAUsEnterTheNACKWindow
//     is the arm that pins it.
//   - What is excluded is raw video only, and for two reasons that survive the allocator's removal:
//     the window's cap is 16 MB, so ONE 4K frame evicts all of it (a 1-frame window retransmits
//     nothing useful); and raw frames are intra, so the receiver resyncs on the very next frame -
//     retransmitting a 33 MB stale frame over the wire is strictly worse than skipping it.
//   - Inc 2 had already re-keyed the test from ownership (Release == nil) to RAW, which is what
//     removed the actual bug: UNPOOLED raw producers were being retained, so every webcam frame
//     displaced the whole window.
//
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

// rawExempt reports whether f is raw video, which NEVER enters the retransmit window whatever its
// buffer ownership. The window's byte cap is 16 MB: one 4K frame (33 MB) evicts all of it and even
// a 1080p frame (8 MB) evicts half, so a raw stream turns the window into a 1-frame buffer that
// retransmits nothing useful. It buys nothing either - raw frames are intra, so the receiver
// resyncs on the very next frame.
//
// Ownership is deliberately NOT part of this test. Keying on `Release == nil` (the old rule) meant
// UNPOOLED raw producers were retained: webcam's framepipe allocates a fresh buffer per frame
// (internal/webcam/framepipe.go), so every webcam frame on a nack-negotiated route displaced the
// whole window.
func rawExempt(f *Frame) bool {
	return f.Kind == KindVideo && !f.Codec.CompressedVideo()
}

// retainOrRelease disposes of a frame the send loop just wrote: keep it in the retransmit window,
// or hand its pooled buffer back.
//
//   - no window (nack unnegotiated): release.
//   - raw video (rawExempt): never retained - see above.
//   - not pooled (Release == nil): the encoder allocated this AU for us - retain as is, free. This
//     is the path EVERY compressed route takes, zero-copy included.
//   - pooled + compressed AU within rebufCopyMax: COPY the (small) AU into the window, then
//     release the pooled buffer.
//   - pooled oversized AU (> 1 MiB): exempt - release now. A single AU that large is a whole
//     intra picture on a starved link; copying it in would evict most of a 16 MB window to
//     retransmit one frame the receiver can resync past. (The original rationale here was
//     allocator relief - "retaining starves the capture pool"; inc 5 restates it on protocol
//     grounds so a future agent does not delete the rung along with the pool.)
func (rio *routeIO) retainOrRelease(f *Frame) {
	switch {
	case rio.rebuf == nil:
	case rawExempt(f):
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
