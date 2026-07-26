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
// std.json float parsing references f128 intrinsics (roundq/__multf3) that Zig's
// bundled compiler-rt doesn't export for gnu targets — libquadmath (mingw on
// windows, gcc's on linux) provides them.
#cgo windows LDFLAGS: -lquadmath
#cgo linux LDFLAGS: -lquadmath -lm
#include "raveui.h"
*/
import "C"
import (
	"encoding/binary"
	"time"
	"unsafe"
)

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
	t0 := time.Now() // perf.go counters: two time.Now() per render, ~50ns on a >50µs render
	out := f(p, C.size_t(len(state)), &n)
	if out == nil {
		noteFallback(2) // 2 frames up = the Render* wrapper (see fallback.go)
		return "", false
	}
	s := C.GoStringN((*C.char)(unsafe.Pointer(out)), C.int(n))
	C.rz_ui_free(out, n)
	NoteRender(len(state), time.Since(t0))
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

// --- peers ---

// RenderPeers renders the full Peers view.
func RenderPeers(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_peers(p, l, n)
	})
}

// RenderPeersBody renders the #peers-body inner fragment (~1 Hz live tick).
func RenderPeersBody(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_peers_body(p, l, n)
	})
}

// --- library_remote ---

// RenderLibRemote renders the remote-control target switcher. ok=false when the switcher is
// hidden (empty fragment) - the Go fallback renders the same empty string.
func RenderLibRemote(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_libremote(p, l, n)
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

// RenderPublishRemote renders the full remote Publish view (a peer's recorded sets).
func RenderPublishRemote(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_publish_remote(p, l, n)
	})
}

// --- settings ---

// RenderSettings renders the full Settings view.
func RenderSettings(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_settings(p, l, n)
	})
}

// RenderSettingsContent renders the #set-content pane (sub-tab / search patch).
func RenderSettingsContent(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_settings_content(p, l, n)
	})
}

// RenderSettingsStatus renders one #stset-<id> status fragment (~1 Hz tick).
func RenderSettingsStatus(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_settings_status(p, l, n)
	})
}

// --- library ---

// RenderLibrary renders the full Library tab.
func RenderLibrary(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_library(p, l, n)
	})
}

// RenderLibraryBody renders the #lib-body inner fragment (section switch + list patches).
func RenderLibraryBody(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_library_body(p, l, n)
	})
}

// RenderLibraryDetail renders the #lib-detail inspector fragment.
func RenderLibraryDetail(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_library_detail(p, l, n)
	})
}

// RenderLibraryQueue renders the #lib-queue-body inner fragment.
func RenderLibraryQueue(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_library_queue(p, l, n)
	})
}

// RenderLibraryCueCell renders one #ce-cell-<hash> drops/cues census fragment.
func RenderLibraryCueCell(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_library_cuecell(p, l, n)
	})
}

// --- cueedit ---

// RenderCueEditTopbar renders the #ce-topbar readout strip (empty when the editor is off).
func RenderCueEditTopbar(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_cueedit_topbar(p, l, n)
	})
}

// RenderCueEditWave renders the full-width cue-edit wave strip (topbar + raw player markup).
func RenderCueEditWave(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_cueedit_wave(p, l, n)
	})
}

// RenderCueEditRail renders the cue-editor rail (#lib-detail inner in cue-edit mode).
func RenderCueEditRail(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_cueedit_rail(p, l, n)
	})
}

// --- libviews ---

// RenderLibMirror renders the #lib-body peer-mirror surface (banner + iframe).
func RenderLibMirror(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_libmirror(p, l, n)
	})
}

// RenderLibMirrorBanner renders the #rmirror-banner status strip.
func RenderLibMirrorBanner(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_libmirror_banner(p, l, n)
	})
}

// RenderRCEBody renders the #lib-body remote-cue-edit surface.
func RenderRCEBody(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_rce_body(p, l, n)
	})
}

// RenderRCEInfo renders the #rce-info pane.
func RenderRCEInfo(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_rce_info(p, l, n)
	})
}

// RenderRCESave renders the rce save/write-back rail section.
func RenderRCESave(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_rce_save(p, l, n)
	})
}

