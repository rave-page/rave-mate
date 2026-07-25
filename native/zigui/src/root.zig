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
