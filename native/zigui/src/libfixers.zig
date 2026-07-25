//! Library fixer/section SUBVIEWS (Zig migration wave 3): the five seams the Library tab used
//! to embed as pre-rendered markup - nav rail, beatgrid-fixer rail + results, tag-fixer results
//! + editor, prep-playlist picker, "works well together" section.
//!
//! Byte-identical to the pure Go renderers in internal/webui/render_library_fixers.go
//! (golden gate: zigui_golden_libfixers_test.go + the whole-tab library goldens).
//!
//! Contract notes worth keeping straight:
//!   - nav-row icons are Go SOURCE LITERALS (glyphs) spliced UNESCAPED, like Go navIt;
//!   - GFStat.n is ESCAPED but GFTile.n is RAW - the two Go helpers differ on purpose
//!     (gfStat escapes its string, gfTile splices fmt.Sprint(int)); do not unify them;
//!   - a batch-result row's status token (FIX/OK/SKIP/ERR) is spliced raw into both the chip
//!     class (pre-lowered Go-side: stLow) and the chip text;
//!   - every number (progress fill, BPM/ms deltas, counters) arrives pre-formatted from Go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");
const k = @import("library_kit.zig");

// ── nav rail ──

/// NavRow is one rail row: a group header (hd) or a clickable item.
pub const NavRow = struct {
    hd: bool = false,
    label: []const u8 = "",
    act: []const u8 = "",
    icon: []const u8 = "",
    count: []const u8 = "",
    on: bool = false,
};

pub const Nav = struct {
    rows: []const NavRow = &.{},
};

fn navHd(h: *Html, label: []const u8) !void {
    try h.raw("<div class=libnav-hd>");
    try h.esc(label);
    try h.raw("</div>");
}

fn navIt(h: *Html, r: NavRow) !void {
    try h.raw("<div class=\"libnav-it");
    if (r.on) try h.raw(" on");
    try h.raw("\" data-act=\"");
    try h.esc(r.act);
    try h.raw("\"><span class=libnav-ic>");
    try h.raw(r.icon); // glyph literal, unescaped (Go parity)
    try h.raw("</span><span class=libnav-t>");
    try h.esc(r.label);
    try h.raw("</span>");
    if (r.count.len != 0) {
        try h.raw("<span class=libnav-n>");
        try h.esc(r.count);
        try h.raw("</span>");
    }
    try h.raw("</div>");
}

pub fn renderNav(h: *Html, st: Nav) !void {
    try h.raw("<div class=libnav>");
    for (st.rows) |r| {
        if (r.hd) {
            try navHd(h, r.label);
            continue;
        }
        try navIt(h, r);
    }
    try h.raw("</div>");
}

// ── prep-playlist picker ──

/// renderPrep is the collection toolbar's P-key target (a bare smart select).
pub fn renderPrep(h: *Html, st: c.Select) !void {
    try c.selectBox(h, st);
}

// ── beatgrid fixer rail ──

/// GFStat is one idle health-card stat (Go gfStat: n is ESCAPED).
pub const GFStat = struct {
    n: []const u8 = "",
    label: []const u8 = "",
    tone: []const u8 = "",
};

/// GFTile is one running/done counter tile (Go gfTile: n is RAW).
pub const GFTile = struct {
    n: []const u8 = "",
    label: []const u8 = "",
    tone: []const u8 = "",
};

/// GFLive is the #gf-live fragment: tiles (batch run) or bar-only (calibration).
pub const GFLive = struct {
    tiles: []const GFTile = &.{},
    pct: []const u8 = "",
    caption: []const u8 = "",
    current: []const u8 = "",
};

pub const GF = struct {
    kind: []const u8 = "",
    eyebrow: []const u8 = "",
    title: []const u8 = "",

    // health
    stats: []const GFStat = &.{},
    note: []const u8 = "",
    noteAfter: bool = false,
    btns: []const c.Btn = &.{},

    // confirm
    confirmNote: []const u8 = "",
    force: c.Toggle = .{},
    forceHint: []const u8 = "",
    scopes: []const c.Btn = &.{},

    // running / cal
    live: GFLive = .{},
    stopLbl: []const u8 = "",

    // done
    tiles: []const GFTile = &.{},
    cachedNote: []const u8 = "",
    hints: []const k.Hint = &.{},
    acts: []const c.Btn = &.{},
    notes: []const []const u8 = &.{},
    applyNote: []const u8 = "",
};

