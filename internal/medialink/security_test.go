package medialink

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func bytes16(b byte) []byte {
	out := make([]byte, routeNonceLen)
	for i := range out {
		out[i] = b + byte(i)
	}
	return out
}

// manualClock is a settable ClockSource for deterministic rate-limit tests.
type manualClock struct{ ns atomic.Int64 }

func (c *manualClock) Now() int64            { return c.ns.Load() }
func (c *manualClock) Quality() ClockQuality { return ClockQuality{Tier: TierMonotonic, Locked: true} }

// TestPerRouteKeyScheduleDiffers: two routes to the same peer (different route nonces) derive
// different keys, nonce prefixes and tokens, and a datagram sealed on route A cannot be opened on
// route B (finding 1 - no cross-route key/nonce reuse).
func TestPerRouteKeyScheduleDiffers(t *testing.T) {
	master := testKey()
	saltA := routeKeySalt(bytes16(1), bytes16(2), "sess")
	saltB := routeKeySalt(bytes16(3), bytes16(4), "sess") // same session, different nonces (concurrent route)
	if saltA == nil || saltB == nil {
		t.Fatal("salt nil")
	}
	tokA, tokB := routeToken(master, saltA), routeToken(master, saltB)
	if tokA == tokB {
		t.Fatal("per-route tokens identical")
	}
	cA, err := newDgCrypto(master, saltA, true) // route A initiator
	if err != nil {
		t.Fatal(err)
	}
	cB, err := newDgCrypto(master, saltB, false) // route B responder
	if err != nil {
		t.Fatal(err)
	}
	if cA.sendPre == cB.recvPre {
		t.Fatal("nonce prefixes collide across routes")
	}
	// A datagram sealed on route A must NOT open on route B (different keys).
	encA := newDgEncoder(tokA, cA, 1200)
	dgs, _, _, _ := encA.encodeFrame(nil, sampleFrame(1, 1, 200))
	h, off, err := parseDatagramHeader(dgs[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeBody(h, dgs[0][off:], tokB, cB, dgs[0][:off]); err == nil {
		t.Fatal("route-A datagram opened on route B (key/nonce reuse)")
	}
}

// TestRouteKeySaltFreshOnRestart: a route restart (fresh nonces, even same session) yields a
// different salt → different keys (finding 1 - restart/renegotiation never reuses keys).
func TestRouteKeySaltFreshOnRestart(t *testing.T) {
	s1 := routeKeySalt(newRouteNonce(), newRouteNonce(), "sess")
	s2 := routeKeySalt(newRouteNonce(), newRouteNonce(), "sess")
	if string(s1) == string(s2) {
		t.Fatal("route restart reused the key salt")
	}
	// An old peer (no nonce) → nil salt → legacy derivation path.
	if routeKeySalt(nil, bytes16(1), "sess") != nil || routeKeySalt(bytes16(1), nil, "sess") != nil {
		t.Fatal("missing nonce should yield nil salt (legacy)")
	}
}

// TestDatagramSeqMonotonicAndHardStop: the encoder sequence is strictly monotonic and never resets
// (an "encoder respawn" is a new route with fresh keys, not a seq reset on a live key), and the hard
// stop fires before the sequence can approach wrap (finding 1).
func TestDatagramSeqMonotonicAndHardStop(t *testing.T) {
	enc := newDgEncoder(1, nil, 300)
	var last uint64
	first := true
	for i := 0; i < 2000; i++ {
		dgs, firstSeq, _, err := enc.encodeFrame(nil, sampleFrame(uint32(i), 1, 1000)) // ~4 frags each
		if err != nil {
			t.Fatal(err)
		}
		if !first && firstSeq <= last {
			t.Fatalf("seq not monotonic across frames: %d <= %d", firstSeq, last)
		}
		for _, dg := range dgs {
			h, _, _ := parseDatagramHeader(dg)
			if !first && h.Seq <= last && h.Seq != firstSeq {
				t.Fatalf("seq reset/dup within stream: %d", h.Seq)
			}
			last = h.Seq
		}
		// Interleave control traffic: must share the same monotonic seq, never reset it.
		if c := enc.control(dgTypePing, []byte{0, 0, 0, 0, 0, 0, 0, 1}); c != nil {
			h, _, _ := parseDatagramHeader(c)
			if h.Seq <= last {
				t.Fatalf("control seq not monotonic: %d <= %d", h.Seq, last)
			}
			last = h.Seq
		}
		first = false
	}
	// Hard stop: at the cap, media encode errors and control returns nil (renegotiate for fresh keys).
	enc.seq = dgSeqHardStop
	if _, _, _, err := enc.encodeFrame(nil, sampleFrame(1, 1, 100)); err != errDgSeqExhausted {
		t.Fatalf("hard stop not enforced on encode: %v", err)
	}
	if enc.control(dgTypePing, nil) != nil {
		t.Fatal("hard stop not enforced on control")
	}
}

// TestDatagramNonceUniqueness: over a long run every datagram seq (the AEAD nonce input) is unique -
// the one rule GCM cannot survive breaking (finding 1).
func TestDatagramNonceUniqueness(t *testing.T) {
	enc := newDgEncoder(1, nil, 64)
	seen := make(map[uint64]struct{}, 200000)
	for i := 0; i < 2000; i++ {
		dgs, _, _, err := enc.encodeFrame(nil, sampleFrame(uint32(i), 1, 3000)) // ~48 frags each
		if err != nil {
			t.Fatal(err)
		}
		for _, dg := range dgs {
			h, _, _ := parseDatagramHeader(dg)
			if _, dup := seen[h.Seq]; dup {
				t.Fatalf("duplicate datagram seq (nonce reuse): %d", h.Seq)
			}
			seen[h.Seq] = struct{}{}
		}
	}
	if len(seen) < 90000 {
		t.Fatalf("expected a long run, only %d datagrams", len(seen))
	}
}

// TestBindRemoteOnlyOnAuthRateLimited: the send target binds once, then changes only after the
// rebind interval (finding 2 - rebinding is authenticated + rate-limited). handleDatagram calls
// bindRemote ONLY after decodeBody succeeds (see TestForgedFirstDatagramNoClaim for the unauth case).
func TestBindRemoteOnlyOnAuthRateLimited(t *testing.T) {
	mc := &manualClock{}
	mc.ns.Store(1000)
	u := &udpConn{clock: mc, seqTrk: newReplayWin()}
	a := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 1}
	b := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 2}
	u.bindRemote(a)
	if u.remoteAddr().String() != a.String() {
		t.Fatal("first bind failed")
	}
	u.bindRemote(b) // immediate change → rate-limited, stays a
	if u.remoteAddr().String() != a.String() {
		t.Fatal("rebind was not rate-limited")
	}
	mc.ns.Store(1000 + int64(udpRebindMin) + 1) // past the interval
	u.bindRemote(b)
	if u.remoteAddr().String() != b.String() {
		t.Fatal("authenticated rebind after the interval did not apply")
	}
	if u.Stats().Rebinds != 1 {
		t.Fatalf("rebind count = %d want 1", u.Stats().Rebinds)
	}
}

