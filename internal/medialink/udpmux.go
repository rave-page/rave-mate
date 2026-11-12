package medialink

import (
	"encoding/binary"
	"net"
	"sync"
	"time"
)

// udpmux.go demultiplexes the ONE shared UDP media socket (bound on the same port the TCP listener
// picked, 47641-5) to per-route virtual packetConns, keyed by the per-route token (routeToken). The
// well-known answerer port fields datagrams from many peers; the dialer uses a dedicated socket per
// route (one socket = one remote, no demux).
//
// Security (finding 2): a datagram for a PENDING route is handed to onFirst, which AUTHENTICATES it
// (AEAD open / token+mode) BEFORE any state is created or the pending answer is claimed. An
// unauthenticated first datagram creates nothing and never binds a remote; the pending registration
// survives so a later legitimate datagram still starts the route. The send target binds only from an
// authenticated datagram (inside the udpConn), so an off-path attacker without the per-route token
// can neither hijack nor consume a pending route. The pending table is bounded (populated only by our
// own onOffer, capped by maxRoutes) and expires with the pending answer's TTL.

// muxInbox bounds a route's pending-datagram queue (drop-oldest; the udpConn's own loss machinery
// recovers, and a stalled consumer must never let the queue grow).
const muxInbox = 512

// muxDatagram is one received datagram + its source (so the udpConn binds its send target from the
// AUTHENTICATED source, not whatever arrived first).
type muxDatagram struct {
	buf []byte
	src *net.UDPAddr
}

// muxConn is one route's virtual packetConn over the shared socket. It holds NO remote: the udpConn
// owns the send target (bound on authentication) and passes it to WriteTo.
type muxConn struct {
	mux    *udpMux
	token  uint32
	inbox  chan muxDatagram
	closed chan struct{}
	once   sync.Once
}

func (c *muxConn) ReadFrom(p []byte) (int, net.Addr, error) {
	select {
	case d := <-c.inbox:
		n := copy(p, d.buf)
		if d.src != nil {
			return n, d.src, nil
		}
		return n, nil, nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
}

// WriteTo sends to the target the udpConn passes (its authenticated remote); a nil/foreign addr is
// dropped (the answerer never sends before the handshake binds a remote).
func (c *muxConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	ua, ok := addr.(*net.UDPAddr)
	if !ok || ua == nil {
		return len(p), nil
	}
	return c.mux.pc.WriteToUDP(p, ua)
}

// SetReadDeadline is a no-op: muxConn.ReadFrom is inbox-driven and unblocked by Close.
func (c *muxConn) SetReadDeadline(time.Time) error { return nil }

func (c *muxConn) Close() error {
	c.once.Do(func() {
		close(c.closed)
		c.mux.remove(c.token)
	})
	return nil
}

// deliver hands a datagram + its source to the route, drop-oldest on overflow (bounded).
func (c *muxConn) deliver(buf []byte, src *net.UDPAddr) {
	d := muxDatagram{buf: buf, src: src}
	select {
	case c.inbox <- d:
	default:
		select {
		case <-c.inbox:
		default:
		}
		select {
		case c.inbox <- d:
		default:
		}
	}
}

// udpMux owns the shared answerer UDP socket + the token→route demux. onFirst authenticates the first
// datagram for a pending token and, only on success + a won claim, builds + returns the route's
// muxConn (nil = unauthenticated, claim lost, or not viable → no state).
type udpMux struct {
	pc      *net.UDPConn
	mu      sync.Mutex
	active  map[uint32]*muxConn
	pending map[uint32]string // token → session (awaiting the first AUTHENTICATED datagram)
	onFirst func(token uint32, session string, src *net.UDPAddr, datagram []byte) *muxConn
	done    chan struct{}
	once    sync.Once
}

func newUDPMux(pc *net.UDPConn, onFirst func(uint32, string, *net.UDPAddr, []byte) *muxConn) *udpMux {
	m := &udpMux{pc: pc, active: map[uint32]*muxConn{}, pending: map[uint32]string{},
		onFirst: onFirst, done: make(chan struct{})}
	_ = pc.SetReadBuffer(udpReadBufBytes)
	_ = pc.SetWriteBuffer(udpReadBufBytes)
	go m.readLoop()
	return m
}

// expect registers a token→session so the first AUTHENTICATED datagram starts the route (answerer).
func (m *udpMux) expect(token uint32, session string) {
	m.mu.Lock()
	m.pending[token] = session
	m.mu.Unlock()
}

// unexpect drops a pending registration (answer claimed / expired).
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

// newMuxConn builds a virtual conn for a route over this socket. The udpConn binds the remote on
// authentication; the muxConn holds none.
func (m *udpMux) newMuxConn(token uint32) *muxConn {
	return &muxConn{mux: m, token: token, inbox: make(chan muxDatagram, muxInbox), closed: make(chan struct{})}
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
		if mc == nil {
			if !waiting {
				continue // unknown token (off-path / stray): no state, no work
			}
			// First datagram for a pending route: authenticate + claim inside onFirst (it feeds this
			// datagram to the new route on success). On failure the pending registration survives so a
			// later legitimate datagram retries; the loop is single-goroutine so there is no re-entrancy.
			mc = m.onFirst(token, session, cloneUDPAddr(src), append([]byte(nil), buf[:n]...))
			if mc != nil {
				m.mu.Lock()
				m.active[token] = mc
				m.mu.Unlock()
			}
			continue // first datagram handled (fed) inside onFirst
		}
		mc.deliver(append([]byte(nil), buf[:n]...), cloneUDPAddr(src))
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
