//go:build spout

// Windows Spout2 backend: one named Spout sender per deck ("RaveMate Deck A" …), each
// driven by its own LockOSThread'd goroutine owning its own SPOUTLIBRARY handle + OpenGL
// context (SendImage requires a current GL context bound to the calling thread).
package videoshare

// SpoutLibrary.dll is resolved at RUNTIME by the shim (LoadLibrary), NOT import-linked - so a
// missing DLL disables only the Spout feature instead of preventing the whole exe from starting
// (a load-time import would crash before main). Hence no -lSpoutLibrary in the cgo LDFLAGS below;
// only the header include path is needed.

// #cgo CPPFLAGS: -I${SRCDIR}/../../third_party/spout/include
// #include <stdlib.h>
// #include "spout_shim.h"
import "C"

import (
	"fmt"
	"image"
	"os"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"rave.page/mate/internal/logbus"
	"rave.page/mate/internal/spoutdll"
)

const backendName = "Spout"

// openSenderWait bounds the eager sender create (GL context + one zeroed frame + GetHandle). A
// wedged driver must not hang a route open; the caller falls back to the frame path.
const openSenderWait = 5 * time.Second

var _ Sender = (*spoutSender)(nil)

// spoutSender fans deck frames out to per-deck worker goroutines. Send/Remove/Close come
// from the Sink's single goroutine; the mutex guards the map against the (unused today but
// cheap to keep correct) chance of concurrent access.
type spoutSender struct {
	log     *logbus.Bus
	mu      sync.Mutex
	workers map[string]*deckWorker
}

// deckWorker owns one Spout handle + GL context on a locked OS thread for one deck.
type deckWorker struct {
	frames  chan *frameJob // cap 1, newest-wins; Send waits for the read (handoff.go)
	done    chan struct{}
	stopped chan struct{} // closed by the worker AFTER ReleaseSender+CloseOpenGL ran
}

// newSender builds the Spout backend. The GL context is created lazily per deck in the
// worker goroutine (so a host without a GPU only fails the affected deck, not the sink).
// Errors if SpoutLibrary.dll isn't present so the Sink degrades to no-op (logged once) rather
// than warning per deck.
func newSender(log *logbus.Bus) (Sender, error) {
	preloadManagedDLL() // pull in the managed-bin SpoutLibrary.dll (install button) if present
	if C.rave_spout_available() == 0 {
		return nil, fmt.Errorf("SpoutLibrary.dll not found - install it from Settings → Overlay → Video share, or place it beside the exe")
	}
	return &spoutSender{log: log, workers: map[string]*deckWorker{}}, nil
}

// preloadManagedDLL loads a managed-bin SpoutLibrary.dll by ABSOLUTE path so the shim's
// LoadLibrary("SpoutLibrary.dll") then resolves to the already-loaded module (the managed bin dir
// isn't on the default DLL search path). No-op if it's absent or already beside the exe.
func preloadManagedDLL() {
	st := spoutdll.Probe()
	if !st.Installed {
		return
	}
	if _, err := syscall.LoadLibrary(st.Path); err != nil {
		return // best-effort; the availability check still covers the beside-exe case
	}
}

// Send delivers img to deck's worker (starting it on first frame) and returns once the worker has
// finished reading its pixels - the caller owns img again on return (handoff.go). A frame a newer
// one displaces is dropped, never queued.
func (s *spoutSender) Send(deck string, img *image.NRGBA) error {
	if img == nil || len(img.Pix) == 0 {
		return nil
	}
	s.mu.Lock()
	w := s.workers[deck]
	if w == nil {
		w = &deckWorker{frames: make(chan *frameJob, 1),
			done: make(chan struct{}), stopped: make(chan struct{})}
		s.workers[deck] = w
		go s.run(deck, w)
	}
	s.mu.Unlock()
	b := img.Bounds()
	// Blocks until the worker finished reading img.Pix: the caller may recycle it on return.
	_ = handoff(w.frames, img.Pix, b.Dx(), b.Dy(), handoffBudget, func() {
		s.log.Warn(source, "spout: sender worker stuck in a driver call - frame handoff waiting (the caller's buffer cannot be recycled until it returns)",
			map[string]any{"deck": deck})
	})
	return nil
}

