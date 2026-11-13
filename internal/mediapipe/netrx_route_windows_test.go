//go:build windows && cgo

package mediapipe

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mfenc"
)

// End-to-end native receive WITHOUT a GPU: two RouteManagers negotiate a UDP video route over
// loopback; the receiver's decode sink is a REAL mfDecoder whose child session runs null_dec, so the
// whole receive path (socket -> auth -> reassembly -> NACK -> playout -> "publish") runs in the real
// rave-mate-enc.exe through the real parent ops and the real medialink hook. The sender is the Go
// UDP pump (wire-compatible legacy sender) behind a fake encoder.

type netBus struct {
	mu   sync.Mutex
	subs []netSub
}
type netSub struct {
	self, topic string
	fn          func(medialink.Event)
	dead        bool
}
type netBusView struct {
	h    *netBus
	self string
}

func (b *netBusView) Publish(topic string, data json.RawMessage) {
	b.h.mu.Lock()
	subs := append([]netSub(nil), b.h.subs...)
	b.h.mu.Unlock()
	for _, s := range subs {
		if !s.dead && s.topic == topic {
			s.fn(medialink.Event{Origin: b.self, Local: s.self == b.self, Data: data})
		}
	}
}
func (b *netBusView) Subscribe(topic string, fn func(medialink.Event)) func() {
	b.h.mu.Lock()
	idx := len(b.h.subs)
	b.h.subs = append(b.h.subs, netSub{self: b.self, topic: topic, fn: fn})
	b.h.mu.Unlock()
	return func() { b.h.mu.Lock(); b.h.subs[idx].dead = true; b.h.mu.Unlock() }
}

type netSecrets struct{}

func (netSecrets) MediaSecret(string) ([]byte, bool) {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(0x40 + i)
	}
	return k, true
}

// pacedSource emits n raw frames at 60 fps, then EOF (the sender ends the route with a bye).
type pacedSource struct {
	n, i int
	t0   time.Time
}

func (s *pacedSource) Next(ctx context.Context) (*medialink.Frame, error) {
	if s.i >= s.n {
		return nil, io.EOF
	}
	if s.t0.IsZero() {
		s.t0 = time.Now()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Until(s.t0.Add(time.Duration(s.i) * time.Second / 60))):
	}
	s.i++
	return &medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA, Payload: []byte{byte(s.i)}}, nil
}
func (s *pacedSource) Close() error { return nil }

// fakeH264 stamps the negotiated codec, a 6 kB payload (multi-fragment) and a keyframe every 60;
// records the receiver's rate hints (MetaRate reached the sender).
type fakeH264 struct {
	inner medialink.Source
	codec medialink.Codec
	n     int
	force atomic.Bool
	mu    sync.Mutex
	hints []medialink.RateHint
}

func (e *fakeH264) Next(ctx context.Context) (*medialink.Frame, error) {
	f, err := e.inner.Next(ctx)
	if err != nil {
		return nil, err
	}
	out := &medialink.Frame{Kind: medialink.KindVideo, Codec: e.codec, PTS: f.PTS, Payload: make([]byte, 6000)}
	out.Payload[0] = 0xA5
	if e.n%60 == 0 || e.force.Swap(false) {
		out.Flags = medialink.FlagKeyframe
	}
	e.n++
	return out, nil
}
func (e *fakeH264) Close() error     { return e.inner.Close() }
func (e *fakeH264) RequestKeyframe() { e.force.Store(true) }
func (e *fakeH264) SetRateHint(h medialink.RateHint) {
	e.mu.Lock()
	e.hints = append(e.hints, h)
	e.mu.Unlock()
}
func (e *fakeH264) gotHint() bool { e.mu.Lock(); defer e.mu.Unlock(); return len(e.hints) > 0 }

type nullSink struct{}

func (nullSink) Write(*medialink.Frame) error { return nil }
func (nullSink) Close() error                 { return nil }

func netEncExe(t *testing.T) {
	t.Helper()
	wd, _ := os.Getwd()
	for d := wd; ; d = filepath.Dir(d) {
		p := filepath.Join(d, "native", "zigenc", "zig-out", "bin", "rave-mate-enc.exe")
		if _, err := os.Stat(filepath.Join(filepath.Dir(p), "rave-mate-txtest.exe")); err == nil {
			t.Setenv("RAVE_MATE_ENC_EXE", p) // the in-repo v5 build (the gate may hide the child from GPU tests)
			return
		}
		if filepath.Dir(d) == d {
			t.Skip("native/zigenc not built")
		}
	}
}

