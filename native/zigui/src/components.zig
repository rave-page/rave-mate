//! Ports of internal/webui/components.go helpers used by migrated tabs.
//! Byte-exact markup contract: extend by porting the Go helper verbatim — never
//! restyle here. Variant/class strings are trusted literals (raw), dynamic text
//! is always escaped, matching the Go originals.

const std = @import("std");
const Html = @import("html.zig").Html;

/// panel: titled page header (page-title + optional subtitle).
pub fn panel(h: *Html, title: []const u8, sub: []const u8) !void {
    try h.raw("<h1 class=page-title>");
    try h.esc(title);
    try h.raw("</h1>");
    if (sub.len != 0) {
        try h.raw("<p class=page-sub>");
        try h.esc(sub);
        try h.raw("</p>");
    }
}

/// emptyState: the rp-empty placeholder.
pub fn emptyState(h: *Html, msg: []const u8) !void {
    try h.raw("<div class=\"rp-empty\"><div class=\"rp-empty__title\">");
    try h.esc(msg);
    try h.raw("</div></div>");
}

/// badge: rp-badge. Empty variant defaults to "secondary" (Go badge parity).
pub fn badge(h: *Html, text: []const u8, variant: []const u8) !void {
    const v = if (variant.len == 0) "secondary" else variant;
    try h.raw("<span class=\"rp-badge rp-badge--");
    try h.raw(v);
    try h.raw("\">");
    try h.esc(text);
    try h.raw("</span>");
}

/// dot: small status dot (color via variant → CSS var).
pub fn dot(h: *Html, variant: []const u8) !void {
    try h.raw("<span class=\"dot dot--");
    try h.raw(variant);
    try h.raw("\"></span>");
}

/// badgeDot: status dot + badge pair (render_appgroups.go badgeDot).
pub fn badgeDot(h: *Html, text: []const u8, variant: []const u8) !void {
    try dot(h, variant);
    try h.raw(" ");
    try badge(h, text, variant);
}

/// btn: rp-btn. Empty variant defaults to "outline"; act/val become escaped
/// data-act/data-val; empty act = plain (non-action) button (Go btn parity).
pub fn btn(h: *Html, label: []const u8, variant: []const u8, act: []const u8, val: []const u8) !void {
    const v = if (variant.len == 0) "outline" else variant;
    try h.raw("<button class=\"rp-btn rp-btn--");
    try h.raw(v);
    try h.raw("\"");
    if (act.len != 0) {
        try h.raw(" data-act=\"");
        try h.esc(act);
        try h.raw("\"");
        if (val.len != 0) {
            try h.raw(" data-val=\"");
            try h.esc(val);
            try h.raw("\"");
        }
    }
    try h.raw(">");
    try h.esc(label);
    try h.raw("</button>");
}

/// btnRowOpen/btnRowClose bracket buttons horizontally (Go btnRow, streaming form).
pub fn btnRowOpen(h: *Html) !void {
    try h.raw("<div class=btn-row>");
}

pub fn btnRowClose(h: *Html) !void {
    try h.raw("</div>");
}

/// btnGated: disabled button whose title names the missing dependency (Go btnGated).
pub fn btnGated(h: *Html, label: []const u8, why: []const u8) !void {
    try h.raw("<button class=\"rp-btn rp-btn--outline\" disabled title=");
    try h.attrQ(why);
    try h.raw(">");
    try h.esc(label);
    try h.raw("</button>");
}

/// hint: small dynamic-info chip. Empty tone defaults to "info" (Go hint parity).
pub fn hint(h: *Html, tone: []const u8, text: []const u8) !void {
    const t = if (tone.len == 0) "info" else tone;
    try h.raw("<span class=\"hint hint--");
    try h.raw(t);
    try h.raw("\">");
    try h.esc(text);
    try h.raw("</span>");
}

/// sectionOpen/sectionClose bracket a titled block (Go section, streaming form).
pub fn sectionOpen(h: *Html, title: []const u8) !void {
    try h.raw("<section class=sec><h2 class=sec-title>");
    try h.esc(title);
    try h.raw("</h2>");
}

pub fn sectionClose(h: *Html) !void {
    try h.raw("</section>");
}

