//go:build !zigdsp || !cgo

// Stub when built without -tags zigdsp: callers keep pure-Go paths.
package zignative

// Available reports the Zig core is linked (never, in stub builds).
func Available() bool { return false }

// Resampler stub — NewResampler always returns nil; methods exist for type-compat.
type Resampler struct{}

func NewResampler(inRate, outRate, ch int) *Resampler { return nil }
func (r *Resampler) Free()                            {}
func (r *Resampler) Reset()                           {}
func (r *Resampler) OutCap(inFrames int) int          { return 0 }
func (r *Resampler) Process(in, out []float32) int    { return -1 }

func BucketPeaks(pcm []byte, n int, out []byte) int     { return 0 }
func BucketBands(pcm []byte, n, fs int, out []byte) int { return 0 }
func ApplyGain(buf []float32, gain float32)             {}
func PeakAbs(in []float32) float32                      { return 0 }