func TestNativeRXRouteEndToEnd(t *testing.T) {
	netEncExe(t)
	const n = 240 // 4 s at 60 fps
	log := logbus.New(256)
	enc := &fakeH264{}
	var decs atomic.Int32
	h := &netBus{}
	encFac := func(_ context.Context, s medialink.EncodeSpec, src medialink.Source) (medialink.Source, error) {
		enc.inner, enc.codec = src, s.Codec
		return enc, nil
	}
	decFac := func(_ context.Context, s medialink.DecodeSpec, sink medialink.Sink) (medialink.Sink, error) {
		d, err := mfenc.OpenProcDecSession(mfenc.ProcDecOpts{LUID: 0x6e6577, InW: s.Width, InH: s.Height, OutW: s.Width,
			OutH: s.Height, FPS: s.FPS, KbpsHint: decRingKbpsHint, NullDecode: true})
		if err != nil {
			return nil, err
		}
		decs.Add(1)
		md := &mfDecoder{log: log, dec: d, sink: sink, spec: s, opened: time.Now()}
		md.needKey.Store(true)
		return md, nil
	}
	rmB := medialink.New(medialink.Options{Self: "B", Bus: &netBusView{h, "B"}, Secrets: netSecrets{}, Ports: []int{0},
		AdvertHost: "127.0.0.1", UDP: true, Log: log, Encoders: []string{"h264_nvenc"}, Encoder: encFac})
	rmA := medialink.New(medialink.Options{Self: "A", Bus: &netBusView{h, "A"}, Secrets: netSecrets{}, Ports: []int{0},
		AdvertHost: "127.0.0.1", UDP: true, Log: log, Decoders: []string{medialink.DecodeH264}, Decoder: decFac,
		PlayoutPreset: func() string { return "low-latency" }})
	rmB.RegisterSource(medialink.SourceDesc{ID: "cam", Name: "Cam", Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA,
		Width: 1280, Height: 720, FPS: 60},
		func(context.Context, medialink.Offer) (medialink.Source, error) { return &pacedSource{n: n}, nil })
	rmA.RegisterSink(medialink.SinkDesc{ID: "out", Name: "Out", Kind: medialink.KindVideo},
		func(context.Context, medialink.Answer) (medialink.Sink, error) { return nullSink{}, nil })
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
	waitUntil(t, 2*time.Second, func() bool { _, ok := rmA.RemoteAdverts()["B"]; return ok })
	if _, err := rmA.OfferRoute("B", "cam", "out", medialink.OfferOptions{BitrateKbps: 8000}); err != nil {
		t.Fatal(err)
	}

	var last medialink.RouteStat
	t.Cleanup(func() {
		if t.Failed() {
			for _, e := range log.Snapshot() {
				t.Logf("log: %s %s %v", e.Source, e.Msg, e.Fields)
			}
			t.Logf("last recv stat: %+v nrx=%+v", last, last.NativeRX)
		}
	})
	waitUntil(t, 8*time.Second, func() bool {
		for _, s := range rmA.Stats() {
			if s.Direction == "recv" {
				last = s
				return s.TransportImpl == "native" && s.NativeRX != nil && s.NativeRX.Published >= 120
			}
		}
		return false
	})
	if decs.Load() != 1 || last.Transport != medialink.TransportUDP || last.JB == nil || last.JB.Preset != "low-latency" ||
		last.Stages == nil || last.Stages.Publish.Count == 0 || last.UDP == nil || last.UDP.RTTns <= 0 {
		t.Fatalf("native recv route telemetry: %+v nrx=%+v", last, last.NativeRX)
	}
	if last.NativeRX.GapDrops != 0 {
		t.Fatalf("gap drops on a clean loopback: %+v", last.NativeRX)
	}
	// Congestion governor -> MetaRate through the CHILD's control counter -> Go sender's encoder.
	waitUntil(t, 5*time.Second, enc.gotHint)
	// Sync pings through the child, pongs forwarded back (T4 from the child's arrival stamp).
	waitUntil(t, 5*time.Second, func() bool {
		for _, s := range rmA.SyncStats() {
			if s.Peer == "B" && s.Samples > 0 {
				return true
			}
		}
		return false
	})
	// Source EOF -> the Go sender's bye -> the child sees it -> the route ends cleanly.
	waitUntil(t, 10*time.Second, func() bool {
		for _, s := range rmA.Stats() {
			if s.Direction == "recv" {
				last = s
				return false
			}
		}
		return true
	})
	if p := last.NativeRX; p == nil || p.Published < n-30 {
		t.Fatalf("frames published before the bye: %+v", p)
	}
}

func waitUntil(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	dl := time.Now().Add(d)
	for !cond() {
		if time.Now().After(dl) {
			t.Fatal("timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
