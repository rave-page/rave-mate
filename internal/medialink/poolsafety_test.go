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
