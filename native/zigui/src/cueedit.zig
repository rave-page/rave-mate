//! Cue-editor subview renderers (Go render_library_cueedit.go): the `#ce-topbar` readout
//! strip, the full-width wave strip (topbar + player) and the editor rail inside
//! `#lib-detail`. Markup is byte-exact with the Go originals (golden-gated).
//!
//! Trusted raw pass-throughs, emitted verbatim exactly where Go inserts them unescaped:
//!   - `player` — player.go mpHTML: the 30 fps `__rt` playhead surface. Every id/data-*
//!     the client rAF runtime touches, and all of its float math (`%.2f` coords, waveform
//!     SVG), stays Go-side. NEVER re-implement it here.
//!   - `prepSel` — library_prep.go prep-playlist picker
//!   - `writeBack` — library_cuewrite.go ceWriteHTML / library_remotecue.go rceSaveHTML
//!   - `tip` — tooltip.go tipTopic("cue-edit")
//!   - `cursor` / `barBeat` / `when` / `tag` — pre-formatted Go-side (pubClock, ceBarBeat,
//!     "DROP <n>"); floats never cross the ABI, and `act` carries the Go `%f` ms.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");
const k = @import("library_kit.zig");

/// TbDrop is one clickable drop chip in the topbar.
pub const TbDrop = struct {
    act: []const u8 = "",
    lbl: []const u8 = "",
    when: []const u8 = "",
};

/// Topbar mirrors Go ceTopbarSt. show=false ⇒ "" (renderJSON then returns NULL and the Go
/// fallback renders the same empty string).
pub const Topbar = struct {
    show: bool = false,
    eyebrow: []const u8 = "",
    title: []const u8 = "",

    hasRce: bool = false,
    rceMeta: []const u8 = "",
    dirty: bool = false,
    dirtyTip: []const u8 = "",

    meta: []const u8 = "",
    cursor: []const u8 = "",
    barLbl: []const u8 = "",
    barBeat: []const u8 = "",
    jump: []const u8 = "",

    drops: []const TbDrop = &.{},
    census: []const u8 = "",

    noTag: bool = false,
    noTagTip: []const u8 = "",

    verified: bool = false,
    verifiable: bool = false,
    verifyAct: []const u8 = "",
    verifiedTip: []const u8 = "",
    verifiedLbl: []const u8 = "",
    verifyTip: []const u8 = "",
    verifyLbl: []const u8 = "",
    tip: []const u8 = "", // legacy raw (bridge)
    tipSt: ?c.Tip = null, // structured tooltip — wins over tip
    close: c.Btn = .{},
};

/// Wave mirrors Go ceWaveSt: the topbar wrapper + the raw player strip.
pub const Wave = struct {
    topbar: Topbar = .{},
    player: []const u8 = "",
};

/// Defaults mirrors Go ceDefaultsSt (collapsible per-mode defaults).
pub const Defaults = struct {
    arrow: []const u8 = "",
    title: []const u8 = "",
    open: bool = false,
    pads: c.Select = .{},
    ow: c.Toggle = .{},
    split: c.Toggle = .{},

    hasPromote: bool = false,
    promote: c.Toggle = .{},
    hasGrid: bool = false,
    grid: c.Toggle = .{},
    note: []const u8 = "",
};

/// ARow mirrors Go ceARowSt (one drop→pattern assign row).
pub const ARow = struct {
    placed: bool = false,
    tag: []const u8 = "",
    act: []const u8 = "",
    when: []const u8 = "",
    unplacedTip: []const u8 = "",
    unplacedLbl: []const u8 = "",
    hasSel: bool = false,
    sel: c.Select = .{},
};

/// Assign mirrors Go ceAssignSt.
pub const Assign = struct {
    title: []const u8 = "",
    rows: []const ARow = &.{},
    showNoDrops: bool = false,
    noDropsHint: []const u8 = "",
};

/// Batch mirrors Go ceBatchSt (the checked-rows block; hidden in rce mode).
pub const Batch = struct {
    show: bool = false,
    header: []const u8 = "",
    applyHot: c.Btn = .{},
    applyMem: c.Btn = .{},
    promoteSel: c.Btn = .{},
    convertSel: c.Btn = .{},
    clearSel: c.Btn = .{},
    note: []const u8 = "",
};

