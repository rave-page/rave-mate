package mediaroute

import (
	"context"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/videoshare"
)

// zcManager builds a Manager whose receiver opens are COUNTED - the readback oracle.
func zcManager(t *testing.T, share func(string) (uint64, uint32, int, int, bool), put func([]byte)) (*Manager, *fakeRecv, *int) {
	t.Helper()
	recv := newFakeRecv()
	opens := new(int)
	if put == nil {
		put = func([]byte) {}
	}
	m := New(Options{
		Log:    logbus.New(64),
		Router: nil,
		Cfg:    func() config.MediaLinkFeature { return config.MediaLinkFeature{MaxFPS: 60} },
		OpenReceiver: func(string, float64) (videoshare.FrameReceiver, error) {
			*opens++
			return recv, nil
		},
		PutPix:      put,
		SenderShare: share,
	})
	return m, recv, opens
}

// TestZeroCopyRouteDoesNoReadback is the increment-1 wiring gate: a zero-copy consumer asks the
// source for its shared texture and never calls Next, so NO capture is ever opened - no GL
// context, no poll loop, no pooled pixel buffers. An eager attach (the pre-inc-1 behaviour)
// would fail this.
func TestZeroCopyRouteDoesNoReadback(t *testing.T) {
	m, _, opens := zcManager(t, func(string) (uint64, uint32, int, int, bool) {
		return 0xdeadbeef, 87, 3840, 2160, true
	}, nil)
	src, err := m.openSpoutSource("OBS", 3840, 2160)
	if err != nil {
		t.Fatalf("openSpoutSource: %v", err)
	}
	if *opens != 0 || m.hub.live() != 0 {
		t.Fatalf("opening the source started a capture: opens=%d live=%d", *opens, m.hub.live())
	}
	zc, ok := src.(medialink.ZeroCopySource)
	if !ok {
		t.Fatal("spoutSource must implement medialink.ZeroCopySource")
	}
	h, fmtID, w, hh, name, ok := zc.SharedTexture()
	if !ok || h != 0xdeadbeef || fmtID != 87 || w != 3840 || hh != 2160 || name != "OBS" {
		t.Fatalf("SharedTexture() = %#x %d %dx%d %q %v", h, fmtID, w, hh, name, ok)
	}
	if *opens != 0 || m.hub.live() != 0 {
		t.Fatalf("SharedTexture opened a capture: opens=%d live=%d", *opens, m.hub.live())
	}
	if err := src.Close(); err != nil { // closing a never-attached source is a clean no-op
		t.Fatalf("Close: %v", err)
	}
	if *opens != 0 || m.hub.live() != 0 {
		t.Fatalf("Close opened/leaked a capture: opens=%d live=%d", *opens, m.hub.live())
	}
}

// TestReadbackRouteAttachesOnFirstNext is the control: the FALLBACK path must be unchanged -
// the first Next attaches exactly one shared capture and delivers the frame.
func TestReadbackRouteAttachesOnFirstNext(t *testing.T) {
	pool := newPoisonPool(t)
	m, recv, opens := zcManager(t, nil, pool.put)
	src, err := m.openSpoutSource("OBS", 64, 64)
	if err != nil {
		t.Fatal(err)
	}
	if *opens != 0 {
		t.Fatalf("attach was eager: opens=%d", *opens)
	}
	recv.ch <- pool.frame(64, 64)
	f, err := src.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if *opens != 1 || m.hub.live() != 1 {
		t.Fatalf("first Next: opens=%d live=%d, want 1/1", *opens, m.hub.live())
	}
	if len(f.Payload) != 64*64*4 {
		t.Fatalf("payload %d bytes", len(f.Payload))
	}
	f.Release()
	// A second Next reuses the same capture (one readback per sender, unchanged).
	recv.ch <- pool.frame(64, 64)
	src.(*spoutSource).minGap = 0
	f2, err := src.Next(context.Background())
	if err != nil {
		t.Fatalf("Next 2: %v", err)
	}
	f2.Release()
	if *opens != 1 {
		t.Fatalf("second Next reopened the capture: opens=%d", *opens)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the capture to tear down with the route", func() bool { return m.hub.live() == 0 })
	waitFor(t, "every buffer recycled", pool.settled)
}

// TestSharedTextureRefusesWithoutBackend: no resolver / an unknown sender = "no shared texture",
// never a bogus handle - the encode side must then take the readback path.
func TestSharedTextureRefusesWithoutBackend(t *testing.T) {
	m, _, _ := zcManager(t, nil, nil)
	src, err := m.openSpoutSource("OBS", 64, 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, ok := src.(medialink.ZeroCopySource).SharedTexture(); ok {
		t.Fatal("SharedTexture reported ok with no backend")
	}
	m2, _, _ := zcManager(t, func(string) (uint64, uint32, int, int, bool) { return 0, 0, 0, 0, false }, nil)
	src2, err := m2.openSpoutSource("gone", 64, 64)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, ok := src2.(medialink.ZeroCopySource).SharedTexture(); ok {
		t.Fatal("SharedTexture reported ok for an unresolvable sender")
	}
	_ = src.Close()
	_ = src2.Close()
}

// TestLazyAttachErrorSurfaces: an attach failure on the first Next must reach the caller (the
// route ends cleanly) instead of being swallowed into an endless block.
func TestLazyAttachErrorSurfaces(t *testing.T) {
	s := &spoutSource{name: "x", attach: nil}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.Next(ctx); err == nil {
		t.Fatal("Next with no capture backend must error")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close after a failed attach: %v", err)
	}
}
