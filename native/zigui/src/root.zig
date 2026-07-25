//! raveui — rave-mate webui render layer (Zig). C ABI consumed by Go via cgo
//! (internal/zigui). Exports prefixed rz_ui_. ABI contract: include/raveui.h.
//! Go builds per-view state JSON (all data + RESOLVED i18n strings — catalogs stay
//! single-source in Go); renderers here emit HTML byte-identical to the Go originals.
//! Allocation via libc malloc-compatible c_allocator (mingw runtime provides it).

const std = @import("std");
const html = @import("html.zig");
const appgroups = @import("appgroups.zig");
const logs = @import("logs.zig");

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

test {
    _ = midimon;
}
