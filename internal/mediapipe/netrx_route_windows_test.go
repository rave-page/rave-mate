//go:build windows && cgo

package mediapipe

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
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

// txSource is a NativeTXSource backed by rave-mate-txtest: the send route's native hook hands it the
// route (keys, InitialSeq, receiver address) and the zigmedia sender transmits from its OWN socket.
type txSource struct {
	exe     string
	frames  int
	mu      sync.Mutex
	hints   []medialink.RateHint
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	gen     atomic.Uint64
	br, fps atomic.Int64
	done    chan struct{}
	quitted atomic.Bool
}

func (s *txSource) Next(ctx context.Context) (*medialink.Frame, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (s *txSource) Close() error { s.quit(); return nil }
func (s *txSource) SetRateHint(h medialink.RateHint) {
	s.mu.Lock()
	s.hints = append(s.hints, h)
	s.mu.Unlock()
}
func (s *txSource) gotHint() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.hints) > 0 }

func (s *txSource) AttachNativeTX(p medialink.NativeTXParams) (medialink.NativeTX, error) {
	params, _ := json.Marshal(map[string]any{"peer": p.PeerAddr, "token": p.Token, "skey": hex.EncodeToString(p.SendKey[:]),
		"spfx": p.SendPrefix, "rkey": hex.EncodeToString(p.RecvKey[:]), "rpfx": p.RecvPrefix, "enc": p.Encrypt,
		"iseq": p.InitialSeq, "stream": p.Stream, "codec": int(p.Codec), "frames": s.frames, "au": "small"})
	cmd := exec.Command(s.exe)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s.cmd, s.stdin, s.done = cmd, stdin, make(chan struct{})
	go func() {
		defer close(s.done)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			var ev struct {
				Gen uint64 `json:"gen"`
				Br  int64  `json:"br"`
				FPS int64  `json:"fps"`
			}
			if json.Unmarshal(sc.Bytes(), &ev) == nil && ev.Gen > 0 {
				s.br.Store(ev.Br)
				s.fps.Store(ev.FPS)
				s.gen.Store(ev.Gen)
			}
		}
		_ = cmd.Wait()
	}()
	if _, err := stdin.Write(append(params, '\n')); err != nil {
		return nil, err
	}
	return (*txNative)(s), nil
}

func (s *txSource) quit() {
	if s.cmd == nil || !s.quitted.CompareAndSwap(false, true) {
		return
	}
	_, _ = s.stdin.Write([]byte("quit\n"))
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
	}
}

// txNative is the NativeTX view of a running txtest.
type txNative txSource

func (n *txNative) SetRate(int, int) {}
func (n *txNative) Stats() medialink.NativeTXStats {
	return medialink.NativeTXStats{}
}
func (n *txNative) Done() <-chan struct{} { return n.done }
func (n *txNative) Err() error            { return nil }
func (n *txNative) Close() error          { (*txSource)(n).quit(); return nil }
func (n *txNative) PollRateHint() (medialink.RateHint, uint64) {
	return medialink.RateHint{Type: medialink.MetaRate, MaxBitrateKbps: int(n.br.Load()), MaxFPS: int(n.fps.Load())}, n.gen.Load()
}

// TestNativeTXToNativeRXRoute: both ends native through both RouteManager hooks - the production rig
// shape. The native sender transmits from its own socket with InitialSeq continued from the answerer's
// Go conn; the child's receiver must REBIND its control target to it (else NACK/ping/MetaRate would
// go to the dead mux port), MetaRate reaches the sender's rate-hint poll, the sender's bye ends the
// route.
func TestNativeTXToNativeRXRoute(t *testing.T) {
	netEncExe(t)
	txExe := filepath.Join(filepath.Dir(os.Getenv("RAVE_MATE_ENC_EXE")), "rave-mate-txtest.exe")
	log := logbus.New(256)
	src := &txSource{exe: txExe, frames: 300}
	h := &netBus{}
	encFac := func(context.Context, medialink.EncodeSpec, medialink.Source) (medialink.Source, error) {
		return src, nil
	}
	decFac := func(_ context.Context, s medialink.DecodeSpec, sink medialink.Sink) (medialink.Sink, error) {
		d, err := mfenc.OpenProcDecSession(mfenc.ProcDecOpts{LUID: 0x6e6579, InW: s.Width, InH: s.Height, OutW: s.Width,
			OutH: s.Height, FPS: s.FPS, KbpsHint: decRingKbpsHint, NullDecode: true})
		if err != nil {
			return nil, err
		}
		md := &mfDecoder{log: log, dec: d, sink: sink, spec: s, opened: time.Now()}
		md.needKey.Store(true)
		return md, nil
	}
	rmB := medialink.New(medialink.Options{Self: "B", Bus: &netBusView{h, "B"}, Secrets: netSecrets{}, Ports: []int{0},
		AdvertHost: "127.0.0.1", UDP: true, Log: log, Encoders: []string{"h264_nvenc"}, Encoder: encFac})
	rmA := medialink.New(medialink.Options{Self: "A", Bus: &netBusView{h, "A"}, Secrets: netSecrets{}, Ports: []int{0},
		AdvertHost: "127.0.0.1", UDP: true, Log: log, Decoders: []string{medialink.DecodeH264}, Decoder: decFac})
	rmB.RegisterSource(medialink.SourceDesc{ID: "cam", Name: "Cam", Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA,
		Width: 1280, Height: 720, FPS: 60},
		func(context.Context, medialink.Offer) (medialink.Source, error) { return &pacedSource{n: 1}, nil })
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
	defer src.quit()
	var last medialink.RouteStat
	t.Cleanup(func() {
		if t.Failed() {
			for _, e := range log.Snapshot() {
				t.Logf("log: %s %s %v", e.Source, e.Msg, e.Fields)
			}
			t.Logf("last recv stat nrx=%+v", last.NativeRX)
		}
	})
	rmB.Advertise()
	waitUntil(t, 2*time.Second, func() bool { _, ok := rmA.RemoteAdverts()["B"]; return ok })
	if _, err := rmA.OfferRoute("B", "cam", "out", medialink.OfferOptions{BitrateKbps: 8000}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 10*time.Second, func() bool {
		for _, s := range rmA.Stats() {
			if s.Direction == "recv" && s.NativeRX != nil {
				last = s
				return s.NativeRX.Published >= 240
			}
		}
		return false
	})
	// KeyWaits > 0 is expected here: the native sender starts before the receive hand-off, the first
	// IDR lands on the Go conn, the child waits for a key and ONE PLI recovers it.
	if n := last.NativeRX; n.Rebinds < 1 || n.GapDrops != 0 || n.KeyWaits > 60 || (n.KeyWaits > 0 && n.PLIsSent == 0) {
		t.Fatalf("native<->native: %+v", n)
	}
	sendNative := false
	for _, s := range rmB.Stats() {
		sendNative = sendNative || (s.Direction == "send" && s.TransportImpl == "native")
	}
	if !sendNative {
		t.Fatal("send route did not take the native sender")
	}
	waitUntil(t, 5*time.Second, src.gotHint) // receiver MetaRate -> child control -> native sender -> poll -> source
	src.quit()                               // native sender bye
	waitUntil(t, 5*time.Second, func() bool {
		for _, s := range rmA.Stats() {
			if s.Direction == "recv" {
				return false
			}
		}
		return true
	})
}
