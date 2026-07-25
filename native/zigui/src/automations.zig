//! Automations view renderer — byte-exact port of internal/webui/render_automations.go
//! (automationsHTML/autoBodyHTML + the three section renderers). State arrives fully
//! resolved from Go (service data + localized strings + summaries); this file only
//! walks state → markup. Golden gate: internal/webui/zigui_golden_automations_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

/// Labels shared by automation + schedule cards.
pub const Labels = struct {
    enabled: []const u8 = "",
    enabledDl: []const u8 = "", // Go strings.ToLower(enabled)
    run: []const u8 = "",
    schAdd: []const u8 = "",
    edit: []const u8 = "",
    delete: []const u8 = "",
};

pub const Card = struct {
    id: []const u8 = "",
    label: []const u8 = "",
    watchDir: []const u8 = "",
    status: []const u8 = "", // "" = no badge
    statusVar: []const u8 = "",
    chain: []const u8 = "",
    enabled: bool = false,
};

pub const ListState = struct {
    new: []const u8 = "",
    empty: []const u8 = "",
    cards: []const Card = &.{},
};

pub const SchedCard = struct {
    id: []const u8 = "",
    label: []const u8 = "",
    target: []const u8 = "",
    stateText: []const u8 = "",
    stateVar: []const u8 = "",
    trigger: []const u8 = "",
    gates: []const u8 = "",
    lastFired: []const u8 = "",
    warnTone: []const u8 = "", // "" = no warning
    warnText: []const u8 = "",
    enabled: bool = false,
};

pub const SchedsState = struct {
    new: []const u8 = "",
    gated: bool = false, // no automation to target → New disabled, gateWhy is the empty text
    gateWhy: []const u8 = "",
    empty: []const u8 = "",
    cards: []const SchedCard = &.{},
};

pub const RunRow = struct {
    name: []const u8 = "",
    trigger: []const u8 = "",
    status: []const u8 = "",
    variant: []const u8 = "",
};

pub const RunsState = struct {
    empty: []const u8 = "",
    rows: []const RunRow = &.{},
};

pub const Body = struct {
    listTitle: []const u8 = "",
    schedTitle: []const u8 = "",
    runsTitle: []const u8 = "",
    labels: Labels = .{},
    list: ListState = .{},
    scheds: SchedsState = .{},
    runs: RunsState = .{},
};

pub const State = struct {
    title: []const u8 = "",
    sub: []const u8 = "",
    available: bool = false,
    unavailable: []const u8 = "",
    body: Body = .{},
};

/// render mirrors Go automationsHTML (full tab view).
pub fn render(h: *Html, s: State) !void {
    if (!s.available) {
        try c.panel(h, s.title, "");
        try c.emptyState(h, s.unavailable);
        return;
    }
    try c.panel(h, s.title, s.sub);
    try h.raw("<div id=auto-body>");
    try renderBody(h, s.body);
    try h.raw("</div>");
}

/// renderBody mirrors Go autoBodyHTML (#auto-body inner, the version-gated tick patch).
pub fn renderBody(h: *Html, s: Body) !void {
    try c.sectionOpen(h, s.listTitle);
    try renderList(h, s.list, s.labels);
    try c.sectionClose(h);
    try c.sectionOpen(h, s.schedTitle);
    try renderScheds(h, s.scheds, s.labels);
    try c.sectionClose(h);
    try c.sectionOpen(h, s.runsTitle);
    try renderRuns(h, s.runs);
    try c.sectionClose(h);
}

/// toggleAct mirrors Go toggleRowDL(label, dl, prefix+id, on) — act needs the id appended.
fn toggleAct(h: *Html, lb: Labels, prefix: []const u8, id: []const u8, on: bool) !void {
    try h.raw("<label class=row data-label=");
    try h.attrQ(lb.enabledDl);
    try h.raw("><span class=row-label>");
    try h.esc(lb.enabled);
    try h.raw("</span><span class=switch><input type=checkbox");
    if (on) try h.raw(" checked");
    try h.raw(" data-act=\"");
    try h.esc(prefix);
    try h.esc(id);
    try h.raw("\" data-value=");
    try h.attrQ(if (on) "true" else "false");
    try h.raw("><span class=switch-track></span></span></label>");
}

fn renderList(h: *Html, s: ListState, lb: Labels) !void {
    try c.btnRowOpen(h);
    try c.btn(h, s.new, "primary", "auto-new", "");
    try c.btnRowClose(h);
    if (s.cards.len == 0) {
        try c.emptyState(h, s.empty);
        return;
    }
    try h.raw("<div class=grid>");
    for (s.cards) |a| {
        try h.raw("<div class=\"rp-card\"><div class=card-label>");
        try h.esc(a.label);
        try h.raw("</div><div class=np-artist>");
        try h.esc(a.watchDir);
        try h.raw("</div><div class=np-meta>");
        if (a.status.len != 0) try c.badge(h, a.status, a.statusVar);
        try h.raw("</div><div class=np-meta>");
        try h.esc(a.chain);
        try h.raw("</div>");
        try toggleAct(h, lb, "auto-toggle:", a.id, a.enabled);
        try c.btnRowOpen(h);
        try c.btnAct(h, lb.run, "go", "auto-run:", a.id);
        try c.btnAct(h, lb.schAdd, "outline", "auto-sch-add:", a.id);
        try c.btnAct(h, lb.edit, "outline", "auto-edit:", a.id);
        try c.btnAct(h, lb.delete, "destructive", "auto-del:", a.id);
        try c.btnRowClose(h);
        try h.raw("</div>");
    }
    try h.raw("</div>");
}

