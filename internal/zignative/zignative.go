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
