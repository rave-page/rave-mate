package bridge

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
)

func testLog() *logbus.Bus { return logbus.New(64) }

// ── client vs the contract ───────────────────────────────────────────────────

// TestClientRoundTrip drives all 7 endpoints against the fake, proving the request shapes and
// the SSE parse (padding comment, hello, framed events).
func TestClientRoundTrip(t *testing.T) {
	f := newFakeBridge(t)
	c := NewClient(f.srv.URL, staticToken("jwt"), testLog())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a, err := c.Register(ctx, "nd-a", "Studio PC", []string{CapPeerlink, CapLocalStudio})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if a.SID == "" || a.NodeID != "nd-a" {
		t.Fatalf("bad session: %+v", a)
	}
	if !a.Has(CapLocalStudio) {
		t.Error("capabilities not round-tripped")
	}
	b, err := c.Register(ctx, "nd-b", "Laptop", []string{CapPeerlink})
	if err != nil {
		t.Fatalf("Register b: %v", err)
	}

	list, err := c.ListSessions(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListSessions = %d sessions, err %v", len(list), err)
	}
	if err := c.Heartbeat(ctx, a.SID); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	// Stream + a signal frame end to end.
	got := make(chan Frame, 4)
	hello := make(chan string, 1)
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	go func() {
		_ = c.Stream(sctx, b.SID, StreamHandlers{
			OnHello: func(sid string) { hello <- sid },
			OnFrame: func(fr Frame) { got <- fr },
		})
	}()
	select {
	case sid := <-hello:
		if sid != b.SID {
			t.Errorf("hello sid = %s, want %s", sid, b.SID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no hello from the stream")
	}

	// signal needs NO accept.
	if err := c.Send(ctx, a.SID, b.SID, 7, KindSignal, []byte("ping")); err != nil {
		t.Fatalf("Send signal: %v", err)
	}
	select {
	case fr := <-got:
		if fr.Kind != KindSignal || string(fr.Payload) != "ping" || fr.SID != a.SID || fr.Seq != 7 {
			t.Errorf("frame = %+v, want signal 'ping' from %s seq 7", fr, a.SID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("signal frame never arrived")
	}

	if err := c.Deregister(ctx, a.SID); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if _, err := c.ListSessions(ctx); err != nil {
		t.Fatalf("ListSessions after deregister: %v", err)
	}
}

// TestRelayGatedOnMutualAccept: the contract's central authorization rule.
func TestRelayGatedOnMutualAccept(t *testing.T) {
	f := newFakeBridge(t)
	c := NewClient(f.srv.URL, staticToken("jwt"), testLog())
	ctx := context.Background()

	a, _ := c.Register(ctx, "nd-a", "A", nil)
	b, _ := c.Register(ctx, "nd-b", "B", nil)

	// No accepts yet → 403.
	err := c.Send(ctx, a.SID, b.SID, 1, KindRelay, []byte("x"))
	if !errors.Is(err, ErrNotAccepted) {
		t.Fatalf("relay before accept: err = %v, want ErrNotAccepted", err)
	}

	// One direction only → still 403.
	if err := c.Accept(ctx, a.SID, b.SID); err != nil {
		t.Fatalf("Accept a→b: %v", err)
	}
	err = c.Send(ctx, a.SID, b.SID, 1, KindRelay, []byte("x"))
	if !errors.Is(err, ErrNotAccepted) {
		t.Fatalf("relay after one-way accept: err = %v, want ErrNotAccepted", err)
	}

	// Mutual → flows.
	if err := c.Accept(ctx, b.SID, a.SID); err != nil {
		t.Fatalf("Accept b→a: %v", err)
	}
	if err := c.Send(ctx, a.SID, b.SID, 1, KindRelay, []byte("x")); err != nil {
		t.Fatalf("relay after mutual accept: %v", err)
	}
}

func TestClientErrorMapping(t *testing.T) {
	f := newFakeBridge(t)
	ctx := context.Background()

	// 401 - no bearer.
	anon := NewClient(f.srv.URL, staticToken(""), testLog())
	if _, err := anon.Register(ctx, "nd", "X", nil); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("no token: err = %v, want ErrUnauthorized", err)
	}

	c := NewClient(f.srv.URL, staticToken("jwt"), testLog())
	a, _ := c.Register(ctx, "nd-a", "A", nil)

	// 404 for an unknown sid (never 403 - BOLA-safe).
	if err := c.Heartbeat(ctx, "bses_deadbeef"); !errors.Is(err, ErrSessionGone) {
		t.Errorf("unknown sid: err = %v, want ErrSessionGone", err)
	}

	// 413 over the payload cap - caught client-side before the round trip.
	big := make([]byte, MaxPayload+1)
	if err := c.Send(ctx, a.SID, a.SID, 1, KindRelay, big); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversized: err = %v, want ErrTooLarge", err)
	}

	// 429 + Retry-After.
	f.setRateLimit(true)
	err := c.Send(ctx, a.SID, a.SID, 1, KindSignal, []byte("x"))
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("rate limited: err = %v, want ErrRateLimited", err)
	}
	var ae *APIError
	if !errors.As(err, &ae) || ae.RetryAfter != 10*time.Second {
		t.Errorf("Retry-After = %v, want 10s", ae.RetryAfter)
	}
}

// ── Conn: the ARQ, fragmentation, and the AEAD ───────────────────────────────

// linkedPair wires two Conns to each other through a sender that models the relay: publish
// succeeds (202), delivery is best-effort, and every Nth frame is DROPPED.
type lossySender struct {
	mu     sync.Mutex
	peer   map[string]*Conn // sid → the conn that receives frames addressed to it
	n      int
	drop   int // drop every Nth frame; 0 = lossless
	seen   [][]byte
	closed bool
}

func (l *lossySender) Send(_ context.Context, _, toSID string, _ int64, _ string, payload []byte) error {
	l.mu.Lock()
	l.n++
	l.seen = append(l.seen, append([]byte{}, payload...))
	drop := l.drop > 0 && l.n%l.drop == 0
	target := l.peer[toSID]
	closed := l.closed
	l.mu.Unlock()
	if closed || drop || target == nil {
		return nil // 202: published. Delivery is not promised.
	}
	target.deliver(payload)
	return nil
}

func (l *lossySender) payloads() [][]byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([][]byte{}, l.seen...)
}

