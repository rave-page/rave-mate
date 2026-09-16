package automation

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rave.page/mate/internal/governor"
)

// coordCtrl records which jobs started, in order.
type coordCtrl struct {
	mu      sync.Mutex
	started []string
}

func (cc *coordCtrl) record(id string) {
	cc.mu.Lock()
	cc.started = append(cc.started, id)
	cc.mu.Unlock()
}
func (cc *coordCtrl) count(id string) int {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	n := 0
	for _, s := range cc.started {
		if s == id {
			n++
		}
	}
	return n
}
func (cc *coordCtrl) total() int { cc.mu.Lock(); defer cc.mu.Unlock(); return len(cc.started) }

// blockJob builds a job that records its start, then blocks on rel (nil = returns at once). Distinct
// default dir per id (no path conflict) unless dirs are given.
func blockJob(cc *coordCtrl, id string, heavy, coalescable bool, rel chan struct{}, dirs ...string) *coordJob {
	if len(dirs) == 0 {
		dirs = []string{id}
	}
	return &coordJob{
		autoID: id, label: id, dirs: dirs, heavy: heavy, coalescable: coalescable, ctx: context.Background(),
		run: func(context.Context) []Run {
			cc.record(id)
			if rel != nil {
				<-rel
			}
			return nil
		},
	}
}

func waitCount(t *testing.T, fn func() int, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for count %d, got %d", want, fn())
}

// stayAt asserts a count does not change over a short window (a negative: proves something did NOT run).
func stayAt(t *testing.T, fn func() int, want int) {
	t.Helper()
	time.Sleep(80 * time.Millisecond)
	if got := fn(); got != want {
		t.Fatalf("expected count to stay %d, got %d", want, got)
	}
}

// TestCoordHeavySlotSerializes: with MaxHeavyRuns=1, two heavy chains run one at a time; a light
// chain runs alongside.
func TestCoordHeavySlotSerializes(t *testing.T) {
	cc := &coordCtrl{}
	c := newCoordinator(1, nil)
	defer c.stop()
	relA := make(chan struct{})
	rA := c.submit(blockJob(cc, "A", true, false, relA))
	rB := c.submit(blockJob(cc, "B", true, false, nil))  // heavy → waits for the slot
	rC := c.submit(blockJob(cc, "C", false, false, nil)) // light → runs alongside
	if rA.queued || !rB.queued || rB.reason != reasonHeavy || rC.queued {
		t.Fatalf("dispositions: A=%+v B=%+v C=%+v", rA, rB, rC)
	}
	waitCount(t, func() int { return cc.count("C") }, 1)
	stayAt(t, func() int { return cc.count("B") }, 0)
	if cc.count("A") != 1 {
		t.Fatalf("A should be running: %v", cc.started)
	}
	close(relA)
	waitCount(t, func() int { return cc.count("B") }, 1)
}

// TestCoordPathOverlapSerializes: two automations whose watch/output dirs nest never run at once.
func TestCoordPathOverlapSerializes(t *testing.T) {
	cc := &coordCtrl{}
	c := newCoordinator(4, nil)
	defer c.stop()
	relA := make(chan struct{})
	// OS-native paths: a Windows literal is one opaque name on Linux (ubuntu CI caught this).
	rec := filepath.Join(t.TempDir(), "rec")
	rA := c.submit(blockJob(cc, "A", false, false, relA, rec))
	rB := c.submit(blockJob(cc, "B", false, false, nil, filepath.Join(rec, "sub"))) // nested under A → waits
	if rA.queued || !rB.queued || rB.reason != reasonPath {
		t.Fatalf("A=%+v B=%+v", rA, rB)
	}
	waitCount(t, func() int { return cc.count("A") }, 1)
	stayAt(t, func() int { return cc.count("B") }, 0)
	close(relA)
	waitCount(t, func() int { return cc.count("B") }, 1)
}

// TestCoordSelfCoalesces: a sweep of an already-running automation becomes ONE pending follow-up;
// further sweeps fold into it. Exactly two runs happen (the current + one follow-up), never three.
func TestCoordSelfCoalesces(t *testing.T) {
	cc := &coordCtrl{}
	c := newCoordinator(4, nil)
	defer c.stop()
	rel1 := make(chan struct{})
	r1 := c.submit(blockJob(cc, "A", false, true, rel1))
	waitCount(t, func() int { return cc.count("A") }, 1)
	r2 := c.submit(blockJob(cc, "A", false, true, nil)) // the single pending follow-up
	r3 := c.submit(blockJob(cc, "A", false, true, nil)) // folds into the follow-up
	if r1.queued || !r2.queued || !r3.coalesced {
		t.Fatalf("r1=%+v r2=%+v r3=%+v", r1, r2, r3)
	}
	st := c.Status()
	if len(st.Running) != 1 || len(st.Queued) != 1 || !st.Queued[0].Coalesced {
		t.Fatalf("status: %+v", st)
	}
	close(rel1)
	waitCount(t, func() int { return cc.count("A") }, 2)
	stayAt(t, func() int { return cc.count("A") }, 2) // never a third
}

