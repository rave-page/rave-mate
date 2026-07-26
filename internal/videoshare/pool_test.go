package videoshare

import (
	"runtime"
	"testing"
)

const (
	fhd4KBytes = 3840 * 2160 * 4 // 31.6 MiB
	hdBytes    = 1280 * 720 * 4  // 3.5 MiB
)

// TestFrameBytesRefusesShimGarbage is the OOM regression: the Spout shim was measured
// reporting w=139846784 h=3840 for a 3840x2160 sender on a receiver's first poll, and
// sizing a buffer from it (2 TB) killed the media child with
// "fatal error: runtime: cannot allocate memory". No geometry from the shim may ever reach
// an allocator unvalidated.
func TestFrameBytesRefusesShimGarbage(t *testing.T) {
	bad := [][2]int{
		{139846784, 3840}, // the observed field value (2 TB)
		{0, 2160}, {3840, 0}, {-1, 1080}, {1920, -1},
		{MaxFrameDim + 1, 1080},
		{1080, MaxFrameDim + 1},
		{16384, 16384}, // dims in range but 1 GiB - over MaxFrameBytes
	}
	for _, d := range bad {
		if n, ok := FrameBytes(d[0], d[1]); ok {
			t.Errorf("FrameBytes(%d,%d) = %d, ok - want refused", d[0], d[1], n)
		}
	}
	good := map[[2]int]int{
		{3840, 2160}: fhd4KBytes,
		{1280, 720}:  hdBytes,
		{7680, 4320}: 7680 * 4320 * 4, // 8K = 126 MiB, must still pass
		{1, 1}:       4,
	}
	for d, want := range good {
		n, ok := FrameBytes(d[0], d[1])
		if !ok || n != want {
			t.Errorf("FrameBytes(%d,%d) = %d,%v - want %d,true", d[0], d[1], n, ok, want)
		}
	}
}

// TestPoolSteadyStateNoAllocs is the allocation regression: once the pipeline is primed, N
// 4K frames through the pool must allocate ~nothing. A miss costs 31.6 MiB, i.e. 2 GB/s at
// 4K60 - the churn that made the GC lose under the child's GOMEMLIMIT.
func TestPoolSteadyStateNoAllocs(t *testing.T) {
	const depth = 4 // poller + receiver slot + route slot + encoder in flight
	const frames = 200
	p := &pixRing{idle: map[int][][]byte{}}
	inflight := make([][]byte, 0, depth)
	for i := 0; i < depth; i++ {
		b, ok := p.get(fhd4KBytes, true)
		if !ok {
			t.Fatalf("priming get %d refused", i)
		}
		inflight = append(inflight, b)
	}
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	for i := 0; i < frames; i++ {
		b := inflight[0]
		b[0] = byte(i) // touch it like a readback would
		inflight = inflight[1:]
		p.put(b)
		nb, ok := p.get(fhd4KBytes, true)
		if !ok {
			t.Fatalf("steady-state get %d refused (live ceiling reached with depth %d)", i, depth)
		}
		inflight = append(inflight, nb)
	}
	runtime.ReadMemStats(&m1)
	for _, b := range inflight {
		p.put(b)
	}
	perFrame := float64(m1.TotalAlloc-m0.TotalAlloc) / frames
	if perFrame > 4096 { // a whole frame is 33 M; 4 KB/frame is bookkeeping noise only
		t.Fatalf("steady-state allocation = %.0f B/frame over %d 4K frames, want ~0 (a pool miss is %d B)",
			perFrame, frames, fhd4KBytes)
	}
	t.Logf("steady state: %.0f B/frame over %d 4K frames", perFrame, frames)
}

// TestPoolSizeKeyedNoCrossContamination: two captures of different geometry share the pool.
// The 4K gets must NOT miss because a 720p buffer was on top (the sync.Pool failure mode).
func TestPoolSizeKeyedNoCrossContamination(t *testing.T) {
	p := &pixRing{idle: map[int][][]byte{}}
	big, ok := p.get(fhd4KBytes, true)
	if !ok {
		t.Fatal("4K get refused")
	}
	small, ok := p.get(hdBytes, true)
	if !ok {
		t.Fatal("720p get refused")
	}
	p.put(big)
	p.put(small)
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	for i := 0; i < 50; i++ { // interleave both geometries
		b, _ := p.get(fhd4KBytes, true)
		sm, _ := p.get(hdBytes, true)
		p.put(b)
		p.put(sm)
	}
	runtime.ReadMemStats(&m1)
	if grew := m1.TotalAlloc - m0.TotalAlloc; grew > 64*1024 {
		t.Fatalf("interleaved 4K/720p gets allocated %d B, want ~0 (pool is not size-keyed)", grew)
	}
}

// TestPoolIdleRetentionBounded: PutPix drops over the per-geometry / total idle caps instead
// of retaining every buffer a stalled route hands back.
func TestPoolIdleRetentionBounded(t *testing.T) {
	p := &pixRing{idle: map[int][][]byte{}}
	var got [][]byte
	for i := 0; i < poolMaxPerSize+4; i++ {
		b, ok := p.get(fhd4KBytes, false)
		if !ok {
			t.Fatalf("get %d refused", i)
		}
		got = append(got, b)
	}
	for _, b := range got {
		p.put(b)
	}
	live, idle, bufs := p.stats()
	if live != 0 {
		t.Errorf("liveBytes = %d after every put, want 0", live)
	}
	if bufs > poolMaxPerSize {
		t.Errorf("retained %d buffers, want <= %d", bufs, poolMaxPerSize)
	}
	if idle > poolMaxIdleBytes {
		t.Errorf("retained %d idle bytes, want <= %d", idle, poolMaxIdleBytes)
	}
}

// TestPoolLiveCeilingDrops: at the in-flight ceiling a bounded get REFUSES, so the capture
// poller drops the frame (newest-wins) instead of growing the heap - a producer that
// outruns its consumer must drop, never accumulate.
func TestPoolLiveCeilingDrops(t *testing.T) {
	p := &pixRing{idle: map[int][][]byte{}}
	n := 0
	for {
		if _, ok := p.get(fhd4KBytes, true); !ok {
			break
		}
		n++
		if n > 1000 {
			t.Fatal("bounded get never refused - the live ceiling is not enforced")
		}
	}
	live, _, _ := p.stats()
	if live > poolMaxLiveBytes {
		t.Fatalf("liveBytes = %d over the ceiling %d", live, poolMaxLiveBytes)
	}
	if want := poolMaxLiveBytes / fhd4KBytes; n != want {
		t.Errorf("handed out %d 4K frames before refusing, want %d", n, want)
	}
	// Unbounded get still succeeds (the single resize buffer must never be refused).
	if b := getPixFrom(p, fhd4KBytes); b == nil {
		t.Error("unbounded get refused at the live ceiling")
	}
}

func getPixFrom(p *pixRing, n int) []byte {
	b, _ := p.get(n, false)
	return b
}

// TestPoolRefusesOversized: a buffer bigger than MaxFrameBytes is never allocated or
// retained, whichever way it arrives.
func TestPoolRefusesOversized(t *testing.T) {
	p := &pixRing{idle: map[int][][]byte{}}
	if b, ok := p.get(MaxFrameBytes+1, false); ok || b != nil {
		t.Error("get accepted an oversized request")
	}
	p.put(make([]byte, 1)) // len 1 is fine
	if _, _, bufs := p.stats(); bufs != 1 {
		t.Errorf("retained %d buffers, want 1", bufs)
	}
}
