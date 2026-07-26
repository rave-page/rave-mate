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
	recvPollEvery = 4 * time.Millisecond  // ~250 Hz poll while frames flow; IsFrameNew gates actual work
	recvPollIdle  = 50 * time.Millisecond // backed-off poll once the sender goes quiet (no frame for recvIdleAfter)
	recvIdleAfter = 2 * time.Second       // quiet period before backing off (reconnect latency ≤ recvPollIdle)
	recvNameCap   = 256
	scanMaxN      = 64 // per-scan sender ceiling (bounded staging buffers; Spout registries are far smaller)
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
	// Adaptive poll: 4ms while frames flow (latency), 50ms once the sender goes quiet -
	// a 250 Hz busy-poll against an idle/closed sender is pure wakeup churn.
	interval := recvPollEvery
	lastFrame := time.Now()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-r.done:
			return
		case <-t.C:
		}
		// FPS cap: skip the whole readback while over budget. Only once connected (len(buf) > 0) -
		// (re)connect/resize detection stays at the fast poll rate so a route comes up promptly.
		if len(buf) > 0 && !r.gate.allow(time.Now().UnixNano()) {
			continue
		}
		var px *C.uchar
		if len(buf) > 0 {
			px = (*C.uchar)(unsafe.Pointer(&buf[0]))
		}
		got := false
		switch C.rave_spout_recv(h, px, C.uint(len(buf)), &w, &hgt) {
		case 2: // (re)connected / resized: size the buffer, frame arrives on the next poll
			if w > 0 && hgt > 0 {
				buf = getPix(int(w) * int(hgt) * 4)
			}
			got = true // activity - stay/return to the fast poll
		case 1:
			// zero-copy handoff: the readback buffer IS the delivered frame; the next
			// readback targets a fresh pooled buffer (no full-frame memcpy - 2 GB/s at
			// 4K60). Consumers release via Frame.Release → PutPix.
			img := &image.NRGBA{Pix: buf, Stride: int(w) * 4,
				Rect: image.Rect(0, 0, int(w), int(hgt))}
			buf = getPix(len(buf))
			r.deliver(img) // newest-wins, never blocks the poller
			got = true
		}
		if got {
			lastFrame = time.Now()
			if interval != recvPollEvery {
				interval = recvPollEvery
				t.Reset(interval)
			}
		} else if interval != recvPollIdle && time.Since(lastFrame) > recvIdleAfter {
			interval = recvPollIdle
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
