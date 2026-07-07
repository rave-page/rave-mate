package medialink

import "testing"

// Jitter-buffer tests drive the pure core with a synthetic clock: interval 10 ms (fps 100),
// transit base 1 ms. deadline(f) = PTS + base + depth·interval.

const (
	jbtInterval = int64(10_000_000) // 10 ms
	jbtBase     = int64(1_000_000)  // 1 ms nominal transit
)

// jbtPush pushes seq i with PTS = i·interval and the given transit.
func jbtPush(b *jitterBuffer, seq uint32, transit int64, key bool) {
	f := &Frame{Kind: KindVideo, Codec: CodecHEVC, Seq: seq, PTS: int64(seq) * jbtInterval}
	if key {
		f.Flags = FlagKeyframe
	}
	b.push(f, f.PTS+transit)
}

// drainAt pops everything releasable at now.
func drainAt(b *jitterBuffer, now int64) []*Frame {
	var out []*Frame
	for {
		f, _ := b.pop(now)
		if f == nil {
			return out
		}
		out = append(out, f)
	}
}

func TestJitterBufferReleasesInOrder(t *testing.T) {
	b := newJitterBuffer(100, false)
	for i := uint32(0); i < 5; i++ {
		jbtPush(b, i, jbtBase, i == 0)
	}
	// Before any deadline: nothing releases; wake = frame 0's deadline.
	if f, wake := b.pop(0); f != nil || wake != jbtBase+jbtInterval {
		t.Fatalf("pop early: f=%v wake=%d", f, wake)
	}
	got := drainAt(b, 10*jbtInterval)
	if len(got) != 5 {
		t.Fatalf("released %d/5", len(got))
	}
	for i, f := range got {
		if f.Seq != uint32(i) {
			t.Fatalf("order: got seq %d at %d", f.Seq, i)
		}
	}
	if s := b.stats(); s.Depth != 1 || s.PolicyDrops != 0 || s.Late != 0 {
		t.Fatalf("stats: %+v", s)
	}
}

func TestJitterBufferGrowOnBigLate(t *testing.T) {
	b := newJitterBuffer(100, true)
	for i := uint32(0); i < 10; i++ {
		jbtPush(b, i, jbtBase, true)
	}
	// One frame late by > one interval → immediate grow (§3.3), regardless of sample count.
	jbtPush(b, 10, jbtBase+2*jbtInterval+jbtInterval/2, true)
	if s := b.stats(); s.Depth != 2 || s.Grows != 1 || s.Late != 1 {
		t.Fatalf("stats after burst: %+v", s)
	}
}

func TestJitterBufferGrowOnLateRate(t *testing.T) {
	b := newJitterBuffer(100, true)
	// 24 on-time frames, then one mildly late (late by < interval): 1/25 = 4% > 2% with ≥20 samples.
	for i := uint32(0); i < 24; i++ {
		jbtPush(b, i, jbtBase, true)
	}
	jbtPush(b, 24, jbtBase+jbtInterval+jbtInterval/2, true) // deadline slack is depth·interval
	if s := b.stats(); s.Depth != 2 || s.Grows != 1 {
		t.Fatalf("late-rate grow: %+v", s)
	}
}

func TestJitterBufferDecayAfterClean(t *testing.T) {
	b := newJitterBuffer(100, true)
	jbtPush(b, 0, jbtBase, true)
	jbtPush(b, 1, jbtBase+2*jbtInterval+jbtInterval/2, true) // grow → depth 2
	if b.stats().Depth != 2 {
		t.Fatal("precondition: grow")
	}
	// >30 s of clean frames → decay back to 1.
	n := uint32(2)
	for ; int64(n)*jbtInterval < jbDecayCleanNs+8*jbtInterval; n++ {
		jbtPush(b, n, jbtBase, true)
		drainAt(b, int64(n)*jbtInterval+jbtBase+5*jbtInterval) // keep occupancy bounded
	}
	if s := b.stats(); s.Depth != 1 || s.Decays != 1 {
		t.Fatalf("decay: %+v", s)
	}
}

