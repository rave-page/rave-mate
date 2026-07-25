//! Publish view renderers — byte-exact ports of internal/webui/render_publish.go
//! (local recording/publishing cockpit) and render_publish_remote.go (the peer's
//! recorded-sets browser). State arrives fully resolved from Go: every number is
//! already a string (pubClock offsets, progressBar "%.1f%%" percentages, row indices
//! cross as integers), every i18n string is resolved, and the markup owned by other
//! subsystems rides through as trusted RAW HTML — the unified player/editor
//! (player.go mpHTML) and the peer target switcher (targetSwitcherHTML).
//! This file owns the STRUCTURE: which elements appear, the conditionals, escaping.
//! Golden gate: internal/webui/zigui_golden_publish_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

// ── local Publish ───────────────────────────────────────────────────────────────

/// Badge is one hero badge (REC/CAPTURE/OBS); dl = Go strings.ToLower(key).
pub const Badge = struct {
    key: []const u8 = "",
    dl: []const u8 = "",
    variant: []const u8 = "",
    line: []const u8 = "",
};

/// Bar is a resolved progressBar: pct = Go "%.1f%%" of the clamped fraction.
pub const Bar = struct {
    show: bool = false,
    pct: []const u8 = "",
    cap: []const u8 = "",
};

pub const Np = struct {
    label: []const u8 = "",
    title: []const u8 = "",
    meta: []const u8 = "",
    state: []const u8 = "",
    bar: Bar = .{},
};

pub const Player = struct {
    show: bool = false,
    label: []const u8 = "",
    pos: []const u8 = "",
    bar: Bar = .{},
};

pub const Hero = struct {
    show: bool = false,
    rec: Badge = .{},
    cap: Badge = .{},
    obs: Badge = .{},
    finish: []const u8 = "", // "" = no live set → no Finish-set button
    np: Np = .{},
    player: Player = .{},
};

pub const SetRow = struct {
    id: []const u8 = "",
    title: []const u8 = "",
    sub: []const u8 = "",
    sel: bool = false,
    rename: []const u8 = "",
};

pub const List = struct {
    empty: []const u8 = "",
    count: []const u8 = "",
    rows: []const SetRow = &.{},
};

pub const Cap = struct {
    caption: []const u8 = "",
    btns: []const c.Btn = &.{},
    menu: c.Select = .{},
};

pub const Loose = struct {
    count: []const u8 = "",
    desc: []const u8 = "",
    caps: []const Cap = &.{},
};

pub const Captures = struct {
    player: []const u8 = "", // RAW: player.go mpHTML
    empty: []const u8 = "",
    caps: []const Cap = &.{},
};

/// Track is one tracklist row. lead ∈ resolving|none|chk; ctx = the data-ctx value
/// ("" = no context menu); offAct/offDl only used on editable (finished-set) rows.
pub const Track = struct {
    num: i64 = 0,
    label: []const u8 = "",
    off: []const u8 = "",
    lead: []const u8 = "",
    leadTip: []const u8 = "",
    checked: bool = false,
    path: []const u8 = "",
    ctx: []const u8 = "",
    offAct: []const u8 = "",
    offDl: []const u8 = "",
};

pub const Batch = struct {
    count: []const u8 = "", // "" = no batch bar
    btns: []const c.Btn = &.{},
};

pub const Tracklist = struct {
    empty: []const u8 = "",
    resolving: []const u8 = "", // "" = library links resolved
    editable: bool = false,
    offTip: []const u8 = "",
    rows: []const Track = &.{},
    showFix: bool = false,
    fix: c.Btn = .{},
    help: []const u8 = "",
    unres: []const u8 = "",
    batch: Batch = .{},
};

pub const Detail = struct {
    cardTitle: []const u8 = "",
    sel: bool = false,
    hint: []const u8 = "",
    player: []const u8 = "", // RAW: pinned loose-capture player
    loose: Loose = .{},

    name: []const u8 = "",
    meta: []const u8 = "",
    actions: []const c.Btn = &.{},
    active: []const u8 = "",
    capsLbl: []const u8 = "",
    tracksLbl: []const u8 = "",
    captures: Captures = .{},
    tracklist: Tracklist = .{},
};

