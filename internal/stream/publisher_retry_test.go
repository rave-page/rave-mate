package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/api"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/session"
)

// fakeStreamServer scripts /streams create + ingest/heartbeat/token-refresh auth
// per token generation.
type fakeStreamServer struct {
	mu         sync.Mutex
	creates    int
	ingests    int
	refreshes  int
	ingestErr  map[string]int // publish token → status to return (0/absent = 200)
	hbErr      map[string]int
	refreshErr map[string]int // publish token → status for token-refresh (0/absent = mint)
	srv        *httptest.Server
}

func newFakeStreamServer(t *testing.T) *fakeStreamServer {
	t.Helper()
	f := &fakeStreamServer{ingestErr: map[string]int{}, hbErr: map[string]int{}, refreshErr: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/streams", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.creates++
		n := f.creates
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{
			"stream_id": fmt.Sprintf("s%d", n), "publish_token": fmt.Sprintf("tok%d", n),
			"publish_token_expires_at": "2099-01-01T00:00:00Z", "started_at": "2026-01-01T00:00:00Z",
		})
	})
	mux.HandleFunc("/streams/", func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/ingest"):
			f.ingests++
			if code := f.ingestErr[tok]; code != 0 {
				w.WriteHeader(code)
				return
			}
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			if code := f.hbErr[tok]; code != 0 {
				w.WriteHeader(code)
				return
			}
		case strings.HasSuffix(r.URL.Path, "/token-refresh"):
			f.refreshes++
			if code := f.refreshErr[tok]; code != 0 {
				w.WriteHeader(code)
				return
			}
			// Mint a fresh token for the SAME stream_id (parsed from the path).
			id := strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/token-refresh"), "/streams/")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"stream_id":                id,
				"publish_token":            fmt.Sprintf("rtok%d", f.refreshes),
				"publish_token_expires_at": "2099-01-01T00:00:00Z",
			})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// startPaused starts the publisher against the fake server and stops the run loop so
