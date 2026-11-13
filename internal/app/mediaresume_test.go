package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/medialink"
	"rave.page/mate/internal/mediaroute"
	"rave.page/mate/internal/testcard"
)

// fakeRecv is a minimal ReceiveControl for the resume-layer tests.
type fakeRecv struct {
	avail  []mediaroute.RemoteSource
	recvs  []mediaroute.Receive
	starts int
	held   bool // StartReceive answers a pre-offer VRAM hold
}

func (f *fakeRecv) Start(context.Context)                         {}
func (f *fakeRecv) RemoteVideoSources() []mediaroute.RemoteSource { return f.avail }
func (f *fakeRecv) Receives() []mediaroute.Receive                { return f.recvs }
func (f *fakeRecv) StopReceive(string)                            {}
func (f *fakeRecv) FrameShot(string, int, int, int, [4]int) (mediaroute.FrameShot, error) {
	return mediaroute.FrameShot{}, nil
}
func (f *fakeRecv) Testcard(string, int, int, int) (testcard.Report, error) {
	return testcard.Report{}, nil
}
func (f *fakeRecv) StartReceive(peer, src string) (string, error) {
	f.starts++
	if f.held {
		return "", errors.New(`mediaroute: held: "VRC": VRAM budget refuses the receive - no offer sent |hold={"sender":"rave-mate link VRC","since":"2026-11-13T20:00:00Z","freeMB":140,"needMB":226,"requiredMB":290,"policy":"balanced","reason":"insufficient VRAM","nextRetry":"2026-11-13T20:00:02Z"}`)
	}
	// Models an in-flight offer: a receive entry exists immediately (Since now), not yet live/up.
	f.recvs = append(f.recvs, mediaroute.Receive{Session: "sess", Peer: peer, SourceID: src, Since: time.Now()})
	return "sess", nil
}

func newRR(f *fakeRecv, remembered ...config.RememberedReceive) *rememberingReceives {
	cfg := &config.Config{}
	cfg.Features.MediaLink.RememberedReceives = remembered
	return &rememberingReceives{ReceiveControl: f, log: logbus.New(64), cfg: cfg, save: func() {}}
}

func srcAvail(peer, id string) mediaroute.RemoteSource {
	return mediaroute.RemoteSource{Peer: peer, Desc: medialink.SourceDesc{ID: id, Name: id, Kind: medialink.KindVideo, Width: 1920, Height: 1080}}
}

// A remembered source that is advertised but has NO live route is re-offered immediately (no 30s wait).
func TestReconcileReoffersDeadSourceImmediately(t *testing.T) {
	f := &fakeRecv{avail: []mediaroute.RemoteSource{srcAvail("p1", "cam")}}
	rr := newRR(f, config.RememberedReceive{Peer: "p1", SourceID: "cam"})
	rr.reconcile()
	if f.starts != 1 {
		t.Fatalf("dead source not re-offered immediately: starts=%d want 1", f.starts)
	}
	// Route came up (Active): no further re-offer while live.
	f.recvs[0].Active = true
	rr.reconcile()
	if f.starts != 1 {
		t.Fatalf("re-offered a LIVE route: starts=%d want 1", f.starts)
	}
}

// A route that came up then DIED (EverUp, not Active, not GC'd) is re-offered fast - not counted as
// live (the 33s bug) and not mistaken for an in-flight offer (it already came up once).
func TestReconcileReoffersDeadRouteThatCameUp(t *testing.T) {
	f := &fakeRecv{
		avail: []mediaroute.RemoteSource{srcAvail("p1", "cam")},
		recvs: []mediaroute.Receive{{Session: "old", Peer: "p1", SourceID: "cam",
			Active: false, EverUp: true, Since: time.Now()}}, // died (came up once), still recent
	}
	rr := newRR(f, config.RememberedReceive{Peer: "p1", SourceID: "cam"})
	rr.reconcile()
	if f.starts != 1 {
		t.Fatalf("died route not re-offered: starts=%d want 1", f.starts)
	}
}

// Core start-up race fix: an IN-FLIGHT offer (receive present, not yet live, within the establish
// grace) blocks a SECOND offer - a burst of near-simultaneous triggers yields exactly one route.
func TestReconcileNoSecondOfferWhileInFlight(t *testing.T) {
	f := &fakeRecv{avail: []mediaroute.RemoteSource{srcAvail("p1", "cam")}}
	rr := newRR(f, config.RememberedReceive{Peer: "p1", SourceID: "cam"})
	rr.reconcile() // first trigger: offers (starts=1), leaves an in-flight receive
	if f.starts != 1 {
		t.Fatalf("first trigger did not offer: starts=%d", f.starts)
	}
	for i := 0; i < 5; i++ { // more triggers while still establishing
		rr.reconcile()
	}
	if f.starts != 1 {
		t.Fatalf("in-flight offer did not dedup a second offer: starts=%d want 1 (double-offer race)", f.starts)
	}
	// Once up, still exactly one (no re-offer while live).
	f.recvs[0].Active = true
	rr.reconcile()
	if f.starts != 1 {
		t.Fatalf("re-offered a live route: starts=%d", f.starts)
	}
}

