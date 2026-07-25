//! Live view renderer — byte-exact port of internal/webui/render_live.go (liveHTML +
//! every tick-patched fragment). State arrives fully resolved from Go: service
//! snapshots, localized strings, ALL number formatting, and the Go-built raw fragments
//! (sparkline SVGs, graph legends, tooltip cards) which are embedded verbatim.
//!
//! Contract notes (do not "clean up"):
//!   - ids live-stream-state / live-rec-state / live-tc / live-link-fill / live-link-cap
//!     are patch + client-rAF (`__rt 'link'`) targets. The phrase-bar fill width arrives
//!     pre-formatted ("43.75%") because the rAF runtime overwrites it per frame.
//!   - status rows emit data-label UNESCAPED (Go splices strings.ToLower(k) raw); the
//!     signals card's rows carry no data-label at all. Both replicate the Go original.
//! Golden gate: internal/webui/zigui_golden_live_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

/// KV is one k/v row: k label, kl its Go-lowered data-label, v value.
pub const KV = struct {
    k: []const u8 = "",
    kl: []const u8 = "",
    v: []const u8 = "",
};

/// SRow is one resolved statusRow (dl = Go strings.ToLower(label)).
pub const SRow = struct {
    variant: []const u8 = "",
    label: []const u8 = "",
    dl: []const u8 = "",
    line: []const u8 = "",
};

pub const Transport = struct {
    streamHint: []const u8 = "",
    streamLabel: []const u8 = "",
    dotVar: []const u8 = "",
    state: []const u8 = "",
    metaOnly: []const u8 = "",
    pauseLabel: []const u8 = "",
    pauseHint: []const u8 = "",
    paused: bool = false,
    hasRec: bool = false,
    recHint: []const u8 = "",
    recLabel: []const u8 = "",
    recBtn: []const u8 = "",
    recState: []const u8 = "",
    hasTc: bool = false,
    tcLabel: []const u8 = "",
    tc: []const u8 = "",
    startLbl: []const u8 = "",
    stopLbl: []const u8 = "",
};

pub const NP = struct {
    line1: []const u8 = "",
    line2: []const u8 = "",
};

pub const Status = struct {
    rows: []const KV = &.{},
};

pub const Deck = struct {
    cls: []const u8 = "", // resolved class list (trusted literals)
    name: []const u8 = "",
    title: []const u8 = "",
    meta: []const u8 = "",
    via: []const u8 = "", // "" = no provenance line
};

pub const Decks = struct {
    note: []const u8 = "",
    decks: []const Deck = &.{},
};

pub const Signals = struct {
    rows: []const KV = &.{},
};

pub const CockpitRow = struct {
    variant: []const u8 = "",
    name: []const u8 = "",
    state: []const u8 = "",
    streamLbl: []const u8 = "",
    streamAct: []const u8 = "",
    recLbl: []const u8 = "",
    recAct: []const u8 = "",
};

pub const Cockpit = struct {
    empty: []const u8 = "",
    caption: []const u8 = "",
    rows: []const CockpitRow = &.{},
};

pub const Link = struct {
    available: bool = false,
    backend: SRow = .{},
    fill: []const u8 = "0.00%", // pre-formatted width
    cap: []const u8 = "",
    session: SRow = .{},
    resyncLbl: []const u8 = "",
    sources: []const SRow = &.{},
};

pub const Graph = struct {
    tooltip: []const u8 = "",
    legend: []const u8 = "", // raw (Go-built, carries inline colors)
    graph: []const u8 = "", // raw sparkline SVG
};

pub const Perf = struct {
    tooltip: []const u8 = "",
    cpuLeg: []const u8 = "",
    cpuGraph: []const u8 = "",
    ramLeg: []const u8 = "",
    ramGraph: []const u8 = "",
    head: []const u8 = "",
    headColor: []const u8 = "",
};

pub const Strip = struct {
    left: []const u8 = "",
    center: []const u8 = "",
    right: []const u8 = "",
};

