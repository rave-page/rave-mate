package videoshare

import "sync"

// pool.go - BOUNDED, size-keyed pixel-buffer recycling for the capture→encode hot path. A
// 1080p60 route otherwise allocates ~500 MB/s of short-lived 8 MB frame copies (2 GB/s at
// 4K60); GC churn was a contributor to the spout-over-peerlink source-PC melt. Downstream
// consumers release via medialink Frame.Release → PutPix; frames dropped inside videoshare
// recycle directly.
//
// Not a sync.Pool: it is cleared on every GC and is not size-keyed, so (a) a get right
// after a GC and (b) a 4K get that pops a 1080p buffer - two captures of different
// geometry share this pool - both re-allocate a whole frame, which is exactly the churn the
// pool exists to remove. This is an explicit ring keyed by byte size.
//
// Caps + policy (repo rule: every data-path buffer is bounded, frames AND bytes, policy
// stated):
//   - per buffer: MaxFrameBytes. Bigger is a bogus geometry from the platform shim, never
//     allocated - see FrameBytes.
//   - idle (retained): poolMaxPerSize buffers per geometry, poolMaxIdleBytes overall.
//     PutPix DROPS over either cap (GC reclaims it) - the buffer is garbage either way.
//   - live (checked out): poolMaxLiveBytes. TryGetPix returns ok=false at the ceiling so
//     the CAPTURE poller DROPS the frame (newest-wins) instead of growing the heap: a
//     producer that outruns its consumer must drop, never accumulate.
const (
	// MaxFrameDim bounds one frame edge. Matches the encode-side guard in
	// internal/mediapipe (spec.Width/Height > 16384 is refused there).
	MaxFrameDim = 16384
	// MaxFrameBytes bounds one frame buffer (8K RGBA = 132 MB, so this clears real
	// geometries with room to spare while refusing shim garbage).
	MaxFrameBytes = 256 << 20

	poolMaxPerSize   = 4         // idle buffers retained per geometry
	poolMaxIdleBytes = 128 << 20 // ≈4 × 4K frames retained overall
	poolMaxLiveBytes = 384 << 20 // ≈11 × 4K frames checked out at once (all captures)
)

// FrameBytes returns the RGBA byte size of w×h, ok=false when the geometry is not
// plausible. The platform shim can hand back TORN sender info while a large shared texture
// is still being created: a 3840×2160 Spout sender was observed reporting
// w=139846784 h=3840 on the receiver's FIRST poll, and sizing a buffer from that
// (139846784*3840*4 = 2 TB) killed the media child with
// "fatal error: runtime: cannot allocate memory" - the job object refuses the commit long
// before the allocation completes. NEVER size a buffer from unvalidated shim output.
func FrameBytes(w, h int) (int, bool) {
	if w <= 0 || h <= 0 || w > MaxFrameDim || h > MaxFrameDim {
		return 0, false
	}
	n := w * h * 4
	if n > MaxFrameBytes {
		return 0, false
	}
	return n, true
}

// pixPool is the process-wide bounded pixel-buffer ring.
var pixPool = pixRing{idle: map[int][][]byte{}}

type pixRing struct {
	mu        sync.Mutex
	idle      map[int][][]byte // byte size → retained buffers
	idleBytes int
	liveBytes int
}

// get takes a buffer of exactly n bytes, honouring the live ceiling when bounded is set.
// ok=false means the caller must DROP (bounded) or n was out of range.
func (p *pixRing) get(n int, bounded bool) ([]byte, bool) {
	if n <= 0 || n > MaxFrameBytes {
		return nil, false
	}
	p.mu.Lock()
	if bounded && p.liveBytes+n > poolMaxLiveBytes {
		p.mu.Unlock()
		return nil, false
	}
	p.liveBytes += n
	if q := p.idle[n]; len(q) > 0 {
		b := q[len(q)-1]
		p.idle[n] = q[:len(q)-1]
		p.idleBytes -= n
		p.mu.Unlock()
		return b, true
	}
	p.mu.Unlock()
	return make([]byte, n), true
}

// put returns a buffer, dropping it when either idle cap is reached.
func (p *pixRing) put(b []byte) {
	n := len(b)
	if n <= 0 || n > MaxFrameBytes {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.liveBytes >= n {
		p.liveBytes -= n
	} else {
		p.liveBytes = 0 // a put without a matching get (test seam / double release) must not underflow
	}
	if len(p.idle[n]) >= poolMaxPerSize {
		return // this geometry is at its cap: drop, GC reclaims it
	}
	// Total-byte cap: evict from the LARGEST other geometry first. Without this a 4K
	// capture's idle buffers fill the whole cap and a concurrent 720p capture misses on
	// every frame (220 MB/s of churn - exactly what the pool exists to remove).
	for p.idleBytes+n > poolMaxIdleBytes && p.evictLargestLocked(n) {
	}
	if p.idleBytes+n > poolMaxIdleBytes {
		return // still no room (n alone is over the cap): drop
	}
	p.idle[n] = append(p.idle[n], b)
	p.idleBytes += n
}

// evictLargestLocked drops one idle buffer from the largest geometry other than keep (or
// from keep itself when nothing else is retained). false = nothing left to evict.
func (p *pixRing) evictLargestLocked(keep int) bool {
	best, bestOther := 0, 0
	for size, q := range p.idle {
		if len(q) == 0 {
			continue
		}
		if size > best {
			best = size
		}
		if size != keep && size > bestOther {
			bestOther = size
		}
	}
	victim := bestOther
	if victim == 0 {
		victim = best
	}
	if victim == 0 {
		return false
	}
	q := p.idle[victim]
	p.idle[victim] = q[:len(q)-1]
	p.idleBytes -= victim
	if len(p.idle[victim]) == 0 {
		delete(p.idle, victim) // stale geometry (sender resized): don't keep the bucket
	}
	return true
}

// stats reports (liveBytes, idleBytes, idleBuffers) - diagnostics + tests.
func (p *pixRing) stats() (live, idle, bufs int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, q := range p.idle {
		bufs += len(q)
	}
	return p.liveBytes, p.idleBytes, bufs
}

// getPix returns a pixel buffer of exactly n bytes, or nil when n is not a plausible frame
// size. Unbounded on the live ceiling: for the ONE buffer a (re)size pass needs.
func getPix(n int) []byte {
	b, _ := pixPool.get(n, false)
	return b
}

// tryGetPix is getPix under the live-bytes ceiling: ok=false means the caller must drop
// this frame rather than grow the heap (capture newest-wins).
func tryGetPix(n int) ([]byte, bool) { return pixPool.get(n, true) }

// PutPix recycles a frame's pixel buffer. Callers MUST be completely done with it -
// the next capture may overwrite it immediately.
func PutPix(b []byte) { pixPool.put(b) }

// PoolStats reports the pool's live/idle byte occupancy + retained buffer count (route
// telemetry + tests).
func PoolStats() (liveBytes, idleBytes, idleBuffers int) { return pixPool.stats() }

// GetPixForTest hands out a pooled buffer to other packages' allocation-regression tests
// (the capture fan-out in internal/mediaroute models the receiver's get/put pattern).
func GetPixForTest(n int) []byte { return getPix(n) }