// TestMuxUnknownTokenNoState: a datagram for an UNKNOWN token creates no state (onFirst not called);
// a datagram for a KNOWN-but-forged token (onFirst returns nil) also creates no active route and
// leaves the pending registration for a legitimate retry (finding 2 - bounded, unauth = no state).
func TestMuxUnknownTokenNoState(t *testing.T) {
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	mux := newUDPMux(pc, func(uint32, string, *net.UDPAddr, []byte) *muxConn {
		calls.Add(1)
		return nil // simulate an unauthenticated first datagram
	})
	defer mux.Close()
	send, err := net.DialUDP("udp", nil, pc.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer send.Close()

	// Unknown token → onFirst never called, no state.
	dgUnknown := dgHeader{Typ: dgTypeMedia, Token: 0x11112222, FragCount: 1}.appendHeader(nil)
	_, _ = send.Write(dgUnknown)
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("onFirst called for an unknown token")
	}
	mux.mu.Lock()
	na, np := len(mux.active), len(mux.pending)
	mux.mu.Unlock()
	if na != 0 || np != 0 {
		t.Fatalf("unknown-token datagram created state: active=%d pending=%d", na, np)
	}

	// Known pending token, forged (onFirst returns nil) → no active route, pending retained.
	const tok = 0x33334444
	mux.expect(tok, "sessX")
	dgForged := dgHeader{Typ: dgTypeMedia, Token: tok, FragCount: 1}.appendHeader(nil)
	_, _ = send.Write(append(dgForged, 1, 2, 3, 4))
	time.Sleep(150 * time.Millisecond)
	if calls.Load() == 0 {
		t.Fatal("onFirst not called for a pending token")
	}
	mux.mu.Lock()
	na, np = len(mux.active), len(mux.pending)
	mux.mu.Unlock()
	if na != 0 {
		t.Fatal("forged first datagram created an active route")
	}
	if np != 1 {
		t.Fatal("pending registration dropped after a forged datagram (legit retry would fail)")
	}
}

