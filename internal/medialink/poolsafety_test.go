package medialink

import (
	"bytes"
	"testing"
)

// poolsafety_test.go - the send side must never keep a POOLED capture buffer in the NACK
// retransmit window. It used to: `rebuf.add(f)` retained Payload and skipped Release, so with NACK
// armed (always, on every route) the capture pool never got a buffer back and every readback
// re-allocated 8 MB (1080p) / 33 MB (4K). The oracle here is a pool that fails on a double release
// AND on a buffer that never comes back.

type testPool struct {
	t     *testing.T
	freed map[*byte]int
}

func newTestPool(t *testing.T) *testPool { return &testPool{t: t, freed: map[*byte]int{}} }

func (p *testPool) buf(n int, fill byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = fill
	}
	return b
}

func (p *testPool) release(b []byte) func() {
	return func() {
		p.t.Helper()
		k := &b[0]
		p.freed[k]++
		if p.freed[k] > 1 {
			p.t.Errorf("POISON: pooled buffer released %d times", p.freed[k])
		}
		for i := range b { // a recycled buffer is immediately reused by the next readback
			b[i] = 0xEE
		}
	}
}

func (p *testPool) count(b []byte) int { return p.freed[&b[0]] }

// TestRebufReleasesRawPooledFrames: a raw NRGBA route with NACK armed releases every pooled frame
// and keeps NOTHING in the window (one 4K frame would evict the whole 16 MB window anyway).
func TestRebufReleasesRawPooledFrames(t *testing.T) {
	p := newTestPool(t)
	rio := &routeIO{rebuf: newRetransmitBuf(0, 0)}
	var bufs [][]byte
	for seq := 0; seq < 4; seq++ {
		b := p.buf(64, byte(seq))
		bufs = append(bufs, b)
		rio.retainOrRelease(&Frame{Stream: 1, Kind: KindVideo, Codec: CodecNRGBA,
			Seq: uint32(seq), Payload: b, Release: p.release(b)})
	}
	for i, b := range bufs {
		if got := p.count(b); got != 1 {
			t.Fatalf("raw frame %d: released %d times, want exactly 1", i, got)
		}
	}
	if got := rio.rebuf.get(1, 0, 3); len(got) != 0 {
		t.Fatalf("raw pooled frames retained %d entries in the NACK window, want 0", len(got))
	}
	if rio.rebuf.bytes != 0 {
		t.Fatalf("window holds %d bytes of raw pixels, want 0", rio.rebuf.bytes)
	}
}

// TestRebufCopiesPooledAUs: a pooled COMPRESSED AU is copied into the window (retransmit still
// works) and the pooled buffer goes back - the copy must survive the buffer being recycled.
func TestRebufCopiesPooledAUs(t *testing.T) {
	p := newTestPool(t)
	rio := &routeIO{rebuf: newRetransmitBuf(0, 0)}
	b := p.buf(32, 0xA7)
	want := append([]byte(nil), b...)
	rio.retainOrRelease(&Frame{Stream: 1, Kind: KindVideo, Codec: CodecH264, Seq: 7,
		PTS: 42, Flags: FlagKeyframe, Payload: b, Release: p.release(b)})
	if got := p.count(b); got != 1 {
		t.Fatalf("pooled AU released %d times, want 1", got)
	}
	got := rio.rebuf.get(1, 7, 7)
	if len(got) != 1 {
		t.Fatalf("window holds %d frames for seq 7, want 1", len(got))
	}
	rf := got[0]
	if !bytes.Equal(rf.Payload, want) {
		t.Fatalf("retained AU = %x, want %x (the copy must outlive the recycled buffer)", rf.Payload, want)
	}
	if &rf.Payload[0] == &b[0] {
		t.Fatal("window aliases the pooled buffer - the next capture would overwrite the retransmit")
	}
	if rf.Release != nil {
		t.Fatal("retained copy must not carry a Release hook (it is not pooled)")
	}
	if rf.Seq != 7 || rf.PTS != 42 || !rf.Keyframe() {
		t.Fatalf("retained copy lost its header: %+v", rf)
	}
}

// TestRebufRetainsUnpooledAUs: the normal encoder path (fresh per-AU allocation, no Release) is
// retained as-is - no copy, no behaviour change.
func TestRebufRetainsUnpooledAUs(t *testing.T) {
	rio := &routeIO{rebuf: newRetransmitBuf(0, 0)}
	f := &Frame{Stream: 2, Kind: KindVideo, Codec: CodecHEVC, Seq: 3, Payload: []byte{1, 2, 3}}
	rio.retainOrRelease(f)
	got := rio.rebuf.get(2, 3, 3)
	if len(got) != 1 || got[0] != f {
		t.Fatalf("unpooled AU: window holds %d frames, want the original", len(got))
	}
}

