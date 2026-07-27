package medialink

import (
	"math"
	"testing"
	"time"
)

// TestJitterRFC3550A8 replays the §A.8 EWMA by hand: PTS spaced 10 ms, arrivals alternating
// ±1 ms of on-time → |D| alternates 2 ms/0 ms after the first pair.
func TestJitterRFC3550A8(t *testing.T) {
	st := newRouteStat("s", "p", 1, "recv")
	const ptsStep = int64(10_000_000)
	arrivalJit := []int64{0, 1_000_000, 0, 1_000_000, 0, 1_000_000, 0, 1_000_000}
	var wantJ float64
	var lastTransit int64
	for i, aj := range arrivalJit {
		pts := int64(i) * ptsStep
		arrival := pts + 5_000_000 + aj // constant 5 ms transit + alternating jitter
		st.recvMedia(&Frame{Stream: 1, Kind: KindAudio, Seq: uint32(i), PTS: pts, Payload: []byte{1}}, arrival)
		transit := arrival - pts
		if i > 0 {
			d := transit - lastTransit
			if d < 0 {
				d = -d
			}
			wantJ += (float64(d) - wantJ) / 16
		}
		lastTransit = transit
	}
	got := st.snapshot().JitterNs
	if math.Abs(got-wantJ) > 1e-9 {
		t.Fatalf("jitter = %v, want %v", got, wantJ)
	}
	if got <= 0 {
		t.Fatal("jitter must be positive for a jittery arrival pattern")
	}
}

// TestLatencyWindowPercentiles: known sample set → exact p50/p95/max; ring overwrite works.
func TestLatencyWindowPercentiles(t *testing.T) {
	var w latencyWindow
	if p50, p95, max := w.percentiles(); p50 != 0 || p95 != 0 || max != 0 {
		t.Fatal("empty window must report zeros")
	}
	for i := int64(1); i <= 100; i++ {
		w.add(i * 1000)
	}
	p50, p95, max := w.percentiles()
	if p50 != 51_000 || p95 != 96_000 || max != 100_000 {
		t.Fatalf("p50/p95/max = %d/%d/%d", p50, p95, max)
	}
	// Overflow the ring: newest latWindowSize samples win.
	for i := int64(0); i < latWindowSize; i++ {
		w.add(5_000)
	}
	if _, _, max := w.percentiles(); max != 5_000 {
		t.Fatalf("ring not overwritten: max=%d", max)
	}
}

// TestRecvGapDupAccounting covers gap detection, duplicate/late recovery, and highest tracking.
func TestRecvGapDupAccounting(t *testing.T) {
	st := newRouteStat("s", "p", 1, "recv")
	f := func(seq uint32) *Frame {
		return &Frame{Stream: 1, Kind: KindVideo, Seq: seq, PTS: int64(seq) * 1000, Payload: []byte{9}}
	}
	if g := st.recvMedia(f(0), 0); g.Gapped {
		t.Fatal("first frame is never a gap")
	}
	if g := st.recvMedia(f(1), 0); g.Gapped {
		t.Fatal("in-order frame is not a gap")
	}
	g := st.recvMedia(f(5), 0) // 2,3,4 missing
	if !g.Gapped || g.From != 2 || g.To != 4 {
		t.Fatalf("gap = %+v, want [2,4]", g)
	}
	// Late retransmit of seq 3 fills one estimated loss.
	if g := st.recvMedia(f(3), 0); g.Gapped {
		t.Fatal("late frame must not re-count a gap")
	}
	s := st.snapshot()
	if s.SeqGaps != 1 || s.LostEst != 2 || s.Recovered != 1 {
		t.Fatalf("gaps/lost/recovered = %d/%d/%d, want 1/2/1", s.SeqGaps, s.LostEst, s.Recovered)
	}
	if s.HighestSeq != 5 {
		t.Fatalf("highest = %d, want 5", s.HighestSeq)
	}
	if s.Frames != 4 {
		t.Fatalf("frames = %d, want 4", s.Frames)
	}
}

