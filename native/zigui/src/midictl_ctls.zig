//! MIDI-in controllers card + DJ bridge card — byte-exact ports of
//! internal/webui/render_midictl_controllers.go (midiCtlsHTML & friends). Tooltips arrive
//! as pre-rendered HTML (raw): tooltip.go stays the single source for that markup.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

pub const Link = struct {
    label: []const u8 = "",
    url: []const u8 = "",
};

/// PortStat is the #midi-ctlstat-<i> inner state (~1 Hz patch target).
pub const PortStat = struct {
    hasRow: bool = false,
    variant: []const u8 = "",
    label: []const u8 = "",
    labelDl: []const u8 = "",
    line: []const u8 = "",
    hint: []const u8 = "",
    hasAct: bool = false,
    act: []const u8 = "",
    actMsg: []const u8 = "",
};

pub const Chip = struct {
    label: []const u8 = "",
    act: []const u8 = "",
    active: bool = false,
};

pub const DrvThru = struct {
    show: bool = false,
    useInDj: []const u8 = "",
    port: []const u8 = "",
    cloneLbl: []const u8 = "",
    cloneDl: []const u8 = "",
    cloneAct: []const u8 = "",
    cloneOn: bool = false,
    cloneNote: []const u8 = "",
    drvNote: []const u8 = "",
    hasState: bool = false,
    stVariant: []const u8 = "",
    stLabel: []const u8 = "",
    stLabelDl: []const u8 = "",
    stLine: []const u8 = "",
    filterLbl: []const u8 = "",
    filterTip: []const u8 = "", // pre-rendered tooltip HTML (raw)
    chips: []const Chip = &.{},
};

pub const Warn = struct {
    show: bool = false,
    label: []const u8 = "",
    labelDl: []const u8 = "",
    line: []const u8 = "",
    hint: []const u8 = "",
};

pub const LearnCell = struct {
    act: []const u8 = "",
    clearAct: []const u8 = "",
    tid: []const u8 = "",
    set: bool = false,
    readout: []const u8 = "",
};

pub const LearnRow = struct {
    label: []const u8 = "",
    cells: []const LearnCell = &.{},
};

pub const LearnGrid = struct {
    hdr: []const u8 = "",
    hdrTip: []const u8 = "", // pre-rendered tooltip HTML (raw)
    cols: []const u8 = "", // digits only (CSS var) — raw
    chHdrs: []const []const u8 = &.{},
    rows: []const LearnRow = &.{},
    learn: []const u8 = "",
    relearn: []const u8 = "",
    clear: []const u8 = "",
};

pub const Block = struct {
    tid: []const u8 = "",
    title: []const u8 = "",
    statId: []const u8 = "", // midi-ctlstat-<i> (digits/ASCII id) — raw
    port: c.Select = .{},
    portLbl: []const u8 = "", // pre-rendered ss-label (raw)
    stat: PortStat = .{},
    enableLbl: []const u8 = "",
    enableDl: []const u8 = "",
    enableAct: []const u8 = "",
    enableOn: bool = false,
    thru: c.Select = .{},
    thruLbl: []const u8 = "",
    drvThru: DrvThru = .{},
    warn: Warn = .{},
    remove: []const u8 = "",
    removeAct: []const u8 = "",
    grid: LearnGrid = .{},
};

pub const State = struct {
    show: bool = false,
    card: []const u8 = "",
    badge: []const u8 = "",
    intro: []const u8 = "",
    introTip: []const u8 = "",
    linksLbl: []const u8 = "",
    links: []const Link = &.{},
    empty: []const u8 = "",
    blocks: []const Block = &.{},
    add: []const u8 = "",
};

/// render mirrors Go midiCtlsHTML (controllers card).
pub fn render(h: *Html, s: State) !void {
    if (!s.show) return;
    try c.cardOpen(h, s.card, true);
    try c.badge(h, s.badge, "info");
    try c.cardTrailClose(h);
    try h.raw("<p class=midi-help-note>");
    try h.esc(s.intro);
    try h.raw(" ");
    try h.raw(s.introTip);
    try h.raw("</p>");
    try renderLinks(h, s.linksLbl, s.links);
    if (s.blocks.len == 0) try c.emptyState(h, s.empty);
    for (s.blocks) |b| try renderBlock(h, b);
    try c.btnRowOpen(h);
    try c.btn(h, s.add, "primary", "midi-ctl-add", "");
    try c.btnRowClose(h);
    try c.cardClose(h);
}

