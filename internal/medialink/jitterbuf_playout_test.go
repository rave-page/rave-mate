package medialink

import (
	"math/rand"
	"sort"
	"testing"
)

// Playout-margin tests replay synthetic arrival traces through the jitter buffer with an
// event-driven clock (release at the exact wake the buffer asks for) and measure release
// intervals - the receiver's visible cadence.

type simArrival struct {
	at int64
	f  *Frame
}

type simStall struct{ at, dur int64 }

// simProfile describes a synthetic link/receiver.
type simProfile struct {
	fps       float64
	durNs     int64
	transitNs int64   // nominal one-way transit
	jitterNs  int64   // uniform arrival jitter [0, jitterNs)
	lossP     float64 // frame recovered by retransmit (+retxNs)
	retxNs    int64
	reorderP  float64 // swap with the previous frame's arrival
	stalls    []simStall
	keyEvery  int
}

// rigStalls draws receiver arrival stalls: 20-140 ms at 0.5-2/min (gap 30-120 s).
func rigStalls(rng *rand.Rand, durNs int64) []simStall {
	var out []simStall
	t := int64(5e9) + rng.Int63n(25e9)
	for t < durNs {
		out = append(out, simStall{at: t, dur: 20e6 + rng.Int63n(120e6)})
		t += 30e9 + rng.Int63n(90e9)
	}
	return out
}

func buildTrace(p simProfile, rng *rand.Rand) []simArrival {
	iv := int64(1e9 / p.fps)
	n := int(p.durNs / iv)
	arr := make([]simArrival, 0, n)
	for i := 0; i < n; i++ {
		f := &Frame{Kind: KindVideo, Codec: CodecHEVC, Seq: uint32(i), PTS: int64(i) * iv}
		if p.keyEvery > 0 && i%p.keyEvery == 0 {
			f.Flags = FlagKeyframe
		}
		a := f.PTS + p.transitNs
		if p.jitterNs > 0 {
			a += rng.Int63n(p.jitterNs)
		}
		if p.lossP > 0 && rng.Float64() < p.lossP {
			a += p.retxNs
		}
		arr = append(arr, simArrival{at: a, f: f})
	}
	for i := 1; i < len(arr); i++ {
		if p.reorderP > 0 && rng.Float64() < p.reorderP {
			arr[i-1].at = arr[i].at + 100_000 // previous frame overtaken (never earlier than sent)
		}
	}
	// Receiver stall: everything that would arrive inside [at, at+dur) lands at the stall end.
	for _, s := range p.stalls {
		k := int64(0)
		for i := range arr {
			if arr[i].at >= s.at && arr[i].at < s.at+s.dur {
				arr[i].at = s.at + s.dur + k*50_000
				k++
			}
		}
	}
	sort.SliceStable(arr, func(i, j int) bool { return arr[i].at < arr[j].at })
	return arr
}

type simResult struct {
	rel       []int64 // release times
	maxGapNs  int64   // max release interval after the convergence window
	badGaps   int     // release intervals > 1.5 frames after convergence
	maxMargin int64
	added     []int64 // release - PTS - transit (added playout latency)
}

func runSim(b *jitterBuffer, arr []simArrival, transit, convergeNs int64) simResult {
	var r simResult
	now, idx := int64(0), 0
	for {
		for idx < len(arr) && arr[idx].at <= now {
			b.push(arr[idx].f, arr[idx].at)
			idx++
		}
		if b.margin > r.maxMargin {
			r.maxMargin = b.margin
		}
		f, wake := b.pop(now)
		if f != nil {
			if n := len(r.rel); n > 0 && now >= convergeNs {
				g := now - r.rel[n-1]
				if g > r.maxGapNs {
					r.maxGapNs = g
				}
				if g*2 > 3*b.interval {
					r.badGaps++
				}
			}
			r.rel = append(r.rel, now)
			r.added = append(r.added, now-f.PTS-transit)
			continue
		}
		next := int64(-1)
		if idx < len(arr) {
			next = arr[idx].at
		}
		if wake != 0 && (next < 0 || wake < next) {
			next = wake
		}
		if next < 0 {
			return r
		}
		if next <= now {
			next = now + 1
		}
		now = next
	}
}

func pct(xs []int64, q float64) int64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]int64(nil), xs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(q*float64(len(s)-1))]
}

