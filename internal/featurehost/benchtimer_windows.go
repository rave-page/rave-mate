//go:build windows && cgo

package featurehost

import (
	"context"
	"fmt"
	"time"

	"rave.page/mate/internal/encoderscan"
	"rave.page/mate/internal/gpubench"
	"rave.page/mate/internal/mfenc"
)

// Benchmark tuning: warm-up frames (encoder spin-up) then timed frames. ~1-1.5 s of encode work at
// 60 fps; the encoder runs faster than realtime so wall time is well under the Runner's timeout.
const (
	benchWarmupFrames = 10
	benchTimedFrames  = 90
	benchKbps         = 20000 // high, so the rate-control never throttles the throughput measurement
)

// encodeBenchTimer opens a native encode session, submits synthetic RGBA frames as fast as the ring
// accepts, drains the AUs, and returns measured pixels/sec. Honours ctx (OpenProcSession is admitted
// through gpubudget; Encode/drain stop on ctx cancel and Close kills the child session). Runs only in
// the supervised media child. Content-independent: throughput is what we measure, so a zeroed frame
// is fine.
func encodeBenchTimer(ctx context.Context, t gpubench.Target) (float64, error) {
	var luid int64
	if v, ok := encoderscan.LUIDInt64(t.AdapterLUID); ok {
		luid = v
	}
	fps := t.FPS
	if fps <= 0 {
		fps = 60
	}
	s, err := mfenc.OpenProcSession(luid, t.W, t.H, t.W, t.H, float64(fps), benchKbps, fps)
	if err != nil {
		return 0, err
	}
	defer s.Close()

	var drained int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-s.Output():
				if !ok {
					return
				}
				drained++
			}
		}
	}()

	frame := make([]byte, t.W*t.H*4) // synthetic (zeroed) BGRA/RGBA; one reused buffer
	frameNs := int64(time.Second) / int64(fps)
	for i := 0; i < benchWarmupFrames; i++ {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if err := s.Encode(frame, int64(i)*frameNs); err != nil {
			return 0, err
		}
	}
	start := time.Now()
	for i := 0; i < benchTimedFrames; i++ {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if err := s.Encode(frame, int64(benchWarmupFrames+i)*frameNs); err != nil {
			return 0, err
		}
	}
	// Wait until the timed frames are encoded (drained) or the deadline hits. Encode submits to a
	// ring; the AU drain is the true encode-completion signal.
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for drained < int64(benchTimedFrames) {
		select {
		case <-ctx.Done():
			goto measure
		case <-deadline.C:
			goto measure
		case <-time.After(5 * time.Millisecond):
		}
	}
measure:
	elapsed := time.Since(start)
	n := drained
	if n > int64(benchTimedFrames) {
		n = int64(benchTimedFrames)
	}
	if n <= 0 || elapsed <= 0 {
		return 0, fmt.Errorf("gpubench: no frames encoded (%d in %v)", n, elapsed)
	}
	return float64(n) * float64(t.W) * float64(t.H) / elapsed.Seconds(), nil
}