// RenderLibSmartModal renders the smart-rules editor modal.
func RenderLibSmartModal(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_lib_smartmodal(p, l, n)
	})
}

// RenderLibRelocModal renders the relocate-missing modal.
func RenderLibRelocModal(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_lib_relocmodal(p, l, n)
	})
}

// --- libfixers ---
// The Library tab's fixer/section subviews. They also render inside RenderLibrary/
// RenderLibraryBody/RenderLibraryDetail; these are the direct entry points (golden gate)
// plus RenderLibFixGFLive, the one independently patched fragment.

// RenderLibFixNavRail renders the triPane nav column (Collection tree / Browse places).
func RenderLibFixNavRail(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_libfix_navrail(p, l, n)
	})
}

// RenderLibFixPrep renders the prep-playlist picker (collection toolbar).
func RenderLibFixPrep(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_libfix_prep(p, l, n)
	})
}

// RenderLibFixGFRail renders the beatgrid-fixer rail (health/confirm/running/done).
func RenderLibFixGFRail(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_libfix_gfrail(p, l, n)
	})
}

// RenderLibFixGFLive renders the #gf-live inner fragment (~2 Hz run tick).
func RenderLibFixGFLive(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_libfix_gflive(p, l, n)
	})
}

// RenderLibFixResults renders the fixer results view that replaces the collection list.
func RenderLibFixResults(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_libfix_results(p, l, n)
	})
}

// RenderLibFixTagEdit renders the per-track tag editor (inspector Tags tail).
func RenderLibFixTagEdit(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_libfix_tagedit(p, l, n)
	})
}

// RenderLibFixCompat renders the "works well together" inspector section.
func RenderLibFixCompat(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_libfix_compat(p, l, n)
	})
}

// --- settings-sub ---
// Settings card bodies owned by other webui files (gridfix, gridfix model, account bridge,
// the #inst-update flow). They also render inside the settings tab via its block list.

// RenderSettingsGridfix renders the gridfix card body (engine variants + install controls).
func RenderSettingsGridfix(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_settings_gridfix(p, l, n)
	})
}

// RenderSettingsGridfixModel renders the gridfix model card body (checkpoints + fine-tuning).
func RenderSettingsGridfixModel(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_settings_gridfixmodel(p, l, n)
	})
}

// RenderSettingsBridge renders the account-bridge card body (enrolment + trusted sessions).
func RenderSettingsBridge(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_settings_bridge(p, l, n)
	})
}

// RenderSettingsUpdFlow renders the #inst-update region (patchUpd). ok=false when the flow is
// hidden (empty fragment) - the Go fallback renders the same empty string.
func RenderSettingsUpdFlow(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_settings_updflow(p, l, n)
	})
}

// --- end settings-sub ---

// --- player ---
// The unified media player/editor (player.go + render_player.go). One export per patch
// target: the full component, its root inner, and the vid/wave/tp/edit/export/ro/hov
// fragments. The waveform SVG + the shared loudness block ride through the state as
// trusted RAW markup (Go owns every float) - these renderers own the chrome.

// RenderPlayer renders the full .mplayer component (mpHTML).
func RenderPlayer(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player(p, l, n)
	})
}

// RenderPlayerRoot renders the #mp-<host>-root inner (mpPatchAll).
func RenderPlayerRoot(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_root(p, l, n)
	})
}

// RenderPlayerVid renders the #mp-<host>-vid inner. ok=false when no video media exists
// (empty fragment) - the Go fallback renders the same empty string.
func RenderPlayerVid(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_vid(p, l, n)
	})
}

// RenderPlayerWave renders the #mp-<host>-wave inner (SVG + chips + captions).
func RenderPlayerWave(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_wave(p, l, n)
	})
}

// RenderPlayerTp renders the #mp-<host>-tp inner (transport + seek + volume).
func RenderPlayerTp(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_tp(p, l, n)
	})
}

// RenderPlayerEdit renders the #mp-<host>-edit inner. ok=false while edit mode is OFF
// (the container stays empty so the toggle patch has a target).
func RenderPlayerEdit(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_edit(p, l, n)
	})
}