// TestPlayoutRigProfile: this rig (arrival stalls 20-140 ms at 0.5-2/min, 0.03 % retransmit-
// recovered loss, small jitter, mild reorder) on 60 fps for 10 min, balanced preset: after 10 s
// zero release intervals > 1.5 frames, margin within the balanced bound. Several seeds.
func TestPlayoutRigProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("10-min synthetic traces")
	}
	for seed := int64(1); seed <= 12; seed++ {
		rng := rand.New(rand.NewSource(seed))
		dur := int64(600e9)
		p := simProfile{fps: 60, durNs: dur, transitNs: 3e6, jitterNs: 2e6, lossP: 0.0003, retxNs: 6e6,
			reorderP: 0.001, keyEvery: 120, stalls: rigStalls(rng, dur)}
		arr := buildTrace(p, rng)
		b := newJitterBuffer(60, false)
		r := runSim(b, arr, p.transitNs, 10e9)
		s := b.statsAt(dur)
		t.Logf("seed %d: stalls=%d releases=%d badGaps=%d maxGap=%.1fms maxMargin=%.1fms final=%.1fms target=%.1fms misses=%d repeats=%d added p50/p99=%.1f/%.1fms",
			seed, len(p.stalls), len(r.rel), r.badGaps, float64(r.maxGapNs)/1e6, float64(r.maxMargin)/1e6,
			s.PlayoutMs, s.TargetMs, s.Late, s.Repeats, float64(pct(r.added, 0.5))/1e6, float64(pct(r.added, 0.99))/1e6)
		if len(r.rel) != len(arr) || s.PolicyDrops != 0 {
			t.Fatalf("seed %d: released %d/%d drops=%d", seed, len(r.rel), len(arr), s.PolicyDrops)
		}
		if r.badGaps != 0 {
			t.Fatalf("seed %d: %d release intervals > 1.5 frames after convergence (max %.1f ms)",
				seed, r.badGaps, float64(r.maxGapNs)/1e6)
		}
		if r.maxMargin > PlayoutBalanced.MaxNs {
			t.Fatalf("seed %d: margin %d over bound", seed, r.maxMargin)
		}
	}
}

// TestPlayoutCalmDecaysToOneFrame: a calm link decays the balanced start margin back to 1 frame.
func TestPlayoutCalmDecaysToOneFrame(t *testing.T) {
	if testing.Short() {
		t.Skip("30-min synthetic trace")
	}
	rng := rand.New(rand.NewSource(7))
	p := simProfile{fps: 60, durNs: 1800e9, transitNs: 3e6, jitterNs: 1e6, keyEvery: 120}
	b := newJitterBuffer(60, false)
	r := runSim(b, buildTrace(p, rng), p.transitNs, 0)
	s := b.stats()
	if s.Depth != 1 || s.Late != 0 || r.badGaps != 0 {
		t.Fatalf("calm link: %+v badGaps=%d", s, r.badGaps)
	}
	t.Logf("calm: final %.1f ms after %d decays", s.PlayoutMs, s.Decays)
}

// TestPlayoutPresetsRespected: a 400 ms stall never pushes margin over the preset bound, every
// frame is still delivered, and the post-stall backlog never releases closer than interval/2.
func TestPlayoutPresetsRespected(t *testing.T) {
	for _, pr := range []PlayoutPreset{PlayoutLowLatency, PlayoutBalanced, PlayoutSmooth} {
		rng := rand.New(rand.NewSource(3))
		p := simProfile{fps: 60, durNs: 30e9, transitNs: 3e6, keyEvery: 120,
			stalls: []simStall{{at: 10e9, dur: 30e6}, {at: 20e9, dur: 400e6}}}
		arr := buildTrace(p, rng)
		b := newJitterBufferPreset(60, false, pr)
		r := runSim(b, arr, p.transitNs, 0)
		if r.maxMargin > pr.MaxNs {
			t.Fatalf("%s: margin %d > max %d", pr.Name, r.maxMargin, pr.MaxNs)
		}
		if len(r.rel) != len(arr) {
			t.Fatalf("%s: released %d/%d", pr.Name, len(r.rel), len(arr))
		}
		for i := 1; i < len(r.rel); i++ {
			if r.rel[i]-r.rel[i-1] < b.interval/2 {
				t.Fatalf("%s: burst release %d ns at %d", pr.Name, r.rel[i]-r.rel[i-1], i)
			}
		}
		s := b.stats()
		if pr.StartAtMax && s.Grows != 0 {
			t.Fatalf("%s: started at max, must not grow: %+v", pr.Name, s)
		}
		if !pr.StartAtMax && s.Grows == 0 {
			t.Fatalf("%s: stall must grow the margin: %+v", pr.Name, s)
		}
		t.Logf("%s: maxMargin %.0f ms grows=%d misses=%d repeats=%d", pr.Name, float64(r.maxMargin)/1e6, s.Grows, s.Late, s.Repeats)
	}
}

