package mediaroute

import (
	"image"
	"sort"
	"sync"
	"sync/atomic"

	"rave.page/mate/internal/debuglog"
	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/videoshare"
)

// capture.go - ONE capture per Spout source, fanned out to every route that wants it.
//
// Before: each accepted route opened its own FrameReceiver, i.e. its own GL context + poll loop +
// GPU→CPU readback of the SAME texture. Two peers on one 1080p60 source = 2× 500 MB/s of readback
// on the machine that is also encoding; N peers = N× (the melt multiplier called out in
// .devnotes/MEDIALINK_MELT_FIXES_2026-07-18.md "Still open").
//
// Semantics with differing per-route settings:
//   - FPS: capture runs at the HIGHEST rate any live subscriber asks for (uncapped wins). Each
//     route then drops down to its own cap downstream (spoutSource.minGap) - a slow route costs
//     nothing extra because the expensive readback already happened once. The rate is recomputed on
//     every attach/detach and pushed into the receiver live (videoshare.FPSLimiter).
//   - FORMAT: Spout capture has exactly one format (NRGBA at the sender's current dimensions) and
//     resize is detected by the receiver itself, so there is nothing per-route to reconcile here.
//     Per-route resolution/codec differences live further down the pipe (encode spec MaxHeight +
//     the negotiated codec), which is per route already.
//   - Buffers: one pooled pixel buffer serves N subscribers, refcounted (pixRef). The pool gets it
//     back exactly once, when the last subscriber releases - never before (a second reader would
//     see a recycled buffer being overwritten by the next readback) and never twice.
//
// Lifecycle: refcounted per sender name. Last subscriber detaching stops the poll loop and closes
// the receiver; a later attach opens a fresh one.

// pixRef is a pooled pixel buffer shared by N subscribers. Released exactly once, by the last one.
type pixRef struct {
	n   atomic.Int32
	pix []byte
	put func([]byte)
	log *logbus.Bus
}

// release drops one reference; the last one recycles the buffer. A release past zero is a bug
// (double release = two routes writing the same recycled buffer) - it is logged, never acted on.
func (p *pixRef) release() {
	switch left := p.n.Add(-1); {
	case left == 0:
		p.put(p.pix)
	case left < 0 && p.log != nil:
		p.log.Warn(source, "capture buffer released past zero (double release)",
			map[string]any{"refs": left, "bytes": len(p.pix)})
	}
}

// sharedFrame is one captured frame handed to a subscriber (image + its shared buffer ref).
type sharedFrame struct {
	img *image.NRGBA
	ref *pixRef
}

// captureFeed is a route's frame feed (satisfied by *captureSub; a seam for tests).
type captureFeed interface {
	frames() <-chan *sharedFrame
	close()
}

// captureHub owns the per-source captures. Safe for concurrent use.
type captureHub struct {
	log  *logbus.Bus
	open func(name string, maxFPS float64) (videoshare.FrameReceiver, error)
	put  func([]byte)

	mu   sync.Mutex
	caps map[string]*capture
}

func newCaptureHub(log *logbus.Bus, open func(string, float64) (videoshare.FrameReceiver, error), put func([]byte)) *captureHub {
	if open == nil {
		open = func(name string, maxFPS float64) (videoshare.FrameReceiver, error) {
			return videoshare.NewFrameReceiverOpts(log, name, videoshare.RecvOptions{MaxFPS: maxFPS})
		}
	}
	if put == nil {
		put = videoshare.PutPix
	}
	return &captureHub{log: log, open: open, put: put, caps: map[string]*capture{}}
}

// capture is one live receiver + its subscribers.
type capture struct {
	hub  *captureHub
	name string
	recv videoshare.FrameReceiver

	mu   sync.Mutex
	subs map[*captureSub]struct{}
}

// captureSub is one route's subscription. Frames are newest-wins with a 1-deep slot, exactly like
// the receiver's own delivery: a route whose encoder is behind drops frames instead of queuing.
type captureSub struct {
	cap    *capture
	maxFPS float64

	mu     sync.Mutex
	closed bool
	ch     chan *sharedFrame
}

