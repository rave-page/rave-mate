//! Twitch view renderer — byte-exact port of internal/webui/render_twitch.go
//! (twitchHTML + the #twitch-obs / #twitch-presets / #twitch-feed fragments). State
//! arrives fully resolved from Go: the rolling feed buffer already holds row STATE, so
//! this file only walks it. Golden gate: internal/webui/zigui_golden_twitch_test.go.
//!
//! `obs.cockpit` is render_live.go's cockpitHTML output — that renderer belongs to the
//! Live tab, so it rides through as pre-rendered trusted markup and is emitted raw.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

pub const Tag = struct {
    text: []const u8 = "",
    variant: []const u8 = "",
};

/// Row is one feed row. kind ∈ day|chat|alert.
pub const Row = struct {
    kind: []const u8 = "",

    date: []const u8 = "", // day

    name: []const u8 = "", // chat
    nameStyle: []const u8 = "", // trusted inline colour (Go-validated)
    tags: []const Tag = &.{},
    mod: bool = false,
    modVal: []const u8 = "",
    modTitle: []const u8 = "",

    text: []const u8 = "", // chat message / alert line
    variant: []const u8 = "", // alert accent (trusted literal)
};

pub const Viewers = struct {
    cls: []const u8 = "", // trusted class literal
    text: []const u8 = "",
};

pub const Obs = struct {
    viewers: Viewers = .{},
    cockpit: []const u8 = "", // RAW markup from the Live tab's renderer
};

pub const Presets = struct {
    chips: []const c.Btn = &.{},
    empty: []const u8 = "",
    manage: []const u8 = "",
    add: []const u8 = "",
};

pub const Feed = struct {
    empty: []const u8 = "",
    rows: []const Row = &.{},
};

pub const State = struct {
    title: []const u8 = "",
    sub: []const u8 = "",
    available: bool = false,
    unavailable: []const u8 = "",

    showObs: bool = false,
    obsTitle: []const u8 = "",
    obs: Obs = .{},

    showPresets: bool = false,
    presetsTitle: []const u8 = "",
    presets: Presets = .{},

    feed: Feed = .{},

    showSend: bool = false,
    sendPh: []const u8 = "",
    sendLbl: []const u8 = "",
};

/// render mirrors Go twitchHTML (full tab view).
pub fn render(h: *Html, s: State) !void {
    try c.panel(h, s.title, s.sub);
    if (!s.available) {
        try c.emptyState(h, s.unavailable);
        return;
    }
    if (s.showObs) {
        try c.sectionOpen(h, s.obsTitle);
        try h.raw("<div id=twitch-obs>");
        try renderObs(h, s.obs);
        try h.raw("</div>");
        try c.sectionClose(h);
    }
    if (s.showPresets) {
        try c.sectionOpen(h, s.presetsTitle);
        try h.raw("<div id=twitch-presets>");
        try renderPresets(h, s.presets);
        try h.raw("</div>");
        try c.sectionClose(h);
    }
    try h.raw("<div id=twitch-feed class=log-view>");
    try renderFeed(h, s.feed);
    try h.raw("</div>");
    if (s.showSend) {
        try h.raw("<form data-act=twitch-send class=tw-send><input class=field-input name=text placeholder=");
        try h.attrQ(s.sendPh);
        try h.raw(" style=\"flex:1\" autocomplete=off><button class=\"rp-btn rp-btn--primary\" type=submit>");
        try h.esc(s.sendLbl);
        try h.raw("</button></form>");
    }
}

/// renderObs mirrors Go twObsHTML (#twitch-obs fragment).
pub fn renderObs(h: *Html, s: Obs) !void {
    try h.raw("<div class=tw-viewers><span class=\"");
    try h.raw(s.viewers.cls);
    try h.raw("\">");
    try h.esc(s.viewers.text);
    try h.raw("</span></div>");
    try h.raw(s.cockpit);
}

/// renderPresets mirrors Go twPresetsHTML (#twitch-presets fragment).
pub fn renderPresets(h: *Html, s: Presets) !void {
    try h.raw("<div class=tw-presets>");
    for (s.chips) |ch| try c.btnOf(h, ch);
    if (s.chips.len == 0) {
        try h.raw("<span class=tw-hint>");
        try h.esc(s.empty);
        try h.raw("</span>");
    }
    try h.raw("</div>");
    try c.btnRowOpen(h);
    try c.btn(h, s.manage, "secondary", "tw-presets", "");
    try c.btn(h, s.add, "ghost", "tw-preset-add", "");
    try c.btnRowClose(h);
}

