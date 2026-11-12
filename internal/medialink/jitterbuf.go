package medialink

import (
	"strings"
	"sync"
)

// jitterbuf.go - §3.3 receive jitter buffer + keyframe-aware policy drops (P4). LEGACY Go data path
// (stopgap until the native Zig receiver, ZIGMEDIA_DESIGN.md): policy only, no new data-path work.
//
// Release schedule is PTS-locked: deadline = PTS + base + margin. base = windowed min transit
// (self-calibrating, no shared epoch needed). margin = playout delay budget sized from MEASURED
// arrival lateness (transit - base), per owner directive 2026-11-12 (receiver host stalls 20-140 ms
// at kernel/driver level; ride through, do not chase):
//   - grow fast: any MISS (frame arrives after its deadline) raises margin at once to cover the
//     observed stall (x preset grow factor + headroom), bounded by preset max.
//   - balanced/smooth start AT max and hold it for one full lateness window (no startup hitch,
//     target unknown until observed); low-latency starts at 1 frame.
//   - decay slow: one step (<= interval/4) per preset DecayStep toward target = lateness quantile
//     (balanced/smooth: max(quantile, worst recent stall × grow factor), worst stall forgotten at
//     1 ms per step) + headroom, floor 1 frame. Lowering margin by
//     <= interval/4 compresses one release interval instead of skipping a frame (an inter-coded
//     stream cannot drop a reference frame), i.e. "converge by at most one frame per N s".
//   - after a stall: frames that arrived in a burst release at cadence (margin grew to cover them);
//     beyond the max bound a min release spacing (interval/2) stops burst release; a run of frames is
//     skipped only when the head is stale past max margin + 2 intervals (sink wedged).
// p99.9 alone would let margin decay between stalls 30-120 s apart (a stall is ~8 late samples
// out of ~10k), so balanced/smooth also hold the window's peak lateness.
// Inter-coded streams never feed the decoder an undecodable run: after an unrecovered gap the
// buffer drops to the next keyframe and requests one over the control plane (PLI, §2.5).

const (
	jbGrowWindowNs = 2_000_000_000 // late-rate telemetry window
	jbWarmupNs     = 2_000_000_000 // §startup: never GROW in the first 2 s (encoder fill, first IDR,
	//                                   base settling make early frames transiently late)
	jbPLIMinGapNs    = 250_000_000   // PLI rate limit
	jbBaseBuckets    = 4             // min-transit window = 4 × 2 s buckets
	jbBaseBucketNs   = 2_000_000_000 // one transit-min bucket
	jbStaleGraceIntv = 2             // head is "stale" past deadline + max margin + this many intervals
	jbDefaultFPS     = 60.0          // frame interval fallback when the advert carried none
	jbHeadroomNs     = 2_000_000     // margin headroom over measured lateness (timer wake slop)
	jbHistBinNs      = 1_000_000     // lateness histogram bin (1 ms)
	jbHistSlices     = 10            // sliding window = 10 slices
	jbStatMinutes    = 10            // "last 10 min" telemetry window

	// Memory guards (pending-frame caps). INTRA streams (raw RGBA / MJPEG) have no decode
	// dependency, so deep buffering buys nothing - a couple of frames is plenty and keeps the
	// footprint tiny (raw 720p = ~3.7 MB/frame). INTER streams keep a larger frame cap (encoded
	// frames are small) but a sane byte ceiling. Policy: drop-head to next resync point.
	jbIntraCapFrames = 6
	jbIntraCapBytes  = 48 << 20
	jbInterCapFrames = 120
	jbInterCapBytes  = 64 << 20
)

