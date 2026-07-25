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

/* --- settings --- */
const uint8_t *rz_ui_render_settings(const uint8_t *state_json, size_t len, size_t *out_len);
/* #set-content inner HTML (sub-tab switch + debounced search patch target). */
const uint8_t *rz_ui_render_settings_content(const uint8_t *state_json, size_t len, size_t *out_len);
/* #stset-<id> inner HTML (~1 Hz per-card status tick). */
const uint8_t *rz_ui_render_settings_status(const uint8_t *state_json, size_t len, size_t *out_len);

/* --- library --- */
/* Whole Library tab (Browse · Favorites · Collection · Playlists · History · ID Marks ·
 * Queue · Presets + the shared inspector). Sub-views owned by other renderers (nav rail,
 * cue-edit, gridfix/tagfix panels, compat section, player, loudness block, key wheel) ride
 * in the state as trusted pre-rendered markup. */
const uint8_t *rz_ui_render_library(const uint8_t *state_json, size_t len, size_t *out_len);
/* Live-patched Library fragments: #lib-body, #lib-detail, #lib-queue-body, #ce-cell-<hash>. */
const uint8_t *rz_ui_render_library_body(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_library_detail(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_library_queue(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_library_cuecell(const uint8_t *state_json, size_t len, size_t *out_len);

/* --- cueedit --- */
/* Cue-editor subview (library_cueedit.go): the #ce-topbar readout strip, the full-width
 * wave strip (topbar + the raw player markup - player.go keeps the 30 fps __rt playhead
 * surface and all of its float math), and the editor rail inside #lib-detail. The topbar
 * and rail render EMPTY when the editor is off => NULL, and the Go fallback agrees. */
const uint8_t *rz_ui_render_cueedit_topbar(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_cueedit_wave(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_cueedit_rail(const uint8_t *state_json, size_t len, size_t *out_len);


/* --- libviews --- (Library alternate bodies + the Library modals) */
/* #lib-body while a paired peer is targeted: status banner + peer-document iframe. */
const uint8_t *rz_ui_render_libmirror(const uint8_t *state_json, size_t len, size_t *out_len);
/* #rmirror-banner inner (patched on every session-status move). */
const uint8_t *rz_ui_render_libmirror_banner(const uint8_t *state_json, size_t len, size_t *out_len);
/* #lib-body while remote-cue-editing (waveform + #rce-info + the shared inspector). */
const uint8_t *rz_ui_render_rce_body(const uint8_t *state_json, size_t len, size_t *out_len);
/* #rce-info inner; empty once the session ends => NULL (Go fallback emits ""). */
const uint8_t *rz_ui_render_rce_info(const uint8_t *state_json, size_t len, size_t *out_len);
/* Save/write-back section of the cue-editor rail in rce mode; empty otherwise => NULL. */
const uint8_t *rz_ui_render_rce_save(const uint8_t *state_json, size_t len, size_t *out_len);
/* Library modals: full dialog markup (scrim + card), rendered into the modal root. */
const uint8_t *rz_ui_render_lib_smartmodal(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_lib_relocmodal(const uint8_t *state_json, size_t len, size_t *out_len);

/* --- libfixers --- */
/* The Library tab's fixer/section subviews (nav rail, beatgrid-fixer rail + results,
 * tag-fixer results + editor, prep picker, compat section). They also render inside the
 * library tab/body/detail exports; these are the direct entry points, plus #gf-live -
 * the one independently patched fragment (batch/calibration run tick). */
const uint8_t *rz_ui_render_libfix_navrail(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_libfix_prep(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_libfix_gfrail(const uint8_t *state_json, size_t len, size_t *out_len);
/* #gf-live inner HTML (~2 Hz run tick). */
const uint8_t *rz_ui_render_libfix_gflive(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_libfix_results(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_libfix_tagedit(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_libfix_compat(const uint8_t *state_json, size_t len, size_t *out_len);

/* --- settings-sub --- */
/* Settings card bodies owned by other webui files (rendered inside the settings tab through its
 * block list; exported for the standalone patch targets + the per-body golden tests). */
const uint8_t *rz_ui_render_settings_gridfix(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_settings_gridfixmodel(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_settings_bridge(const uint8_t *state_json, size_t len, size_t *out_len);
/* #inst-update inner (patchUpd). Hidden/unchecked states render empty => NULL, and the Go
 * fallback renders the same "". */
const uint8_t *rz_ui_render_settings_updflow(const uint8_t *state_json, size_t len, size_t *out_len);
/* --- end settings-sub --- */

/* --- player --- */
/* The unified media player/editor (internal/webui/player.go + render_player.go): the full
 * .mplayer component plus one export per patch target (#mp-<host>-root/-vid/-wave/-tp/
 * -edit/-export/-ro/-hov). The waveform SVG (float geometry + the rAF-driven `mp-<host>-ph`
 * ids) and the shared loudness block ride through the state as trusted RAW markup.
 * Legitimately-empty fragments (no video media, edit mode off, no active media) render
 * empty => NULL, and the Go fallback renders the same "". */
const uint8_t *rz_ui_render_player(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_root(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_vid(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_wave(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_tp(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_edit(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_export(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_ro(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_hov(const uint8_t *state_json, size_t len, size_t *out_len);
/* --- end player --- */

/* --- dialogs-a --- */
/* Wave-4 dialog sweep A: the publish/transcode dialog family. Each call returns a WHOLE
 * dialog (scrim + card + footer) for Go's openModal. rz_ui_render_dlg_choice is the shared
 * message+buttons shape (confirm / format picker / row context menu), 6 call sites. */
const uint8_t *rz_ui_render_dlg_choice(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_dlg_txtexport(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_dlg_exportprev(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_dlg_rename(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_dlg_fix(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_dlg_preset(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_dlg_patmgr(const uint8_t *state_json, size_t len, size_t *out_len);
/* --- end dialogs-a --- */

/* --- dialogs-b --- */
/* Wave-4 dialog sweep B: feature-tab dialog families. The fragment exports serve in-modal
 * patch targets (#vrcg-role-body, #vrcg-inv-list, #world-fr-list, #world-grp-list,
 * #world-role-list); the rest render a whole dialog including the modal chrome. */
const uint8_t *rz_ui_render_vg_rolebody(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_vg_invitelist(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_vg_rolesmodal(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_vg_invitemodal(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_vg_memberconfirm(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_vg_postconfirm(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_ws_listeditor(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_ws_postereditor(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_ws_friendpicker(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_ws_friendlist(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_ws_grouppicker(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_ws_grouplist(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_ws_rolepicker(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_ws_rolelist(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_ws_device(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_auto_editor(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_auto_runnow(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_auto_schedule(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_pc_viewer(const uint8_t *state_json, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_pc_gpu(const uint8_t *state_json, size_t len, size_t *out_len);
/* --- end dialogs-b --- */
/* --- phaseb-tip --- */
/* tooltip primitive (tipSt) - parity-gate export; migrated tabs compose it in-process. */
const uint8_t *rz_ui_render_tip(const uint8_t *state_json, size_t len, size_t *out_len);
/* --- end phaseb-tip --- */


/* --- phaseb-wire --- */
/* RZW1 binary state wire (phase B pilots). Same renderers as the JSON exports, fed by a
 * length-prefixed TLV document (magic "RZW1", u16 message id, u32 schema hash, u32 arena
 * length, strings arena, field-tagged body). Encoder: internal/zigui/wire.go; both sides
 * generated from one schema (internal/zigui/wiregen). A header/schema/bounds mismatch
 * returns NULL - the caller then tries the _v1 (JSON) export, then its Go renderer.
 * Free with rz_ui_free(ptr, *out_len), same as the JSON exports. */
const uint8_t *rz_ui_render_appgroups_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_appgroups_body_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_logs_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_logs_lines_v2(const uint8_t *state, size_t len, size_t *out_len);
/* B-2 fan-out. The _frag_v2 exports take the same kind selector as their JSON twins; each
 * fragment is its own root message, so a document built for another fragment is refused. */
const uint8_t *rz_ui_render_live_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_live_frag_v2(const uint8_t *kind, size_t kind_len, const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_motion_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_motion_body_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_publish_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_publish_hero_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_settings_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_settings_content_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_settings_status_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_library_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_library_body_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_library_detail_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_library_queue_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_library_cuecell_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_root_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_vid_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_wave_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_tp_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_edit_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_export_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_ro_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_player_hov_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_automations_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_automations_body_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_peers_v2(const uint8_t *state, size_t len, size_t *out_len);
const uint8_t *rz_ui_render_peers_body_v2(const uint8_t *state, size_t len, size_t *out_len);
/* --- end phaseb-wire --- */

#ifdef __cplusplus
}
#endif

#endif /* RAVEUI_H */
