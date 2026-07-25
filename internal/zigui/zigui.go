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

// --- motion + live (fleet: live batch) ---

// RenderMotion renders the full Motion view.
func RenderMotion(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_motion(p, l, n)
	})
}

// RenderMotionBody renders the #mo-body inner fragment (section switch patch).
func RenderMotionBody(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_motion_body(p, l, n)
	})
}

// RenderLive renders the full Live cockpit view.
func RenderLive(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_live(p, l, n)
	})
}

// RenderLiveFrag renders one live-tab fragment (kind: transport|np|status|decks|signals|
// cockpit|link|graph|perf|strip). Unknown kind → ok=false → the Go renderer.
func RenderLiveFrag(kind string, stateJSON []byte) (string, bool) {
	if kind == "" {
		return "", false
	}
	kb := []byte(kind)
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_live_frag((*C.uint8_t)(unsafe.Pointer(&kb[0])), C.size_t(len(kb)), p, l, n)
	})
}

// --- end motion + live ---

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