// PlayoutPreset is a latency <-> smoothness trade-off for the playout margin (config
// mediaLink.playout).
type PlayoutPreset struct {
	Name        string
	MaxNs       int64   // margin bound
	Quantile    float64 // lateness quantile the decay target follows
	PeakHold    bool    // decay target also holds the window's peak lateness
	WindowNs    int64   // lateness window
	DecayStepNs int64   // min time between decay steps (and after a miss)
	GrowFactor  float64 // miss -> margin = observed lateness × factor + headroom
	StartAtMax  bool    // start at MaxNs and decay (no startup hitch) vs start at 1 frame
}

// Playout presets.
var (
	PlayoutLowLatency = PlayoutPreset{Name: "low-latency", MaxNs: 60e6, Quantile: 0.99,
		WindowNs: 120e9, DecayStepNs: 5e9, GrowFactor: 1}
	PlayoutBalanced = PlayoutPreset{Name: "balanced", MaxNs: 150e6, Quantile: 0.999, PeakHold: true,
		WindowNs: 300e9, DecayStepNs: 30e9, GrowFactor: 1.25, StartAtMax: true}
	PlayoutSmooth = PlayoutPreset{Name: "smooth", MaxNs: 250e6, Quantile: 0.9999, PeakHold: true,
		WindowNs: 600e9, DecayStepNs: 60e9, GrowFactor: 1.5, StartAtMax: true}
)

// ParsePlayoutPreset maps a config value to a preset ("" / unknown = balanced).
func ParsePlayoutPreset(s string) PlayoutPreset {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low-latency", "lowlatency", "low":
		return PlayoutLowLatency
	case "smooth":
		return PlayoutSmooth
	default:
		return PlayoutBalanced
	}
}

// JitterStats is one buffer's live telemetry (§7: playout delay, misses, drop proof).
type JitterStats struct {
	Depth       int     // margin in frames (ceil), legacy view
	Buffered    int     // frames pending release
	LateRate    float64 // late fraction over the current 2 s window
	Late        uint64  // total late frames (= misses)
	PolicyDrops uint64  // keyframe-policy + overflow drops (counted, logged by the route)
	Grows       uint64  // margin grow events
	Decays      uint64  // margin decay steps
	Dups        uint64  // duplicate / already-delivered arrivals (late retransmits)
	WaitingKey  bool    // holding for a keyframe after an unrecoverable gap
	PLIsSent    uint64

	Preset     string  // playout preset name
	PlayoutMs  float64 // current margin (playout delay over min transit)
	TargetMs   float64 // decay target from measured lateness
	MaxMs      float64 // preset bound
	Misses10m  uint64  // frames missing at their deadline, last 10 min
	Repeats10m uint64  // release intervals > 1.5 frames, counted in repeated frames, last 10 min
	Repeats    uint64  // total repeated frames
}

// MarginEvent is the last margin grow (for the "playout hitch" log line).
type MarginEvent struct {
	At             int64 // media clock ns (0 = none)
	FromNs, ToNs   int64
	ObservedLateNs int64
}

type jbSample struct {
	at   int64 // arrival ns (prune key)
	late bool
}

// minuteRing counts events over the last jbStatMinutes minutes (fixed array, bounded).
type minuteRing struct {
	min [jbStatMinutes]int64
	n   [jbStatMinutes]uint64
}

func (r *minuteRing) add(now int64, k uint64) {
	m := now / 60e9
	i := int(m % jbStatMinutes)
	if r.min[i] != m {
		r.min[i], r.n[i] = m, 0
	}
	r.n[i] += k
}

func (r *minuteRing) sum(now int64) uint64 {
	m := now / 60e9
	var s uint64
	for i := range r.min {
		if r.n[i] > 0 && m-r.min[i] < jbStatMinutes {
			s += r.n[i]
		}
	}
	return s
}

// latHist is a sliding-window lateness histogram: jbHistSlices slices of 1 ms bins up to the preset
// max (+ overflow bin) and a per-slice peak. Bounded: slices × bins counters.
type latHist struct {
	sliceNs int64
	cur     int64 // current slice id
	has     bool
	counts  [jbHistSlices][]uint32
	peak    [jbHistSlices]int64
	agg     []uint64
	total   uint64
}

