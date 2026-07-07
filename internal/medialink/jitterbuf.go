package medialink

import "sync"

// jitterbuf.go - §3.3 adaptive receive jitter buffer + keyframe-aware policy drops (P4). Sits
// between the route conn and the (decoder-wrapped) sink on video recv routes. Frames release in
// seq order once the media clock passes PTS + base + depth·interval, where base is a windowed
// min-transit estimate - self-calibrating, so it works even when the two clocks share no epoch
// (monotonic tier). Grow fast / decay slow per §3.3; every drop is counted (§7 no-silent-drops).
// Inter-coded streams never feed the decoder an undecodable run: after an unrecovered gap the
// buffer drops to the next keyframe and requests one over the control plane (PLI, §2.5).

const (
	jbMinDepth       = 1
	jbMaxDepth       = 4              // §3.3 cap: 4 frames video
	jbGrowWindowNs   = 2_000_000_000  // late-rate sliding window (grow)
	jbDecayCleanNs   = 30_000_000_000 // sustained-clean time before decay
	jbGrowLateRate   = 0.02           // §3.3: late-rate > 2% → grow
	jbDecayLateRate  = 0.001          // §3.3: late-rate < 0.1% for 30 s → decay
	jbMinSamples     = 20             // never grow off a handful of frames
	jbPLIMinGapNs    = 250_000_000    // PLI rate limit
	jbBaseBuckets    = 4              // min-transit window = 4 × 2 s buckets
	jbBaseBucketNs   = 2_000_000_000  // one transit-min bucket
	jbStaleGraceIntv = 2              // head is "stale" past deadline + this many intervals
	jbDefaultFPS     = 60.0           // frame interval fallback when the advert carried none

	// Memory guards (pending-frame caps). INTRA streams (raw RGBA / MJPEG) have no decode
	// dependency, so deep buffering buys nothing - a couple of frames is plenty and keeps the
	// footprint tiny (raw 720p = ~3.7 MB/frame, so the old 256 MB cap pinned ~69 frames = a
	// quarter-gig PER ROUTE for zero benefit - a big part of the webcam→Spout RAM blowup).
	// INTER streams keep a larger frame cap (encoded frames are small) but a sane byte ceiling.
	jbIntraCapFrames = 6
	jbIntraCapBytes  = 48 << 20 // ~13× 720p raw frames - hard ceiling even at 4K raw
	jbInterCapFrames = 120
	jbInterCapBytes  = 64 << 20
)

// JitterStats is one buffer's live telemetry (§7: current depth, late rate, resize + drop proof).
type JitterStats struct {
	Depth       int     // current B, frames
	Buffered    int     // frames pending release
	LateRate    float64 // late fraction over the current 2 s window
	Late        uint64  // total late frames
	PolicyDrops uint64  // keyframe-policy + overflow drops (counted, logged by the route)
	Grows       uint64  // depth resize events
	Decays      uint64
	Dups        uint64 // duplicate / already-delivered arrivals (late retransmits)
	WaitingKey  bool   // holding for a keyframe after an unrecoverable gap
	PLIsSent    uint64
}

type jbSample struct {
	at   int64 // arrival ns (prune key)
	late bool
}

// jitterBuffer is the per-route buffer. push comes from the route recv loop, pop from the release
// runner; the mutex covers both.
type jitterBuffer struct {
	mu        sync.Mutex
	interval  int64 // frame interval ns
	intra     bool  // every frame independently decodable (raw pixels / MJPEG)
	depth     int
	capFrames int // pending-frame hard cap (intra: tiny; inter: larger)
	capBytes  int // pending-payload hard cap

	started      bool
	nextSeq      uint32
	pending      []*Frame // seq-ascending
	pendingBytes int

	// transit-min window (base): current bucket + previous mins.
	bucketStart int64
	bucketMin   int64
	bucketHas   bool
	prevMins    []int64

	// §3.3 grow/decay bookkeeping.
	samples    []jbSample
	cleanSince int64
	lastPLI    int64
	waitKey    bool

	late        uint64
	policyDrops uint64
	grows       uint64
	decays      uint64
	dups        uint64
	plis        uint64

	pli    func()        // rate-limited keyframe request (set once before use)
	notify chan struct{} // cap-1 kick for the release runner
}

