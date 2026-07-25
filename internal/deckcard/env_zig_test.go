//go:build zigdsp && cgo

package deckcard

import (
	"math"
	"math/rand"
	"testing"

	"rave.page/mate/internal/zignative"
)

// Bit-exact parity: Zig envelope kernel vs the Go buildEnv (both zoom branches).
func TestZigWaveEnvParity(t *testing.T) {
	if !zignative.Available() {
		t.Fatal("zignative not available in zigdsp build")
	}
	rng := rand.New(rand.NewSource(1))
	cases := []struct {
		n      int
		dur    float64
		imgPps float64
	}{
		{1, 1, 10},
		{8, 2, 10},        // upsampled (zoomed in)
		{8192, 240, 40},   // typical track, zoomed out
		{8192, 30, 400},   // zoomed in on a short track
		{100000, 7200, 5}, // multi-hour set, coarse
	}
	for _, c := range cases {
		peaks := make([]byte, c.n)
		rng.Read(peaks)
		goEnv := buildEnv(peaks, c.dur, c.imgPps) // dispatches to Zig
		iw := int(c.dur*c.imgPps) + 1
		ref := make([]float64, iw)
		buildEnvGo(peaks, ref, c.dur, c.imgPps)
		if len(goEnv) != len(ref) {
			t.Fatalf("env len mismatch n=%d: %d vs %d", c.n, len(goEnv), len(ref))
		}
		for i := range ref {
			if math.Float64bits(ref[i]) != math.Float64bits(goEnv[i]) {
				t.Fatalf("env mismatch n=%d pps=%v i=%d: go=%v zig=%v", c.n, c.imgPps, i, ref[i], goEnv[i])
			}
		}
	}
}

func BenchmarkWaveEnvGo(b *testing.B) {
	peaks := make([]byte, 8192)
	rand.New(rand.NewSource(2)).Read(peaks)
	amp := make([]float64, int(240.0*40)+1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buildEnvGo(peaks, amp, 240, 40)
	}
}

func BenchmarkWaveEnvZig(b *testing.B) {
	peaks := make([]byte, 8192)
	rand.New(rand.NewSource(2)).Read(peaks)
	amp := make([]float64, int(240.0*40)+1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		zignative.WaveEnv(peaks, 240, 40, amp)
	}
}