// Remove tears down a deck's worker. No-op if absent.
func (s *spoutSender) Remove(deck string) error {
	s.mu.Lock()
	w := s.workers[deck]
	delete(s.workers, deck)
	s.mu.Unlock()
	if w != nil {
		close(w.done)
	}
	return nil
}

// Close tears down every worker and joins them (bounded) so ReleaseSender + CloseOpenGL
// provably ran - the old fixed sleep returned regardless, orphaning GL contexts + DXGI
// shared handles when a worker sat in a blocking driver call.
func (s *spoutSender) Close() {
	s.mu.Lock()
	ws := make([]*deckWorker, 0, len(s.workers))
	for _, w := range s.workers {
		ws = append(ws, w)
	}
	s.workers = map[string]*deckWorker{}
	s.mu.Unlock()
	stopped := make([]<-chan struct{}, 0, len(ws))
	for _, w := range ws {
		close(w.done)
		stopped = append(stopped, w.stopped)
	}
	if n := waitAll(stopped, closeJoin); n > 0 {
		s.log.Error(source, "spout: sender workers stuck in a driver call at close - abandoning (GL context + DXGI handle may leak)",
			map[string]any{"stuck": n})
	}
}

// run is the per-deck worker: locks its OS thread, creates the Spout handle + GL context,
// then sends each frame until done. A create failure logs once and drains until done.
func (s *spoutSender) run(deck string, w *deckWorker) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(w.stopped) // AFTER the deferred release - Close's join proves teardown ran

	name := SenderName(deck)
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))

	h := C.rave_spout_create()
	if h == nil {
		s.log.Warn(source, "spout: OpenGL context unavailable; deck idle",
			map[string]any{"deck": deck, "sender": name})
		// Drain frames until torn down so Send never blocks on a dead worker. Ack each one
		// immediately (unread) - a discarded frame must still release its producer.
		for {
			select {
			case <-w.done:
				return
			case j := <-w.frames:
				j.reclaim()
			}
		}
	}
	defer C.rave_spout_release(h)

	for {
		select {
		case <-w.done:
			return
		case j := <-w.frames:
			if !j.claim() {
				continue // reclaimed by a newer frame or an expired waiter
			}
			ok := C.rave_spout_send(h, cname,
				(*C.uchar)(unsafe.Pointer(&j.pix[0])),
				C.uint(j.w), C.uint(j.h), spoutFlip)
			j.finish(ok != 0) // pixels are ours no longer: the producer may recycle now
			if ok == 0 {
				s.log.Debug(source, "spout: SendImage failed",
					map[string]any{"deck": deck, "sender": name})
			}
		}
	}
}

// spoutSenderCount returns the number of registered Spout senders (-1 if unavailable).
// Receiver-side registry query via a throwaway handle (no GL context needed). Test/diagnostic.
// spoutFlip is the geometric flip requested for every frame (bit0=horizontal, bit1=vertical).
// Cost note: flip 0 (the default) costs NO host pass; a vertical flip is whole-row memcpys and a
// horizontal mirror a 32-bit-per-pixel row reverse. Spout's own bInvert would do the vertical flip
// on the GPU but publishes a BLACK texture on this SDK pairing - see rave_spout_send.
// spoutFlip is the geometric flip applied to every frame before Spout send (bit0=horizontal,
// bit1=vertical), set once from RAVE_SPOUT_FLIP: none|h|v|hv (default h). Spout”s GL/DX path on
// some GPUs mirrors/flips the shared texture vs the receiver; this lets the user land it upright
// without a rebuild. Receivers (OBS) vary, so it is configurable rather than hard-coded.
var spoutFlip = parseSpoutFlip(os.Getenv("RAVE_SPOUT_FLIP"))