pub const State = struct {
    title: []const u8 = "",
    sub: []const u8 = "",
    transport: Transport = .{},
    np: NP = .{},
    statusTitle: []const u8 = "",
    status: Status = .{},
    decksTitle: []const u8 = "",
    decks: Decks = .{},
    hasSignals: bool = false,
    signalsTitle: []const u8 = "",
    signalsTip: []const u8 = "", // legacy raw (bridge)
    signalsTipSt: ?c.Tip = null, // structured tooltip — wins over signalsTip
    signals: Signals = .{},
    hasCockpit: bool = false,
    cockpitTitle: []const u8 = "",
    cockpit: Cockpit = .{},
    hasLink: bool = false,
    linkTitle: []const u8 = "",
    link: Link = .{},
    hasNet: bool = false,
    netTitle: []const u8 = "",
    netTip: []const u8 = "", // legacy raw (bridge)
    netTipSt: ?c.Tip = null, // structured tooltip — wins over netTip
    net: Graph = .{},
    timTitle: []const u8 = "",
    timTip: []const u8 = "", // legacy raw (bridge)
    timTipSt: ?c.Tip = null, // structured tooltip — wins over timTip
    tim: Graph = .{},
    hasPerf: bool = false,
    perfTitle: []const u8 = "",
    perfTip: []const u8 = "", // legacy raw (bridge)
    perfTipSt: ?c.Tip = null, // structured tooltip — wins over perfTip
    perf: Perf = .{},
    strip: Strip = .{},
};

/// render mirrors Go liveHTML (full cockpit; every fragment wrapped in its patch id).
pub fn render(h: *Html, s: State) !void {
    try c.panel(h, s.title, s.sub);
    try h.raw("<div id=live-transport>");
    try renderTransport(h, s.transport);
    try h.raw("</div><div id=live-np>");
    try renderNP(h, s.np);
    try h.raw("</div>");
    try c.sectionOpen(h, s.statusTitle);
    try h.raw("<div id=live-status>");
    try renderStatus(h, s.status);
    try h.raw("</div>");
    try c.sectionClose(h);
    try c.sectionOpen(h, s.decksTitle);
    try h.raw("<div id=live-decks>");
    try renderDecks(h, s.decks);
    try h.raw("</div>");
    try c.sectionClose(h);
    if (s.hasSignals) {
        var signalstb = try c.tipBuf(h, s.signalsTipSt, s.signalsTip);
        defer signalstb.deinit();
        try c.sectionOpenTip(h, s.signalsTitle, signalstb.b.items);
        try h.raw("<div id=live-signals>");
        try renderSignals(h, s.signals);
        try h.raw("</div>");
        try c.sectionClose(h);
    }
    if (s.hasCockpit) {
        try c.sectionOpen(h, s.cockpitTitle);
        try h.raw("<div id=live-cockpit>");
        try renderCockpit(h, s.cockpit);
        try h.raw("</div>");
        try c.sectionClose(h);
    }
    if (s.hasLink) {
        try c.sectionOpen(h, s.linkTitle);
        try h.raw("<div id=live-ablelink>");
        try renderLink(h, s.link);
        try h.raw("</div>");
        try c.sectionClose(h);
    }
    if (s.hasNet) {
        var nettb = try c.tipBuf(h, s.netTipSt, s.netTip);
        defer nettb.deinit();
        try c.sectionOpenTip(h, s.netTitle, nettb.b.items);
        try h.raw("<div id=live-net>");
        try renderGraph(h, s.net);
        try h.raw("</div>");
        try c.sectionClose(h);
        var timtb = try c.tipBuf(h, s.timTipSt, s.timTip);
        defer timtb.deinit();
        try c.sectionOpenTip(h, s.timTitle, timtb.b.items);
        try h.raw("<div id=live-tim>");
        try renderGraph(h, s.tim);
        try h.raw("</div>");
        try c.sectionClose(h);
    }
    if (s.hasPerf) {
        var perftb = try c.tipBuf(h, s.perfTipSt, s.perfTip);
        defer perftb.deinit();
        try c.sectionOpenTip(h, s.perfTitle, perftb.b.items);
        try h.raw("<div id=live-perf2>");
        try renderPerf(h, s.perf);
        try h.raw("</div>");
        try c.sectionClose(h);
    }
    try h.raw("<div id=live-strip class=livestrip>");
    try renderStrip(h, s.strip);
    try h.raw("</div>");
}