fn gfStat(h: *Html, s: GFStat) !void {
    try h.raw("<div class=\"gf-stat");
    if (s.tone.len != 0) {
        try h.raw(" gf-");
        try h.raw(s.tone);
    }
    try h.raw("\"><div class=gf-n>");
    try h.esc(s.n);
    try h.raw("</div><div class=gf-l>");
    try h.esc(s.label);
    try h.raw("</div></div>");
}

fn gfTile(h: *Html, t: GFTile) !void {
    try h.raw("<div class=\"gf-tile gf-");
    try h.raw(t.tone);
    try h.raw("\"><div class=gf-n>");
    try h.raw(t.n); // pre-formatted int, spliced raw (Go parity)
    try h.raw("</div><div class=gf-l>");
    try h.esc(t.label);
    try h.raw("</div></div>");
}

fn gfTiles(h: *Html, ts: []const GFTile) !void {
    try h.raw("<div class=gf-tiles>");
    for (ts) |t| try gfTile(h, t);
    try h.raw("</div>");
}

fn gfNote(h: *Html, text: []const u8) !void {
    try h.raw("<div class=set-note>");
    try h.esc(text);
    try h.raw("</div>");
}

pub fn renderGFLive(h: *Html, st: GFLive) !void {
    if (st.tiles.len != 0) try gfTiles(h, st.tiles);
    try c.progressBar(h, st.pct, st.caption);
    try h.raw("<div class=gf-current>");
    try h.esc(st.current);
    try h.raw("</div>");
}

pub fn renderGF(h: *Html, st: GF) !void {
    const eql = std.mem.eql;
    try h.raw("<div class=insp-hd><div class=insp-eyebrow>");
    try h.esc(st.eyebrow);
    try h.raw("</div><div class=insp-title>");
    try h.esc(st.title);
    try h.raw("</div></div>");
    if (eql(u8, st.kind, "running")) {
        try h.raw("<div id=gf-live>");
        try renderGFLive(h, st.live);
        try h.raw("</div>");
        try k.btnRowOf1(h, st.stopLbl, "outline", "gf-cancel");
        return;
    }
    if (eql(u8, st.kind, "confirm")) {
        try gfNote(h, st.confirmNote);
        // force re-analyze: override the multi-marker/lock skips + the cache
        try c.toggleOf(h, st.force);
        try gfNote(h, st.forceHint);
        try h.raw("<div class=btn-col>");
        for (st.scopes) |b| try c.btnOf(h, b);
        try h.raw("</div>");
        return;
    }
    if (eql(u8, st.kind, "done")) {
        try gfTiles(h, st.tiles);
        if (st.cachedNote.len != 0) try gfNote(h, st.cachedNote);
        for (st.hints) |x| try c.hint(h, x.tone, x.text);
        try h.raw("<div class=btn-col>");
        for (st.acts) |b| try c.btnOf(h, b);
        try h.raw("</div>");
        for (st.notes) |n| try gfNote(h, n);
        try gfNote(h, st.applyNote);
        return;
    }
    // health: collection at a glance + the fixer entry
    try h.raw("<div class=gf-stats>");
    for (st.stats) |s| try gfStat(h, s);
    try h.raw("</div>");
    if (!st.noteAfter and st.note.len != 0) try gfNote(h, st.note);
    if (st.btns.len != 0) try c.btnRowOf(h, st.btns);
    if (st.noteAfter and st.note.len != 0) try gfNote(h, st.note);
}

// ── fixer results (they replace the collection track list) ──

