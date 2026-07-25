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

// --- vrchat ---

// RenderVRChat renders the full VRChat tab.
func RenderVRChat(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vrchat(p, l, n)
	})
}

// RenderVRChatStatus renders the #vrc-status-region fragment (live tick).
func RenderVRChatStatus(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vrchat_status(p, l, n)
	})
}

// RenderVRChatEditor renders the #vrc-editor fragment.
func RenderVRChatEditor(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vrchat_editor(p, l, n)
	})
}

// RenderVRChatCampaths renders the #vrc-campaths fragment.
func RenderVRChatCampaths(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vrchat_campaths(p, l, n)
	})
}

// RenderVRChatPhotos renders the #vrc-photos-body fragment.
func RenderVRChatPhotos(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vrchat_photos(p, l, n)
	})
}

// RenderVRCGroups renders the VRChat ▸ Groups sub-view (#vrcg-body).
func RenderVRCGroups(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vrcgroups(p, l, n)
	})
}

// --- worlds ---

// RenderWorlds renders the full Worlds tab.
func RenderWorlds(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_worlds(p, l, n)
	})
}

// RenderWorldsLinkHint renders the #world-linkhint fragment (live tick).
func RenderWorldsLinkHint(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_worlds_linkhint(p, l, n)
	})
}

// RenderWorldsGitHub renders the #world-gh fragment.
func RenderWorldsGitHub(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_worlds_github(p, l, n)
	})
}

// RenderWorldsStatus renders one #world-st-<key> fragment.
func RenderWorldsStatus(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_worlds_status(p, l, n)
	})
}

// RenderWorldsUnityRows renders the #world-unity-rows fragment.
func RenderWorldsUnityRows(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_worlds_unityrows(p, l, n)
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

// RenderMIDICtl renders the full MIDI tab.
func RenderMIDICtl(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_midictl(p, l, n)
	})
}

// RenderMIDIActive renders the #midi-active status line (tick patch).
func RenderMIDIActive(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_midictl_active(p, l, n)
	})
}

// RenderMIDICtlStat renders a controller's #midi-ctlstat-<i> inner (tick patch). ok=false
// when the fragment is empty - the Go fallback renders the same empty string.
func RenderMIDICtlStat(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_midictl_stat(p, l, n)
	})
}

// --- media --- (automations, overlays, twitch, editor)

// RenderAutomations renders the full Automations view.
func RenderAutomations(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_automations(p, l, n)
	})
}

// RenderAutomationsBody renders the #auto-body inner fragment (tick patch).
func RenderAutomationsBody(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_automations_body(p, l, n)
	})
}

// RenderOverlays renders the full Overlays view.
func RenderOverlays(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_overlays(p, l, n)
	})
}

// RenderOverlaysAppearance renders the #ovl-appearance fragment.
func RenderOverlaysAppearance(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_overlays_appearance(p, l, n)
	})
}

// RenderOverlaysSpout renders the #ovl-spout fragment.
func RenderOverlaysSpout(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_overlays_spout(p, l, n)
	})
}

// RenderOverlaysStrip renders the #ovl-strip fragment.
func RenderOverlaysStrip(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_overlays_strip(p, l, n)
	})
}

// RenderOverlaysStatus renders one #ovl-st-<kind> status fragment.
func RenderOverlaysStatus(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_overlays_status(p, l, n)
	})
}

// RenderTwitch renders the full Twitch view.
func RenderTwitch(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_twitch(p, l, n)
	})
}

// RenderTwitchObs renders the #twitch-obs fragment.
func RenderTwitchObs(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_twitch_obs(p, l, n)
	})
}

// RenderTwitchPresets renders the #twitch-presets fragment.
func RenderTwitchPresets(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_twitch_presets(p, l, n)
	})
}

// RenderTwitchFeed renders the #twitch-feed inner fragment.
func RenderTwitchFeed(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_twitch_feed(p, l, n)
	})
}

// RenderEditor renders the full Editor view.
func RenderEditor(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_editor(p, l, n)
	})
}

// RenderEditorPreview renders the #ed-preview inner fragment (tick patch).
func RenderEditorPreview(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_editor_preview(p, l, n)
	})
}

// --- publish ---

// RenderPublish renders the full local Publish cockpit.
func RenderPublish(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_publish(p, l, n)
	})
}

// RenderPublishHero renders the #pub-hero inner fragment (live tick patch). A state
// with no recorder renders empty ⇒ ok=false ⇒ the Go fallback renders the same "".
func RenderPublishHero(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_publish_hero(p, l, n)
	})
}