// TestReplayDoesNotRebindOrRefreshLiveness: a REPLAYED authenticated datagram (captured + re-sent
// from another address) must not redirect the send target or refresh liveness; only a strictly-newer
// datagram may rebind (rate-limited). Guards the receive-path hijack/DoS via replay.
func TestReplayDoesNotRebindOrRefreshLiveness(t *testing.T) {
	mc := &manualClock{}
	const tok = 0x1234
	u := &udpConn{clock: mc, token: tok, seqTrk: newReplayWin(), reasm: newReassembler(0, 0),
		deadline: 200 * time.Millisecond, reachableCh: make(chan struct{}), lastRTx: map[uint16]int64{},
		recvCh: make(chan *Frame, 16)}
	enc := newDgEncoder(tok, nil, 1200)                         // clear route (crypto nil)
	dg0, _, _, _ := enc.encodeFrame(nil, sampleFrame(0, 1, 50)) // datagram seq 0
	dg1, _, _, _ := enc.encodeFrame(nil, sampleFrame(1, 1, 50)) // datagram seq 1
	srcA := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 1}
	srcB := &net.UDPAddr{IP: net.IPv4(10, 0, 0, 2), Port: 2}

	mc.ns.Store(1000)
	u.handleDatagram(dg0[0], srcA) // strictly-newest, first → binds srcA, liveness=1000
	if u.remoteAddr().String() != srcA.String() {
		t.Fatalf("first datagram did not bind srcA: %v", u.remoteAddr())
	}
	if u.lastRecvNs.Load() != 1000 {
		t.Fatalf("liveness not set on first datagram")
	}

	mc.ns.Store(5000)
	u.handleDatagram(dg0[0], srcB) // REPLAY of seq 0 from srcB → must change nothing
	if u.remoteAddr().String() != srcA.String() {
		t.Fatalf("replay rebound the send target to %v (hijack)", u.remoteAddr())
	}
	if u.lastRecvNs.Load() != 1000 {
		t.Fatalf("replay refreshed liveness (was %d)", u.lastRecvNs.Load())
	}

	mc.ns.Store(1000 + int64(udpRebindMin) + 1) // past the rebind interval
	u.handleDatagram(dg1[0], srcB)              // strictly-newer (seq 1) from srcB → legit rebind
	if u.remoteAddr().String() != srcB.String() {
		t.Fatalf("a genuinely newer datagram did not rebind to srcB: %v", u.remoteAddr())
	}
	if u.lastRecvNs.Load() != mc.Now() {
		t.Fatalf("newer datagram did not refresh liveness")
	}
	if u.Stats().DupDgram == 0 {
		t.Fatal("replayed datagram not counted as a duplicate")
	}
}

// TestForgedFirstDatagramNoClaim: a forged first datagram (wrong mode / wrong key) must NOT claim the
// pending answer or bind a remote; a correctly-sealed datagram does (finding 2).
func TestForgedFirstDatagramNoClaim(t *testing.T) {
	h := &hub{}
	master := testKey()
	rm := New(Options{Self: "B", Bus: &busView{h, "B"}, Secrets: fakeSecrets{key: master}, Ports: []int{0}, AdvertHost: "127.0.0.1", UDP: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rm.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer rm.Stop()
	if rm.mux == nil {
		t.Fatal("no mux")
	}

	const session = "sessF"
	salt := routeKeySalt(bytes16(7), bytes16(9), session)
	token := routeToken(master, salt)
	frames := []*Frame{{Kind: KindAudio, Codec: CodecPCMS16, Payload: []byte{1}}}
	pa := &pendingAnswer{peer: "A", offer: Offer{Session: session, SourceID: "mic"},
		open:    func(context.Context, Offer) (Source, error) { return &sliceSource{frames: frames}, nil },
		srcDesc: SourceDesc{ID: "mic", Kind: KindAudio, Codec: CodecPCMS16},
		stream:  1, encrypt: true, transport: TransportUDP, token: token, salt: salt}
	rm.mu.Lock()
	rm.pendingAns[session] = pa
	rm.mu.Unlock()
	rm.mux.expect(token, session)
	src := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 59999}

	// Forged 1: cleartext ping on an AEAD route (mode mismatch).
	forgedClear := newDgEncoder(token, nil, 1200).control(dgTypePing, []byte{0, 0, 0, 0, 0, 0, 0, 1})
	if mc := rm.serveUDPFirst(token, session, src, forgedClear); mc != nil {
		t.Fatal("cleartext forged datagram claimed an AEAD route")
	}
	if rm.peekPendingAnswer(session) == nil {
		t.Fatal("forged datagram consumed the pending answer")
	}

	// Forged 2: sealed with the WRONG key.
	wrong := make([]byte, 32)
	wc, _ := newDgCrypto(wrong, salt, true)
	forgedKey := newDgEncoder(token, wc, 1200).control(dgTypePing, []byte{0, 0, 0, 0, 0, 0, 0, 1})
	if mc := rm.serveUDPFirst(token, session, src, forgedKey); mc != nil {
		t.Fatal("wrong-key forged datagram claimed the route")
	}
	if rm.peekPendingAnswer(session) == nil {
		t.Fatal("wrong-key forged datagram consumed the pending answer")
	}

	// Legitimate: sealed with the route's real keys (dialer = initiator).
	realCrypto, _ := newDgCrypto(master, salt, true)
	realPing := newDgEncoder(token, realCrypto, 1200).control(dgTypePing, []byte{0, 0, 0, 0, 0, 0, 0, 1})
	mc := rm.serveUDPFirst(token, session, src, realPing)
	if mc == nil {
		t.Fatal("legitimate datagram failed to start the route")
	}
	if rm.peekPendingAnswer(session) != nil {
		t.Fatal("legitimate datagram did not claim the pending answer")
	}
}