/// Rail mirrors Go ceRailSt (the `#lib-detail` inner in cue-edit mode).
pub const Rail = struct {
    show: bool = false,
    eyebrow: []const u8 = "",
    title: []const u8 = "",

    mode: c.Select = .{},
    defaults: Defaults = .{},
    prepSel: []const u8 = "",
    prepHint: []const u8 = "",
    assign: Assign = .{},
    addDrop: c.Btn = .{},
    delDrop: c.Btn = .{},

    hasSel: bool = false,
    selLbl: []const u8 = "",
    patNamePh: []const u8 = "",
    savePat: c.Btn = .{},

    hasDsel: bool = false,
    dselLbl: []const u8 = "",
    showDelHint: bool = false,
    delHint: []const u8 = "",

    hasPats: bool = false,
    manage: c.Btn = .{},

    hasDrops: bool = false,
    applyHot: c.Btn = .{},
    applyMem: c.Btn = .{},
    showOwNote: bool = false,
    owNote: []const u8 = "",

    promoteAll: c.Btn = .{},
    convertAll: c.Btn = .{},
    clearOne: c.Btn = .{},

    hints: []const k.Hint = &.{},
    batch: Batch = .{},

    writeBack: []const u8 = "",
    close: c.Btn = .{},
};

/// renderTopbar mirrors Go ceTopbarHTMLOf.
pub fn renderTopbar(h: *Html, st: Topbar) !void {
    if (!st.show) return;
    try h.raw("<div class=ce-topbar><span class=ce-tb-eyebrow>");
    try h.esc(st.eyebrow);
    try h.raw("</span><span class=ce-tb-title>");
    try h.esc(st.title);
    try h.raw("</span>");
    if (st.hasRce) { // remote session: whose track + unsaved marker
        try h.raw("<span class=ce-tb-meta>");
        try h.esc(st.rceMeta);
        try h.raw("</span>");
        if (st.dirty) {
            try h.raw("<span class=ce-tb-warn title=");
            try h.attrQ(st.dirtyTip);
            try h.raw(">●</span>");
        }
    }
    if (st.meta.len != 0) {
        try h.raw("<span class=ce-tb-meta>");
        try h.esc(st.meta);
        try h.raw("</span>");
    }
    try h.raw("<span class=ce-tb-cursor>▸ ");
    try h.raw(st.cursor); // pubClock, Go-formatted + spliced raw
    try h.raw(" · ");
    try h.esc(st.barLbl);
    try h.raw(" ");
    try h.raw(st.barBeat); // ceBarBeat, Go-formatted + spliced raw
    try h.raw("</span><span class=ce-jump>");
    try h.esc(st.jump);
    try h.raw("</span>");
    for (st.drops) |d| {
        try h.raw("<span class=ce-tb-drop data-act=");
        try h.attrQ(d.act);
        try h.raw(">D");
        try h.raw(d.lbl);
        try h.raw(" ");
        try h.raw(d.when);
        try h.raw("</span>");
    }
    try h.raw("<span class=ce-tb-meta>");
    try h.esc(st.census);
    try h.raw("</span>");
    if (st.noTag) {
        try h.raw("<span class=ce-tb-warn title=");
        try h.attrQ(st.noTagTip);
        try h.raw(">⚠</span>");
    }
    // verified-grid chip: mint ✓ when verified (click = unmark), outline "Mark verified" when
    // eligible. Verified locks grid nudging, so the chip doubles as the toggle for it.
    if (st.verified) {
        try h.raw("<span class=ce-tb-verified title=");
        try h.attrQ(st.verifiedTip);
        try h.raw(" data-act=");
        try h.attrQ(st.verifyAct);
        try h.raw(">✓ ");
        try h.esc(st.verifiedLbl);
        try h.raw("</span>");
    } else if (st.verifiable) {
        try h.raw("<span class=ce-tb-verify title=");
        try h.attrQ(st.verifyTip);
        try h.raw(" data-act=");
        try h.attrQ(st.verifyAct);
        try h.raw(">");
        try h.esc(st.verifyLbl);
        try h.raw("</span>");
    }
    try h.raw("<span class=ce-tb-spacer></span>");
    try c.tipOr(h, st.tipSt, st.tip);
    try c.btnOf(h, st.close);
    try h.raw("</div>");
}

