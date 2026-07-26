//! raveui — rave-mate webui render layer (Zig). C ABI consumed by Go via cgo
//! (internal/zigui). Exports prefixed rz_ui_. ABI contract: include/raveui.h.
//! Go builds per-view state JSON (all data + RESOLVED i18n strings — catalogs stay
//! single-source in Go); renderers here emit HTML byte-identical to the Go originals.
//! Allocation via libc malloc-compatible c_allocator (mingw runtime provides it).

const std = @import("std");
const html = @import("html.zig");
const appgroups = @import("appgroups.zig");
const logs = @import("logs.zig");
const vrchat = @import("vrchat.zig");
const vrcgroups = @import("vrcgroups.zig");
const worlds = @import("worlds.zig");

const alloc = std.heap.c_allocator;

/// ABI version — bump on any breaking export change; Go side asserts at init.
pub const abi_version: u32 = 1;

export fn rz_ui_abi_version() u32 {
    return abi_version;
}

/// Parse state JSON → run renderFn → return owned buffer (null on any failure;
/// the Go caller falls back to its own renderer).
fn renderJSON(comptime StateT: type, comptime renderFn: fn (*html.Html, StateT) anyerror!void, state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    const p = state_json orelse return null;
    if (len == 0) return null;
    const parsed = std.json.parseFromSlice(StateT, alloc, p[0..len], .{ .ignore_unknown_fields = true }) catch return null;
    defer parsed.deinit();
    var h = html.Html.init(alloc);
    defer h.deinit();
    renderFn(&h, parsed.value) catch return null;
    const out = h.toOwnedSlice() catch return null;
    if (out.len == 0) {
        alloc.free(out);
        return null;
    }
    out_len.* = out.len;
    return out.ptr;
}

export fn rz_ui_render_appgroups(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(appgroups.State, appgroups.render, state_json, len, out_len);
}

export fn rz_ui_render_appgroups_body(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(appgroups.State, appgroups.renderBody, state_json, len, out_len);
}

export fn rz_ui_render_logs(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(logs.State, logs.render, state_json, len, out_len);
}

export fn rz_ui_render_logs_lines(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(logs.Lines, logs.renderLines, state_json, len, out_len);
}

// --- motion + live (fleet: live batch) ---

const motion = @import("motion.zig");

export fn rz_ui_render_motion(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(motion.State, motion.render, state_json, len, out_len);
}

export fn rz_ui_render_motion_body(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(motion.State, motion.renderBody, state_json, len, out_len);
}

const live = @import("live.zig");

export fn rz_ui_render_live(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(live.State, live.render, state_json, len, out_len);
}

/// Render one live-tab fragment (the ~1 Hz tickPatch targets). kind selects the
/// fragment + its state type: transport|np|status|decks|signals|cockpit|link|graph|perf|
/// strip ("graph" serves both #live-net and #live-tim). Unknown kind → NULL (Go falls
/// back). One dispatch export beats ten near-identical ones on the C ABI surface.
export fn rz_ui_render_live_frag(kind: ?[*]const u8, kind_len: usize, state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    const kp = kind orelse return null;
    if (kind_len == 0) return null;
    const k = kp[0..kind_len];
    if (std.mem.eql(u8, k, "transport")) return renderJSON(live.Transport, live.renderTransport, state_json, len, out_len);
    if (std.mem.eql(u8, k, "np")) return renderJSON(live.NP, live.renderNP, state_json, len, out_len);
    if (std.mem.eql(u8, k, "status")) return renderJSON(live.Status, live.renderStatus, state_json, len, out_len);
    if (std.mem.eql(u8, k, "decks")) return renderJSON(live.Decks, live.renderDecks, state_json, len, out_len);
    if (std.mem.eql(u8, k, "signals")) return renderJSON(live.Signals, live.renderSignals, state_json, len, out_len);
    if (std.mem.eql(u8, k, "cockpit")) return renderJSON(live.Cockpit, live.renderCockpit, state_json, len, out_len);
    if (std.mem.eql(u8, k, "link")) return renderJSON(live.Link, live.renderLink, state_json, len, out_len);
    if (std.mem.eql(u8, k, "graph")) return renderJSON(live.Graph, live.renderGraph, state_json, len, out_len);
    if (std.mem.eql(u8, k, "perf")) return renderJSON(live.Perf, live.renderPerf, state_json, len, out_len);
    if (std.mem.eql(u8, k, "strip")) return renderJSON(live.Strip, live.renderStrip, state_json, len, out_len);
    return null;
}

test {
    _ = motion;
    _ = live;
}

// --- end motion + live ---

// --- vrchat ---

export fn rz_ui_render_vrchat(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(vrchat.State, vrchat.render, state_json, len, out_len);
}

export fn rz_ui_render_vrchat_status(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(vrchat.Status, vrchat.renderStatus, state_json, len, out_len);
}

export fn rz_ui_render_vrchat_editor(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(vrchat.Editor, vrchat.renderEditor, state_json, len, out_len);
}

