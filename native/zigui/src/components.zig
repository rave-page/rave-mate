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

// --- media ---
// Primitives for the media-batch tabs (overlays, twitch, editor). Ports of the
// components.go helpers named in each doc line; Go resolves every `dl` (data-label) and
// every number to a string, so nothing here formats floats or lowercases Unicode.

/// Btn is a btn() call as data (for button lists).
pub const Btn = struct {
    label: []const u8 = "",
    variant: []const u8 = "",
    act: []const u8 = "",
    val: []const u8 = "",
};

pub fn btnOf(h: *Html, b: Btn) !void {
    try btn(h, b.label, b.variant, b.act, b.val);
}

/// btnRowOf brackets a slice of buttons in one btn-row (Go btnRow over a slice).
pub fn btnRowOf(h: *Html, bs: []const Btn) !void {
    try btnRowOpen(h);
    for (bs) |b| try btnOf(h, b);
    try btnRowClose(h);
}

/// btnAct is btn with a per-row act built by concatenation (Go `btn(label, variant,
/// "act:"+id, "")`): one data-act of prefix++id, NO data-val.
pub fn btnAct(h: *Html, label: []const u8, variant: []const u8, act_prefix: []const u8, id: []const u8) !void {
    try h.raw("<button class=\"rp-btn rp-btn--");
    try h.raw(if (variant.len == 0) "outline" else variant);
    try h.raw("\" data-act=\"");
    try h.esc(act_prefix);
    try h.esc(id);
    try h.raw("\">");
    try h.esc(label);
    try h.raw("</button>");
}

/// Toggle is a toggleRow() call as data (dl = Go strings.ToLower(label)).
pub const Toggle = struct {
    label: []const u8 = "",
    dl: []const u8 = "",
    act: []const u8 = "",
    on: bool = false,
};

pub fn toggleOf(h: *Html, t: Toggle) !void {
    try toggleRow(h, t.label, t.dl, t.act, t.on);
}

/// Field is a fieldEx() call as data. inputType "" → "text"; ph "" → no placeholder;
/// tip is pre-rendered trusted markup (Go tipTopic), emitted raw beside the label.
pub const Field = struct {
    label: []const u8 = "",
    dl: []const u8 = "",
    act: []const u8 = "",
    value: []const u8 = "",
    inputType: []const u8 = "",
    ph: []const u8 = "",
    tip: []const u8 = "",
};

/// field mirrors Go fieldEx: labelled text/number input dispatching on change.
pub fn field(h: *Html, f: Field) !void {
    try h.raw("<label class=field data-label=");
    try h.attrQ(f.dl);
    try h.raw("><span class=field-label>");
    try h.esc(f.label);
    try h.raw(f.tip);
    try h.raw("</span><input class=field-input type=");
    try h.raw(if (f.inputType.len == 0) "text" else f.inputType);
    try h.raw(" value=");
    try h.attrQ(f.value);
    try h.raw(" data-value=");
    try h.attrQ(f.value);
    try h.raw(" data-act=");
    try h.attrQ(f.act);
    if (f.ph.len != 0) {
        try h.raw(" placeholder=");
        try h.attrQ(f.ph);
    }
    try h.raw("></label>");
}

/// KV is a kv() call as data (dl = Go strings.ToLower(label)).
pub const KV = struct {
    label: []const u8 = "",
    dl: []const u8 = "",
    value: []const u8 = "",
};

/// kv mirrors Go kv: key/value line, value ctl-readable via data-label/data-value.
pub fn kv(h: *Html, k: KV) !void {
    try h.raw("<div class=kv><span class=kv-k>");
    try h.esc(k.label);
    try h.raw("</span><span class=kv-v data-label=");
    try h.attrQ(k.dl);
    try h.raw(" data-value=");
    try h.attrQ(k.value);
    try h.raw(">");
    try h.esc(k.value);
    try h.raw("</span></div>");
}

/// Status is a statusRow() call as data. variant "" = render nothing (Go ovlStatus's
/// unknown-kind case returns "").
pub const Status = struct {
    variant: []const u8 = "",
    label: []const u8 = "",
    dl: []const u8 = "",
    line: []const u8 = "",
};

/// statusRow mirrors Go statusRow: status dot + label + muted sub-line.
pub fn statusRow(h: *Html, s: Status) !void {
    if (s.variant.len == 0) return;
    try h.raw("<div class=strow>");
    try dot(h, s.variant);
    try h.raw("<div class=strow-tx><div class=strow-l data-label=");
    try h.attrQ(s.dl);
    try h.raw(">");
    try h.esc(s.label);
    try h.raw("</div><div class=strow-s data-value=");
    try h.attrQ(s.line);
    try h.raw(">");
    try h.esc(s.line);
    try h.raw("</div></div></div>");
}

/// Slider is a slider() call as data. min/max/step/val arrive PRE-FORMATTED (Go trimNum)
/// and unitJs pre-quoted (Go jsQuote) — Go's shortest-round-trip float formatting has no
/// guaranteed Zig equivalent, so it stays Go-side.
pub const Slider = struct {
    label: []const u8 = "",
    dl: []const u8 = "",
    act: []const u8 = "",
    min: []const u8 = "",
    max: []const u8 = "",
    step: []const u8 = "",
    val: []const u8 = "",
    unit: []const u8 = "",
    unitJs: []const u8 = "", // JS string literal, inserted raw into the oninput attr
};

