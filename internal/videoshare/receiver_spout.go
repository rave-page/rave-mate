//go:build spout

// Windows Spout2 receiver backend: one LockOSThread'd worker per receiver owning its own
// SPOUTLIBRARY handle + GL context (ReceiveImage needs a current context, like SendImage).
package videoshare

// #cgo CPPFLAGS: -I${SRCDIR}/../../third_party/spout/include
// #include <stdlib.h>
// #include "spout_shim.h"
import "C"

import (
	"fmt"
	"image"
	"runtime"
	"time"
	"unsafe"

	"rave.page/mate/internal/logbus"
)

const (
	recvPollEvery = 4 * time.Millisecond // ~250 Hz poll; IsFrameNew gates actual work
	recvNameCap   = 256
)

// listSenders enumerates the registered Spout sender names (registry query, no GL).
func listSenders() []string {
	n := int(C.rave_spout_sender_count())
	if n <= 0 {
		return nil
	}
	out := make([]string, 0, n)
	buf := make([]byte, recvNameCap)
	for i := 0; i < n; i++ {
		if C.rave_spout_sender_name(C.int(i), (*C.char)(unsafe.Pointer(&buf[0])), recvNameCap) != 1 {
			continue
		}
		if name := cstr(buf); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// senderSize queries a named sender's dimensions from the registry.
func senderSize(name string) (int, int, bool) {
	cn := C.CString(name)
	defer C.free(unsafe.Pointer(cn))
	var w, h C.uint
	if C.rave_spout_sender_size(cn, &w, &h) != 1 {
		return 0, 0, false
	}
	return int(w), int(h), true
}

// spoutReceiver polls one named sender on a locked OS thread.
type spoutReceiver struct {
	log    *logbus.Bus
	frames chan *image.NRGBA
	done   chan struct{}
}

func newFrameReceiver(log *logbus.Bus, name string) (FrameReceiver, error) {
	preloadManagedDLL()
	if C.rave_spout_available() == 0 {
		return nil, fmt.Errorf("SpoutLibrary.dll not found - install it from Settings or place it beside the exe")
	}
	r := &spoutReceiver{log: log, frames: make(chan *image.NRGBA, 1), done: make(chan struct{})}
	go r.run(name)
	return r, nil
}

func (r *spoutReceiver) Frames() <-chan *image.NRGBA { return r.frames }

func (r *spoutReceiver) Close() {
	close(r.done)
	time.Sleep(150 * time.Millisecond) // let the worker ReleaseReceiver + CloseOpenGL on its thread
}

// run owns the handle + GL context and polls frames until Close.
func (r *spoutReceiver) run(name string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(r.frames)

	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	h := C.rave_spout_create()
	if h == nil {
		r.log.Warn(source, "spout: OpenGL context unavailable; receiver idle", map[string]any{"sender": name})
		<-r.done
		return
	}
	defer C.rave_spout_recv_release(h)
	C.rave_spout_set_receiver(h, cname)

	var buf []byte
	var w, hgt C.uint
	t := time.NewTicker(recvPollEvery)
	defer t.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-t.C:
		}
		var px *C.uchar
		if len(buf) > 0 {
			px = (*C.uchar)(unsafe.Pointer(&buf[0]))
		}
		switch C.rave_spout_recv(h, px, C.uint(len(buf)), &w, &hgt) {
		case 2: // (re)connected / resized: size the buffer, frame arrives on the next poll
			if w > 0 && hgt > 0 {
				buf = make([]byte, int(w)*int(hgt)*4)
			}
		case 1:
			img := &image.NRGBA{Pix: append([]byte(nil), buf...), Stride: int(w) * 4,
				Rect: image.Rect(0, 0, int(w), int(hgt))}
			deliver(r.frames, img) // newest-wins, never blocks the poller
		}
	}
}

// cstr converts a NUL-terminated byte buffer to a string.
func cstr(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}
