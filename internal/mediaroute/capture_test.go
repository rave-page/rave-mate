package mediaroute

import (
	"context"
	"image"
	"runtime"
	"sync"
	"testing"
	"time"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/videoshare"
)

// fakeRecv is a FrameReceiver whose frames the test pushes by hand.
type fakeRecv struct {
	ch chan *image.NRGBA

	mu     sync.Mutex
	rates  []float64 // SetMaxFPS history
	closes int
}

func newFakeRecv() *fakeRecv { return &fakeRecv{ch: make(chan *image.NRGBA, 8)} }

func (r *fakeRecv) Frames() <-chan *image.NRGBA { return r.ch }

func (r *fakeRecv) Close() {
	r.mu.Lock()
	r.closes++
	n := r.closes
	r.mu.Unlock()
	if n == 1 {
		close(r.ch)
	}
}

func (r *fakeRecv) SetMaxFPS(fps float64) {
	r.mu.Lock()
	r.rates = append(r.rates, fps)
	r.mu.Unlock()
}

func (r *fakeRecv) rate() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.rates) == 0 {
		return -1
	}
	return r.rates[len(r.rates)-1]
}

func (r *fakeRecv) closed() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closes
}

// poisonPool is the pool-safety oracle: it tracks every buffer it handed out and FAILS on a second
// release of the same buffer (double release = two routes writing one recycled buffer) and on any
// buffer that never comes back (leak - the pool starves and the capture path re-allocates 8 MB a
// frame, the GC churn the pool exists to remove).
type poisonPool struct {
	t *testing.T

	mu    sync.Mutex
	freed map[*byte]int
	given []*byte
}

func newPoisonPool(t *testing.T) *poisonPool {
	return &poisonPool{t: t, freed: map[*byte]int{}}
}

func (p *poisonPool) alloc(n int) []byte {
	b := make([]byte, n)
	p.mu.Lock()
	p.given = append(p.given, &b[0])
	p.mu.Unlock()
	return b
}

func (p *poisonPool) put(b []byte) {
	if len(b) == 0 {
		p.t.Error("released an empty buffer")
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	k := &b[0]
	p.freed[k]++
	if p.freed[k] > 1 {
		p.t.Errorf("POISON: buffer released %d times (double release)", p.freed[k])
	}
}

// settled waits until every handed-out buffer came back exactly once.
func (p *poisonPool) settled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, k := range p.given {
		if p.freed[k] != 1 {
			return false
		}
	}
	return true
}

