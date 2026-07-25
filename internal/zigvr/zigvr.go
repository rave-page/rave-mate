//go:build zigvr && cgo

package zigvr

/*
#cgo CFLAGS: -I${SRCDIR}/../../native/zigvr/include
#cgo LDFLAGS: -L${SRCDIR}/../../native/zigvr/zig-out/lib -lravevr
#include "ravevr.h"
*/
import "C"
import (
	"fmt"
	"unsafe"
)

// abiVersion the lib must report; mismatch = stale artifact, refuse to run.
const abiVersion = 1

// Available reports the Zig raster lib is linked and ABI-compatible.
func Available() bool { return uint32(C.rz_vr_abi_version()) == abiVersion }

// Render executes the display list into pix (NRGBA, stride 4*w). Ops must be
// pre-clipped to [0,w)×[0,h); the lib re-validates every bound and errors out
// rather than writing out of range.
func Render(pix []byte, w, h int, l *List) error {
	if w <= 0 || h <= 0 || len(pix) < w*h*4 {
		return fmt.Errorf("zigvr: bad canvas %dx%d (%d bytes)", w, h, len(pix))
	}
	if len(l.Ops) == 0 {
		return nil
	}
	var mp *C.uint8_t
	if len(l.Mask) > 0 {
		mp = (*C.uint8_t)(unsafe.Pointer(&l.Mask[0]))
	}
	rc := C.rz_vr_render(
		(*C.uint8_t)(unsafe.Pointer(&pix[0])), C.int32_t(w), C.int32_t(h),
		(*C.RzVrOp)(unsafe.Pointer(&l.Ops[0])), C.size_t(len(l.Ops)),
		mp, C.size_t(len(l.Mask)))
	if rc != 0 {
		return fmt.Errorf("zigvr: rz_vr_render rc %d", int32(rc))
	}
	return nil
}
