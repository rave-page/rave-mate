package audio

import "math"

// resampler is a streaming linear-interpolation sample-rate converter (interleaved, `ch`
// channels). Linear is cheap + low-latency; adequate for 44.1<->48k playback. A polyphase/sinc
// upgrade is a later quality pass (noted in the design). Carries phase + the last input frame
// across process() calls so block boundaries don't click.
type resampler struct {
	inRate, outRate, ch int
	hasPrev             bool
	prev                []float32 // last input frame (ch)
	p                   float64   // fractional input position in the current buffer (0 = prev)
}

func newResampler(in, out, ch int) *resampler {
	return &resampler{inRate: in, outRate: out, ch: ch, prev: make([]float32, ch)}
}

func (r *resampler) reset() {
	r.hasPrev = false
	r.p = 0
}

// process resamples one interleaved input block to interleaved output at outRate.
func (r *resampler) process(in []float32) []float32 {
	ch := r.ch
	frames := len(in) / ch
	if frames == 0 {
		return nil
	}
	step := float64(r.inRate) / float64(r.outRate)
	if !r.hasPrev {
		copy(r.prev, in[0:ch]) // seed: frame(0) = in[0], start emitting at p=0
		r.hasPrev = true
		r.p = 0
	}
	// frame(j): j==0 -> prev; j>=1 -> in[j-1]. Interpolate frame(floor p)..frame(floor p +1).
	frameAt := func(j int) []float32 {
		if j == 0 {
			return r.prev
		}
		return in[(j-1)*ch : (j-1)*ch+ch]
	}
	est := int(float64(frames)/step) + ch
	out := make([]float32, 0, est*ch)
	for {
		j := int(math.Floor(r.p))
		if j+1 > frames { // need frame(j+1)=in[j]; unavailable => wait for next block
			break
		}
		frac := float32(r.p - float64(j))
		a, b := frameAt(j), frameAt(j+1)
		for c := 0; c < ch; c++ {
			out = append(out, a[c]+(b[c]-a[c])*frac)
		}
		r.p += step
	}
	// Re-base to the next block: its in[0] follows this block's last frame.
	copy(r.prev, in[(frames-1)*ch:frames*ch])
	r.p -= float64(frames)
	if r.p < 0 {
		r.p = 0
	}
	return out
}

// flush emits nothing (the <1-frame tail at EOF is sub-millisecond; dropped for simplicity).
func (r *resampler) flush() []float32 { return nil }
