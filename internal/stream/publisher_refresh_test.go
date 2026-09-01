package stream

import (
	"context"
	"testing"
	"time"
)

// refreshDelay returns 70-80% of the token's remaining lifetime (jittered).
func TestRefreshDelayBounds(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	expStr := now.Add(10 * time.Minute).Format(time.RFC3339)
	for i := 0; i < 1000; i++ {
		d := refreshDelay(expStr, now)
		if d < 7*time.Minute || d >= 8*time.Minute {
			t.Fatalf("refreshDelay=%v out of [7m,8m) for a 10m TTL", d)
		}
	}
}

// Empty / unparseable / already-expired expiry → 0 (caller skips scheduling;
// the 401-grace path covers expiry).
func TestRefreshDelayEdgeCases(t *testing.T) {
	now := time.Now()
	if d := refreshDelay("", now); d != 0 {
		t.Errorf("empty expiry: got %v want 0", d)
	}
	if d := refreshDelay("not-a-time", now); d != 0 {
		t.Errorf("bad expiry: got %v want 0", d)
	}
	if d := refreshDelay(now.Add(-time.Minute).Format(time.RFC3339), now); d != 0 {
		t.Errorf("already-expired: got %v want 0", d)
	}
}

// A tiny positive TTL floors at 1s (never busy-loop). A 1s lifetime yields
// 0.70-0.80s pre-floor, so it clamps up to exactly 1s.
func TestRefreshDelayFloor(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	d := refreshDelay(now.Add(time.Second).Format(time.RFC3339), now)
	if d != time.Second {
		t.Fatalf("floor: got %v want 1s", d)
	}
}

// A proactive refresh (driven directly, as the run-loop timer would) swaps the
// token in place and keeps stream_id.
func TestProactiveRefreshSwapsTokenInPlace(t *testing.T) {
	f := newFakeStreamServer(t)
	p := startPaused(t, f)
	before := p.Status().StreamID // s1
	p.mu.Lock()
	beforeTok := p.pubToken
	p.mu.Unlock()

	if err := p.refreshToken(context.Background()); err != nil {
		t.Fatalf("refreshToken: %v", err)
	}
	f.mu.Lock()
	creates, refreshes := f.creates, f.refreshes
	f.mu.Unlock()
	if creates != 1 || refreshes != 1 {
		t.Errorf("proactive refresh: creates=%d refreshes=%d want 1/1", creates, refreshes)
	}
	st := p.Status()
	if st.StreamID != before {
		t.Errorf("stream_id changed: %s -> %s", before, st.StreamID)
	}
	p.mu.Lock()
	afterTok, afterExp := p.pubToken, p.pubExp
	p.mu.Unlock()
	if afterTok == beforeTok || afterTok == "" {
		t.Errorf("token not swapped in place: before=%q after=%q", beforeTok, afterTok)
	}
	if afterExp == "" {
		t.Error("pubExp not updated after refresh")
	}
}

// nextRefreshDelay is 0 once the publisher is not live (timer stays disarmed).
func TestNextRefreshDelayNotLive(t *testing.T) {
	f := newFakeStreamServer(t)
	p := startPaused(t, f)
	if d := p.nextRefreshDelay(); d <= 0 {
		t.Fatalf("live publisher with far-future expiry should arm: got %v", d)
	}
	if _, err := p.End(context.Background()); err != nil {
		t.Fatalf("End: %v", err)
	}
	if d := p.nextRefreshDelay(); d != 0 {
		t.Errorf("ended publisher must not arm refresh: got %v", d)
	}
}
