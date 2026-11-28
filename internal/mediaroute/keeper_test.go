package mediaroute

import (
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/senderkeeper"
	"rave.page/mate/internal/videoshare"
)

func keeperCfg() *config.MediaLinkFeature {
	on := true
	return &config.MediaLinkFeature{SenderKeeper: &on, RememberedReceives: []config.RememberedReceive{
		{Peer: "sus", SourceID: "spout:VRC", Name: "VRC", W: 3840, H: 2160, FPS: 60}}}
}

// keeperManager: a media plane whose destinations live in a (fake) keeper; no SpoutLibrary seam.
func keeperManager(f *senderkeeper.FakeServer, cfg *config.MediaLinkFeature, allow *bool) *Manager {
	sup := senderkeeper.NewSupervisor(senderkeeper.Options{Dial: f.Dial})
	m := New(Options{Log: logbus.New(32), Router: newFakeRouter(),
		Cfg:    func() config.MediaLinkFeature { return *cfg },
		Keeper: sup,
		AdmitSender: func(w, h int) Admission {
			return Admission{Allow: *allow, Reason: "test", Policy: "balanced", FreeMB: 140, RequiredMB: 290}
		},
		AdmitReceive: func(w, h, fps int, newSender bool) Admission {
			return Admission{Allow: *allow || !newSender, Reason: "test", Policy: "balanced", FreeMB: 140, RequiredMB: 290}
		},
		NewSharedSender: nil,
	})
	return m
}

// Daemon/media-child restart on a FULL card: the destination survives in the keeper, the new process
// ADOPTS it without admission (same handle, no texture creation, nothing new in Resolume), and the
// remembered receive is offered (kept destination => only the decoder is gated).
func TestRestartOnFullCardAdoptsKeeperDestination(t *testing.T) {
	f := senderkeeper.NewFakeServer()
	cfg := keeperCfg()
	allow := true
	m1 := keeperManager(f, cfg, &allow)
	m1.claimRemembered()
	if f.Creates != 1 {
		t.Fatalf("claim must create the keeper destination once, creates=%d", f.Creates)
	}
	h1 := f.Senders()[0].Handle
	m1.destroyAllParked() // process exit: detach only

	allow = false // Resolume filled the card meanwhile
	m2 := keeperManager(f, cfg, &allow)
	m2.now = func() time.Time { return time.Unix(9_000_000, 0) }
	m2.claimRemembered()
	if f.Creates != 1 || len(f.Senders()) != 1 || f.Senders()[0].Handle != h1 {
		t.Fatalf("restart must adopt: creates=%d senders=%+v", f.Creates, f.Senders())
	}
	fr := m2.router.(*fakeRouter)
	fr.adverts["sus"] = advertWith(medialink.SourceDesc{ID: "spout:VRC", Name: "VRC", Kind: medialink.KindVideo, Width: 3840, Height: 2160, FPS: 60})
	if _, err := m2.StartReceive("sus", "spout:VRC"); err != nil {
		t.Fatalf("adopted destination must not be held on a full card: %v", err)
	}
	sink, err := m2.openReceiveSink(linkPrefix+"VRC", 3840, 2160)
	if err != nil {
		t.Fatal(err)
	}
	zc, ok := sink.(medialink.ZeroCopySink)
	if !ok {
		t.Fatal("keeper sink must expose the shared texture")
	}
	if h, fmtv, w, hh, _, ok := zc.SharedTexture(); !ok || h != h1 || fmtv != 87 || w != 3840 || hh != 2160 {
		t.Fatalf("adopted texture %#x %d %dx%d ok=%v, want %#x", h, fmtv, w, hh, ok, h1)
	}
	_ = sink.Close() // park: keeper clear, sender stays
	if f.Clears == 0 || len(f.Senders()) != 1 {
		t.Fatalf("park must clear in the keeper, not release: clears=%d senders=%d", f.Clears, len(f.Senders()))
	}
}

// keeperFallbackManager: keeper-hosted destinations with an in-process shared-sender fallback.
func keeperFallbackManager(f *senderkeeper.FakeServer, cfg *config.MediaLinkFeature) (*Manager, *fakeSharedSender) {
	ss := &fakeSharedSender{handle: 0xBEEF}
	m := New(Options{Log: logbus.New(64), Router: newFakeRouter(),
		Cfg:             func() config.MediaLinkFeature { return *cfg },
		Keeper:          senderkeeper.NewSupervisor(senderkeeper.Options{Dial: f.Dial}),
		AdmitSender:     func(w, h int) Admission { return Admission{Allow: true, Reason: "test"} },
		AdmitReceive:    func(w, h, fps int, newSender bool) Admission { return Admission{Allow: true, Reason: "test"} },
		NewSharedSender: func(string, int, int) (videoshare.SharedSender, error) { return ss, nil },
	})
	return m, ss
}