/// slider mirrors Go slider: labelled range input with an inline display-only readout.
pub fn slider(h: *Html, s: Slider) !void {
    try h.raw("<label class=slider data-label=");
    try h.attrQ(s.dl);
    try h.raw("><span class=field-label>");
    try h.esc(s.label);
    try h.raw(" <b class=slider-val>");
    try h.raw(s.val);
    try h.esc(s.unit);
    try h.raw("</b></span><input class=slider-input type=range min=");
    try h.raw(s.min);
    try h.raw(" max=");
    try h.raw(s.max);
    try h.raw(" step=");
    try h.raw(s.step);
    try h.raw(" value=");
    try h.raw(s.val);
    try h.raw(" data-act=");
    try h.attrQ(s.act);
    try h.raw(" data-value=");
    try h.attrQ(s.val);
    try h.raw(" oninput='var b=this.parentNode.querySelector(\".slider-val\");if(b)b.textContent=this.value+");
    try h.raw(s.unitJs);
    try h.raw("'></label>");
}

/// cardOpen/cardClose bracket an rp-card (Go card, streaming form). trailing is
/// pre-rendered trusted markup; the head is omitted only when BOTH are empty.
pub fn cardOpen(h: *Html, title: []const u8, trailing: []const u8) !void {
    try h.raw("<div class=\"rp-card\">");
    if (title.len != 0 or trailing.len != 0) {
        try h.raw("<div class=card-head><span class=card-h>");
        try h.esc(title);
        try h.raw("</span><span class=card-trail>");
        try h.raw(trailing);
        try h.raw("</span></div>");
    }
}

pub fn cardClose(h: *Html) !void {
    try h.raw("</div>");
}

/// fpairOpen/fpairClose put two short fields side by side (Go fpair, streaming form).
pub fn fpairOpen(h: *Html) !void {
    try h.raw("<div class=fpair>");
}

pub fn fpairClose(h: *Html) !void {
    try h.raw("</div>");
}

test "field defaults type, omits empty placeholder" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try field(&h, .{ .label = "Port", .dl = "port", .act = "set:p", .value = "80\"8" });
    try std.testing.expectEqualStrings("<label class=field data-label=\"port\"><span class=field-label>Port</span>" ++
        "<input class=field-input type=text value=\"80&#34;8\" data-value=\"80&#34;8\" data-act=\"set:p\"></label>", h.b.items);
    h.b.clearRetainingCapacity();
    try field(&h, .{ .label = "N", .dl = "n", .act = "a", .value = "1", .inputType = "number", .ph = "p&h" });
    try std.testing.expectEqualStrings("<label class=field data-label=\"n\"><span class=field-label>N</span>" ++
        "<input class=field-input type=number value=\"1\" data-value=\"1\" data-act=\"a\" placeholder=\"p&amp;h\"></label>", h.b.items);
}

test "kv escapes label and value" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try kv(&h, .{ .label = "URL", .dl = "url", .value = "http://x/?a&b" });
    try std.testing.expectEqualStrings("<div class=kv><span class=kv-k>URL</span>" ++
        "<span class=kv-v data-label=\"url\" data-value=\"http://x/?a&amp;b\">http://x/?a&amp;b</span></div>", h.b.items);
}

test "statusRow renders dot + label + line; empty variant renders nothing" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try statusRow(&h, .{ .variant = "success", .label = "On", .dl = "on", .line = "" });
    try std.testing.expectEqualStrings("<div class=strow><span class=\"dot dot--success\"></span>" ++
        "<div class=strow-tx><div class=strow-l data-label=\"on\">On</div>" ++
        "<div class=strow-s data-value=\"\"></div></div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try statusRow(&h, .{});
    try std.testing.expectEqualStrings("", h.b.items);
}

test "slider assembles pre-formatted numbers + raw unitJs" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try slider(&h, .{ .label = "Opacity", .dl = "opacity", .act = "o", .min = "0", .max = "1", .step = "0.05", .val = "0.8", .unit = "%", .unitJs = "\"%\"" });
    try std.testing.expectEqualStrings("<label class=slider data-label=\"opacity\"><span class=field-label>Opacity" ++
        " <b class=slider-val>0.8%</b></span><input class=slider-input type=range min=0 max=1 step=0.05 value=0.8" ++
        " data-act=\"o\" data-value=\"0.8\" oninput='var b=this.parentNode.querySelector(\".slider-val\");if(b)b.textContent=this.value+\"%\"'></label>", h.b.items);
}

test "cardOpen omits head when title and trailing are empty" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try cardOpen(&h, "", "");
    try cardClose(&h);
    try std.testing.expectEqualStrings("<div class=\"rp-card\"></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try cardOpen(&h, "T&x", "<b>t</b>");
    try cardClose(&h);
    try std.testing.expectEqualStrings("<div class=\"rp-card\"><div class=card-head><span class=card-h>T&amp;x</span>" ++
        "<span class=card-trail><b>t</b></span></div></div>", h.b.items);
}

test "btnRowOf and toggleOf delegate" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const bs = [_]Btn{ .{ .label = "A", .variant = "primary", .act = "a" }, .{ .label = "B", .variant = "ghost", .act = "b", .val = "v" } };
    try btnRowOf(&h, &bs);
    try std.testing.expectEqualStrings("<div class=btn-row>" ++
        "<button class=\"rp-btn rp-btn--primary\" data-act=\"a\">A</button>" ++
        "<button class=\"rp-btn rp-btn--ghost\" data-act=\"b\" data-val=\"v\">B</button></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try toggleOf(&h, .{ .label = "On", .dl = "on", .act = "t", .on = true });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-value=\"true\"") != null);
}