// newLinkedPair builds two Conns talking to each other over a lossy relay.
func newLinkedPair(t *testing.T, dropEveryNth int) (a, b *Conn, s *lossySender) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	s = &lossySender{peer: map[string]*Conn{}, drop: dropEveryNth}
	a = newConn(ctx, "sid-a", "sid-b", s, testLog())
	b = newConn(ctx, "sid-b", "sid-a", s, testLog())
	s.mu.Lock()
	s.peer["sid-a"], s.peer["sid-b"] = a, b
	s.mu.Unlock()
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b, s
}

func TestConnRoundTrip(t *testing.T) {
	a, b, _ := newLinkedPair(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := a.Send(ctx, []byte("hello peer")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := b.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if string(got) != "hello peer" {
		t.Errorf("got %q", got)
	}

	// And back.
	if err := b.Send(ctx, []byte("hi")); err != nil {
		t.Fatalf("Send back: %v", err)
	}
	got, err = a.Recv(ctx)
	if err != nil || string(got) != "hi" {
		t.Fatalf("reverse: %q err %v", got, err)
	}
}

// TestConnSurvivesFireAndForgetLoss is the headline reliability property: the relay drops
// frames, and the ARQ still delivers every message, in order, byte-exact.
func TestConnSurvivesFireAndForgetLoss(t *testing.T) {
	a, b, _ := newLinkedPair(t, 3) // every 3rd frame vanishes
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const n = 20
	want := make([][]byte, n)
	for i := range n {
		want[i] = bytes.Repeat([]byte{byte('a' + i%26)}, 100+i)
	}

	done := make(chan error, 1)
	go func() {
		for i := range n {
			got, err := b.Recv(ctx)
			if err != nil {
				done <- err
				return
			}
			if !bytes.Equal(got, want[i]) {
				done <- fmt.Errorf("message %d corrupted or out of order", i)
				return
			}
		}
		done <- nil
	}()

	for i := range n {
		if err := a.Send(ctx, want[i]); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("lossy delivery: %v", err)
		}
	case <-time.After(50 * time.Second):
		t.Fatal("messages never arrived through a lossy relay - the ARQ is not recovering")
	}
}

// TestConnFragmentation: a message far larger than one relay frame is chunked and reassembled.
func TestConnFragmentation(t *testing.T) {
	a, b, s := newLinkedPair(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	msg := make([]byte, chunkBody*3+1234) // 4 chunks
	if _, err := rand.Read(msg); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if err := a.Send(ctx, msg); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := b.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatal("reassembled message differs from the original")
	}

	// Every published frame must fit the relay's decoded-payload cap, or the server 413s.
	for i, p := range s.payloads() {
		if len(p) > MaxPayload {
			t.Fatalf("chunk %d is %d bytes, over the %d cap", i, len(p), MaxPayload)
		}
	}
}

func TestConnRejectsOversizedMessage(t *testing.T) {
	a, _, _ := newLinkedPair(t, 0)
	ctx := context.Background()
	if err := a.Send(ctx, make([]byte, MaxMessage+1)); !errors.Is(err, ErrTooBig) {
		t.Errorf("err = %v, want ErrTooBig", err)
	}
}

// TestConnAEADBlindsTheRelay is the security headline: after Upgrade, everything the relay
// sees is ciphertext. Without this, peerlink's plaintext data plane would hand the operator
// every remote-control command and the whole RemoteUI Library stream.
func TestConnAEADBlindsTheRelay(t *testing.T) {
	a, b, s := newLinkedPair(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	master := bytes.Repeat([]byte{0x42}, 32) // stands in for the peerlink session key
	if err := a.Upgrade(master, true); err != nil {
		t.Fatalf("Upgrade a: %v", err)
	}
	if err := b.Upgrade(master, false); err != nil {
		t.Fatalf("Upgrade b: %v", err)
	}

	secret := []byte(`{"method":"library.listTracks","params":{"q":"very secret crate"}}`)
	if err := a.Send(ctx, secret); err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := b.Recv(ctx)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatal("AEAD round trip corrupted the message")
	}

	// The relay saw only ciphertext.
	for _, p := range s.payloads() {
		if bytes.Contains(p, []byte("very secret crate")) || bytes.Contains(p, []byte("library.listTracks")) {
			t.Fatal("PLAINTEXT ON THE WIRE: the relay can read the payload - the blind-server guarantee is broken")
		}
	}

	// Both directions, and a forged frame must not open.
	if err := b.Send(ctx, []byte("reply")); err != nil {
		t.Fatalf("Send back: %v", err)
	}
	if got, err := a.Recv(ctx); err != nil || string(got) != "reply" {
		t.Fatalf("reverse AEAD: %q err %v", got, err)
	}
}

// TestConnAEADRejectsForgery: a tampered ciphertext must not open - the relay operator cannot
// inject commands.
func TestConnAEADRejectsForgery(t *testing.T) {
	a, b, _ := newLinkedPair(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	master := bytes.Repeat([]byte{7}, 32)
	_ = a.Upgrade(master, true)
	_ = b.Upgrade(master, false)

	// Hand b a chunk whose ciphertext is garbage but whose framing is valid.
	forged := encodeChunk(typeData, 0, 0, true, []byte("not a valid ciphertext at all!!!"))
	b.deliver(forged)

	// b must fail the connection rather than deliver anything.
	_, err := b.Recv(ctx)
	if err == nil {
		t.Fatal("a forged ciphertext was accepted")
	}
}

// TestConnWindowIsBounded: the send window has a hard cap, so a peer that never acks cannot
// make us buffer without limit (repo hard rule).
func TestConnWindowIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A sender that swallows everything: nothing is ever acked.
	dead := &lossySender{peer: map[string]*Conn{}, drop: 1}
	c := newConn(ctx, "sid-a", "sid-b", dead, testLog())
	defer c.Close()

	// Fill past the window, with a deadline: Send must BLOCK (backpressure), not accumulate.
	sctx, scancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer scancel()
	body := make([]byte, chunkBody)
	var sent int
	for {
		if err := c.Send(sctx, body); err != nil {
			break // blocked, then the deadline fired - the correct behaviour
		}
		sent++
		if sent > maxWindowChunks*4 {
			t.Fatal("Send never blocked - the window is unbounded")
		}
	}

	c.mu.Lock()
	inWindow, bytesInWindow := len(c.window), c.windowB
	c.mu.Unlock()
	if inWindow > maxWindowChunks {
		t.Errorf("window holds %d chunks, cap is %d", inWindow, maxWindowChunks)
	}
	if bytesInWindow > maxWindowBytes {
		t.Errorf("window holds %d bytes, cap is %d", bytesInWindow, maxWindowBytes)
	}
}