/// renderTransport mirrors Go liveTransHTML (#live-transport).
pub fn renderTransport(h: *Html, s: Transport) !void {
    try h.raw("<div class=transport><span class=tlabel title=");
    try h.attrQ(s.streamHint);
    try h.raw(">");
    try h.esc(s.streamLabel);
    try h.raw("</span><span class=np-artist id=live-stream-state title=");
    try h.attrQ(s.streamHint);
    try h.raw(">");
    try c.dot(h, s.dotVar);
    try h.raw(" ");
    try h.esc(s.state);
    try h.raw("</span><span class=tlabel style=\"opacity:.7\">");
    try h.esc(s.metaOnly);
    try h.raw("</span><span class=tlabel>");
    try h.esc(s.pauseLabel);
    try h.raw("</span><label class=switch data-label=\"private stream\" title=");
    try h.attrQ(s.pauseHint);
    try h.raw("><input type=checkbox");
    if (s.paused) try h.raw(" checked");
    try h.raw(" data-act=stream-pause data-value=");
    try h.attrQ(if (s.paused) "true" else "false");
    try h.raw("><span class=switch-track></span></label>");
    if (s.hasRec) {
        try h.raw("<span class=tsep></span><span class=tlabel title=");
        try h.attrQ(s.recHint);
        try h.raw(">");
        try h.esc(s.recLabel);
        try h.raw("</span><button class=\"rp-btn rp-btn--outline\" data-act=arec-toggle title=");
        try h.attrQ(s.recHint);
        try h.raw(">");
        try h.esc(s.recBtn);
        try h.raw("</button><span class=np-artist id=live-rec-state title=");
        try h.attrQ(s.recHint);
        try h.raw(">");
        try h.esc(s.recState);
        try h.raw("</span>");
    }
    if (s.hasTc) {
        try h.raw("<span class=tsep></span><span class=tlabel>");
        try h.esc(s.tcLabel);
        try h.raw("</span><span class=tmono id=live-tc>");
        try h.esc(s.tc);
        try h.raw("</span><button class=\"rp-btn rp-btn--go\" data-act=tc-start>");
        try h.esc(s.startLbl);
        try h.raw("</button><button class=\"rp-btn rp-btn--outline\" data-act=tc-stop>");
        try h.esc(s.stopLbl);
        try h.raw("</button>");
    }
    try h.raw("</div>");
}

/// renderNP mirrors Go liveNPHTML (#live-np LCD).
pub fn renderNP(h: *Html, s: NP) !void {
    try h.raw("<div class=lcd><div class=lcd-1 data-label=\"now playing\" data-value=\"");
    try h.esc(s.line1);
    try h.raw("\">");
    try h.esc(s.line1);
    try h.raw("</div><div class=lcd-2>");
    try h.esc(s.line2);
    try h.raw("</div></div>");
}

/// renderStatus mirrors Go liveStatusFragHTML (#live-status; data-label emitted RAW).
pub fn renderStatus(h: *Html, s: Status) !void {
    try h.raw("<div class=\"rp-card\">");
    for (s.rows) |r| {
        try h.raw("<div class=st-row><span class=st-k>");
        try h.esc(r.k);
        try h.raw("</span><span data-label=\"");
        try h.raw(r.kl);
        try h.raw("\" data-value=\"");
        try h.esc(r.v);
        try h.raw("\">");
        try h.esc(r.v);
        try h.raw("</span></div>");
    }
    try h.raw("</div>");
}

/// renderDecks mirrors Go liveDecksFragHTML (#live-decks).
pub fn renderDecks(h: *Html, s: Decks) !void {
    if (s.note.len != 0) {
        try h.raw("<div class=decks-note>");
        try h.esc(s.note);
        try h.raw("</div>");
    }
    try h.raw("<div class=decks-grid>");
    for (s.decks) |d| {
        try h.raw("<div class=\"");
        try h.raw(d.cls);
        try h.raw("\"><div class=deckbig-id>");
        try h.esc(d.name);
        try h.raw("</div><div class=deckbig-t>");
        try h.esc(d.title);
        try h.raw("</div><div class=deckbig-m>");
        try h.esc(d.meta);
        try h.raw("</div>");
        if (d.via.len != 0) {
            try h.raw("<div class=\"deckbig-m deckbig-src\">");
            try h.esc(d.via);
            try h.raw("</div>");
        }
        try h.raw("</div>");
    }
    try h.raw("</div>");
}

/// renderSignals mirrors Go liveSignalsFragHTML (#live-signals; rows carry no data-label).
pub fn renderSignals(h: *Html, s: Signals) !void {
    try h.raw("<div class=\"rp-card\">");
    for (s.rows) |r| {
        try h.raw("<div class=st-row><span class=st-k>");
        try h.esc(r.k);
        try h.raw("</span><span>");
        try h.esc(r.v);
        try h.raw("</span></div>");
    }
    try h.raw("</div>");
}