func TestJitterBufferKeyframePolicyOnGap(t *testing.T) {
	var plis int
	b := newJitterBuffer(100, false) // inter-coded
	b.pli = func() { plis++ }
	jbtPush(b, 0, jbtBase, true)
	jbtPush(b, 1, jbtBase, false)
	jbtPush(b, 2, jbtBase, false)
	// seq 3 lost; 4..6 delta frames arrive.
	for i := uint32(4); i <= 6; i++ {
		jbtPush(b, i, jbtBase, false)
	}
	// Head 0..2 release; head 4 has a missing dependency → held to its deadline first.
	now := int64(2)*jbtInterval + jbtBase + jbtInterval // past seq 2's deadline, before 4's
	got := drainAt(b, now)
	if len(got) != 3 {
		t.Fatalf("pre-gap release: %d", len(got))
	}
	if f, wake := b.pop(now); f != nil || wake == 0 {
		t.Fatalf("gap head must wait for its deadline: f=%v wake=%d", f, wake)
	}
	// Past every deadline: 4..6 policy-drop, PLI fires once, buffer waits for a key.
	got = drainAt(b, 20*jbtInterval)
	if len(got) != 0 {
		t.Fatalf("undecodable deltas must not release: %d", len(got))
	}
	s := b.stats()
	if s.PolicyDrops != 3 || !s.WaitingKey || s.PLIsSent != 1 || plis != 1 {
		t.Fatalf("policy: %+v plis=%d", s, plis)
	}
	// Keyframe arrives → delivered, waitKey cleared; a later delta flows again.
	jbtPush(b, 7, jbtBase, true)
	jbtPush(b, 8, jbtBase, false)
	got = drainAt(b, 20*jbtInterval)
	if len(got) != 2 || !got[0].Keyframe() || got[1].Seq != 8 {
		t.Fatalf("post-key release: %+v", got)
	}
	if b.stats().WaitingKey {
		t.Fatal("waitKey must clear on keyframe")
	}
}

func TestJitterBufferIntraGapResyncs(t *testing.T) {
	var plis int
	b := newJitterBuffer(100, true) // MJPEG/raw: every frame decodable
	b.pli = func() { plis++ }
	jbtPush(b, 0, jbtBase, true)
	jbtPush(b, 3, jbtBase, false) // gap 1-2
	// Drain at each deadline (paced sink - no stale catch-up in play).
	got := drainAt(b, jbtBase+jbtInterval+1)
	got = append(got, drainAt(b, 3*jbtInterval+jbtBase+jbtInterval+1)...)
	if len(got) != 2 || got[1].Seq != 3 {
		t.Fatalf("intra resync: %+v", got)
	}
	if s := b.stats(); s.PolicyDrops != 0 || plis != 0 {
		t.Fatalf("intra must not drop/PLI: %+v", s)
	}
}

func TestJitterBufferStaleCatchUp(t *testing.T) {
	b := newJitterBuffer(100, false)
	jbtPush(b, 0, jbtBase, true)
	jbtPush(b, 1, jbtBase, false)
	jbtPush(b, 2, jbtBase, false)
	jbtPush(b, 3, jbtBase, true) // newer resync point
	jbtPush(b, 4, jbtBase, false)
	// Sink stalled way past every deadline → skip to the buffered keyframe.
	got := drainAt(b, 100*jbtInterval)
	if len(got) != 2 || got[0].Seq != 3 || got[1].Seq != 4 {
		t.Fatalf("catch-up: %+v", got)
	}
	if s := b.stats(); s.PolicyDrops != 3 {
		t.Fatalf("catch-up drops: %+v", s)
	}
}

func TestJitterBufferLateRetransmitFillsGap(t *testing.T) {
	b := newJitterBuffer(100, false)
	jbtPush(b, 0, jbtBase, true)
	jbtPush(b, 2, jbtBase, false) // gap: 1 missing
	jbtPush(b, 1, jbtBase+jbtInterval/2, false)
	got := drainAt(b, 10*jbtInterval)
	if len(got) != 3 || got[1].Seq != 1 || got[2].Seq != 2 {
		t.Fatalf("retransmit fill: %+v", got)
	}
	if s := b.stats(); s.PolicyDrops != 0 || s.PLIsSent != 0 {
		t.Fatalf("no drops expected: %+v", s)
	}
	// A retransmit of an already-delivered seq is a counted dup.
	jbtPush(b, 1, jbtBase, false)
	if b.stats().Dups != 1 {
		t.Fatal("dup not counted")
	}
}

func TestJitterBufferHardCap(t *testing.T) {
	b := newJitterBuffer(100, false)
	for i := uint32(0); i <= uint32(jbInterCapFrames)+8; i++ {
		jbtPush(b, i, jbtBase, i%40 == 0)
	}
	s := b.stats()
	if s.PolicyDrops == 0 {
		t.Fatalf("hard cap must policy-drop: %+v", s)
	}
	if s.Buffered > jbInterCapFrames {
		t.Fatalf("occupancy not bounded: %+v", s)
	}
	// A plain burst below the cap never drops (pacing handles it).
	b2 := newJitterBuffer(100, false)
	for i := uint32(0); i < 60; i++ {
		jbtPush(b2, i, jbtBase, i == 0)
	}
	if s := b2.stats(); s.PolicyDrops != 0 || s.Buffered != 60 {
		t.Fatalf("burst below cap dropped: %+v", s)
	}
}