// newJitterBuffer builds a buffer for one video stream. fps sizes the frame interval (0 = 60);
// intra marks codecs where any frame resyncs the decoder.
func newJitterBuffer(fps float64, intra bool) *jitterBuffer {
	if fps <= 0 {
		fps = jbDefaultFPS
	}
	capFrames, capBytes := jbInterCapFrames, jbInterCapBytes
	if intra {
		capFrames, capBytes = jbIntraCapFrames, jbIntraCapBytes
	}
	return &jitterBuffer{
		interval:  int64(1e9 / fps),
		intra:     intra,
		depth:     jbMinDepth,
		capFrames: capFrames,
		capBytes:  capBytes,
		notify:    make(chan struct{}, 1),
	}
}

// deadline is a frame's release time on the media clock. Caller holds mu.
func (b *jitterBuffer) deadline(f *Frame) int64 {
	return f.PTS + b.base() + int64(b.depth)*b.interval
}

// base returns the windowed min transit (0 until the first frame). Caller holds mu.
func (b *jitterBuffer) base() int64 {
	var m int64
	has := false
	if b.bucketHas {
		m, has = b.bucketMin, true
	}
	for _, v := range b.prevMins {
		if !has || v < m {
			m, has = v, true
		}
	}
	if !has {
		return 0
	}
	return m
}