func newLatHist(p PlayoutPreset) *latHist {
	bins := int(p.MaxNs/jbHistBinNs) + 2
	h := &latHist{sliceNs: p.WindowNs / jbHistSlices, agg: make([]uint64, bins)}
	if h.sliceNs <= 0 {
		h.sliceNs = 1
	}
	for i := range h.counts {
		h.counts[i] = make([]uint32, bins)
	}
	return h
}

func (h *latHist) rotate(now int64) {
	id := now / h.sliceNs
	if !h.has {
		h.cur, h.has = id, true
		return
	}
	for steps := 0; h.cur < id && steps < jbHistSlices; steps++ {
		h.cur++
		i := int(h.cur % jbHistSlices)
		for b, c := range h.counts[i] {
			h.agg[b] -= uint64(c)
			h.total -= uint64(c)
			h.counts[i][b] = 0
		}
		h.peak[i] = 0
	}
	if id > h.cur {
		h.cur = id // jumped past the whole window: every slice already cleared
	}
}

func (h *latHist) add(now, lat int64) {
	h.rotate(now)
	if lat < 0 {
		lat = 0
	}
	b := int(lat / jbHistBinNs)
	if b >= len(h.agg) {
		b = len(h.agg) - 1
	}
	i := int(h.cur % jbHistSlices)
	h.counts[i][b]++
	h.agg[b]++
	h.total++
	if lat > h.peak[i] {
		h.peak[i] = lat
	}
}

// quantile returns the q-quantile lateness (upper bin edge) and the window peak.
func (h *latHist) quantile(now int64, q float64) (qv, peak int64) {
	h.rotate(now)
	for _, p := range h.peak {
		if p > peak {
			peak = p
		}
	}
	if h.total == 0 {
		return 0, peak
	}
	need := uint64(q * float64(h.total))
	if need < 1 {
		need = 1
	}
	var acc uint64
	for b, c := range h.agg {
		acc += c
		if acc >= need {
			return int64(b+1) * jbHistBinNs, peak
		}
	}
	return int64(len(h.agg)) * jbHistBinNs, peak
}

// jitterBuffer is the per-route buffer. push comes from the route recv loop, pop from the release
// runner; the mutex covers both.
type jitterBuffer struct {
	mu         sync.Mutex
	interval   int64 // frame interval ns
	intra      bool  // every frame independently decodable (raw pixels / MJPEG)
	preset     PlayoutPreset
	margin     int64 // playout delay over base, ns
	minSpacing int64 // min release spacing (anti-burst); 0 disables
	capFrames  int   // pending-frame hard cap (intra: tiny; inter: larger)
	capBytes   int   // pending-payload hard cap
	warmupNs   int64 // no-grow window after the first frame

	started      bool
	startAt      int64 // first-frame arrival (warmup gate)
	nextSeq      uint32
	pending      []*Frame // seq-ascending
	pendingBytes int

	// transit-min window (base): current bucket + previous mins.
	bucketStart int64
	bucketMin   int64
	bucketHas   bool
	prevMins    []int64

	hist       *latHist
	longPeak   int64 // worst lateness (slow forgetting, see longPeakAt)
	longPeakT  int64
	samples    []jbSample // 2 s late-rate telemetry window
	lastChange int64      // last margin grow/decay (decay pacing)
	lastMiss   int64
	lastGrow   MarginEvent
	lastRel    int64 // last release time (media clock)
	lastPLI    int64
	waitKey    bool

	misses10m, repeats10m minuteRing

	late        uint64
	repeats     uint64
	policyDrops uint64
	grows       uint64
	decays      uint64
	dups        uint64
	plis        uint64

	pli    func()        // rate-limited keyframe request (set once before use)
	notify chan struct{} // cap-1 kick for the release runner
}