// TestRebufExemptsUnpooledRawFrames: an UNPOOLED raw producer (webcam framepipe allocates a fresh
// buffer per frame, so Release == nil) must be exempt too. It was not: `case f.Release == nil`
// retained the frame, and at 1080p a single 8 MB raw frame displaces half the 16 MB window - i.e.
// the window degenerated into a 1-frame buffer that could retransmit nothing.
func TestRebufExemptsUnpooledRawFrames(t *testing.T) {
	rio := &routeIO{rebuf: newRetransmitBuf(0, 0)}
	au := &Frame{Stream: 1, Kind: KindVideo, Codec: CodecH264, Seq: 1, Payload: []byte{1, 2, 3}}
	rio.retainOrRelease(au) // a real AU is in the window first: the raw frame must not evict it
	for seq := 2; seq < 6; seq++ {
		rio.retainOrRelease(&Frame{Stream: 1, Kind: KindVideo, Codec: CodecNRGBA,
			Seq: uint32(seq), Payload: make([]byte, 1<<20)}) // 1 MB raw, no Release
	}
	if rio.rebuf.bytes != len(au.Payload) {
		t.Fatalf("window holds %d bytes, want only the %d-byte AU (raw frames must not enter)",
			rio.rebuf.bytes, len(au.Payload))
	}
	got := rio.rebuf.get(1, 1, 5)
	if len(got) != 1 || got[0] != au {
		t.Fatalf("window holds %d frames, want just the retained AU", len(got))
	}
}

// TestZeroCopyAUsEnterTheNACKWindow is the zigmedia inc-5 arm for the raw-video carve-out. The
// design (§12.1) flagged that carve-out as "an output-visible protocol feature switched off to
// relieve the allocator" and deferred lifting it to inc 5. The re-evaluation's verdict is that the
// feature was never off for the routes that matter, and this pins it: an encoder-child AU - the
// ONLY frame shape a zero-copy route puts on the wire, Release == nil because the child allocates
// it - must be retained verbatim and be retransmittable by seq.
//
// So the carve-out does not need lifting, and this test is what a future agent should break if they
// think otherwise.
func TestZeroCopyAUsEnterTheNACKWindow(t *testing.T) {
	rio := &routeIO{rebuf: newRetransmitBuf(0, 0)}
	var sent []*Frame
	for seq := 1; seq <= 8; seq++ {
		// A native/zero-copy route's AU: compressed, unpooled (procparent allocates per AU), and
		// realistically sized for 1080p inter-frames.
		f := &Frame{Stream: 7, Kind: KindVideo, Codec: CodecH264, Seq: uint32(seq),
			Payload: make([]byte, 24<<10)}
		sent = append(sent, f)
		rio.retainOrRelease(f)
	}
	got := rio.rebuf.get(7, 3, 6)
	if len(got) != 4 {
		t.Fatalf("the window holds %d of the 4 requested AUs - a zero-copy route cannot answer a NACK", len(got))
	}
	for i, f := range got {
		if f != sent[i+2] {
			t.Fatalf("retransmit %d returned seq %d, want %d (payload identity must survive)",
				i, f.Seq, sent[i+2].Seq)
		}
	}
	if rio.rebuf.bytes != 8*(24<<10) {
		t.Fatalf("window holds %d bytes, want the whole 8-AU burst", rio.rebuf.bytes)
	}
	// And the carve-out still bites for raw video on the SAME window: one 4K frame would evict all
	// of it, and an intra frame needs no retransmit.
	rio.retainOrRelease(&Frame{Stream: 7, Kind: KindVideo, Codec: CodecNRGBA, Seq: 99,
		Payload: make([]byte, 33<<20)})
	if got := rio.rebuf.get(7, 3, 6); len(got) != 4 {
		t.Fatalf("a raw frame evicted the AU window down to %d entries", len(got))
	}
}

// TestRebufOversizedPooledAUExempt: a pooled payload too big to copy is released, not retained.
func TestRebufOversizedPooledAUExempt(t *testing.T) {
	p := newTestPool(t)
	rio := &routeIO{rebuf: newRetransmitBuf(0, 0)}
	b := p.buf(rebufCopyMax+1, 9)
	rio.retainOrRelease(&Frame{Stream: 1, Kind: KindVideo, Codec: CodecH264, Seq: 1,
		Payload: b, Release: p.release(b)})
	if got := p.count(b); got != 1 {
		t.Fatalf("oversized pooled AU released %d times, want 1", got)
	}
	if got := rio.rebuf.get(1, 1, 1); len(got) != 0 {
		t.Fatalf("oversized pooled AU retained %d entries, want 0", len(got))
	}
}

// TestRebufNoWindowStillReleases: NACK unnegotiated → the pooled buffer still comes back.
func TestRebufNoWindowStillReleases(t *testing.T) {
	p := newTestPool(t)
	rio := &routeIO{}
	b := p.buf(16, 3)
	rio.retainOrRelease(&Frame{Kind: KindVideo, Codec: CodecNRGBA, Payload: b, Release: p.release(b)})
	if got := p.count(b); got != 1 {
		t.Fatalf("released %d times without a window, want 1", got)
	}
}
