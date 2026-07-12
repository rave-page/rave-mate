package worker

import "math"

// 3-band spectral analysis for the coloured waveform. Each bucket carries a low/mid/high
// max-abs peak (uint8) alongside the amplitude peak, so the UI colours the waveform by
// frequency content (Traktor-style: bass = warm, mids = green, air = cool). Bands are split
// with RBJ-cookbook biquads run over the decoded mono PCM in one streaming pass.

// biquad is a direct-form-1 second-order IIR section (stateful across the whole stream).
type biquad struct {
	b0, b1, b2, a1, a2 float64
	x1, x2, y1, y2     float64
}

func (f *biquad) process(x float64) float64 {
	y := f.b0*x + f.b1*f.x1 + f.b2*f.x2 - f.a1*f.y1 - f.a2*f.y2
	f.x2, f.x1 = f.x1, x
	f.y2, f.y1 = f.y1, y
	return y
}

// newLowpass/newHighpass/newBandpass build normalized RBJ biquads at fc (Hz) over fs (Hz), Q.
func newLowpass(fc, fs, q float64) *biquad {
	w0 := 2 * math.Pi * fc / fs
	c, s := math.Cos(w0), math.Sin(w0)
	al := s / (2 * q)
	a0 := 1 + al
	return &biquad{b0: (1 - c) / 2 / a0, b1: (1 - c) / a0, b2: (1 - c) / 2 / a0, a1: -2 * c / a0, a2: (1 - al) / a0}
}

func newHighpass(fc, fs, q float64) *biquad {
	w0 := 2 * math.Pi * fc / fs
	c, s := math.Cos(w0), math.Sin(w0)
	al := s / (2 * q)
	a0 := 1 + al
	return &biquad{b0: (1 + c) / 2 / a0, b1: -(1 + c) / a0, b2: (1 + c) / 2 / a0, a1: -2 * c / a0, a2: (1 - al) / a0}
}

func newBandpass(fc, fs, q float64) *biquad { // constant 0 dB peak gain
	w0 := 2 * math.Pi * fc / fs
	c, s := math.Cos(w0), math.Sin(w0)
	al := s / (2 * q)
	a0 := 1 + al
	return &biquad{b0: al / a0, b1: 0, b2: -al / a0, a1: -2 * c / a0, a2: (1 - al) / a0}
}

// bucketBands folds mono s16 PCM into n buckets of 3 uint8 max-abs band peaks, interleaved
// [low,mid,high] per bucket (3*n bytes). Bucket boundaries mirror bucketPeaks so the colour
// bands line up with the amplitude envelope. fs = decode sample rate (Hz). Streaming: every
// sample is filtered exactly once, in order, so the IIR state stays valid across buckets.
func bucketBands(pcm []byte, n, fs int) []byte {
	samples := len(pcm) / 2
	if samples < n {
		n = samples
	}
	if n <= 0 {
		return nil
	}
	fsf := float64(fs)
	lp := newLowpass(250, fsf, 0.707)   // sub/bass/kick
	bp := newBandpass(1200, fsf, 0.60)  // vocals/snares/synths (broad)
	hp := newHighpass(4000, fsf, 0.707) // hats/cymbals/air
	out := make([]byte, 3*n)
	for b := 0; b < n; b++ {
		lo, hi := b*samples/n, (b+1)*samples/n
		var lPk, mPk, hPk float64
		for i := lo; i < hi; i++ {
			s := float64(int16(uint16(pcm[2*i]) | uint16(pcm[2*i+1])<<8))
			if v := math.Abs(lp.process(s)); v > lPk {
				lPk = v
			}
			if v := math.Abs(bp.process(s)); v > mPk {
				mPk = v
			}
			if v := math.Abs(hp.process(s)); v > hPk {
				hPk = v
			}
		}
		out[3*b], out[3*b+1], out[3*b+2] = scale8(lPk), scale8(mPk), scale8(hPk)
	}
	return out
}

// scale8 maps a |sample| magnitude to 0-255 (filters can overshoot the s16 range - clamp).
func scale8(v float64) byte {
	if v > 32767 {
		v = 32767
	}
	return byte(int(v) >> 7)
}