// newJitterBuffer builds a balanced-preset buffer for one video stream.
func newJitterBuffer(fps float64, intra bool) *jitterBuffer {
	return newJitterBufferPreset(fps, intra, PlayoutBalanced)
}

// newJitterBufferPreset builds a buffer for one video stream. fps sizes the frame interval
// (0 = 60); intra marks codecs where any frame resyncs the decoder.
func newJitterBufferPreset(fps float64, intra bool, p PlayoutPreset) *jitterBuffer {
	if fps <= 0 {
		fps = jbDefaultFPS
	}
	capFrames, capBytes := jbInterCapFrames, jbInterCapBytes
	if intra {
		capFrames, capBytes = jbIntraCapFrames, jbIntraCapBytes
	}
	iv := int64(1e9 / fps)
	if p.MaxNs < iv {
		p.MaxNs = iv
	}
	b := &jitterBuffer{
		interval:   iv,
		intra:      intra,
		preset:     p,
		margin:     iv,
		minSpacing: iv / 2,
		capFrames:  capFrames,
		capBytes:   capBytes,
		warmupNs:   jbWarmupNs,
		hist:       newLatHist(p),
		notify:     make(chan struct{}, 1),
	}
	if p.StartAtMax {
		b.margin = p.MaxNs
	}
	return b
}

