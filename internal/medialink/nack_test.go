package medialink

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// TestRetransmitBufBounds: range get, stream filter, count + byte eviction.
func TestRetransmitBufBounds(t *testing.T) {
	b := newRetransmitBuf(4, 0)
	mk := func(stream uint16, seq uint32, size int) *Frame {
		return &Frame{Stream: stream, Kind: KindVideo, Seq: seq, Payload: make([]byte, size)}
	}
	for seq := uint32(0); seq < 6; seq++ {
		b.add(mk(1, seq, 10))
	}
	// Cap 4: seqs 0,1 evicted.
	if got := b.get(1, 0, 5); len(got) != 4 || got[0].Seq != 2 || got[3].Seq != 5 {
		t.Fatalf("eviction: got %d frames, first=%v", len(got), got[0].Seq)
	}
	if got := b.get(1, 3, 4); len(got) != 2 || got[0].Seq != 3 || got[1].Seq != 4 {
		t.Fatalf("range get: %v", got)
	}
	if got := b.get(2, 0, 99); got != nil {
		t.Fatalf("wrong stream must return nothing: %v", got)
	}
	if got := b.get(1, 5, 4); got != nil {
		t.Fatalf("inverted (PLI-only) range must return nothing: %v", got)
	}

	// Byte cap evicts regardless of frame count.
	bb := newRetransmitBuf(100, 25)
	for seq := uint32(0); seq < 5; seq++ {
		bb.add(mk(1, seq, 10))
	}
	if got := bb.get(1, 0, 4); len(got) != 2 || got[0].Seq != 3 {
		t.Fatalf("byte eviction: %d frames, first=%v", len(got), got[0].Seq)
	}
}

// kfSource is an idle KeyframeSource: Next blocks until ctx cancel; RequestKeyframe signals.
type kfSource struct{ ch chan struct{} }

func (s *kfSource) Next(ctx context.Context) (*Frame, error) {
	<-ctx.Done()
	return nil, io.EOF
}
func (s *kfSource) Close() error { return nil }
func (s *kfSource) RequestKeyframe() {
	select {
	case s.ch <- struct{}{}:
	default:
	}
}

// TestNACKRetransmitPipe drives the full §2.5 TCP-profile loss loop over net.Pipe: the sender
// policy-drops two frames at the application edge, the receiver NACKs the gap, the sender
// retransmits from its buffer, the receiver recovers - every step counted. Then a PLI-style NACK
// reaches the KeyframeSource.
func TestNACKRetransmitPipe(t *testing.T) {
	key := testKey()
	cR, cS := net.Pipe()
	defer func() { _ = cR.Close(); _ = cS.Close() }()
	connR, err := NewConn(cR, key, true) // receiver = dialer = initiator
	if err != nil {
		t.Fatal(err)
	}
	connS, err := NewConn(cS, key, false)
	if err != nil {
		t.Fatal(err)
	}

	rm := New(Options{Self: "X"})
	stS := newRouteStat("sess", "peer", 1, "send")
	rioS := &routeIO{conn: connS, st: stS, caps: sessionCaps{nack: true}, rebuf: newRetransmitBuf(0, 0)}
	stR := newRouteStat("sess", "peer", 1, "recv")
	rioR := &routeIO{conn: connR, st: stR, caps: sessionCaps{nack: true}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kf := &kfSource{ch: make(chan struct{}, 1)}
	go rm.sendControl(cancel, rioS, kf)
	sink := newCollectSink(6)
	go func() { _ = rm.runReceive(ctx, rioR, sink) }()

	// Sender edge: frames 0..5 buffered for retransmit; 2 and 3 policy-dropped (never written).
	for seq := uint32(0); seq < 6; seq++ {
		f := &Frame{Stream: 1, Kind: KindVideo, Codec: CodecJPEG, Seq: seq, PTS: int64(seq + 1), Payload: []byte{byte(seq)}}
		rioS.rebuf.add(f)
		if seq == 2 || seq == 3 {
			continue
		}
		if err := connS.WriteFrame(f); err != nil {
			t.Fatal(err)
		}
		stS.sent(f)
	}

	select {
	case <-sink.done:
	case <-time.After(3 * time.Second):
		sink.mu.Lock()
		t.Fatalf("recovery incomplete: %d/6 frames", len(sink.got))
	}
	// All six unique seqs arrived (4 in order + 2 retransmitted late).
	seen := map[uint32]bool{}
	sink.mu.Lock()
	for _, f := range sink.got {
		seen[f.Seq] = true
	}
	sink.mu.Unlock()
	for seq := uint32(0); seq < 6; seq++ {
		if !seen[seq] {
			t.Fatalf("seq %d never delivered", seq)
		}
	}

	r := stR.snapshot()
	if r.SeqGaps != 1 || r.NACKsSent != 1 {
		t.Fatalf("receiver gaps/nacks = %d/%d, want 1/1", r.SeqGaps, r.NACKsSent)
	}
	if r.Recovered != 2 || r.LostEst != 0 {
		t.Fatalf("recovered/lost = %d/%d, want 2/0", r.Recovered, r.LostEst)
	}
	waitFor(t, time.Second, func() bool { return stS.snapshot().Retransmits == 2 })

	// PLI-style NACK (inverted range + pli flag) → keyframe request reaches the source.
	if err := rioR.writeMeta(NACK{Type: MetaNACK, Stream: 1, From: 1, To: 0, FrameLevel: true}, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-kf.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("RequestKeyframe never called")
	}
	waitFor(t, time.Second, func() bool { return stS.snapshot().PLIRequests == 1 })
	// The PLI must not have caused any retransmit (empty range).
	if got := stS.snapshot().Retransmits; got != 2 {
		t.Fatalf("retransmits after PLI = %d, want 2", got)
	}
}