pub const Body = struct {
    hero: Hero = .{},
    list: List = .{},
    detail: Detail = .{},
};

pub const State = struct {
    title: []const u8 = "",
    sub: []const u8 = "",
    switcher: []const u8 = "", // RAW: targetSwitcherHTML
    available: bool = false,
    unavailable: []const u8 = "",
    body: Body = .{},
};

/// render mirrors Go publishHTML.
pub fn render(h: *Html, st: State) !void {
    if (!st.available) {
        try c.panel(h, st.title, "");
        try h.raw(st.switcher);
        try c.emptyState(h, st.unavailable);
        return;
    }
    try c.panel(h, st.title, st.sub);
    try h.raw(st.switcher);
    try h.raw("<div id=publish-body>");
    try renderBody(h, st.body);
    try h.raw("</div>");
}

/// renderBody mirrors Go publishBodyHTML (#publish-body inner).
pub fn renderBody(h: *Html, st: Body) !void {
    try h.raw("<div id=pub-hero>");
    try renderHero(h, st.hero);
    try h.raw("</div>");
    try c.mdOpen(h);
    try renderList(h, st.list);
    try c.mdSplit(h);
    try renderDetail(h, st.detail);
    try c.mdClose(h);
}

/// renderHero mirrors Go pubHeroHTML (#pub-hero inner, ~1 Hz tick patch).
pub fn renderHero(h: *Html, st: Hero) !void {
    if (!st.show) return;
    try h.raw("<div class=\"rp-card pub-hero\"><div class=pub-badges>");
    try renderBadge(h, st.rec);
    try renderBadge(h, st.cap);
    try renderBadge(h, st.obs);
    try h.raw("</div>");
    if (st.finish.len != 0) {
        try c.btnRowOpen(h);
        try c.btn(h, st.finish, "destructive", "rec-finish", "");
        try c.btnRowClose(h);
    }
    try renderNp(h, st.np);
    try renderPlayer(h, st.player);
    try h.raw("</div>");
}

fn renderBadge(h: *Html, b: Badge) !void {
    try h.raw("<div class=pub-badge>");
    try c.dot(h, b.variant);
    try h.raw("<div class=pub-badge-tx><div class=pub-badge-k data-label=");
    try h.attrQ(b.dl);
    try h.raw(">");
    try h.esc(b.key);
    try h.raw("</div><div class=pub-badge-v data-value=");
    try h.attrQ(b.line);
    try h.raw(">");
    try h.esc(b.line);
    try h.raw("</div></div></div>");
}

fn renderNp(h: *Html, st: Np) !void {
    try h.raw("<div class=pub-np><div class=card-label>");
    try h.esc(st.label);
    try h.raw("</div><div class=pub-np-t data-label=\"now playing\" data-value=\"");
    try h.esc(st.title);
    try h.raw("\">");
    try h.esc(st.title);
    try h.raw("</div>");
    if (st.meta.len != 0) {
        try h.raw("<div class=np-artist>");
        try h.esc(st.meta);
        try h.raw("</div>");
    }
    if (st.state.len != 0) {
        try h.raw("<div class=np-artist>");
        try h.esc(st.state);
        try h.raw("</div>");
    }
    if (st.bar.show) {
        try h.raw("<div style=\"margin-top:8px\">");
        try c.progressBar(h, st.bar.pct, st.bar.cap);
        try h.raw("</div>");
    }
    try h.raw("</div>");
}

fn renderPlayer(h: *Html, st: Player) !void {
    if (!st.show) return;
    try h.raw("<div class=pub-player><div class=pub-player-l>");
    try h.esc(st.label);
    try h.raw(" <span class=np-artist>");
    try h.esc(st.pos);
    try h.raw("</span></div>");
    try c.progressBar(h, st.bar.pct, st.bar.cap);
    try h.raw("</div>");
}

