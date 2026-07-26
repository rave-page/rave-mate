//go:build zigui && cgo

package zigui

/*
#include "raveui.h"
*/
import "C"
import (
	"sync"
	"time"
	"unsafe"
)

// Retained-doc delta channel bindings (B7 increment ii). The stateless Render*V2 calls above are
// unchanged; these drive the stateful rz_ui_patch_* family. Slot lifetime is the CALLER's
// (internal/webui patch_chan.go owns one handle per UI × patch target and frees on teardown).
//
// One mutex serializes every retain/patch call. The slot table is a plain Zig global: renders run
// on the serialized webui act worker, but a UI teardown, `ctl perf` and the test suite do not, and
// a table this small is not worth making lock-free. Uncontended, it costs ~20 ns on a >20 µs call.
var retainMu sync.Mutex

// Handle names one retained slot ({index:32, gen:32} packed by the lib). 0 = no slot.
type Handle uint64

// RetainNew claims a slot for root message msgID. Handle 0 = the slot table is full; the caller
// stays on the stateless path (PatchCapBreach).
func RetainNew(msgID uint16) Handle {
	retainMu.Lock()
	defer retainMu.Unlock()
	return Handle(C.rz_ui_retain_new(C.uint16_t(msgID)))
}

// RetainFree drops a slot's retained state and makes every handle naming it stale. Idempotent.
func RetainFree(h Handle) {
	if h == 0 {
		return
	}
	retainMu.Lock()
	defer retainMu.Unlock()
	C.rz_ui_retain_free(C.uint64_t(h))
}

// RetainStats reports live/seeded slot counts + retained logical bytes (surfaced in `ctl perf`).
func RetainStats() (live, seeded uint32, bytes uint64) {
	var l, s C.uint32_t
	var b C.uint64_t
	retainMu.Lock()
	C.rz_ui_retain_stats(&l, &s, &b)
	retainMu.Unlock()
	return uint32(l), uint32(s), uint64(b)
}

// patch calls one rz_ui_patch_* export, copies + frees the reply and returns the status. An empty
// result with PatchOK is a legitimate outcome (the state merged; the surface renders nothing) -
// unlike the stateless exports, emptiness here is NOT a decline.
func patch(doc []byte, f func(*C.uint8_t, C.size_t, *C.size_t, *C.uint8_t) *C.uint8_t) (string, PatchStatus) {
	if len(doc) == 0 {
		return "", PatchMalformed
	}
	p := (*C.uint8_t)(unsafe.Pointer(&doc[0]))
	var n C.size_t
	var st C.uint8_t
	retainMu.Lock()
	t0 := time.Now()
	out := f(p, C.size_t(len(doc)), &n, &st)
	var s string
	if out != nil {
		s = C.GoStringN((*C.char)(unsafe.Pointer(out)), C.int(n))
		C.rz_ui_free(out, n)
	}
	retainMu.Unlock()
	if PatchStatus(st) == PatchOK {
		NoteRender(len(doc), time.Since(t0))
	}
	return s, PatchStatus(st)
}

// PatchTwitchFeed merges a delta into #twitch-feed's retained state and re-renders it.
func PatchTwitchFeed(doc []byte) (string, PatchStatus) {
	return patch(doc, func(p *C.uint8_t, l C.size_t, n *C.size_t, st *C.uint8_t) *C.uint8_t {
		return C.rz_ui_patch_twitch_feed(p, l, n, st)
	})
}

// PatchMIDIMonRows merges a delta into #midi-monitor's retained state and re-renders it.
func PatchMIDIMonRows(doc []byte) (string, PatchStatus) {
	return patch(doc, func(p *C.uint8_t, l C.size_t, n *C.size_t, st *C.uint8_t) *C.uint8_t {
		return C.rz_ui_patch_midimon_rows(p, l, n, st)
	})
}

// PatchMIDICtlStat merges a delta into a #midi-ctlstat-<i> retained state and re-renders it.
func PatchMIDICtlStat(doc []byte) (string, PatchStatus) {
	return patch(doc, func(p *C.uint8_t, l C.size_t, n *C.size_t, st *C.uint8_t) *C.uint8_t {
		return C.rz_ui_patch_midictl_stat(p, l, n, st)
	})
}

// PatchCueEditTopbar merges a delta into #ce-topbar's retained state and re-renders it.
func PatchCueEditTopbar(doc []byte) (string, PatchStatus) {
	return patch(doc, func(p *C.uint8_t, l C.size_t, n *C.size_t, st *C.uint8_t) *C.uint8_t {
		return C.rz_ui_patch_cueedit_topbar(p, l, n, st)
	})
}

// PatchTickLive merges a delta into the Live tick surface's retained state and runs the B3
// scheduler over it - the reply is the packed changed-fragment list, not HTML.
func PatchTickLive(doc []byte) ([]Frag, PatchStatus) {
	return patchFrags(doc, func(p *C.uint8_t, l C.size_t, n *C.size_t, st *C.uint8_t) *C.uint8_t {
		return C.rz_ui_patch_tick_live(p, l, n, st)
	})
}

// PatchTickLogs merges a delta into the #log-view tick surface and runs the B3 scheduler over it.
func PatchTickLogs(doc []byte) ([]Frag, PatchStatus) {
	return patchFrags(doc, func(p *C.uint8_t, l C.size_t, n *C.size_t, st *C.uint8_t) *C.uint8_t {
		return C.rz_ui_patch_tick_logs(p, l, n, st)
	})
}

// patchFrags is patch() for a scheduler surface: same status contract, RZF1 reply. A reply that
// does not walk to exactly its end is refused whole (PatchError) - never applied in part.
func patchFrags(doc []byte, f func(*C.uint8_t, C.size_t, *C.size_t, *C.uint8_t) *C.uint8_t) ([]Frag, PatchStatus) {
	if len(doc) == 0 {
		return nil, PatchMalformed
	}
	p := (*C.uint8_t)(unsafe.Pointer(&doc[0]))
	var n C.size_t
	var st C.uint8_t
	retainMu.Lock()
	t0 := time.Now()
	out := f(p, C.size_t(len(doc)), &n, &st)
	var buf []byte
	if out != nil {
		buf = C.GoBytes(unsafe.Pointer(out), C.int(n))
		C.rz_ui_free(out, n)
	}
	retainMu.Unlock()
	if s := PatchStatus(st); s != PatchOK {
		return nil, s
	}
	NoteRender(len(doc), time.Since(t0))
	frs, ok := decodeFrags(buf)
	if !ok {
		return nil, PatchError
	}
	return frs, PatchOK
}