// push inserts a received frame (arrival = media clock at read). Late retransmits of already
// delivered/skipped seqs are dropped as dups.
func (b *jitterBuffer) push(f *Frame, arrival int64) {
	b.mu.Lock()
	if !b.started {
		b.started = true
		b.nextSeq = f.Seq
		b.cleanSince = arrival
	}
	if int32(f.Seq-b.nextSeq) < 0 {
		b.dups++
		b.mu.Unlock()
		return
	}
	// Insert seq-sorted from the tail (in-order arrival is the common case).
	i := len(b.pending)
	for i > 0 && int32(b.pending[i-1].Seq-f.Seq) > 0 {
		i--
	}
	if i > 0 && b.pending[i-1].Seq == f.Seq {
		b.dups++
		b.mu.Unlock()
		return
	}
	b.pending = append(b.pending, nil)
	copy(b.pending[i+1:], b.pending[i:])
	b.pending[i] = f
	b.pendingBytes += len(f.Payload)

	b.updateBase(arrival-f.PTS, arrival)
	b.accountLateness(f, arrival)
	fire := b.overflowDrop(arrival)
	b.mu.Unlock()

	if fire && b.pli != nil {
		b.pli()
	}
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

// updateBase folds a transit sample into the min window. Caller holds mu.
func (b *jitterBuffer) updateBase(transit, now int64) {
	if !b.bucketHas {
		b.bucketStart, b.bucketMin, b.bucketHas = now, transit, true
		return
	}
	if now-b.bucketStart >= jbBaseBucketNs {
		b.prevMins = append(b.prevMins, b.bucketMin)
		if len(b.prevMins) > jbBaseBuckets-1 {
			b.prevMins = b.prevMins[1:]
		}
		b.bucketStart, b.bucketMin = now, transit
		return
	}
	if transit < b.bucketMin {
		b.bucketMin = transit
	}
}

// accountLateness records the §3.3 late sample and applies grow/decay. Caller holds mu.
func (b *jitterBuffer) accountLateness(f *Frame, arrival int64) {
	lateBy := arrival - b.deadline(f)
	late := lateBy > 0
	b.samples = append(b.samples, jbSample{at: arrival, late: late})
	// Prune the 2 s window.
	cut := 0
	for cut < len(b.samples) && arrival-b.samples[cut].at > jbGrowWindowNs {
		cut++
	}
	b.samples = b.samples[cut:]

	var lateN int
	for _, s := range b.samples {
		if s.late {
			lateN++
		}
	}
	rate := float64(lateN) / float64(len(b.samples))
	if late {
		b.late++
	}
	if rate >= jbDecayLateRate {
		b.cleanSince = arrival
	}
	// Grow fast: late-rate over threshold, or any frame late by more than one interval.
	if b.depth < jbMaxDepth &&
		((late && lateBy > b.interval) || (len(b.samples) >= jbMinSamples && rate > jbGrowLateRate)) {
		b.depth++
		b.grows++
		b.samples = b.samples[:0] // fresh window - don't re-trigger on the same burst
		b.cleanSince = arrival
		return
	}
	// Decay slow: sustained clean.
	if b.depth > jbMinDepth && arrival-b.cleanSince >= jbDecayCleanNs {
		b.depth--
		b.decays++
		b.cleanSince = arrival
	}
}

// overflowDrop is the memory guard: on a runaway buffer (sink stalled long past deadlines) drop
// from the head until a keyframe leads or occupancy halves. Burst arrivals below the hard cap are
// NEVER dropped here - pacing (pop) handles them. Returns true when the caller should fire a PLI
// (after releasing mu). Caller holds mu.
func (b *jitterBuffer) overflowDrop(now int64) bool {
	if len(b.pending) <= b.capFrames && b.pendingBytes <= b.capBytes {
		return false
	}
	dropped := false
	for len(b.pending) > b.capFrames/2 || b.pendingBytes > b.capBytes/2 {
		h := b.pending[0]
		if dropped && (h.Keyframe() || b.intra) {
			break // resync point reached
		}
		b.dropHead()
		dropped = true
	}
	if dropped && !b.intra && !(len(b.pending) > 0 && b.pending[0].Keyframe()) {
		return b.enterWaitKey(now)
	}
	return false
}

// enterWaitKey arms keyframe-wait; true = fire the rate-limited PLI after releasing mu.
func (b *jitterBuffer) enterWaitKey(now int64) bool {
	b.waitKey = true
	if b.pli != nil && (b.lastPLI == 0 || now-b.lastPLI >= jbPLIMinGapNs) {
		b.lastPLI = now
		b.plis++
		return true
	}
	return false
}

// pop returns the next releasable frame, or nil + the wake time (0 = empty, wait for push).
// Applies the keyframe-aware policy: in waitKey mode non-key frames drop immediately; a head
// following an unrecovered gap on an inter-coded stream is held to its deadline (NACK window),
// then dropped with a PLI.
func (b *jitterBuffer) pop(now int64) (*Frame, int64) {
	f, wake, fire := b.popLocked(now)
	if fire && b.pli != nil {
		b.pli()
	}
	return f, wake
}

func (b *jitterBuffer) popLocked(now int64) (*Frame, int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fire := false
	for len(b.pending) > 0 {
		h := b.pending[0]
		if b.waitKey {
			if !h.Keyframe() {
				b.dropHead()
				continue
			}
			b.waitKey = false
		}
		// Catch-up: head is stale (sink persistently behind) and a newer resync point is
		// buffered → skip forward (latency beats completeness on a live stream).
		if now > b.deadline(h)+jbStaleGraceIntv*b.interval {
			if ki := b.nextResyncIdx(); ki > 0 {
				for j := 0; j < ki; j++ {
					b.dropHead()
				}
				continue
			}
		}
		gap := h.Seq != b.nextSeq
		if gap && !b.intra && !h.Keyframe() {
			// Missing dependency: give retransmit until the deadline, then give up + PLI.
			if dl := b.deadline(h); now < dl {
				return nil, dl, fire
			}
			b.dropHead()
			fire = b.enterWaitKey(now) || fire
			continue
		}
		if dl := b.deadline(h); now < dl {
			return nil, dl, fire
		}
		b.pending[0] = nil
		b.pending = b.pending[1:]
		b.pendingBytes -= len(h.Payload)
		b.nextSeq = h.Seq + 1
		return h, 0, fire
	}
	return nil, 0, fire
}

// nextResyncIdx returns the index of the nearest buffered resync point PAST the head (keyframe;
// any frame on intra streams), or 0 when none. Caller holds mu.
func (b *jitterBuffer) nextResyncIdx() int {
	if b.intra {
		if len(b.pending) > 1 {
			return 1
		}
		return 0
	}
	for i := 1; i < len(b.pending); i++ {
		if b.pending[i].Keyframe() {
			return i
		}
	}
	return 0
}

// dropHead policy-drops the head frame. Caller holds mu.
func (b *jitterBuffer) dropHead() {
	h := b.pending[0]
	b.pending[0] = nil
	b.pending = b.pending[1:]
	b.pendingBytes -= len(h.Payload)
	b.nextSeq = h.Seq + 1
	b.policyDrops++
}

// stats snapshots the buffer counters (§7).
func (b *jitterBuffer) stats() JitterStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	var lateN int
	for _, s := range b.samples {
		if s.late {
			lateN++
		}
	}
	var rate float64
	if len(b.samples) > 0 {
		rate = float64(lateN) / float64(len(b.samples))
	}
	return JitterStats{
		Depth: b.depth, Buffered: len(b.pending), LateRate: rate, Late: b.late,
		PolicyDrops: b.policyDrops, Grows: b.grows, Decays: b.decays, Dups: b.dups,
		WaitingKey: b.waitKey, PLIsSent: b.plis,
	}
}