// TestReceiverReportIntervals checks §A.3 cumulative + interval fraction-lost math.
func TestReceiverReportIntervals(t *testing.T) {
	st := newRouteStat("s", "p", 1, "recv")
	if _, ok := st.receiverReport(1, 2); ok {
		t.Fatal("no media yet → no report")
	}
	f := func(seq uint32) *Frame {
		return &Frame{Stream: 1, Kind: KindAudio, Seq: seq, PTS: 1, Payload: []byte{1}}
	}
	for _, seq := range []uint32{0, 1, 2, 3} {
		st.recvMedia(f(seq), 10)
	}
	r, ok := st.receiverReport(111, 222)
	if !ok || r.Type != MetaReport || r.Stream != 1 {
		t.Fatalf("report = %+v ok=%v", r, ok)
	}
	if r.Lost != 0 || r.FractionLost != 0 || r.HighestSeq != 3 {
		t.Fatalf("clean interval: %+v", r)
	}
	if r.WallNanos != 111 || r.PTSNanos != 222 {
		t.Fatalf("anchor not carried: %+v", r)
	}
	// Second interval: 4..9 expected, 6,7 lost → 8 expected-in-interval? No: expected interval =
	// highest moves 3→9 (6 new), received 4 of them → fraction 2/6.
	for _, seq := range []uint32{4, 5, 8, 9} {
		st.recvMedia(f(seq), 20)
	}
	r2, ok := st.receiverReport(0, 0)
	if !ok {
		t.Fatal("second report missing")
	}
	if r2.Lost != 2 {
		t.Fatalf("cumulative lost = %d, want 2", r2.Lost)
	}
	if math.Abs(r2.FractionLost-2.0/6.0) > 1e-12 {
		t.Fatalf("fraction lost = %v, want 1/3", r2.FractionLost)
	}
	// Third interval: retransmits 6,7 arrive → cumulative lost back to 0, no new loss.
	st.recvMedia(f(6), 30)
	st.recvMedia(f(7), 30)
	r3, _ := st.receiverReport(0, 0)
	if r3.Lost != 0 || r3.FractionLost != 0 {
		t.Fatalf("post-recovery report: %+v", r3)
	}
}

// TestSenderReportCounts: packets/octets/highest + wall↔PTS anchor.
func TestSenderReportCounts(t *testing.T) {
	st := newRouteStat("s", "p", 3, "send")
	st.sent(&Frame{Stream: 3, Seq: 0, Payload: make([]byte, 100)})
	st.sent(&Frame{Stream: 3, Seq: 1, Payload: make([]byte, 50)})
	r := st.senderReport(77, 88)
	if r.Packets != 2 || r.Octets != 150 || r.HighestSeq != 1 || r.WallNanos != 77 || r.PTSNanos != 88 {
		t.Fatalf("sender report: %+v", r)
	}
	if st.snapshot().ReportsSent != 1 {
		t.Fatal("reportsSent not counted")
	}
}

// TestApplyRemote stores the far-end report for the Stats() snapshot.
func TestApplyRemote(t *testing.T) {
	st := newRouteStat("s", "p", 1, "send")
	if st.snapshot().Remote != nil {
		t.Fatal("no remote report yet")
	}
	st.applyRemote(Report{Type: MetaReport, Stream: 1, FractionLost: 0.25, Jitter: 42})
	s := st.snapshot()
	if s.Remote == nil || s.Remote.FractionLost != 0.25 || s.Remote.Jitter != 42 {
		t.Fatalf("remote = %+v", s.Remote)
	}
	if s.ReportsRecv != 1 || s.RemoteAt.IsZero() {
		t.Fatalf("reportsRecv/remoteAt: %+v", s)
	}
}

// TestRateWindowIsProducerDriven: the rolling bitrate/wire-fps must be anchored by the FRAMES, not
// by whoever polls Stats(). It used to be computed inside snapshot(), so the number was whatever
// the last reader's phase happened to measure - and with the UI tick paused (activity governor)
// the first read after the pause divided the whole idle gap into "live bitrate".
func TestRateWindowIsProducerDriven(t *testing.T) {
	st := newRouteStat("s", "p", 1, "send")
	now := time.Unix(1000, 0)
	st.now = func() time.Time { return now }

	// 2 s of 60 fps × 1000 B, nobody reading.
	for i := 0; i < 120; i++ {
		st.sent(&Frame{Stream: 1, Kind: KindVideo, Payload: make([]byte, 1000)})
		now = now.Add(time.Second / 60)
	}
	s := st.snapshot()
	if s.RateBps < 470_000 || s.RateBps > 490_000 {
		t.Fatalf("RateBps = %.0f, want ~480000 (60 × 1000 B × 8) measured without any prior read", s.RateBps)
	}
	if s.WireFPS < 55 || s.WireFPS > 65 {
		t.Fatalf("WireFPS = %.1f, want ~60", s.WireFPS)
	}

	// snapshot() is a PURE read: at the same instant, reading again gives the same answer and
	// moves no state. (Across time it decays - that is the stall behaviour below, not a reader
	// effect: the window's boundaries come from the ring, never from the caller.)
	if again := st.snapshot(); again.RateBps != s.RateBps || again.WireFPS != s.WireFPS {
		t.Fatalf("reading the stats moved them: %.0f/%.1f → %.0f/%.1f",
			s.RateBps, s.WireFPS, again.RateBps, again.WireFPS)
	}

	// Frames stop. The last window's rate is not "live" - a dead route must not keep advertising
	// the bitrate it had when it died.
	now = now.Add(rateStale + time.Second)
	if dead := st.snapshot(); dead.RateBps != 0 || dead.WireFPS != 0 {
		t.Fatalf("a stalled route still reports %.0f bps / %.1f fps", dead.RateBps, dead.WireFPS)
	}
}

