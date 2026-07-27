//go:build spout

package videoshare

// #cgo CPPFLAGS: -I${SRCDIR}/../../third_party/spout/include
// #include <stdlib.h>
// #include "spout_shim.h"
import "C"

import "unsafe"

// flipRows applies the send-path geometric flip (bit0=horizontal, bit1=vertical) from src into dst.
// Test seam over the shim's own transform - the send path calls the C function directly.
func flipRows(dst, src []byte, w, h int, flip int) {
	if len(dst) < w*h*4 || len(src) < w*h*4 || w <= 0 || h <= 0 {
		return
	}
	C.rave_spout_flip_rows((*C.uchar)(unsafe.Pointer(&dst[0])), (*C.uchar)(unsafe.Pointer(&src[0])),
		C.uint(w), C.uint(h), C.int(flip))
}
