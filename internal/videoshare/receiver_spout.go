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
	log     *logbus.Bus
	frames  chan *image.NRGBA
	done    chan struct{}
	stopped chan struct{} // closed by the worker AFTER ReleaseReceiver+CloseOpenGL ran
	gate    fpsGate       // capture-rate cap, checked BEFORE the readback
}

func newFrameReceiver(log *logbus.Bus, name string, o RecvOptions) (FrameReceiver, error) {
	preloadManagedDLL()
	if C.rave_spout_available() == 0 {
		return nil, fmt.Errorf("SpoutLibrary.dll not found - install it from Settings or place it beside the exe")
	}
	r := &spoutReceiver{log: log, frames: make(chan *image.NRGBA, 1),
		done: make(chan struct{}), stopped: make(chan struct{})}
	r.gate.setFPS(o.MaxFPS)
	go r.run(name)
	return r, nil
}

func (r *spoutReceiver) Frames() <-chan *image.NRGBA { return r.frames }

// SetMaxFPS implements FPSLimiter (live cap change; <= 0 = uncapped).
func (r *spoutReceiver) SetMaxFPS(fps float64) { r.gate.setFPS(fps) }

// Close joins the worker (bounded) so ReleaseReceiver + CloseOpenGL provably ran before
// return - the old fixed sleep let route churn orphan GL contexts + DXGI shared handles.
func (r *spoutReceiver) Close() {
	close(r.done)
	if waitAll([]<-chan struct{}{r.stopped}, closeJoin) > 0 {
		r.log.Error(source, "spout: receiver worker stuck in a driver call at close - abandoning (GL context + DXGI handle may leak)", nil)
	}
}

// run owns the handle + GL context and polls frames until Close.
func (r *spoutReceiver) run(name string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(r.stopped) // AFTER the deferred release below - Close's join proves teardown ran
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
	var bw, bh int // geometry buf was sized for (a resize between poll passes must not tear Rect)
	var w, hgt C.uint
	var dropped int     // frames dropped at the pool's live-bytes ceiling (bounded capture)
	var warnedGeom bool // rate-limit the implausible-geometry warning
	defer func() {
		if len(buf) > 0 {
			PutPix(buf) // the un-delivered readback buffer (leaked on every receiver close)
		}
		if dropped > 0 {
			r.log.Info(source, "spout: capture frames dropped at the in-flight ceiling",
				map[string]any{"sender": name, "dropped": dropped})
		}
	}()
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
		// The cgo call WRITES w/hgt, so read them in a separate statement: Go leaves the
		// evaluation order of non-call operands vs. the call in one expression unspecified.
		code := int(C.rave_spout_recv(h, cname, px, C.uint(len(buf)), &w, &hgt))
		act := p.apply(code, int(w), int(hgt), len(buf), time.Now())
		if act.badGeom && !warnedGeom {
			warnedGeom = true
			r.log.Warn(source, "spout: sender reported an implausible frame size - ignoring until it settles",
				map[string]any{"sender": name, "w": int(w), "h": int(hgt)})
		}
		if act.resize { // (re)connect/resize: size the buffer, frame arrives on a later poll
			if len(buf) > 0 {
				PutPix(buf) // recycle the old size's buffer (leaked before)
			}
			buf = getPix(act.size) // act.size is validated (FrameBytes); nil only if the pool refused
			bw, bh = int(w), int(hgt)
		}
		if act.frame && len(buf) > 0 {
			// zero-copy handoff: the readback buffer IS the delivered frame; the next
			// readback targets a fresh pooled buffer (no full-frame memcpy - 2 GB/s at
			// 4K60). Consumers release via Frame.Release → PutPix.
			//
			// Geometry comes from the LAST RESIZE (bw/bh), not the current poll: the buffer
			// was sized then, and a sender resize between the two would otherwise publish a
			// Rect that disagrees with len(Pix).
			next, ok := tryGetPix(len(buf))
			if !ok {
				// Live-bytes ceiling: downstream is holding the maximum in-flight frames.
				// Newest-wins - drop THIS frame and re-use its buffer for the next readback
				// rather than allocating (allocating is what OOM'd the child at 4K60).
				dropped++
				continue
			}
			img := &image.NRGBA{Pix: buf, Stride: bw * 4, Rect: image.Rect(0, 0, bw, bh)}
			buf = next
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

// grabSenderFrame is a ONE-SHOT receive that ignores Spout's IsFrameNew hint and returns whatever
// the sender's texture currently holds.
//
// Why it exists: a natively DECODED route (zigmedia inc 2) has its texture written by the encoder
// child, and Spout bumps a sender's frame counter only inside SendTexture/SendImage - so
// IsFrameNew() stays false while the CONTENT changes every frame. The polling receiver above gates
// on that hint (correctly: it is what keeps an idle sender from costing 250 readbacks/s), which
// makes it useless as an oracle for "is a picture actually being published". This is that oracle.
//
// MEASURED LIMITATION (this rig, 2026-07-27): even this helper reads all-zero pixels for a Spout
// sender in ANOTHER process while ReceiveImage reports success (rc=1) - including for an ordinary
// SendImage publish. So Spout's receive side is NOT a usable oracle here at all, and the decode
// path's picture is verified by the child's own GPU read-back instead
// (RAVE_MATE_MFDEC_PROBE_BANDS, native/zigenc/src/dec.zig probeBands). Kept because the live gate
// uses it as a POSITIVE CONTROL: it only trusts a blank read-back once it has seen a non-blank one.
//
// Own thread + own GL context + own handle, released before returning. Not for the hot path.
func grabSenderFrame(name string, w, h int) (*image.NRGBA, error) {
	size, ok := FrameBytes(w, h)
	if !ok {
		return nil, fmt.Errorf("videoshare: bad grab size %dx%d", w, h)
	}
	preloadManagedDLL()
	if C.rave_spout_available() == 0 {
		return nil, errShareDims
	}
	type res struct {
		img *image.NRGBA
		err error
	}
	ch := make(chan res, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		cn := C.CString(name)
		defer C.free(unsafe.Pointer(cn))
		hdl := C.rave_spout_create()
		if hdl == nil {
			ch <- res{err: fmt.Errorf("videoshare: no OpenGL context for a grab")}
			return
		}
		defer C.rave_spout_recv_release(hdl)
		C.rave_spout_set_receiver(hdl, cn)
		pix := make([]byte, size)
		var gw, gh C.uint
		deadline := time.Now().Add(3 * time.Second)
		lastRC, polls := -99, 0
		for time.Now().Before(deadline) {
			rc := int(C.rave_spout_recv(hdl, cn, (*C.uchar)(unsafe.Pointer(&pix[0])),
				C.uint(len(pix)), &gw, &gh))
			lastRC, polls = rc, polls+1
			// rc 0 = connected, "no NEW frame" - but ReceiveImage still copied the current texture,
			// which is exactly the case this helper exists for. Keep polling only while the buffer is
			// still blank (the sender's initial zeroed frame).
			if (rc == 0 || rc == 1) && int(gw) == w && int(gh) == h && !allZero(pix) {
				ch <- res{img: &image.NRGBA{Pix: pix, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}}
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		ch <- res{err: fmt.Errorf("videoshare: sender %q published no non-blank frame within 3s (polls=%d lastRC=%d dims=%dx%d)",
			name, polls, lastRC, int(gw), int(gh))}
	}()
	r := <-ch
	return r.img, r.err
}

// allZero reports whether every byte is 0 (a freshly created, never-written sender texture).
func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
