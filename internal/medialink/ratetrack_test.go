package medialink

// The reported bitrate must track the route's OWN cumulative bytes. It is the number that means
// "black frames" to an operator (0.1 Mbps), so a healthy route displaying it is worse than a
// frozen counter. This drives a real paced route over loopback for several seconds and compares
// the DISPLAYED rate against cumulative-bytes/elapsed measured from the same snapshots.

import (
	"context"
	"testing"
	"time"
)

// pacedSource emits a fixed-size frame every interval until ctx dies - a live capture, not a
// pre-filled slice: burstiness of the arrival pattern is what a rate window has to survive.
type pacedSource struct {
	every time.Duration
	size  int
	// keyEvery: every Nth frame is `keyMul` times larger, so the window sees the same
	// I-frame/P-frame spikes a real encoder produces.
	keyEvery int
	keyMul   int
	n        int
	t        *time.Ticker
}

func (s *pacedSource) Next(ctx context.Context) (*Frame, error) {
	if s.t == nil {
		s.t = time.NewTicker(s.every)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.t.C:
	}
	sz := s.size
	if s.keyEvery > 0 && s.n%s.keyEvery == 0 {
		sz *= s.keyMul
	}
	s.n++
	return &Frame{Kind: KindVideo, Codec: CodecNRGBA, Payload: make([]byte, sz)}, nil
}

func (s *pacedSource) Close() error {
	if s.t != nil {
		s.t.Stop()
	}
	return nil
}

// clumpSource is the FIELD SHAPE, and the shape that breaks a short window: frames arrive at a
// CONSTANT cadence (so a frame-driven window keeps closing on schedule) while the BYTES are
// wildly uneven - a trickle of small frames most of the time plus one large clump every clumpEvery.
// A 1 s window then closes on the trickle in most seconds and reports ~1/8 of the mean; only the
// second that happens to contain a clump overshoots. That is exactly the field signature: 0.1 Mbps
// five readings out of six on a route whose real mean was ~0.9 Mbps, with one 2.3 Mbps outlier.
//
// A source that also PAUSES between bursts does NOT reproduce it: the window closes on the first
// frame of the next burst and therefore always spans exactly one burst. Frames must keep coming.
type clumpSource struct {
	every      time.Duration // frame cadence (constant)
	small      int           // trickle payload
	clumpEvery time.Duration // one large payload this often
	clump      int
	t          *time.Ticker
	last       time.Duration
	elapsed    time.Duration
}

func (s *clumpSource) Next(ctx context.Context) (*Frame, error) {
	if s.t == nil {
		s.t = time.NewTicker(s.every)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.t.C:
	}
	s.elapsed += s.every
	size := s.small
	if s.elapsed-s.last >= s.clumpEvery {
		s.last = s.elapsed
		size = s.clump
	}
	return &Frame{Kind: KindAudio, Codec: CodecPCMS16, Payload: make([]byte, size)}, nil
}

func (s *clumpSource) Close() error {
	if s.t != nil {
		s.t.Stop()
	}
	return nil
}

type drainSink struct{}

func (drainSink) Write(*Frame) error { return nil }
func (drainSink) Close() error       { return nil }

// TestReportedRateTracksCumulativeBytes samples a live receive route the way an operator does -
// every 500 ms over several seconds - and requires the displayed RateBps to agree with the
// cumulative-byte slope measured across the SAME samples. A window whose numerator and divisor
// come from different spans, or one too short to survive the arrival pattern, shows up here.
//
// The BURSTY arm is the field reproduction: on a 1 s window it reported a fraction of truth in
// most readings with occasional overshoots - the "0.1 Mbps five times in six" that reads as black
// frames on a perfectly healthy route.
func TestReportedRateTracksCumulativeBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("multi-second live route")
	}
	// ~0.9 Mbps mean in both arms: steady 38 fps with periodic keyframe spikes, and a constant
	// frame cadence whose BYTES clump (the field shape - see clumpSource).
	t.Run("paced", func(t *testing.T) {
		runRateProbe(t, &pacedSource{every: time.Second / 38, size: 2600, keyEvery: 114, keyMul: 10})
	})
	t.Run("clumped", func(t *testing.T) {
		// 40 fps × 300 B trickle (≈96 kbps, the field's "0.1 Mbps") + a 120 kB clump every 1.3 s
		// ⇒ ~0.83 Mbps mean. The trickle alone is what a 1 s window reports most seconds.
		runRateProbe(t, &clumpSource{every: 25 * time.Millisecond, small: 300,
			clumpEvery: 1300 * time.Millisecond, clump: 120_000})
	})
}