fn renderList(h: *Html, st: List) !void {
    if (st.rows.len == 0) return c.emptyState(h, st.empty);
    try h.raw("<div class=card-label>");
    try h.esc(st.count);
    try h.raw("</div>");
    for (st.rows) |r| {
        try h.raw("<div class=\"irow pub-setrow");
        if (r.sel) try h.raw(" selected");
        try h.raw("\" data-act=\"pub-select:");
        try h.esc(r.id);
        try h.raw("\"><div class=irow-main><div class=irow-title>");
        try h.esc(r.title);
        try h.raw("</div><div class=irow-sub>");
        try h.esc(r.sub);
        try h.raw("</div></div><div class=irow-actions>");
        try c.btnAct(h, r.rename, "ghost", "pub-rename:", r.id);
        try h.raw("</div></div>");
    }
}

fn renderDetail(h: *Html, st: Detail) !void {
    try c.cardOpen(h, st.cardTitle, true);
    try c.cardHeadClose(h);
    if (!st.sel) {
        try c.hint(h, "info", st.hint);
        try h.raw(st.player);
        try renderLoose(h, st.loose);
        return c.cardClose(h);
    }
    try h.raw("<div class=pub-detail-h><div class=pub-detail-name>");
    try h.esc(st.name);
    try h.raw("</div><div class=np-artist>");
    try h.esc(st.meta);
    try h.raw("</div>");
    try c.btnRowOf(h, st.actions);
    try h.raw("</div>");
    const tabs = [_]c.Tab{
        .{ .val = "captures", .label = st.capsLbl },
        .{ .val = "tracklist", .label = st.tracksLbl },
    };
    try c.subTabs(h, "pub-tab:", st.active, &tabs);
    try h.raw("<div class=pub-subbody>");
    if (std.mem.eql(u8, st.active, "tracklist")) {
        try renderTracklist(h, st.tracklist);
    } else {
        try renderCaptures(h, st.captures);
        try renderLoose(h, st.loose);
    }
    try h.raw("</div>");
    try c.cardClose(h);
}

fn renderTracklist(h: *Html, st: Tracklist) !void {
    if (st.rows.len == 0) return c.hint(h, "info", st.empty);
    if (st.resolving.len != 0) try c.hint(h, "info", st.resolving);
    try h.raw("<div class=pub-tracklist>");
    for (st.rows) |row| {
        try h.raw("<div class=pub-track");
        if (row.ctx.len != 0) {
            try h.raw(" data-ctx=");
            try h.attrQ(row.ctx);
        }
        try h.raw(">");
        if (std.mem.eql(u8, row.lead, "resolving") or std.mem.eql(u8, row.lead, "none")) {
            try h.raw("<span class=\"pub-track-chk none\" title=");
            try h.attrQ(row.leadTip);
            try h.raw(">");
            try h.raw(if (std.mem.eql(u8, row.lead, "none")) "·" else "…");
            try h.raw("</span>");
        } else {
            try h.raw("<span class=pub-track-chk><input type=checkbox data-act=\"pub-tsel:");
            try h.esc(row.path);
            try h.raw("\"");
            if (row.checked) try h.raw(" checked");
            try h.raw("></span>");
        }
        try h.raw("<span class=pub-track-n>");
        try c.num(h, row.num);
        try h.raw(".</span>");
        if (st.editable) {
            // Go inserts the offset raw in the read-only cell but attrQ's it here.
            try h.raw("<input class=pub-track-oin type=text value=");
            try h.attrQ(row.off);
            try h.raw(" data-value=");
            try h.attrQ(row.off);
            try h.raw(" data-act=");
            try h.attrQ(row.offAct);
            try h.raw(" data-label=");
            try h.attrQ(row.offDl);
            try h.raw(" title=");
            try h.attrQ(st.offTip);
            try h.raw(">");
        } else {
            try h.raw("<span class=pub-track-o>[");
            try h.raw(row.off);
            try h.raw("]</span>");
        }
        try h.raw("<span class=pub-track-l>");
        try h.esc(row.label);
        try h.raw("</span></div>");
    }
    try h.raw("</div>");
    if (st.showFix) {
        try c.btnRowOpen(h);
        try c.btnOf(h, st.fix);
        try c.btnRowClose(h);
    }
    try h.raw("<p class=page-sub>");
    try h.esc(st.help);
    try h.raw("</p>");
    if (st.unres.len != 0) {
        try h.raw("<p class=page-sub>");
        try h.esc(st.unres);
        try h.raw("</p>");
    }
    if (st.batch.count.len != 0) {
        try h.raw("<div class=batchbar><span class=cnt>");
        try h.esc(st.batch.count);
        try h.raw("</span>");
        for (st.batch.btns) |b| try c.btnOf(h, b);
        try h.raw("</div>");
    }
}