// TestRateWindowSurvivesClumpedBytes is the field reproduction, deterministic: frames arrive at a
// CONSTANT cadence while the bytes clump (small P-frames + a periodic keyframe-sized payload).
// Windows shorter than the clump interval then miss the clump entirely in some windows and report
// the trickle alone - on the wire that was 0.1 Mbps on a route whose real mean was ~0.9 Mbps, i.e.
// a healthy route displaying the number an operator reads as BLACK FRAMES.
//
// A source that PAUSES between bursts does not reproduce it: a frame-driven window then closes on
// the first frame of the next burst and always spans exactly one burst. The frames must keep
// coming while only their SIZE clumps.
func TestRateWindowSurvivesClumpedBytes(t *testing.T) {
	st := newRouteStat("s", "p", 1, "recv")
	base := time.Unix(1000, 0)
	now := base
	st.now = func() time.Time { return now }

	const (
		step       = 25 * time.Millisecond   // 40 fps, constant
		small      = 300                     // trickle payload
		clumpEvery = 1300 * time.Millisecond // keyframe-ish cadence
		clump      = 120_000
		run        = 30 * time.Second
	)
	truth := (float64(small)*float64(time.Second/step) + float64(clump)/clumpEvery.Seconds()) * 8

	var lastClump time.Duration
	var readings []float64
	var seq uint32
	for elapsed := time.Duration(0); elapsed < run; elapsed += step {
		now = base.Add(elapsed)
		size := small
		if elapsed-lastClump >= clumpEvery {
			lastClump, size = elapsed, clump
		}
		st.recvMedia(&Frame{Stream: 1, Kind: KindVideo, Seq: seq, Payload: make([]byte, size)}, int64(elapsed))
		seq++
		// Sample every 500 ms once the window is fully populated.
		if elapsed > rateSpan+time.Second && elapsed%(500*time.Millisecond) == 0 {
			readings = append(readings, st.snapshot().RateBps)
		}
	}
	if len(readings) < 20 {
		t.Fatalf("only %d readings", len(readings))
	}
	for i, r := range readings {
		if r < truth/2 || r > truth*2 {
			t.Fatalf("reading %d = %.0f bps against a mean of %.0f - a clumped but healthy route "+
				"must never display a fraction of its real rate (all: %v)", i, r, truth, readings)
		}
	}
}

// TestLatencyPercentilesAreOrdered pins the invariant the panel violated: p50 ≤ p95 ≤ max, on a
// distribution that includes NEGATIVE transits. A negative transit is normal - the two media
// clocks are process-relative and only the sync tier aligns them - and it is exactly the case
// that produced "latency 29.0 ms/26.1 ms p50/p95" in the field, because the renderers printed
// both through an abs(). The values were always ordered; the display was not.
func TestLatencyPercentilesAreOrdered(t *testing.T) {
	st := newRouteStat("s", "p", 1, "recv")
	// A window straddling zero: median negative, tail positive - the field shape.
	for i := -60; i <= 40; i++ {
		st.recvMedia(&Frame{Stream: 1, Kind: KindVideo, Seq: uint32(i + 60), Payload: []byte{1}},
			int64(i)*int64(time.Millisecond))
	}
	s := st.snapshot()
	if s.LatencySamples == 0 {
		t.Fatal("no plausible samples - the premise is broken")
	}
	if !(s.LatencyP50Ns <= s.LatencyP95Ns && s.LatencyP95Ns <= s.LatencyMaxNs) {
		t.Fatalf("percentiles out of order: p50=%d p95=%d max=%d",
			s.LatencyP50Ns, s.LatencyP95Ns, s.LatencyMaxNs)
	}
	if s.LatencyP50Ns >= 0 {
		t.Fatalf("premise: this distribution must have a negative median, got p50=%d", s.LatencyP50Ns)
	}
}
