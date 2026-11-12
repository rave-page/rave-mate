package app

import (
	"context"
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
	f.recvs = append(f.recvs, mediaroute.Receive{Session: "sess", Peer: peer, SourceID: src, Active: false})
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

// A died-but-not-GC'd receive (present in Receives() with Active=false) must NOT count as live - the
// resume layer re-offers it (the 33s bug was it counting mere membership as active).
func TestReconcileReoffersWhenReceiveLingersButDead(t *testing.T) {
	f := &fakeRecv{
		avail: []mediaroute.RemoteSource{srcAvail("p1", "cam")},
		recvs: []mediaroute.Receive{{Session: "old", Peer: "p1", SourceID: "cam", Active: false}}, // lingering dead
	}
	rr := newRR(f, config.RememberedReceive{Peer: "p1", SourceID: "cam"})
	rr.reconcile()
	if f.starts != 1 {
		t.Fatalf("lingering dead receive blocked re-offer: starts=%d want 1", f.starts)
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
