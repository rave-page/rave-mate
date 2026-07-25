//go:build zigdsp && cgo

package worker

import (
	"bytes"
	"math/rand"
	"testing"

	"rave.page/mate/internal/zignative"
)

// Byte-exact parity: Zig kernels vs the authoritative Go loops.
func TestZigBucketParity(t *testing.T) {
	if !zignative.Available() {
		t.Fatal("zignative not available in zigdsp build")
	}
	rng := rand.New(rand.NewSource(1))
	for _, samples := range []int{0, 1, 7, 4096, 160000} {
		pcm := make([]byte, samples*2)
		rng.Read(pcm)
		for _, n := range []int{1, 3, 100, 60000} {
			goPeaks := bucketPeaks(pcm, n)
			zgPeaks := peaksBuckets(pcm, n)
			if !bytes.Equal(goPeaks, zgPeaks) {
				t.Fatalf("peaks mismatch samples=%d n=%d", samples, n)
			}
			goBands := bucketBands(pcm, n, 16000)
			zgBands := bandsBuckets(pcm, n, 16000)
			if !bytes.Equal(goBands, zgBands) {
				t.Fatalf("bands mismatch samples=%d n=%d (go %d bytes, zig %d bytes)",
					samples, n, len(goBands), len(zgBands))
			}
		}
	}
}

func BenchmarkBandsGo(b *testing.B) {
	pcm := make([]byte, 16000*2*300) // 5min mono s16 @16k
	rand.New(rand.NewSource(2)).Read(pcm)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bucketBands(pcm, 30000, 16000)
	}
}

func BenchmarkBandsZig(b *testing.B) {
	pcm := make([]byte, 16000*2*300)
	rand.New(rand.NewSource(2)).Read(pcm)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bandsBuckets(pcm, 30000, 16000)
	}
}