// TestPlayoutLowLatencyConverges: low-latency starts at 1 frame, a first 40 ms stall costs one
// hitch, the same stall again is absorbed.
func TestPlayoutLowLatencyConverges(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	p := simProfile{fps: 60, durNs: 20e9, transitNs: 3e6, keyEvery: 120,
		stalls: []simStall{{at: 5e9, dur: 40e6}, {at: 8e9, dur: 40e6}}}
	b := newJitterBufferPreset(60, false, PlayoutLowLatency)
	r := runSim(b, buildTrace(p, rng), p.transitNs, 6e9)
	if r.badGaps != 0 {
		t.Fatalf("second identical stall not absorbed: %d bad gaps", r.badGaps)
	}
	if s := b.stats(); s.Grows != 1 || s.Misses10m == 0 || s.Repeats == 0 {
		t.Fatalf("first stall must grow once + count the hitch: %+v", s)
	}
}

func TestParsePlayoutPreset(t *testing.T) {
	for in, want := range map[string]string{"": "balanced", "junk": "balanced", "Smooth": "smooth",
		"low-latency": "low-latency", " balanced ": "balanced"} {
		if got := ParsePlayoutPreset(in).Name; got != want {
			t.Fatalf("%q -> %q, want %q", in, got, want)
		}
	}
}

// stepTrace applies a persistent transit step of stepNs from atNs on (clock / PTS-domain step).
func stepTrace(arr []simArrival, atNs, stepNs int64) {
	for i := range arr {
		if arr[i].at >= atNs {
			arr[i].at += stepNs
		}
	}
	sort.SliceStable(arr, func(i, j int) bool { return arr[i].at < arr[j].at })
}

// TestPlayoutClockStepRebases: a persistent +300 ms transit step (receiver clock / sender PTS-domain
// step, live symptom: 355 misses in the first seconds at max margin) is re-anchored within ~1 s; the
// run's misses are un-counted and the margin returns to its pre-step value. A step under the margin
// re-anchors too (no silent margin consumption).
func TestPlayoutClockStepRebases(t *testing.T) {
	for _, step := range []int64{300e6, 80e6} {
		rng := rand.New(rand.NewSource(9))
		p := simProfile{fps: 60, durNs: 30e9, transitNs: 3e6, jitterNs: 1e6, keyEvery: 120}
		arr := buildTrace(p, rng)
		stepTrace(arr, 10e9, step)
		b := newJitterBuffer(60, false)
		r := runSim(b, arr, p.transitNs, 0)
		s := b.statsAt(30e9)
		t.Logf("step %d ms: rebases=%d misses10m=%d late=%d playout=%.1f target=%.1f repeats=%d", step/1e6, s.Rebases, s.Misses10m, s.Late, s.PlayoutMs, s.TargetMs, s.Repeats)
		if s.Rebases != 1 || s.Misses10m != 0 || s.Late != 0 {
			t.Fatalf("step %d: %+v", step, s)
		}
		if s.PlayoutMs != PlayoutBalanced.MaxMs() || len(r.rel) != len(arr) || s.PolicyDrops != 0 {
			t.Fatalf("step %d: margin/delivery after rebase: %+v released=%d/%d", step, s, len(r.rel), len(arr))
		}
	}
}

// TestPlayoutStartupSettlingNotCounted: the first second arrives with a much lower transit than the
// steady state (encoder fill / first IDR / base settling); none of it may count as misses or repeats
// and the margin must not be driven by it.
func TestPlayoutStartupSettlingNotCounted(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	p := simProfile{fps: 60, durNs: 20e9, transitNs: 3e6, jitterNs: 1e6, keyEvery: 120}
	arr := buildTrace(p, rng)
	stepTrace(arr, 1e9, 200e6) // steady state 200 ms above the first second's transit
	b := newJitterBuffer(60, false)
	runSim(b, arr, p.transitNs, 0)
	s := b.statsAt(20e9)
	if s.Misses10m != 0 || s.PlayoutMs != PlayoutBalanced.MaxMs() || s.Rebases != 1 {
		t.Fatalf("startup settling counted: %+v", s)
	}
}
