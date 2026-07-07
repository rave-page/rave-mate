// Package netstats turns cumulative byte counters + per-peer RTT probes into 1 Hz rate
// series (fixed-size rings) for the dashboard NETWORK/TIMING graphs.
package netstats

import (
	"io"
	"sync/atomic"
)

// Ring is a fixed-capacity float64 series, oldest→newest.
type Ring struct {
	buf  []float64
	head int // next write slot
	n    int
}

// NewRing returns a ring holding up to size samples.
func NewRing(size int) *Ring {
	if size < 1 {
		size = 1
	}
	return &Ring{buf: make([]float64, size)}
}

// Push appends v, evicting the oldest sample when full.
func (r *Ring) Push(v float64) {
	r.buf[r.head] = v
	r.head = (r.head + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
}

// Values returns the samples oldest→newest (len ≤ capacity).
func (r *Ring) Values() []float64 {
	out := make([]float64, r.n)
	start := (r.head - r.n + len(r.buf)) % len(r.buf)
	for i := range out {
		out[i] = r.buf[(start+i)%len(r.buf)]
	}
	return out
}

// Latest returns the newest sample; ok=false when empty.
func (r *Ring) Latest() (float64, bool) {
	if r.n == 0 {
		return 0, false
	}
	return r.buf[(r.head-1+len(r.buf))%len(r.buf)], true
}

// countingRC counts bytes read through an io.ReadCloser into ctr.
type countingRC struct {
	rc  io.ReadCloser
	ctr *atomic.Uint64
}

func (c *countingRC) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 {
		c.ctr.Add(uint64(n))
	}
	return n, err
}

func (c *countingRC) Close() error { return c.rc.Close() }

// CountBody wraps rc so every byte read adds to ctr (HTTP body byte accounting).
func CountBody(rc io.ReadCloser, ctr *atomic.Uint64) io.ReadCloser {
	return &countingRC{rc: rc, ctr: ctr}
}