/// renderWave mirrors Go ceWaveHTMLOf: the topbar wrapper + the raw player strip.
pub fn renderWave(h: *Html, st: Wave) !void {
    try h.raw("<div id=ce-topbar>");
    try renderTopbar(h, st.topbar);
    try h.raw("</div>");
    try h.raw(st.player);
}

/// renderDefaults mirrors Go ceDefaultsHTMLOf.
fn renderDefaults(h: *Html, st: Defaults) !void {
    try h.raw("<div class=\"pb-label ce-prefs-hd\" data-act=ce-prefs-tgl>");
    try h.raw(st.arrow); // ▸/▾ literal, raw like Go
    try h.raw(" ");
    try h.esc(st.title);
    try h.raw("</div>");
    if (!st.open) return;
    try c.selectBox(h, st.pads);
    try c.toggleOf(h, st.ow);
    try c.toggleOf(h, st.split);
    if (st.hasPromote) try c.toggleOf(h, st.promote);
    if (st.hasGrid) try c.toggleOf(h, st.grid);
    try h.raw("<div class=set-note>");
    try h.esc(st.note);
    try h.raw("</div>");
}

/// renderAssign mirrors Go ceAssignGridHTMLOf.
fn renderAssign(h: *Html, st: Assign) !void {
    try h.raw("<div class=pb-label>");
    try h.esc(st.title);
    try h.raw("</div><div class=ce-agrid>");
    for (st.rows) |r| {
        try h.raw("<div class=\"ce-arow");
        if (!r.placed) try h.raw(" unplaced");
        try h.raw("\">");
        if (r.placed) {
            try h.raw("<span class=ce-arow-tag data-act=");
            try h.attrQ(r.act);
            try h.raw(">");
            try h.raw(r.tag);
            try h.raw("</span><span class=ce-arow-when>");
            try h.raw(r.when);
            try h.raw("</span>");
        } else {
            try h.raw("<span class=ce-arow-tag>");
            try h.raw(r.tag);
            try h.raw("</span><span class=\"ce-arow-when unplaced\" title=");
            try h.attrQ(r.unplacedTip);
            try h.raw(">");
            try h.esc(r.unplacedLbl);
            try h.raw("</span>");
        }
        if (r.hasSel) try c.selectBox(h, r.sel);
        try h.raw("</div>");
    }
    try h.raw("</div>");
    if (st.showNoDrops) {
        try h.raw("<div class=set-note>");
        try h.esc(st.noDropsHint);
        try h.raw("</div>");
    }
}

/// btnRow1/btnRow2 mirror Go btnRow with one/two buttons.
fn btnRow1(h: *Html, b: c.Btn) !void {
    try c.btnRowOpen(h);
    try c.btnOf(h, b);
    try c.btnRowClose(h);
}

fn btnRow2(h: *Html, a: c.Btn, b: c.Btn) !void {
    try c.btnRowOpen(h);
    try c.btnOf(h, a);
    try c.btnOf(h, b);
    try c.btnRowClose(h);
}

/// btnCol2 mirrors Go `<div class=btn-col>` + two buttons.
fn btnCol2(h: *Html, a: c.Btn, b: c.Btn) !void {
    try h.raw("<div class=btn-col>");
    try c.btnOf(h, a);
    try c.btnOf(h, b);
    try h.raw("</div>");
}

fn pbLabel(h: *Html, text: []const u8) !void {
    try h.raw("<div class=pb-label>");
    try h.esc(text);
    try h.raw("</div>");
}

fn setNote(h: *Html, text: []const u8) !void {
    try h.raw("<div class=set-note>");
    try h.esc(text);
    try h.raw("</div>");
}