func parseSpoutFlip(v string) C.int {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "none", "0", "off":
		return 0
	case "h", "horizontal", "mirror":
		return 1
	case "v", "vertical", "flip":
		return 2
	case "hv", "vh", "180", "both":
		return 3
	default:
		return 0 // default: none - measured upright on the dev rig; adjust via RAVE_SPOUT_FLIP if your receiver differs
	}
}

func spoutSenderCount() int { return int(C.rave_spout_sender_count()) }

// spoutFindSender reports whether a sender with name is currently registered. Test/diagnostic.
func spoutFindSender(name string) bool {
	cn := C.CString(name)
	defer C.free(unsafe.Pointer(cn))
	return C.rave_spout_find(cn) == 1
}

// ── single-name FrameSender (VRSL grid) ──────────────────────────────────────

// frameSender publishes frames under one fixed Spout name from its own LockOSThread'd goroutine
// (own SPOUTLIBRARY handle + GL context), mirroring deckWorker but for a single stream.
type frameSender struct {
	log     *logbus.Bus
	name    string
	frames  chan *frameJob // cap 1; Send waits for the read (handoff.go)
	done    chan struct{}
	stopped chan struct{} // closed by the worker AFTER ReleaseSender+CloseOpenGL ran

	// Eager-create (SharedSender, zigmedia inc 2): the worker initialises the sender at start and
	// reports its shared-texture handle here, so a foreign decoder can render straight into it.
	// Written once by the worker before ready closes; read-only afterwards.
	openW, openH int
	share        uint64
	shareFmt     uint32
	ready        chan struct{} // closed after the eager create attempt (success or not)
	openErr      string
}

// spoutSenderFmt is the DXGI format requested for an eagerly created sender: B8G8R8A8_UNORM, the
// Spout DX11 default and the one format every D3D11 video processor accepts as an OUTPUT view.
// Pinning it means the decoder child never has to guess (and its allowlist can stay tight).
const spoutSenderFmt = 87

// Handle implements SharedSender.
func (f *frameSender) Handle() uint64 { return f.share }

// Format implements SharedSender.
func (f *frameSender) Format() uint32 { return f.shareFmt }

// newFrameSender opens a Spout sender named name. Errors if SpoutLibrary.dll is absent so the
// caller can fall back (e.g. to a PNG file).
func newFrameSender(log *logbus.Bus, name string) (FrameSender, error) {
	preloadManagedDLL()
	if C.rave_spout_available() == 0 {
		return nil, fmt.Errorf("SpoutLibrary.dll not found - install it from Settings or place it beside the exe")
	}
	f := &frameSender{log: log, name: name, frames: make(chan *frameJob, 1),
		done: make(chan struct{}), stopped: make(chan struct{}), ready: make(chan struct{})}
	go f.run(name)
	return f, nil
}

// newSharedSender opens the sender EAGERLY at w×h and reports its shared-texture handle. The
// worker owns the GL context, so the create happens there and this blocks on it (bounded) - the
// handle has to be known before the caller can decide between the native decode session and the
// frame path.
func newSharedSender(log *logbus.Bus, name string, w, h int) (SharedSender, error) {
	preloadManagedDLL()
	if C.rave_spout_available() == 0 {
		return nil, fmt.Errorf("SpoutLibrary.dll not found - install it from Settings or place it beside the exe")
	}
	f := &frameSender{log: log, name: name, frames: make(chan *frameJob, 1),
		done: make(chan struct{}), stopped: make(chan struct{}), ready: make(chan struct{}),
		openW: w, openH: h}
	go f.run(name)
	select {
	case <-f.ready:
	case <-time.After(openSenderWait):
		f.Close()
		return nil, fmt.Errorf("videoshare: sender %q did not initialise within %s", name, openSenderWait)
	}
	if f.share == 0 {
		err := f.openErr
		if err == "" {
			err = "no DX11 shared texture"
		}
		f.Close()
		return nil, fmt.Errorf("videoshare: sender %q has no GPU destination texture: %s", name, err)
	}
	log.Info(source, "shared sender open - GPU destination texture published", map[string]any{
		"sender": name, "w": w, "h": h, "fmt": f.shareFmt})
	return f, nil
}

