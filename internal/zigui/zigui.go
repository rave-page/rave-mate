//go:build zigui && cgo

// Package zigui binds the Zig webui render layer (native/zigui, C ABI static lib).
// Tag-gated like zignative: build with -tags zigui after `make zig` (or
// scripts/build-zig.sh) produced native/zigui/zig-out/lib/libraveui.a.
// Untagged builds use the stub (Available()=false); the Go renderers in
// internal/webui stay the fallback + golden reference either way.
package zigui

/*
#cgo CFLAGS: -I${SRCDIR}/../../native/zigui/include
#cgo LDFLAGS: -L${SRCDIR}/../../native/zigui/zig-out/lib -lraveui
// std.json float parsing references f128 intrinsics (roundq) that Zig's bundled
// compiler-rt doesn't export for gnu targets — mingw libquadmath provides them.
#cgo windows LDFLAGS: -lquadmath
#include "raveui.h"
*/
import "C"
import "unsafe"

// abiVersion the lib must report; mismatch = stale artifact, refuse to render.
const abiVersion = 1

// Available reports the Zig UI lib is linked and ABI-compatible.
func Available() bool { return uint32(C.rz_ui_abi_version()) == abiVersion }

// RenderAppGroups renders the full App Groups view. ok=false → use the Go renderer.
func RenderAppGroups(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_appgroups(p, l, n)
	})
}

// RenderAppGroupsBody renders the #appgroups-body inner fragment (tick patch).
func RenderAppGroupsBody(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_appgroups_body(p, l, n)
	})
}

// RenderLogs renders the full Logs view.
func RenderLogs(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_logs(p, l, n)
	})
}

// RenderLogsLines renders the #log-view inner fragment (filter/tick patch).
func RenderLogsLines(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_logs_lines(p, l, n)
	})
}

// render calls a Zig renderer; copies the result and frees the Zig buffer.
func render(state []byte, f func(*C.uint8_t, C.size_t, *C.size_t) *C.uint8_t) (string, bool) {
	if len(state) == 0 {
		return "", false
	}
	p := (*C.uint8_t)(unsafe.Pointer(&state[0]))
	var n C.size_t
	out := f(p, C.size_t(len(state)), &n)
	if out == nil {
		return "", false
	}
	s := C.GoStringN((*C.char)(unsafe.Pointer(out)), C.int(n))
	C.rz_ui_free(out, n)
	return s, true
}

// ── midi ──

// RenderMIDIMon renders the MIDI monitor card.
func RenderMIDIMon(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_midimon(p, l, n)
	})
}

// RenderMIDIMonRows renders the #midi-monitor inner rows (tick patch).
func RenderMIDIMonRows(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_midimon_rows(p, l, n)
	})
}

// RenderMIDITrace renders the ravemidi driver wire-trace block.
func RenderMIDITrace(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_miditrace(p, l, n)
	})
}