export fn rz_ui_render_vrchat_campaths(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(vrchat.Campaths, vrchat.renderCampaths, state_json, len, out_len);
}

export fn rz_ui_render_vrchat_photos(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(vrchat.Photos, vrchat.renderPhotos, state_json, len, out_len);
}

export fn rz_ui_render_vrcgroups(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(vrcgroups.State, vrcgroups.render, state_json, len, out_len);
}

// --- worlds ---

export fn rz_ui_render_worlds(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(worlds.State, worlds.render, state_json, len, out_len);
}

export fn rz_ui_render_worlds_linkhint(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(worlds.Hint, worlds.renderHint, state_json, len, out_len);
}

export fn rz_ui_render_worlds_github(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(worlds.GitHub, worlds.renderGitHub, state_json, len, out_len);
}

export fn rz_ui_render_worlds_status(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(worlds.Status, worlds.renderStatus, state_json, len, out_len);
}

export fn rz_ui_render_worlds_unityrows(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(worlds.Unity, worlds.renderUnityRows, state_json, len, out_len);
}

/// Free a buffer returned by an rz_ui_render_* call (len = its *out_len).
export fn rz_ui_free(ptr: ?[*]const u8, len: usize) void {
    const p = ptr orelse return;
    if (len == 0) return;
    alloc.free(@constCast(p[0..len]));
}

test {
    _ = html;
    _ = appgroups;
    _ = logs;
    _ = vrchat;
    _ = vrcgroups;
    _ = worlds;
    _ = @import("components.zig");
}

test "renderJSON end-to-end via export" {
    const state =
        \\{"title":"T","subtitle":"S","available":true,"unavailable":"","empty":"none","admin":"","launch":"","groups":[]}
    ;
    var n: usize = 0;
    const out = rz_ui_render_appgroups(state.ptr, state.len, &n) orelse return error.RenderFailed;
    defer rz_ui_free(out, n);
    try std.testing.expectEqualStrings(
        "<h1 class=page-title>T</h1><p class=page-sub>S</p><div id=appgroups-body>" ++
            "<div class=\"rp-empty\"><div class=\"rp-empty__title\">none</div></div></div>",
        out[0..n],
    );
}

test "bad JSON returns null" {
    var n: usize = 0;
    try std.testing.expect(rz_ui_render_appgroups("{nope", 5, &n) == null);
}

// --- midi ---

const midimon = @import("midimon.zig");

export fn rz_ui_render_midimon(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(midimon.State, midimon.render, state_json, len, out_len);
}

export fn rz_ui_render_midimon_rows(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(midimon.Lines, midimon.renderRows, state_json, len, out_len);
}

export fn rz_ui_render_miditrace(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(midimon.Trace, midimon.renderTrace, state_json, len, out_len);
}

const midictl = @import("midictl.zig");

export fn rz_ui_render_midictl(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(midictl.State, midictl.render, state_json, len, out_len);
}

export fn rz_ui_render_midictl_active(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(midictl.Active, midictl.renderActive, state_json, len, out_len);
}

export fn rz_ui_render_midictl_stat(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(midictl_ctls.PortStat, midictl_ctls.renderPortStat, state_json, len, out_len);
}

const midictl_ctls = @import("midictl_ctls.zig");

test {
    _ = midimon;
    _ = midictl;
    _ = midictl_ctls;
    _ = @import("midictl_uimap.zig");
}

// --- media ---
// Tabs: automations, overlays, twitch, editor.

const automations = @import("automations.zig");

export fn rz_ui_render_automations(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(automations.State, automations.render, state_json, len, out_len);
}

export fn rz_ui_render_automations_body(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(automations.Body, automations.renderBody, state_json, len, out_len);
}

const overlays = @import("overlays.zig");

export fn rz_ui_render_overlays(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(overlays.State, overlays.render, state_json, len, out_len);
}

export fn rz_ui_render_overlays_appearance(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(overlays.Appearance, overlays.renderAppearance, state_json, len, out_len);
}

export fn rz_ui_render_overlays_spout(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(overlays.Spout, overlays.renderSpout, state_json, len, out_len);
}

export fn rz_ui_render_overlays_strip(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(overlays.Strip, overlays.renderStrip, state_json, len, out_len);
}

export fn rz_ui_render_overlays_status(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(@import("components.zig").Status, overlays.renderStatus, state_json, len, out_len);
}

const twitch = @import("twitch.zig");

export fn rz_ui_render_twitch(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(twitch.State, twitch.render, state_json, len, out_len);
}

export fn rz_ui_render_twitch_obs(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(twitch.Obs, twitch.renderObs, state_json, len, out_len);
}

export fn rz_ui_render_twitch_presets(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(twitch.Presets, twitch.renderPresets, state_json, len, out_len);
}

export fn rz_ui_render_twitch_feed(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(twitch.Feed, twitch.renderFeed, state_json, len, out_len);
}

const editor = @import("editor.zig");

export fn rz_ui_render_editor(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(editor.State, editor.render, state_json, len, out_len);
}

