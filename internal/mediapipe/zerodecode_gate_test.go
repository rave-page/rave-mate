package mediapipe

// Increment-2 gate: the native decode path is requested ONLY when the whole gate holds, and every
// refusal must fall through to the ffmpeg decode child rather than breaking the route.

import (
	"context"
	"testing"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
)

// gateSink is a plain Sink: no GPU destination texture.
type gateSink struct{ writes int }

func (g *gateSink) Write(*medialink.Frame) error { g.writes++; return nil }
func (g *gateSink) Close() error                 { return nil }

// gateZCSink also exposes a (fake) destination texture.
type gateZCSink struct {
	gateSink
	h     uint64
	w, h2 int
	name  string
	ok    bool
}

func (g *gateZCSink) SharedTexture() (uint64, uint32, int, int, string, bool) {
	return g.h, 87, g.w, g.h2, g.name, g.ok
}

func withDecodeGate(t *testing.T, on bool) {
	t.Helper()
	prev := ZeroCopyDecode
	ZeroCopyDecode = func() bool { return on }
	t.Cleanup(func() { ZeroCopyDecode = prev })
}

func decSpec() medialink.DecodeSpec {
	return medialink.DecodeSpec{Codec: medialink.CodecH264, Width: 1280, Height: 720, FPS: 60}
}

// TestNativeDecodeSkippedWhenFlagOff: flag off = the decode factory never even looks at the sink.
func TestNativeDecodeSkippedWhenFlagOff(t *testing.T) {
	withDecodeGate(t, false)
	sink := &gateZCSink{h: 0xAAAA, w: 1280, h2: 720, name: "dest", ok: true}
	if _, err := newMFDecoder(context.Background(), logbus.New(16), decSpec(), sink); err == nil {
		t.Fatal("native decode opened with the flag OFF")
	}
}

// TestNativeDecodeNeedsAZeroCopySink: an ordinary sink has no texture to render into.
func TestNativeDecodeNeedsAZeroCopySink(t *testing.T) {
	withDecodeGate(t, true)
	if _, err := newMFDecoder(context.Background(), logbus.New(16), decSpec(), &gateSink{}); err == nil {
		t.Fatal("native decode opened over a sink with no GPU destination")
	}
}

// TestNativeDecodeNeedsAHandle: ok=false / handle 0 (CPU-memoryshare sender) = frame path.
func TestNativeDecodeNeedsAHandle(t *testing.T) {
	withDecodeGate(t, true)
	log := logbus.New(16)
	for _, s := range []*gateZCSink{
		{h: 0, w: 1280, h2: 720, name: "dest", ok: true},
		{h: 0xAAAA, w: 1280, h2: 720, name: "dest", ok: false},
	} {
		if _, err := newMFDecoder(context.Background(), log, decSpec(), s); err == nil {
			t.Fatalf("native decode opened without a usable destination handle (%+v)", s)
		}
	}
}

// TestNativeDecodeOnlyForH264AndHEVC: MJPEG / raw stay on the ffmpeg path (the child has no
// decoder for them), and the codec map itself is the contract.
func TestNativeDecodeOnlyForH264AndHEVC(t *testing.T) {
	cases := []struct {
		c    medialink.Codec
		hevc bool
		ok   bool
	}{
		{medialink.CodecH264, false, true},
		{medialink.CodecHEVC, true, true},
		{medialink.CodecJPEG, false, false},
		{medialink.CodecNRGBA, false, false},
		{medialink.CodecAV1, false, false},
	}
	for _, c := range cases {
		hevc, ok := decCodecSupported(c.c)
		if ok != c.ok || hevc != c.hevc {
			t.Errorf("decCodecSupported(%v) = (%v,%v), want (%v,%v)", c.c, hevc, ok, c.hevc, c.ok)
		}
	}
}

// TestDecoderFactoryFallsThroughToFfmpeg: the whole point of the ladder - the flag on but nothing
// usable must still hand back a working ffmpeg-backed sink, never an error.
func TestDecoderFactoryFallsThroughToFfmpeg(t *testing.T) {
	withDecodeGate(t, true)
	_, dec := Factories(logbus.New(16))
	sink := &gateSink{}
	s, err := dec(context.Background(), decSpec(), sink)
	if err != nil {
		t.Skipf("no ffmpeg on this host: %v", err) // the fallback itself needs ffmpeg
	}
	if _, native := s.(*mfDecoder); native {
		t.Fatal("factory returned the native decoder for a sink with no GPU destination")
	}
	_ = s.Close()
}
