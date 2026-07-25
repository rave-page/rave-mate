package waveform

import "rave.page/mate/internal/zignative"

// peaksBuckets dispatches to the Zig bucket-peaks kernel when linked (-tags zigdsp;
// byte-exact, parity-tested), else the Go loop below stays authoritative.
func peaksBuckets(pcm []byte, n int) []byte {
	if !zignative.Available() {
		return bucketPeaks(pcm, n)
	}
	if samples := len(pcm) / 2; samples < n {
		n = samples
	}
	if n <= 0 {
		return nil
	}
	out := make([]byte, n)
	_ = zignative.BucketPeaks(pcm, n, out)
	return out
}