// A remembered source NOT yet advertised is not re-offered and burns no backoff step.
func TestReconcileWaitsForAvailability(t *testing.T) {
	f := &fakeRecv{} // nothing advertised
	rr := newRR(f, config.RememberedReceive{Peer: "p1", SourceID: "cam"})
	rr.reconcile()
	if f.starts != 0 {
		t.Fatalf("re-offered an unavailable source: starts=%d want 0", f.starts)
	}
	b := rr.backoffFor("p1\x00cam")
	if !b.next.IsZero() {
		t.Fatal("unavailable source advanced the backoff (should wait, not back off)")
	}
}

// Backoff doubles from the base and is capped; a stable live route resets it.
func TestReoffBackoffBoundedAndResets(t *testing.T) {
	rr := &rememberingReceives{}
	b := rr.backoffFor("k")
	now := time.Unix(1000, 0)
	var last time.Duration
	for i := 0; i < 12; i++ {
		rr.armBackoff(b, now)
		if b.interval > reoffCap {
			t.Fatalf("backoff interval %v exceeded cap %v", b.interval, reoffCap)
		}
		last = b.interval
	}
	if last != reoffCap {
		t.Fatalf("backoff did not reach the cap: %v want %v", last, reoffCap)
	}
	// A route live < reoffResetStable does not reset; >= does.
	b2 := rr.backoffFor("k2")
	rr.armBackoff(b2, now)
	rr.noteLive(b2, now)                         // liveSince = now
	rr.noteLive(b2, now.Add(reoffResetStable-1)) // not stable long enough
	if b2.interval == reoffBase {
		t.Fatal("backoff reset before the route was stable")
	}
	rr.noteLive(b2, now.Add(reoffResetStable+1)) // stable → reset
	if b2.interval != reoffBase || !b2.next.IsZero() {
		t.Fatalf("stable live route did not reset backoff: interval=%v next=%v", b2.interval, b2.next)
	}
}

// A VRAM-held source (pre-offer refusal) is re-checked on the short fixed cadence, never pushed into
// the exponential backoff (it sent nothing to the source PC), and offers as soon as it is admitted.
func TestReconcileHeldRechecksWithoutBackoff(t *testing.T) {
	f := &fakeRecv{avail: []mediaroute.RemoteSource{srcAvail("p1", "vrc")}, held: true}
	rr := newRR(f, config.RememberedReceive{Peer: "p1", SourceID: "vrc"})
	rr.reconcile()
	b := rr.backoff["p1\x00vrc"]
	if f.starts != 1 || b.interval != reoffBase || time.Until(b.next) > reoffHeldRecheck {
		t.Fatalf("held: starts=%d interval=%v next in %v", f.starts, b.interval, time.Until(b.next))
	}
	if hs := rr.Holds(); len(hs) != 1 || hs[0].FreeMB != 140 || hs[0].RequiredMB != 290 || hs[0].Policy != "balanced" {
		t.Fatalf("hold numbers not surfaced daemon-side: %+v", hs)
	}
	rr.reconcile() // inside the recheck window: no call at all
	if f.starts != 1 {
		t.Fatalf("held recheck hammered: starts=%d", f.starts)
	}
	b.next = time.Time{}
	f.held = false
	rr.reconcile()
	if f.starts != 2 || len(f.recvs) != 1 {
		t.Fatalf("admitted source not offered: starts=%d recvs=%d", f.starts, len(f.recvs))
	}
	if hs := rr.Holds(); len(hs) != 0 {
		t.Fatalf("admitted source must clear its hold: %+v", hs)
	}
}

// A remembered source deep in backoff (peer link was down) is re-offered as soon as its peer link
// comes back up - a link flap must not cost up to reoffCap of black screen.
func TestReconcileResumesOnPeerReconnect(t *testing.T) {
	f := &fakeRecv{avail: []mediaroute.RemoteSource{srcAvail("p1", "vrc")}}
	rr := newRR(f, config.RememberedReceive{Peer: "p1", SourceID: "vrc"})
	up := true
	rr.peerUp = func(string) bool { return up }
	rr.reconcile() // initial offer
	b := rr.backoff["p1\x00vrc"]
	b.next, b.interval = time.Now().Add(time.Hour), reoffCap // deep backoff from repeated failures
	f.recvs = nil                                            // route died with the link
	up = false
	rr.reconcile()
	if f.starts != 1 {
		t.Fatalf("offered while backing off: %d", f.starts)
	}
	up = true // link flap recovered
	rr.reconcile()
	if f.starts != 2 {
		t.Fatalf("not re-offered on peer reconnect: starts=%d", f.starts)
	}
}
