package medialink

import (
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// udpmux.go demultiplexes the ONE shared UDP media socket (bound on the same port the TCP listener
// picked, 47641-5) to per-route virtual packetConns, keyed by the per-route token (routeToken). The
// well-known answerer port fields datagrams from many peers; the dialer uses a dedicated socket per
// route (one socket = one remote, no demux). Every buffer here is bounded.

// muxInbox bounds a route's pending-datagram queue (drop-oldest; the udpConn's own loss machinery
// recovers, and a stalled consumer must never let the queue grow).
const muxInbox = 512

// muxConn is one route's virtual packetConn over the shared socket. The answerer learns the remote
// from the first datagram; writes go back to it.
type muxConn struct {
	mux    *udpMux
	token  uint32
	inbox  chan []byte
	remote atomic.Pointer[net.UDPAddr]
	closed chan struct{}
	once   sync.Once
}

func (c *muxConn) ReadFrom(p []byte) (int, net.Addr, error) {
	select {
	case b := <-c.inbox:
		n := copy(p, b)
		if r := c.remote.Load(); r != nil {
			return n, r, nil
		}
		return n, nil, nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
}

func (c *muxConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	r := c.remote.Load()
	if r == nil {
		return len(p), nil // remote not learned yet (answerer, pre-first-datagram): drop, never block
	}
	return c.mux.pc.WriteToUDP(p, r)
}

// SetReadDeadline is a no-op: muxConn.ReadFrom is inbox-driven and unblocked by Close, which is all
// the udpConn recv loop needs.
func (c *muxConn) SetReadDeadline(time.Time) error { return nil }

func (c *muxConn) Close() error {
	c.once.Do(func() {
		close(c.closed)
		c.mux.remove(c.token)
	})
	return nil
}

// deliver hands a datagram copy to the route, drop-oldest on overflow (bounded).
func (c *muxConn) deliver(b []byte) {
	select {
	case c.inbox <- b:
	default:
		select {
		case <-c.inbox:
		default:
		}
		select {
		case c.inbox <- b:
		default:
		}
	}
}

// udpMux owns the shared answerer UDP socket + the token→route demux. onFirst is the RouteManager
// callback invoked on the first datagram for a registered-but-not-yet-active token: it claims the
// pending answer, builds the route's udpConn over a fresh muxConn, starts the source pump, and
// returns the muxConn (nil = claim lost to the TCP fallback / expired).
type udpMux struct {
	pc      *net.UDPConn
	mu      sync.Mutex
	active  map[uint32]*muxConn
	pending map[uint32]string // token → session (awaiting first datagram)
	onFirst func(token uint32, session string, remote *net.UDPAddr) *muxConn
	done    chan struct{}
	once    sync.Once
}

func newUDPMux(pc *net.UDPConn, onFirst func(uint32, string, *net.UDPAddr) *muxConn) *udpMux {
	m := &udpMux{pc: pc, active: map[uint32]*muxConn{}, pending: map[uint32]string{},
		onFirst: onFirst, done: make(chan struct{})}
	_ = pc.SetReadBuffer(udpReadBufBytes)
	_ = pc.SetWriteBuffer(udpReadBufBytes)
	go m.readLoop()
	return m
}

// expect registers a token→session so the first matching datagram starts the route (answerer side).
func (m *udpMux) expect(token uint32, session string) {
	m.mu.Lock()
	m.pending[token] = session
	m.mu.Unlock()
}

// unexpect drops a pending registration (answer expired / route claimed elsewhere).
func (m *udpMux) unexpect(token uint32) {
	m.mu.Lock()
	delete(m.pending, token)
	m.mu.Unlock()
}

func (m *udpMux) remove(token uint32) {
	m.mu.Lock()
	delete(m.active, token)
	delete(m.pending, token)
	m.mu.Unlock()
}

// newMuxConn builds a virtual conn for a route over this socket with a known remote (used by onFirst
// and by the dialer? - dialer uses its own socket). remote may be set later via the datagram source.
func (m *udpMux) newMuxConn(token uint32, remote *net.UDPAddr) *muxConn {
	c := &muxConn{mux: m, token: token, inbox: make(chan []byte, muxInbox), closed: make(chan struct{})}
	if remote != nil {
		c.remote.Store(remote)
	}
	return c
}

func (m *udpMux) Close() {
	m.once.Do(func() { close(m.done); _ = m.pc.Close() })
}

func (m *udpMux) readLoop() {
	buf := make([]byte, dgMaxPayload+dgHeaderLen+64)
	for {
		n, src, err := m.pc.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-m.done:
				return
			default:
				continue
			}
		}
		if n < dgHeaderLen {
			continue // too short to carry a token - ignore
		}
		token := binary.BigEndian.Uint32(buf[2:6]) // Token field (offset 2) in dgHeader
		m.mu.Lock()
		mc := m.active[token]
		session, waiting := "", false
		if mc == nil {
			session, waiting = m.pending[token]
		}
		m.mu.Unlock()
		if mc == nil && waiting {
			// First datagram for a pending route: claim + build (outside the lock - onFirst takes it).
			mc = m.onFirst(token, session, cloneUDPAddr(src))
			m.mu.Lock()
			if mc != nil {
				m.active[token] = mc
			}
			delete(m.pending, token)
			m.mu.Unlock()
		}
		if mc == nil {
			continue
		}
		mc.deliver(append([]byte(nil), buf[:n]...))
	}
}

func cloneUDPAddr(a *net.UDPAddr) *net.UDPAddr {
	if a == nil {
		return nil
	}
	ip := make(net.IP, len(a.IP))
	copy(ip, a.IP)
	return &net.UDPAddr{IP: ip, Port: a.Port, Zone: a.Zone}
}

var _ packetConn = (*muxConn)(nil)