/// GFResRow is one batch-result row (status token spliced raw into class + text).
pub const GFResRow = struct {
    path: []const u8 = "",
    st: []const u8 = "",
    stLow: []const u8 = "",
    title: []const u8 = "",
    detail: []const u8 = "",
    delta: []const u8 = "",
};

pub const GFRes = struct {
    chips: []const k.Chip = &.{},
    rows: []const GFResRow = &.{},
    isEmpty: bool = false,
    empty: []const u8 = "",
};

/// TFRow is one proposed tag repair (idx spliced raw into the act, like Go).
pub const TFRow = struct {
    idx: []const u8 = "",
    checked: bool = false,
    path: []const u8 = "",
    base: []const u8 = "",
    field: []const u8 = "",
    cur: []const u8 = "",
    proposed: []const u8 = "",
};

pub const TFGrp = struct {
    title: []const u8 = "",
    badge: []const u8 = "",
    allLbl: []const u8 = "",
    allAct: []const u8 = "",
    noneLbl: []const u8 = "",
    noneAct: []const u8 = "",
    desc: []const u8 = "",
    rows: []const TFRow = &.{},
    more: []const u8 = "",
};

pub const TFRes = struct {
    eyebrow: []const u8 = "",
    title: []const u8 = "",
    desc: []const u8 = "",

    scanning: bool = false,
    pct: []const u8 = "",
    scanCap: []const u8 = "",
    closeLbl: []const u8 = "",

    applyLbl: []const u8 = "",
    rescanLbl: []const u8 = "",
    hints: []const k.Hint = &.{},
    skipped: []const u8 = "",
    isEmpty: bool = false,
    empty: []const u8 = "",
    groups: []const TFGrp = &.{},
};

/// Results is one kind + that fixer's outcome view.
pub const Results = struct {
    kind: []const u8 = "",
    gf: GFRes = .{},
    tf: TFRes = .{},
};

pub fn renderResults(h: *Html, st: Results) !void {
    if (std.mem.eql(u8, st.kind, "tf")) return renderTFRes(h, st.tf);
    return renderGFRes(h, st.gf);
}

pub fn renderGFRes(h: *Html, st: GFRes) !void {
    try h.raw("<div class=lib-toolbar>");
    for (st.chips) |ch| try k.chip(h, ch);
    try h.raw("</div><div class=trk-table>");
    for (st.rows) |r| {
        try h.raw("<div class=trk-row data-ctx=\"lib-ctx:");
        try h.esc(r.path);
        try h.raw("\"><span class=\"gf-chip gf-");
        try h.raw(r.stLow);
        try h.raw("\">");
        try h.raw(r.st);
        try h.raw("</span><span class=trk-main data-act=\"lib-track:");
        try h.esc(r.path);
        try h.raw("\"><span class=trk-title>");
        try h.esc(r.title);
        try h.raw("</span><span class=trk-sub>");
        try h.esc(r.detail);
        try h.raw("</span></span><span class=gf-delta>");
        try h.esc(r.delta);
        try h.raw("</span></div>");
    }
    try h.raw("</div>");
    if (st.isEmpty) try c.emptyState(h, st.empty);
}

