package medialink

import (
	"sort"
	"sync"
	"time"
)

// telemetry.go is the §7 per-route accounting (P2): RFC 3550 §A.8 interarrival jitter, §6.4/§A.3
// cumulative + interval fraction lost, e2e latency percentiles (receiver mediaclock − PTS), and the
// Report meta-frame build/apply pair. Receiver-side numbers are computed on the runReceive
// goroutine; snapshots are mutex-guarded for the Stats() API.

// latWindowSize bounds the rolling latency sample window (per route).
const latWindowSize = 256

// latencyWindow is a fixed ring of e2e latency samples (ns) with percentile snapshots.
type latencyWindow struct {
	buf  [latWindowSize]int64
	n    int // filled count (≤ latWindowSize)
	next int // ring cursor
}

func (w *latencyWindow) add(v int64) {
	w.buf[w.next] = v
	w.next = (w.next + 1) % latWindowSize
	if w.n < latWindowSize {
		w.n++
	}
}

// percentiles returns rolling p50/p95/max over the window (zeros when empty).
func (w *latencyWindow) percentiles() (p50, p95, max int64) {
	if w.n == 0 {
		return 0, 0, 0
	}
	s := make([]int64, w.n)
	copy(s, w.buf[:w.n])
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[w.n/2], s[(w.n*95)/100], s[w.n-1]
}

// streamRecv is per-stream receive accounting (seq continuity, §A.8 jitter, §A.3 loss intervals).
type streamRecv struct {
	started      bool
	base         uint32 // first seq seen
	highest      uint32 // highest seq seen (signed-distance compare, wrap-safe)
	expect       uint32 // next expected seq
	received     uint64 // frames received (incl. dups/retransmits)
	hasTransit   bool
	lastTransit  int64   // arrival − PTS of the previous frame (ns)
	jitter       float64 // §A.8 EWMA, ns
	prevExpected uint64  // interval anchors for fraction-lost (§A.3)
	prevReceived uint64
}

// expected returns the cumulative expected frame count (highest − base + 1, wrap-safe).
func (s *streamRecv) expected() uint64 { return uint64(s.highest-s.base) + 1 }

// gapRange is a detected seq discontinuity: frames [From,To] never arrived (so far).
type gapRange struct {
	From, To uint32
	Gapped   bool
}

// routeStat is one route's live counters. Counter writes happen on the route's own goroutines;
// the mutex makes snapshots + cross-goroutine report reads safe.
type routeStat struct {
	session   string
	peer      string
	stream    uint16 // negotiated media stream id
	direction string // "send" | "recv"

	mu          sync.Mutex
	encoder     string // §3.2 negotiated encoder ("" = raw/echo route)
	tier        int
	software    bool
	keyframes   uint64 // video keyframes seen (sent or received)
	rateAt      time.Time
	rateBytes   uint64
	rateBps     float64
	frames      uint64
	bytes       uint64
	seqGaps     uint64
	lostEst     uint64
	recovered   uint64 // late frames (retransmit/dup) that filled a prior gap
	nacksSent   uint64
	retransmits uint64
	pliReqs     uint64
	reportsSent uint64
	reportsRecv uint64
	streams     map[uint16]*streamRecv
	lat         latencyWindow
	sentHighest uint32 // send side: last media seq written
	sentAny     bool
	remote      Report // last report received from the far end
	remoteAt    time.Time
}

func newRouteStat(session, peer string, stream uint16, direction string) *routeStat {
	return &routeStat{session: session, peer: peer, stream: stream, direction: direction,
		streams: map[uint16]*streamRecv{}}
}

// setCodec records the §3.2 negotiated encode choice (route-stat surface; both directions).
func (st *routeStat) setCodec(encoder string, tier int, software bool) {
	st.mu.Lock()
	st.encoder, st.tier, st.software = encoder, tier, software
	st.mu.Unlock()
}

// sent counts an outbound frame (media or meta).
func (st *routeStat) sent(f *Frame) {
	st.mu.Lock()
	st.frames++
	st.bytes += uint64(len(f.Payload))
	if f.Stream != metaStream {
		st.sentHighest, st.sentAny = f.Seq, true
		if f.Kind == KindVideo && f.Keyframe() {
			st.keyframes++
		}
	}
	st.mu.Unlock()
}

