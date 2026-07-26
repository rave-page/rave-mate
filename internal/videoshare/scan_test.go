package videoshare

import (
	"testing"
	"time"
)

func newTestScan(now *time.Time, fetch func() []SenderInfo) *senderScan {
	return &senderScan{ttl: scanTTL, now: func() time.Time { return *now }, fetch: fetch}
}

// TestScanCacheFoldsOneScanPass: the mediaroute pattern (list once + one size per name) must cost
// exactly ONE backend call - that churn was 1+2N Spout objects every 2 s.
func TestScanCacheFoldsOneScanPass(t *testing.T) {
	now := time.Unix(1000, 0)
	calls := 0
	s := newTestScan(&now, func() []SenderInfo {
		calls++
		return []SenderInfo{{Name: "OBS", W: 1920, H: 1080}, {Name: "TD", W: 1280, H: 720}}
	})
	names := s.all()
	if len(names) != 2 {
		t.Fatalf("all() = %v", names)
	}
	for _, n := range []string{"OBS", "TD"} {
		if _, _, ok := s.size(n); !ok {
			t.Fatalf("size(%q) not found", n)
		}
	}
	if calls != 1 {
		t.Fatalf("one scan pass hit the backend %d times, want 1", calls)
	}
	if s.hits != 1 {
		t.Fatalf("hits = %d, want 1", s.hits)
	}
}

// TestScanCacheExpires: past the TTL the next read refreshes (a vanished sender must disappear).
func TestScanCacheExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	list := []SenderInfo{{Name: "OBS", W: 1920, H: 1080}}
	calls := 0
	s := newTestScan(&now, func() []SenderInfo { calls++; return list })
	_ = s.all()
	now = now.Add(scanTTL - time.Millisecond)
	_ = s.all()
	if calls != 1 {
		t.Fatalf("inside TTL hit the backend %d times, want 1", calls)
	}
	now = now.Add(2 * time.Millisecond)
	list = nil
	if got := s.all(); len(got) != 0 {
		t.Fatalf("after TTL all() = %v, want empty", got)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

// TestScanCacheMissForcesRefresh: a sender that appeared inside the TTL window resolves on the
// spot (a route opens off this call - a stale miss would fail the offer).
func TestScanCacheMissForcesRefresh(t *testing.T) {
	now := time.Unix(1000, 0)
	list := []SenderInfo{{Name: "OBS", W: 1920, H: 1080}}
	calls := 0
	s := newTestScan(&now, func() []SenderInfo { calls++; return list })
	_ = s.all()
	list = append(list, SenderInfo{Name: "Resolume", W: 3840, H: 2160})
	w, h, ok := s.size("Resolume")
	if !ok || w != 3840 || h != 2160 {
		t.Fatalf("size(Resolume) = %d×%d ok=%v, want 3840×2160 true", w, h, ok)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (one forced refresh on the miss)", calls)
	}
	// Still-absent name: one refresh, then a clean not-found (never an unbounded retry loop).
	before := calls
	if _, _, ok := s.size("nope"); ok {
		t.Fatal("unknown sender reported found")
	}
	if calls != before+1 {
		t.Fatalf("miss on an absent sender cost %d calls, want 1", calls-before)
	}
}

// TestScanCacheZeroSizeNotOK: a registered sender without dimensions is not usable as a source.
func TestScanCacheZeroSizeNotOK(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newTestScan(&now, func() []SenderInfo { return []SenderInfo{{Name: "starting", W: 0, H: 0}} })
	if _, _, ok := s.size("starting"); ok {
		t.Fatal("0×0 sender must not report ok")
	}
}

// TestScanCacheBound: the cache never keeps more than scanMaxKeep entries.
func TestScanCacheBound(t *testing.T) {
	now := time.Unix(1000, 0)
	big := make([]SenderInfo, scanMaxKeep+10)
	for i := range big {
		big[i] = SenderInfo{Name: string(rune('a'+i%26)) + string(rune('0'+i/26)), W: 64, H: 64}
	}
	s := newTestScan(&now, func() []SenderInfo { return big })
	if got := len(s.all()); got != scanMaxKeep {
		t.Fatalf("cached %d entries, want the %d bound", got, scanMaxKeep)
	}
}
