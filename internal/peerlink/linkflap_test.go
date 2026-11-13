package peerlink

import (
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// flapProxy forwards TCP between a dialer and a peer listener. blackhole() simulates a NIC link
// reset: every EXISTING connection silently stops passing bytes in both directions but stays open
// (half-open - no FIN/RST ever arrives), exactly the state that left both peers "paired" forever.
// New connections after restore() pass normally.
type flapProxy struct {
	ln     net.Listener
	target string
	gen    atomic.Int64 // bumped by blackhole: connections of an older generation are dead
	mu     sync.Mutex
	conns  []net.Conn
}

func newFlapProxy(t *testing.T, target string) *flapProxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &flapProxy{ln: ln, target: target}
	go p.serve()
	t.Cleanup(func() {
		_ = ln.Close()
		p.mu.Lock()
		for _, c := range p.conns {
			_ = c.Close()
		}
		p.mu.Unlock()
	})
	return p
}

func (p *flapProxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		u, err := net.Dial("tcp", p.target)
		if err != nil {
			_ = c.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, c, u)
		p.mu.Unlock()
		g := p.gen.Load()
		go p.pipe(c, u, g)
		go p.pipe(u, c, g)
	}
}

func (p *flapProxy) pipe(src, dst net.Conn, g int64) {
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if err != nil {
			return
		}
		if p.gen.Load() != g {
			continue // black-holed: swallow, never close
		}
		if _, err := dst.Write(buf[:n]); err != nil {
			return
		}
	}
}

func (p *flapProxy) blackhole() { p.gen.Add(1) }

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func linkOf(m *Manager, peer string) *connState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.conns[peer]
}

func connectedTo(m *Manager, peer string) bool {
	for _, c := range m.Connections() {
		if c.NodeID == peer && c.Status == StatusConnected {
			return true
		}
	}
	return false
}

var dbg = func() any { return nil }

func waitCond(t *testing.T, d time.Duration, what string, f func() bool) time.Duration {
	t.Helper()
	t0 := time.Now()
	for time.Since(t0) < d {
		if f() {
			return time.Since(t0)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout after %v waiting for %s (debug: %+v)", d, what, dbg())
	return 0
}

// TestLinkFlapReconnects: two paired peers on real loopback listeners, linked through a proxy. A
// simulated NIC link reset (half-open, no FIN) is detected by the ping timeout on BOTH ends, and
// the lower-id peer re-dials its last-known address (no mDNS) until the link is back.
func TestLinkFlapReconnects(t *testing.T) {
	oldEvery, oldTO, oldBase, oldTick := pingEvery, pongTimeout, redialBase, redialTick
	pingEvery, pongTimeout, redialBase, redialTick = 100*time.Millisecond, 500*time.Millisecond, 200*time.Millisecond, 50*time.Millisecond
	defer func() { pingEvery, pongTimeout, redialBase, redialTick = oldEvery, oldTO, oldBase, oldTick }()
	t.Setenv("RAVE_MATE_PEER_BIND", "127.0.0.1")

	mA, _ := mkPairMgr(t, "A")
	mB, _ := mkPairMgr(t, "B")
	sasA, sasB := drivePair(t, mA, mB)
	recvSAS(t, sasA)
	recvSAS(t, sasB)
	mA.ConfirmSAS(mB.id.NodeID, true)
	mB.ConfirmSAS(mA.id.NodeID, true)
	waitCond(t, 3*time.Second, "pipe pairing", func() bool { return connectedTo(mA, mB.id.NodeID) && connectedTo(mB, mA.id.NodeID) })
	for _, m := range []*Manager{mA, mB} { // drop the in-memory pairing links
		for _, c := range m.Connections() {
			m.mu.Lock()
			cs := m.conns[c.NodeID]
			m.mu.Unlock()
			cs.cancel()
			cs.link.Close()
		}
	}

	// The lower node id is the re-dialer; it reaches the other through the proxy.
	dialer, listener := mA, mB
	if mB.id.NodeID < mA.id.NodeID {
		dialer, listener = mB, mA
	}
	ctx := t.Context()
	pD, pL := freePort(t), freePort(t)
	t.Setenv("RAVE_MATE_PEER_PORTS", strconv.Itoa(pL))
	if err := listener.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer listener.Stop()
	t.Setenv("RAVE_MATE_PEER_PORTS", strconv.Itoa(pD))
	if err := dialer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer dialer.Stop()
	px := newFlapProxy(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(pL)))
	dialer.ConnectAddr(px.ln.Addr().String())
	waitCond(t, 5*time.Second, "link up via proxy", func() bool {
		return connectedTo(dialer, listener.id.NodeID) && connectedTo(listener, dialer.id.NodeID)
	})
	if p, _ := dialer.store.Get(listener.id.NodeID); p.DialAddress != px.ln.Addr().String() {
		t.Fatalf("dial address not remembered: %q", p.DialAddress)
	}

	dbg = func() any {
		return []any{dialer.Connections(), listener.Connections()}
	}
	for flap := 1; flap <= 3; flap++ {
		oldD, oldL := linkOf(dialer, listener.id.NodeID), linkOf(listener, dialer.id.NodeID)
		px.blackhole()
		// Success = BOTH ends replaced the dead link with a fresh, connected one (proves the dead link
		// was detected on each side and the re-dial restored it; "both down" can be shorter than a poll).
		d := waitCond(t, 5*time.Second, "dead link replaced on both ends", func() bool {
			nd, nl := linkOf(dialer, listener.id.NodeID), linkOf(listener, dialer.id.NodeID)
			return nd != nil && nl != nil && nd != oldD && nl != oldL &&
				connectedTo(dialer, listener.id.NodeID) && connectedTo(listener, dialer.id.NodeID)
		})
		t.Logf("flap %d: half-open link detected + re-dialed in %v (ping timeout %v)", flap, d, pongTimeout)
	}
}
