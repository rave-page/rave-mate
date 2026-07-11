package bridge

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"
)

// Live probes against the DEPLOYED rave.page API. Opt-in:
//
//	RAVE_BRIDGE_LIVE=1 go test ./internal/bridge/ -run Live -v
//
// Without RAVE_BRIDGE_TOKEN these run UNAUTHENTICATED on purpose: they prove the endpoints
// exist, that our request shapes reach them, and that the real 401 body decodes onto the
// right sentinel. A full authenticated round trip needs a signed-in account - see
// TestLiveAuthenticatedRoundTrip, which skips unless a token is supplied.
//
// NEVER hardcode a token here, and never point these at production.
func liveBase(t *testing.T) string {
	t.Helper()
	if os.Getenv("RAVE_BRIDGE_LIVE") == "" {
		t.Skip("set RAVE_BRIDGE_LIVE=1 to probe the deployed API")
	}
	base := os.Getenv("RAVE_API_BASE_URL")
	if base == "" {
		base = "https://development.api.rave.page"
	}
	if base == "https://api.rave.page" {
		t.Fatal("refusing to run live bridge probes against PRODUCTION")
	}
	return base
}

// TestLiveUnauthorizedShape pins the real API's error body. The contract doc calls these
// "RFC7807 Problem Details", but the deployed API actually sends
//
//	{"status":"error","trace_id":"...","message":"...","details":{"code":"UNAUTHORIZED"}}
//
// i.e. the human text is `message`, NOT `detail`/`title`. decodeProblem reads all three; this
// test fails if that drifts again.
func TestLiveUnauthorizedShape(t *testing.T) {
	base := liveBase(t)
	c := NewClient(base, staticToken("not.a.real.token"), testLog())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := c.Register(ctx, "nd-live-probe", "probe", []string{CapPeerlink})
	if err == nil {
		t.Fatal("a bogus bearer was ACCEPTED by the live API")
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	var ae *APIError
	if !errors.As(err, &ae) {
		t.Fatalf("err is not an *APIError: %T", err)
	}
	if ae.Status != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", ae.Status)
	}
	if ae.Code != "UNAUTHORIZED" {
		t.Errorf("details.code = %q, want UNAUTHORIZED", ae.Code)
	}
	if ae.Detail == "" {
		t.Error("no human text decoded - the API's `message` field is not being read")
	}
	if ae.TraceID == "" {
		t.Error("no trace_id decoded - it's needed to report backend problems")
	}
	t.Logf("live 401 decoded: code=%s detail=%q trace=%s", ae.Code, ae.Detail, ae.TraceID)
}

// TestLiveEndpointsExist walks every endpoint unauthenticated. All must 401 (not 404/405):
// proof the routes are deployed and our method+path shapes hit them.
func TestLiveEndpointsExist(t *testing.T) {
	base := liveBase(t)
	c := NewClient(base, staticToken("not.a.real.token"), testLog())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	probes := []struct {
		name string
		call func() error
	}{
		{"registerBridgeSession", func() error { _, e := c.Register(ctx, "nd", "p", nil); return e }},
		{"listBridgeSessions", func() error { _, e := c.ListSessions(ctx); return e }},
		{"heartbeatBridgeSession", func() error { return c.Heartbeat(ctx, "bses_probe") }},
		{"deleteBridgeSession", func() error { return c.Deregister(ctx, "bses_probe") }},
		{"acceptBridgePeer", func() error { return c.Accept(ctx, "bses_probe", "bses_peer") }},
		{"sendBridgeFrame", func() error { return c.Send(ctx, "bses_a", "bses_b", 1, KindSignal, []byte("x")) }},
		{"streamBridge", func() error { return c.Stream(ctx, "bses_probe", StreamHandlers{}) }},
	}
	for _, p := range probes {
		err := p.call()
		if err == nil {
			t.Errorf("%s: unauthenticated call SUCCEEDED", p.name)
			continue
		}
		var ae *APIError
		if !errors.As(err, &ae) {
			t.Errorf("%s: %v (not an APIError - endpoint may be unreachable)", p.name, err)
			continue
		}
		if ae.Status != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401 (404/405 ⇒ the route is not deployed as we call it)", p.name, ae.Status)
		}
	}
}

// TestLiveAuthenticatedRoundTrip is the only test that can prove real delivery. It needs a
// live account token:
//
//	RAVE_BRIDGE_LIVE=1 RAVE_BRIDGE_TOKEN=<jwt> go test ./internal/bridge/ -run LiveAuthenticated -v
//
// It registers TWO sessions on the same account, streams one, mutually accepts, and relays a
// frame between them - the exact path the WAN link depends on.
func TestLiveAuthenticatedRoundTrip(t *testing.T) {
	base := liveBase(t)
	tok := os.Getenv("RAVE_BRIDGE_TOKEN")
	if tok == "" {
		t.Skip("set RAVE_BRIDGE_TOKEN=<account jwt> for the authenticated round trip")
	}
	c := NewClient(base, staticToken(tok), testLog())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	a, err := c.Register(ctx, "nd-live-a", "live probe A", []string{CapPeerlink})
	if err != nil {
		t.Fatalf("register A: %v", err)
	}
	defer func() { _ = c.Deregister(context.Background(), a.SID) }()

	b, err := c.Register(ctx, "nd-live-b", "live probe B", []string{CapPeerlink})
	if err != nil {
		t.Fatalf("register B: %v", err)
	}
	defer func() { _ = c.Deregister(context.Background(), b.SID) }()

	frames := make(chan Frame, 8)
	hello := make(chan string, 1)
	sctx, scancel := context.WithCancel(ctx)
	defer scancel()
	go func() {
		_ = c.Stream(sctx, b.SID, StreamHandlers{
			OnHello: func(sid string) { hello <- sid },
			OnFrame: func(f Frame) { frames <- f },
		})
	}()
	select {
	case <-hello:
	case <-time.After(20 * time.Second):
		t.Fatal("no hello from the live SSE stream")
	}

	// Relay must be refused until BOTH sides accept.
	if err := c.Send(ctx, a.SID, b.SID, 1, KindRelay, []byte("early")); !errors.Is(err, ErrNotAccepted) {
		t.Errorf("relay before accept: err = %v, want ErrNotAccepted", err)
	}
	if err := c.Accept(ctx, a.SID, b.SID); err != nil {
		t.Fatalf("accept a→b: %v", err)
	}
	if err := c.Accept(ctx, b.SID, a.SID); err != nil {
		t.Fatalf("accept b→a: %v", err)
	}
	if err := c.Send(ctx, a.SID, b.SID, 42, KindRelay, []byte("hello over the wire")); err != nil {
		t.Fatalf("relay after mutual accept: %v", err)
	}
	select {
	case f := <-frames:
		if string(f.Payload) != "hello over the wire" {
			t.Errorf("payload = %q", f.Payload)
		}
		t.Logf("live relay round trip OK: seq=%d from=%s", f.Seq, f.SID)
	case <-time.After(20 * time.Second):
		t.Fatal("relay frame never arrived (fire-and-forget loss, or the stream is broken)")
	}
}
