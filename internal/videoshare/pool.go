package videoshare

import "sync"

// pool.go - pixel-buffer recycling for the capture→encode hot path. A 1080p60 route
// otherwise allocates ~500 MB/s of short-lived 8 MB frame copies (GC churn was a
// contributor to the spout-over-peerlink source-PC melt). Downstream consumers release
// via medialink Frame.Release → PutPix; frames dropped inside videoshare recycle
// directly. Cap policy: the pool holds whatever sync.Pool keeps between GCs - buffers
// are full frames, so a handful at most are live per route.

var pixPool sync.Pool // stores *[]byte (pointer form avoids per-Put allocation)

// getPix returns a pixel buffer of length n (recycled when a cached one fits).
func getPix(n int) []byte {
	if v, _ := pixPool.Get().(*[]byte); v != nil && cap(*v) >= n {
		return (*v)[:n]
	}
	return make([]byte, n)
}

// PutPix recycles a frame's pixel buffer. Callers MUST be completely done with it -
// the next capture may overwrite it immediately.
func PutPix(b []byte) {
	if cap(b) == 0 {
		return
	}
	b = b[:cap(b)]
	pixPool.Put(&b)
}