func runRateProbe(t *testing.T, src Source) {
	t.Helper()
	h := &hub{}
	secrets := fakeSecrets{key: testKey()}
	clk := NewMonotonicClock()
	rmB := New(Options{Self: "B", Bus: &busView{h, "B"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1", Clock: clk})
	rmA := New(Options{Self: "A", Bus: &busView{h, "A"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1", Clock: clk})

	// KindAudio, not KindVideo: raw big-video routes are refused by design (no encoder here), and
	// the counting path (recvMedia → count) is byte-identical for both kinds.
	rmB.RegisterSource(SourceDesc{ID: "cam", Kind: KindAudio, Codec: CodecPCMS16, FPS: 38},
		func(context.Context, Offer) (Source, error) { return src, nil })
	rmA.RegisterSink(SinkDesc{ID: "out", Kind: KindAudio},
		func(context.Context, Answer) (Sink, error) { return drainSink{}, nil })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rmB.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer rmB.Stop()
	if err := rmA.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer rmA.Stop()
	rmB.Advertise()
	waitFor(t, 2*time.Second, func() bool {
		adv, ok := rmA.RemoteAdverts()["B"]
		return ok && len(adv.Sources) == 1
	})
	if _, err := rmA.Offer("B", "cam", "out", CodecPCMS16); err != nil {
		t.Fatal(err)
	}

	recv := func() (RouteStat, bool) {
		for _, s := range rmA.Stats() {
			if s.Direction == "recv" {
				return s, true
			}
		}
		return RouteStat{}, false
	}
	waitFor(t, 3*time.Second, func() bool { s, ok := recv(); return ok && s.Frames > 20 })

	// Warm-up: the sliding window must be FULLY populated before sampling, else the first
	// readings measure a partial span and the tolerance below would have to be loosened to
	// uselessness.
	time.Sleep(rateSpan + time.Second)

	type sample struct {
		at    time.Time
		bytes uint64
		bps   float64
	}
	var ss []sample
	// 20 × 500 ms = 10 s. Long enough that a burst clipped at either endpoint biases the
	// cumulative-slope reference by ~13%, not by a factor.
	for i := 0; i < 20; i++ {
		s, ok := recv()
		if !ok {
			t.Fatal("receive route vanished mid-run")
		}
		ss = append(ss, sample{time.Now(), s.Bytes, s.RateBps})
		time.Sleep(500 * time.Millisecond)
	}

	first, last := ss[0], ss[len(ss)-1]
	truth := float64(last.bytes-first.bytes) * 8 / last.at.Sub(first.at).Seconds()
	if truth <= 0 {
		t.Fatalf("no traffic on the route: %d → %d bytes", first.bytes, last.bytes)
	}
	var sum float64
	low := 0
	for _, s := range ss {
		sum += s.bps
		if s.bps < truth/2 {
			low++
		}
		t.Logf("bytes=%-9d displayed=%.0f bps (truth %.0f)", s.bytes, s.bps, truth)
	}
	mean := sum / float64(len(ss))
	if r := mean / truth; r < 0.8 || r > 1.25 {
		t.Fatalf("displayed rate averages %.0f bps against a cumulative slope of %.0f bps (ratio %.2f)",
			mean, truth, r)
	}
	// The field failure was not a bad average - it was individual readings at ~1/8 of truth
	// ("0.1 Mbps", the number that means black frames) with occasional overshoots. A good average
	// over bad readings is still a lying panel, so NO reading may halve the truth.
	if low > 0 {
		t.Fatalf("%d/%d readings below half the true rate (%.0f bps): a healthy route displayed "+
			"the number an operator reads as broken", low, len(ss), truth)
	}
}
