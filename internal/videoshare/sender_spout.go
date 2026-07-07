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
	frames chan *image.NRGBA // cap 1, newest-wins (slow frame never blocks the Sink)
	done   chan struct{}
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

// Send delivers img to deck's worker (starting it on first frame). Non-blocking: if a frame
// is already pending it is replaced with the newer one.
func (s *spoutSender) Send(deck string, img *image.NRGBA) error {
	s.mu.Lock()
	w := s.workers[deck]
	if w == nil {
		w = &deckWorker{frames: make(chan *image.NRGBA, 1), done: make(chan struct{})}
		s.workers[deck] = w
		go s.run(deck, w)
	}
	s.mu.Unlock()
	deliver(w.frames, img)
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

// Close tears down every worker and waits briefly for clean GL/sender release.
func (s *spoutSender) Close() {
	s.mu.Lock()
	ws := make([]*deckWorker, 0, len(s.workers))
	for _, w := range s.workers {
		ws = append(ws, w)
	}
	s.workers = map[string]*deckWorker{}
	s.mu.Unlock()
	for _, w := range ws {
		close(w.done)
	}
	// Give workers a moment to ReleaseSender + CloseOpenGL on their own threads.
	time.Sleep(150 * time.Millisecond)
}

// run is the per-deck worker: locks its OS thread, creates the Spout handle + GL context,
// then sends each frame until done. A create failure logs once and drains until done.
func (s *spoutSender) run(deck string, w *deckWorker) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	name := SenderName(deck)
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))

	h := C.rave_spout_create()
	if h == nil {
		s.log.Warn(source, "spout: OpenGL context unavailable; deck idle",
			map[string]any{"deck": deck, "sender": name})
		// Drain frames until torn down so Send never blocks on a dead worker.
		for {
			select {
			case <-w.done:
				return
			case <-w.frames:
			}
		}
	}
	defer C.rave_spout_release(h)

	for {
		select {
		case <-w.done:
			return
		case img := <-w.frames:
			if img == nil || len(img.Pix) == 0 {
				continue
			}
			b := img.Bounds()
			ok := C.rave_spout_send(h, cname,
				(*C.uchar)(unsafe.Pointer(&img.Pix[0])),
				C.uint(b.Dx()), C.uint(b.Dy()), spoutFlip)
			if ok == 0 {
				s.log.Debug(source, "spout: SendImage failed",
					map[string]any{"deck": deck, "sender": name})
			}
		}
	}
}

// spoutSenderCount returns the number of registered Spout senders (-1 if unavailable).
// Receiver-side registry query via a throwaway handle (no GL context needed). Test/diagnostic.
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
	log    *logbus.Bus
	frames chan *image.NRGBA // cap 1, newest-wins
	done   chan struct{}
}

// newFrameSender opens a Spout sender named name. Errors if SpoutLibrary.dll is absent so the
// caller can fall back (e.g. to a PNG file).
func newFrameSender(log *logbus.Bus, name string) (FrameSender, error) {
	preloadManagedDLL()
	if C.rave_spout_available() == 0 {
		return nil, fmt.Errorf("SpoutLibrary.dll not found - install it from Settings or place it beside the exe")
	}
	f := &frameSender{log: log, frames: make(chan *image.NRGBA, 1), done: make(chan struct{})}
	go f.run(name)
	return f, nil
}

func (f *frameSender) Send(img *image.NRGBA) error { deliver(f.frames, img); return nil }

func (f *frameSender) Close() {
	close(f.done)
	time.Sleep(150 * time.Millisecond) // let the worker ReleaseSender + CloseOpenGL on its thread
}

func (f *frameSender) run(name string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))
	h := C.rave_spout_create()
	if h == nil {
		f.log.Warn(source, "spout: OpenGL context unavailable; frame sender idle", map[string]any{"sender": name})
		for {
			select {
			case <-f.done:
				return
			case <-f.frames:
			}
		}
	}
	defer C.rave_spout_release(h)
	for {
		select {
		case <-f.done:
			return
		case img := <-f.frames:
			if img == nil || len(img.Pix) == 0 {
				continue
			}
			b := img.Bounds()
			if C.rave_spout_send(h, cname, (*C.uchar)(unsafe.Pointer(&img.Pix[0])), C.uint(b.Dx()), C.uint(b.Dy()), spoutFlip) == 0 {
				f.log.Debug(source, "spout: SendImage failed", map[string]any{"sender": name})
			}
		}
	}
}

// deliver pushes img onto a cap-1 channel, replacing any pending frame (newest-wins).
func deliver(ch chan *image.NRGBA, img *image.NRGBA) {
	for {
		select {
		case ch <- img:
			return
		default:
			select {
			case <-ch: // drop the stale pending frame, retry
			default:
			}
		}
	}
}
