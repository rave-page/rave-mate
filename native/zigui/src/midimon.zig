//! MIDI monitor + ravemidi wire-trace renderers — byte-exact ports of
//! internal/webui/render_midimon.go (midiMonHTML/midiMonRowsHTML/midiTraceHTML).
//! State arrives fully resolved from Go (bus tail, driver ioctl rows, localized
//! strings). Golden gate: internal/webui/zigui_golden_midimon_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

pub const Row = struct {
    ago: []const u8 = "",
    src: []const u8 = "",
    msg: []const u8 = "",
};

/// Lines is the #midi-monitor inner state (~1 Hz patch target).
pub const Lines = struct {
    empty: []const u8 = "",
    rows: []const Row = &.{},
};

pub const State = struct {
    card: []const u8 = "",
    badge: []const u8 = "",
    sub: []const u8 = "",
    lines: Lines = .{},
};

/// render mirrors Go midiMonHTML (the monitor card).
pub fn render(h: *Html, s: State) !void {
    try c.cardOpen(h, s.card, true);
    try c.badge(h, s.badge, "info");
    try c.cardTrailClose(h);
    try h.raw("<p class=page-sub>");
    try h.esc(s.sub);
    try h.raw("</p><div id=midi-monitor>");
    try renderRows(h, s.lines);
    try h.raw("</div>");
    try c.cardClose(h);
}

/// renderRows mirrors Go midiMonRowsHTML (#midi-monitor inner).
pub fn renderRows(h: *Html, s: Lines) !void {
    if (s.rows.len == 0) return c.emptyState(h, s.empty);
    try h.raw("<div class=midi-monrows>");
    for (s.rows) |r| {
        try h.raw("<div class=midi-monrow><span class=midi-mont>");
        try h.esc(r.ago);
        try h.raw("</span><span class=midi-monsrc>");
        try h.esc(r.src);
        try h.raw("</span><span class=midi-monmsg>");
        try h.esc(r.msg);
        try h.raw("</span></div>");
    }
    try h.raw("</div>");
}

pub const TraceRow = struct {
    dt: []const u8 = "",
    dir: []const u8 = "", // digits only (CSS suffix) — raw
    label: []const u8 = "",
    hex: []const u8 = "",
    len: []const u8 = "",
    dec: []const u8 = "",
};

pub const Trace = struct {
    hdr: []const u8 = "",
    hasErr: bool = false,
    err: []const u8 = "",
    empty: []const u8 = "",
    rows: []const TraceRow = &.{},
    refresh: []const u8 = "",
    close: []const u8 = "",
};

/// renderTrace mirrors Go midiTraceHTML (driver wire-trace block).
pub fn renderTrace(h: *Html, s: Trace) !void {
    try h.raw("<div class=midi-trace><div class=pb-label>");
    try h.esc(s.hdr);
    try h.raw("</div>");
    if (s.hasErr) {
        try c.hint(h, "warn", s.err);
    } else if (s.rows.len == 0) {
        try c.emptyState(h, s.empty);
    } else {
        try h.raw("<div class=midi-tracerows>");
        for (s.rows) |r| {
            try h.raw("<div class=midi-tracerow><span class=midi-tracedt>");
            try h.esc(r.dt);
            try h.raw("</span><span class=\"midi-tracedir midi-tracedir--");
            try h.raw(r.dir);
            try h.raw("\">");
            try h.esc(r.label);
            try h.raw("</span><span class=midi-tracehex>");
            try h.esc(r.hex);
            try h.raw("</span><span class=midi-tracelen>");
            try h.esc(r.len);
            try h.raw("</span><span class=midi-tracedec>");
            try h.esc(r.dec);
            try h.raw("</span></div>");
        }
        try h.raw("</div>");
    }
    try c.btnRowOpen(h);
    try c.btn(h, s.refresh, "outline", "midi-drv-trace-refresh", "");
    try c.btn(h, s.close, "ghost", "midi-drv-trace:0", "");
    try c.btnRowClose(h);
    try h.raw("</div>");
}

test "monitor rows empty" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderRows(&h, .{ .empty = "no input" });
    try std.testing.expectEqualStrings("<div class=\"rp-empty\"><div class=\"rp-empty__title\">no input</div></div>", h.b.items);
}

test "monitor card wraps rows" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const rows = [_]Row{.{ .ago = "2s", .src = "DDJ<400", .msg = "CC 20" }};
    try render(&h, .{ .card = "Monitor", .badge = "live", .sub = "s&b", .lines = .{ .rows = &rows } });
    try std.testing.expectEqualStrings("<div class=\"rp-card\"><div class=card-head><span class=card-h>Monitor</span>" ++
        "<span class=card-trail><span class=\"rp-badge rp-badge--info\">live</span></span></div>" ++
        "<p class=page-sub>s&amp;b</p><div id=midi-monitor><div class=midi-monrows>" ++
        "<div class=midi-monrow><span class=midi-mont>2s</span><span class=midi-monsrc>DDJ&lt;400</span>" ++
        "<span class=midi-monmsg>CC 20</span></div></div></div></div>", h.b.items);
}

test "trace error takes precedence over rows" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderTrace(&h, .{ .hdr = "Port 3", .hasErr = true, .err = "ioctl \"failed\"", .refresh = "R", .close = "X" });
    try std.testing.expectEqualStrings("<div class=midi-trace><div class=pb-label>Port 3</div>" ++
        "<span class=\"hint hint--warn\">ioctl &#34;failed&#34;</span>" ++
        "<div class=btn-row><button class=\"rp-btn rp-btn--outline\" data-act=\"midi-drv-trace-refresh\">R</button>" ++
        "<button class=\"rp-btn rp-btn--ghost\" data-act=\"midi-drv-trace:0\">X</button></div></div>", h.b.items);
}

test "trace row uses raw dir suffix" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const rows = [_]TraceRow{.{ .dt = "+5ms", .dir = "2", .label = "to app", .hex = "B0 14 7F", .len = "3B", .dec = "CC 20" }};
    try renderTrace(&h, .{ .hdr = "H", .rows = &rows, .refresh = "R", .close = "X" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<span class=\"midi-tracedir midi-tracedir--2\">to app</span>") != null);
}