/// toggleRow: labelled switch (Go toggleRowDL). data_label = strings.ToLower(label)
/// resolved Go-side (Unicode lowercasing stays in Go).
pub fn toggleRow(h: *Html, label: []const u8, data_label: []const u8, act: []const u8, on: bool) !void {
    try h.raw("<label class=row data-label=");
    try h.attrQ(data_label);
    try h.raw("><span class=row-label>");
    try h.esc(label);
    try h.raw("</span><span class=switch><input type=checkbox");
    if (on) try h.raw(" checked");
    try h.raw(" data-act=");
    try h.attrQ(act);
    try h.raw(" data-value=");
    try h.attrQ(if (on) "true" else "false");
    try h.raw("><span class=switch-track></span></span></label>");
}

/// Tab is one subTabs item ([value,label]).
pub const Tab = struct {
    val: []const u8 = "",
    label: []const u8 = "",
};

/// subTabs: segmented control; each button's act = act_prefix ++ item.val (Go subTabs).
pub fn subTabs(h: *Html, act_prefix: []const u8, active: []const u8, items: []const Tab) !void {
    try h.raw("<div class=subtabs>");
    for (items) |it| {
        try h.raw("<button class=\"subtab");
        if (std.mem.eql(u8, it.val, active)) try h.raw(" active");
        try h.raw("\" data-act=\"");
        try h.esc(act_prefix);
        try h.esc(it.val);
        try h.raw("\" data-val=\"");
        try h.esc(it.val);
        try h.raw("\">");
        try h.esc(it.label);
        try h.raw("</button>");
    }
    try h.raw("</div>");
}

/// SelectRow is one filter-passing smart-select option row.
pub const SelectRow = struct {
    val: []const u8 = "",
    label: []const u8 = "",
    sub: []const u8 = "",
    badge: []const u8 = "",
    cur: bool = false,
};

/// Select: smart-select resolved render state (webui selState) — id, plain label,
/// current label, open/filter, filter-passing rows. Filtering resolved Go-side.
pub const Select = struct {
    id: []const u8 = "",
    label: []const u8 = "",
    curLabel: []const u8 = "",
    open: bool = false,
    filter: []const u8 = "",
    rows: []const SelectRow = &.{},
};

/// selectBox mirrors Go selHTML: ss-field wrapper + label + inner control.
pub fn selectBox(h: *Html, s: Select) !void {
    try h.raw("<div class=ss-field>");
    if (s.label.len != 0) {
        try h.raw("<span class=ss-label>");
        try h.esc(s.label);
        try h.raw("</span>");
    }
    try h.raw("<div class=ss id=\"ss-");
    try h.esc(s.id);
    try h.raw("\">");
    try selectInner(h, s);
    try h.raw("</div></div>");
}

/// selectInner mirrors Go selInnerHTML (the <div class=ss> inner markup).
pub fn selectInner(h: *Html, s: Select) !void {
    try h.raw("<button type=button class=\"ss-btn");
    if (s.open) try h.raw(" open");
    try h.raw("\" data-act=\"ss-tgl:");
    try h.esc(s.id);
    try h.raw("\" data-label=");
    try h.attrQ(s.id);
    try h.raw("><span class=ss-cur>");
    try h.esc(s.curLabel);
    try h.raw("</span><svg class=ss-chev viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"2\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\"><path d=\"m6 9 6 6 6-6\"/></svg></button>");
    if (!s.open) return;
    try h.raw("<div class=ss-bd data-act=\"ss-tgl:");
    try h.esc(s.id);
    try h.raw("\"></div><div class=ss-panel><form class=ss-fw data-act=\"ss-first:");
    try h.esc(s.id);
    try h.raw("\"><input class=ss-filter id=\"ss-f-");
    try h.esc(s.id);
    try h.raw("\" data-actinput=\"ss-flt:");
    try h.esc(s.id);
    try h.raw("\" data-label=\"");
    try h.esc(s.id);
    try h.raw("-flt\" placeholder=\"Type to filter…\" value=");
    try h.attrQ(s.filter);
    try h.raw(" autocomplete=off></form><div class=ss-list id=\"ss-l-");
    try h.esc(s.id);
    try h.raw("\">");
    try selectList(h, s.id, s.rows);
    try h.raw("</div></div>");
}

