package medialink

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pipeline_test.go - P4 route wiring with FAKE encode/decode children: negotiation lands a tier,
// the send route wraps its source with the encoder factory, the recv route wraps its sink with the
// decoder + jitter buffer, and stats surface tier/encoder/keyframes/JB.

// fakeEnc wraps a raw source: prefixes payloads with 'E', stamps the negotiated codec, marks every
// 4th frame (and the first) a keyframe. Implements KeyframeSource + PipelineReporter.
type fakeEnc struct {
	spec     EncodeSpec
	inner    Source
	n        uint32
	keyReqs  atomic.Uint32
	restarts int
}

func (e *fakeEnc) Next(ctx context.Context) (*Frame, error) {
	f, err := e.inner.Next(ctx)
	if err != nil {
		return nil, err
	}
	out := &Frame{Kind: KindVideo, Codec: e.spec.Codec, PTS: f.PTS,
		Payload: append([]byte{'E'}, f.Payload...)}
	if e.n%4 == 0 {
		out.Flags = FlagKeyframe
	}
	e.n++
	return out, nil
}
func (e *fakeEnc) Close() error     { return e.inner.Close() }
func (e *fakeEnc) RequestKeyframe() { e.keyReqs.Add(1) }
func (e *fakeEnc) PipeStats() PipelineStats {
	return PipelineStats{Encoder: e.spec.Encoder, Restarts: e.restarts}
}

// fakeDec unwraps the 'E' prefix and forwards raw frames.
type fakeDec struct {
	spec  DecodeSpec
	inner Sink
	mu    sync.Mutex
	bad   int
}

func (d *fakeDec) Write(f *Frame) error {
	if f.Codec != d.spec.Codec || len(f.Payload) == 0 || f.Payload[0] != 'E' {
		d.mu.Lock()
		d.bad++
		d.mu.Unlock()
		return nil
	}
	return d.inner.Write(&Frame{Stream: f.Stream, Kind: f.Kind, Codec: CodecNRGBA, Seq: f.Seq,
		PTS: f.PTS, Flags: f.Flags, Payload: f.Payload[1:]})
}
func (d *fakeDec) Close() error { return d.inner.Close() }
func (d *fakeDec) PipeStats() PipelineStats {
	return PipelineStats{Encoder: "fake-dec"}
}

// videoRoutePair spins up a negotiated video route B→A with fake children; returns the managers,
// a getter for the captured encode spec, and the sink.
func videoRoutePair(t *testing.T, encoders, decoders []string, opt OfferOptions, n int) (rmA, rmB *RouteManager, spec func() EncodeSpec, sink *collectSink, teardown func()) {
	t.Helper()
	h := &hub{}
	secrets := fakeSecrets{key: testKey()}
	clk := NewMonotonicClock()
	var gotSpec EncodeSpec
	var specMu sync.Mutex
	encFac := func(_ context.Context, s EncodeSpec, src Source) (Source, error) {
		specMu.Lock()
		gotSpec = s
		specMu.Unlock()
		return &fakeEnc{spec: s, inner: src}, nil
	}
	spec = func() EncodeSpec { specMu.Lock(); defer specMu.Unlock(); return gotSpec }
	decFac := func(_ context.Context, s DecodeSpec, snk Sink) (Sink, error) {
		return &fakeDec{spec: s, inner: snk}, nil
	}
	rmB = New(Options{Self: "B", Bus: &busView{h, "B"}, Secrets: secrets, Ports: []int{0},
		AdvertHost: "127.0.0.1", Clock: clk, Encoders: encoders, Encoder: encFac})
	rmA = New(Options{Self: "A", Bus: &busView{h, "A"}, Secrets: secrets, Ports: []int{0},
		AdvertHost: "127.0.0.1", Clock: clk, Decoders: decoders, Decoder: decFac})

	frames := make([]*Frame, n)
	for i := range frames {
		frames[i] = &Frame{Kind: KindVideo, Codec: CodecNRGBA, Payload: []byte{byte(i), 0x42}}
	}
	rmB.RegisterSource(SourceDesc{ID: "cam", Name: "Cam", Kind: KindVideo, Codec: CodecNRGBA,
		Width: 64, Height: 48, FPS: 120},
		func(context.Context, Offer) (Source, error) { return &sliceSource{frames: frames}, nil })
	sink = newCollectSink(n)
	rmA.RegisterSink(SinkDesc{ID: "out", Name: "Out", Kind: KindVideo},
		func(context.Context, Answer) (Sink, error) { return sink, nil })

	ctx, cancel := context.WithCancel(context.Background())
	if err := rmB.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rmA.Start(ctx); err != nil {
		rmB.Stop()
		t.Fatal(err)
	}
	teardown = func() { cancel(); rmA.Stop(); rmB.Stop() }
	rmB.Advertise()
	waitFor(t, time.Second, func() bool { _, ok := rmA.RemoteAdverts()["B"]; return ok })
	if _, err := rmA.OfferRoute("B", "cam", "out", opt); err != nil {
		teardown()
		t.Fatal(err)
	}
	return rmA, rmB, spec, sink, teardown
}

