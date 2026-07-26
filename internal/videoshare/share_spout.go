//go:build spout

package videoshare

// #cgo CPPFLAGS: -I${SRCDIR}/../../third_party/spout/include
// #include <stdlib.h>
// #include "spout_shim.h"
import "C"

import "unsafe"

// senderShare reads the sender registry's shared-memory entry (no GL context, no receiver
// binding, no COM object churn - the same process-wide handle the scan uses).
func senderShare(name string) (uint64, uint32, int, int, bool) {
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	var sh C.ulonglong
	var fmt, w, h C.uint
	if C.rave_spout_sender_share(cname, &sh, &fmt, &w, &h) == 0 {
		return 0, 0, 0, 0, false
	}
	return uint64(sh), uint32(fmt), int(w), int(h), true
}
