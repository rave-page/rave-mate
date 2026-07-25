//go:build zigdsp && cgo

package giokit

import (
	"bytes"
	"math/rand"
	"testing"

	"rave.page/mate/internal/zignative"
)

// Byte-exact parity: Zig wave-columns kernel vs the Go fold.
func TestZigWaveColumnsParity(t *testing.T) {
	if !zignative.Available() {
		t.Fatal("zignative not available in zigdsp build")
	}
	rng := rand.New(rand.NewSource(1))
	for _, n := range []int{1, 2, 7, 8192, 100000} {
		peaks := make([]byte, n)
		rng.Read(peaks)
		for _, cols := range []int{1, 3, 375, 1900, 200000} {
			goOut := make([]byte, cols)
			waveColumnsGo(peaks, goOut)
			zgOut := make([]byte, cols)
			zignative.WaveColumns(peaks, cols, zgOut)
			if !bytes.Equal(goOut, zgOut) {
				t.Fatalf("waveColumns mismatch n=%d cols=%d", n, cols)
			}
		}
	}
}

func BenchmarkWaveColumnsGo(b *testing.B) {
	peaks := make([]byte, 8192)
	rand.New(rand.NewSource(2)).Read(peaks)
	out := make([]byte, 1900)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		waveColumnsGo(peaks, out)
	}
}

func BenchmarkWaveColumnsZig(b *testing.B) {
	peaks := make([]byte, 8192)
	rand.New(rand.NewSource(2)).Read(peaks)
	out := make([]byte, 1900)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		zignative.WaveColumns(peaks, 1900, out)
	}
}
