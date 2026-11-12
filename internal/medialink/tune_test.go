package medialink

import (
	"testing"
	"time"
)

// acceptFrag folds one media fragment into a reassembler at media-clock ns `at`.
func acceptFrag(r *reassembler, stream uint16, fseq uint32, idx, count uint16, at int64) {
	r.accept(dgHeader{Typ: dgTypeMedia, Stream: stream, FrameSeq: fseq, FragIdx: idx, FragCount: count},
		[]byte{byte(idx)}, at)
}

// TestScanNackOnlyProvenHoles (item 1): the fragment NACK fires only for a hole PROVEN missing - a
// frame overtaken by a later frame, or past its deadline - never for the newest frame still arriving
// (the cause of the 279 spurious NACKs at 0% loss).
func TestScanNackOnlyProvenHoles(t *testing.T) {
	mc := &manualClock{}
	u := &udpConn{clock: mc, reasm: newReassembler(0, 0), deadline: 200 * time.Millisecond}

	// Frame 1: fragments 0,1,3 arrive, fragment 2 is a hole. Frame 1 is the newest in-flight frame.
	acceptFrag(u.reasm, 1, 1, 0, 4, 1_000_000)
	acceptFrag(u.reasm, 1, 1, 1, 4, 1_100_000)
	acceptFrag(u.reasm, 1, 1, 3, 4, 1_200_000)

	// Well past the stale floor, but frame 1 is still the newest → fragment 2 may be in flight → no NACK.
	mc.ns.Store(1_200_000 + int64(udpNackStale)*4)
	if items := u.scanNack(mc.Now()); len(items) != 0 {
		t.Fatalf("NACKed the newest in-flight frame (spurious): %v", items)
	}

	// A later frame (2) starts arriving → frame 1 is overtaken → its hole is proven missing → NACK it.
	acceptFrag(u.reasm, 1, 2, 0, 4, mc.Now())
	mc.ns.Store(mc.Now() + int64(udpNackStale)*2)
	items := u.scanNack(mc.Now())
	if len(items) != 1 || items[0] != (nackItem{Stream: 1, FrameSeq: 1, FragIdx: 2}) {
		t.Fatalf("overtaken hole not NACKed once proven: %v", items)
	}
}

// TestScanNackDeadlineLastChance (item 1): a lone incomplete frame with no successor still gets a
// last-chance NACK as it approaches the reassembly deadline (so a tail-of-stream loss can recover).
func TestScanNackDeadlineLastChance(t *testing.T) {
	mc := &manualClock{}
	u := &udpConn{clock: mc, reasm: newReassembler(0, 0), deadline: 100 * time.Millisecond}
	acceptFrag(u.reasm, 1, 7, 0, 2, 1_000_000) // frame 7, fragment 1 missing, no later frame

	// Fresh: not overtaken, time left → no NACK.
	mc.ns.Store(1_000_000 + int64(udpNackStale)*2)
	if items := u.scanNack(mc.Now()); len(items) != 0 {
		t.Fatalf("NACKed a lone frame with time left: %v", items)
	}
	// Near the deadline (deadline - lead) → last-chance NACK.
	mc.ns.Store(1_000_000 + int64(u.deadline) - int64(udpNackDeadlineLead) + 1)
	if items := u.scanNack(mc.Now()); len(items) != 1 || items[0].FragIdx != 1 {
		t.Fatalf("no last-chance NACK near the deadline: %v", items)
	}
}

// TestOwdQueuingDelayOffsetFree (item 4): the published one-way-delay is queuing delay above a
// windowed-min baseline - never the raw transit - so a large constant clock offset (the -39 ms
// seen live) does NOT appear, and it is never negative.
func TestOwdQueuingDelayOffsetFree(t *testing.T) {
	u := &udpConn{}
	const offset = int64(-39_000_000) // like the live -39 ms clock offset
	var now int64
	for i := 0; i < 60; i++ {
		transit := offset + int64(i%4)*200_000 // baseline offset + 0..600 µs of jitter
		u.updateOwd(transit, now)
		now += 16_000_000
	}
	o := u.owdNs.Load()
	if o < 0 {
		t.Fatalf("owd negative (%d ns) - absolute transit leaked into the display", o)
	}
	if o > 3_000_000 { // queuing delay should be sub-ms-ish, not the 39 ms offset
		t.Fatalf("owd = %d ns reflects the clock offset, not queuing delay", o)
	}
}