/// selectList mirrors Go selListHTML (resolved rows / ss-none).
pub fn selectList(h: *Html, id: []const u8, rows: []const SelectRow) !void {
    if (rows.len == 0) {
        try h.raw("<div class=ss-none>No matches</div>");
        return;
    }
    for (rows) |o| {
        try h.raw("<div class=\"ss-opt");
        if (o.cur) try h.raw(" cur");
        try h.raw("\" data-act=\"ss-pick:");
        try h.esc(id);
        try h.raw("\" data-val=");
        try h.attrQ(o.val);
        try h.raw("><span class=ss-main><span class=ss-ol>");
        try h.esc(o.label);
        try h.raw("</span>");
        if (o.sub.len != 0) {
            try h.raw("<span class=ss-sub>");
            try h.esc(o.sub);
            try h.raw("</span>");
        }
        try h.raw("</span>");
        if (o.badge.len != 0) {
            try h.raw("<span class=ss-badge>");
            try h.esc(o.badge);
            try h.raw("</span>");
        }
        try h.raw("</div>");
    }
}

test "panel with and without sub" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try panel(&h, "T<x>", "");
    try std.testing.expectEqualStrings("<h1 class=page-title>T&lt;x&gt;</h1>", h.b.items);
    h.b.clearRetainingCapacity();
    try panel(&h, "T", "S&s");
    try std.testing.expectEqualStrings("<h1 class=page-title>T</h1><p class=page-sub>S&amp;s</p>", h.b.items);
}

test "btn escapes act, omits empty val, defaults variant" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try btn(&h, "Go", "", "a\"b", "");
    try std.testing.expectEqualStrings("<button class=\"rp-btn rp-btn--outline\" data-act=\"a&#34;b\">Go</button>", h.b.items);
    h.b.clearRetainingCapacity();
    try btn(&h, "L", "go", "act", "v'1");
    try std.testing.expectEqualStrings("<button class=\"rp-btn rp-btn--go\" data-act=\"act\" data-val=\"v&#39;1\">L</button>", h.b.items);
}

test "badge default variant" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try badge(&h, "x", "");
    try std.testing.expectEqualStrings("<span class=\"rp-badge rp-badge--secondary\">x</span>", h.b.items);
}

test "toggleRow on/off" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try toggleRow(&h, "Auto", "auto", "t-act", true);
    try std.testing.expectEqualStrings("<label class=row data-label=\"auto\"><span class=row-label>Auto</span>" ++
        "<span class=switch><input type=checkbox checked data-act=\"t-act\" data-value=\"true\">" ++
        "<span class=switch-track></span></span></label>", h.b.items);
    h.b.clearRetainingCapacity();
    try toggleRow(&h, "A", "a", "x", false);
    try std.testing.expectEqualStrings("<label class=row data-label=\"a\"><span class=row-label>A</span>" ++
        "<span class=switch><input type=checkbox data-act=\"x\" data-value=\"false\">" ++
        "<span class=switch-track></span></span></label>", h.b.items);
}

test "subTabs marks active" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const items = [_]Tab{ .{ .val = "app", .label = "App" }, .{ .val = "midi", .label = "MIDI" } };
    try subTabs(&h, "logs-bus:", "midi", &items);
    try std.testing.expectEqualStrings("<div class=subtabs>" ++
        "<button class=\"subtab\" data-act=\"logs-bus:app\" data-val=\"app\">App</button>" ++
        "<button class=\"subtab active\" data-act=\"logs-bus:midi\" data-val=\"midi\">MIDI</button>" ++
        "</div>", h.b.items);
}

test "hint default tone + btnGated + section" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try hint(&h, "", "t&x");
    try std.testing.expectEqualStrings("<span class=\"hint hint--info\">t&amp;x</span>", h.b.items);
    h.b.clearRetainingCapacity();
    try btnGated(&h, "New", "why\"y");
    try std.testing.expectEqualStrings("<button class=\"rp-btn rp-btn--outline\" disabled title=\"why&#34;y\">New</button>", h.b.items);
    h.b.clearRetainingCapacity();
    try sectionOpen(&h, "T<");
    try sectionClose(&h);
    try std.testing.expectEqualStrings("<section class=sec><h2 class=sec-title>T&lt;</h2></section>", h.b.items);
}

