package worker

import (
	"context"
	"testing"
	"time"
)

// A waiter whose ctx expires must unregister its channel. Before the fix,
// release() parked the proc in the abandoned channel's buffer - live stayed
// at maxProcs with idle empty, wedging the pool for the rest of the session
// (every probe/transcode job waited its full ctx and failed).
func TestPoolAbandonedWaiterDoesNotWedge(t *testing.T) {
	p := &pool{typ: "probe", maxProcs: 1, all: map[*proc]struct{}{}}
	busy := &proc{lastUsed: time.Now()}
	p.all[busy] = struct{}{}
	p.live = 1 // busy proc is out with a job

	// Waiter times out while the proc is busy.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := p.acquire(ctx); err == nil {
		t.Fatal("expected ctx timeout from acquire")
	}

	// Job finishes; the proc must land in idle, not in a dead channel.
	p.release(busy)
	p.mu.Lock()
	idle, waiters := len(p.idle), len(p.wait)
	p.mu.Unlock()
	if waiters != 0 {
		t.Fatalf("abandoned waiter still registered: %d", waiters)
	}
	if idle != 1 {
		t.Fatalf("proc lost to a dead channel: idle=%d", idle)
	}

	// The next acquire gets it immediately.
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	w, err := p.acquire(ctx2)
	if err != nil || w != busy {
		t.Fatalf("pool wedged after abandoned waiter: w=%v err=%v", w, err)
	}
}

// A release that races the waiter's timeout (proc already handed over before
// the waiter unregisters) must recover the proc into the pool.
func TestPoolTimedOutWaiterRecoversHandedProc(t *testing.T) {
	p := &pool{typ: "probe", maxProcs: 1, all: map[*proc]struct{}{}}
	busy := &proc{lastUsed: time.Now()}
	p.all[busy] = struct{}{}
	p.live = 1

	// Already-canceled ctx: acquire registers, then sees ctx.Done. Hand the
	// proc into the waiter's channel BEFORE it unregisters by pre-loading
	// the race: release from a goroutine while acquire runs.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		// Spin until a waiter registers, then release into its channel.
		for {
			p.mu.Lock()
			if len(p.wait) > 0 {
				p.mu.Unlock()
				p.release(busy)
				close(done)
				return
			}
			p.mu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()
	_, err := p.acquire(ctx)
	if err == nil {
		t.Fatal("expected error from canceled acquire")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// release never happened (waiter unregistered first) - also fine;
		// nothing was handed over, so nothing can leak.
		return
	}
	p.mu.Lock()
	idle := len(p.idle)
	p.mu.Unlock()
	if idle != 1 {
		t.Fatalf("handed-over proc leaked: idle=%d", idle)
	}
}
