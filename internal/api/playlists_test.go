package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rave.page/mate/internal/logbus"
)

func mkPlaylistItems(n int) []PlaylistItemIn {
	items := make([]PlaylistItemIn, n)
	for i := range items {
		items[i] = PlaylistItemIn{Title: fmt.Sprintf("t%d", i)}
	}
	return items
}

// capturedChunk decodes one PUT body: raw keys (append/expect_count presence) + item titles.
type capturedChunk struct {
	keys   map[string]any
	titles []string
}

func decodeChunk(t *testing.T, b []byte) capturedChunk {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("chunk body decode: %v (%s)", err, b)
	}
	c := capturedChunk{keys: m}
	its, _ := m["items"].([]any)
	for _, it := range its {
		o, _ := it.(map[string]any)
		s, _ := o["title"].(string)
		c.titles = append(c.titles, s)
	}
	return c
}

// TestPutPlaylistItemsChunked: 2500 items => replace(1000) + append(1000,ec=1000) +
// append(500,ec=2000); asserts append/expect_count presence, values, and item order.
func TestPutPlaylistItemsChunked(t *testing.T) {
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/playlists/pl_x/items" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"id":"pl_x","title":"chunk%d"}`, len(bodies))))
	}))
	defer srv.Close()

	c := New(srv.URL, logbus.New(8))
	out, err := c.PutPlaylistItems(context.Background(), "tok", "pl_x", mkPlaylistItems(2500))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if len(bodies) != 3 {
		t.Fatalf("requests = %d, want 3", len(bodies))
	}
	if out.Title != "chunk3" {
		t.Errorf("returned playlist = %q, want last chunk (chunk3)", out.Title)
	}

	c0 := decodeChunk(t, bodies[0])
	if _, ok := c0.keys["append"]; ok {
		t.Errorf("chunk 1 must not carry append: %s", bodies[0])
	}
	if _, ok := c0.keys["expect_count"]; ok {
		t.Errorf("chunk 1 must not carry expect_count: %s", bodies[0])
	}
	if len(c0.titles) != 1000 {
		t.Errorf("chunk 1 items = %d, want 1000", len(c0.titles))
	}

	// chunks 2 and 3: append=true, expect_count = cumulative prior count.
	for i, wantEC := range []float64{1000, 2000} {
		ci := decodeChunk(t, bodies[i+1])
		if ci.keys["append"] != true {
			t.Errorf("chunk %d append = %v, want true", i+2, ci.keys["append"])
		}
		if ec, _ := ci.keys["expect_count"].(float64); ec != wantEC {
			t.Errorf("chunk %d expect_count = %v, want %v", i+2, ci.keys["expect_count"], wantEC)
		}
	}
	wantN := []int{1000, 1000, 500}
	// item order must be contiguous t0..t2499 across the chunks.
	next := 0
	for i, b := range bodies {
		ci := decodeChunk(t, b)
		if len(ci.titles) != wantN[i] {
			t.Errorf("chunk %d items = %d, want %d", i+1, len(ci.titles), wantN[i])
		}
		for _, title := range ci.titles {
			if want := fmt.Sprintf("t%d", next); title != want {
				t.Fatalf("item order broke: got %q want %q", title, want)
			}
			next++
		}
	}
	if next != 2500 {
		t.Errorf("total items across chunks = %d, want 2500", next)
	}
}

// TestPutPlaylistItemsSingleChunkWireCompat: ≤1000 items => exactly 1 request whose JSON
// carries NO append/expect_count keys (an un-upgraded server keeps working).
func TestPutPlaylistItemsSingleChunkWireCompat(t *testing.T) {
	for _, n := range []int{0, 1, 1000} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			var bodies [][]byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				bodies = append(bodies, b)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"pl_x","title":"T"}`))
			}))
			defer srv.Close()

			c := New(srv.URL, logbus.New(8))
			if _, err := c.PutPlaylistItems(context.Background(), "tok", "pl_x", mkPlaylistItems(n)); err != nil {
				t.Fatalf("put: %v", err)
			}
			if len(bodies) != 1 {
				t.Fatalf("requests = %d, want 1", len(bodies))
			}
			s := string(bodies[0])
			if strings.Contains(s, "append") || strings.Contains(s, "expect_count") {
				t.Errorf("legacy body must omit append/expect_count, got %s", s)
			}
		})
	}
}

// TestPutPlaylistItemsConflictAborts: a 409 on chunk 2 aborts the push with an error naming
// the chunk/range + concurrent modification; chunk 3 is never sent.
func TestPutPlaylistItemsConflictAborts(t *testing.T) {
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		if reqs == 2 {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"expect_count mismatch"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"pl_x","title":"T"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, logbus.New(8))
	_, err := c.PutPlaylistItems(context.Background(), "tok", "pl_x", mkPlaylistItems(2500))
	if err == nil {
		t.Fatal("want error on 409, got nil")
	}
	if reqs != 2 {
		t.Errorf("requests = %d, want 2 (chunk 3 must not be sent)", reqs)
	}
	msg := err.Error()
	if !strings.Contains(msg, "chunk 2/3") || !strings.Contains(msg, "1000-1999") {
		t.Errorf("error must name the chunk/range, got %q", msg)
	}
	if !strings.Contains(msg, "concurrently") {
		t.Errorf("409 must surface concurrent modification, got %q", msg)
	}
	if StatusCode(err) != http.StatusConflict {
		t.Errorf("StatusCode(err) = %d, want 409", StatusCode(err))
	}
}

// TestPutPlaylistItemsCtxCancel: cancelling the context stops the push - no chunk after the
// cancel is sent.
func TestPutPlaylistItemsCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var reqs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs++
		cancel() // cancel during the first chunk; the next chunk's guard must catch it
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"pl_x","title":"T"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, logbus.New(8))
	_, err := c.PutPlaylistItems(ctx, "tok", "pl_x", mkPlaylistItems(1500))
	if err == nil {
		t.Fatal("want error after cancel, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if reqs != 1 {
		t.Errorf("requests = %d, want 1 (no chunk sent after cancel)", reqs)
	}
}