// TestCoordGovernorDefersHeavy: a heavy run defers while a stream is live (reusing the governor
// gate); light work runs; it starts once the stream ends.
func TestCoordGovernorDefersHeavy(t *testing.T) {
	cc := &coordCtrl{}
	c := newCoordinator(4, nil)
	defer c.stop()
	governor.SetStreaming(true)
	defer governor.SetStreaming(false)
	rel := make(chan struct{})
	rH := c.submit(blockJob(cc, "H", true, false, rel))
	if !rH.queued || rH.reason != reasonStreaming {
		t.Fatalf("heavy must defer while streaming: %+v", rH)
	}
	stayAt(t, func() int { return cc.count("H") }, 0)
	c.submit(blockJob(cc, "L", false, false, nil)) // light still runs while streaming
	waitCount(t, func() int { return cc.count("L") }, 1)
	governor.SetStreaming(false)
	c.wake()
	waitCount(t, func() int { return cc.count("H") }, 1)
	close(rel)
}

// TestCoordQueueCapBounded: the pending queue is bounded; an over-cap submit is folded, never grows.
func TestCoordQueueCapBounded(t *testing.T) {
	cc := &coordCtrl{}
	c := newCoordinator(1, nil)
	c.queueCap = 2
	defer c.stop()
	relA := make(chan struct{})
	c.submit(blockJob(cc, "A", true, false, relA)) // running (heavy slot)
	r1 := c.submit(blockJob(cc, "B", true, false, nil))
	r2 := c.submit(blockJob(cc, "C", true, false, nil))
	r3 := c.submit(blockJob(cc, "D", true, false, nil)) // over cap
	if !r1.queued || !r2.queued || !r3.coalesced {
		t.Fatalf("r1=%+v r2=%+v r3=%+v", r1, r2, r3)
	}
	close(relA)
	waitCount(t, func() int { return cc.count("B") + cc.count("C") }, 2)
	stayAt(t, func() int { return cc.count("D") }, 0)
}

// TestCoordCancelDropsQueued: a queued run whose context is cancelled is dropped (never runs) and
// its waiter released.
func TestCoordCancelDropsQueued(t *testing.T) {
	cc := &coordCtrl{}
	c := newCoordinator(1, nil)
	defer c.stop()
	relA := make(chan struct{})
	c.submit(blockJob(cc, "A", true, false, relA))
	ctx, cancel := context.WithCancel(context.Background())
	j := &coordJob{autoID: "B", label: "B", dirs: []string{"B"}, heavy: true, ctx: ctx,
		run: func(context.Context) []Run { cc.record("B"); return nil }}
	r := c.submit(j)
	if !r.queued {
		t.Fatalf("B should be queued: %+v", r)
	}
	cancel()
	c.wake()
	select {
	case <-r.done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled queued job's done not closed")
	}
	close(relA)
	stayAt(t, func() int { return cc.count("B") }, 0)
}

// TestCoordBumpsOnChange: state changes call the version bump.
func TestCoordBumpsOnChange(t *testing.T) {
	var n int32
	c := newCoordinator(1, func() { atomic.AddInt32(&n, 1) })
	defer c.stop()
	rel := make(chan struct{})
	c.submit(blockJob(&coordCtrl{}, "A", false, false, rel))
	close(rel)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&n) >= 2 { // at least start + finish
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected ≥2 version bumps, got %d", atomic.LoadInt32(&n))
}

// TestCoordInteractiveHoldsSlot: an attended interactive run holds a slot so an unattended sweep of
// the same automation defers to it (coalesced follow-up), then runs when it finishes.
func TestCoordInteractiveHoldsSlot(t *testing.T) {
	cc := &coordCtrl{}
	c := newCoordinator(1, nil)
	defer c.stop()
	rec := filepath.Join(t.TempDir(), "rec")
	a := Automation{ID: "A", Label: "A", WatchDir: rec, Actions: []Action{{Type: ActionTranscode, PresetID: "x"}}}
	ij := c.trackInteractive(a, filepath.Join(rec, "live.wav"))
	// a background sweep of the same automation coalesces behind the interactive run
	r := c.submit(&coordJob{autoID: "A", label: "A", dirs: chainDirs(a), heavy: true, coalescable: true,
		ctx: context.Background(), run: func(context.Context) []Run { cc.record("A"); return nil }})
	if !r.queued {
		t.Fatalf("sweep should wait for the interactive run: %+v", r)
	}
	stayAt(t, func() int { return cc.count("A") }, 0)
	c.done(ij)
	waitCount(t, func() int { return cc.count("A") }, 1)
}
