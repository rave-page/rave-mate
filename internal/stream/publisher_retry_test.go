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

// fakeStreamServer scripts /streams create + ingest/heartbeat auth per token generation.
type fakeStreamServer struct {
	mu        sync.Mutex
	creates   int
	ingests   int
	ingestErr map[string]int // publish token → status to return (0/absent = 200)
	hbErr     map[string]int
	srv       *httptest.Server
}

func newFakeStreamServer(t *testing.T) *fakeStreamServer {
	t.Helper()
	f := &fakeStreamServer{ingestErr: map[string]int{}, hbErr: map[string]int{}}
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

// Test401Reacquire proves an expired publish token (401) triggers ONE CreateStream
// re-acquire with the stored user token and publishing continues on the fresh
// stream+token instead of retrying 401 forever.
func Test401Reacquire(t *testing.T) {
	f := newFakeStreamServer(t)
	p := startPaused(t, f)
	f.mu.Lock()
	f.ingestErr["tok1"] = 401 // first token expired
	f.mu.Unlock()

	p.enqueueOne(t)
	p.flush(context.Background()) // 401 → re-acquire (create #2)

	f.mu.Lock()
	creates := f.creates
	f.mu.Unlock()
	if creates != 2 {
		t.Fatalf("creates=%d, want 2 (one re-acquire)", creates)
	}
	st := p.Status()
	if st.StreamID != "s2" {
		t.Errorf("stream not swapped: %+v", st)
	}
	if !st.IsLive {
		t.Error("must stay live across re-acquire")
	}

	// fresh token works: next flush lands
	p.enqueueOne(t)
	p.flush(context.Background())
	if st := p.Status(); !st.LastFlushOK {
		t.Errorf("flush with re-acquired token failed: %+v", st)
	}

	// a second 401 within reacquireMin must NOT create again (rate limit)
	f.mu.Lock()
	f.ingestErr["tok2"] = 401
	f.mu.Unlock()
	p.enqueueOne(t)
	p.flush(context.Background())
	f.mu.Lock()
	creates = f.creates
	f.mu.Unlock()
	if creates != 2 {
		t.Errorf("creates=%d, want 2 (re-acquire rate-limited)", creates)
	}
	p.mu.Lock()
	b := p.backoff
	p.mu.Unlock()
	if b == 0 {
		t.Error("rate-limited 401 must arm a backoff (no full-cadence 401 loop)")
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

// TestHeartbeat401Reacquire proves the heartbeat arm re-acquires too.
func TestHeartbeat401Reacquire(t *testing.T) {
	f := newFakeStreamServer(t)
	p := startPaused(t, f)
	f.mu.Lock()
	f.hbErr["tok1"] = 401
	f.mu.Unlock()

	p.heartbeat(context.Background())
	f.mu.Lock()
	creates := f.creates
	f.mu.Unlock()
	if creates != 2 {
		t.Errorf("creates=%d, want 2 (heartbeat 401 re-acquires)", creates)
	}
	if st := p.Status(); st.StreamID != "s2" {
		t.Errorf("stream not swapped: %+v", st)
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
