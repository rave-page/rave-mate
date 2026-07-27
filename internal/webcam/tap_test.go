package webcam

import (
	"context"
	"io"
	"sync"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/videoshare"
)

// fakeRouter records medialink source registration (P4 network route).
type fakeRouter struct {
	mu      sync.Mutex
	sources map[string]medialink.SourceDesc
}

func (r *fakeRouter) RegisterSource(d medialink.SourceDesc, _ medialink.SourceOpen) {
	r.mu.Lock()
	r.sources[d.ID] = d
	r.mu.Unlock()
}
func (r *fakeRouter) UnregisterSource(id string) {
	r.mu.Lock()
	delete(r.sources, id)
	r.mu.Unlock()
}

func TestTapFanout(t *testing.T) {
	m := New(logbus.New(16), nil, "n", "h", func() config.WebcamFeature { return config.WebcamFeature{} })
	// Tap while the camera is off: refused.
	if _, err := m.openTap(context.Background(), medialink.Offer{}); err == nil {
		t.Fatal("tap must fail while camera off")
	}
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()
	src, err := m.openTap(context.Background(), medialink.Offer{})
	if err != nil {
		t.Fatal(err)
	}
	// Newest-wins: two frames without a read → the tap holds the second.
	cf1 := testCapFrame(1)
	cf2 := testCapFrame(2)
	f2 := cf2.f
	m.fanout(cf1)
	cf1.release()
	m.fanout(cf2)
	cf2.release()
	got, err := src.Next(context.Background())
	if err != nil || got.Payload[0] != 2 {
		t.Fatalf("newest-wins: %+v err=%v", got, err)
	}
	// Taps get a COPY: route-side stamping never mutates the shared frame.
	if got == f2 {
		t.Fatal("tap must receive a shallow copy")
	}
	got.Seq = 99
	if f2.Seq != 0 {
		t.Fatal("shared frame mutated")
	}
	// closeTaps (camera stop) → EOF.
	m.closeTaps()
	if _, err := src.Next(context.Background()); err != io.EOF {
		t.Fatalf("want EOF after stop, got %v", err)
	}
	// Closing the tap source again is a safe no-op.
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRouterRegistrationLifecycle(t *testing.T) {
	fr := &fakeRouter{sources: map[string]medialink.SourceDesc{}}
	cfg := config.WebcamFeature{Enabled: true, Device: "Cam", Width: 640, Height: 480, FPS: 30}
	m := New(logbus.New(16), nil, "n", "h", func() config.WebcamFeature { return cfg })
	m.SetRouter(fr)
	m.openRoute = func(_ context.Context, d capDesc) (func(), func() capStats, string, error) {
		return func() {}, func() capStats { return capStats{} }, SenderName(d.Device), nil
	}
	m.mu.Lock()
	m.ctx = context.Background()
	m.mu.Unlock()

	if err := m.StartCamera("", 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	fr.mu.Lock()
	d, ok := fr.sources[camSourceID]
	fr.mu.Unlock()
	if !ok || d.Width != 640 || d.Height != 480 || d.FPS != 30 || d.Kind != medialink.KindVideo {
		t.Fatalf("registered desc: %+v ok=%v", d, ok)
	}
	m.StopCamera()
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if _, still := fr.sources[camSourceID]; still {
		t.Fatal("source must unregister on stop")
	}
}

// testCapFrame builds a refcounted capFrame over an unpooled buffer (release just drops the ref).
func testCapFrame(v byte) capFrame {
	ref := videoshare.NewPixRef([]byte{v}, false, nil)
	return capFrame{ref: ref, f: &medialink.Frame{Kind: medialink.KindVideo,
		Codec: medialink.CodecNRGBA, Payload: ref.Pix(), Release: ref.Release}}
}

// TestTapFanoutRefcount: every frame handed to a tap holds a reference until the tap consumes or
// drops it, and the buffer is released EXACTLY once overall.
func TestTapFanoutRefcount(t *testing.T) {
	m := New(logbus.New(16), nil, "n", "h", func() config.WebcamFeature { return config.WebcamFeature{} })
	m.mu.Lock()
	m.running = true
	m.mu.Unlock()
	src, err := m.openTap(context.Background(), medialink.Offer{})
	if err != nil {
		t.Fatal(err)
	}
	cf := testCapFrame(7)
	m.fanout(cf)
	if got := cf.ref.Refs(); got != 2 {
		t.Fatalf("refs after fanout to 1 tap = %d, want 2 (ours + the tap's)", got)
	}
	cf.release() // the distributor's own reference
	if got := cf.ref.Refs(); got != 1 {
		t.Fatalf("refs = %d after the distributor released, want 1 (the tap still holds it)", got)
	}
	got, err := src.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	releaseFrame(got) // stands in for medialink's send loop
	if n := cf.ref.Refs(); n != 0 {
		t.Fatalf("refs = %d after the tap consumed the frame, want 0", n)
	}
	// A frame displaced inside a tap must be released too, not forgotten.
	a, b := testCapFrame(1), testCapFrame(2)
	m.fanout(a)
	a.release()
	m.fanout(b)
	b.release()
	if n := a.ref.Refs(); n != 0 {
		t.Fatalf("displaced frame refs = %d, want 0 (its buffer never came back)", n)
	}
	// Teardown with a frame still queued: closeTaps must release it.
	m.closeTaps()
	if n := b.ref.Refs(); n != 0 {
		t.Fatalf("refs = %d after closeTaps, want 0 - a pending buffer was abandoned", n)
	}
	_ = src.Close()
}