export fn rz_ui_render_editor_preview(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(editor.Preview, editor.renderPreview, state_json, len, out_len);
}

test "media tab modules" {
    _ = automations;
    _ = overlays;
    _ = twitch;
    _ = editor;
}

// --- peers ---

const peers = @import("peers.zig");

export fn rz_ui_render_peers(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(peers.State, peers.render, state_json, len, out_len);
}

export fn rz_ui_render_peers_body(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(peers.Body, peers.renderBody, state_json, len, out_len);
}

test "peers tab module" {
    _ = peers;
}

// --- library_remote ---

const libremote = @import("libremote.zig");

/// Renders the "Controlling [This computer ▾]" switcher. show=false ⇒ empty ⇒ NULL ⇒ the
/// Go fallback renders the same empty string (see zigui_golden_libremote_test.go).
export fn rz_ui_render_libremote(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(libremote.State, libremote.render, state_json, len, out_len);
}

test "library_remote module" {
    _ = libremote;
}

// --- publish ---

const publish = @import("publish.zig");

export fn rz_ui_render_publish(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(publish.State, publish.render, state_json, len, out_len);
}

/// #pub-hero inner (~1 Hz tick patch). Legitimately EMPTY when no recorder exists ⇒
/// NULL ⇒ the Go fallback renders the same empty string.
export fn rz_ui_render_publish_hero(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(publish.Hero, publish.renderHero, state_json, len, out_len);
}

export fn rz_ui_render_publish_remote(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(publish.Remote, publish.renderRemote, state_json, len, out_len);
}

test "publish tab module" {
    _ = publish;
}

// --- settings ---

const settings = @import("settings.zig");

export fn rz_ui_render_settings(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(settings.State, settings.render, state_json, len, out_len);
}

export fn rz_ui_render_settings_content(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(settings.Content, settings.renderContent, state_json, len, out_len);
}

export fn rz_ui_render_settings_status(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(settings.Status, settings.renderStatus, state_json, len, out_len);
}

test "settings tab module" {
    _ = settings;
}

// --- library ---

const library = @import("library.zig");

export fn rz_ui_render_library(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(library.State, library.render, state_json, len, out_len);
}

export fn rz_ui_render_library_body(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(library.Body, library.renderBody, state_json, len, out_len);
}

export fn rz_ui_render_library_detail(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(library.Detail, library.renderDetail, state_json, len, out_len);
}

export fn rz_ui_render_library_queue(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(library.Queue, library.renderQueue, state_json, len, out_len);
}

export fn rz_ui_render_library_cuecell(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(library.CueCell, library.renderCueCell, state_json, len, out_len);
}

test "library tab module" {
    _ = library;
}

// --- cueedit ---

const cueedit = @import("cueedit.zig");

/// `#ce-topbar` inner: the cue-editor readout strip (patched on every cursor move/edit).
/// Legitimately EMPTY when the editor is off ⇒ NULL ⇒ the Go fallback renders the same "".
export fn rz_ui_render_cueedit_topbar(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(cueedit.Topbar, cueedit.renderTopbar, state_json, len, out_len);
}

/// Full-width cue-edit wave strip (topbar wrapper + the raw player markup, which owns the
/// 30 fps `__rt` playhead surface and stays Go-rendered).
export fn rz_ui_render_cueedit_wave(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(cueedit.Wave, cueedit.renderWave, state_json, len, out_len);
}

/// Cue-editor rail (the `#lib-detail` inner in cue-edit mode). Empty when the editor is off.
export fn rz_ui_render_cueedit_rail(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(cueedit.Rail, cueedit.renderRail, state_json, len, out_len);
}

test "cueedit subview module" {
    _ = cueedit;
}

// --- libviews ---

const libviews = @import("libviews.zig");

/// #lib-body while a paired peer is targeted (mirror banner + the peer document iframe).
export fn rz_ui_render_libmirror(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(libviews.Mirror, libviews.renderMirror, state_json, len, out_len);
}

/// #rmirror-banner inner (patched on every session-status move).
export fn rz_ui_render_libmirror_banner(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(libviews.MirrorBanner, libviews.renderMirrorBanner, state_json, len, out_len);
}

/// #lib-body while remote-cue-editing.
export fn rz_ui_render_rce_body(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(libviews.RceBody, libviews.renderRceBody, state_json, len, out_len);
}

/// #rce-info inner. Legitimately EMPTY once the session ends ⇒ NULL ⇒ the Go fallback
/// renders the same empty string.
export fn rz_ui_render_rce_info(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(libviews.RceInfo, libviews.renderRceInfo, state_json, len, out_len);
}

/// The save/write-back section spliced into the cue-editor rail; empty outside rce mode.
export fn rz_ui_render_rce_save(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(libviews.RceSave, libviews.renderRceSave, state_json, len, out_len);
}

/// Library modals (rendered into the modal root, scrim + dialog included).
export fn rz_ui_render_lib_smartmodal(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(libviews.SmartModal, libviews.renderSmartModal, state_json, len, out_len);
}