/// renderRail mirrors Go ceRailHTMLOf.
pub fn renderRail(h: *Html, st: Rail) !void {
    if (!st.show) return;
    try h.raw("<div class=insp-hd><div class=insp-eyebrow>");
    try h.esc(st.eyebrow);
    try h.raw("</div><div class=insp-title>");
    try h.esc(st.title);
    try h.raw("</div></div>");

    // software mode: scopes new cues + apply/promote/write to one DJ app ("" = all)
    try c.selectBox(h, st.mode);
    try renderDefaults(h, st.defaults);

    // preparation playlist: P adds the open track, holding P removes it again
    try h.raw(st.prepSel);
    try setNote(h, st.prepHint);

    // drops → pattern assign grid (fixed rows drop 1-4 + X; unplaced rows still show)
    try renderAssign(h, st.assign);
    try btnRow2(h, st.addDrop, st.delDrop);

    // selection → pattern (cues) / delete (cues + drops)
    if (st.hasSel) {
        try pbLabel(h, st.selLbl);
        try h.raw("<div class=lib-toolbar>");
        try k.fieldRaw(h, "ce-pat-name", "", st.patNamePh);
        try c.btnOf(h, st.savePat);
        try h.raw("</div>");
    }
    if (st.hasDsel) try pbLabel(h, st.dselLbl);
    if (st.showDelHint) try setNote(h, st.delHint);
    if (st.hasPats) try btnRow1(h, st.manage);

    // apply
    if (st.hasDrops) {
        try btnCol2(h, st.applyHot, st.applyMem);
        if (st.showOwNote) try setNote(h, st.owNote);
    }
    try btnRow2(h, st.promoteAll, st.convertAll);
    try btnRow1(h, st.clearOne);
    try k.hints(h, st.hints);

    // batch: every action below runs over the CHECKED collection rows
    if (st.batch.show) {
        try pbLabel(h, st.batch.header);
        try btnCol2(h, st.batch.applyHot, st.batch.applyMem);
        try btnRow2(h, st.batch.promoteSel, st.batch.convertSel);
        try btnRow1(h, st.batch.clearSel);
        try setNote(h, st.batch.note);
    }

    try h.raw(st.writeBack);
    try btnRow1(h, st.close);
}

test "topbar off renders nothing" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderTopbar(&h, .{});
    try std.testing.expectEqualStrings("", h.b.items);
}

test "topbar readouts + drop chips" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const drops = [_]TbDrop{.{ .act = "ce-goto:1500.000000", .lbl = "1", .when = "0:01" }};
    try renderTopbar(&h, .{
        .show = true,
        .eyebrow = "Cue prep",
        .title = "A & B - T",
        .meta = "128.0 BPM · 8A",
        .cursor = "1:23",
        .barLbl = "Bar",
        .barBeat = "5.3",
        .jump = "Jump 4",
        .drops = &drops,
        .census = "3 cues",
        .noTag = true,
        .noTagTip = "no tag",
        .close = .{ .label = "✕ Close", .variant = "ghost", .act = "ce-close" },
    });
    try std.testing.expectEqualStrings("<div class=ce-topbar>" ++
        "<span class=ce-tb-eyebrow>Cue prep</span>" ++
        "<span class=ce-tb-title>A &amp; B - T</span>" ++
        "<span class=ce-tb-meta>128.0 BPM · 8A</span>" ++
        "<span class=ce-tb-cursor>▸ 1:23 · Bar 5.3</span>" ++
        "<span class=ce-jump>Jump 4</span>" ++
        "<span class=ce-tb-drop data-act=\"ce-goto:1500.000000\">D1 0:01</span>" ++
        "<span class=ce-tb-meta>3 cues</span>" ++
        "<span class=ce-tb-warn title=\"no tag\">⚠</span>" ++
        "<span class=ce-tb-spacer></span>" ++
        "<button class=\"rp-btn rp-btn--ghost\" data-act=\"ce-close\">✕ Close</button></div>", h.b.items);
}

test "wave wraps the topbar and passes the player through raw" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderWave(&h, .{ .player = "<div id=mp-library><canvas></canvas></div>" });
    try std.testing.expectEqualStrings("<div id=ce-topbar></div><div id=mp-library><canvas></canvas></div>", h.b.items);
}

test "assign grid unplaced row" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const rows = [_]ARow{.{ .tag = "DROP X", .unplacedTip = "tip", .unplacedLbl = "unplaced" }};
    try renderAssign(&h, .{ .title = "Patterns", .rows = &rows, .showNoDrops = true, .noDropsHint = "add a drop" });
    try std.testing.expectEqualStrings("<div class=pb-label>Patterns</div><div class=ce-agrid>" ++
        "<div class=\"ce-arow unplaced\"><span class=ce-arow-tag>DROP X</span>" ++
        "<span class=\"ce-arow-when unplaced\" title=\"tip\">unplaced</span></div></div>" ++
        "<div class=set-note>add a drop</div>", h.b.items);
}

test "rail off renders nothing" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderRail(&h, .{});
    try std.testing.expectEqualStrings("", h.b.items);
}