pub fn renderTFRes(h: *Html, st: TFRes) !void {
    try h.raw("<div class=insp-hd><div class=insp-eyebrow>");
    try h.esc(st.eyebrow);
    try h.raw("</div><div class=insp-title>");
    try h.esc(st.title);
    try h.raw("</div></div>");
    try k.pageSub(h, st.desc);
    if (st.scanning) {
        try c.progressBar(h, st.pct, st.scanCap);
        try k.btnRowOf1(h, st.closeLbl, "ghost", "tf-close");
        return;
    }
    try h.raw("<div class=lib-toolbar>");
    try c.btn(h, st.applyLbl, "primary", "tf-apply", "");
    try c.btn(h, st.rescanLbl, "outline", "lib-tagfix", "");
    try c.btn(h, st.closeLbl, "ghost", "tf-close", "");
    try h.raw("</div>");
    for (st.hints) |x| try c.hint(h, x.tone, x.text);
    if (st.skipped.len != 0) try k.pageSub(h, st.skipped);
    if (st.isEmpty) return c.emptyState(h, st.empty);
    // grouped by kind, stable order
    for (st.groups) |g| {
        try h.raw("<div class=tf-grp><div class=tf-grphead><span class=tf-grptitle>");
        try h.esc(g.title);
        try h.raw("</span>");
        try c.badge(h, g.badge, "secondary");
        try c.btn(h, g.allLbl, "ghost", g.allAct, "");
        try c.btn(h, g.noneLbl, "ghost", g.noneAct, "");
        try h.raw("</div>");
        try k.pageSub(h, g.desc);
        for (g.rows) |r| {
            try h.raw("<label class=tf-row><input type=checkbox data-act=\"tf-sel:");
            try h.raw(r.idx);
            try h.raw("\"");
            if (r.checked) try h.raw(" checked");
            try h.raw("><span class=tf-file title=\"");
            try h.esc(r.path);
            try h.raw("\">");
            try h.esc(r.base);
            try h.raw("</span><span class=tf-field>");
            try h.esc(r.field);
            try h.raw("</span><span class=tf-diff><s>");
            try h.esc(r.cur);
            try h.raw("</s> → <b>");
            try h.esc(r.proposed);
            try h.raw("</b></span></label>");
        }
        if (g.more.len != 0) try k.pageSub(h, g.more);
        try h.raw("</div>");
    }
}

// ── per-track tag editor (inspector Tags tail) ──

pub const TagEdit = struct {
    open: bool = false,
    openLbl: []const u8 = "",
    desc: []const u8 = "",
    fields: []const k.PBField = &.{},
    saveLbl: []const u8 = "",
    cancelLbl: []const u8 = "",
};

pub fn renderTagEdit(h: *Html, st: TagEdit) !void {
    if (!st.open) return k.btnRowOf1(h, st.openLbl, "outline", "tf-edit-open");
    try k.pageSub(h, st.desc);
    try h.raw("<div class=pbuilder>");
    for (st.fields) |f| try k.pbField(h, f);
    try h.raw("</div>");
    try h.raw("<div class=btn-row>");
    try c.btn(h, st.saveLbl, "primary", "tf-edit-save", "");
    try c.btn(h, st.cancelLbl, "ghost", "tf-edit-close", "");
    try h.raw("</div>");
}

// ── "works well together" ──

pub const CompatRow = struct {
    title: []const u8 = "",
    sub: []const u8 = "",
    act: []const u8 = "",
};

pub const Compat = struct {
    isEmpty: bool = false,
    empty: []const u8 = "",
    rows: []const CompatRow = &.{},
    openLbl: []const u8 = "",
    findLbl: []const u8 = "",
    findAct: []const u8 = "",
};

pub fn renderCompat(h: *Html, st: Compat) !void {
    if (st.isEmpty) {
        try k.pageSub(h, st.empty);
    } else {
        for (st.rows) |r| {
            try c.itemRowOpen(h, r.title, r.sub);
            try c.btn(h, st.openLbl, "ghost", r.act, "");
            try c.itemRowClose(h);
        }
    }
    try k.btnRowOf1(h, st.findLbl, "outline", st.findAct);
}

test "nav rail: headers, glyph raw, count, active row" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const rows = [_]NavRow{
        .{ .hd = true, .label = "Collection" },
        .{ .act = "lib-plclear", .icon = "🎧", .label = "All tracks", .count = "23", .on = true },
        .{ .act = "lib-plgoto:7", .icon = "🎵", .label = "W&armup" },
    };
    try renderNav(&h, .{ .rows = &rows });
    try std.testing.expectEqualStrings("<div class=libnav><div class=libnav-hd>Collection</div>" ++
        "<div class=\"libnav-it on\" data-act=\"lib-plclear\"><span class=libnav-ic>🎧</span>" ++
        "<span class=libnav-t>All tracks</span><span class=libnav-n>23</span></div>" ++
        "<div class=\"libnav-it\" data-act=\"lib-plgoto:7\"><span class=libnav-ic>🎵</span>" ++
        "<span class=libnav-t>W&amp;armup</span></div></div>", h.b.items);
}