func sinkHandle(t *testing.T, s medialink.Sink) uint64 {
	t.Helper()
	zc, ok := s.(medialink.ZeroCopySink)
	if !ok {
		t.Fatal("sink exposes no destination texture")
	}
	h, _, _, _, _, ok := zc.SharedTexture()
	if !ok {
		t.Fatal("sink has no destination texture")
	}
	return h
}

// Keeper crash mid-route: the loss is detected on the scan cadence, the live route is closed (the
// resume layer re-offers), the lost destination is never parked or reused, and the reopen - keeper
// gone - lands on the in-process sender instead of a dead route.
func TestKeeperLossMidRouteFallsBackInProcess(t *testing.T) {
	f := senderkeeper.NewFakeServer()
	cfg := keeperCfg()
	m, _ := keeperFallbackManager(f, cfg)
	fr := m.router.(*fakeRouter)
	fr.adverts["sus"] = advertWith(medialink.SourceDesc{ID: "spout:VRC", Name: "VRC", Kind: medialink.KindVideo, Width: 1920, Height: 1080, FPS: 60})
	sess, err := m.StartReceive("sus", "spout:VRC")
	if err != nil {
		t.Fatal(err)
	}
	name := linkPrefix + "VRC"
	sink, err := m.openReceiveSink(name, 1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	if h := sinkHandle(t, sink); h == 0xBEEF {
		t.Fatal("keeper reachable: destination must be keeper-hosted")
	}
	m.checkKeeper()
	if len(fr.closed) != 0 {
		t.Fatalf("healthy keeper destination must not close its route: %v", fr.closed)
	}

	f.Kill()
	m.checkKeeper()
	if len(fr.closed) != 1 || fr.closed[0] != sess {
		t.Fatalf("lost destination must close its route for re-offer, closed=%v", fr.closed)
	}
	_ = sink.Close() // the router tears the route down
	m.mu.Lock()
	_, parked := m.parked[name]
	m.mu.Unlock()
	if parked {
		t.Fatal("a lost keeper destination must not be parked")
	}
	again, err := m.openReceiveSink(name, 1920, 1080)
	if err != nil {
		t.Fatalf("reopen after keeper loss must not fail the route: %v", err)
	}
	if h := sinkHandle(t, again); h != 0xBEEF {
		t.Fatalf("reopen must use the in-process sender, handle %#x", h)
	}
}

// Keeper sidecar missing / never started: the destination opens in-process (no dead route).
func TestKeeperUnavailableAtOpenUsesInProcess(t *testing.T) {
	f := senderkeeper.NewFakeServer()
	f.Kill()
	m, _ := keeperFallbackManager(f, keeperCfg())
	s, err := m.openSpoutSink(linkPrefix+"VRC", 1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	if h := sinkHandle(t, s); h != 0xBEEF {
		t.Fatalf("unreachable keeper must fall back in-process, handle %#x", h)
	}
}

// A parked keeper destination the keeper lost before checkKeeper ran is not reused by the reopen.
func TestParkedKeeperDestinationLostIsNotReused(t *testing.T) {
	f := senderkeeper.NewFakeServer()
	cfg := keeperCfg()
	m, _ := keeperFallbackManager(f, cfg)
	m.claimRemembered()
	if f.Creates != 1 {
		t.Fatalf("claim must create the keeper destination, creates=%d", f.Creates)
	}
	f.Kill()
	s, err := m.openReceiveSink(linkPrefix+"VRC", 3840, 2160)
	if err != nil {
		t.Fatal(err)
	}
	if h := sinkHandle(t, s); h != 0xBEEF {
		t.Fatalf("lost parked destination must not be reused, handle %#x", h)
	}
}

// Explicit stop releases the keeper destination (the only path that does).
func TestExplicitStopReleasesKeeperDestination(t *testing.T) {
	f := senderkeeper.NewFakeServer()
	cfg := keeperCfg()
	allow := true
	m := keeperManager(f, cfg, &allow)
	fr := m.router.(*fakeRouter)
	fr.adverts["sus"] = advertWith(medialink.SourceDesc{ID: "spout:VRC", Name: "VRC", Kind: medialink.KindVideo, Width: 3840, Height: 2160, FPS: 60})
	sess, err := m.StartReceive("sus", "spout:VRC")
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.openReceiveSink(linkPrefix+"VRC", 3840, 2160)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close() // parked
	m.StopReceive(sess)
	if len(f.Senders()) != 0 {
		t.Fatalf("explicit stop must release the keeper destination: %+v", f.Senders())
	}
}
