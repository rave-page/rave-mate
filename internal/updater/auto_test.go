package updater

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/shared/selfupdate"
)

type autoFakeFeed struct {
	mu        sync.Mutex
	rel       *selfupdate.Release
	dlErr     error
	probeErr  error
	downloads int
	installs  int
	discards  int
}

func (f *autoFakeFeed) Enabled() bool { return true }
func (f *autoFakeFeed) Available(context.Context) (*selfupdate.Release, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rel, f.rel != nil && f.rel.Build > 100, nil
}
func (f *autoFakeFeed) Download(context.Context, *selfupdate.Release, func(int64, int64)) (AutoStaged, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloads++
	if f.dlErr != nil {
		return nil, f.dlErr
	}
	return &autoFakeStaged{f: f}, nil
}

type autoFakeStaged struct{ f *autoFakeFeed }

func (s *autoFakeStaged) Probe(context.Context, int) error { return s.f.probeErr }
func (s *autoFakeStaged) Install() error {
	s.f.mu.Lock()
	s.f.installs++
	s.f.mu.Unlock()
	return nil
}
func (s *autoFakeStaged) Discard() {
	s.f.mu.Lock()
	s.f.discards++
	s.f.mu.Unlock()
}

type autoFakeGuard struct {
	bad    map[int]bool
	marked [][2]int
}

func (g *autoFakeGuard) IsBad(b int) bool   { return g.bad[b] }
func (g *autoFakeGuard) PinBad(b int) error { g.bad[b] = true; return nil }
func (g *autoFakeGuard) Mark(from, to int) error {
	g.marked = append(g.marked, [2]int{from, to})
	return nil
}
func (g *autoFakeGuard) Clear() error { return nil }

func countLogs(b *logbus.Bus, sub string) int {
	n := 0
	for _, e := range b.Snapshot() {
		if e.Source == "update" && strings.Contains(e.Msg, sub) {
			n++
		}
	}
	return n
}

func newTestAuto(feed *autoFakeFeed, g *autoFakeGuard, busy func() string, restarted *int) (*Auto, *logbus.Bus) {
	log := logbus.New(100)
	return NewAuto(AutoConfig{
		Feed: feed, Guard: g, Current: 100, Log: log, Busy: busy,
		Restart:  func(*selfupdate.Release) { *restarted++ },
		IdleFor:  time.Millisecond,
		IdlePoll: time.Millisecond,
	}), log
}

// Unwritable exe: one log line + status flag, no re-download until the feed build changes.
func TestAutoNotWritableLogsOnceNoRetry(t *testing.T) {
	feed := &autoFakeFeed{
		rel:   &selfupdate.Release{Build: 101},
		dlErr: &selfupdate.NotWritableError{Path: "/opt/rave-mate/rave-mate", Err: errors.New("permission denied")},
	}
	restarted := 0
	a, log := newTestAuto(feed, &autoFakeGuard{bad: map[int]bool{}}, nil, &restarted)
	for range 5 {
		a.Cycle(context.Background())
	}
	if feed.downloads != 1 {
		t.Fatalf("downloads = %d, want 1 (no retry for the same build)", feed.downloads)
	}
	if n := countLogs(log, "not writable by this user"); n != 1 {
		t.Fatalf("not-writable log lines = %d, want 1", n)
	}
	st := a.Status()
	if st.State != "notwritable" || st.Build != 101 || !strings.Contains(st.String(), "/opt/rave-mate/rave-mate") {
		t.Fatalf("status = %+v (%s)", st, st)
	}
	feed.rel = &selfupdate.Release{Build: 102} // new nightly: retried once, logged once more
	a.Cycle(context.Background())
	a.Cycle(context.Background())
	if feed.downloads != 2 || countLogs(log, "not writable by this user") != 2 {
		t.Fatalf("after build change: downloads=%d logs=%d, want 2/2", feed.downloads, countLogs(log, "not writable by this user"))
	}
	if restarted != 0 || feed.installs != 0 {
		t.Fatal("unwritable install must never swap or restart")
	}
}