// flush/heartbeat can be driven deterministically by the test.
func startPaused(t *testing.T, f *fakeStreamServer) *Publisher {
	t.Helper()
	bus := logbus.New(64)
	apiC := api.New(f.srv.URL, bus)
	p := New(bus, apiC, func() (<-chan session.Update, func()) {
		return make(chan session.Update), func() {}
	})
	st, err := p.Start(context.Background(), StartArgs{Title: "t", UserToken: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsLive || st.StreamID != "s1" {
		t.Fatalf("unexpected start status %+v", st)
	}
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()
	cancel()
	p.wg.Wait() // run loop gone; p stays live
	return p
}

func (p *Publisher) enqueueOne(t *testing.T) {
	t.Helper()
	p.enqueue(session.Update{Type: "deckUpdate", Scope: session.Scope{Kind: session.ScopeDeck, ID: "A"}})
}

// Test401RefreshInPlace proves an expired publish token (401) triggers an IN-PLACE
// token refresh that KEEPS stream_id (the set-fragmentation fix) - NOT a CreateStream
// re-acquire. A second 401 within reacquireMin is rate-limited and arms a backoff.
func Test401RefreshInPlace(t *testing.T) {
	f := newFakeStreamServer(t)
	p := startPaused(t, f)
	f.mu.Lock()
	f.ingestErr["tok1"] = 401 // first token expired
	f.mu.Unlock()

	p.enqueueOne(t)
	p.flush(context.Background()) // 401 → refresh in place (refresh #1, NO create)

	f.mu.Lock()
	creates, refreshes := f.creates, f.refreshes
	f.mu.Unlock()
	if creates != 1 {
		t.Fatalf("creates=%d, want 1 (refresh must NOT re-create the stream)", creates)
	}
	if refreshes != 1 {
		t.Fatalf("refreshes=%d, want 1", refreshes)
	}
	st := p.Status()
	if st.StreamID != "s1" {
		t.Errorf("stream_id changed on refresh: %+v (want s1)", st)
	}
	if !st.IsLive {
		t.Error("must stay live across refresh")
	}

	// refreshed token works: next flush lands (rtok1 not scripted to fail)
	p.enqueueOne(t)
	p.flush(context.Background())
	if st := p.Status(); !st.LastFlushOK {
		t.Errorf("flush with refreshed token failed: %+v", st)
	}

	// a second 401 within reacquireMin must NOT refresh/create again (rate limit)
	f.mu.Lock()
	f.ingestErr["rtok1"] = 401
	f.mu.Unlock()
	p.enqueueOne(t)
	p.flush(context.Background())
	f.mu.Lock()
	creates, refreshes = f.creates, f.refreshes
	f.mu.Unlock()
	if creates != 1 || refreshes != 1 {
		t.Errorf("recovery not rate-limited: creates=%d refreshes=%d (want 1/1)", creates, refreshes)
	}
	p.mu.Lock()
	b := p.backoff
	p.mu.Unlock()
	if b == 0 {
		t.Error("rate-limited 401 must arm a backoff (no full-cadence 401 loop)")
	}
}

// Test401RefreshFailFallbackReacquire proves that when the refresh endpoint itself
// fails (token expired beyond the server grace window, or an older backend without
// the route), the 401 path falls back to a full CreateStream re-acquire (new
// stream_id) so publishing still recovers.
func Test401RefreshFailFallbackReacquire(t *testing.T) {
	f := newFakeStreamServer(t)
	p := startPaused(t, f)
	f.mu.Lock()
	f.ingestErr["tok1"] = 401  // token expired
	f.refreshErr["tok1"] = 401 // refresh rejects it too (beyond grace)
	f.mu.Unlock()

	p.enqueueOne(t)
	p.flush(context.Background()) // 401 → refresh fails → reacquire (create #2)

	f.mu.Lock()
	creates, refreshes := f.creates, f.refreshes
	f.mu.Unlock()
	if refreshes != 1 {
		t.Fatalf("refreshes=%d, want 1 (one refresh attempt before fallback)", refreshes)
	}
	if creates != 2 {
		t.Fatalf("creates=%d, want 2 (fallback re-acquire)", creates)
	}
	if st := p.Status(); st.StreamID != "s2" {
		t.Errorf("stream not swapped on fallback: %+v", st)
	}
}

// Test429Backoff proves a 429 arms an exponential pause during which flush + heartbeat
// send NOTHING, and that the pause doubles up to the cap and clears on success.
func Test429Backoff(t *testing.T) {
	f := newFakeStreamServer(t)
	p := startPaused(t, f)
	f.mu.Lock()
	f.ingestErr["tok1"] = 429
	f.mu.Unlock()

	p.enqueueOne(t)
	p.flush(context.Background())
	p.mu.Lock()
	b1, ra := p.backoff, p.retryAfter
	p.mu.Unlock()
	if b1 != retryBackoffBase {
		t.Fatalf("backoff=%v, want base %v", b1, retryBackoffBase)
	}
	if !ra.After(time.Now()) {
		t.Fatal("retryAfter not armed")
	}

	// while paused: no requests at all
	f.mu.Lock()
	before := f.ingests
	f.mu.Unlock()
	p.enqueueOne(t)
	p.flush(context.Background())
	p.heartbeat(context.Background())
	f.mu.Lock()
	after := f.ingests
	f.mu.Unlock()
	if after != before {
		t.Errorf("requests sent during backoff: %d", after-before)
	}

	// expire the pause → next failure doubles
	p.mu.Lock()
	p.retryAfter = time.Now().Add(-time.Millisecond)
	p.mu.Unlock()
	p.flush(context.Background())
	p.mu.Lock()
	b2 := p.backoff
	p.mu.Unlock()
	if b2 != 2*retryBackoffBase {
		t.Errorf("backoff=%v, want %v (doubled)", b2, 2*retryBackoffBase)
	}

	// cap: repeated failures never exceed retryBackoffMax
	for i := 0; i < 20; i++ {
		p.mu.Lock()
		p.retryAfter = time.Now().Add(-time.Millisecond)
		p.mu.Unlock()
		p.enqueueOne(t)
		p.flush(context.Background())
	}
	p.mu.Lock()
	bCap := p.backoff
	p.mu.Unlock()
	if bCap != retryBackoffMax {
		t.Errorf("backoff=%v, want cap %v", bCap, retryBackoffMax)
	}

	// success clears the pause
	f.mu.Lock()
	delete(f.ingestErr, "tok1")
	f.mu.Unlock()
	p.mu.Lock()
	p.retryAfter = time.Now().Add(-time.Millisecond)
	p.mu.Unlock()
	p.enqueueOne(t)
	p.flush(context.Background())
	p.mu.Lock()
	b3, ra3 := p.backoff, p.retryAfter
	p.mu.Unlock()
	if b3 != 0 || !ra3.IsZero() {
		t.Errorf("backoff not cleared on success: %v %v", b3, ra3)
	}
}

// TestHeartbeat401Refresh proves the heartbeat arm refreshes in place too
// (keeps stream_id), not re-creates.
func TestHeartbeat401Refresh(t *testing.T) {
	f := newFakeStreamServer(t)
	p := startPaused(t, f)
	f.mu.Lock()
	f.hbErr["tok1"] = 401
	f.mu.Unlock()

	p.heartbeat(context.Background())
	f.mu.Lock()
	creates, refreshes := f.creates, f.refreshes
	f.mu.Unlock()
	if creates != 1 || refreshes != 1 {
		t.Errorf("heartbeat 401: creates=%d refreshes=%d, want 1/1 (refresh in place)", creates, refreshes)
	}
	if st := p.Status(); st.StreamID != "s1" {
		t.Errorf("stream_id changed: %+v (want s1)", st)
	}
}

// TestPendingCap proves the queue drops oldest at pendingMax (bounded during backoff).
func TestPendingCap(t *testing.T) {
	f := newFakeStreamServer(t)
	p := startPaused(t, f)
	p.mu.Lock()
	p.retryAfter = time.Now().Add(time.Hour) // block flushes (incl. batch-size overflow)
	p.mu.Unlock()
	for i := 0; i < pendingMax+50; i++ {
		p.enqueueOne(t)
	}
	p.mu.Lock()
	n := len(p.pending)
	first, last := p.pending[0].Seq, p.pending[n-1].Seq
	p.mu.Unlock()
	if n != pendingMax {
		t.Errorf("pending=%d, want cap %d", n, pendingMax)
	}
	if last != uint64(pendingMax+50) || first != last-uint64(pendingMax)+1 {
		t.Errorf("drop-oldest violated: first=%d last=%d", first, last)
	}
}