// Send publishes img and returns only once the worker has finished reading its pixels - the
// medialink.Sink contract lets the producer (mediaroute's receive sink, over mediapipe's decoder)
// recycle the buffer immediately, and the old async queue read it after that recycle: torn frames.
func (f *frameSender) Send(img *image.NRGBA) error {
	if img == nil || len(img.Pix) == 0 {
		return nil
	}
	b := img.Bounds()
	_ = handoff(f.frames, img.Pix, b.Dx(), b.Dy(), handoffBudget, func() {
		f.log.Warn(source, "spout: frame-sender worker stuck in a driver call - frame handoff waiting (the caller's buffer cannot be recycled until it returns)",
			map[string]any{"sender": f.name})
	})
	return nil
}

// Close joins the worker (bounded) - see spoutSender.Close.
func (f *frameSender) Close() {
	close(f.done)
	if waitAll([]<-chan struct{}{f.stopped}, closeJoin) > 0 {
		f.log.Error(source, "spout: frame-sender worker stuck in a driver call at close - abandoning (GL context + DXGI handle may leak)", nil)
	}
}

func (f *frameSender) run(name string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(f.stopped) // AFTER the deferred release - Close's join proves teardown ran
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	h := C.rave_spout_create()
	if h == nil {
		f.log.Warn(source, "spout: OpenGL context unavailable; frame sender idle", map[string]any{"sender": name})
		f.openErr = "no OpenGL context"
		f.signalReady()
		for {
			select {
			case <-f.done:
				return
			case j := <-f.frames:
				j.reclaim() // discarded, but the producer must still be released
			}
		}
	}
	defer C.rave_spout_release(h)
	f.eagerCreate(h, cname)
	for {
		select {
		case <-f.done:
			return
		case j := <-f.frames:
			if !j.claim() {
				continue
			}
			ok := C.rave_spout_send(h, cname, (*C.uchar)(unsafe.Pointer(&j.pix[0])), C.uint(j.w), C.uint(j.h), spoutFlip)
			j.finish(ok != 0)
			if ok == 0 {
				f.log.Debug(source, "spout: SendImage failed", map[string]any{"sender": name})
			}
		}
	}
}

// eagerCreate initialises the sender + captures its shared-texture handle (SharedSender only).
// Runs on the worker thread, which owns the GL context.
func (f *frameSender) eagerCreate(h unsafe.Pointer, cname *C.char) {
	defer f.signalReady()
	if f.openW <= 0 || f.openH <= 0 {
		return // plain FrameSender: the sender materialises on the first Send, as before
	}
	var share C.ulonglong
	var gotFmt C.uint
	rc := C.rave_spout_open_sender(h, cname, C.uint(f.openW), C.uint(f.openH), spoutSenderFmt, &share, &gotFmt)
	if rc != 1 || share == 0 {
		switch rc {
		case -1:
			f.openErr = "SendImage refused (no GL/DX interop for this geometry)"
		case -2:
			f.openErr = "GetHandle returned no DX11 shared texture (CPU/memoryshare sender)"
		default:
			f.openErr = fmt.Sprintf("rave_spout_open_sender rc=%d share=%#x", int(rc), uint64(share))
		}
		return
	}
	f.share = uint64(share)
	// The format Spout ACTUALLY created, not the one requested: the child validates it against the
	// texture desc and refuses anything outside its allowlist, so guessing here would be a lie.
	f.shareFmt = uint32(gotFmt)
	if f.shareFmt == 0 {
		f.shareFmt = spoutSenderFmt
	}
}

// signalReady closes ready once (both the success and the failure paths run it).
func (f *frameSender) signalReady() {
	select {
	case <-f.ready:
	default:
		close(f.ready)
	}
}