// deadline is a frame's release time on the media clock. Caller holds mu.
func (b *jitterBuffer) deadline(f *Frame) int64 {
	return f.PTS + b.base() + b.margin
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
		b.startAt = arrival
		b.nextSeq = f.Seq
		b.lastChange = arrival
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

// accountLateness records the lateness sample, grows on a miss, decays slowly. Caller holds mu.
func (b *jitterBuffer) accountLateness(f *Frame, arrival int64) {
	lateness := arrival - f.PTS - b.base() // >= 0: base is the min transit incl. this sample
	lateBy := arrival - b.deadline(f)
	miss := lateBy > 0
	warm := arrival-b.startAt < b.warmupNs

	b.samples = append(b.samples, jbSample{at: arrival, late: miss})
	cut := 0
	for cut < len(b.samples) && arrival-b.samples[cut].at > jbGrowWindowNs {
		cut++
	}
	b.samples = b.samples[cut:]

	if warm {
		return // startup transients: no histogram, no grow, no miss count
	}
	b.hist.add(arrival, lateness)
	if lateness > b.longPeakAt(arrival) {
		b.longPeak, b.longPeakT = lateness, arrival
	}
	if miss {
		b.late++
		b.misses10m.add(arrival, 1)
		b.lastMiss = arrival
		want := int64(float64(lateness)*b.preset.GrowFactor) + jbHeadroomNs
		if want > b.preset.MaxNs {
			want = b.preset.MaxNs
		}
		if want > b.margin {
			b.lastGrow = MarginEvent{At: arrival, FromNs: b.margin, ToNs: want, ObservedLateNs: lateness}
			b.margin = want
			b.grows++
			b.lastChange = arrival
		}
		return
	}
	if arrival-b.lastChange < b.preset.DecayStepNs || arrival-b.lastMiss < b.preset.DecayStepNs {
		return
	}
	if b.preset.StartAtMax && arrival-b.startAt < b.preset.WindowNs {
		return // hold the safe start until one full lateness window has been observed
	}
	b.lastChange = arrival // re-evaluate once per step even when on target
	if t := b.targetLocked(arrival); b.margin > t {
		step := b.margin - t
		if lim := b.interval / 4; step > lim {
			step = lim
		}
		b.margin -= step
		b.decays++
	}
}

// targetLocked is the decay target: lateness quantile (peak-hold presets: worst recent stall ×
// grow factor, so a somewhat larger next stall is still covered) + headroom, in [1 frame, max].
func (b *jitterBuffer) targetLocked(now int64) int64 {
	q, peak := b.hist.quantile(now, b.preset.Quantile)
	if b.preset.PeakHold {
		q = max(q, int64(float64(max(peak, b.longPeakAt(now)))*b.preset.GrowFactor))
	}
	t := q + jbHeadroomNs
	if t < b.interval {
		t = b.interval
	}
	if t > b.preset.MaxNs {
		t = b.preset.MaxNs
	}
	return t
}

// longPeakAt is the worst lateness seen, forgetting 1 ms per DecayStep: the host's stall size is
// remembered across windows (a 5-min window can hold only small stalls by chance). Caller holds mu.
func (b *jitterBuffer) longPeakAt(now int64) int64 {
	if b.preset.DecayStepNs <= 0 {
		return 0
	}
	v := b.longPeak - (now-b.longPeakT)/b.preset.DecayStepNs*jbHistBinNs
	return max(v, 0)
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
		// Catch-up: head stale beyond the margin bound (sink wedged) and a newer resync point is
		// buffered → skip forward (latency beats completeness on a live stream).
		if now > b.deadline(h)+b.preset.MaxNs+jbStaleGraceIntv*b.interval {
			if ki := b.nextResyncIdx(); ki > 0 {
				for j := 0; j < ki; j++ {
					b.dropHead()
				}
				continue
			}
		}
		gap := h.Seq != b.nextSeq
		if gap && !b.intra && !h.Keyframe() {
			// Missing dependency: it may still arrive (retransmit of a big IDR fragment) - wait for it up
			// to the max margin (a miss = repeated frames, then the margin grows on its arrival), only
			// then give up + PLI. Never request an IDR while the frame can still arrive within the bound.
			if dl := b.deadline(h) + max(b.preset.MaxNs-b.margin, 0); now < dl {
				return nil, dl, fire
			}
			b.dropHead()
			fire = b.enterWaitKey(now) || fire
			continue
		}
		if dl := b.deadline(h); now < dl {
			return nil, dl, fire
		}
		// Anti-burst: hold cadence after a stall beyond the bound (compressed catch-up).
		if b.minSpacing > 0 && b.lastRel != 0 && now-b.lastRel < b.minSpacing {
			return nil, b.lastRel + b.minSpacing, fire
		}
		b.pending[0] = nil
		b.pending = b.pending[1:]
		b.pendingBytes -= len(h.Payload)
		b.nextSeq = h.Seq + 1
		if b.lastRel != 0 {
			if gap := now - b.lastRel; gap*2 > 3*b.interval {
				k := uint64((gap+b.interval/2)/b.interval) - 1
				b.repeats += k
				b.repeats10m.add(now, k)
			}
		}
		b.lastRel = now
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

// marginInfo returns the current margin + last grow event (hitch log).
func (b *jitterBuffer) marginInfo() (int64, MarginEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.margin, b.lastGrow
}

// stats snapshots the buffer counters (§7). now = media clock (windowed counters).
func (b *jitterBuffer) stats() JitterStats {
	return b.statsAt(b.lastSeen())
}

func (b *jitterBuffer) lastSeen() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	t := b.lastRel
	if n := len(b.samples); n > 0 && b.samples[n-1].at > t {
		t = b.samples[n-1].at
	}
	return t
}

func (b *jitterBuffer) statsAt(now int64) JitterStats {
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
		Depth: int((b.margin + b.interval - 1) / b.interval), Buffered: len(b.pending), LateRate: rate,
		Late: b.late, PolicyDrops: b.policyDrops, Grows: b.grows, Decays: b.decays, Dups: b.dups,
		WaitingKey: b.waitKey, PLIsSent: b.plis,
		Preset: b.preset.Name, PlayoutMs: float64(b.margin) / 1e6,
		TargetMs: float64(b.targetLocked(now)) / 1e6, MaxMs: float64(b.preset.MaxNs) / 1e6,
		Misses10m: b.misses10m.sum(now), Repeats10m: b.repeats10m.sum(now), Repeats: b.repeats,
	}
}