fn renderScheds(h: *Html, s: SchedsState, lb: Labels) !void {
    try c.btnRowOpen(h);
    if (s.gated) try c.btnGated(h, s.new, s.gateWhy) else try c.btn(h, s.new, "primary", "auto-sch-new", "");
    try c.btnRowClose(h);
    if (s.cards.len == 0) {
        try c.emptyState(h, if (s.gated) s.gateWhy else s.empty);
        return;
    }
    try h.raw("<div class=grid>");
    for (s.cards) |sc| {
        try h.raw("<div class=\"rp-card\"><div class=card-label>");
        try h.esc(sc.label);
        try h.raw("</div><div class=np-artist>");
        try h.esc(sc.target);
        try h.raw("</div><div class=np-meta>");
        try c.badge(h, sc.stateText, sc.stateVar);
        try h.raw(" ");
        try h.esc(sc.trigger);
        try h.raw("</div><div class=np-meta>");
        try h.esc(sc.gates);
        try h.raw("</div><div class=np-meta>");
        try h.esc(sc.lastFired);
        try h.raw("</div>");
        if (sc.warnTone.len != 0) try c.hint(h, sc.warnTone, sc.warnText);
        try toggleAct(h, lb, "auto-sch-tgl:", sc.id, sc.enabled);
        try c.btnRowOpen(h);
        try c.btnAct(h, lb.edit, "outline", "auto-sch-edit:", sc.id);
        try c.btnAct(h, lb.delete, "destructive", "auto-sch-del:", sc.id);
        try c.btnRowClose(h);
        try h.raw("</div>");
    }
    try h.raw("</div>");
}

fn renderRuns(h: *Html, s: RunsState) !void {
    if (s.rows.len == 0) {
        try c.emptyState(h, s.empty);
        return;
    }
    try h.raw("<div class=\"rp-card\">");
    for (s.rows) |r| {
        try h.raw("<div class=kv><span class=kv-k>");
        try h.esc(r.name);
        try h.raw(" <span class=np-artist>");
        try h.esc(r.trigger);
        try h.raw("</span></span><span class=kv-v>");
        try c.badge(h, r.status, r.variant);
        try h.raw("</span></div>");
    }
    try h.raw("</div>");
}

test "unavailable" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .title = "Automations", .sub = "ignored", .unavailable = "no svc" });
    try std.testing.expectEqualStrings("<h1 class=page-title>Automations</h1>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">no svc</div></div>", h.b.items);
}

test "empty sections" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderBody(&h, .{
        .listTitle = "Automations",
        .schedTitle = "Schedules",
        .runsTitle = "Recent runs",
        .list = .{ .new = "New", .empty = "none" },
        .scheds = .{ .new = "New schedule", .gated = true, .gateWhy = "need one", .empty = "no scheds" },
        .runs = .{ .empty = "no runs" },
    });
    try std.testing.expectEqualStrings("<section class=sec><h2 class=sec-title>Automations</h2>" ++
        "<div class=btn-row><button class=\"rp-btn rp-btn--primary\" data-act=\"auto-new\">New</button></div>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">none</div></div></section>" ++
        "<section class=sec><h2 class=sec-title>Schedules</h2>" ++
        "<div class=btn-row><button class=\"rp-btn rp-btn--outline\" disabled title=\"need one\">New schedule</button></div>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">need one</div></div></section>" ++
        "<section class=sec><h2 class=sec-title>Recent runs</h2>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">no runs</div></div></section>", h.b.items);
}

test "automation card wires per-id acts" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const cards = [_]Card{.{ .id = "g&1", .label = "L", .watchDir = "D", .status = "success", .statusVar = "success", .chain = "A → B", .enabled = true }};
    try renderList(&h, .{ .new = "New", .cards = &cards }, .{ .enabled = "Enabled", .enabledDl = "enabled", .run = "Run", .schAdd = "Add", .edit = "Edit", .delete = "Del" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-act=\"auto-toggle:g&amp;1\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-act=\"auto-del:g&amp;1\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=np-meta><span class=\"rp-badge rp-badge--success\">success</span></div>") != null);
}

test "schedule card warn + state badge" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const cards = [_]SchedCard{.{ .id = "s1", .label = "Nightly", .target = "gone", .stateText = "daily", .stateVar = "info", .trigger = "at 03:00", .gates = "no gates", .lastFired = "never", .warnTone = "bad", .warnText = "orphan", .enabled = true }};
    try renderScheds(&h, .{ .new = "New", .cards = &cards }, .{ .enabled = "Enabled", .enabledDl = "enabled", .edit = "Edit", .delete = "Del" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<span class=\"hint hint--bad\">orphan</span>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<span class=\"rp-badge rp-badge--info\">daily</span> at 03:00") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-act=\"auto-sch-tgl:s1\"") != null);
}

test "runs rows" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const rows = [_]RunRow{.{ .name = "a<.wav", .trigger = "manual", .status = "error", .variant = "error" }};
    try renderRuns(&h, .{ .rows = &rows });
    try std.testing.expectEqualStrings("<div class=\"rp-card\"><div class=kv><span class=kv-k>a&lt;.wav" ++
        " <span class=np-artist>manual</span></span><span class=kv-v>" ++
        "<span class=\"rp-badge rp-badge--error\">error</span></span></div></div>", h.b.items);
}
