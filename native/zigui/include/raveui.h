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

void rz_ui_free(const uint8_t *ptr, size_t len);

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

#ifdef __cplusplus
}
#endif

#endif /* RAVEUI_H */
