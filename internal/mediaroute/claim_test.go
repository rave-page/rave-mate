package mediaroute

import (
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
)

func claimManager(sf *sinkFactory, cfg *config.MediaLinkFeature, allow *bool, now *time.Time) *Manager {
	m := New(Options{
		Log: logbus.New(32), Router: newFakeRouter(),
		Cfg:      func() config.MediaLinkFeature { return *cfg },
		OpenSink: sf.open,
		AdmitSender: func(w, h int) Admission {
			return Admission{Allow: *allow, Reason: "test", Policy: "balanced"}
		},
	})
	m.now = func() time.Time { return *now }
	return m
}

// A remembered receive with a known nominal geometry gets its destination at start - before any peer
// or advert - kept with no idle TTL, and the later route reuses it (one sender, one Resolume follow).
func TestRememberedDestinationClaimedAtStartAndKept(t *testing.T) {
	sf := &sinkFactory{shared: true}
	cfg := &config.MediaLinkFeature{RememberedReceives: []config.RememberedReceive{
		{Peer: "sus", SourceID: "spout:VRC", Name: "VRC", W: 3840, H: 2160, FPS: 60},
		{Peer: "sus", SourceID: "spout:cam", Name: "cam"}, // geometry unknown: nothing to claim yet
	}}
	allow, now := true, time.Unix(5_000_000, 0)
	m := claimManager(sf, cfg, &allow, &now)
	m.claimRemembered()
	if sf.opens != 1 || sf.sinks[0].w != 3840 || sf.sinks[0].h != 2160 {
		t.Fatalf("want one nominal 4K claim, got opens=%d", sf.opens)
	}
	now = now.Add(3 * parkTTL) // far past the idle TTL: a claimed destination stays
	m.reapParked()
	m.claimRemembered()
	if sf.opens != 1 || sf.sinks[0].closed != 0 {
		t.Fatalf("claimed destination must survive the TTL without re-create: opens=%d closed=%d", sf.opens, sf.sinks[0].closed)
	}
	// The route arrives later (maybe at a capped coded size): it reuses the claimed sender.
	s, err := m.openReceiveSink(linkPrefix+"VRC", 1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	if sf.opens != 1 {
		t.Fatalf("route must reuse the claimed destination, opens=%d", sf.opens)
	}
	_ = s.Close() // park again, still claimed
	// Forgotten (explicit stop elsewhere removed it): the TTL applies again.
	cfg.RememberedReceives = nil
	now = now.Add(2 * parkTTL)
	m.reapParked()
	if sf.sinks[0].closed != 1 {
		t.Fatalf("forgotten destination must be released after the TTL, closed=%d", sf.sinks[0].closed)
	}
}

// A claim refused by the VRAM budget allocates nothing and is retried on a bounded cadence.
func TestClaimRefusedRetriesBounded(t *testing.T) {
	sf := &sinkFactory{shared: true}
	cfg := &config.MediaLinkFeature{RememberedReceives: []config.RememberedReceive{
		{Peer: "sus", SourceID: "spout:VRC", Name: "VRC", W: 3840, H: 2160, FPS: 60}}}
	allow, now := false, time.Unix(6_000_000, 0)
	m := claimManager(sf, cfg, &allow, &now)
	m.claimRemembered()
	allow = true
	now = now.Add(2 * time.Second) // scan tick inside claimRetry: no new try
	m.claimRemembered()
	if sf.opens != 0 {
		t.Fatalf("refused claim must not allocate or re-try inside claimRetry: opens=%d", sf.opens)
	}
	now = now.Add(claimRetry)
	m.claimRemembered()
	if sf.opens != 1 {
		t.Fatalf("claim must be retried after claimRetry: opens=%d", sf.opens)
	}
}

// Cap eviction prefers unclaimed parked senders.
func TestParkEvictionSparesClaimed(t *testing.T) {
	sf := &sinkFactory{shared: true}
	cfg := &config.MediaLinkFeature{RememberedReceives: []config.RememberedReceive{
		{Peer: "sus", SourceID: "spout:VRC", Name: "VRC", W: 64, H: 48}}}
	allow, now := true, time.Unix(7_000_000, 0)
	m := claimManager(sf, cfg, &allow, &now)
	m.claimRemembered() // claimed, oldest
	for i := 0; i < parkCap; i++ {
		now = now.Add(time.Second)
		inner, _ := sf.open("x", 8, 4)
		m.parkInner(linkPrefix+"other"+string(rune('a'+i)), inner, 8, 4, nil, false)
	}
	m.mu.Lock()
	_, kept := m.parked[linkPrefix+"VRC"]
	m.mu.Unlock()
	if !kept {
		t.Fatal("eviction must spare the claimed destination while unclaimed ones exist")
	}
}
