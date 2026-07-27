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

// Rate window (bitrate + wire fps). PRODUCER-driven: closed on the route's own goroutine as
// frames are counted, so the measured span is always the span the bytes were counted over.
// Computing it inside snapshot() made the number depend on WHO read it and how often - with the
// UI tick paused (activity governor) the next read divided the whole idle gap into "live
// bitrate", and with two pollers at different phases a reader got a window it never measured.
const rateWindow = time.Second

// rateStale is how long after the last counted frame the rolling rate stops being reported. A
// route that STOPPED must not keep showing the bitrate it had when it died - that is the same
// "healthy counters over a dead stream" failure the content oracle exists to catch.
const rateStale = 3 * time.Second

// Plausibility bounds for e2e transit (arrival − PTS). A duration only exists when the sender
// stamped PTS on the SAME media clock we read arrival from; a peer whose encode child stamps its
// own wall epoch (mediapipe) yields an epoch-sized value, and feeding that to the window rendered
// a TIMESTAMP as a latency (field: "latency 1785118072019.6 ms" = 2026-07-25 in ns). Out-of-range
// samples are counted, never believed. Generous on both ends: a slewing clock can go slightly
// negative and a saturated LAN route can genuinely sit seconds behind.
const (
	transitMinNs = int64(-2 * time.Second)
	transitMaxNs = int64(60 * time.Second)
)

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

	// now is the clock the rate window closes against (time.Now; a test seam).
	now func() time.Time

	mu        sync.Mutex
	encoder   string // §3.2 negotiated encoder ("" = raw/echo route)
	tier      int
	software  bool
	keyframes uint64 // video keyframes seen (sent or received)
	// Rate window anchors (see rateWindow): closed by count(), read-only in snapshot().
	rateAt      time.Time
	rateBytes   uint64
	rateFrames  uint64
	rateBps     float64
	rateFps     float64
	lastCount   time.Time // wall time of the last counted frame (staleness oracle)
	latBad      uint64    // transit samples outside the plausible range (foreign PTS domain)
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
		streams: map[uint16]*streamRecv{}, now: time.Now}
}

// count records one frame's payload and closes the rate window when it is due. Caller holds st.mu.
// This is the ONLY writer of the rate anchors - snapshot() is a pure read (see rateWindow).
func (st *routeStat) count(n int) {
	st.frames++
	st.bytes += uint64(n)
	now := st.now()
	st.lastCount = now
	if st.rateAt.IsZero() {
		st.rateAt, st.rateBytes, st.rateFrames = now, st.bytes, st.frames
		return
	}
	d := now.Sub(st.rateAt)
	if d < rateWindow {
		return
	}
	secs := d.Seconds()
	st.rateBps = float64(st.bytes-st.rateBytes) * 8 / secs
	st.rateFps = float64(st.frames-st.rateFrames) / secs
	st.rateAt, st.rateBytes, st.rateFrames = now, st.bytes, st.frames
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
	st.count(len(f.Payload))
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
	st.count(len(f.Payload))
	st.recvStream(f)
	st.mu.Unlock()
}

// recvMedia counts an inbound media frame + updates §A.8 jitter and the e2e latency window.
// arrivalNs is the receiver media-clock at read. Returns any newly detected gap (NACK input).
func (st *routeStat) recvMedia(f *Frame, arrivalNs int64) gapRange {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.count(len(f.Payload))
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
	// e2e = receiver mediaclock − PTS (§7; accuracy = clock-sync quality). Jitter above is a
	// DIFFERENCE of transits, so a constant clock-domain offset cancels there and it stays valid
	// either way; the absolute latency does not. Reject the implausible instead of rendering it.
	if transit >= transitMinNs && transit <= transitMaxNs {
		st.lat.add(transit)
	} else {
		st.latBad++
	}
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
	out := RouteStat{
		Session: st.session, Peer: st.peer, Stream: st.stream, Direction: st.direction,
		Frames: st.frames, Bytes: st.bytes,
		Encoder: st.encoder, Tier: st.tier, Software: st.software,
		Keyframes: st.keyframes, RateBps: st.rateBps, WireFPS: st.rateFps,
		SeqGaps: st.seqGaps, LostEst: st.lostEst, Recovered: st.recovered,
		NACKsSent: st.nacksSent, Retransmits: st.retransmits, PLIRequests: st.pliReqs,
		ReportsSent: st.reportsSent, ReportsRecv: st.reportsRecv,
		LatUnsynced: st.latBad, RemoteAt: st.remoteAt,
	}
	// Stalled route: the last window's rate is not "live", it is the rate this route HAD when it
	// stopped. Report nothing rather than a comforting number.
	if st.lastCount.IsZero() || st.now().Sub(st.lastCount) > rateStale {
		out.RateBps, out.WireFPS = 0, 0
	}
	if st.sentAny {
		out.HighestSeq = st.sentHighest
	}
	if s := st.streams[st.stream]; s != nil && s.started {
		out.HighestSeq = s.highest
		out.JitterNs = s.jitter
	}
	out.LatencySamples = st.lat.n
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
	RateBps   float64 // rolling media bitrate, bits/s (0 = no frame within rateStale)
	// WireFPS is frames/s ON THE ROUTE over the same window. It is NOT the encoder's OutFPS: a
	// route whose encoder emits 40 fps while the wire carries 4 reads "healthy" on OutFPS alone,
	// and separating the two is the only way to see that at a glance.
	WireFPS float64

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
	// clock-sync quality (§2.3) - display alongside ClockQuality. Meaningless unless
	// LatencySamples > 0: renderers MUST check that before printing a duration (see
	// LatUnsynced).
	LatencyP50Ns int64
	LatencyP95Ns int64
	LatencyMaxNs int64

	// LatencySamples is how many PLAUSIBLE transit samples the window holds; LatUnsynced counts
	// the rejected ones - media frames whose PTS is not on our media clock (a peer whose encode
	// child stamps its own epoch, or an unsynced clock domain). Non-zero LatUnsynced with zero
	// samples means "this peer's PTS domain is foreign", not "latency is huge".
	LatencySamples int
	LatUnsynced    uint64

	Remote   *Report // last report from the far end (nil = none yet)
	RemoteAt time.Time

	JB   *JitterStats   // recv video routes: adaptive buffer state (§3.3); nil = no buffer
	Pipe *PipelineStats // encode/decode child telemetry; nil = passthrough route
}