func (p *poisonPool) report() (given, freedOnce, missing, doubled int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	given = len(p.given)
	for _, k := range p.given {
		switch n := p.freed[k]; {
		case n == 1:
			freedOnce++
		case n == 0:
			missing++
		default:
			doubled++
		}
	}
	return
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// frame builds one pooled capture frame (the receiver hands the readback buffer over as-is).
func (p *poisonPool) frame(w, h int) *image.NRGBA {
	return &image.NRGBA{Pix: p.alloc(w * h * 4), Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
}

// TestCaptureFanoutOneReadbackManyRoutes: 3 routes on one sender = ONE receiver, and each route
// sees every frame from that single readback.
func TestCaptureFanoutOneReadbackManyRoutes(t *testing.T) {
	pool := newPoisonPool(t)
	recv := newFakeRecv()
	opens := 0
	hub := newCaptureHub(logbus.New(64), func(string, float64) (videoshare.FrameReceiver, error) {
		opens++
		return recv, nil
	}, pool.put)

	var subs []*captureSub
	for i := 0; i < 3; i++ {
		s, err := hub.attach("OBS", 60)
		if err != nil {
			t.Fatalf("attach %d: %v", i, err)
		}
		subs = append(subs, s)
	}
	if opens != 1 {
		t.Fatalf("opened %d receivers for 3 routes, want 1", opens)
	}
	if hub.live() != 1 {
		t.Fatalf("hub.live() = %d, want 1", hub.live())
	}
	if got := hub.sharedNames(); len(got) != 1 || got[0] != "OBS" {
		t.Fatalf("sharedNames() = %v, want [OBS]", got)
	}

	recv.ch <- pool.frame(4, 4)
	ctx := context.Background()
	for i, s := range subs {
		src := &spoutSource{feed: s}
		f, err := src.Next(ctx)
		if err != nil {
			t.Fatalf("route %d Next: %v", i, err)
		}
		if len(f.Payload) != 4*4*4 {
			t.Fatalf("route %d payload %d bytes", i, len(f.Payload))
		}
		f.Release()
	}
	// One buffer, three refs, released exactly once by the third route.
	waitFor(t, "the shared buffer to return to the pool once", pool.settled)
	for _, s := range subs {
		s.close()
	}
	if hub.live() != 0 {
		t.Fatalf("hub.live() = %d after every route closed, want 0", hub.live())
	}
}

// TestCaptureRateIsFastestSubscriber: capture runs at the highest requested rate; detaching the
// fastest route drops it back, and one uncapped route makes the capture uncapped.
func TestCaptureRateIsFastestSubscriber(t *testing.T) {
	pool := newPoisonPool(t)
	recv := newFakeRecv()
	hub := newCaptureHub(logbus.New(64), func(string, float64) (videoshare.FrameReceiver, error) { return recv, nil }, pool.put)

	slow, err := hub.attach("OBS", 30)
	if err != nil {
		t.Fatal(err)
	}
	if got := recv.rate(); got != 30 {
		t.Fatalf("capture rate with one 30 fps route = %v, want 30", got)
	}
	fast, err := hub.attach("OBS", 60)
	if err != nil {
		t.Fatal(err)
	}
	if got := recv.rate(); got != 60 {
		t.Fatalf("capture rate with 30+60 routes = %v, want 60", got)
	}
	fast.close()
	if got := recv.rate(); got != 30 {
		t.Fatalf("capture rate after the 60 fps route left = %v, want 30", got)
	}
	un, err := hub.attach("OBS", 0) // uncapped
	if err != nil {
		t.Fatal(err)
	}
	if got := recv.rate(); got != 0 {
		t.Fatalf("capture rate with an uncapped route = %v, want 0 (uncapped)", got)
	}
	slow.close()
	un.close()
}

// TestCaptureLifecycleRefcount: the receiver stops only when the LAST route leaves, and a later
// route opens a fresh one.
func TestCaptureLifecycleRefcount(t *testing.T) {
	pool := newPoisonPool(t)
	first, second := newFakeRecv(), newFakeRecv()
	opens := 0
	hub := newCaptureHub(logbus.New(64), func(string, float64) (videoshare.FrameReceiver, error) {
		opens++
		if opens == 1 {
			return first, nil
		}
		return second, nil
	}, pool.put)

	var subs []*captureSub
	for i := 0; i < 3; i++ {
		s, err := hub.attach("TD", 60)
		if err != nil {
			t.Fatal(err)
		}
		subs = append(subs, s)
	}
	subs[0].close()
	subs[1].close()
	if first.closed() != 0 {
		t.Fatal("receiver closed while a route was still attached")
	}
	if hub.live() != 1 {
		t.Fatalf("hub.live() = %d after 2 of 3 routes left, want 1", hub.live())
	}
	subs[2].close()
	if first.closed() != 1 {
		t.Fatalf("receiver closes = %d after the last route left, want 1", first.closed())
	}
	if hub.live() != 0 {
		t.Fatalf("hub.live() = %d after teardown, want 0", hub.live())
	}
	// Reopen works and gets a NEW receiver.
	s, err := hub.attach("TD", 60)
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	if opens != 2 {
		t.Fatalf("reattach opened %d receivers total, want 2", opens)
	}
	second.ch <- pool.frame(2, 2)
	src := &spoutSource{feed: s}
	f, err := src.Next(context.Background())
	if err != nil {
		t.Fatalf("Next after reopen: %v", err)
	}
	f.Release()
	s.close()
	waitFor(t, "all buffers recycled", pool.settled)
}

// TestCaptureSubClosePendingReleased: a route that closes with a frame still in its slot returns
// that buffer to the pool (the leak that starved the pool on every route teardown).
func TestCaptureSubClosePendingReleased(t *testing.T) {
	pool := newPoisonPool(t)
	recv := newFakeRecv()
	hub := newCaptureHub(logbus.New(64), func(string, float64) (videoshare.FrameReceiver, error) { return recv, nil }, pool.put)
	s, err := hub.attach("OBS", 60)
	if err != nil {
		t.Fatal(err)
	}
	recv.ch <- pool.frame(8, 8)
	waitFor(t, "the frame to land in the route's slot", func() bool { return len(s.ch) == 1 })
	s.close() // never consumed
	waitFor(t, "the pending buffer to return to the pool", pool.settled)
}

// TestCaptureNewestWinsReleasesDisplaced: a route whose encoder is behind drops the STALE frame and
// releases it - the slot never queues and the pool never loses a buffer.
func TestCaptureNewestWinsReleasesDisplaced(t *testing.T) {
	pool := newPoisonPool(t)
	recv := newFakeRecv()
	hub := newCaptureHub(logbus.New(64), func(string, float64) (videoshare.FrameReceiver, error) { return recv, nil }, pool.put)
	s, err := hub.attach("OBS", 60)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		recv.ch <- pool.frame(4, 4)
	}
	// 5 frames in, 1 slot: 4 must come back to the pool without ever being consumed.
	waitFor(t, "4 displaced frames recycled", func() bool {
		_, freed, _, _ := pool.report()
		return freed >= 4
	})
	src := &spoutSource{feed: s}
	f, err := src.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.Release()
	s.close()
	waitFor(t, "all 5 buffers recycled exactly once", pool.settled)
	given, freed, missing, doubled := pool.report()
	if given != 5 || freed != 5 || missing != 0 || doubled != 0 {
		t.Fatalf("pool: given=%d freedOnce=%d missing=%d doubled=%d", given, freed, missing, doubled)
	}
}

// TestSpoutSourceFPSGateReleases: over-budget frames are dropped at the route with their reference
// released (never held, never double-released).
func TestSpoutSourceFPSGateReleases(t *testing.T) {
	pool := newPoisonPool(t)
	recv := newFakeRecv()
	hub := newCaptureHub(logbus.New(64), func(string, float64) (videoshare.FrameReceiver, error) { return recv, nil }, pool.put)
	s, err := hub.attach("OBS", 60)
	if err != nil {
		t.Fatal(err)
	}
	src := &spoutSource{feed: s, minGap: time.Hour} // everything after the first is over budget
	recv.ch <- pool.frame(2, 2)
	f, err := src.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.Release()
	// Next frame is inside the gap: Next must drop it (and release it), then block for a newer one.
	recv.ch <- pool.frame(2, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := src.Next(ctx); err == nil {
		t.Fatal("Next returned an over-budget frame, want it dropped")
	}
	waitFor(t, "the dropped frame to return to the pool", pool.settled)
	// That discard is the fps CAP doing its job. It must reach the reporter tagged as such: as a
	// bare drop it is indistinguishable from real loss, and on a 60→40 fps route it accumulates
	// forever ("dropped 41902 and climbing" on a perfectly healthy route).
	ps := src.PipeStats()
	if ps.RateCapped != 1 {
		t.Fatalf("RateCapped = %d, want 1", ps.RateCapped)
	}
	if ps.RealDrops() != 0 {
		t.Fatalf("RealDrops = %d, want 0 - nothing was lost, one frame was throttled", ps.RealDrops())
	}
	s.close()
}

// TestCaptureChurnNoDoubleRelease is the poison test under churn: routes attach, consume and detach
// while frames keep flowing. Every buffer must come back to the pool exactly once.
func TestCaptureChurnNoDoubleRelease(t *testing.T) {
	pool := newPoisonPool(t)
	recvs := []*fakeRecv{}
	var rmu sync.Mutex
	hub := newCaptureHub(logbus.New(256), func(string, float64) (videoshare.FrameReceiver, error) {
		r := newFakeRecv()
		rmu.Lock()
		recvs = append(recvs, r)
		rmu.Unlock()
		return r, nil
	}, pool.put)

	cur := func() *fakeRecv {
		rmu.Lock()
		defer rmu.Unlock()
		return recvs[len(recvs)-1]
	}

	var wg sync.WaitGroup
	for round := 0; round < 20; round++ {
		var subs []*captureSub
		for i := 0; i < 3; i++ {
			s, err := hub.attach("OBS", float64(30*(i+1)))
			if err != nil {
				t.Fatal(err)
			}
			subs = append(subs, s)
			wg.Add(1)
			go func(s *captureSub) { // a route consuming + releasing like runSend does
				defer wg.Done()
				src := &spoutSource{feed: s}
				for {
					f, err := src.Next(context.Background())
					if err != nil {
						return
					}
					f.Release()
				}
			}(s)
		}
		r := cur()
		for i := 0; i < 10; i++ {
			r.ch <- pool.frame(4, 4)
		}
		for _, s := range subs {
			s.close()
		}
	}
	wg.Wait()
	waitFor(t, "every buffer to return to the pool exactly once", pool.settled)
	given, freed, missing, doubled := pool.report()
	if missing != 0 || doubled != 0 {
		t.Fatalf("pool churn: given=%d freedOnce=%d missing=%d doubled=%d", given, freed, missing, doubled)
	}
	t.Logf("churn: %d buffers, all recycled exactly once", given)
}

// TestCapture4KSteadyStateNoAllocs is the OOM regression on the capture fan-out: drive N
// synthetic 3840x2160 frames through hub → captureSub → spoutSource (what runSend consumes)
// against the REAL videoshare pool and assert steady-state allocations near zero per frame.
// A single missed frame buffer is 31.6 MiB - at 60 fps that is 2 GB/s of garbage, which is
// what made the media child lose to its GOMEMLIMIT and die under the 2 GB job cap.
func TestCapture4KSteadyStateNoAllocs(t *testing.T) {
	const w, h = 3840, 2160
	const size = w * h * 4
	const frames = 120

	recv := newFakeRecv()
	hub := newCaptureHub(logbus.New(64), func(string, float64) (videoshare.FrameReceiver, error) {
		return recv, nil
	}, videoshare.PutPix) // the real pool, not the poison seam
	sub, err := hub.attach("OBS", 60)
	if err != nil {
		t.Fatal(err)
	}
	src := &spoutSource{feed: sub}
	ctx := context.Background()

	// The receiver hands over its readback buffer and takes a fresh pooled one; model that
	// exactly, so the pool sees the production get/put pattern.
	pump := func() {
		b := videoshare.GetPixForTest(size)
		recv.ch <- &image.NRGBA{Pix: b, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}
	}
	pump() // prime
	f, err := src.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.Release()

	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	for i := 0; i < frames; i++ {
		pump()
		f, err := src.Next(ctx)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if len(f.Payload) != size {
			t.Fatalf("frame %d payload %d bytes, want %d", i, len(f.Payload), size)
		}
		f.Release()
	}
	runtime.ReadMemStats(&m1)
	perFrame := float64(m1.TotalAlloc-m0.TotalAlloc) / frames
	if perFrame > 16*1024 { // bookkeeping only; a missed frame buffer is 31.6 MiB
		t.Fatalf("capture path allocated %.0f B/frame over %d 4K frames, want ~0 (one frame is %d B)",
			perFrame, frames, size)
	}
	live, idle, bufs := videoshare.PoolStats()
	t.Logf("4K fan-out: %.0f B/frame over %d frames; pool live=%dMB idle=%dMB bufs=%d",
		perFrame, frames, live>>20, idle>>20, bufs)
	sub.close()
}
