package medialink

import (
	"context"
	"net"
	"time"
)

// router_udp.go holds the §2.5 UDP-profile router wiring kept out of router.go (merge-clean): the
// transport-preference helpers, the dialer's reachability handshake + TCP fallback (brief req 4),
// and the answerer's mux-driven source start. The negotiated UDP route is a frameConn like the TCP
// one, so the rest of the router (reports/sync/NACK/jitter/telemetry) is transport-agnostic.

// udpReachTimeout bounds the dialer's reachability handshake: if the answerer does not reply to our
// keepalive ping within this, UDP is treated as blocked (firewall) and the route falls back to TCP.
const udpReachTimeout = time.Second

// offerTransports lists this node's media transports in preference order for an Offer. forceTCP (a
// blackout-fallback re-offer) returns nil so the answerer never picks UDP. Absent on an old build.
func (rm *RouteManager) offerTransports(forceTCP bool) []string {
	if forceTCP || !rm.udp {
		return nil
	}
	return []string{TransportUDP, TransportTCP}
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// transportName names the route's transport for telemetry.
func transportName(udp *udpConn) string {
	if udp != nil {
		return TransportUDP
	}
	return TransportTCP
}

// dialConn gives the sink side a route conn: a reachable UDP conn when the answer chose UDP and the
// reachability handshake succeeds, else a TCP conn (default or fallback). Returns the frameConn and,
// when UDP, the *udpConn (for telemetry + the blackout detector).
func (rm *RouteManager) dialConn(ctx context.Context, peer string, ans Answer, salt []byte, encrypt bool) (frameConn, *udpConn, error) {
	if ans.Transport == TransportUDP && rm.udp && salt != nil {
		if u, ok := rm.dialUDP(ctx, peer, ans, salt, encrypt); ok {
			return u, u, nil
		}
		rm.infof("udp unreachable - falling back to TCP", map[string]any{"session": ans.Session, "addr": ans.Addr})
	}
	d := net.Dialer{Timeout: dialTimeout}
	c, err := d.DialContext(ctx, "tcp", ans.Addr)
	if err != nil {
		return nil, nil, err
	}
	setNoDelay(c)
	if err := writePreamble(c, ans.Session); err != nil {
		_ = c.Close()
		return nil, nil, err
	}
	conn, err := rm.mediaConn(c, peer, salt, encrypt, true) // dialing end = initiator
	if err != nil {
		_ = c.Close()
		return nil, nil, err
	}
	return conn, nil, nil
}

// dialUDP opens a UDP conn toward the answered addr and runs the reachability handshake (its
// keepalive ping must draw a reply within udpReachTimeout). ok=false → the caller falls back to TCP.
func (rm *RouteManager) dialUDP(ctx context.Context, peer string, ans Answer, salt []byte, encrypt bool) (*udpConn, bool) {
	sec, ok := rm.secrets.MediaSecret(peer)
	if !ok {
		return nil, false // no media secret → no token → no UDP
	}
	raddr, err := net.ResolveUDPAddr("udp", ans.Addr)
	if err != nil {
		return nil, false
	}
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0}) // dedicated dialer socket
	if err != nil {
		return nil, false
	}
	u, err := newUDPConn(pc, raddr, udpConfig{Token: routeToken(sec, salt), Initiator: true,
		Master: sec, Salt: salt, Encrypt: encrypt, Clock: rm.clock, Keepalive: 100 * time.Millisecond,
		Log: rm.udpLog})
	if err != nil {
		_ = pc.Close()
		return nil, false
	}
	select {
	case <-ctx.Done():
		_ = u.Close()
		return nil, false
	default:
	}
	if !u.waitReachable(udpReachTimeout) {
		_ = u.Close()
		return nil, false
	}
	return u, true
}

// serveUDPFirst is the mux's onFirst callback (answerer source side): the first datagram for a
// pending UDP route arrived. It AUTHENTICATES the datagram with the route's keys/token BEFORE
// claiming the pending answer or creating any state (finding 2); only on success does it atomically
// claim (the TCP path may race), build the route's udpConn over a fresh muxConn, feed the
// authenticated first datagram (so the udpConn binds its remote from that source + replies), and
// start the source pump. nil = unauthenticated / claim lost / not viable → the mux creates no state.
func (rm *RouteManager) serveUDPFirst(token uint32, session string, src *net.UDPAddr, dg []byte) *muxConn {
	pa := rm.peekPendingAnswer(session) // peek, do NOT claim yet
	if pa == nil {
		return nil
	}
	if !rm.authenticateFirstDatagram(pa, token, dg) {
		return nil // forged / off-path: no claim, no state, pending survives for a real retry
	}
	pa = rm.claimPendingAnswer(session) // authenticated → now claim atomically (TCP fallback may win)
	if pa == nil {
		return nil
	}
	sec, ok := rm.secrets.MediaSecret(pa.peer)
	if !ok {
		return nil
	}
	rm.mu.Lock()
	mux := rm.mux
	rm.mu.Unlock()
	if mux == nil {
		return nil
	}
	mc := mux.newMuxConn(token)
	u, err := newUDPConn(mc, nil, udpConfig{Token: token, Initiator: false, Master: sec, Salt: pa.salt,
		Encrypt: pa.encrypt, Clock: rm.clock, Log: rm.udpLog, // remote nil → binds on the authenticated datagram
		Keepalive:  100 * time.Millisecond,           // emit keepalives from udpConn birth (before the encoder opens) so a cold receiver sees liveness
		PacingRate: pacingRateFor(pa.offer.Bitrate)}) // send-pace at a multiple of the budget (item 2)
	if err != nil {
		_ = mc.Close()
		return nil
	}
	mc.deliver(append([]byte(nil), dg...), src) // feed the authenticated first datagram (pong + bind)
	go rm.guard("pumpSourceUDP", func() {
		rm.pumpSource(rm.routeCtx(), session, pa, u, u)
	})
	return mc
}

// authenticateFirstDatagram verifies a would-be first datagram against the route's keys/token WITHOUT
// creating any state: AEAD open (encrypted routes) or token + route-mode check (clear routes). The
// token is re-derived from the route material and must match the datagram's. Returns whether it
// authenticated - the gate that must pass before any pending answer is claimed or a remote bound.
func (rm *RouteManager) authenticateFirstDatagram(pa *pendingAnswer, token uint32, dg []byte) bool {
	sec, ok := rm.secrets.MediaSecret(pa.peer)
	if !ok {
		return false
	}
	if routeToken(sec, pa.salt) != token {
		return false
	}
	var crypto *dgCrypto
	if pa.encrypt {
		c, err := newDgCrypto(sec, pa.salt, false) // responder
		if err != nil {
			return false
		}
		crypto = c
	}
	h, off, err := parseDatagramHeader(dg)
	if err != nil {
		return false
	}
	_, err = decodeBody(h, dg[off:], token, crypto, dg[:off])
	return err == nil
}

// routeCtx returns the manager's run context (routes are torn down by it + their own cancel).
func (rm *RouteManager) routeCtx() context.Context {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.ctx != nil {
		return rm.ctx
	}
	return context.Background()
}

// udpLog adapts the manager logger to the udpConn's log sink (nil-safe).
func (rm *RouteManager) udpLog(msg string, f map[string]any) { rm.infof(msg, f) }
