package medialink

import (
	"math"
	"testing"
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