/// renderLinks mirrors Go midiLinksHTML.
fn renderLinks(h: *Html, lbl: []const u8, links: []const Link) !void {
    try h.raw("<p class=midi-driver-links><span class=midi-driver-lbl>");
    try h.esc(lbl);
    try h.raw("</span> ");
    for (links, 0..) |l, i| {
        if (i > 0) try h.raw(" · ");
        try h.raw("<a href=");
        try h.attrQ(l.url);
        try h.raw(" target=_blank rel=noopener>");
        try h.esc(l.label);
        try h.raw("</a>");
    }
    try h.raw("</p>");
}

/// renderBlock mirrors Go midiCtlBlockHTML.
fn renderBlock(h: *Html, b: Block) !void {
    try h.raw("<div class=midi-ctlblock data-testid=");
    try h.attrQ(b.tid);
    try h.raw("><div class=midi-ctlhead>");
    try h.esc(b.title);
    try h.raw("</div>");
    try c.selectBoxRaw(h, b.port, b.portLbl);
    try h.raw("<div id=\"");
    try h.raw(b.statId);
    try h.raw("\">");
    try renderPortStat(h, b.stat);
    try h.raw("</div>");
    try c.toggleRow(h, b.enableLbl, b.enableDl, b.enableAct, b.enableOn);
    try c.selectBoxRaw(h, b.thru, b.thruLbl);
    try renderDrvThru(h, b.drvThru);
    try renderWarn(h, b.warn);
    try c.btnRowOpen(h);
    try c.btn(h, b.remove, "warn", b.removeAct, "");
    try c.btnRowClose(h);
    try renderGrid(h, b.grid);
    try h.raw("</div>");
}

/// renderPortStat mirrors Go midiPortStatHTML (#midi-ctlstat-<i> inner).
pub fn renderPortStat(h: *Html, s: PortStat) !void {
    if (s.hasRow) {
        try c.statusRow(h, s.variant, s.label, s.labelDl, s.line);
        if (s.hint.len != 0) {
            try h.raw("<p class=midi-help-note>");
            try h.esc(s.hint);
            try h.raw("</p>");
        }
    }
    if (s.hasAct) {
        try h.raw("<div class=midi-activity><span class=midi-actdot></span>");
        try h.esc(s.act);
        try h.raw(" <span class=midi-actmsg>");
        try h.esc(s.actMsg);
        try h.raw("</span></div>");
    }
}

/// renderDrvThru mirrors Go midiDrvThruHTML.
fn renderDrvThru(h: *Html, s: DrvThru) !void {
    if (!s.show) return;
    try h.raw("<div class=midi-drvthru><div class=midi-drvuse>");
    try h.esc(s.useInDj);
    try h.raw(" <code>");
    try h.esc(s.port);
    try h.raw("</code></div>");
    try c.toggleRow(h, s.cloneLbl, s.cloneDl, s.cloneAct, s.cloneOn);
    try h.raw("<p class=midi-help-note>");
    try h.esc(s.cloneNote);
    try h.raw("</p><p class=midi-help-note>");
    try h.esc(s.drvNote);
    try h.raw("</p>");
    if (s.hasState) try c.statusRow(h, s.stVariant, s.stLabel, s.stLabelDl, s.stLine);
    try h.raw("<div class=midi-drvfilters><span class=midi-steplbl>");
    try h.esc(s.filterLbl);
    try h.raw(" ");
    try h.raw(s.filterTip);
    try h.raw("</span>");
    for (s.chips) |ch| try c.fchip(h, ch.label, "", ch.act, ch.active);
    try h.raw("</div></div>");
}

/// renderWarn mirrors Go midiWarnHTML (THRU clash).
fn renderWarn(h: *Html, s: Warn) !void {
    if (!s.show) return;
    try c.statusRow(h, "warn", s.label, s.labelDl, s.line);
    try h.raw("<p class=midi-help-note>");
    try h.esc(s.hint);
    try h.raw("</p>");
}

/// renderGrid mirrors Go midiLearnGridHTML.
fn renderGrid(h: *Html, g: LearnGrid) !void {
    try h.raw("<div class=midi-learnhdr>");
    try h.esc(g.hdr);
    try h.raw(" ");
    try h.raw(g.hdrTip);
    try h.raw("</div><div class=midi-learngrid style=\"--cols:");
    try h.raw(g.cols);
    try h.raw("\"><div class=mlg-h></div>");
    for (g.chHdrs) |hd| {
        try h.raw("<div class=mlg-h>");
        try h.esc(hd);
        try h.raw("</div>");
    }
    for (g.rows) |r| {
        try h.raw("<div class=mlg-rowlbl>");
        try h.esc(r.label);
        try h.raw("</div>");
        for (r.cells) |cell| try renderCell(h, g, cell);
    }
    try h.raw("</div>");
}