// attach subscribes to name's capture, opening it on first use. maxFPS <= 0 = uncapped.
func (h *captureHub) attach(name string, maxFPS float64) (*captureSub, error) {
	h.mu.Lock()
	c := h.caps[name]
	if c == nil {
		recv, err := h.open(name, maxFPS)
		if err != nil {
			h.mu.Unlock()
			return nil, err
		}
		c = &capture{hub: h, name: name, recv: recv, subs: map[*captureSub]struct{}{}}
		h.caps[name] = c
		debuglog.Go(h.log, source, func() { c.fanout() })
	}
	h.mu.Unlock()

	s := &captureSub{cap: c, maxFPS: maxFPS, ch: make(chan *sharedFrame, 1)}
	c.mu.Lock()
	c.subs[s] = struct{}{}
	n := len(c.subs)
	c.mu.Unlock()
	c.rerate()
	if n > 1 {
		h.log.Info(source, "capture shared", map[string]any{"sender": name, "routes": n,
			"captureFps": c.rate()})
	}
	return s, nil
}

// live reports the number of open captures (telemetry + tests).
func (h *captureHub) live() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.caps)
}

// rate returns the capture rate = the fastest subscriber's (0 = uncapped).
func (c *capture) rate() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return rateOf(c.subs)
}

func rateOf(subs map[*captureSub]struct{}) float64 {
	max := 0.0
	for s := range subs {
		if s.maxFPS <= 0 {
			return 0 // one uncapped route ⇒ capture uncapped
		}
		if s.maxFPS > max {
			max = s.maxFPS
		}
	}
	return max
}

// rerate pushes the current fastest-subscriber rate into the receiver.
func (c *capture) rerate() {
	if l, ok := c.recv.(videoshare.FPSLimiter); ok {
		l.SetMaxFPS(c.rate())
	}
}

// fanout pumps the receiver into every subscriber, refcounting the pooled buffer. Ends when the
// receiver closes its channel (detach of the last subscriber).
func (c *capture) fanout() {
	for img := range c.recv.Frames() {
		c.mu.Lock()
		subs := make([]*captureSub, 0, len(c.subs))
		for s := range c.subs {
			subs = append(subs, s)
		}
		c.mu.Unlock()
		if len(subs) == 0 { // last route just left - recycle straight back
			c.hub.put(img.Pix)
			continue
		}
		ref := &pixRef{pix: img.Pix, put: c.hub.put, log: c.hub.log}
		ref.n.Store(int32(len(subs))) // one ref per subscriber; offer releases the ones it drops
		f := &sharedFrame{img: img, ref: ref}
		for _, s := range subs {
			s.offer(f)
		}
	}
}

// offer delivers newest-wins: a displaced pending frame releases its ref here (nobody consumed it).
func (s *captureSub) offer(f *sharedFrame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		f.ref.release() // detached between the snapshot and now
		return
	}
	for {
		select {
		case s.ch <- f:
			return
		default:
			select {
			case old := <-s.ch:
				old.ref.release()
			default:
			}
		}
	}
}

// frames implements captureFeed. Closed when the subscription ends (route sees EOF).
func (s *captureSub) frames() <-chan *sharedFrame { return s.ch }

// close ends this subscription, releasing anything still pending, and tears the capture down when
// it was the last one.
func (s *captureSub) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	for {
		select {
		case f := <-s.ch:
			f.ref.release()
			continue
		default:
		}
		break
	}
	close(s.ch)
	s.mu.Unlock()
	s.cap.detach(s)
}

// detach removes a subscriber; the last one out closes the capture.
func (c *capture) detach(s *captureSub) {
	c.mu.Lock()
	delete(c.subs, s)
	left := len(c.subs)
	c.mu.Unlock()
	if left > 0 {
		c.rerate() // the fastest route may have been the one that left
		return
	}
	// Last subscriber: drop the capture from the hub FIRST so a concurrent attach opens a fresh
	// one instead of racing this teardown, then stop the receiver (fanout drains + exits on the
	// channel close, recycling whatever is still in flight).
	c.hub.mu.Lock()
	if c.hub.caps[c.name] == c {
		delete(c.hub.caps, c.name)
	}
	c.hub.mu.Unlock()
	c.recv.Close()
}

// sharedNames lists the sources with more than one route (diagnostics).
func (h *captureHub) sharedNames() []string {
	h.mu.Lock()
	caps := make([]*capture, 0, len(h.caps))
	for _, c := range h.caps {
		caps = append(caps, c)
	}
	h.mu.Unlock()
	var out []string
	for _, c := range caps {
		c.mu.Lock()
		n := len(c.subs)
		c.mu.Unlock()
		if n > 1 {
			out = append(out, c.name)
		}
	}
	sort.Strings(out)
	return out
}
