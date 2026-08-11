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
func RGBAToRGB24(src []byte, stride, w, h int, dst []byte) bool { return false }

func EdHitTest(boxes []float64, px, py float64) int             { return -1 }
func EdHandleAt(box []float64, px, py, tol, rotOff float64) int { return 0 }
func EdAngleAt(box []float64, px, py float64) float64           { return 0 }
func EdRotateFrom(origRot, downAngle, nowAngle float64, snap bool) float64 {
	return 0
}
func EdSnapMove(boxes []float64, moveIdx int, dx, dy, thresh, docW, docH float64) (float64, float64, [4]float64, int) {
	return dx, dy, [4]float64{}, 0
}
func EdResizeBox(box []float64, handle int, px, py float64, uniform bool) (float64, float64, float64, float64) {
	return 0, 0, 0, 0
}
func PxLabel(pix []byte, stride, w, h, bpp int, bgra bool, targets []byte, tol int, labels []byte) bool {
	return false
}
func FillCells(pix []byte, stride, w, h int, cells []int32) bool { return false }

// PCMDec stub — NewWAVDec/NewAIFFDec always return nil.
const (
	DecOK   = 0
	DecNeed = 1
	DecErr  = -1
)

type PCMDec struct{}

type PCMInfo struct {
	SampleRate  int64
	TotalFrames int64
	DataStart   uint64
	Channels    int
	Bits        int
	BlockAlign  int
	IsFloat     bool
	BigEndian   bool
}

func NewWAVDec() *PCMDec                              { return nil }
func NewAIFFDec() *PCMDec                             { return nil }
func (d *PCMDec) Free()                               {}
func (d *PCMDec) Feed(b []byte) (int, uint64, uint64) { return DecErr, 0, 0 }
func (d *PCMDec) Info() PCMInfo                       { return PCMInfo{} }
func (d *PCMDec) SeekOff(frame int64) (int64, uint64) { return 0, 0 }
func (d *PCMDec) SetPos(frame int64)                  {}
func (d *PCMDec) Plan(dstSamples int) (int, int)      { return 0, 0 }
func (d *PCMDec) Decode(b []byte, dst []float32) int  { return 0 }