func TestVideoRouteEncodeDecodePipeline(t *testing.T) {
	const n = 24
	rmA, rmB, spec, sink, teardown := videoRoutePair(t,
		[]string{"hevc_nvenc", "h264_nvenc"}, []string{DecodeHEVC, DecodeH264},
		OfferOptions{BitrateKbps: 12_000}, n)
	defer teardown()

	select {
	case <-sink.done:
	case <-time.After(5 * time.Second):
		sink.mu.Lock()
		t.Fatalf("timeout: %d/%d frames through encode→wire→jb→decode", len(sink.got), n)
	}
	sink.mu.Lock()
	got := sink.got
	sink.mu.Unlock()
	for i, f := range got {
		if f.Codec != CodecNRGBA || len(f.Payload) != 2 || f.Payload[0] != byte(i) {
			t.Fatalf("frame %d corrupted through the pipeline: %+v", i, f)
		}
	}
	// Encode spec carried the negotiated tier + route params.
	sp := spec()
	if sp.Encoder != "hevc_nvenc" || sp.Codec != CodecHEVC || sp.Tier != 2 || sp.Software {
		t.Fatalf("encode spec: %+v", sp)
	}
	if sp.Width != 64 || sp.Height != 48 || sp.FPS != 120 || sp.BitrateKbps != 12_000 {
		t.Fatalf("encode spec params: %+v", sp)
	}
	// Stats: sender records the choice + keyframes; receiver records tier + JB + pipe.
	waitFor(t, time.Second, func() bool {
		for _, s := range rmB.Stats() {
			if s.Direction == "send" && s.Encoder == "hevc_nvenc" && s.Tier == 2 && !s.Software &&
				s.Keyframes >= n/4 && s.Pipe != nil && s.Pipe.Encoder == "hevc_nvenc" {
				return true
			}
		}
		return false
	})
	waitFor(t, time.Second, func() bool {
		for _, s := range rmA.Stats() {
			if s.Direction == "recv" && s.Encoder == "hevc_nvenc" && s.Tier == 2 &&
				s.JB != nil && s.JB.Depth >= 1 && s.Keyframes >= n/4 && s.Pipe != nil {
				return true
			}
		}
		return false
	})
}

// TestVideoRouteSWOnlyLandsTier4: an encode side probed sw-only negotiates tier 4 and both ends
// flag Software in route stats (the §3.2 CPU warning surface).
func TestVideoRouteSWOnlyLandsTier4(t *testing.T) {
	const n = 8
	rmA, rmB, spec, sink, teardown := videoRoutePair(t,
		[]string{"libx264"}, []string{DecodeHEVC, DecodeH264, DecodeJPEG},
		OfferOptions{}, n)
	defer teardown()
	select {
	case <-sink.done:
	case <-time.After(5 * time.Second):
		t.Fatal("frames never arrived")
	}
	if sp := spec(); sp.Encoder != "libx264" || sp.Tier != 4 || !sp.Software || sp.Codec != CodecH264 {
		t.Fatalf("sw fallback spec: %+v", sp)
	}
	waitFor(t, time.Second, func() bool {
		var sOK, rOK bool
		for _, s := range rmB.Stats() {
			if s.Direction == "send" && s.Tier == 4 && s.Software {
				sOK = true
			}
		}
		for _, s := range rmA.Stats() {
			if s.Direction == "recv" && s.Tier == 4 && s.Software {
				rOK = true
			}
		}
		return sOK && rOK
	})
}

// TestVideoRouteRawWithoutCaps: a video offer with no decode caps (or no encoders on the target)
// echoes the raw codec - no encode child, no decoder, frames flow untouched (same-PC §3 rule +
// P1 compat).
func TestVideoRouteRawWithoutCaps(t *testing.T) {
	const n = 6
	rmA, _, spec, sink, teardown := videoRoutePair(t,
		nil, nil, OfferOptions{}, n) // nothing probed on either end
	defer teardown()
	select {
	case <-sink.done:
	case <-time.After(5 * time.Second):
		t.Fatal("frames never arrived")
	}
	if sp := spec(); sp.Encoder != "" {
		t.Fatalf("encode child must not engage: %+v", sp)
	}
	sink.mu.Lock()
	got := sink.got
	sink.mu.Unlock()
	for i, f := range got {
		if f.Codec != CodecNRGBA || f.Payload[0] != byte(i) {
			t.Fatalf("raw frame %d altered: %+v", i, f)
		}
	}
	// Raw video recv still rides the §3.3 jitter buffer.
	found := false
	for _, s := range rmA.Stats() {
		if s.Direction == "recv" && s.JB != nil && s.Encoder == "" {
			found = true
		}
	}
	if !found {
		t.Fatal("raw video route missing JB stats")
	}
}
