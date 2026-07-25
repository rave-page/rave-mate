//! MIDI tab ▸ "Control rave-mate" mappings card — byte-exact port of
//! internal/webui/render_midictl_uimap.go (umHTML/umRowHTML). Rows carry a heterogeneous
//! trailing list (smart select or button) tagged by `kind`.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

pub const Trail = struct {
    kind: []const u8 = "", // "sel" | "btn"
    sel: c.Select = .{},
    label: []const u8 = "",
    @"var": []const u8 = "",
    act: []const u8 = "",
};

pub const Row = struct {
    title: []const u8 = "",
    sub: []const u8 = "",
    trail: []const Trail = &.{},
};

pub const Profile = struct {
    row: Row = .{},
    hasBinds: bool = false,
    empty: []const u8 = "",
    binds: []const Row = &.{},
};

pub const State = struct {
    show: bool = false,
    title: []const u8 = "",
    titleTip: []const u8 = "", // pre-rendered tooltip HTML (raw)
    sub: []const u8 = "",
    enableLbl: []const u8 = "",
    enableDl: []const u8 = "",
    enableAct: []const u8 = "",
    enableOn: bool = false,
    enableTip: []const u8 = "",
    add: Row = .{},
    profiles: []const Profile = &.{},
    note: []const u8 = "",
};

/// render mirrors Go umHTML (mappings card).
pub fn render(h: *Html, s: State) !void {
    if (!s.show) return;
    try c.cardOpen(h, s.title, true);
    try h.raw(s.titleTip);
    try c.cardTrailClose(h);
    try h.raw("<p class=page-sub>");
    try h.esc(s.sub);
    try h.raw("</p>");
    try c.toggleRowTip(h, s.enableLbl, s.enableDl, s.enableAct, s.enableOn, s.enableTip);
    try renderRow(h, s.add);
    for (s.profiles) |p| {
        try renderRow(h, p.row);
        if (!p.hasBinds) {
            try h.raw("<div class=set-note>");
            try h.esc(p.empty);
            try h.raw("</div>");
            continue;
        }
        for (p.binds) |b| try renderRow(h, b);
    }
    try h.raw("<div class=set-note>");
    try h.esc(s.note);
    try h.raw("</div>");
    try c.cardClose(h);
}

/// renderRow mirrors Go umRowHTML.
fn renderRow(h: *Html, r: Row) !void {
    try c.itemRowOpen(h, r.title, r.sub);
    for (r.trail) |t| {
        if (std.mem.eql(u8, t.kind, "sel")) {
            try c.selectBox(h, t.sel);
            continue;
        }
        try c.btn(h, t.label, t.@"var", t.act, "");
    }
    try c.itemRowClose(h);
}

test "hidden card renders nothing" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{});
    try std.testing.expectEqualStrings("", h.b.items);
}

test "row mixes select and button trailing controls" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const trail = [_]Trail{
        .{ .kind = "sel", .sel = .{ .id = "um-mode-3", .curLabel = "Press" } },
        .{ .kind = "btn", .label = "✕", .@"var" = "ghost", .act = "um-del:3" },
    };
    try renderRow(&h, .{ .title = "↳ Audition", .sub = "CC 20 (ch1) · Press", .trail = &trail });
    try std.testing.expect(std.mem.startsWith(u8, h.b.items, "<div class=irow><div class=irow-main><div class=irow-title>↳ Audition</div>" ++
        "<div class=irow-sub>CC 20 (ch1) · Press</div></div><div class=irow-actions><div class=ss-field>"));
    try std.testing.expect(std.mem.endsWith(u8, h.b.items, "<button class=\"rp-btn rp-btn--ghost\" data-act=\"um-del:3\">✕</button></div></div>"));
}