// RenderPlayerExport renders the #mp-<host>-export inner.
func RenderPlayerExport(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_export(p, l, n)
	})
}

// RenderPlayerRO renders the #mp-<host>-ro trim readout (handle-drag patch).
func RenderPlayerRO(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_ro(p, l, n)
	})
}

// RenderPlayerHov renders the #mp-<host>-hov readout line. ok=false with no active media.
func RenderPlayerHov(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_hov(p, l, n)
	})
}

// --- end player ---

// --- dialogs-a ---
// Wave-4 dialog sweep A: the publish/transcode dialog family. Each renders a WHOLE dialog
// (scrim + card + footer) for webui's openModal.

// RenderDlgChoice renders the shared message+buttons dialog (confirm / format picker /
// row context menu).
func RenderDlgChoice(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_dlg_choice(p, l, n)
	})
}

// RenderDlgTxtExport renders the tracklist text-export style dialog (preset + template +
// header switch + live preview).
func RenderDlgTxtExport(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_dlg_txtexport(p, l, n)
	})
}

// RenderDlgExportPrev renders the CSV/JSON tracklist-export preview (local + remote arms).
func RenderDlgExportPrev(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_dlg_exportprev(p, l, n)
	})
}

// RenderDlgRename renders the rename-set form dialog.
func RenderDlgRename(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_dlg_rename(p, l, n)
	})
}

// RenderDlgFix renders the capture-aligned "Fix start times" preview.
func RenderDlgFix(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_dlg_fix(p, l, n)
	})
}

// RenderDlgPreset renders the export preset editor (pbuilder mp-pedit).
func RenderDlgPreset(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_dlg_preset(p, l, n)
	})
}

// RenderDlgPatMgr renders the cue-editor saved-pattern manager.
func RenderDlgPatMgr(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_dlg_patmgr(p, l, n)
	})
}

// --- end dialogs-a ---

// --- dialogs-b ---
// Wave-4 dialog sweep B: the feature-tab dialog families. Fragment renderers serve the
// in-modal patch targets; the modal renderers include the dialog chrome.

// RenderVgRoleBody renders #vrcg-role-body (the member's add/remove-role list).
func RenderVgRoleBody(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vg_rolebody(p, l, n)
	})
}

// RenderVgInviteList renders #vrcg-inv-list (the filtered friends list).
func RenderVgInviteList(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vg_invitelist(p, l, n)
	})
}

// RenderVgRolesModal renders the group-roles dialog.
func RenderVgRolesModal(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vg_rolesmodal(p, l, n)
	})
}

// RenderVgInviteModal renders the group-invite dialog.
func RenderVgInviteModal(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vg_invitemodal(p, l, n)
	})
}

// RenderVgMemberConfirm renders the kick/ban confirm dialog.
func RenderVgMemberConfirm(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vg_memberconfirm(p, l, n)
	})
}

// RenderVgPostConfirm renders the delete-post confirm dialog.
func RenderVgPostConfirm(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vg_postconfirm(p, l, n)
	})
}

// RenderWsListEditor renders the Worlds permission-list entry editor dialog.
func RenderWsListEditor(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_ws_listeditor(p, l, n)
	})
}

// RenderWsPosterEditor renders the Worlds poster-slot editor dialog.
func RenderWsPosterEditor(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_ws_postereditor(p, l, n)
	})
}

// RenderWsFriendPicker renders the Worlds friend-picker dialog.
func RenderWsFriendPicker(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_ws_friendpicker(p, l, n)
	})
}

// RenderWsFriendList renders #world-fr-list (the filtered friends list).
func RenderWsFriendList(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_ws_friendlist(p, l, n)
	})
}

// RenderWsGroupPicker renders the Worlds group/role-picker dialog.
func RenderWsGroupPicker(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_ws_grouppicker(p, l, n)
	})
}

// RenderWsGroupList renders #world-grp-list (favorites + own groups + search results).
func RenderWsGroupList(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_ws_grouplist(p, l, n)
	})
}

// RenderWsRolePicker renders the Worlds role-grant dialog.
func RenderWsRolePicker(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_ws_rolepicker(p, l, n)
	})
}

// RenderWsRoleList renders #world-role-list (the loaded group roles).
func RenderWsRoleList(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_ws_rolelist(p, l, n)
	})
}

// RenderWsDevice renders the GitHub device-code dialog.
func RenderWsDevice(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_ws_device(p, l, n)
	})
}

// RenderAutoEditor renders the automation editor dialog (identity + match rules + action chain).
func RenderAutoEditor(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_auto_editor(p, l, n)
	})
}

// RenderAutoRunNow renders the automations run-now dialog.
func RenderAutoRunNow(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_auto_runnow(p, l, n)
	})
}

// RenderAutoSchedule renders the automations schedule-editor dialog.
func RenderAutoSchedule(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_auto_schedule(p, l, n)
	})
}

// RenderPCViewer renders the point-cloud viewer dialog shell (canvas + transport chrome).
func RenderPCViewer(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_pc_viewer(p, l, n)
	})
}

// RenderPCGpu renders the point-cloud viewer GPU prompt.
func RenderPCGpu(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_pc_gpu(p, l, n)
	})
}

// --- end dialogs-b ---

// --- phaseb-tip ---

// RenderTip renders one structured tooltip (webui tipSt). The migrated tabs compose tooltips
// in-process inside their own view render; this binding exists for the byte-parity gate.
func RenderTip(stateJSON []byte) (string, bool) {
	return render(stateJSON, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_tip(p, l, n)
	})
}

// --- end phaseb-tip ---
// --- phaseb-wire ---

// RZW1 binary-state exports (phase B pilots: appgroups + logs). Same Zig renderers as the
// JSON bindings above; the state arrives as a length-prefixed TLV document from the
// generated encoders (internal/webui/wire_gen.go) instead of JSON. ok=false → the caller
// tries the JSON binding, then its Go renderer; both downgrades show up in FallbackCounts.

// RenderAppGroupsV2 renders the full App Groups view from an RZW1 document.
func RenderAppGroupsV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_appgroups_v2(p, l, n)
	})
}

// RenderAppGroupsBodyV2 renders the #appgroups-body fragment from an RZW1 document.
func RenderAppGroupsBodyV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_appgroups_body_v2(p, l, n)
	})
}

// RenderLogsV2 renders the full Logs view from an RZW1 document.
func RenderLogsV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_logs_v2(p, l, n)
	})
}

// RenderLogsLinesV2 renders the #log-view fragment from an RZW1 document.
func RenderLogsLinesV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_logs_lines_v2(p, l, n)
	})
}

// ── B-2 fan-out ──

// RenderLiveV2 renders the full Live cockpit from an RZW1 document.
func RenderLiveV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_live_v2(p, l, n)
	})
}

// RenderLiveFragV2 renders one live-tab fragment from an RZW1 document (kind as in
// RenderLiveFrag). Each kind has its own message id, so a document for another fragment is
// refused by the header check.
func RenderLiveFragV2(kind string, state []byte) (string, bool) {
	if kind == "" {
		return "", false
	}
	kb := []byte(kind)
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_live_frag_v2((*C.uint8_t)(unsafe.Pointer(&kb[0])), C.size_t(len(kb)), p, l, n)
	})
}

// RenderMotionV2 renders the full Motion tab from an RZW1 document.
func RenderMotionV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_motion_v2(p, l, n)
	})
}

// RenderMotionBodyV2 renders the #mo-body fragment from an RZW1 document.
func RenderMotionBodyV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_motion_body_v2(p, l, n)
	})
}

// RenderPublishV2 renders the full Publish tab from an RZW1 document.
func RenderPublishV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_publish_v2(p, l, n)
	})
}

// RenderPublishHeroV2 renders the #pub-hero fragment from an RZW1 document (ok=false when the
// hero is legitimately empty - no recorder wired).
func RenderPublishHeroV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_publish_hero_v2(p, l, n)
	})
}

// RenderSettingsV2 renders the full Settings tab from an RZW1 document.
func RenderSettingsV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_settings_v2(p, l, n)
	})
}

// RenderSettingsContentV2 renders the #set-content pane from an RZW1 document.
func RenderSettingsContentV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_settings_content_v2(p, l, n)
	})
}

// RenderSettingsStatusV2 renders one #stset-<id> status fragment from an RZW1 document.
func RenderSettingsStatusV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_settings_status_v2(p, l, n)
	})
}

// RenderLibraryV2 renders the full Library tab from an RZW1 document.
func RenderLibraryV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_library_v2(p, l, n)
	})
}

// RenderLibraryBodyV2 renders the #lib-body active section from an RZW1 document.
func RenderLibraryBodyV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_library_body_v2(p, l, n)
	})
}

// RenderLibraryDetailV2 renders the #lib-detail inspector from an RZW1 document.
func RenderLibraryDetailV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_library_detail_v2(p, l, n)
	})
}

// RenderLibraryQueueV2 renders the #lib-queue-body job list from an RZW1 document.
func RenderLibraryQueueV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_library_queue_v2(p, l, n)
	})
}

// RenderLibraryCueCellV2 renders one cue-census cell from an RZW1 document.
func RenderLibraryCueCellV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_library_cuecell_v2(p, l, n)
	})
}

// RenderPlayerV2 renders the full Player view from an RZW1 document.
func RenderPlayerV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_v2(p, l, n)
	})
}

// RenderPlayerRootV2 renders the #mp-root inner from an RZW1 document.
func RenderPlayerRootV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_root_v2(p, l, n)
	})
}

// RenderPlayerVidV2 renders the #mp-vid fragment from an RZW1 document.
func RenderPlayerVidV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_vid_v2(p, l, n)
	})
}

// RenderPlayerWaveV2 renders the #mp-wave fragment from an RZW1 document.
func RenderPlayerWaveV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_wave_v2(p, l, n)
	})
}

// RenderPlayerTpV2 renders the #mp-tp transport from an RZW1 document.
func RenderPlayerTpV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_tp_v2(p, l, n)
	})
}

// RenderPlayerEditV2 renders the #mp-edit box from an RZW1 document.
func RenderPlayerEditV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_edit_v2(p, l, n)
	})
}

// RenderPlayerExportV2 renders the #mp-export panel from an RZW1 document.
func RenderPlayerExportV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_export_v2(p, l, n)
	})
}

// RenderPlayerROV2 renders the #mp-ro read-only strip from an RZW1 document.
func RenderPlayerROV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_ro_v2(p, l, n)
	})
}

// RenderPlayerHovV2 renders the #mp-hov hover readout from an RZW1 document.
func RenderPlayerHovV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_player_hov_v2(p, l, n)
	})
}

// RenderAutomationsV2 renders the full Automations tab from an RZW1 document.
func RenderAutomationsV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_automations_v2(p, l, n)
	})
}

// RenderAutomationsBodyV2 renders the #auto-body fragment from an RZW1 document.
func RenderAutomationsBodyV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_automations_body_v2(p, l, n)
	})
}

// RenderPeersV2 renders the full Peers tab from an RZW1 document.
func RenderPeersV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_peers_v2(p, l, n)
	})
}

// RenderPeersBodyV2 renders the #peers-body fragment from an RZW1 document.
func RenderPeersBodyV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_peers_body_v2(p, l, n)
	})
}

// ── B7 fan-out: overlays ──

// RenderOverlaysV2 renders the full Overlays view from an RZW1 document.
func RenderOverlaysV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_overlays_v2(p, l, n)
	})
}

// RenderOverlaysAppearanceV2 renders the #ovl-appearance fragment from an RZW1 document.
func RenderOverlaysAppearanceV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_overlays_appearance_v2(p, l, n)
	})
}

// RenderOverlaysSpoutV2 renders the #ovl-spout fragment from an RZW1 document.
func RenderOverlaysSpoutV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_overlays_spout_v2(p, l, n)
	})
}

// RenderOverlaysStatusV2 renders one #ovl-st-<kind> status fragment from an RZW1 document.
func RenderOverlaysStatusV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_overlays_status_v2(p, l, n)
	})
}

// RenderOverlaysStripV2 renders the #ovl-strip fragment from an RZW1 document.
func RenderOverlaysStripV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_overlays_strip_v2(p, l, n)
	})
}

// ── B7 fan-out: twitch ──

// RenderTwitchV2 renders the full Twitch tab from an RZW1 document.
func RenderTwitchV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_twitch_v2(p, l, n)
	})
}

// RenderTwitchObsV2 renders the #twitch-obs fragment from an RZW1 document.
func RenderTwitchObsV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_twitch_obs_v2(p, l, n)
	})
}

// RenderTwitchPresetsV2 renders the #twitch-presets fragment from an RZW1 document.
func RenderTwitchPresetsV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_twitch_presets_v2(p, l, n)
	})
}

// RenderTwitchFeedV2 renders the #twitch-feed fragment from an RZW1 document.
func RenderTwitchFeedV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_twitch_feed_v2(p, l, n)
	})
}

// ── B7 fan-out: midi mixer + pcv modals ──

// RenderMIDICtlV2 renders the full MIDI Mixer tab from an RZW1 document.
func RenderMIDICtlV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_midictl_v2(p, l, n)
	})
}

// RenderMIDIActiveV2 renders the #midi-active status line from an RZW1 document.
func RenderMIDIActiveV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_midictl_active_v2(p, l, n)
	})
}

// RenderMIDICtlStatV2 renders one #midi-ctlstat-<i> inner fragment from an RZW1 document.
func RenderMIDICtlStatV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_midictl_stat_v2(p, l, n)
	})
}

// RenderMIDIMonRowsV2 renders the #midi-monitor inner rows from an RZW1 document.
func RenderMIDIMonRowsV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_midimon_rows_v2(p, l, n)
	})
}

// RenderPCViewerV2 renders the point-cloud viewer modal from an RZW1 document.
func RenderPCViewerV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_pc_viewer_v2(p, l, n)
	})
}

// RenderPCGpuV2 renders the point-cloud GPU prompt modal from an RZW1 document.
func RenderPCGpuV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_pc_gpu_v2(p, l, n)
	})
}

// ── B7 fan-out: vrchat family ──

// RenderVRChatV2 renders the full VRChat tab from an RZW1 document.
func RenderVRChatV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vrchat_v2(p, l, n)
	})
}

// RenderVRChatStatusV2 renders the #vrc-status region from an RZW1 document.
func RenderVRChatStatusV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vrchat_status_v2(p, l, n)
	})
}

// RenderVRChatEditorV2 renders the #vrc-editor fragment from an RZW1 document.
func RenderVRChatEditorV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vrchat_editor_v2(p, l, n)
	})
}

// RenderVRChatCampathsV2 renders the #vrc-campaths fragment from an RZW1 document.
func RenderVRChatCampathsV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vrchat_campaths_v2(p, l, n)
	})
}

// RenderVRChatPhotosV2 renders the #vrc-photos-body fragment from an RZW1 document.
func RenderVRChatPhotosV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vrchat_photos_v2(p, l, n)
	})
}

// RenderVRCGroupsV2 renders the #vrcg-body Groups sub-tab from an RZW1 document.
func RenderVRCGroupsV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vrcgroups_v2(p, l, n)
	})
}

// RenderVgRoleBodyV2 renders the #vrcg-role-body fragment from an RZW1 document.
func RenderVgRoleBodyV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vg_rolebody_v2(p, l, n)
	})
}

// RenderVgInviteListV2 renders the #vrcg-inv-list fragment from an RZW1 document.
func RenderVgInviteListV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vg_invitelist_v2(p, l, n)
	})
}

// RenderVgRolesModalV2 renders the roles dialog from an RZW1 document.
func RenderVgRolesModalV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vg_rolesmodal_v2(p, l, n)
	})
}

// RenderVgInviteModalV2 renders the invite dialog from an RZW1 document.
func RenderVgInviteModalV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vg_invitemodal_v2(p, l, n)
	})
}

// RenderVgMemberConfirmV2 renders the kick/ban confirm dialog from an RZW1 document.
func RenderVgMemberConfirmV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vg_memberconfirm_v2(p, l, n)
	})
}

// RenderVgPostConfirmV2 renders the delete-post confirm dialog from an RZW1 document.
func RenderVgPostConfirmV2(state []byte) (string, bool) {
	return render(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_render_vg_postconfirm_v2(p, l, n)
	})
}

