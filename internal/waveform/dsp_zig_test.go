//go:build zigdsp && cgo

package waveform

import (
	"bytes"
	"math/rand"
	"testing"

	"rave.page/mate/internal/zignative"
)

// Byte-exact parity: Zig bucket-peaks kernel vs the resolver's Go loop.
func TestZigPeaksParity(t *testing.T) {
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
		}
	}
}

func BenchmarkPeaksGo(b *testing.B) {
	pcm := make([]byte, 8000*2*300) // 5min mono s16 @8k (resolver decode rate)
	rand.New(rand.NewSource(2)).Read(pcm)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bucketPeaks(pcm, 3000)
	}
}

func BenchmarkPeaksZig(b *testing.B) {
	pcm := make([]byte, 8000*2*300)
	rand.New(rand.NewSource(2)).Read(pcm)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		peaksBuckets(pcm, 3000)
	}
}
