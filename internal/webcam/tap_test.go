package webcam

import (
	"context"
	"io"
	"sync"
	"testing"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
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
	f1 := &medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA, Payload: []byte{1}}
	f2 := &medialink.Frame{Kind: medialink.KindVideo, Codec: medialink.CodecNRGBA, Payload: []byte{2}}
	m.fanout(f1)
	m.fanout(f2)
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