/// renderCockpit mirrors Go liveCockpitFragHTML (#live-cockpit; also the Twitch tab's copy).
pub fn renderCockpit(h: *Html, s: Cockpit) !void {
    if (s.rows.len == 0) {
        try c.emptyState(h, s.empty);
        return;
    }
    try h.raw("<div class=decks-note>");
    try h.esc(s.caption);
    try h.raw("</div><div class=\"rp-card\">");
    for (s.rows) |r| {
        try h.raw("<div class=row><span class=row-label>");
        try c.dot(h, r.variant);
        try h.raw(" ");
        try h.esc(r.name);
        try h.raw(" <span class=np-artist>");
        try h.esc(r.state);
        try h.raw("</span></span>");
        try c.btnRowOpen(h);
        try c.btn(h, r.streamLbl, "primary", r.streamAct, "");
        try c.btn(h, r.recLbl, "outline", r.recAct, "");
        try c.btnRowClose(h);
        try h.raw("</div>");
    }
    try h.raw("</div>");
}

/// renderLink mirrors Go liveLinkFragHTML (#live-ablelink).
pub fn renderLink(h: *Html, s: Link) !void {
    if (!s.available) {
        try statusRow(h, s.backend);
        return;
    }
    // phrase bar: ids are the client-rAF interpolation targets, fill pre-formatted Go-side
    try h.raw("<div class=pbar><div class=\"pbar-fill\" id=live-link-fill style=\"width:");
    try h.raw(s.fill);
    try h.raw("\"></div><span class=\"pbar-cap\" id=live-link-cap>");
    try h.esc(s.cap);
    try h.raw("</span></div>");
    try statusRow(h, s.session);
    try c.btnRowOpen(h);
    try c.btn(h, s.resyncLbl, "outline", "ablelink-resync", "");
    try c.btnRowClose(h);
    for (s.sources) |src| try statusRow(h, src);
}

fn statusRow(h: *Html, r: SRow) !void {
    try c.statusRow(h, r.variant, r.label, r.dl, r.line);
}

/// renderGraph mirrors Go liveGraphFragHTML (#live-net / #live-tim).
pub fn renderGraph(h: *Html, s: Graph) !void {
    try h.raw("<div class=gwell title=");
    try h.attrQ(s.tooltip);
    try h.raw("><div class=glegend>");
    try h.raw(s.legend);
    try h.raw("</div>");
    try h.raw(s.graph);
    try h.raw("</div>");
}

/// renderPerf mirrors Go livePerfFragHTML (#live-perf2).
pub fn renderPerf(h: *Html, s: Perf) !void {
    try h.raw("<div class=gwell title=");
    try h.attrQ(s.tooltip);
    try h.raw("><div class=glegend>");
    try h.raw(s.cpuLeg);
    try h.raw("</div>");
    try h.raw(s.cpuGraph);
    try h.raw("<div class=glegend>");
    try h.raw(s.ramLeg);
    try h.raw("</div>");
    try h.raw(s.ramGraph);
    try h.raw("<div class=glegend><span style=\"color:");
    try h.raw(s.headColor);
    try h.raw("\">");
    try h.esc(s.head);
    try h.raw("</span></div></div>");
}

/// renderStrip mirrors Go liveStripFragHTML (#live-strip).
pub fn renderStrip(h: *Html, s: Strip) !void {
    try h.raw("<span>");
    try h.esc(s.left);
    try h.raw("</span><span>");
    try h.esc(s.center);
    try h.raw("</span><span>");
    try h.esc(s.right);
    try h.raw("</span>");
}

test "transport minimal (no rec, no timecode)" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderTransport(&h, .{ .streamHint = "meta only", .streamLabel = "STREAM", .dotVar = "muted", .state = "Idle", .metaOnly = "metadata only", .pauseLabel = "Pause", .pauseHint = "hint" });
    try std.testing.expectEqualStrings("<div class=transport><span class=tlabel title=\"meta only\">STREAM</span>" ++
        "<span class=np-artist id=live-stream-state title=\"meta only\"><span class=\"dot dot--muted\"></span> Idle</span>" ++
        "<span class=tlabel style=\"opacity:.7\">metadata only</span><span class=tlabel>Pause</span>" ++
        "<label class=switch data-label=\"private stream\" title=\"hint\"><input type=checkbox data-act=stream-pause " ++
        "data-value=\"false\"><span class=switch-track></span></label></div>", h.b.items);
}

