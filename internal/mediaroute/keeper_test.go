package mediaroute

import (
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/senderkeeper"
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
