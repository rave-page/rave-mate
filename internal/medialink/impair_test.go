package medialink

import (
	"math/rand"
	"net"
	"sync"
	"time"
)

// impair_test.go is the in-process network-impairment shim (Unit 2): a pair of memConn packetConns
// that model loss %, reorder, jitter, duplication and a bandwidth cap deterministically (seeded
// RNG), so the UDP conn + congestion controller are exercised without real sockets or hardware.

// memAddr is a trivial net.Addr for the in-memory fabric.
type memAddr string

func (a memAddr) Network() string { return "mem" }
func (a memAddr) String() string  { return string(a) }

// impairment parameters for one direction of the link.
type impairment struct {
	mu         sync.Mutex
	rng        *rand.Rand
	loss       float64       // [0,1] drop probability
	dup        float64       // [0,1] duplicate probability
	reorder    float64       // [0,1] extra-delay probability (forces reordering)
	jitter     time.Duration // uniform random added delay [0,jitter)
	reorderExt time.Duration // extra delay applied to reordered packets
	bw         float64       // bytes/sec serialization cap (0 = unlimited)
	linkFree   time.Time     // next free instant on the serialized link
}

func newImpairment(seed int64) *impairment {
	return &impairment{rng: rand.New(rand.NewSource(seed)), linkFree: time.Now()}
}

// plan decides this packet's fate: drop, duplicate, and the delivery delay (serialization + jitter).
func (im *impairment) plan(n int) (drop, dupe bool, delay time.Duration) {
	im.mu.Lock()
	defer im.mu.Unlock()
	if im.loss > 0 && im.rng.Float64() < im.loss {
		return true, false, 0
	}
	now := time.Now()
	if im.bw > 0 { // serialize: the link is busy until linkFree
		if im.linkFree.Before(now) {
			im.linkFree = now
		}
		serial := time.Duration(float64(n) / im.bw * float64(time.Second))
		im.linkFree = im.linkFree.Add(serial)
		delay = im.linkFree.Sub(now)
	}
	if im.jitter > 0 {
		delay += time.Duration(im.rng.Int63n(int64(im.jitter)))
	}
	if im.reorder > 0 && im.rng.Float64() < im.reorder {
		delay += im.reorderExt
	}
	dupe = im.dup > 0 && im.rng.Float64() < im.dup
	return false, dupe, delay
}

// memConn is one endpoint of the in-memory impaired link. It satisfies packetConn.
type memConn struct {
	self   memAddr
	peer   *memConn
	impair *impairment // applied to packets THIS conn sends (toward peer)
	inbox  chan []byte

	mu       sync.Mutex
	deadline time.Time
	closed   bool
	done     chan struct{}
}

// newMemPair builds two endpoints wired to each other, each with its own send-direction impairment.
func newMemPair(aToB, bToA *impairment) (a, b *memConn) {
	a = &memConn{self: "A", impair: aToB, inbox: make(chan []byte, 4096), done: make(chan struct{})}
	b = &memConn{self: "B", impair: bToA, inbox: make(chan []byte, 4096), done: make(chan struct{})}
	a.peer, b.peer = b, a
	return a, b
}

func (c *memConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return 0, net.ErrClosed
	}
	drop, dupe, delay := c.impair.plan(len(p))
	if drop {
		return len(p), nil // "sent" but lost on the wire
	}
	deliver := func() {
		cp := append([]byte(nil), p...)
		select {
		case c.peer.inbox <- cp:
		case <-c.peer.done:
		default: // peer inbox full: a real socket would drop here too
		}
	}
	send := func() {
		deliver()
		if dupe {
			deliver()
		}
	}
	if delay > 0 {
		time.AfterFunc(delay, send)
	} else {
		send()
	}
	return len(p), nil
}

func (c *memConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		c.mu.Lock()
		dl := c.deadline
		c.mu.Unlock()
		var timer *time.Timer
		var timeout <-chan time.Time
		if !dl.IsZero() {
			d := time.Until(dl)
			if d <= 0 {
				return 0, nil, timeoutErr{}
			}
			timer = time.NewTimer(d)
			timeout = timer.C
		}
		select {
		case b := <-c.inbox:
			if timer != nil {
				timer.Stop()
			}
			n := copy(p, b)
			return n, c.peer.self, nil
		case <-c.done:
			if timer != nil {
				timer.Stop()
			}
			return 0, nil, net.ErrClosed
		case <-timeout:
			return 0, nil, timeoutErr{}
		}
	}
}

func (c *memConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.deadline = t
	c.mu.Unlock()
	return nil
}

func (c *memConn) Close() error {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.done)
	}
	c.mu.Unlock()
	return nil
}

// timeoutErr is a net.Error reporting a (recoverable) read timeout.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }
