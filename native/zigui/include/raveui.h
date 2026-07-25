/* raveui — rave-mate webui render layer (Zig), C ABI. Mirror of src/root.zig exports.
 * ABI v1. Go binding: internal/zigui. */
#ifndef RAVEUI_H
#define RAVEUI_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

uint32_t rz_ui_abi_version(void);

/* Render a view's HTML from resolved-state JSON (all data + localized strings resolved
 * by Go). Returns a Zig-allocated UTF-8 buffer (NOT NUL-terminated), length in *out_len;
 * NULL on parse/alloc failure — caller falls back to the Go renderer.
 * Free with rz_ui_free(ptr, *out_len). */
const uint8_t *rz_ui_render_appgroups(const uint8_t *state_json, size_t len, size_t *out_len);
/* Body-only fragment (#appgroups-body inner HTML, the ~1 Hz tick patch target). */
const uint8_t *rz_ui_render_appgroups_body(const uint8_t *state_json, size_t len, size_t *out_len);

const uint8_t *rz_ui_render_logs(const uint8_t *state_json, size_t len, size_t *out_len);
/* #log-view inner HTML (filter-change + ~1 Hz tick patch target). */
const uint8_t *rz_ui_render_logs_lines(const uint8_t *state_json, size_t len, size_t *out_len);

/* --- motion + live (fleet: live batch) --- */

const uint8_t *rz_ui_render_motion(const uint8_t *state_json, size_t len, size_t *out_len);
/* #mo-body inner HTML (section switch + avatar-scan patch target). */
const uint8_t *rz_ui_render_motion_body(const uint8_t *state_json, size_t len, size_t *out_len);

const uint8_t *rz_ui_render_live(const uint8_t *state_json, size_t len, size_t *out_len);
/* One live-tab fragment (the ~1 Hz patch targets). kind ∈ transport|np|status|decks|
 * signals|cockpit|link|graph|perf|strip ("graph" = #live-net and #live-tim); unknown
 * kind → NULL. kind is NOT NUL-terminated: pass kind_len. */
const uint8_t *rz_ui_render_live_frag(const uint8_t *kind, size_t kind_len,
                                      const uint8_t *state_json, size_t len, size_t *out_len);

/* --- end motion + live --- */

/* --- vrchat --- */
const uint8_t *rz_ui_render_vrchat(const uint8_t *state_json, size_t len, size_t *out_len);
/* Tick/action-patched fragments: #vrc-status-region, #vrc-editor, #vrc-campaths,
 * #vrc-photos-body and the Groups sub-view root #vrcg-body. */
const uint8_t *rz_ui_render_vrchat_status(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_vrchat_editor(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_vrchat_campaths(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_vrchat_photos(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_vrcgroups(const uint8_t *state_json, size_t len, size_t *out_len);

/* --- worlds --- */
const uint8_t *rz_ui_render_worlds(const uint8_t *state_json, size_t len, size_t *out_len);
/* Tick-patched fragments: #world-linkhint, #world-gh, #world-st-<key>, #world-unity-rows. */
const uint8_t *rz_ui_render_worlds_linkhint(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_worlds_github(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_worlds_status(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_worlds_unityrows(const uint8_t *state_json, size_t len, size_t *out_len);

void rz_ui_free(const uint8_t *ptr, size_t len);

/* --- midi --- */

/* MIDI monitor card + its #midi-monitor inner rows (~1 Hz patch target). */
const uint8_t *rz_ui_render_midimon(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_midimon_rows(const uint8_t *state_json, size_t len, size_t *out_len);
/* ravemidi driver wire-trace block (rendered inside the MIDI driver card). */
const uint8_t *rz_ui_render_miditrace(const uint8_t *state_json, size_t len, size_t *out_len);

/* Whole MIDI tab (controllers + mappings + monitor + output/driver + rack + bridge + help). */
const uint8_t *rz_ui_render_midictl(const uint8_t *state_json, size_t len, size_t *out_len);
/* #midi-active status line and #midi-ctlstat-<i> inner (~1 Hz tick patch targets).
 * The status fragment may legitimately render empty ⇒ NULL, and the Go fallback
 * renders the same empty string. */
const uint8_t *rz_ui_render_midictl_active(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_midictl_stat(const uint8_t *state_json, size_t len, size_t *out_len);
/* --- media --- (automations, overlays, twitch, editor) */
const uint8_t *rz_ui_render_automations(const uint8_t *state_json, size_t len, size_t *out_len);
/* #auto-body inner HTML (version-gated ~1 Hz tick patch target). */
const uint8_t *rz_ui_render_automations_body(const uint8_t *state_json, size_t len, size_t *out_len);

const uint8_t *rz_ui_render_overlays(const uint8_t *state_json, size_t len, size_t *out_len);
/* Live-patched overlays fragments: #ovl-appearance, #ovl-spout, #ovl-strip, #ovl-st-<kind>. */
const uint8_t *rz_ui_render_overlays_appearance(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_overlays_spout(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_overlays_strip(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_overlays_status(const uint8_t *state_json, size_t len, size_t *out_len);

const uint8_t *rz_ui_render_twitch(const uint8_t *state_json, size_t len, size_t *out_len);
/* Live-patched twitch fragments: #twitch-obs, #twitch-presets, #twitch-feed. */
const uint8_t *rz_ui_render_twitch_obs(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_twitch_presets(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_twitch_feed(const uint8_t *state_json, size_t len, size_t *out_len);

const uint8_t *rz_ui_render_editor(const uint8_t *state_json, size_t len, size_t *out_len);
/* #ed-preview inner HTML (~1 Hz placeholder-refresh patch target). */
const uint8_t *rz_ui_render_editor_preview(const uint8_t *state_json, size_t len, size_t *out_len);

/* --- peers --- */
const uint8_t *rz_ui_render_peers(const uint8_t *state_json, size_t len, size_t *out_len);
/* #peers-body inner HTML (~1 Hz live tick patch target). */
const uint8_t *rz_ui_render_peers_body(const uint8_t *state_json, size_t len, size_t *out_len);

/* --- library_remote --- */
/* "Controlling [This computer]" switcher; NULL when hidden (Go fallback emits ""). */
const uint8_t *rz_ui_render_libremote(const uint8_t *state_json, size_t len, size_t *out_len);

/* --- publish --- (local cockpit + the remote peer's recorded-sets browser) */
const uint8_t *rz_ui_render_publish(const uint8_t *state_json, size_t len, size_t *out_len);
/* #pub-hero inner (~1 Hz tick patch). May legitimately render empty (no recorder)
 * => NULL, and the Go fallback renders the same "". */
const uint8_t *rz_ui_render_publish_hero(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_publish_remote(const uint8_t *state_json, size_t len, size_t *out_len);

#ifdef __cplusplus
}
#endif

#endif /* RAVEUI_H */