export fn rz_ui_render_lib_relocmodal(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(libviews.RelocModal, libviews.renderRelocModal, state_json, len, out_len);
}

test "libviews module" {
    _ = libviews;
}

// --- libfixers ---
// The Library tab's fixer/section subviews. They render as part of the library tab/body/detail
// exports too; these standalone entry points serve the direct golden gate plus the ONE
// independently patched fragment, #gf-live (the batch/calibration run's ~2 Hz tick).

const libfixers = @import("libfixers.zig");
const libfixSelect = @import("components.zig").Select;

export fn rz_ui_render_libfix_navrail(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(libfixers.Nav, libfixers.renderNav, state_json, len, out_len);
}

export fn rz_ui_render_libfix_prep(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(libfixSelect, libfixers.renderPrep, state_json, len, out_len);
}

export fn rz_ui_render_libfix_gfrail(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(libfixers.GF, libfixers.renderGF, state_json, len, out_len);
}

/// #gf-live inner (tiles + progress + current track), patched from the run goroutine.
export fn rz_ui_render_libfix_gflive(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(libfixers.GFLive, libfixers.renderGFLive, state_json, len, out_len);
}

export fn rz_ui_render_libfix_results(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(libfixers.Results, libfixers.renderResults, state_json, len, out_len);
}

export fn rz_ui_render_libfix_tagedit(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(libfixers.TagEdit, libfixers.renderTagEdit, state_json, len, out_len);
}

export fn rz_ui_render_libfix_compat(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(libfixers.Compat, libfixers.renderCompat, state_json, len, out_len);
}

test "library fixer subviews module" {
    _ = libfixers;
}

// --- settings-sub ---
// The four settings card bodies owned by other webui files (settings_gridfix.go,
// settings_gridfix_model.go, bridge_actions.go, update_actions.go). They render inside the
// settings tab via its block list; these exports serve the standalone patch targets + the
// per-body golden tests.

const settings_sub = @import("settings_sub.zig");

export fn rz_ui_render_settings_gridfix(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(settings_sub.GfCard, settings_sub.renderGridfix, state_json, len, out_len);
}

export fn rz_ui_render_settings_gridfixmodel(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(settings_sub.GfModel, settings_sub.renderGridfixModel, state_json, len, out_len);
}

export fn rz_ui_render_settings_bridge(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(settings_sub.Bridge, settings_sub.renderBridge, state_json, len, out_len);
}

/// #inst-update inner (patchUpd). The hidden/unchecked states render EMPTY ⇒ NULL ⇒ the Go
/// fallback renders the same empty string.
export fn rz_ui_render_settings_updflow(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(settings_sub.UpdFlow, settings_sub.renderUpdFlow, state_json, len, out_len);
}

test "settings sub-view module" {
    _ = settings_sub;
}

// --- end settings-sub ---

// --- player ---
// The unified media player/editor (internal/webui/player.go + render_player.go), the most
// embedded surface in the app: the library inspector's Player body and BOTH publish
// captures panes render it. One export per patch target - the full component plus the
// root/vid/wave/tp/edit/export/ro/hov fragments (player_actions.go mpPatch*). The waveform
// SVG and the shared loudness block ride through the state as trusted RAW markup; the
// legitimately-empty fragments (no video media, edit mode off, no active media) render
// EMPTY ⇒ NULL ⇒ the Go fallback renders the same empty string.

const player = @import("player.zig");

export fn rz_ui_render_player(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(player.State, player.render, state_json, len, out_len);
}

export fn rz_ui_render_player_root(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(player.Inner, player.renderInner, state_json, len, out_len);
}

export fn rz_ui_render_player_vid(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(player.Vid, player.renderVid, state_json, len, out_len);
}

export fn rz_ui_render_player_wave(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(player.Wave, player.renderWave, state_json, len, out_len);
}

export fn rz_ui_render_player_tp(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(player.Tp, player.renderTp, state_json, len, out_len);
}

export fn rz_ui_render_player_edit(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(player.Edit, player.renderEdit, state_json, len, out_len);
}

export fn rz_ui_render_player_export(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(player.Export, player.renderExport, state_json, len, out_len);
}

export fn rz_ui_render_player_ro(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(player.RO, player.renderRO, state_json, len, out_len);
}

export fn rz_ui_render_player_hov(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(player.Hov, player.renderHov, state_json, len, out_len);
}

test "player module" {
    _ = player;
}

// --- end player ---

// --- dialogs-a ---
// Wave-4 dialog sweep A: the publish/transcode dialog family. Each export returns a WHOLE
// dialog (scrim + card + footer) - Go's openModal writes the same bytes either way. The
// shared confirm/picker/context-menu shape has ONE export (rz_ui_render_dlg_choice) serving
// six call sites. No dialog here has a live sub-patch, so there is no _frag export.

const dialogs_a = @import("dialogs_a.zig");
const componentsA = @import("components.zig");

