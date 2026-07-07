package webcam

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/eventbus"
	"rave.page/mate/internal/logbus"
)

// fakeSeams wires a Manager with in-memory device/route/prop backends.
type fakeSeams struct {
	mu       sync.Mutex
	started  []capDesc
	stops    atomic.Int32
	setCalls []string // "prop=value/auto"
}

func newFakeManager(t *testing.T, bus *eventbus.Bus, self string, cfg *config.WebcamFeature) (*Manager, *fakeSeams) {
	t.Helper()
	fs := &fakeSeams{}
	m := New(logbus.New(64), bus, self, self+"-host", func() config.WebcamFeature { return *cfg })
	m.enumerate = func(context.Context) ([]DeviceInfo, error) {
		return []DeviceInfo{{Name: "FakeCam", Modes: []Mode{{W: 1280, H: 720, FPS: 30}}}}, nil
	}
	m.getProps = func(string) ([]PropState, error) {
		return []PropState{{ID: "zoom", Label: "Zoom", Min: 100, Max: 400, Step: 10, Default: 100, Value: 100, CanAuto: false}}, nil
	}
	m.setProp = func(dev, prop string, v int32, auto bool) error {
		fs.mu.Lock()
		fs.setCalls = append(fs.setCalls, prop)
		fs.mu.Unlock()
		return nil
	}
	m.openRoute = func(_ context.Context, d capDesc) (func(), func() capStats, string, error) {
		fs.mu.Lock()
		fs.started = append(fs.started, d)
		fs.mu.Unlock()
		return func() { fs.stops.Add(1) }, func() capStats { return capStats{SrcUp: true} }, SenderName(d.Device), nil
	}
	return m, fs
}

// linkBuses cross-wires two eventbus instances (in-memory transport).
func linkBuses(a, b *eventbus.Bus, aID, bID string) {
	a.SetTransport(func(p []byte) { b.Inbound(aID, p) }, func(_ string, p []byte) { b.Inbound(aID, p) })
	b.SetTransport(func(p []byte) { a.Inbound(bID, p) }, func(_ string, p []byte) { a.Inbound(bID, p) })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// TestBusCtlRoundTrip drives instance B's camera from instance A over the linked bus:
// start (with mode), set a property, stop - and A sees B's status broadcasts.
func TestBusCtlRoundTrip(t *testing.T) {
	log := logbus.New(64)
	busA := eventbus.New(log, "node-a")
	busB := eventbus.New(log, "node-b")
	linkBuses(busA, busB, "node-a", "node-b")

	cfgA := &config.WebcamFeature{Enabled: true}
	cfgB := &config.WebcamFeature{Enabled: true, Device: "FakeCam"}
	mgrA, _ := newFakeManager(t, busA, "node-a", cfgA)
	mgrB, fsB := newFakeManager(t, busB, "node-b", cfgB)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgrA.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := mgrB.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgrA.Stop()
	defer mgrB.Stop()

	// A → B: start with an explicit mode.
	if err := mgrA.Command(Cmd{Target: "node-b", Action: ActStart, Device: "FakeCam", W: 640, H: 480, FPS: 15}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B capture start", func() bool {
		fsB.mu.Lock()
		defer fsB.mu.Unlock()
		return len(fsB.started) == 1
	})
	fsB.mu.Lock()
	d := fsB.started[0]
	fsB.mu.Unlock()
	if d != (capDesc{Device: "FakeCam", W: 640, H: 480, FPS: 15}) {
		t.Fatalf("started with %+v", d)
	}

	// B's status broadcast reaches A (remote instance visible, running, sender named).
	waitFor(t, "A sees B running", func() bool {
		for _, in := range mgrA.Instances() {
			if in.Node == "node-b" && !in.Local && in.Running && in.Sender == SenderName("FakeCam") {
				return true
			}
		}
		return false
	})

	// A → B: set a UVC property.
	if err := mgrA.Command(Cmd{Target: "node-b", Action: ActSet, Prop: "zoom", Value: 250}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B prop set", func() bool {
		fsB.mu.Lock()
		defer fsB.mu.Unlock()
		return len(fsB.setCalls) == 1 && fsB.setCalls[0] == "zoom"
	})

	// A → B: stop.
	if err := mgrA.Command(Cmd{Target: "node-b", Action: ActStop}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "B capture stop", func() bool { return fsB.stops.Load() >= 1 })

	// Targeting isolation: the node-b-directed start never touched A.
	if got := len(mgrA.Instances()); got < 1 || !mgrA.Instances()[0].Local {
		t.Fatalf("A instances malformed: %d", got)
	}
	if st := mgrA.Instances()[0]; st.Running {
		t.Fatal("A started a capture it was never asked to")
	}
}