fn renderCaptures(h: *Html, st: Captures) !void {
    try h.raw(st.player);
    if (st.caps.len == 0) return c.hint(h, "info", st.empty);
    for (st.caps) |s| try renderCap(h, s);
}

fn renderLoose(h: *Html, st: Loose) !void {
    if (st.caps.len == 0) return;
    try h.raw("<div class=pub-loose><div class=card-label>");
    try h.esc(st.count);
    try h.raw("</div><div class=np-artist>");
    try h.esc(st.desc);
    try h.raw("</div>");
    for (st.caps) |s| try renderCap(h, s);
    try h.raw("</div>");
}

fn renderCap(h: *Html, st: Cap) !void {
    try h.raw("<div class=pub-cap><div class=pub-cap-cap>");
    try h.esc(st.caption);
    try h.raw("</div>");
    try c.btnRowOpen(h);
    for (st.btns) |b| try c.btnOf(h, b);
    try c.actionMenu(h, st.menu);
    try c.btnRowClose(h);
    try h.raw("</div>");
}

test "hero badges + now-playing" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderHero(&h, .{
        .show = true,
        .rec = .{ .key = "REC", .dl = "rec", .variant = "error", .line = "Live \"set\"" },
        .cap = .{ .key = "CAPTURE", .dl = "capture", .variant = "muted", .line = "Off" },
        .obs = .{ .key = "OBS", .dl = "obs", .variant = "warning", .line = "Not connected" },
        .finish = "Finish set",
        .np = .{ .label = "Now playing", .title = "A & B", .meta = "Deck A", .state = "confirming", .bar = .{ .show = true, .pct = "42.0%", .cap = "in 3s" } },
    });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-label=\"rec\">REC</div>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-value=\"Live &#34;set&#34;\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "rp-btn--destructive\" data-act=\"rec-finish\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=pbar><div class=pbar-fill style=\"width:42.0%\">") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-label=\"now playing\" data-value=\"A &amp; B\"") != null);
}

test "tracklist lead variants + editable offsets" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const rows = [_]Track{
        .{ .num = 1, .label = "a", .off = "0:00", .lead = "chk", .checked = true, .path = "C:\\a.flac", .ctx = "pub-tctx:C:\\a.flac" },
        .{ .num = 2, .label = "b", .off = "3:20", .lead = "none", .leadTip = "no match" },
        .{ .num = 3, .label = "c", .off = "9:59", .lead = "resolving", .leadTip = "linking…" },
    };
    try renderTracklist(&h, .{ .rows = &rows, .help = "help" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-act=\"pub-tsel:C:\\a.flac\" checked>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "title=\"no match\">·</span>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "title=\"linking…\">…</span>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<span class=pub-track-o>[0:00]</span>") != null);

    h.b.clearRetainingCapacity();
    const one = [_]Track{.{ .num = 1, .label = "a", .off = "1:02", .lead = "chk", .path = "p", .offAct = "pub-toff:r\x1f0", .offDl = "offset-1" }};
    try renderTracklist(&h, .{ .rows = &one, .editable = true, .offTip = "tip", .help = "h" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<input class=pub-track-oin type=text value=\"1:02\" data-value=\"1:02\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-label=\"offset-1\" title=\"tip\">") != null);
}
