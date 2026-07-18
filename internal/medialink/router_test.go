package medialink

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"
)

// ── fake control plane + secrets ─────────────────────────────────────────────

// hub is a shared in-memory eventbus fan-out for the two RouteManagers under test.
type hub struct {
	mu   sync.Mutex
	subs []hubSub
}
type hubSub struct {
	self, topic string
	fn          func(Event)
	dead        bool
}

func (h *hub) publish(fromSelf, topic string, data json.RawMessage) {
	h.mu.Lock()
	subs := append([]hubSub(nil), h.subs...)
	h.mu.Unlock()
	for _, s := range subs {
		if s.dead || s.topic != topic {
			continue
		}
		s.fn(Event{Origin: fromSelf, Local: s.self == fromSelf, Data: data})
	}
}

type busView struct {
	h    *hub
	self string
}

func (b *busView) Publish(topic string, data json.RawMessage) { b.h.publish(b.self, topic, data) }
func (b *busView) Subscribe(topic string, fn func(Event)) func() {
	b.h.mu.Lock()
	idx := len(b.h.subs)
	b.h.subs = append(b.h.subs, hubSub{self: b.self, topic: topic, fn: fn})
	b.h.mu.Unlock()
	return func() { b.h.mu.Lock(); b.h.subs[idx].dead = true; b.h.mu.Unlock() }
}

// fakeSecrets returns a fixed media master for any peer - both ends derive matching AEAD keys
// (the initiator flag splits directions), mirroring peerlink's symmetric MediaSecret.
type fakeSecrets struct{ key []byte }

func (s fakeSecrets) MediaSecret(string) ([]byte, bool) { return s.key, true }

// ── test source / sink ───────────────────────────────────────────────────────

type sliceSource struct {
	frames []*Frame
	i      int
}

func (s *sliceSource) Next(ctx context.Context) (*Frame, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.i >= len(s.frames) {
		<-ctx.Done() // frames drained: idle like a live source until the route is torn down
		return nil, ctx.Err()
	}
	f := s.frames[s.i]
	s.i++
	return f, nil
}
func (s *sliceSource) Close() error { return nil }

type collectSink struct {
	mu   sync.Mutex
	got  []*Frame
	want int
	done chan struct{}
}

func newCollectSink(want int) *collectSink {
	return &collectSink{want: want, done: make(chan struct{})}
}
func (c *collectSink) Write(f *Frame) error {
	c.mu.Lock()
	c.got = append(c.got, f)
	if len(c.got) == c.want {
		close(c.done)
	}
	c.mu.Unlock()
	return nil
}
func (c *collectSink) Close() error { return nil }