/// renderCell mirrors Go midiLearnCellHTML.
fn renderCell(h: *Html, g: LearnGrid, cell: LearnCell) !void {
    if (cell.set) {
        try h.raw("<span class=mlg-cell><button class=\"mlg-chip mlg-chip--set\" data-act=");
        try h.attrQ(cell.act);
        try h.raw(" data-testid=");
        try h.attrQ(cell.tid);
        try h.raw(" title=");
        try h.attrQ(g.relearn);
        try h.raw(">");
        try h.esc(cell.readout);
        try h.raw("</button><button class=\"mlg-clear\" data-act=");
        try h.attrQ(cell.clearAct);
        try h.raw(" aria-label=");
        try h.attrQ(g.clear);
        try h.raw(">✕</button></span>");
        return;
    }
    try h.raw("<button class=mlg-chip data-act=");
    try h.attrQ(cell.act);
    try h.raw(" data-testid=");
    try h.attrQ(cell.tid);
    try h.raw(">");
    try h.esc(g.learn);
    try h.raw("</button>");
}

/// Bridge is the two-port DJ bridge card state.
pub const Bridge = struct {
    show: bool = false,
    card: []const u8 = "",
    badge: []const u8 = "",
    intro: []const u8 = "",
    introTip: []const u8 = "",
    enableLbl: []const u8 = "",
    enableDl: []const u8 = "",
    enableAct: []const u8 = "",
    enableOn: bool = false,
    enableTip: []const u8 = "",
    toDj: c.Select = .{},
    toDjLbl: []const u8 = "",
    fromDj: c.Select = .{},
    fromDjLbl: []const u8 = "",
};

/// renderBridge mirrors Go midiBridgeHTML.
pub fn renderBridge(h: *Html, s: Bridge) !void {
    if (!s.show) return;
    try c.cardOpen(h, s.card, true);
    try c.badge(h, s.badge, "info");
    try c.cardTrailClose(h);
    try h.raw("<p class=midi-help-note>");
    try h.esc(s.intro);
    try h.raw(" ");
    try h.raw(s.introTip);
    try h.raw("</p>");
    try c.toggleRowTip(h, s.enableLbl, s.enableDl, s.enableAct, s.enableOn, s.enableTip);
    try c.selectBoxRaw(h, s.toDj, s.toDjLbl);
    try c.selectBoxRaw(h, s.fromDj, s.fromDjLbl);
    try c.cardClose(h);
}

test "hidden cards render nothing" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{});
    try renderBridge(&h, .{});
    try std.testing.expectEqualStrings("", h.b.items);
}

test "port stat row + activity" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderPortStat(&h, .{
        .hasRow = true,
        .variant = "warn",
        .label = "Port",
        .labelDl = "port",
        .line = "in use",
        .hint = "close the other app",
        .hasAct = true,
        .act = "last input 2s ago",
        .actMsg = "CC 20",
    });
    try std.testing.expectEqualStrings("<div class=strow><span class=\"dot dot--warn\"></span>" ++
        "<div class=strow-tx><div class=strow-l data-label=\"port\">Port</div>" ++
        "<div class=strow-s data-value=\"in use\">in use</div></div></div>" ++
        "<p class=midi-help-note>close the other app</p>" ++
        "<div class=midi-activity><span class=midi-actdot></span>last input 2s ago" ++
        " <span class=midi-actmsg>CC 20</span></div>", h.b.items);
}

test "learn cell set vs empty" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const g = LearnGrid{ .learn = "Learn", .relearn = "Re&learn", .clear = "Clear" };
    try renderCell(&h, g, .{ .act = "midi-learn:0:play:1", .clearAct = "midi-unlearn:0:play:1", .tid = "t1", .set = true, .readout = "CC24" });
    try std.testing.expectEqualStrings("<span class=mlg-cell><button class=\"mlg-chip mlg-chip--set\" " ++
        "data-act=\"midi-learn:0:play:1\" data-testid=\"t1\" title=\"Re&amp;learn\">CC24</button>" ++
        "<button class=\"mlg-clear\" data-act=\"midi-unlearn:0:play:1\" aria-label=\"Clear\">✕</button></span>", h.b.items);
    h.b.clearRetainingCapacity();
    try renderCell(&h, g, .{ .act = "midi-learn:0:cue:2", .tid = "t2" });
    try std.testing.expectEqualStrings("<button class=mlg-chip data-act=\"midi-learn:0:cue:2\" data-testid=\"t2\">Learn</button>", h.b.items);
}