// TestConfigGating: a disabled feature refuses to start and the capability is dropped on Stop.
func TestConfigGating(t *testing.T) {
	log := logbus.New(64)
	bus := eventbus.New(log, "node-x")
	cfg := &config.WebcamFeature{Enabled: false, Device: "FakeCam"}
	mgr, fs := newFakeManager(t, bus, "node-x", cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	if err := mgr.StartCamera("", 0, 0, 0); err == nil {
		t.Fatal("StartCamera succeeded with the feature off")
	}
	if n := func() int { fs.mu.Lock(); defer fs.mu.Unlock(); return len(fs.started) }(); n != 0 {
		t.Fatalf("capture opened despite gate: %d", n)
	}

	// Enable + start locally (no device arg → config device, mode from enumeration).
	cfg.Enabled = true
	waitFor(t, "device enumeration", func() bool {
		return len(mgr.Instances()[0].Devices) == 1
	})
	if err := mgr.StartCamera("", 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	fs.mu.Lock()
	d := fs.started[0]
	fs.mu.Unlock()
	if d != (capDesc{Device: "FakeCam", W: 1280, H: 720, FPS: 30}) {
		t.Fatalf("mode fallback wrong: %+v", d)
	}
	mgr.StopCamera()
	if fs.stops.Load() != 1 {
		t.Fatal("stop not delivered")
	}
}

// TestCapabilityAdvertised: Start advertises media.cam, Stop retracts it.
func TestCapabilityAdvertised(t *testing.T) {
	log := logbus.New(64)
	bus := eventbus.New(log, "node-x")
	cfg := &config.WebcamFeature{Enabled: true}
	mgr, _ := newFakeManager(t, bus, "node-x", cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !bus.HasLocal(CapCam) {
		t.Fatal("capability not advertised")
	}
	mgr.Stop()
	if bus.HasLocal(CapCam) {
		t.Fatal("capability not retracted")
	}
}

// TestPickMode covers the size/rate fallback chain.
func TestPickMode(t *testing.T) {
	devs := []DeviceInfo{{Name: "Cam", Modes: []Mode{{W: 1920, H: 1080, FPS: 30}}}}
	if w, h, fps := pickMode(devs, "Cam", 0); w != 1920 || h != 1080 || fps != 30 {
		t.Fatalf("mode pick: %dx%d@%d", w, h, fps)
	}
	if w, h, fps := pickMode(devs, "Cam", 15); fps != 15 || w != 1920 || h != 1080 {
		t.Fatalf("explicit fps lost: %dx%d@%d", w, h, fps)
	}
	if w, h, fps := pickMode(nil, "Unknown", 0); w != defaultW || h != defaultH || fps != defaultFPS {
		t.Fatalf("default fallback: %dx%d@%d", w, h, fps)
	}

	// The C920 bug: modes are largest-first, top one is a 2304x1536@2 stills mode. Auto-pick must
	// NOT choose it - prefer exact 720p when offered.
	c920 := []DeviceInfo{{Name: "C920", Modes: []Mode{
		{W: 2304, H: 1536, FPS: 2}, {W: 1920, H: 1080, FPS: 30}, {W: 1280, H: 720, FPS: 30},
	}}}
	if w, h, fps := pickMode(c920, "C920", 0); w != 1280 || h != 720 || fps != 30 {
		t.Fatalf("C920 auto-pick should be 720p30, got %dx%d@%d", w, h, fps)
	}
	// No 720p offered: pick the biggest mode within the cap at >=24fps, never the oversized stills one.
	noHD := []DeviceInfo{{Name: "X", Modes: []Mode{
		{W: 2304, H: 1536, FPS: 2}, {W: 1920, H: 1080, FPS: 30}, {W: 640, H: 480, FPS: 30},
	}}}
	if w, h, _ := pickMode(noHD, "X", 0); w != 1920 || h != 1080 {
		t.Fatalf("expected 1080p within cap, got %dx%d", w, h)
	}
}

// TestResolveCaptureMode: explicit within-cap sizes are honored; oversized ones are clamped.
func TestResolveCaptureMode(t *testing.T) {
	devs := []DeviceInfo{{Name: "C920", Modes: []Mode{
		{W: 2304, H: 1536, FPS: 2}, {W: 1280, H: 720, FPS: 30},
	}}}
	if w, h, fps, clamped := resolveCaptureMode(devs, "C920", 1600, 900, 24); clamped || w != 1600 || h != 900 || fps != 24 {
		t.Fatalf("in-cap explicit should pass through, got %dx%d@%d clamped=%v", w, h, fps, clamped)
	}
	if w, h, _, clamped := resolveCaptureMode(devs, "C920", 2304, 1536, 0); !clamped || w != 1280 || h != 720 {
		t.Fatalf("oversized should clamp to 720p, got %dx%d clamped=%v", w, h, clamped)
	}
	if _, _, _, clamped := resolveCaptureMode(devs, "C920", 0, 0, 0); clamped {
		t.Fatal("empty request must auto-pick, not report clamped")
	}
}
