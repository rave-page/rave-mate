//! App Groups view renderer — byte-exact port of internal/webui/render_appgroups.go
//! (appGroupsHTML/appGroupsBodyHTML). State arrives fully resolved from Go (data +
//! localized strings); this file only walks state → markup. Golden gate:
//! internal/webui/zigui_golden_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

pub const App = struct {
    base: []const u8 = "",
    elevated: bool = false,
};

pub const Group = struct {
    id: []const u8 = "",
    name: []const u8 = "",
    up: []const u8 = "", // resolved "{running}/{total} up"
    variant: []const u8 = "", // success|warning|muted (decided by Go)
    apps: []const App = &.{},
};

pub const State = struct {
    title: []const u8 = "",
    subtitle: []const u8 = "",
    available: bool = false,
    unavailable: []const u8 = "",
    empty: []const u8 = "",
    admin: []const u8 = "", // elevated-app badge text
    launch: []const u8 = "", // launch button label
    groups: []const Group = &.{},
};

/// render mirrors Go appGroupsHTML (full tab view).
pub fn render(h: *Html, s: State) !void {
    if (!s.available) {
        try c.panel(h, s.title, "");
        try c.emptyState(h, s.unavailable);
        return;
    }
    try c.panel(h, s.title, s.subtitle);
    try h.raw("<div id=appgroups-body>");
    try renderBody(h, s);
    try h.raw("</div>");
}

/// renderBody mirrors Go appGroupsBodyHTML (#appgroups-body inner, the ~1 Hz tick patch).
pub fn renderBody(h: *Html, s: State) !void {
    if (s.groups.len == 0) {
        try c.emptyState(h, s.empty);
        return;
    }
    try h.raw("<div class=grid>");
    for (s.groups) |g| {
        try h.raw("<div class=\"rp-card\"><div class=card-label>");
        try h.esc(g.name);
        try h.raw("</div><div class=np-meta>");
        try c.badgeDot(h, g.up, g.variant);
        try h.raw("</div>");
        for (g.apps) |app| {
            try h.raw("<div class=kv><span class=kv-k>");
            try h.esc(app.base);
            if (app.elevated) {
                try h.raw(" ");
                try c.badge(h, s.admin, "warning");
            }
            try h.raw("</span><span class=kv-v></span></div>");
        }
        const act = try std.fmt.allocPrint(h.a, "ag-launch:{s}", .{g.id});
        defer h.a.free(act);
        try c.btnRowOpen(h);
        try c.btn(h, s.launch, "go", act, "");
        try c.btnRowClose(h);
        try h.raw("</div>");
    }
    try h.raw("</div>");
}

test "unavailable view" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .title = "App Groups", .unavailable = "n/a" });
    try std.testing.expectEqualStrings(
        "<h1 class=page-title>App Groups</h1><div class=\"rp-empty\"><div class=\"rp-empty__title\">n/a</div></div>",
        h.b.items,
    );
}

test "empty body inside full view" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .title = "T", .subtitle = "S", .available = true, .empty = "none" });
    try std.testing.expectEqualStrings(
        "<h1 class=page-title>T</h1><p class=page-sub>S</p><div id=appgroups-body>" ++
            "<div class=\"rp-empty\"><div class=\"rp-empty__title\">none</div></div></div>",
        h.b.items,
    );
}

test "populated group card" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const apps = [_]App{ .{ .base = "obs64.exe" }, .{ .base = "traktor.exe", .elevated = true } };
    const groups = [_]Group{.{ .id = "g1", .name = "Rig", .up = "1/2 up", .variant = "warning", .apps = &apps }};
    try renderBody(&h, .{ .available = true, .admin = "admin", .launch = "Launch", .groups = &groups });
    try std.testing.expectEqualStrings(
        "<div class=grid><div class=\"rp-card\"><div class=card-label>Rig</div>" ++
            "<div class=np-meta><span class=\"dot dot--warning\"></span> " ++
            "<span class=\"rp-badge rp-badge--warning\">1/2 up</span></div>" ++
            "<div class=kv><span class=kv-k>obs64.exe</span><span class=kv-v></span></div>" ++
            "<div class=kv><span class=kv-k>traktor.exe <span class=\"rp-badge rp-badge--warning\">admin</span></span><span class=kv-v></span></div>" ++
            "<div class=btn-row><button class=\"rp-btn rp-btn--go\" data-act=\"ag-launch:g1\">Launch</button></div>" ++
            "</div></div>",
        h.b.items,
    );
}