// recvStream updates a stream's seq continuity and returns any new gap. Caller holds st.mu.
func (st *routeStat) recvStream(f *Frame) gapRange {
	s := st.streams[f.Stream]
	if s == nil {
		s = &streamRecv{}
		st.streams[f.Stream] = s
	}
	var gap gapRange
	if !s.started {
		s.started = true
		s.base, s.highest, s.expect = f.Seq, f.Seq, f.Seq+1
		s.received = 1
		return gap
	}
	switch d := int32(f.Seq - s.expect); {
	case d == 0:
		s.expect++
	case d > 0:
		st.seqGaps++
		st.lostEst += uint64(d)
		gap = gapRange{From: s.expect, To: f.Seq - 1, Gapped: true}
		s.expect = f.Seq + 1
	default: // late arrival: retransmitted / duplicated frame filling a counted gap
		st.recovered++
		if st.lostEst > 0 {
			st.lostEst--
		}
	}
	s.received++
	if int32(f.Seq-s.highest) > 0 {
		s.highest = f.Seq
	}
	return gap
}

// recvMeta counts an inbound stream-0 meta frame (seq-tracked, never jitter/latency material).
func (st *routeStat) recvMeta(f *Frame) {
	st.mu.Lock()
	st.frames++
	st.bytes += uint64(len(f.Payload))
	st.recvStream(f)
	st.mu.Unlock()
}

// recvMedia counts an inbound media frame + updates §A.8 jitter and the e2e latency window.
// arrivalNs is the receiver media-clock at read. Returns any newly detected gap (NACK input).
func (st *routeStat) recvMedia(f *Frame, arrivalNs int64) gapRange {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.frames++
	st.bytes += uint64(len(f.Payload))
	if f.Kind == KindVideo && f.Keyframe() {
		st.keyframes++
	}
	gap := st.recvStream(f)
	s := st.streams[f.Stream]
	transit := arrivalNs - f.PTS
	if s.hasTransit {
		d := transit - s.lastTransit
		if d < 0 {
			d = -d
		}
		s.jitter += (float64(d) - s.jitter) / 16 // RFC 3550 §A.8, ns units (§2.1)
	}
	s.hasTransit, s.lastTransit = true, transit
	st.lat.add(transit) // e2e = receiver mediaclock − PTS (§7; accuracy = clock-sync quality)
	return gap
}

// addNACKSent counts an emitted NACK (recv side).
func (st *routeStat) addNACKSent() { st.mu.Lock(); st.nacksSent++; st.mu.Unlock() }

// addRetransmit counts a re-sent frame (send side; not double-counted in Frames/Bytes).
func (st *routeStat) addRetransmit() { st.mu.Lock(); st.retransmits++; st.mu.Unlock() }

// addPLI counts a keyframe request honoured/received.
func (st *routeStat) addPLI() { st.mu.Lock(); st.pliReqs++; st.mu.Unlock() }

// applyRemote stores the far end's latest report.
func (st *routeStat) applyRemote(r Report) {
	st.mu.Lock()
	st.remote, st.remoteAt = r, time.Now()
	st.reportsRecv++
	st.mu.Unlock()
}

// senderReport builds the RFC 3550 SR-semantic report (packet/octet counts + wall↔PTS anchor).
func (st *routeStat) senderReport(wallNs, ptsNs int64) Report {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.reportsSent++
	return Report{Type: MetaReport, Stream: st.stream, Packets: st.frames, Octets: st.bytes,
		HighestSeq: st.sentHighest, WallNanos: wallNs, PTSNanos: ptsNs}
}

