package worker

import (
	"math"
	"testing"
)

// sineS16 renders n samples of a freq-Hz sine at fs into little-endian s16 PCM.
func sineS16(freq, fs float64, n int) []byte {
	pcm := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		v := int16(20000 * math.Sin(2*math.Pi*freq*float64(i)/fs))
		pcm[2*i] = byte(uint16(v))
		pcm[2*i+1] = byte(uint16(v) >> 8)
	}
	return pcm
}

// A pure tone should light up the band that owns its frequency, not the others.
func TestBucketBandsSeparation(t *testing.T) {
	const fs = 16000
	cases := []struct {
		freq float64
		band int // expected dominant band index (0=low,1=mid,2=high)
	}{
		{120, 0}, {1200, 1}, {6000, 2},
	}
	for _, c := range cases {
		out := bucketBands(sineS16(c.freq, fs, fs), 1, fs) // 1 bucket over 1 s
		if len(out) != 3 {
			t.Fatalf("%.0fHz: got %d bytes, want 3", c.freq, len(out))
		}
		dom := 0
		for i := 1; i < 3; i++ {
			if out[i] > out[dom] {
				dom = i
			}
		}
		if dom != c.band {
			t.Errorf("%.0fHz: dominant band %d, want %d (lo=%d mid=%d hi=%d)", c.freq, dom, c.band, out[0], out[1], out[2])
		}
	}
}
