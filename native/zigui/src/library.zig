//! Library tab (the biggest webui surface): tab header + section tabs + the eight sections
//! and the shared inspector. Byte-identical to the pure Go renderers in
//! internal/webui/render_library.go (golden gate: zigui_golden_library_test.go).
//!
//! Sub-views owned by OTHER renderers ride in the state as trusted pre-rendered markup and
//! are emitted raw, exactly as the Go renderer splices them: the target switcher, the nav
//! rail, the cue-edit waveform + rail, the remote-mirror / remote-cue-edit bodies, the
//! gridfix + tagfix panels, the prepare-select, the compat section, the media player, the
//! shared loudness block and the Camelot wheel SVG.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");
const k = @import("library_kit.zig");
const s = @import("library_sections.zig");
const d = @import("library_detail.zig");

pub const Detail = d.Detail;
pub const CueCell = s.CueCell;
pub const Queue = s.Queue;

/// Body is the #lib-body inner state: one kind + that section's sub-state.
pub const Body = struct {
    kind: []const u8 = "",
    raw: []const u8 = "",
    msg: []const u8 = "",
    navRail: []const u8 = "",
    ceFull: bool = false,
    ceWave: []const u8 = "",

    detail: d.Detail = .{},
    browse: s.Browse = .{},
    coll: s.Coll = .{},
    fav: s.Fav = .{},
    pls: s.Pls = .{},
    hist: s.Hist = .{},
    idm: s.IDM = .{},
    queue: s.Queue = .{},
    presets: s.Presets = .{},
};

pub const State = struct {
    title: []const u8 = "",
    navTitle: []const u8 = "",
    switcher: []const u8 = "",
    embedded: bool = false,
    section: []const u8 = "",
    tabs: []const c.Tab = &.{},
    body: Body = .{},
};

pub fn render(h: *Html, st: State) !void {
    try c.panel(h, st.title, st.navTitle);
    try h.raw(st.switcher);
    if (!st.embedded) {
        // remote mirror / remote cue edit: the embedded peer view carries its OWN section
        // tabs - a local duplicate row would be dead weight and shadow ctl clicks
        try c.subTabs(h, "lib-section:", st.section, st.tabs);
    }
    try h.raw("<div id=lib-body>");
    try renderBody(h, st.body);
    try h.raw("</div>");
}

pub fn renderBody(h: *Html, st: Body) !void {
    const eql = std.mem.eql;
    if (eql(u8, st.kind, "raw")) return h.raw(st.raw);
    if (eql(u8, st.kind, "msg")) return c.emptyState(h, st.msg);
    if (eql(u8, st.kind, "favorites")) return s.renderFav(h, st.fav);
    if (eql(u8, st.kind, "collection")) {
        if (st.ceFull) {
            // cue-edit mode: the waveform (grid + markers) spans the full tab width
            // above the list; the rail keeps only the editor controls.
            try h.raw("<div class=ce-fullwave>");
            try h.raw(st.ceWave);
            try h.raw("</div>");
        }
        try c.triOpen(h, "lib-nav-w", "lib-det-w", st.navRail);
        try s.renderColl(h, st.coll);
        try c.triMid(h, "lib-det-w");
        try detailWrap(h, st.detail);
        try c.triClose(h);
        return;
    }
    if (eql(u8, st.kind, "playlists")) {
        try c.mdOpen(h);
        try s.renderPls(h, st.pls);
        try c.mdSplit(h);
        try detailWrap(h, st.detail);
        try c.mdClose(h);
        return;
    }
    if (eql(u8, st.kind, "history")) {
        try c.mdWideOpen(h);
        try s.renderHist(h, st.hist);
        try c.mdSplit(h);
        try detailWrap(h, st.detail);
        try c.mdClose(h);
        return;
    }
    if (eql(u8, st.kind, "idmarks")) return s.renderIDM(h, st.idm);
    if (eql(u8, st.kind, "queue")) {
        try h.raw("<div id=lib-queue-body>");
        try s.renderQueue(h, st.queue);
        try h.raw("</div>");
        return;
    }
    if (eql(u8, st.kind, "presets")) return s.renderPresets(h, st.presets);
    // default: Browse renders the dir listing regardless (the collection hydrates async)
    try c.triOpen(h, "lib-nav-w", "lib-det-w", st.navRail);
    try s.renderBrowse(h, st.browse);
    try c.triMid(h, "lib-det-w");
    try detailWrap(h, st.detail);
    try c.triClose(h);
}

fn detailWrap(h: *Html, st: d.Detail) !void {
    try h.raw("<div id=lib-detail>");
    try d.render(h, st);
    try h.raw("</div>");
}

pub fn renderDetail(h: *Html, st: d.Detail) !void {
    return d.render(h, st);
}

pub fn renderQueue(h: *Html, st: s.Queue) !void {
    return s.renderQueue(h, st);
}

pub fn renderCueCell(h: *Html, st: s.CueCell) !void {
    return s.renderCueCell(h, st);
}

test {
    _ = k;
    _ = s;
    _ = d;
}

test "tab header + embedded skips the section tabs" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .title = "Library", .navTitle = "Local files", .switcher = "<div class=lib-target></div>", .embedded = true, .body = .{ .kind = "raw", .raw = "M" } });
    try std.testing.expectEqualStrings("<h1 class=page-title>Library</h1><p class=page-sub>Local files</p>" ++
        "<div class=lib-target></div><div id=lib-body>M</div>", h.b.items);
}

test "queue body wrapper" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderBody(&h, .{ .kind = "queue", .queue = .{ .desc = "Jobs", .empty = "none" } });
    try std.testing.expectEqualStrings("<div id=lib-queue-body><p class=page-sub>Jobs</p>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">none</div></div></div>", h.b.items);
}