// receiverReport builds the RR-semantic report for the negotiated media stream: cumulative lost,
// interval fraction lost (§A.3), §A.8 jitter. ok=false until media frames arrived.
func (st *routeStat) receiverReport(wallNs, ptsNs int64) (Report, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := st.streams[st.stream]
	if s == nil || !s.started {
		return Report{}, false
	}
	expected := s.expected()
	var lost int64
	if expected > s.received {
		lost = int64(expected - s.received)
	}
	expInt := expected - s.prevExpected
	recInt := s.received - s.prevReceived
	var frac float64
	if expInt > 0 && expInt > recInt {
		frac = float64(expInt-recInt) / float64(expInt)
	}
	s.prevExpected, s.prevReceived = expected, s.received
	st.reportsSent++
	return Report{Type: MetaReport, Stream: st.stream, HighestSeq: s.highest, Lost: lost,
		FractionLost: frac, Jitter: s.jitter, WallNanos: wallNs, PTSNanos: ptsNs}, true
}

// snapshot exports the route's counters for the Stats() API.
func (st *routeStat) snapshot() RouteStat {
	st.mu.Lock()
	defer st.mu.Unlock()
	// Rolling media bitrate: refresh the anchor at most every 500 ms of wall time.
	now := time.Now()
	if st.rateAt.IsZero() {
		st.rateAt, st.rateBytes = now, st.bytes
	} else if d := now.Sub(st.rateAt); d >= 500*time.Millisecond {
		st.rateBps = float64(st.bytes-st.rateBytes) * 8 / d.Seconds()
		st.rateAt, st.rateBytes = now, st.bytes
	}
	out := RouteStat{
		Session: st.session, Peer: st.peer, Stream: st.stream, Direction: st.direction,
		Frames: st.frames, Bytes: st.bytes,
		Encoder: st.encoder, Tier: st.tier, Software: st.software,
		Keyframes: st.keyframes, RateBps: st.rateBps,
		SeqGaps: st.seqGaps, LostEst: st.lostEst, Recovered: st.recovered,
		NACKsSent: st.nacksSent, Retransmits: st.retransmits, PLIRequests: st.pliReqs,
		ReportsSent: st.reportsSent, ReportsRecv: st.reportsRecv,
		RemoteAt: st.remoteAt,
	}
	if st.sentAny {
		out.HighestSeq = st.sentHighest
	}
	if s := st.streams[st.stream]; s != nil && s.started {
		out.HighestSeq = s.highest
		out.JitterNs = s.jitter
	}
	out.LatencyP50Ns, out.LatencyP95Ns, out.LatencyMaxNs = st.lat.percentiles()
	if st.reportsRecv > 0 {
		r := st.remote
		out.Remote = &r
	}
	return out
}

// RouteStat is a per-route traffic/loss/jitter/latency snapshot (§7; drives the telemetry panel).
type RouteStat struct {
	Session   string
	Peer      string
	Stream    uint16
	Direction string // "send" | "recv"
	Frames    uint64
	Bytes     uint64

	// §3.2 negotiated encode choice (video routes; zero values = raw/echo route).
	Encoder  string
	Tier     int
	Software bool // tier-4 software encode - surface the CPU warning

	Keyframes uint64  // video keyframes (send: emitted; recv: seen)
	RateBps   float64 // rolling media bitrate, bits/s

	SeqGaps   uint64 // detected sequence discontinuities (recv side)
	LostEst   uint64 // estimated missing frames (gap sizes − recovered)
	Recovered uint64 // late frames that filled a counted gap (retransmit/dup)

	NACKsSent   uint64 // recv side: NACK meta frames emitted (§2.5)
	Retransmits uint64 // send side: frames re-sent on NACK
	PLIRequests uint64 // keyframe requests (sent or honoured)

	ReportsSent uint64
	ReportsRecv uint64

	HighestSeq uint32  // recv: highest media seq seen; send: last media seq written
	JitterNs   float64 // RFC 3550 §A.8 interarrival jitter, ns (recv side)

	// Rolling e2e latency (receiver mediaclock − PTS, ns) over the last window. Accuracy is the
	// clock-sync quality (§2.3) - display alongside ClockQuality.
	LatencyP50Ns int64
	LatencyP95Ns int64
	LatencyMaxNs int64

	Remote   *Report // last report from the far end (nil = none yet)
	RemoteAt time.Time

	JB   *JitterStats   // recv video routes: adaptive buffer state (§3.3); nil = no buffer
	Pipe *PipelineStats // encode/decode child telemetry; nil = passthrough route
}