export fn rz_ui_render_dlg_choice(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(componentsA.Choice, componentsA.choiceDialog, state_json, len, out_len);
}

export fn rz_ui_render_dlg_txtexport(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_a.TxtExport, dialogs_a.renderTxtExport, state_json, len, out_len);
}

export fn rz_ui_render_dlg_exportprev(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_a.ExportPrev, dialogs_a.renderExportPrev, state_json, len, out_len);
}

export fn rz_ui_render_dlg_rename(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_a.Rename, dialogs_a.renderRename, state_json, len, out_len);
}

export fn rz_ui_render_dlg_fix(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_a.Fix, dialogs_a.renderFix, state_json, len, out_len);
}

export fn rz_ui_render_dlg_preset(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_a.Preset, dialogs_a.renderPreset, state_json, len, out_len);
}

export fn rz_ui_render_dlg_patmgr(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_a.PatMgr, dialogs_a.renderPatMgr, state_json, len, out_len);
}

test "dialogs-a module" {
    _ = dialogs_a;
}

// --- end dialogs-a ---

// --- dialogs-b ---
// Wave-4 dialog sweep B: the feature-tab dialog families (VRChat ▸ Groups, Worlds,
// Automations). Fragment exports (#vrcg-role-body, #vrcg-inv-list, #world-fr-list,
// #world-grp-list, #world-role-list) serve the in-modal patch targets; the rest are whole
// dialogs including the modal chrome, so openModal sees the same bytes either way.

const dialogs_b = @import("dialogs_b.zig");

export fn rz_ui_render_vg_rolebody(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.RoleBody, dialogs_b.renderRoleBody, state_json, len, out_len);
}

export fn rz_ui_render_vg_invitelist(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.InviteList, dialogs_b.renderInviteList, state_json, len, out_len);
}

export fn rz_ui_render_vg_rolesmodal(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.RolesModal, dialogs_b.renderRolesModal, state_json, len, out_len);
}

export fn rz_ui_render_vg_invitemodal(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.InviteModal, dialogs_b.renderInviteModal, state_json, len, out_len);
}

export fn rz_ui_render_vg_memberconfirm(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.MemberConfirm, dialogs_b.renderMemberConfirm, state_json, len, out_len);
}

export fn rz_ui_render_vg_postconfirm(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.PostConfirm, dialogs_b.renderPostConfirm, state_json, len, out_len);
}

export fn rz_ui_render_ws_listeditor(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.WsListEditor, dialogs_b.renderWsListEditor, state_json, len, out_len);
}

export fn rz_ui_render_ws_postereditor(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.WsPosterEditor, dialogs_b.renderWsPosterEditor, state_json, len, out_len);
}

export fn rz_ui_render_ws_friendpicker(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.WsFriendPicker, dialogs_b.renderWsFriendPicker, state_json, len, out_len);
}

/// #world-fr-list inner (patched when the async friends load lands / the filter changes).
export fn rz_ui_render_ws_friendlist(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.WsFriendList, dialogs_b.renderWsFriendList, state_json, len, out_len);
}

export fn rz_ui_render_ws_grouppicker(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.WsGroupPicker, dialogs_b.renderWsGroupPicker, state_json, len, out_len);
}

/// #world-grp-list inner (own-groups load + every group search).
export fn rz_ui_render_ws_grouplist(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.WsGroupList, dialogs_b.renderWsGroupList, state_json, len, out_len);
}

export fn rz_ui_render_ws_rolepicker(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.WsRolePicker, dialogs_b.renderWsRolePicker, state_json, len, out_len);
}

/// #world-role-list inner (patched once the group roles load).
export fn rz_ui_render_ws_rolelist(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.WsRoleList, dialogs_b.renderWsRoleList, state_json, len, out_len);
}

export fn rz_ui_render_ws_device(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.WsDevice, dialogs_b.renderWsDevice, state_json, len, out_len);
}

export fn rz_ui_render_auto_editor(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.AeModal, dialogs_b.renderAeModal, state_json, len, out_len);
}

export fn rz_ui_render_auto_runnow(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.ArModal, dialogs_b.renderArModal, state_json, len, out_len);
}

export fn rz_ui_render_auto_schedule(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.AsModal, dialogs_b.renderAsModal, state_json, len, out_len);
}

export fn rz_ui_render_pc_viewer(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.PCViewer, dialogs_b.renderPCViewer, state_json, len, out_len);
}

export fn rz_ui_render_pc_gpu(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(dialogs_b.PCGpu, dialogs_b.renderPCGpu, state_json, len, out_len);
}

test "dialogs-b module" {
    _ = dialogs_b;
}

// --- end dialogs-b ---

// --- phaseb-tip ---
// The tooltip primitive as its OWN export: the migrated tabs compose it in-process (settings.zig
// / player.zig / dialogs_b.zig call components.renderTip directly), so this export exists purely
// for the byte-parity gate over the whole helpTopics registry x locales
// (internal/webui/zigui_golden_tip_test.go).

const components = @import("components.zig");

