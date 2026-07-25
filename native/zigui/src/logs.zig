//! Logs view renderer — byte-exact port of internal/webui/render_logs.go
//! (logsHTML/logsLinesHTML). State arrives fully resolved from Go (filters +
//! localized strings + the filtered tail); this file only walks state → markup.
//! Golden gate: internal/webui/zigui_golden_logs_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

pub const Entry = struct {
    time: []const u8 = "",
    lvl: []const u8 = "", // upper-cased level text (unpadded; padded to 5 here)
    cls: []const u8 = "", // classLetters CSS suffix (trusted A–Z, raw)
    src: []const u8 = "",
    msg: []const u8 = "",
    fields: []const u8 = "", // "" = none
};

pub const Lines = struct {
    wired: bool = false,
    noBus: []const u8 = "",
    noEntries: []const u8 = "",
    entries: []const Entry = &.{},
};

pub const State = struct {
    title: []const u8 = "",
    sub: []const u8 = "",
    showBus: bool = false,
    busActive: []const u8 = "",
    busItems: []const c.Tab = &.{},
    level: c.Select = .{},
    source: c.Select = .{},
    searchLabel: []const u8 = "",
    searchPh: []const u8 = "",
    searchVal: []const u8 = "",
    autoLabel: []const u8 = "",
    autoDl: []const u8 = "", // Go strings.ToLower(autoLabel)
    autoOn: bool = false,
    copy: []const u8 = "",
    clear: []const u8 = "",
    tailing: []const u8 = "",
    lines: Lines = .{},
};

/// render mirrors Go logsHTML (full tab view).
pub fn render(h: *Html, s: State) !void {
    try c.panel(h, s.title, s.sub);
    if (s.showBus) try c.subTabs(h, "logs-bus:", s.busActive, s.busItems);
    try h.raw("<div class=log-filters>");
    try c.selectBox(h, s.level);
    try c.selectBox(h, s.source);
    try h.raw("<label class=\"field log-search\" data-label=\"logs-search\"><span class=field-label>");
    try h.esc(s.searchLabel);
    try h.raw("</span><input class=field-input type=text placeholder=");
    try h.attrQ(s.searchPh);
    try h.raw(" value=");
    try h.attrQ(s.searchVal);
    try h.raw(" data-actinput=\"logs-search\"></label>");
    try c.toggleRow(h, s.autoLabel, s.autoDl, "logs-autoscroll", s.autoOn);
    try c.btn(h, s.copy, "outline", "logs-copy", "");
    try c.btn(h, s.clear, "outline", "logs-clear", "");
    try h.raw("<span class=page-sub style=\"margin:0\">");
    try h.esc(s.tailing);
    try h.raw("</span></div><div id=log-view class=log-view>");
    try renderLines(h, s.lines);
    try h.raw("</div>");
}

/// renderLines mirrors Go logsLinesHTML (#log-view inner, the filter/tick patch).
pub fn renderLines(h: *Html, s: Lines) !void {
    if (!s.wired) {
        try h.raw("<div class=log-line>");
        try h.esc(s.noBus);
        try h.raw("</div>");
        return;
    }
    if (s.entries.len == 0) {
        try h.raw("<div class=log-line>");
        try h.esc(s.noEntries);
        try h.raw("</div>");
        return;
    }
    for (s.entries) |e| {
        try h.raw("<div class=log-line>");
        try h.esc(e.time);
        try h.raw(" <span class=\"lv-");
        try h.raw(e.cls);
        try h.raw("\">");
        try h.esc(e.lvl);
        // padRight(lvl, 5): byte-length pad, same as Go
        var n: usize = e.lvl.len;
        while (n < 5) : (n += 1) try h.raw(" ");
        try h.raw("</span> <span class=lv-src>[");
        try h.esc(e.src);
        try h.raw("]</span> ");
        try h.esc(e.msg);
        if (e.fields.len != 0) {
            try h.raw(" ");
            try h.esc(e.fields);
        }
        try h.raw("</div>");
    }
}

test "unwired lines" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderLines(&h, .{ .noBus = "no bus" });
    try std.testing.expectEqualStrings("<div class=log-line>no bus</div>", h.b.items);
}

test "empty lines" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderLines(&h, .{ .wired = true, .noEntries = "none" });
    try std.testing.expectEqualStrings("<div class=log-line>none</div>", h.b.items);
}

test "entry line pads level and appends fields" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const es = [_]Entry{.{ .time = "12:00:00.000", .lvl = "WARN", .cls = "WARN", .src = "app", .msg = "m<1>", .fields = "map[a:1]" }};
    try renderLines(&h, .{ .wired = true, .entries = &es });
    try std.testing.expectEqualStrings("<div class=log-line>12:00:00.000 <span class=\"lv-WARN\">WARN </span>" ++
        " <span class=lv-src>[app]</span> m&lt;1&gt; map[a:1]</div>", h.b.items);
}

test "full view minimal" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{
        .title = "Logs",
        .sub = "S",
        .level = .{ .id = "logs-level", .label = "Level", .curLabel = "All" },
        .source = .{ .id = "logs-source", .label = "Source", .curLabel = "All sources" },
        .searchLabel = "Search",
        .searchPh = "ph",
        .autoLabel = "Auto",
        .autoDl = "auto",
        .autoOn = true,
        .copy = "Copy",
        .clear = "Clear",
        .tailing = "tail",
        .lines = .{ .wired = true, .noEntries = "none" },
    });
    // spot-check the frame around the components
    try std.testing.expect(std.mem.startsWith(u8, h.b.items, "<h1 class=page-title>Logs</h1><p class=page-sub>S</p><div class=log-filters><div class=ss-field>"));
    try std.testing.expect(std.mem.endsWith(u8, h.b.items, "<span class=page-sub style=\"margin:0\">tail</span></div><div id=log-view class=log-view><div class=log-line>none</div></div>"));
}
