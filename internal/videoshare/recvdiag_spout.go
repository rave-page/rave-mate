//go:build spout

package videoshare

// #cgo CPPFLAGS: -I${SRCDIR}/../../third_party/spout/include
// #include <stdlib.h>
// #include "spout_shim.h"
import "C"

import (
	"runtime"
	"unsafe"
)

// recvdiag - one receive attempt with every SDK flag alongside it, for triaging the class of bug
// where the receive path reports success + frame-new and delivers nothing. The caller canaries the
// buffer, so "copied zeros" and "never written" become distinguishable.

// RecvDiagSample is one probe attempt.
type RecvDiagSample struct {
	RecvOK, Updated, FrameNew, Connected bool
	SenderW, SenderH, SenderFmt          uint32
	SenderCPU, SenderGLDX                bool
	SenderFrame                          int64
	SenderHandle                         uint64
	Canary, Zeros, Other                 int // byte census of the buffer AFTER the call
}

// RecvDiag binds a throwaway receiver to name and probes it `attempts` times, canarying the buffer
// before each attempt. Own thread + own GL context, released before returning. Diagnostic only.
func RecvDiag(name string, w, h, attempts int, canary byte, pause func()) ([]RecvDiagSample, error) {
	size, ok := FrameBytes(w, h)
	if !ok {
		return nil, errShareDims
	}
	preloadManagedDLL()
	if C.rave_spout_available() == 0 {
		return nil, errShareDims
	}
	type res struct {
		s   []RecvDiagSample
		err error
	}
	ch := make(chan res, 1)
	go func() {
		runtime.LockOSThread() // ReceiveImage needs the GL context current on THIS thread
		defer runtime.UnlockOSThread()
		cn := C.CString(name)
		defer C.free(unsafe.Pointer(cn))
		hdl := C.rave_spout_create()
		if hdl == nil {
			ch <- res{err: errShareDims}
			return
		}
		defer C.rave_spout_recv_release(hdl)
		pix := make([]byte, size)
		out := make([]RecvDiagSample, 0, attempts)
		var d C.rave_spout_diag
		for i := 0; i < attempts; i++ {
			for j := range pix {
				pix[j] = canary
			}
			C.rave_spout_recv_diag(hdl, cn, (*C.uchar)(unsafe.Pointer(&pix[0])), C.uint(size), &d)
			s := RecvDiagSample{
				RecvOK: d.recv_ok != 0, Updated: d.updated != 0, FrameNew: d.frame_new != 0,
				Connected: d.connected != 0,
				SenderW:   uint32(d.sw), SenderH: uint32(d.sh), SenderFmt: uint32(d.sfmt),
				SenderCPU: d.cpu != 0, SenderGLDX: d.gldx != 0,
				SenderFrame: int64(d.frame), SenderHandle: uint64(d.handle),
			}
			for _, b := range pix {
				switch b {
				case canary:
					s.Canary++
				case 0:
					s.Zeros++
				default:
					s.Other++
				}
			}
			out = append(out, s)
			if pause != nil {
				pause()
			}
		}
		ch <- res{s: out}
	}()
	r := <-ch
	return r.s, r.err
}