export fn rz_ui_render_tip(state_json: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderJSON(components.Tip, components.renderTip, state_json, len, out_len);
}

test "phaseb-tip module" {
    _ = components;
}

// --- end phaseb-tip ---
// --- phaseb-wire ---

// RZW1 binary state wire (phase B pilots: appgroups + logs). Same renderers as the JSON
// exports above, fed by a length-prefixed TLV document instead of a per-render
// state→JSON→parse round trip. Decoder: wire.zig; per-message decoders: wire_gen.zig -
// BOTH sides generated from ONE schema (internal/zigui/wiregen), so the Go encoder and the
// Zig decoder cannot drift into silent memory corruption. The _v1 (JSON) exports stay:
// Go prefers v2, falls back to v1, then to its own renderer.

const wire = @import("wire.zig");
const wire_gen = @import("wire_gen.zig");

/// Parse an RZW1 document → run renderFn → owned buffer (null on any malformed input; the
/// Go caller then tries the JSON export and finally its own renderer).
fn renderWire(
    comptime StateT: type,
    comptime decodeFn: fn (*wire.Reader, *StateT) wire.Error!void,
    comptime renderFn: fn (*html.Html, StateT) anyerror!void,
    comptime msg_id: u16,
    state: ?[*]const u8,
    len: usize,
    out_len: *usize,
) ?[*]const u8 {
    const p = state orelse return null;
    if (len == 0) return null;
    const parsed = wire.parse(StateT, decodeFn, alloc, msg_id, wire_gen.schema_hash, p[0..len]) catch return null;
    defer parsed.deinit();
    var h = html.Html.init(alloc);
    defer h.deinit();
    renderFn(&h, parsed.value) catch return null;
    const out = h.toOwnedSlice() catch return null;
    if (out.len == 0) {
        alloc.free(out);
        return null;
    }
    out_len.* = out.len;
    return out.ptr;
}

export fn rz_ui_render_appgroups_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(appgroups.State, wire_gen.decodeAgState, appgroups.render, wire_gen.msg_ag_state, state, len, out_len);
}

export fn rz_ui_render_appgroups_body_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(appgroups.State, wire_gen.decodeAgState, appgroups.renderBody, wire_gen.msg_ag_state, state, len, out_len);
}

export fn rz_ui_render_logs_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(logs.State, wire_gen.decodeLogsState, logs.render, wire_gen.msg_logs_state, state, len, out_len);
}

export fn rz_ui_render_logs_lines_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(logs.Lines, wire_gen.decodeLogsLines, logs.renderLines, wire_gen.msg_logs_lines, state, len, out_len);
}

// ── B-2 fan-out: live ──
// The full cockpit is rendered rarely; the ten fragments are the ~1 Hz tick and are where the
// round trip actually costs. Each fragment state crosses on its own, so each is its own root
// message - the header id keeps refusing a document built for a different fragment.

export fn rz_ui_render_live_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(live.State, wire_gen.decodeLiveState, live.render, wire_gen.msg_live_state, state, len, out_len);
}

/// kind selects the fragment + its state type, same set as rz_ui_render_live_frag ("graph"
/// serves both #live-net and #live-tim). Unknown kind → NULL → the caller tries v1, then Go.
export fn rz_ui_render_live_frag_v2(kind: ?[*]const u8, kind_len: usize, state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    const kp = kind orelse return null;
    if (kind_len == 0) return null;
    const k = kp[0..kind_len];
    if (std.mem.eql(u8, k, "transport")) return renderWire(live.Transport, wire_gen.decodeLiveTransport, live.renderTransport, wire_gen.msg_live_transport, state, len, out_len);
    if (std.mem.eql(u8, k, "np")) return renderWire(live.NP, wire_gen.decodeLiveNP, live.renderNP, wire_gen.msg_live_n_p, state, len, out_len);
    if (std.mem.eql(u8, k, "status")) return renderWire(live.Status, wire_gen.decodeLiveStatus, live.renderStatus, wire_gen.msg_live_status, state, len, out_len);
    if (std.mem.eql(u8, k, "decks")) return renderWire(live.Decks, wire_gen.decodeLiveDecks, live.renderDecks, wire_gen.msg_live_decks, state, len, out_len);
    if (std.mem.eql(u8, k, "signals")) return renderWire(live.Signals, wire_gen.decodeLiveSignals, live.renderSignals, wire_gen.msg_live_signals, state, len, out_len);
    if (std.mem.eql(u8, k, "cockpit")) return renderWire(live.Cockpit, wire_gen.decodeLiveCockpit, live.renderCockpit, wire_gen.msg_live_cockpit, state, len, out_len);
    if (std.mem.eql(u8, k, "link")) return renderWire(live.Link, wire_gen.decodeLiveLink, live.renderLink, wire_gen.msg_live_link, state, len, out_len);
    if (std.mem.eql(u8, k, "graph")) return renderWire(live.Graph, wire_gen.decodeLiveGraph, live.renderGraph, wire_gen.msg_live_graph, state, len, out_len);
    if (std.mem.eql(u8, k, "perf")) return renderWire(live.Perf, wire_gen.decodeLivePerf, live.renderPerf, wire_gen.msg_live_perf, state, len, out_len);
    if (std.mem.eql(u8, k, "strip")) return renderWire(live.Strip, wire_gen.decodeLiveStrip, live.renderStrip, wire_gen.msg_live_strip, state, len, out_len);
    return null;
}

