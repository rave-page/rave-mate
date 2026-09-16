package eventbus

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"rave.page/mate/internal/logbus"
)

// linkPair cross-wires two buses over a synchronous in-memory transport (what the peerlink does,
// minus the network): every frame A sends is fed straight into B.Inbound and vice versa.
func linkPair(a, b *Bus) {
	a.SetTransport(func(p []byte) { b.Inbound(a.self, p) }, func(_ string, p []byte) { b.Inbound(a.self, p) })
	b.SetTransport(func(p []byte) { a.Inbound(b.self, p) }, func(_ string, p []byte) { a.Inbound(b.self, p) })
}

// TestConcurrentPublishersNeverDropAtPeer: the peer dedups by per-origin monotonic seq and treats
// a lower seq arriving after a higher one as a reorder (dropped for good). Two goroutines publishing
// on the SAME node must therefore hand their frames to the transport in seq order, or a command can
// be lost because a status broadcast overtook it (the webcam bus round-trip flake on ubuntu CI).
// Every frame published on A must arrive at B exactly once.
func TestConcurrentPublishersNeverDropAtPeer(t *testing.T) {
	a := New(logbus.New(16), "node-a")
	b := New(logbus.New(16), "node-b")
	linkPair(a, b)

	var got atomic.Int64
	b.Subscribe("t", func(Event) { got.Add(1) })
	base := dupDropped(t, b) // the link handshake echoes B's own advertise back to B: a designed drop

	const workers, each = 8, 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				a.Publish("t", json.RawMessage(`1`))
			}
		}()
	}
	wg.Wait()
	if n := got.Load(); n != workers*each {
		t.Fatalf("peer received %d of %d frames (reorder drops): %s", n, workers*each, b.Stats())
	}
	if d := dupDropped(t, b); d != base {
		t.Fatalf("peer dropped %d frames from a single origin (reorder): %s", d-base, b.Stats())
	}
}

// TestPublishReentrantFromSubscriber: a subscriber that publishes on the same bus from inside the
// delivery (a handler answering an event) must not deadlock the ordered send path, and its frame
// must still reach the peer.
func TestPublishReentrantFromSubscriber(t *testing.T) {
	a := New(logbus.New(16), "node-a")
	b := New(logbus.New(16), "node-b")
	linkPair(a, b)
	var replies atomic.Int64
	// B answers every "ask" with a "reply" on ITS bus, from inside the inbound delivery.
	b.Subscribe("ask", func(Event) { b.Publish("reply", json.RawMessage(`1`)) })
	a.Subscribe("reply", func(e Event) {
		if !e.Local {
			replies.Add(1)
		}
	})
	for i := 0; i < 50; i++ {
		a.Publish("ask", json.RawMessage(`1`))
	}
	if n := replies.Load(); n != 50 {
		t.Fatalf("want 50 replies at A, got %d: A=%s B=%s", n, a.Stats(), b.Stats())
	}
}

// dupDropped parses the dedup-drop counter out of Stats.
func dupDropped(t *testing.T, b *Bus) int {
	t.Helper()
	for _, f := range strings.Fields(b.Stats()) {
		if n, ok := strings.CutPrefix(f, "dupDropped="); ok {
			v, err := strconv.Atoi(n)
			if err != nil {
				t.Fatalf("bad stats field %q", f)
			}
			return v
		}
	}
	t.Fatalf("no dupDropped in %s", b.Stats())
	return 0
}
