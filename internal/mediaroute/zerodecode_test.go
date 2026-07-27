package mediaroute

import (
	"errors"
	"image"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/videoshare"
)

// zerodecode_test.go - increment-2 gate on the receive SINK: with the flag on it must open the
// sender eagerly and publish its destination texture; with the flag off (or when the eager open
// fails) it must be today's lazy frame sender, byte for byte.

type fakeSharedSender struct {
	sent   int
	handle uint64
	closed bool
}

func (f *fakeSharedSender) Send(*image.NRGBA) error { f.sent++; return nil }
func (f *fakeSharedSender) Close()                  { f.closed = true }
func (f *fakeSharedSender) Handle() uint64          { return f.handle }
func (f *fakeSharedSender) Format() uint32          { return 87 }

func decodeMgr(t *testing.T, on bool, shared func(string, int, int) (videoshare.SharedSender, error)) *Manager {
	t.Helper()
	return New(Options{
		Log:             logbus.New(32),
		Router:          &fakeRouter{},
		Cfg:             func() config.MediaLinkFeature { c := config.MediaLinkFeature{}; c.SetZeroCopyDecode(on); return c },
		NewSharedSender: shared,
	})
}

// TestSinkExposesDestinationTextureWhenGateOn: the decoder can only render into a texture that
// already exists, so the sink must open it at route-open time and report it.
func TestSinkExposesDestinationTextureWhenGateOn(t *testing.T) {
	ss := &fakeSharedSender{handle: 0xC0FFEE}
	m := decodeMgr(t, true, func(string, int, int) (videoshare.SharedSender, error) { return ss, nil })
	sink, err := m.openSpoutSink("rave-mate link cam", 1280, 720)
	if err != nil {
		t.Fatalf("openSpoutSink: %v", err)
	}
	zc, ok := sink.(medialink.ZeroCopySink)
	if !ok {
		t.Fatal("sink does not implement medialink.ZeroCopySink")
	}
	h, fmt, w, hh, name, ok := zc.SharedTexture()
	if !ok || h != 0xC0FFEE || fmt != 87 || w != 1280 || hh != 720 || name != "rave-mate link cam" {
		t.Fatalf("SharedTexture = (%#x,%d,%dx%d,%q,%v)", h, fmt, w, hh, name, ok)
	}
	// It is still a full sink: a refused native session must publish through the SAME sender.
	if err := sink.Write(&medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA,
		Payload: make([]byte, 1280*720*4)}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if ss.sent != 1 {
		t.Fatalf("frame path sent %d frames through the shared sender, want 1", ss.sent)
	}
	_ = sink.Close()
	if !ss.closed {
		t.Fatal("Close did not release the sender")
	}
}

// TestSinkHasNoDestinationTextureWhenGateOff: flag off = no eager open at all, and no texture to
// hand a decoder (so the native path is unreachable).
func TestSinkHasNoDestinationTextureWhenGateOff(t *testing.T) {
	called := 0
	m := decodeMgr(t, false, func(string, int, int) (videoshare.SharedSender, error) {
		called++
		return &fakeSharedSender{handle: 1}, nil
	})
	// The lazy frame sender needs a real backend, so the sink may not open at all in the untagged
	// build. The DECISION is what is under test: the eager open must never even be attempted.
	sink, err := m.openSpoutSink("rave-mate link cam", 1280, 720)
	if called != 0 {
		t.Fatalf("the eager shared-sender open ran %d times with the flag OFF", called)
	}
	if err != nil {
		return
	}
	defer func() { _ = sink.Close() }()
	if zc, ok := sink.(medialink.ZeroCopySink); ok {
		if _, _, _, _, _, ok := zc.SharedTexture(); ok {
			t.Fatal("a flag-off sink reported a destination texture")
		}
	}
}

// TestSinkFallsBackWhenEagerOpenFails: a host without a GPU destination must NOT lose the route -
// it falls through to the lazy frame sender.
func TestSinkFallsBackWhenEagerOpenFails(t *testing.T) {
	called := 0
	m := decodeMgr(t, true, func(string, int, int) (videoshare.SharedSender, error) {
		called++
		return nil, errors.New("no DX11 shared texture")
	})
	sink, err := m.openSpoutSink("rave-mate link cam", 1280, 720)
	if called != 1 {
		t.Fatalf("the eager open was attempted %d times, want 1", called)
	}
	if err != nil {
		return // no lazy backend here either: the route degrades, which is the fallback rung
	}
	defer func() { _ = sink.Close() }()
	if zc, ok := sink.(medialink.ZeroCopySink); ok {
		if _, _, _, _, _, ok := zc.SharedTexture(); ok {
			t.Fatal("reported a destination texture after the eager open failed")
		}
	}
}

// TestSinkWithZeroHandleReportsNoTexture: a backend that "succeeded" with handle 0 must still be
// treated as no destination (CPU/memoryshare sender).
func TestSinkWithZeroHandleReportsNoTexture(t *testing.T) {
	m := decodeMgr(t, true, func(string, int, int) (videoshare.SharedSender, error) {
		return &fakeSharedSender{handle: 0}, nil
	})
	sink, err := m.openSpoutSink("rave-mate link cam", 640, 480)
	if err != nil {
		t.Fatalf("openSpoutSink: %v", err)
	}
	defer func() { _ = sink.Close() }()
	zc := sink.(medialink.ZeroCopySink)
	if _, _, _, _, _, ok := zc.SharedTexture(); ok {
		t.Fatal("handle 0 must report no destination texture")
	}
}