// TestRouteLoopback drives a full P1 negotiation over real loopback TCP: B advertises an audio
// source, A offers to receive it into a sink, A dials B's media listener, and B streams frames.
func TestRouteLoopback(t *testing.T) {
	h := &hub{}
	secrets := fakeSecrets{key: testKey()}
	rmB := New(Options{Self: "B", Bus: &busView{h, "B"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1"})
	rmA := New(Options{Self: "A", Bus: &busView{h, "A"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1"})

	const n = 32
	want := make([]*Frame, n)
	for i := range want {
		pts := int64(0)
		if i > 0 {
			pts = int64(i) * 5_000_000 // 5 ms audio framing; PTS preserved when the source sets it
		}
		want[i] = &Frame{Kind: KindAudio, Codec: CodecPCMS16, PTS: pts, Payload: []byte{byte(i), 0xAA, byte(i * 3)}}
	}
	src := &sliceSource{frames: want}
	rmB.RegisterSource(SourceDesc{ID: "mic", Name: "Mic", Kind: KindAudio, Codec: CodecPCMS16, Sample: 48000, Channels: 2},
		func(context.Context, Offer) (Source, error) { return src, nil })

	sink := newCollectSink(n)
	rmA.RegisterSink(SinkDesc{ID: "spk", Name: "Speakers", Kind: KindAudio},
		func(context.Context, Answer) (Sink, error) { return sink, nil })

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

	// B re-advertises now that A is subscribed (the app re-advertises on peer connect).
	rmB.Advertise()
	waitFor(t, time.Second, func() bool { _, ok := rmA.RemoteAdverts()["B"]; return ok })

	if _, err := rmA.Offer("B", "mic", "spk", CodecPCMS16); err != nil {
		t.Fatal(err)
	}

	select {
	case <-sink.done:
	case <-time.After(3 * time.Second):
		sink.mu.Lock()
		t.Fatalf("timeout: got %d/%d frames", len(sink.got), n)
	}

	sink.mu.Lock()
	got := sink.got
	sink.mu.Unlock()
	for i := range want {
		if got[i].Stream == 0 {
			t.Fatalf("frame %d: stream id not assigned", i)
		}
		if got[i].Seq != uint32(i) {
			t.Fatalf("frame %d: seq = %d, want %d", i, got[i].Seq, i)
		}
		frameEq(t, want[i], got[i])
	}
	// PTS==0 source frame gets stamped from the media clock (frame 0).
	if got[0].PTS <= 0 {
		t.Fatalf("frame 0 PTS not stamped: %d", got[0].PTS)
	}
	// All frames share one negotiated stream id.
	if got[0].Stream != got[n-1].Stream {
		t.Fatalf("stream id drifted: %d vs %d", got[0].Stream, got[n-1].Stream)
	}

	// Telemetry: A's recv route saw all frames, zero gaps.
	waitFor(t, time.Second, func() bool {
		for _, s := range rmA.Stats() {
			if s.Direction == "recv" && s.Frames == n && s.SeqGaps == 0 {
				return true
			}
		}
		return false
	})
}

// TestRouteReportsFlow: with P2 caps negotiated, RFC 3550-style reports flow both ways - the
// receiver sees the sender's SR (packet/octet counts) and the sender sees the receiver's RR
// (zero loss, jitter), and jitter/latency telemetry populates (§7).
func TestRouteReportsFlow(t *testing.T) {
	h := &hub{}
	secrets := fakeSecrets{key: testKey()}
	clk := NewMonotonicClock() // shared clock: e2e latency is meaningful
	rmB := New(Options{Self: "B", Bus: &busView{h, "B"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1", Clock: clk})
	rmA := New(Options{Self: "A", Bus: &busView{h, "A"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1", Clock: clk})
	rmA.reportEvery = 20 * time.Millisecond
	rmB.reportEvery = 20 * time.Millisecond

	const n = 16
	frames := make([]*Frame, n)
	for i := range frames {
		// PTS pre-stamped before the clock epoch: arrival (≥0 on the shared clock) is provably
		// later → e2e latency > 0 deterministically, even at coarse monotonic granularity.
		frames[i] = &Frame{Kind: KindAudio, Codec: CodecPCMS16, PTS: int64(i+1) - 1_000_000, Payload: []byte{byte(i)}}
	}
	rmB.RegisterSource(SourceDesc{ID: "mic", Kind: KindAudio, Codec: CodecPCMS16},
		func(context.Context, Offer) (Source, error) { return &sliceSource{frames: frames}, nil })
	sink := newCollectSink(n)
	rmA.RegisterSink(SinkDesc{ID: "spk", Kind: KindAudio},
		func(context.Context, Answer) (Sink, error) { return sink, nil })

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
	if _, err := rmA.Offer("B", "mic", "spk", CodecPCMS16); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sink.done:
	case <-time.After(3 * time.Second):
		t.Fatal("frames never arrived")
	}

	// Receiver (A) gets the sender's SR; sender (B) gets the receiver's RR.
	waitFor(t, 2*time.Second, func() bool {
		var aOK, bOK bool
		for _, s := range rmA.Stats() {
			if s.Direction == "recv" && s.Remote != nil && s.Remote.Packets >= n && s.ReportsSent > 0 {
				aOK = true
			}
		}
		for _, s := range rmB.Stats() {
			if s.Direction == "send" && s.Remote != nil && s.Remote.HighestSeq == n-1 &&
				s.Remote.FractionLost == 0 && s.Remote.Lost == 0 {
				bOK = true
			}
		}
		return aOK && bOK
	})

	// Receiver-side §7 numbers exist: latency window filled, jitter computed, zero gaps.
	for _, s := range rmA.Stats() {
		if s.Direction != "recv" {
			continue
		}
		if s.LatencyMaxNs <= 0 {
			t.Fatalf("latency not measured: %+v", s)
		}
		if s.SeqGaps != 0 || s.LostEst != 0 {
			t.Fatalf("unexpected loss on loopback TCP: %+v", s)
		}
		if s.JitterNs < 0 {
			t.Fatalf("negative jitter: %+v", s)
		}
	}
}

// TestNACKAndCapsNegotiated: a P2↔P2 offer round-trip grants report+sync caps and arms NACK in
// the Answer (the reserved §2.5 field, echoed only when the offer asked).
func TestNACKAndCapsNegotiated(t *testing.T) {
	h := &hub{}
	secrets := fakeSecrets{key: testKey()}
	rmB := New(Options{Self: "B", Bus: &busView{h, "B"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1"})
	rmA := New(Options{Self: "A", Bus: &busView{h, "A"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1"})
	rmB.RegisterSource(SourceDesc{ID: "mic", Kind: KindAudio, Codec: CodecPCMS16},
		func(context.Context, Offer) (Source, error) { return &sliceSource{}, nil })
	rmA.RegisterSink(SinkDesc{ID: "spk", Kind: KindAudio},
		func(context.Context, Answer) (Sink, error) { return &collectSink{done: make(chan struct{})}, nil })
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

	ansCh := make(chan Answer, 1)
	unsub := (&busView{h, "observer"}).Subscribe(TopicAnswer, func(ev Event) {
		var a Answer
		if json.Unmarshal(ev.Data, &a) == nil {
			select {
			case ansCh <- a:
			default:
			}
		}
	})
	defer unsub()
	if _, err := rmA.Offer("B", "mic", "spk", CodecPCMS16); err != nil {
		t.Fatal(err)
	}
	select {
	case a := <-ansCh:
		if !a.Accept || !a.NACK {
			t.Fatalf("NACK not armed: %+v", a)
		}
		if a.Caps == nil || !a.Caps.Report || !a.Caps.Sync {
			t.Fatalf("caps not granted: %+v", a.Caps)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no answer")
	}
}

// TestRouteClockSync: over a live loopback route, A (SoftwareClock, SyncPeer=B) probes B whose
// clock runs 500 ms ahead - A's clock disciplines to B within a few ms and SyncStats report a
// locked estimate (§2.3 tier 2 accept: stable offset shown with the active tier).
func TestRouteClockSync(t *testing.T) {
	h := &hub{}
	secrets := fakeSecrets{key: testKey()}
	clkB := NewMonotonicClock()
	clkB.SetOffset(500_000_000) // B's media clock is +500 ms
	clkA := NewSoftwareClock()
	rmB := New(Options{Self: "B", Bus: &busView{h, "B"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1", Clock: clkB})
	rmA := New(Options{Self: "A", Bus: &busView{h, "A"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1", Clock: clkA, SyncPeer: "B"})
	rmA.syncBurst, rmA.syncSteady = 5*time.Millisecond, 50*time.Millisecond

	rmB.RegisterSource(SourceDesc{ID: "mic", Kind: KindAudio, Codec: CodecPCMS16},
		func(context.Context, Offer) (Source, error) {
			return &sliceSource{frames: []*Frame{{Kind: KindAudio, Codec: CodecPCMS16, Payload: []byte{1}}}}, nil
		})
	sink := newCollectSink(1)
	rmA.RegisterSink(SinkDesc{ID: "spk", Kind: KindAudio},
		func(context.Context, Answer) (Sink, error) { return sink, nil })

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
	if diff := clkB.Now() - clkA.Now(); diff < 400_000_000 {
		t.Fatalf("precondition: clocks should start ~500 ms apart, diff=%d", diff)
	}
	if _, err := rmA.Offer("B", "mic", "spk", CodecPCMS16); err != nil {
		t.Fatal(err)
	}

	// Wait for a locked estimate on A about B.
	waitFor(t, 3*time.Second, func() bool {
		for _, s := range rmA.SyncStats() {
			if s.Peer == "B" && s.Locked {
				return true
			}
		}
		return false
	})
	// Disciplined: A's clock now tracks B within loopback error bounds.
	if q := rmA.ClockQuality(); q.Tier != TierSoftware || !q.Locked {
		t.Fatalf("clock quality = %+v", q)
	}
	diff := clkB.Now() - clkA.Now()
	if diff < 0 {
		diff = -diff
	}
	if diff > 20_000_000 { // 20 ms guard band (doc target ±1 ms on idle LAN; CI boxes jitter)
		t.Fatalf("clock not disciplined: |B−A| = %d ns", diff)
	}
	// The responder (B) never probes: no sync telemetry about A appears on B.
	for _, s := range rmB.SyncStats() {
		if s.Peer == "A" {
			t.Fatalf("responder must not accumulate sync samples: %+v", s)
		}
	}
}

// TestP1OfferCompat: an offer WITHOUT the P2 caps extension (a P1 peer) gets an Answer with no
// caps/nack keys, and the P2 sender emits zero meta frames on the route - the P1 receiver sees
// only media frames, exactly the P1 wire.
func TestP1OfferCompat(t *testing.T) {
	h := &hub{}
	secrets := fakeSecrets{key: testKey()}
	rmB := New(Options{Self: "B", Bus: &busView{h, "B"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1"})
	rmB.reportEvery = 10 * time.Millisecond // would spam reports fast if wrongly granted

	const n = 5
	frames := make([]*Frame, n)
	for i := range frames {
		frames[i] = &Frame{Kind: KindAudio, Codec: CodecPCMS16, PTS: int64(i + 1), Payload: []byte{byte(i)}}
	}
	rmB.RegisterSource(SourceDesc{ID: "mic", Kind: KindAudio, Codec: CodecPCMS16},
		func(context.Context, Offer) (Source, error) { return &sliceSource{frames: frames}, nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rmB.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer rmB.Stop()

	// Simulate the P1 requester by hand: raw offer on the bus, then a raw dial + preamble.
	ansCh := make(chan json.RawMessage, 1)
	bv := &busView{h, "A"}
	unsub := bv.Subscribe(TopicAnswer, func(ev Event) {
		if !ev.Local {
			select {
			case ansCh <- ev.Data:
			default:
			}
		}
	})
	defer unsub()
	off, err := json.Marshal(Offer{Session: "p1sess", Target: "B", SourceID: "mic", SinkID: "spk",
		Codec: CodecPCMS16, Transport: TransportTCP})
	if err != nil {
		t.Fatal(err)
	}
	bv.Publish(TopicOffer, off)

	var raw json.RawMessage
	select {
	case raw = <-ansCh:
	case <-time.After(2 * time.Second):
		t.Fatal("no answer")
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"caps", "nack"} {
		if _, present := keys[k]; present {
			t.Fatalf("answer to a P1 offer must omit %q: %s", k, raw)
		}
	}
	var ans Answer
	if err := json.Unmarshal(raw, &ans); err != nil || !ans.Accept {
		t.Fatalf("answer: %+v err=%v", ans, err)
	}

	c, err := net.Dial("tcp", ans.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := writePreamble(c, "p1sess"); err != nil {
		t.Fatal(err)
	}
	conn, err := NewConn(c, secrets.key, true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		f, err := conn.ReadFrame()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if f.Stream == 0 || f.Kind == KindMeta {
			t.Fatalf("meta frame leaked to a P1 peer: %+v", f)
		}
	}
	// No further traffic: the sender must stay silent (no reports) toward a P1 peer.
	_ = c.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if f, err := conn.ReadFrame(); err == nil {
		t.Fatalf("unexpected extra frame toward P1 peer: %+v", f)
	} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("want read timeout (silence), got %v", err)
	}
}

// TestOfferUnknownSourceRejected: offering a source no peer owns yields a reject Answer, no route.
func TestOfferUnknownSourceRejected(t *testing.T) {
	h := &hub{}
	secrets := fakeSecrets{key: testKey()}
	rmB := New(Options{Self: "B", Bus: &busView{h, "B"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1"})
	rmA := New(Options{Self: "A", Bus: &busView{h, "A"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1"})
	rmA.RegisterSink(SinkDesc{ID: "spk", Kind: KindAudio}, func(context.Context, Answer) (Sink, error) {
		return &collectSink{done: make(chan struct{})}, nil
	})
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

	if _, err := rmA.Offer("B", "does-not-exist", "spk", CodecPCMS16); err != nil {
		t.Fatal(err)
	}
	// Give the offer→reject round-trip time, then assert no active route came up.
	time.Sleep(200 * time.Millisecond)
	if len(rmA.Stats()) != 0 || len(rmB.Stats()) != 0 {
		t.Fatalf("no route expected: A=%d B=%d", len(rmA.Stats()), len(rmB.Stats()))
	}

	// Offering into an unregistered local sink errors synchronously.
	if _, err := rmA.Offer("B", "mic", "no-sink", CodecPCMS16); err == nil {
		t.Fatal("expected error offering into an unregistered sink")
	}
}

// Raw-video guard: a big uncompressed video source is refused on BOTH sides - the
// requester errors synchronously when it has no decoders, and the sender refuses the
// offer when negotiation yields no codec (no encoders). No route may come up either way.
func TestRawVideoRouteRefused(t *testing.T) {
	h := &hub{}
	secrets := fakeSecrets{key: testKey()}
	rmB := New(Options{Self: "B", Bus: &busView{h, "B"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1"})
	rmA := New(Options{Self: "A", Bus: &busView{h, "A"}, Secrets: secrets, Ports: []int{0}, AdvertHost: "127.0.0.1"})
	rmB.RegisterSource(SourceDesc{ID: "vj", Kind: KindVideo, Codec: CodecNRGBA,
		Width: 1920, Height: 1080, FPS: 60}, func(context.Context, Offer) (Source, error) {
		t.Error("raw big-video source must never be opened")
		return nil, context.Canceled
	})
	rmA.RegisterSink(SinkDesc{ID: "scr", Kind: KindVideo}, func(context.Context, Answer) (Sink, error) {
		return &collectSink{done: make(chan struct{})}, nil
	})
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
	rmB.Advertise() // B's start-advert predates A's subscription; don't wait for the 5s re-advert
	waitFor(t, 2*time.Second, func() bool {
		adv, ok := rmA.RemoteAdverts()["B"]
		return ok && len(adv.Sources) == 1
	})

	// requester side: no local decoders → synchronous error at the click
	if _, err := rmA.Offer("B", "vj", "scr", CodecNone); err == nil {
		t.Fatal("expected requester-side raw-video refusal")
	}
	// sender side: requester claims decoders, sender has no encoders → async refusal
	if _, err := rmA.OfferRoute("B", "vj", "scr", OfferOptions{Decoders: []string{DecodeH264}}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if len(rmA.Stats()) != 0 || len(rmB.Stats()) != 0 {
		t.Fatalf("no route expected: A=%d B=%d", len(rmA.Stats()), len(rmB.Stats()))
	}
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