test "gf stat escapes n, gf tile splices n raw" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try gfStat(&h, .{ .n = "1&2", .label = "Tracks" });
    try std.testing.expectEqualStrings("<div class=\"gf-stat\"><div class=gf-n>1&amp;2</div>" ++
        "<div class=gf-l>Tracks</div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try gfStat(&h, .{ .n = "3", .label = "Verified", .tone = "mint" });
    try std.testing.expectEqualStrings("<div class=\"gf-stat gf-mint\"><div class=gf-n>3</div>" ++
        "<div class=gf-l>Verified</div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try gfTile(&h, .{ .n = "42", .label = "F&IX", .tone = "violet" });
    try std.testing.expectEqualStrings("<div class=\"gf-tile gf-violet\"><div class=gf-n>42</div>" ++
        "<div class=gf-l>F&amp;IX</div></div>", h.b.items);
}

test "gf live: calibration variant drops the tiles" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderGFLive(&h, .{ .pct = "50.0%", .caption = "5 / 10", .current = "a&b.flac" });
    try std.testing.expectEqualStrings("<div class=pbar><div class=pbar-fill style=\"width:50.0%\"></div>" ++
        "<span class=pbar-cap>5 / 10</span></div><div class=gf-current>a&amp;b.flac</div>", h.b.items);
}

test "gf health: note before buttons, or after" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const btns = [_]c.Btn{.{ .label = "Start", .variant = "primary", .act = "gf-open" }};
    try renderGF(&h, .{ .kind = "health", .eyebrow = "E", .title = "T", .note = "N", .btns = &btns });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=set-note>N</div><div class=btn-row>") != null);
    h.b.clearRetainingCapacity();
    try renderGF(&h, .{ .kind = "health", .eyebrow = "E", .title = "T", .note = "N", .noteAfter = true, .btns = &btns });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "</button></div><div class=set-note>N</div>") != null);
}

test "gf results row splices the status token raw" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const rows = [_]GFResRow{.{ .path = "C:\\m\\a&b.flac", .st = "FIX", .stLow = "fix", .title = "T&t", .detail = "d", .delta = "128.00 → 129.00 BPM" }};
    try renderGFRes(&h, .{ .rows = &rows });
    try std.testing.expectEqualStrings("<div class=lib-toolbar></div><div class=trk-table>" ++
        "<div class=trk-row data-ctx=\"lib-ctx:C:\\m\\a&amp;b.flac\">" ++
        "<span class=\"gf-chip gf-fix\">FIX</span>" ++
        "<span class=trk-main data-act=\"lib-track:C:\\m\\a&amp;b.flac\"><span class=trk-title>T&amp;t</span>" ++
        "<span class=trk-sub>d</span></span>" ++
        "<span class=gf-delta>128.00 → 129.00 BPM</span></div></div>", h.b.items);
}

test "tag editor: closed shows only the opener" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderTagEdit(&h, .{ .openLbl = "Edit tags" });
    try std.testing.expectEqualStrings("<div class=btn-row><button class=\"rp-btn rp-btn--outline\" " ++
        "data-act=\"tf-edit-open\">Edit tags</button></div>", h.b.items);
}

test "compat: empty line vs rows, always the find button" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderCompat(&h, .{ .isEmpty = true, .empty = "No marks", .findLbl = "Find", .findAct = "lib-compat-find:p" });
    try std.testing.expectEqualStrings("<p class=page-sub>No marks</p><div class=btn-row>" ++
        "<button class=\"rp-btn rp-btn--outline\" data-act=\"lib-compat-find:p\">Find</button></div>", h.b.items);
    h.b.clearRetainingCapacity();
    const rows = [_]CompatRow{.{ .title = "B", .sub = "Blend", .act = "lib-compat-go:b" }};
    try renderCompat(&h, .{ .rows = &rows, .openLbl = "Open", .findLbl = "Find", .findAct = "a" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=irow-title>B</div><div class=irow-sub>Blend</div>") != null);
}