// ── B-2 fan-out: motion ──
// One message for both surfaces (the tab and the #mo-body section switch), like appgroups.

export fn rz_ui_render_motion_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(motion.State, wire_gen.decodeMoState, motion.render, wire_gen.msg_mo_state, state, len, out_len);
}

export fn rz_ui_render_motion_body_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(motion.State, wire_gen.decodeMoState, motion.renderBody, wire_gen.msg_mo_state, state, len, out_len);
}

// ── B-2 fan-out: publish ──
// #pub-hero is the ~1 Hz tick target (recorder progress); it renders EMPTY when no recorder is
// wired, and an empty render is a legitimate NULL that the Go fallback reproduces.

export fn rz_ui_render_publish_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(publish.State, wire_gen.decodePub, publish.render, wire_gen.msg_pub, state, len, out_len);
}

export fn rz_ui_render_publish_hero_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(publish.Hero, wire_gen.decodePubHero, publish.renderHero, wire_gen.msg_pub_hero, state, len, out_len);
}

// ── B-2 fan-out: settings ──
// Three messages: the tab, the #set-content pane (patched on its own so the search input keeps
// focus) and one #stset-<id> status line, which the settings tick patches per card.

export fn rz_ui_render_settings_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(settings.State, wire_gen.decodeSetState, settings.render, wire_gen.msg_set_state, state, len, out_len);
}

export fn rz_ui_render_settings_content_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(settings.Content, wire_gen.decodeSetContent, settings.renderContent, wire_gen.msg_set_content, state, len, out_len);
}

export fn rz_ui_render_settings_status_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(settings.Status, wire_gen.decodeSetStatus, settings.renderStatus, wire_gen.msg_set_status, state, len, out_len);
}

// ── B-2 fan-out: library ──
// Five messages: the tab, #lib-body (section switch), #lib-detail (inspector),
// #lib-queue-body (job progress, patched from the job goroutines) and one cue-census cell
// (#ce-cell-<hash>, patched per row when a drop is toggled).

export fn rz_ui_render_library_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(library.State, wire_gen.decodeLibState, library.render, wire_gen.msg_lib_state, state, len, out_len);
}

export fn rz_ui_render_library_body_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(library.Body, wire_gen.decodeLibBody, library.renderBody, wire_gen.msg_lib_body, state, len, out_len);
}

export fn rz_ui_render_library_detail_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(library.Detail, wire_gen.decodeLibDetail, library.renderDetail, wire_gen.msg_lib_detail, state, len, out_len);
}

export fn rz_ui_render_library_queue_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(library.Queue, wire_gen.decodeLibQueue, library.renderQueue, wire_gen.msg_lib_queue, state, len, out_len);
}

export fn rz_ui_render_library_cuecell_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(library.CueCell, wire_gen.decodeLibCueCell, library.renderCueCell, wire_gen.msg_lib_cue_cell, state, len, out_len);
}

// ── B-2 fan-out: player ──
// Nine patch targets, one message each. The full view carries the 29 kB raw waveform SVG
// (mpWaveSVG stays Go by design), so its document is dominated by ONE string - see
// PHASEB_BASELINE.md for what that does to the delta.

export fn rz_ui_render_player_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(player.State, wire_gen.decodeMpFull, player.render, wire_gen.msg_mp_full, state, len, out_len);
}

export fn rz_ui_render_player_root_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(player.Inner, wire_gen.decodeMpInner, player.renderInner, wire_gen.msg_mp_inner, state, len, out_len);
}

export fn rz_ui_render_player_vid_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(player.Vid, wire_gen.decodeMpVid, player.renderVid, wire_gen.msg_mp_vid, state, len, out_len);
}

export fn rz_ui_render_player_wave_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(player.Wave, wire_gen.decodeMpWave, player.renderWave, wire_gen.msg_mp_wave, state, len, out_len);
}

export fn rz_ui_render_player_tp_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(player.Tp, wire_gen.decodeMpTp, player.renderTp, wire_gen.msg_mp_tp, state, len, out_len);
}

export fn rz_ui_render_player_edit_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(player.Edit, wire_gen.decodeMpEdit, player.renderEdit, wire_gen.msg_mp_edit, state, len, out_len);
}

export fn rz_ui_render_player_export_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(player.Export, wire_gen.decodeMpExport, player.renderExport, wire_gen.msg_mp_export, state, len, out_len);
}

export fn rz_ui_render_player_ro_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(player.RO, wire_gen.decodeMpRO, player.renderRO, wire_gen.msg_mp_r_o, state, len, out_len);
}