test "np lcd duplicates line1 into data-value" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderNP(&h, .{ .line1 = "♪ A&B", .line2 = "Deck A" });
    try std.testing.expectEqualStrings("<div class=lcd><div class=lcd-1 data-label=\"now playing\" data-value=\"♪ A&amp;B\">" ++
        "♪ A&amp;B</div><div class=lcd-2>Deck A</div></div>", h.b.items);
}

test "status row emits data-label raw, value escaped" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const rows = [_]KV{.{ .k = "API", .kl = "api", .v = "https://x/?a=1&b=2" }};
    try renderStatus(&h, .{ .rows = &rows });
    try std.testing.expectEqualStrings("<div class=\"rp-card\"><div class=st-row><span class=st-k>API</span>" ++
        "<span data-label=\"api\" data-value=\"https://x/?a=1&amp;b=2\">https://x/?a=1&amp;b=2</span></div></div>", h.b.items);
}

test "deck tile with via line" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const ds = [_]Deck{.{ .cls = "deckbig deckbig--live", .name = "DECK A", .title = "x - y", .meta = "1:00 / 2:00", .via = "via traktor" }};
    try renderDecks(&h, .{ .note = "from peer", .decks = &ds });
    try std.testing.expectEqualStrings("<div class=decks-note>from peer</div><div class=decks-grid>" ++
        "<div class=\"deckbig deckbig--live\"><div class=deckbig-id>DECK A</div><div class=deckbig-t>x - y</div>" ++
        "<div class=deckbig-m>1:00 / 2:00</div><div class=\"deckbig-m deckbig-src\">via traktor</div></div></div>", h.b.items);
}

test "cockpit empty state" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderCockpit(&h, .{ .empty = "No OBS" });
    try std.testing.expectEqualStrings("<div class=\"rp-empty\"><div class=\"rp-empty__title\">No OBS</div></div>", h.b.items);
}

test "link unavailable falls back to one status row" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderLink(&h, .{ .backend = .{ .variant = "warning", .label = "Backend", .dl = "backend", .line = "unavailable" } });
    try std.testing.expectEqualStrings("<div class=strow><span class=\"dot dot--warning\"></span><div class=strow-tx>" ++
        "<div class=strow-l data-label=\"backend\">Backend</div><div class=strow-s data-value=\"unavailable\">unavailable</div>" ++
        "</div></div>", h.b.items);
}

test "link phrase bar keeps rAF ids + pre-formatted fill" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderLink(&h, .{ .available = true, .fill = "37.50%", .cap = "Beat 7 / 16", .resyncLbl = "Resync" });
    try std.testing.expect(std.mem.startsWith(u8, h.b.items, "<div class=pbar><div class=\"pbar-fill\" id=live-link-fill " ++
        "style=\"width:37.50%\"></div><span class=\"pbar-cap\" id=live-link-cap>Beat 7 / 16</span></div>"));
}

test "graph + perf wells embed Go-built legends raw" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderGraph(&h, .{ .tooltip = "t&t", .legend = "<span style=\"color:#08F79B\">x</span>", .graph = "<svg/>" });
    try std.testing.expectEqualStrings("<div class=gwell title=\"t&amp;t\"><div class=glegend>" ++
        "<span style=\"color:#08F79B\">x</span></div><svg/></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try renderPerf(&h, .{ .tooltip = "p", .cpuLeg = "<b>c</b>", .cpuGraph = "<svg id=c/>", .ramLeg = "<b>r</b>", .ramGraph = "<svg id=r/>", .head = "head &room", .headColor = "#08F79B" });
    try std.testing.expectEqualStrings("<div class=gwell title=\"p\"><div class=glegend><b>c</b></div><svg id=c/>" ++
        "<div class=glegend><b>r</b></div><svg id=r/><div class=glegend><span style=\"color:#08F79B\">head &amp;room</span>" ++
        "</div></div>", h.b.items);
}

test "full view section frame" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .title = "Live", .sub = "S", .statusTitle = "Status", .decksTitle = "Decks" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<section class=sec><h2 class=sec-title>Status</h2><div id=live-status>") != null);
    try std.testing.expect(std.mem.endsWith(u8, h.b.items, "<div id=live-strip class=livestrip><span></span><span></span><span></span></div>"));
}
