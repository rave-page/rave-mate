package webcam

import (
	"testing"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/videoshare"
)

// capture_pool_test.go - the allocation-regression gate for the produce path. framepipe used to
// allocate a FRESH full frame per capture ("never reused"), ~250 MB/s of garbage at 1080p30 and the
// opposite policy from the rest of the media plane. Buffers now come from the bounded pixel pool,
// so the oracle is: a steady capture RECYCLES rather than allocating, and every buffer comes back.

func testCapture(t *testing.T, w, h int) *capture {
	t.Helper()
	size, err := frameSize(w, h)
	if err != nil {
		t.Fatal(err)
	}
	return &capture{log: logbus.New(16), size: size, frames: make(chan capFrame, 1)}
}

// TestCaptureRecyclesBuffers: over many frames with a consumer keeping up, the pool hands the SAME
// buffers back rather than allocating a new one per frame.
func TestCaptureRecyclesBuffers(t *testing.T) {
	const w, h, frames = 320, 180, 200
	c := testCapture(t, w, h)
	seen := map[*byte]int{}
	for i := 0; i < frames; i++ {
		buf := c.allocFrame()
		if len(buf) != c.size {
			t.Fatalf("buffer %d bytes, want %d", len(buf), c.size)
		}
		seen[&buf[0]]++
		c.deliver(buf)
		cf, err := c.next(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		cf.release() // the distributor's reference, no taps attached
	}
	if len(seen) > 4 {
		t.Fatalf("%d distinct buffers for %d frames - the pool is not recycling", len(seen), frames)
	}
	if got := c.poolMiss.Load(); got != 0 {
		t.Fatalf("poolMiss=%d on a well-behaved capture", got)
	}
	if got := c.refBugs.Load(); got != 0 {
		t.Fatalf("refBugs=%d - a buffer was released past zero", got)
	}
}

// TestCaptureDroppedFramesComeBack: a producer outrunning its consumer must return the buffers it
// drops. Without that the pool's live ceiling creeps up until every frame allocates again.
func TestCaptureDroppedFramesComeBack(t *testing.T) {
	const w, h, frames = 320, 180, 300
	c := testCapture(t, w, h)
	live0, _, _ := videoshare.PoolStats()
	seen := map[*byte]int{}
	for i := 0; i < frames; i++ { // nothing ever reads c.frames except the displacement path
		buf := c.allocFrame()
		seen[&buf[0]]++
		c.deliver(buf)
	}
	if got := c.dropped.Load(); got == 0 {
		t.Fatal("no frames were dropped - the test is not exercising the displacement path")
	}
	// Drain the last pending frame the way Close does.
	if err := c.drainForTest(); err != nil {
		t.Fatal(err)
	}
	live1, _, _ := videoshare.PoolStats()
	if live1 != live0 {
		t.Fatalf("pool live bytes %d → %d: %d bytes never came back", live0, live1, live1-live0)
	}
	if len(seen) > 4 {
		t.Fatalf("%d distinct buffers for %d dropped frames - drops are not recycling", len(seen), frames)
	}
	if got := c.refBugs.Load(); got != 0 {
		t.Fatalf("refBugs=%d", got)
	}
}

// TestCaptureAllocFallsBackAtTheCeiling: when the pool refuses (live ceiling), the capture ALLOCATES
// rather than dropping every frame - a leaked reference must degrade the optimisation, never wedge a
// live camera.
func TestCaptureAllocFallsBackAtTheCeiling(t *testing.T) {
	c := testCapture(t, 320, 180)
	var held [][]byte
	t.Cleanup(func() {
		for _, b := range held {
			videoshare.PutPix(b)
		}
	})
	// Pin the pool at its ceiling with big buffers, then confirm the capture still gets a frame.
	for i := 0; i < 64; i++ {
		b, ok := videoshare.TryGetPix(3840 * 2160 * 4)
		if !ok {
			break
		}
		held = append(held, b)
	}
	if len(held) == 0 {
		t.Skip("could not reach the pool ceiling")
	}
	before := c.poolMiss.Load()
	buf := c.allocFrame()
	if len(buf) != c.size {
		t.Fatalf("fallback buffer is %d bytes, want %d", len(buf), c.size)
	}
	if c.poolMiss.Load() == before {
		t.Log("pool still had room for this geometry; fallback path not exercised")
	}
}
