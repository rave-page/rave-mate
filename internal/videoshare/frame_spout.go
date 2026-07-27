//go:build spout

package videoshare

// #cgo CPPFLAGS: -I${SRCDIR}/../../third_party/spout/include
// #include <stdlib.h>
// #include "spout_shim.h"
import "C"

import "unsafe"

// senderFrame reads a sender's Spout frame counter via a metadata-only receiver: no GL context, no
// ReceiveImage, no readback - one shared-memory read (see spout_shim.cpp framereader).
func senderFrame(name string) (int64, float64, bool) {
	cn := C.CString(name)
	defer C.free(unsafe.Pointer(cn))
	var f C.longlong
	var fps C.double
	if C.rave_spout_sender_frame(cn, &f, &fps) == 0 {
		return 0, 0, false
	}
	return int64(f), float64(fps), true
}
