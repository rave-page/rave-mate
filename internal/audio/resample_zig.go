package audio

import "rave.page/mate/internal/zignative"

// rateConverter seam: Zig polyphase sinc (zigdsp builds) or Go linear fallback.
type rateConverter interface {
	process(in []float32) []float32
	reset()
	flush() []float32
}

// newRateConverter prefers the Zig resampler when linked (-tags zigdsp + make zig).
func newRateConverter(in, out, ch int) rateConverter {
	if z := zignative.NewResampler(in, out, ch); z != nil {
		return &zigSRC{r: z, ch: ch}
	}
	return newResampler(in, out, ch)
}

// zigSRC adapts zignative.Resampler to the rateConverter seam.
type zigSRC struct {
	r  *zignative.Resampler
	ch int
}

func (z *zigSRC) process(in []float32) []float32 {
	frames := len(in) / z.ch
	if frames == 0 {
		return nil
	}
	out := make([]float32, z.r.OutCap(frames)*z.ch)
	n := z.r.Process(in, out)
	if n <= 0 {
		return nil
	}
	return out[:n]
}

func (z *zigSRC) reset() { z.r.Reset() }

// flush: sinc tail is sub-ms like the linear path; dropped for identical semantics.
func (z *zigSRC) flush() []float32 { return nil }
