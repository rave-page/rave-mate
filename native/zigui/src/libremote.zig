//! Remote-control target switcher — byte-exact port of internal/webui/render_library_remote.go
//! `libRemoteHTML`: the "Controlling [This computer ▾]" row shared by the Library + Publish
//! tabs. The peer list, current target and open/filter state are resolved Go-side into a
//! smart-select (`selState`); this file only frames it.
//!
//! show=false renders NOTHING - an empty buffer makes renderJSON return NULL, so the bridge
//! falls back to the Go renderer, which emits the same empty string.
//! Golden gate: internal/webui/zigui_golden_libremote_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

pub const State = struct {
    show: bool = false,
    sel: c.Select = .{},
};

pub fn render(h: *Html, s: State) !void {
    if (!s.show) return;
    try h.raw("<div class=lib-target>");
    try c.selectBox(h, s.sel);
    try h.raw("</div>");
}

test "hidden renders nothing" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .sel = .{ .id = "libtarget" } });
    try std.testing.expectEqualStrings("", h.b.items);
}

test "closed switcher" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .show = true, .sel = .{ .id = "libtarget", .label = "Controlling", .curLabel = "This computer" } });
    try std.testing.expectEqualStrings("<div class=lib-target><div class=ss-field>" ++
        "<span class=ss-label>Controlling</span><div class=ss id=\"ss-libtarget\">" ++
        "<button type=button class=\"ss-btn\" data-act=\"ss-tgl:libtarget\" data-label=\"libtarget\">" ++
        "<span class=ss-cur>This computer</span>" ++
        "<svg class=ss-chev viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"2\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\"><path d=\"m6 9 6 6 6-6\"/></svg></button>" ++
        "</div></div></div>", h.b.items);
}

test "open switcher lists peers" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const rows = [_]c.SelectRow{
        .{ .val = "", .label = "This computer" },
        .{ .val = "node-1", .label = "▸ Studio PC", .cur = true },
    };
    try render(&h, .{ .show = true, .sel = .{
        .id = "libtarget",
        .label = "Controlling",
        .curLabel = "▸ Studio PC",
        .open = true,
        .rows = &rows,
    } });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=\"ss-opt cur\" data-act=\"ss-pick:libtarget\" data-val=\"node-1\">") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<span class=ss-ol>▸ Studio PC</span>") != null);
}