// --- end phaseb-wire ---
// --- phaseb-sched ---

// B3 fragment scheduler (one call per tick per surface). The tick's whole state crosses once as
// an RZW1 document carrying the hash of what Go last pushed per fragment id; the lib renders
// every fragment of the surface, suppresses the ones whose bytes are unchanged and returns a
// packed RZF1 list of the CHANGED ones. Unchanged fragment HTML never crosses the ABI.
// Design: .devnotes/ZIG_UI_GUIDE.md "Phase B — B3 fragment scheduler".

// Frag is one changed fragment: its patch id, the Wyhash-64 of its HTML (the caller stores this
// as the next tick's dedup key) and the HTML itself.
type Frag struct {
	ID   string
	Hash uint64
	HTML string
}

// fragMagic + fragHdrLen frame the packed reply (native/zigui/src/tick.zig).
const (
	fragMagic   = "RZF1"
	fragHdrLen  = 6
	fragMaxCall = 1 << 20 // per-fragment sanity cap (1 MiB); the biggest real fragment is ~50 kB
)

// TickLive renders the Live cockpit's ~1 Hz fragments from one RZW1 document (wireTkLive).
// ok=false → the caller runs its legacy per-fragment path for this tick.
func TickLive(state []byte) ([]Frag, bool) {
	return tickBatch(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_tick_live(p, l, n)
	})
}

// TickLogs renders the #log-view tail from one RZW1 document (wireTkLogs).
func TickLogs(state []byte) ([]Frag, bool) {
	return tickBatch(state, func(p *C.uint8_t, l C.size_t, n *C.size_t) *C.uint8_t {
		return C.rz_ui_tick_logs(p, l, n)
	})
}

// tickBatch calls a scheduler export, copies + frees the reply and decodes it. A reply that
// doesn't walk to exactly its end is refused whole - never applied in part.
func tickBatch(state []byte, f func(*C.uint8_t, C.size_t, *C.size_t) *C.uint8_t) ([]Frag, bool) {
	if len(state) == 0 {
		return nil, false
	}
	p := (*C.uint8_t)(unsafe.Pointer(&state[0]))
	var n C.size_t
	t0 := time.Now()
	out := f(p, C.size_t(len(state)), &n)
	if out == nil {
		noteFallback(2) // 2 frames up = the Tick* wrapper
		return nil, false
	}
	buf := C.GoBytes(unsafe.Pointer(out), C.int(n))
	C.rz_ui_free(out, n)
	NoteRender(len(state), time.Since(t0))
	frs, ok := decodeFrags(buf)
	if !ok {
		noteFallback(2)
		return nil, false
	}
	return frs, true
}

// decodeFrags walks a packed RZF1 reply. Every length is checked against the remaining bytes and
// the buffer must end exactly on the last entry (same discipline as the RZW1 decoder).
func decodeFrags(buf []byte) ([]Frag, bool) {
	if len(buf) < fragHdrLen || string(buf[:4]) != fragMagic {
		return nil, false
	}
	count := int(binary.LittleEndian.Uint16(buf[4:]))
	pos := fragHdrLen
	need := func(k int) bool { return k >= 0 && len(buf)-pos >= k }
	out := make([]Frag, 0, count)
	for i := 0; i < count; i++ {
		if !need(2) {
			return nil, false
		}
		idLen := int(binary.LittleEndian.Uint16(buf[pos:]))
		pos += 2
		if !need(idLen) {
			return nil, false
		}
		id := string(buf[pos : pos+idLen])
		pos += idLen
		if !need(12) {
			return nil, false
		}
		h := binary.LittleEndian.Uint64(buf[pos:])
		pos += 8
		htmlLen := int(binary.LittleEndian.Uint32(buf[pos:]))
		pos += 4
		if htmlLen > fragMaxCall || !need(htmlLen) {
			return nil, false
		}
		out = append(out, Frag{ID: id, Hash: h, HTML: string(buf[pos : pos+htmlLen])})
		pos += htmlLen
	}
	if pos != len(buf) {
		return nil, false // trailing garbage: refuse the whole batch
	}
	return out, true
}

// --- end phaseb-sched ---
