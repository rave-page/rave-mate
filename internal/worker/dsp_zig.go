package worker

import "rave.page/mate/internal/zignative"

// peaksBuckets/bandsBuckets dispatch to the Zig kernels when linked (-tags zigdsp;
// byte-exact ports, parity-tested), else the Go loops below stay authoritative.

func peaksBuckets(pcm []byte, n int) []byte {
	if !zignative.Available() {
		return bucketPeaks(pcm, n)
	}
	if samples := len(pcm) / 2; samples < n {
		n = samples
	}
	out := make([]byte, n)
	if n > 0 {
		_ = zignative.BucketPeaks(pcm, n, out)
	}
	return out
}

func bandsBuckets(pcm []byte, n, fs int) []byte {
	if !zignative.Available() {
		return bucketBands(pcm, n, fs)
	}
	if samples := len(pcm) / 2; samples < n {
		n = samples
	}
	if n <= 0 {
		return nil
	}
	out := make([]byte, 3*n)
	_ = zignative.BucketBands(pcm, n, fs, out)
	return out
}
