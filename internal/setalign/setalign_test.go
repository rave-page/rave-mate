package setalign

import (
	"math"
	"testing"
)

const rate = 50.0

// synth builds a busy pseudo-random envelope of n samples (deterministic LCG noise + sines +
// per-10s random level segments - the aperiodic long-range structure real sets have).
func synth(n int, seed uint64) []float64 {
	out := make([]float64, n)
	s := seed
	lvl := func(seg int) float64 {
		h := (uint64(seg)*0x9E3779B97F4A7C15 ^ seed*0xBF58476D1CE4E5B9) * 6364136223846793005
		return 0.3 + 0.7*float64(h>>33)/float64(1<<31)
	}
	for i := range out {
		s = s*6364136223846793005 + 1442695040888963407
		noise := float64(s>>33) / float64(1<<31)
		t := float64(i) / rate
		out[i] = lvl(i/int(10*rate))*(0.6+0.2*math.Sin(t*0.7)+0.15*math.Sin(t*3.1)) + 0.4*noise
	}
	return out
}

// slice extracts B as A shifted by offSec, lasting durSec, with mild noise.
func slice(a []float64, offSec, durSec float64, seed uint64) []float64 {
	start := int(offSec * rate)
	n := int(durSec * rate)
	out := make([]float64, n)
	s := seed
	for i := range out {
		s = s*6364136223846793005 + 1442695040888963407
		noise := float64(s>>33)/float64(1<<31) - 0.5
		if j := start + i; j >= 0 && j < len(a) {
			out[i] = a[j] + 0.05*noise
		} else {
			out[i] = 0.5 + 0.05*noise // B extends past A: uncorrelated filler
		}
	}
	return out
}

func TestAlignRecovers(t *testing.T) {
	a := synth(int(20*60*rate), 7) // 20 min "audio recording"
	for _, want := range []float64{37.2, 0, 312.04} {
		b := slice(a, want, 10*60, 99) // 10 min "video audio"
		r, err := Align(a, b, rate, 0, false, 0)
		if err != nil {
			t.Fatalf("off=%v: %v", want, err)
		}
		if math.Abs(r.OffsetSec-want) > 0.06 {
			t.Fatalf("off=%v: got %v (conf %v)", want, r.OffsetSec, r.Confidence)
		}
		if !r.Decisive || r.Confidence < 0.6 {
			t.Fatalf("off=%v: weak result %+v", want, r)
		}
	}
}

func TestAlignNegativeOffsetWithPrior(t *testing.T) {
	b := synth(int(15*60*rate), 3) // video audio is the LONGER, earlier recording
	a := slice(b, 42.5, 8*60, 5)   // audio capture starts 42.5 s into it
	want := -42.5                  // B starts 42.5 s before A
	r, err := Align(a, b, rate, -40, true, 120)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(r.OffsetSec-want) > 0.06 {
		t.Fatalf("got %v want %v (conf %v)", r.OffsetSec, want, r.Confidence)
	}
}

func TestAlignRejectsGarbage(t *testing.T) {
	a := synth(int(5*60*rate), 1)
	b := synth(int(5*60*rate), 2) // unrelated signal
	r, err := Align(a, b, rate, 0, false, 0)
	if err == nil && (r.Decisive && r.Confidence > 0.6) {
		t.Fatalf("unrelated signals reported confident alignment: %+v", r)
	}
	if _, err := Align(a[:10], b, rate, 0, false, 0); err == nil {
		t.Fatal("too-short envelope accepted")
	}
	flat := make([]float64, len(a))
	if _, err := Align(flat, b, rate, 0, false, 0); err == nil {
		t.Fatal("flat envelope accepted")
	}
}