// A live media route defers the swap; it lands once the route ends.
func TestAutoDefersWhileRouteActive(t *testing.T) {
	feed := &autoFakeFeed{rel: &selfupdate.Release{Build: 101}}
	g := &autoFakeGuard{bad: map[int]bool{}}
	var mu sync.Mutex
	busy := "1 media route(s) active (send peerA)"
	restarted := 0
	a, _ := newTestAuto(feed, g, func() string { mu.Lock(); defer mu.Unlock(); return busy }, &restarted)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	a.Cycle(ctx) // busy for the whole window → ctx ends → discarded, nothing installed
	cancel()
	if feed.installs != 0 || restarted != 0 {
		t.Fatal("installed while a route was active")
	}
	if feed.discards != 1 {
		t.Fatalf("discards = %d, want 1 (staged payload dropped when the wait ends)", feed.discards)
	}

	done := make(chan CycleResult, 1)
	go func() { done <- a.Cycle(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	if st := a.Status(); st.State != "deferred" || !strings.Contains(st.Detail, "media route") {
		t.Fatalf("status while busy = %+v, want deferred(media route ...)", st)
	}
	mu.Lock()
	busy = ""
	mu.Unlock()
	select {
	case r := <-done:
		if r != CycleApplied {
			t.Fatalf("cycle result = %v, want applied", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("swap never happened after the route ended")
	}
	if feed.installs != 1 || restarted != 1 || len(g.marked) != 1 || g.marked[0] != [2]int{100, 101} {
		t.Fatalf("installs=%d restarted=%d marked=%v", feed.installs, restarted, g.marked)
	}
}

// A new binary that fails its start probe is pinned bad and never re-downloaded.
func TestAutoProbeFailurePinsBad(t *testing.T) {
	feed := &autoFakeFeed{rel: &selfupdate.Release{Build: 101}, probeErr: errors.New("exit status 127")}
	g := &autoFakeGuard{bad: map[int]bool{}}
	restarted := 0
	a, _ := newTestAuto(feed, g, nil, &restarted)
	a.Cycle(context.Background())
	a.Cycle(context.Background())
	if !g.bad[101] || feed.downloads != 1 || feed.installs != 0 || feed.discards != 1 {
		t.Fatalf("bad=%v downloads=%d installs=%d discards=%d", g.bad, feed.downloads, feed.installs, feed.discards)
	}
	if st := a.Status(); st.State != "pinnedbad" {
		t.Fatalf("status = %+v, want pinnedbad", st)
	}
}

// A payload failing sha256 verification is refused and not re-downloaded for that build.
func TestAutoChecksumMismatchSkipsBuild(t *testing.T) {
	feed := &autoFakeFeed{rel: &selfupdate.Release{Build: 101}, dlErr: errors.New("checksum mismatch: got aa want bb")}
	restarted := 0
	a, log := newTestAuto(feed, &autoFakeGuard{bad: map[int]bool{}}, nil, &restarted)
	for range 3 {
		a.Cycle(context.Background())
	}
	if feed.downloads != 1 || feed.installs != 0 || countLogs(log, "failed verification") != 1 {
		t.Fatalf("downloads=%d installs=%d logs=%d", feed.downloads, feed.installs, countLogs(log, "failed verification"))
	}
}

// A build pinned bad by an earlier rollback is never downloaded.
func TestAutoSkipsPinnedBad(t *testing.T) {
	feed := &autoFakeFeed{rel: &selfupdate.Release{Build: 101}}
	restarted := 0
	a, _ := newTestAuto(feed, &autoFakeGuard{bad: map[int]bool{101: true}}, nil, &restarted)
	a.Cycle(context.Background())
	if feed.downloads != 0 || a.Status().State != "pinnedbad" {
		t.Fatalf("downloads=%d status=%+v", feed.downloads, a.Status())
	}
}