export fn rz_ui_render_player_hov_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(player.Hov, wire_gen.decodeMpHov, player.renderHov, wire_gen.msg_mp_hov, state, len, out_len);
}

// ── B-2 fan-out: automations ──
// #auto-body is version-gated (~1 Hz, only re-rendered when the automation store changes).

export fn rz_ui_render_automations_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(automations.State, wire_gen.decodeAutoState, automations.render, wire_gen.msg_auto_state, state, len, out_len);
}

export fn rz_ui_render_automations_body_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(automations.Body, wire_gen.decodeAutoBodyState, automations.renderBody, wire_gen.msg_auto_body_state, state, len, out_len);
}

// ── B-2 fan-out: peers ──
// #peers-body is the ~1 Hz live tick (route telemetry, transfer progress). Peers carries the
// only []string on the wire (media sync lines → kStrList).

export fn rz_ui_render_peers_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(peers.State, wire_gen.decodePeers, peers.render, wire_gen.msg_peers, state, len, out_len);
}

export fn rz_ui_render_peers_body_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(peers.Body, wire_gen.decodePeersBody, peers.renderBody, wire_gen.msg_peers_body, state, len, out_len);
}

// ── B7 fan-out: overlays (root ids 45-49; the B7 partition extends B-2's 10-44) ──
// Full tab + the four live-patched fragments. The status fragment's root message is the shared
// c.Status (UiStatus, id 48) - nested everywhere else, root only here.

export fn rz_ui_render_overlays_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(overlays.State, wire_gen.decodeOvlState, overlays.render, wire_gen.msg_ovl_state, state, len, out_len);
}

export fn rz_ui_render_overlays_appearance_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(overlays.Appearance, wire_gen.decodeOvlAppr, overlays.renderAppearance, wire_gen.msg_ovl_appr, state, len, out_len);
}

export fn rz_ui_render_overlays_spout_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(overlays.Spout, wire_gen.decodeOvlSpout, overlays.renderSpout, wire_gen.msg_ovl_spout, state, len, out_len);
}

export fn rz_ui_render_overlays_status_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(@import("components.zig").Status, wire_gen.decodeUiStatus, overlays.renderStatus, wire_gen.msg_ui_status, state, len, out_len);
}

export fn rz_ui_render_overlays_strip_v2(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return renderWire(overlays.Strip, wire_gen.decodeOvlStrip, overlays.renderStrip, wire_gen.msg_ovl_strip, state, len, out_len);
}

test "wire modules" {
    _ = wire;
    _ = wire_gen;
}

// --- end phaseb-wire ---
// --- phaseb-sched ---

// B3 fragment scheduler: ONE call per tick per surface. The tick's whole state crosses once as
// an RZW1 document; tick.zig renders EVERY fragment of that surface, hashes each one and drops
// the ones whose bytes match what Go last pushed (the hashes travel in the document - the
// exports stay stateless). What comes back is a packed RZF1 changed-fragment list that Go turns
// into ONE batched Eval. Design + rationale: .devnotes/ZIG_UI_GUIDE.md "Phase B — B3 fragment
// scheduler". Free with rz_ui_free(ptr, *out_len), like every other export.
//
// Own aliases for wire.zig / wire_gen.zig so this block does not depend on the phaseb-wire
// block's names (a duplicate @import of one file is the same type, not a second copy).

const sched_wire = @import("wire.zig");
const sched_wire_gen = @import("wire_gen.zig");
const tick = @import("tick.zig");

/// Parse an RZW1 tick document → run the surface's scheduler → owned RZF1 buffer. NULL on any
/// malformed input or OOM; the Go caller then runs its legacy per-fragment path for that tick.
fn tickWire(
    comptime StateT: type,
    comptime decodeFn: fn (*sched_wire.Reader, *StateT) sched_wire.Error!void,
    comptime runFn: fn (std.mem.Allocator, StateT) anyerror![]u8,
    comptime msg_id: u16,
    state: ?[*]const u8,
    len: usize,
    out_len: *usize,
) ?[*]const u8 {
    const p = state orelse return null;
    if (len == 0) return null;
    const parsed = sched_wire.parse(StateT, decodeFn, alloc, msg_id, sched_wire_gen.schema_hash, p[0..len]) catch return null;
    defer parsed.deinit();
    const out = runFn(alloc, parsed.value) catch return null;
    out_len.* = out.len;
    return out.ptr;
}

export fn rz_ui_tick_live(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return tickWire(tick.LiveBatch, sched_wire_gen.decodeTkLive, tick.runLive, sched_wire_gen.msg_tk_live, state, len, out_len);
}

export fn rz_ui_tick_logs(state: ?[*]const u8, len: usize, out_len: *usize) ?[*]const u8 {
    return tickWire(tick.LogsBatch, sched_wire_gen.decodeTkLogs, tick.runLogs, sched_wire_gen.msg_tk_logs, state, len, out_len);
}

test "phaseb-sched module" {
    _ = tick;
}

// --- end phaseb-sched ---
