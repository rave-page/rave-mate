//go:build zigdsp && cgo

package audio

import (
	"math"
	"testing"

	"rave.page/mate/internal/zignative"
)

// Zig path selected + quality: 1kHz sine through 44.1->48k must clear 70dB SNR
// (linear lands ~35dB). Also asserts output-count ratio + reset behavior.
func TestZigResamplerQuality(t *testing.T) {
	if !zignative.Available() {
		t.Fatal("zignative not available in zigdsp build")
	}
	rc := newRateConverter(44100, 48000, 2)
	if _, ok := rc.(*zigSRC); !ok {
		t.Fatalf("expected zigSRC, got %T", rc)
	}
	const freq = 1000.0
	nIn, nOut := 0, 0
	var sig, errE float64
	for b := 0; b < 40; b++ {
		in := make([]float32, 512*2)
		for f := 0; f < 512; f++ {
			v := float32(math.Sin(2 * math.Pi * freq * float64(nIn) / 44100))
			in[f*2], in[f*2+1] = v, v
			nIn++
		}
		out := rc.process(in)
		for f := 0; f < len(out)/2; f++ {
			want := math.Sin(2 * math.Pi * freq * float64(nOut) / 48000)
			if nOut > 64 { // skip zero-padded ramp-in
				d := float64(out[f*2]) - want
				sig += want * want
				errE += d * d
			}
			nOut++
		}
	}
	snr := 10 * math.Log10(sig/errE)
	if snr < 70 {
		t.Fatalf("SNR %.1fdB < 70dB", snr)
	}
	ratio := float64(nOut) / float64(nIn)
	if math.Abs(ratio-48000.0/44100.0) > 0.01 {
		t.Fatalf("output ratio %.4f", ratio)
	}
	rc.reset()
	if got := rc.process(make([]float32, 128*2)); len(got)%2 != 0 {
		t.Fatalf("odd sample count after reset: %d", len(got))
	}
}
