//go:build zigdsp && cgo

// Package zignative binds the Zig native core (native/zigcore, C ABI static lib).
// Tag-gated like abletonlink: build with -tags zigdsp after `make zig` (or
// scripts/build-zig.sh) produced native/zigcore/zig-out/lib/ravezig.lib.
// Untagged builds use the pure-Go stub (Available()=false) and existing Go paths.
package zignative

/*
#cgo CFLAGS: -I${SRCDIR}/../../native/zigcore/include
#cgo LDFLAGS: -L${SRCDIR}/../../native/zigcore/zig-out/lib -lravezig
#include "ravezig.h"
*/
import "C"
import (
	"runtime"
	"unsafe"
)

// abiVersion the lib must report; mismatch = stale artifact, refuse to run.
const abiVersion = 1

// Available reports the Zig core is linked and ABI-compatible.
func Available() bool { return uint32(C.rz_abi_version()) == abiVersion }

// Resampler is a streaming polyphase windowed-sinc SRC (interleaved f32, zero added latency).
type Resampler struct {
	p  *C.RzResampler
	ch int
}

// NewResampler returns nil on bad args or alloc failure (caller falls back to Go linear).
func NewResampler(inRate, outRate, ch int) *Resampler {
	if inRate <= 0 || outRate <= 0 || ch <= 0 {
		return nil
	}
	p := C.rz_resampler_new(C.uint32_t(inRate), C.uint32_t(outRate), C.uint32_t(ch))
	if p == nil {
		return nil
	}
	r := &Resampler{p: p, ch: ch}
	runtime.SetFinalizer(r, func(r *Resampler) { r.Free() })
	return r
}

// Free releases the native state (idempotent).
func (r *Resampler) Free() {
	if r.p != nil {
		C.rz_resampler_free(r.p)
		r.p = nil
	}
}

// Reset clears streaming state for a new file/seek.
func (r *Resampler) Reset() { C.rz_resampler_reset(r.p) }

// OutCap returns the max frames Process can emit for inFrames.
func (r *Resampler) OutCap(inFrames int) int {
	return int(C.rz_resampler_out_cap(r.p, C.size_t(inFrames)))
}

// Process resamples interleaved in into out (len >= OutCap(frames)*ch). Returns
// samples (frames*ch) written, or -1 on error.
func (r *Resampler) Process(in, out []float32) int {
	if len(in) == 0 {
		return 0
	}
	inFrames := len(in) / r.ch
	outCap := len(out) / r.ch
	n := C.rz_resampler_process(r.p,
		(*C.float)(unsafe.Pointer(&in[0])), C.size_t(inFrames),
		(*C.float)(unsafe.Pointer(&out[0])), C.size_t(outCap))
	if n == ^C.size_t(0) {
		return -1
	}
	return int(n) * r.ch
}

// BucketPeaks fills out (n bytes) with per-bucket max-abs u8 peaks of mono s16le pcm.
// Returns buckets written. Byte-exact with worker bucketPeaks.
func BucketPeaks(pcm []byte, n int, out []byte) int {
	if len(pcm) < 2 || n <= 0 {
		return 0
	}
	return int(C.rz_bucket_peaks((*C.uint8_t)(unsafe.Pointer(&pcm[0])), C.size_t(len(pcm)),
		C.size_t(n), (*C.uint8_t)(unsafe.Pointer(&out[0]))))
}

// BucketBands fills out (3*n bytes) with [low,mid,high] u8 band peaks per bucket.
// Returns buckets written. Byte-exact with worker bucketBands.
func BucketBands(pcm []byte, n, fs int, out []byte) int {
	if len(pcm) < 2 || n <= 0 {
		return 0
	}
	return int(C.rz_bucket_bands((*C.uint8_t)(unsafe.Pointer(&pcm[0])), C.size_t(len(pcm)),
		C.size_t(n), C.uint32_t(fs), (*C.uint8_t)(unsafe.Pointer(&out[0]))))
}

// F32ToLEBytes serializes samples to LE bytes (out: 4*len) with pre-gain + ±1 clamp;
// gain 0/1 = unity passthrough. Byte-exact with source.writeBytes.
func F32ToLEBytes(samples []float32, gain float32, out []byte) {
	if len(samples) == 0 {
		return
	}
	C.rz_f32_to_le((*C.float)(unsafe.Pointer(&samples[0])), C.size_t(len(samples)),
		C.float(gain), (*C.uint8_t)(unsafe.Pointer(&out[0])))
}

// FoldStereo folds interleaved ch-channel samples to stereo (out: frames*2).
// Byte-exact with source.toDeviceStereo (ch != 2 only; 2ch is a Go-side no-op).
func FoldStereo(in []float32, frames, ch int, out []float32) {
	if frames <= 0 || ch <= 0 {
		return
	}
	C.rz_fold_stereo((*C.float)(unsafe.Pointer(&in[0])), C.size_t(frames), C.uint32_t(ch),
		(*C.float)(unsafe.Pointer(&out[0])))
}

// PCMToF32 batch-converts frames packed PCM frames (src: frames*blockAlign bytes) to
// interleaved f32 (out: frames*ch). Byte-exact with wav decodeSample / aiff decodeSampleBE.
func PCMToF32(src []byte, frames, ch, blockAlign, bits int, isFloat, bigEndian bool, out []float32) {
	if frames <= 0 || ch <= 0 {
		return
	}
	b2u := func(b bool) C.uint32_t {
		if b {
			return 1
		}
		return 0
	}
	C.rz_pcm_to_f32((*C.uint8_t)(unsafe.Pointer(&src[0])), C.size_t(frames), C.uint32_t(ch),
		C.uint32_t(blockAlign), C.uint32_t(bits), b2u(isFloat), b2u(bigEndian),
		(*C.float)(unsafe.Pointer(&out[0])))
}

// WaveColumns folds peak buckets into per-column maxima (out: cols bytes).
// Byte-exact with giokit.WaveColumns.
func WaveColumns(peaks []byte, cols int, out []byte) {
	if len(peaks) == 0 || cols <= 0 {
		return
	}
	C.rz_wave_columns((*C.uint8_t)(unsafe.Pointer(&peaks[0])), C.size_t(len(peaks)),
		C.size_t(cols), (*C.uint8_t)(unsafe.Pointer(&out[0])))
}

// WaveEnv fills out with the smoothed 0..1 envelope at imgPps columns/sec.
// Byte-exact with deckcard.buildEnv (caller sizes out = int(dur*imgPps)+1).
func WaveEnv(peaks []byte, dur, imgPps float64, out []float64) {
	if len(peaks) == 0 || len(out) == 0 {
		return
	}
	C.rz_wave_env((*C.uint8_t)(unsafe.Pointer(&peaks[0])), C.size_t(len(peaks)),
		C.double(dur), C.double(imgPps), (*C.double)(unsafe.Pointer(&out[0])), C.size_t(len(out)))
}

// ApplyGain scales buf in place.
func ApplyGain(buf []float32, gain float32) {
	if len(buf) == 0 {
		return
	}
	C.rz_apply_gain((*C.float)(unsafe.Pointer(&buf[0])), C.size_t(len(buf)), C.float(gain))
}

// PeakAbs returns the abs peak of interleaved samples.
func PeakAbs(in []float32) float32 {
	if len(in) == 0 {
		return 0
	}
	return float32(C.rz_peak_abs((*C.float)(unsafe.Pointer(&in[0])), C.size_t(len(in))))
}
