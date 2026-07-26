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

// Poll cadence constants live in recvpoll.go (untagged) with the state machine.
const (
	recvNameCap = 256
	scanMaxN    = 64 // per-scan sender ceiling (bounded staging buffers; Spout registries are far smaller)
)

// scanSenders enumerates every registered sender name + its dimensions in ONE call on the
// process-wide registry handle (no COM object churn, no GL context). Cached by scan.go.
func scanSenders() []SenderInfo {
	names := make([]byte, recvNameCap*scanMaxN)
	dims := make([]C.uint, 2*scanMaxN)
	n := int(C.rave_spout_scan((*C.char)(unsafe.Pointer(&names[0])), C.int(recvNameCap),
		C.int(scanMaxN), (*C.uint)(unsafe.Pointer(&dims[0]))))
	if n <= 0 {
		return nil
	}
	out := make([]SenderInfo, 0, n)
	for i := 0; i < n; i++ {
		name := cstr(names[i*recvNameCap : (i+1)*recvNameCap])
		if name == "" {
			continue
		}
		out = append(out, SenderInfo{Name: name, W: int(dims[i*2]), H: int(dims[i*2+1])})
	}
	return out
}

// spoutReceiver polls one named sender on a locked OS thread.
type spoutReceiver struct {
	log    *logbus.Bus
	frames chan *image.NRGBA
	done   chan struct{}
	gate   fpsGate // capture-rate cap, checked BEFORE the readback
}

func newFrameReceiver(log *logbus.Bus, name string, o RecvOptions) (FrameReceiver, error) {
	preloadManagedDLL()
	if C.rave_spout_available() == 0 {
		return nil, fmt.Errorf("SpoutLibrary.dll not found - install it from Settings or place it beside the exe")
	}
	r := &spoutReceiver{log: log, frames: make(chan *image.NRGBA, 1), done: make(chan struct{})}
	r.gate.setFPS(o.MaxFPS)
	go r.run(name)
	return r, nil
}

func (r *spoutReceiver) Frames() <-chan *image.NRGBA { return r.frames }

// SetMaxFPS implements FPSLimiter (live cap change; <= 0 = uncapped).
func (r *spoutReceiver) SetMaxFPS(fps float64) { r.gate.setFPS(fps) }

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
	// Poll discipline (interval backoff, FPS gate, resize/deliver decisions) lives in
	// recvPoller (recvpoll.go) - the gate runs BEFORE ReceiveImage, connected or not, so
	// an over-budget or stale poll never acquires the sender's shared-texture mutex.
	p := newRecvPoller(&r.gate, time.Now())
	interval := p.interval
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-t.C:
		}
		if !p.allow(time.Now()) {
			continue
		}
		var px *C.uchar
		if len(buf) > 0 {
			px = (*C.uchar)(unsafe.Pointer(&buf[0]))
		}
		act := p.apply(int(C.rave_spout_recv(h, px, C.uint(len(buf)), &w, &hgt)),
			int(w), int(hgt), len(buf), time.Now())
		if act.resize { // (re)connect/resize: size the buffer, frame arrives on a later poll
			buf = getPix(int(w) * int(hgt) * 4)
		}
		if act.frame {
			// zero-copy handoff: the readback buffer IS the delivered frame; the next
			// readback targets a fresh pooled buffer (no full-frame memcpy - 2 GB/s at
			// 4K60). Consumers release via Frame.Release → PutPix.
			img := &image.NRGBA{Pix: buf, Stride: int(w) * 4,
				Rect: image.Rect(0, 0, int(w), int(hgt))}
			buf = getPix(len(buf))
			r.deliver(img) // newest-wins, never blocks the poller
		}
		if act.interval != interval {
			interval = act.interval
			t.Reset(interval)
		}
	}
}

// deliver is newest-wins like the shared helper, but recycles the replaced frame's
// pooled pixels - a dropped pending frame was never consumed, so nobody else holds it.
func (r *spoutReceiver) deliver(img *image.NRGBA) {
	for {
		select {
		case r.frames <- img:
			return
		default:
			select {
			case old := <-r.frames: // drop the stale pending frame, retry
				PutPix(old.Pix)
			default:
			}
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
