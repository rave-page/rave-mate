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

func F32ToLEBytes(samples []float32, gain float32, out []byte) {}
func FoldStereo(in []float32, frames, ch int, out []float32)   {}
func WaveColumns(peaks []byte, cols int, out []byte)           {}
func WaveEnv(peaks []byte, dur, imgPps float64, out []float64) {}
func PCMToF32(src []byte, frames, ch, blockAlign, bits int, isFloat, bigEndian bool, out []float32) {
}