test "selectBox closed" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try selectBox(&h, .{ .id = "logs-level", .label = "Level", .curLabel = "All" });
    try std.testing.expectEqualStrings("<div class=ss-field><span class=ss-label>Level</span>" ++
        "<div class=ss id=\"ss-logs-level\">" ++
        "<button type=button class=\"ss-btn\" data-act=\"ss-tgl:logs-level\" data-label=\"logs-level\">" ++
        "<span class=ss-cur>All</span>" ++
        "<svg class=ss-chev viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"2\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\"><path d=\"m6 9 6 6 6-6\"/></svg></button>" ++
        "</div></div>", h.b.items);
}

test "selectBox open with rows and cur" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const rows = [_]SelectRow{ .{ .val = "", .label = "All sources" }, .{ .val = "traktor", .label = "traktor", .cur = true } };
    try selectBox(&h, .{ .id = "logs-source", .label = "Source", .curLabel = "traktor", .open = true, .filter = "tr", .rows = &rows });
    try std.testing.expectEqualStrings("<div class=ss-field><span class=ss-label>Source</span>" ++
        "<div class=ss id=\"ss-logs-source\">" ++
        "<button type=button class=\"ss-btn open\" data-act=\"ss-tgl:logs-source\" data-label=\"logs-source\">" ++
        "<span class=ss-cur>traktor</span>" ++
        "<svg class=ss-chev viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"2\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\"><path d=\"m6 9 6 6 6-6\"/></svg></button>" ++
        "<div class=ss-bd data-act=\"ss-tgl:logs-source\"></div><div class=ss-panel>" ++
        "<form class=ss-fw data-act=\"ss-first:logs-source\"><input class=ss-filter id=\"ss-f-logs-source\" data-actinput=\"ss-flt:logs-source\" data-label=\"logs-source-flt\" placeholder=\"Type to filter…\" value=\"tr\" autocomplete=off></form>" ++
        "<div class=ss-list id=\"ss-l-logs-source\">" ++
        "<div class=\"ss-opt\" data-act=\"ss-pick:logs-source\" data-val=\"\"><span class=ss-main><span class=ss-ol>All sources</span></span></div>" ++
        "<div class=\"ss-opt cur\" data-act=\"ss-pick:logs-source\" data-val=\"traktor\"><span class=ss-main><span class=ss-ol>traktor</span></span></div>" ++
        "</div></div></div></div>", h.b.items);
}

test "selectList empty = no matches" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try selectList(&h, "x", &.{});
    try std.testing.expectEqualStrings("<div class=ss-none>No matches</div>", h.b.items);
}

// --- midi ---

/// cardOpen opens an rp-card (Go card, streaming form). head=true also opens the
/// card-head + card-trail slot: the caller renders the trailing HTML, then calls
/// cardTrailClose. Go emits the head when title or trailing is non-empty.
pub fn cardOpen(h: *Html, title: []const u8, head: bool) !void {
    try h.raw("<div class=\"rp-card\">");
    if (head) {
        try h.raw("<div class=card-head><span class=card-h>");
        try h.esc(title);
        try h.raw("</span><span class=card-trail>");
    }
}

pub fn cardTrailClose(h: *Html) !void {
    try h.raw("</span></div>");
}

pub fn cardClose(h: *Html) !void {
    try h.raw("</div>");
}

test "cardOpen head + trail + close" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try cardOpen(&h, "T&x", true);
    try badge(&h, "b", "info");
    try cardTrailClose(&h);
    try h.raw("body");
    try cardClose(&h);
    try std.testing.expectEqualStrings("<div class=\"rp-card\"><div class=card-head><span class=card-h>T&amp;x</span>" ++
        "<span class=card-trail><span class=\"rp-badge rp-badge--info\">b</span></span></div>body</div>", h.b.items);
    h.b.clearRetainingCapacity();
    try cardOpen(&h, "ignored", false);
    try cardClose(&h);
    try std.testing.expectEqualStrings("<div class=\"rp-card\"></div>", h.b.items);
}