/// renderFeed mirrors Go twFeedHTML (#twitch-feed inner fragment).
pub fn renderFeed(h: *Html, s: Feed) !void {
    if (s.rows.len == 0) {
        try h.raw("<div class=log-line>");
        try h.esc(s.empty);
        try h.raw("</div>");
        return;
    }
    for (s.rows) |r| try renderRow(h, r);
}

/// renderRow mirrors Go twRowHTML.
fn renderRow(h: *Html, r: Row) !void {
    if (std.mem.eql(u8, r.kind, "day")) {
        try h.raw("<div class=\"log-line tw-sep\">— ");
        try h.esc(r.date);
        try h.raw(" —</div>");
        return;
    }
    if (std.mem.eql(u8, r.kind, "alert")) {
        try h.raw("<div class=\"log-line tw-alert tw-alert--");
        try h.raw(r.variant);
        try h.raw("\">");
        try h.esc(r.text);
        try h.raw("</div>");
        return;
    }
    try h.raw("<div class=\"log-line tw-row\">");
    if (r.mod) {
        try h.raw("<button class=\"rp-btn rp-btn--ghost tw-modbtn\" data-act=tw-mod data-val=\"");
        try h.esc(r.modVal);
        try h.raw("\" title=");
        try h.attrQ(r.modTitle);
        try h.raw(">⋮</button>");
    }
    try h.raw("<span class=tw-name style=\"");
    try h.raw(r.nameStyle);
    try h.raw("\">");
    try h.esc(r.name);
    try h.raw("</span>");
    for (r.tags) |t| try c.badge(h, t.text, t.variant);
    try h.raw(" <span class=tw-msg>");
    try h.esc(r.text);
    try h.raw("</span></div>");
}

test "unavailable keeps the panel" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .title = "Twitch", .sub = "S", .unavailable = "no twitch" });
    try std.testing.expectEqualStrings("<h1 class=page-title>Twitch</h1><p class=page-sub>S</p>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">no twitch</div></div>", h.b.items);
}

test "empty feed" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderFeed(&h, .{ .empty = "no messages" });
    try std.testing.expectEqualStrings("<div class=log-line>no messages</div>", h.b.items);
}

test "day separator" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const rows = [_]Row{.{ .kind = "day", .date = "2026-07-25" }};
    try renderFeed(&h, .{ .rows = &rows });
    try std.testing.expectEqualStrings("<div class=\"log-line tw-sep\">— 2026-07-25 —</div>", h.b.items);
}

test "chat row with mod button, badges and colour" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const tags = [_]Tag{ .{ .text = "HOST", .variant = "error" }, .{ .text = "CHEER", .variant = "warning" } };
    const rows = [_]Row{.{ .kind = "chat", .name = "dj&x", .nameStyle = "color:#08F79B", .tags = &tags, .mod = true, .modVal = "m1|u1|dj&x", .modTitle = "Moderate", .text = "hi <there>" }};
    try renderFeed(&h, .{ .rows = &rows });
    try std.testing.expectEqualStrings("<div class=\"log-line tw-row\">" ++
        "<button class=\"rp-btn rp-btn--ghost tw-modbtn\" data-act=tw-mod data-val=\"m1|u1|dj&amp;x\" title=\"Moderate\">⋮</button>" ++
        "<span class=tw-name style=\"color:#08F79B\">dj&amp;x</span>" ++
        "<span class=\"rp-badge rp-badge--error\">HOST</span><span class=\"rp-badge rp-badge--warning\">CHEER</span>" ++
        " <span class=tw-msg>hi &lt;there&gt;</span></div>", h.b.items);
}

test "alert row" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const rows = [_]Row{.{ .kind = "alert", .variant = "sub", .text = "a&b subscribed" }};
    try renderFeed(&h, .{ .rows = &rows });
    try std.testing.expectEqualStrings("<div class=\"log-line tw-alert tw-alert--sub\">a&amp;b subscribed</div>", h.b.items);
}

test "obs passes the cockpit markup through raw" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderObs(&h, .{ .viewers = .{ .cls = "tw-vc tw-vc--live", .text = "1,234 viewers" }, .cockpit = "<div class=x>raw & kept</div>" });
    try std.testing.expectEqualStrings("<div class=tw-viewers><span class=\"tw-vc tw-vc--live\">1,234 viewers</span></div>" ++
        "<div class=x>raw & kept</div>", h.b.items);
}

test "presets empty shows the hint" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderPresets(&h, .{ .empty = "no presets", .manage = "Manage", .add = "Add" });
    try std.testing.expectEqualStrings("<div class=tw-presets><span class=tw-hint>no presets</span></div>" ++
        "<div class=btn-row><button class=\"rp-btn rp-btn--secondary\" data-act=\"tw-presets\">Manage</button>" ++
        "<button class=\"rp-btn rp-btn--ghost\" data-act=\"tw-preset-add\">Add</button></div>", h.b.items);
}
