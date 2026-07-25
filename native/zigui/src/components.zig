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

// --- motion + live (fleet: live batch) ---

/// masterDetailOpen/Mid/Close bracket the list|detail split (Go masterDetail, streaming form).
pub fn masterDetailOpen(h: *Html) !void {
    try h.raw("<div class=mdsplit><div class=md-list>");
}

pub fn masterDetailMid(h: *Html) !void {
    try h.raw("</div><div class=md-detail>");
}

pub fn masterDetailClose(h: *Html) !void {
    try h.raw("</div></div>");
}

/// sectionOpenTip is sectionOpen with a pre-rendered tooltip after the title (Go sectionTip).
/// tip_html is trusted markup resolved Go-side (tipTopic) — emitted raw.
pub fn sectionOpenTip(h: *Html, title: []const u8, tip_html: []const u8) !void {
    try h.raw("<section class=sec><h2 class=sec-title>");
    try h.esc(title);
    try h.raw(tip_html);
    try h.raw("</h2>");
}

/// statusRow: status dot + label + muted sub-line (Go statusRow/statusRowDL — shared by the
/// live + vrchat batches). data_label = Go strings.ToLower(label).
pub fn statusRow(h: *Html, variant: []const u8, label: []const u8, data_label: []const u8, line: []const u8) !void {
    try h.raw("<div class=strow>");
    try dot(h, variant);
    try h.raw("<div class=strow-tx><div class=strow-l data-label=");
    try h.attrQ(data_label);
    try h.raw(">");
    try h.esc(label);
    try h.raw("</div><div class=strow-s data-value=");
    try h.attrQ(line);
    try h.raw(">");
    try h.esc(line);
    try h.raw("</div></div></div>");
}

/// Slider is a resolved labelled range input (Go slider): data_label pre-lowered Go-side,
/// every number pre-formatted with Go trimNum, unitJs = jsQuote(unit). Zig never formats
/// a float — the Go state builder owns all numeric formatting.
pub const Slider = struct {
    label: []const u8 = "",
    dl: []const u8 = "",
    act: []const u8 = "",
    unit: []const u8 = "",
    unitJs: []const u8 = "\"\"",
    minS: []const u8 = "0",
    maxS: []const u8 = "0",
    stepS: []const u8 = "1",
    valS: []const u8 = "0",
};

/// slider mirrors Go slider() byte-for-byte (live value display is inline JS, no act).
pub fn slider(h: *Html, s: Slider) !void {
    try h.raw("<label class=slider data-label=");
    try h.attrQ(s.dl);
    try h.raw("><span class=field-label>");
    try h.esc(s.label);
    try h.raw(" <b class=slider-val>");
    try h.raw(s.valS);
    try h.esc(s.unit);
    try h.raw("</b></span><input class=slider-input type=range min=");
    try h.raw(s.minS);
    try h.raw(" max=");
    try h.raw(s.maxS);
    try h.raw(" step=");
    try h.raw(s.stepS);
    try h.raw(" value=");
    try h.raw(s.valS);
    try h.raw(" data-act=");
    try h.attrQ(s.act);
    try h.raw(" data-value=");
    try h.attrQ(s.valS);
    try h.raw(" oninput='var b=this.parentNode.querySelector(\".slider-val\");if(b)b.textContent=this.value+");
    try h.raw(s.unitJs);
    try h.raw("'></label>");
}

// --- end motion + live ---

// --- vrchat ---
// Ports of the components.go helpers the vrchat/worlds tabs need. Label-derived data-labels
// arrive pre-lowered from Go (the *DL variants there) — Unicode lowering stays in Go.
// statusRow lives in the motion+live block above (identical port, deduped at merge).

/// kv: key/value line (Go kvDL). dl = Go strings.ToLower(label).
pub fn kv(h: *Html, label: []const u8, dl: []const u8, value: []const u8) !void {
    try h.raw("<div class=kv><span class=kv-k>");
    try h.esc(label);
    try h.raw("</span><span class=kv-v data-label=");
    try h.attrQ(dl);
    try h.raw(" data-value=");
    try h.attrQ(value);
    try h.raw(">");
    try h.esc(value);
    try h.raw("</span></div>");
}

/// fieldEx: labelled input dispatching act on change (Go fieldExDL). Empty input_type → "text";
/// empty placeholder omits the attribute; tip is pre-rendered markup (trusted, raw).
pub fn fieldEx(h: *Html, label: []const u8, dl: []const u8, act: []const u8, value: []const u8, input_type: []const u8, placeholder: []const u8, tip: []const u8) !void {
    try h.raw("<label class=field data-label=");
    try h.attrQ(dl);
    try h.raw("><span class=field-label>");
    try h.esc(label);
    try h.raw(tip);
    try h.raw("</span><input class=field-input type=");
    try h.raw(if (input_type.len == 0) "text" else input_type);
    try h.raw(" value=");
    try h.attrQ(value);
    try h.raw(" data-value=");
    try h.attrQ(value);
    try h.raw(" data-act=");
    try h.attrQ(act);
    if (placeholder.len != 0) {
        try h.raw(" placeholder=");
        try h.attrQ(placeholder);
    }
    try h.raw("></label>");
}

/// cardOpen/cardHeadClose/cardClose bracket an rp-card (Go card, streaming form). head=true
/// emits the card-head; the caller writes the trailing-slot markup, then cardHeadClose, then
/// the body, then cardClose. head must be (title.len != 0 or trailing markup non-empty).
pub fn cardOpen(h: *Html, title: []const u8, head: bool) !void {
    try h.raw("<div class=\"rp-card\">");
    if (head) {
        try h.raw("<div class=card-head><span class=card-h>");
        try h.esc(title);
        try h.raw("</span><span class=card-trail>");
    }
}

pub fn cardHeadClose(h: *Html) !void {
    try h.raw("</span></div>");
}

pub fn cardClose(h: *Html) !void {
    try h.raw("</div>");
}

/// itemRowOpen/itemRowClose bracket a list row (Go itemRow, streaming form): title + optional
/// sub-line, then the trailing action buttons the caller writes before itemRowClose.
pub fn itemRowOpen(h: *Html, title: []const u8, sub: []const u8) !void {
    try h.raw("<div class=irow><div class=irow-main><div class=irow-title>");
    try h.esc(title);
    try h.raw("</div>");
    if (sub.len != 0) {
        try h.raw("<div class=irow-sub>");
        try h.esc(sub);
        try h.raw("</div>");
    }
    try h.raw("</div><div class=irow-actions>");
}

pub fn itemRowClose(h: *Html) !void {
    try h.raw("</div></div>");
}

/// mdOpen/mdSplit/mdClose bracket a master/detail split (Go masterDetail, streaming form).
pub fn mdOpen(h: *Html) !void {
    try h.raw("<div class=mdsplit><div class=md-list>");
}

pub fn mdSplit(h: *Html) !void {
    try h.raw("</div><div class=md-detail>");
}

pub fn mdClose(h: *Html) !void {
    try h.raw("</div></div>");
}

/// fpairOpen/fpairClose bracket two side-by-side fields (Go fpair, streaming form).
pub fn fpairOpen(h: *Html) !void {
    try h.raw("<div class=fpair>");
}

pub fn fpairClose(h: *Html) !void {
    try h.raw("</div>");
}

/// num appends a base-10 integer (Go %d).
pub fn num(h: *Html, v: i64) !void {
    var buf: [24]u8 = undefined;
    try h.raw(std.fmt.bufPrint(&buf, "{d}", .{v}) catch unreachable);
}

test "kv + statusRow + fieldEx" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try kv(&h, "Owner", "owner", "me&you");
    try std.testing.expectEqualStrings("<div class=kv><span class=kv-k>Owner</span>" ++
        "<span class=kv-v data-label=\"owner\" data-value=\"me&amp;you\">me&amp;you</span></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try statusRow(&h, "success", "VRChat", "vrchat", "live");
    try std.testing.expectEqualStrings("<div class=strow><span class=\"dot dot--success\"></span>" ++
        "<div class=strow-tx><div class=strow-l data-label=\"vrchat\">VRChat</div>" ++
        "<div class=strow-s data-value=\"live\">live</div></div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try fieldEx(&h, "Link", "link", "world-np-link", "v", "", "", "");
    try std.testing.expectEqualStrings("<label class=field data-label=\"link\"><span class=field-label>Link</span>" ++
        "<input class=field-input type=text value=\"v\" data-value=\"v\" data-act=\"world-np-link\"></label>", h.b.items);
}

test "card + itemRow + masterDetail brackets" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try cardOpen(&h, "T&t", true);
    try btn(&h, "R", "ghost", "a", "");
    try cardHeadClose(&h);
    try emptyState(&h, "none");
    try cardClose(&h);
    try std.testing.expectEqualStrings("<div class=\"rp-card\"><div class=card-head><span class=card-h>T&amp;t</span>" ++
        "<span class=card-trail><button class=\"rp-btn rp-btn--ghost\" data-act=\"a\">R</button></span></div>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">none</div></div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try itemRowOpen(&h, "N", "s");
    try itemRowClose(&h);
    try std.testing.expectEqualStrings("<div class=irow><div class=irow-main><div class=irow-title>N</div>" ++
        "<div class=irow-sub>s</div></div><div class=irow-actions></div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try mdOpen(&h);
    try mdSplit(&h);
    try mdClose(&h);
    try std.testing.expectEqualStrings("<div class=mdsplit><div class=md-list></div><div class=md-detail></div></div>", h.b.items);
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

// --- motion + live tests ---

test "masterDetail split + sectionOpenTip" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try masterDetailOpen(&h);
    try h.raw("L");
    try masterDetailMid(&h);
    try h.raw("D");
    try masterDetailClose(&h);
    try std.testing.expectEqualStrings("<div class=mdsplit><div class=md-list>L</div><div class=md-detail>D</div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try sectionOpenTip(&h, "T&x", "<i>tip</i>");
    try sectionClose(&h);
    try std.testing.expectEqualStrings("<section class=sec><h2 class=sec-title>T&amp;x<i>tip</i></h2></section>", h.b.items);
}

test "statusRow escapes label + line" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try statusRow(&h, "success", "Se&ssion", "se&ssion", "1 peer <ok>");
    try std.testing.expectEqualStrings("<div class=strow><span class=\"dot dot--success\"></span>" ++
        "<div class=strow-tx><div class=strow-l data-label=\"se&amp;ssion\">Se&amp;ssion</div>" ++
        "<div class=strow-s data-value=\"1 peer &lt;ok&gt;\">1 peer &lt;ok&gt;</div></div></div>", h.b.items);
}

test "slider markup matches Go slider" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try slider(&h, .{ .label = "Scrub", .dl = "scrub", .act = "mo-scrub", .minS = "0", .maxS = "1000", .stepS = "1", .valS = "12.5" });
    try std.testing.expectEqualStrings("<label class=slider data-label=\"scrub\"><span class=field-label>Scrub " ++
        "<b class=slider-val>12.5</b></span><input class=slider-input type=range min=0 max=1000 step=1 value=12.5 " ++
        "data-act=\"mo-scrub\" data-value=\"12.5\" oninput='var b=this.parentNode.querySelector(\".slider-val\");" ++
        "if(b)b.textContent=this.value+\"\"'></label>", h.b.items);
}

// --- midi ---
// cardOpen/cardClose live in the vrchat block, statusRow in the motion+live block,
// itemRowOpen/Close in the vrchat block (identical ports, deduped at merge).
// cardTrailClose == cardHeadClose (same markup) — kept for the midi callers.

pub fn cardTrailClose(h: *Html) !void {
    try h.raw("</span></div>");
}

/// fchip: segmented/filter chip (Go fchip). Empty val omits data-val.
pub fn fchip(h: *Html, label: []const u8, val: []const u8, act: []const u8, active: bool) !void {
    try h.raw("<button class=\"fchip");
    if (active) try h.raw(" active");
    try h.raw("\" data-act=\"");
    try h.esc(act);
    try h.raw("\"");
    if (val.len != 0) {
        try h.raw(" data-val=\"");
        try h.esc(val);
        try h.raw("\"");
    }
    try h.raw(">");
    try h.esc(label);
    try h.raw("</button>");
}

/// toggleRowTip: toggleRow with pre-rendered tooltip markup beside the label
/// (Go toggleRowTipDL). tip_html is raw (tooltip.go owns that markup).
pub fn toggleRowTip(h: *Html, label: []const u8, data_label: []const u8, act: []const u8, on: bool, tip_html: []const u8) !void {
    try h.raw("<label class=row data-label=");
    try h.attrQ(data_label);
    try h.raw("><span class=row-label>");
    try h.esc(label);
    try h.raw(tip_html);
    try h.raw("</span><span class=switch><input type=checkbox");
    if (on) try h.raw(" checked");
    try h.raw(" data-act=");
    try h.attrQ(act);
    try h.raw(" data-value=");
    try h.attrQ(if (on) "true" else "false");
    try h.raw("><span class=switch-track></span></span></label>");
}

/// selectBoxRaw is selectBox with a pre-rendered label (Go selHTMLRaw) — for select
/// labels that carry a tooltip/badge. label_html is raw; s.label is ignored.
pub fn selectBoxRaw(h: *Html, s: Select, label_html: []const u8) !void {
    try h.raw("<div class=ss-field>");
    try h.raw(label_html);
    try h.raw("<div class=ss id=\"ss-");
    try h.esc(s.id);
    try h.raw("\">");
    try selectInner(h, s);
    try h.raw("</div></div>");
}

test "statusRow + fchip + itemRow + toggleRowTip" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try statusRow(&h, "ok", "St&ate", "st&ate", "li\"ne");
    try std.testing.expectEqualStrings("<div class=strow><span class=\"dot dot--ok\"></span>" ++
        "<div class=strow-tx><div class=strow-l data-label=\"st&amp;ate\">St&amp;ate</div>" ++
        "<div class=strow-s data-value=\"li&#34;ne\">li&#34;ne</div></div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try fchip(&h, "Cl&ock", "", "midi-ctl-filter:0:clock", true);
    try std.testing.expectEqualStrings("<button class=\"fchip active\" data-act=\"midi-ctl-filter:0:clock\">Cl&amp;ock</button>", h.b.items);
    h.b.clearRetainingCapacity();
    try fchip(&h, "L", "v'1", "a", false);
    try std.testing.expectEqualStrings("<button class=\"fchip\" data-act=\"a\" data-val=\"v&#39;1\">L</button>", h.b.items);
    h.b.clearRetainingCapacity();
    try itemRowOpen(&h, "T<x", "");
    try itemRowClose(&h);
    try std.testing.expectEqualStrings("<div class=irow><div class=irow-main><div class=irow-title>T&lt;x</div>" ++
        "</div><div class=irow-actions></div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try toggleRowTip(&h, "En", "en", "um-enable", true, "<i>tip</i>");
    try std.testing.expectEqualStrings("<label class=row data-label=\"en\"><span class=row-label>En<i>tip</i></span>" ++
        "<span class=switch><input type=checkbox checked data-act=\"um-enable\" data-value=\"true\">" ++
        "<span class=switch-track></span></span></label>", h.b.items);
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

// --- media ---
// Struct-shaped wrappers for the media-batch tabs (automations, overlays, twitch, editor):
// the tabs carry these controls around as state, so a struct beats 8 positional args. Each
// one DELEGATES to the flat primitive above - no markup is duplicated here. Deduped at the
// development merge: Slider/slider, cardOpen/cardHeadClose/cardClose, fpairOpen/fpairClose,
// statusRow, kv and fieldEx all come from the motion+live / vrchat / midi blocks unchanged.

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

pub fn fieldOf(h: *Html, f: Field) !void {
    try fieldEx(h, f.label, f.dl, f.act, f.value, f.inputType, f.ph, f.tip);
}

/// KV is a kv() call as data (dl = Go strings.ToLower(label)).
pub const KV = struct {
    label: []const u8 = "",
    dl: []const u8 = "",
    value: []const u8 = "",
};

pub fn kvOf(h: *Html, k: KV) !void {
    try kv(h, k.label, k.dl, k.value);
}

/// Status is a statusRow() call as data. variant "" = render NOTHING - Go ovlStatus returns
/// "" for an unknown output kind, and the overlays fragment must match that byte-for-byte.
pub const Status = struct {
    variant: []const u8 = "",
    label: []const u8 = "",
    dl: []const u8 = "",
    line: []const u8 = "",
};

pub fn statusOf(h: *Html, s: Status) !void {
    if (s.variant.len == 0) return;
    try statusRow(h, s.variant, s.label, s.dl, s.line);
}

test "media struct wrappers delegate to the flat primitives" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    var f = Html.init(std.testing.allocator);
    defer f.deinit();

    try fieldOf(&h, .{ .label = "Port", .dl = "port", .act = "set:p", .value = "80\"8", .inputType = "number" });
    try fieldEx(&f, "Port", "port", "set:p", "80\"8", "number", "", "");
    try std.testing.expectEqualStrings(f.b.items, h.b.items);

    h.b.clearRetainingCapacity();
    f.b.clearRetainingCapacity();
    try kvOf(&h, .{ .label = "URL", .dl = "url", .value = "http://x/?a&b" });
    try kv(&f, "URL", "url", "http://x/?a&b");
    try std.testing.expectEqualStrings(f.b.items, h.b.items);

    h.b.clearRetainingCapacity();
    f.b.clearRetainingCapacity();
    try statusOf(&h, .{ .variant = "success", .label = "On", .dl = "on", .line = "" });
    try statusRow(&f, "success", "On", "on", "");
    try std.testing.expectEqualStrings(f.b.items, h.b.items);

    h.b.clearRetainingCapacity();
    try toggleOf(&h, .{ .label = "On", .dl = "on", .act = "t", .on = true });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-value=\"true\"") != null);
}

test "statusOf renders nothing for an empty variant" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try statusOf(&h, .{});
    try std.testing.expectEqualStrings("", h.b.items);
}

test "btnRowOf and btnAct" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const bs = [_]Btn{ .{ .label = "A", .variant = "primary", .act = "a" }, .{ .label = "B", .variant = "ghost", .act = "b", .val = "v" } };
    try btnRowOf(&h, &bs);
    try std.testing.expectEqualStrings("<div class=btn-row>" ++
        "<button class=\"rp-btn rp-btn--primary\" data-act=\"a\">A</button>" ++
        "<button class=\"rp-btn rp-btn--ghost\" data-act=\"b\" data-val=\"v\">B</button></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try btnAct(&h, "Run", "go", "auto-run:", "g&1");
    try std.testing.expectEqualStrings("<button class=\"rp-btn rp-btn--go\" data-act=\"auto-run:g&amp;1\">Run</button>", h.b.items);
}

// --- peers + publish ---
// Both batches reuse the blocks above unchanged; progressBar was ported identically by
// both (deduped at merge), actionMenu comes from the publish batch.

/// progressBar mirrors Go progressBarStr: a .pbar whose fill width is PRE-FORMATTED
/// Go-side (progressPct, "%.1f%%") — floats never cross the ABI. Empty caption falls
/// back to the percentage, exactly like the Go helper.
pub fn progressBar(h: *Html, pct: []const u8, caption: []const u8) !void {
    try h.raw("<div class=pbar><div class=pbar-fill style=\"width:");
    try h.raw(pct);
    try h.raw("\"></div><span class=pbar-cap>");
    try h.esc(if (caption.len == 0) pct else caption);
    try h.raw("</span></div>");
}

/// actionMenu: the compact "⋯" one-shot-action dropdown (Go actionMenu / actionMenuHTML).
/// The menu label rides as the resolved select's curLabel (leading empty-Val option), so
/// this is just the amenu wrapper around a bare smart select.
pub fn actionMenu(h: *Html, s: Select) !void {
    try h.raw("<span class=amenu>");
    try selectBox(h, s);
    try h.raw("</span>");
}

test "progressBar caption defaults to the percentage" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try progressBar(&h, "42.5%", "4.2 MB / 10.0 MB");
    try std.testing.expectEqualStrings("<div class=pbar><div class=pbar-fill style=\"width:42.5%\"></div>" ++
        "<span class=pbar-cap>4.2 MB / 10.0 MB</span></div>", h.b.items);
}

test "progressBar + actionMenu" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try progressBar(&h, "42.5%", "3 of 7 <done>");
    try std.testing.expectEqualStrings("<div class=pbar><div class=pbar-fill style=\"width:42.5%\"></div>" ++
        "<span class=pbar-cap>3 of 7 &lt;done&gt;</span></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try progressBar(&h, "0.0%", "");
    try std.testing.expectEqualStrings("<div class=pbar><div class=pbar-fill style=\"width:0.0%\"></div>" ++
        "<span class=pbar-cap>0.0%</span></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try actionMenu(&h, .{ .id = "capmenu-1", .curLabel = "⋯ More" });
    try std.testing.expectEqualStrings("<span class=amenu><div class=ss-field><div class=ss id=\"ss-capmenu-1\">" ++
        "<button type=button class=\"ss-btn\" data-act=\"ss-tgl:capmenu-1\" data-label=\"capmenu-1\">" ++
        "<span class=ss-cur>⋯ More</span>" ++
        "<svg class=ss-chev viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"2\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\"><path d=\"m6 9 6 6 6-6\"/></svg></button>" ++
        "</div></div></span>", h.b.items);
}

// --- end peers + publish ---

// --- settings ---
// The settings tab reuses everything above (panel/emptyState/hint/section*/toggleRow*/fieldEx/
// kv*/selectBox*/btn*/itemRow*/fpair*) — only the gated switch was missing.

/// toggleRowGated: disabled switch + a warn hint naming what to install to unlock it (Go
/// toggleRowGatedDL). Same rule as btnGated: gated controls stay visible, greyed, explained.
/// data_label = Go strings.ToLower(label).
pub fn toggleRowGated(h: *Html, label: []const u8, data_label: []const u8, on: bool, gate_hint: []const u8) !void {
    try h.raw("<label class=\"row row--gated\" data-label=");
    try h.attrQ(data_label);
    try h.raw("><span class=row-label>");
    try h.esc(label);
    try h.raw("</span><span class=switch><input type=checkbox");
    if (on) try h.raw(" checked");
    try h.raw(" disabled><span class=switch-track></span></span></label><div class=set-gate>");
    try hint(h, "warn", gate_hint);
    try h.raw("</div>");
}

test "toggleRowGated: disabled switch + warn hint" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try toggleRowGated(&h, "Embed p&layer", "embed p&layer", true, "Install mpv");
    try std.testing.expectEqualStrings("<label class=\"row row--gated\" data-label=\"embed p&amp;layer\">" ++
        "<span class=row-label>Embed p&amp;layer</span><span class=switch>" ++
        "<input type=checkbox checked disabled><span class=switch-track></span></span></label>" ++
        "<div class=set-gate><span class=\"hint hint--warn\">Install mpv</span></div>", h.b.items);
}

// --- library ---
// The Library tab reuses everything above (panel/emptyState/badge/btn*/fchip/toggleRow/
// selectBox*/card*/itemRow*/kv*/subTabs/sectionOpen/num/masterDetail brackets, plus the
// peers+publish progressBar and actionMenu). Only the wide + tri-pane layouts were missing.

/// mdWideOpen brackets the wide list|detail split (Go masterDetailWide): the list is the
/// primary work surface, the detail a fixed-width right inspector. Close with mdSplit/mdClose.
pub fn mdWideOpen(h: *Html) !void {
    try h.raw("<div class=\"mdsplit wide\"><div class=md-list>");
}

/// triOpen/triMid/triClose bracket the nav|list|detail split with draggable dividers
/// (Go triPane). nav_var/detail_var are :root custom-property names the splitter JS
/// persists - trusted literals, emitted raw exactly like the Go original. nav_html is
/// the pre-rendered nav column.
pub fn triOpen(h: *Html, nav_var: []const u8, detail_var: []const u8, nav_html: []const u8) !void {
    try h.raw("<div class=\"mdsplit wide tri\" style=\"grid-template-columns:var(--");
    try h.raw(nav_var);
    try h.raw(",220px) 6px minmax(0,1fr) 6px var(--");
    try h.raw(detail_var);
    try h.raw(",clamp(300px,28vw,400px))\">");
    try h.raw("<div class=md-nav>");
    try h.raw(nav_html);
    try h.raw("</div>");
    try h.raw("<div class=split-h data-splitvar=\"");
    try h.raw(nav_var);
    try h.raw("\" data-splitdef=220></div>");
    try h.raw("<div class=md-list>");
}

pub fn triMid(h: *Html, detail_var: []const u8) !void {
    try h.raw("</div><div class=split-h data-splitvar=\"");
    try h.raw(detail_var);
    try h.raw("\" data-splitdef=340 data-splitdir=r></div><div class=md-detail>");
}

pub fn triClose(h: *Html) !void {
    try h.raw("</div></div>");
}

test "library layout primitives" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try mdWideOpen(&h);
    try mdSplit(&h);
    try mdClose(&h);
    try std.testing.expectEqualStrings("<div class=\"mdsplit wide\"><div class=md-list></div>" ++
        "<div class=md-detail></div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try triOpen(&h, "lib-nav-w", "lib-det-w", "N");
    try h.raw("L");
    try triMid(&h, "lib-det-w");
    try h.raw("D");
    try triClose(&h);
    try std.testing.expectEqualStrings("<div class=\"mdsplit wide tri\" style=\"grid-template-columns:" ++
        "var(--lib-nav-w,220px) 6px minmax(0,1fr) 6px var(--lib-det-w,clamp(300px,28vw,400px))\">" ++
        "<div class=md-nav>N</div><div class=split-h data-splitvar=\"lib-nav-w\" data-splitdef=220></div>" ++
        "<div class=md-list>L</div><div class=split-h data-splitvar=\"lib-det-w\" data-splitdef=340 " ++
        "data-splitdir=r></div><div class=md-detail>D</div></div>", h.b.items);
}

// --- libviews ---
// First MODAL port (library mirror / remote-cue-edit bodies + the Library modals). The Go
// `modal()` helper concatenates scrim + head + body + foot, so the Zig twin is a streaming
// bracket triple: modalOpen(title) → body → modalFoot() → footer → modalClose().

/// modalOpen emits the scrim + dialog head and opens `.modal-body` (Go components.go modal).
pub fn modalOpen(h: *Html, title: []const u8) !void {
    try h.raw("<div class=modal-scrim data-act=modal-close></div>" ++
        "<div class=modal role=dialog><div class=modal-head><h3 class=modal-title>");
    try h.esc(title);
    try h.raw("</h3><button class=modal-x data-act=modal-close aria-label=Close>✕</button></div>" ++
        "<div class=modal-body>");
}

/// modalFoot closes `.modal-body` and opens `.modal-foot`.
pub fn modalFoot(h: *Html) !void {
    try h.raw("</div><div class=modal-foot>");
}

/// modalFootDefault emits Go's default footer: a single Close button. "Close" is a HARDCODED
/// ENGLISH literal in components.go modal() (not an i18n key) - replicated verbatim for parity.
pub fn modalFootDefault(h: *Html) !void {
    try btn(h, "Close", "outline", "modal-close", "");
}

/// modalClose closes `.modal-foot` + the dialog.
pub fn modalClose(h: *Html) !void {
    try h.raw("</div></div>");
}

test "modal brackets match Go modal() with the default footer" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try modalOpen(&h, "Re&locate \"missing\"");
    try h.raw("BODY");
    try modalFoot(&h);
    try modalFootDefault(&h);
    try modalClose(&h);
    try std.testing.expectEqualStrings("<div class=modal-scrim data-act=modal-close></div>" ++
        "<div class=modal role=dialog><div class=modal-head><h3 class=modal-title>" ++
        "Re&amp;locate &#34;missing&#34;</h3>" ++
        "<button class=modal-x data-act=modal-close aria-label=Close>✕</button></div>" ++
        "<div class=modal-body>BODY</div><div class=modal-foot>" ++
        "<button class=\"rp-btn rp-btn--outline\" data-act=\"modal-close\">Close</button>" ++
        "</div></div>", h.b.items);
}
// --- end libviews ---

// --- settings-sub ---
// The settings sub-view bodies (gridfix, gridfix model, account bridge, update flow) reuse
// everything above (hint/btn*/btnGated/fieldOf/toggleOf/toggleRowTip/statusOf/selectBox/
// progressBar/section*/emptyState) — only the dialog-style list row was missing.

/// listRowOpen/listRowClose bracket a dialog list entry (Go listRow, settings_actions.go):
/// title + optional sub-line, then the trailing action buttons the caller writes.
pub fn listRowOpen(h: *Html, title: []const u8, sub: []const u8) !void {
    try h.raw("<div class=set-listrow><div class=set-listmain>");
    try h.esc(title);
    if (sub.len != 0) {
        try h.raw("<div class=set-listsub>");
        try h.esc(sub);
        try h.raw("</div>");
    }
    try h.raw("</div><div class=irow-actions>");
}

pub fn listRowClose(h: *Html) !void {
    try h.raw("</div></div>");
}

test "listRow with and without a sub-line" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try listRowOpen(&h, "Chrome on <Studio>", "lan · expires 2026-08-01 12:00");
    try btn(&h, "Revoke", "destructive", "bridge-revoke:p1", "");
    try listRowClose(&h);
    try std.testing.expectEqualStrings("<div class=set-listrow><div class=set-listmain>Chrome on &lt;Studio&gt;" ++
        "<div class=set-listsub>lan · expires 2026-08-01 12:00</div></div><div class=irow-actions>" ++
        "<button class=\"rp-btn rp-btn--destructive\" data-act=\"bridge-revoke:p1\">Revoke</button></div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try listRowOpen(&h, "T", "");
    try listRowClose(&h);
    try std.testing.expectEqualStrings("<div class=set-listrow><div class=set-listmain>T</div>" ++
        "<div class=irow-actions></div></div>", h.b.items);
}

// --- end settings-sub ---

// --- dialogs-a ---
// Wave-4 dialog sweep: the shape most confirm/picker/context dialogs share, plus the two
// form-modal inputs (Go hiddenField/labeledInput, library_actions.go). Additive only.

/// Choice is the message + button-list dialog: a confirm, a format picker or a row context
/// menu. hasMsg is explicit (a blank message still emits an empty `.np-artist`, like Go);
/// msgRaw marks a message Go splices UNESCAPED (source literals with quotes around an
/// already-escaped value); inBody puts the btn-row inside `.modal-body`, so the footer is
/// Go's default Close button instead of the buttons.
pub const Choice = struct {
    title: []const u8 = "",
    msg: []const u8 = "",
    msgRaw: bool = false,
    hasMsg: bool = false,
    btns: []const Btn = &.{},
    inBody: bool = false,
};

/// choiceDialog renders a whole Choice dialog (Go modal(title, body, footer)).
pub fn choiceDialog(h: *Html, st: Choice) !void {
    try modalOpen(h, st.title);
    if (st.hasMsg) {
        try h.raw("<div class=np-artist>");
        if (st.msgRaw) try h.raw(st.msg) else try h.esc(st.msg);
        try h.raw("</div>");
    }
    if (st.inBody) try btnRowOf(h, st.btns);
    try modalFoot(h);
    if (st.inBody) try modalFootDefault(h) else try btnRowOf(h, st.btns);
    try modalClose(h);
}

/// hiddenField mirrors Go hiddenField: a form's carried-through value.
pub fn hiddenField(h: *Html, name: []const u8, val: []const u8) !void {
    try h.raw("<input type=hidden name=\"");
    try h.esc(name);
    try h.raw("\" value=\"");
    try h.esc(val);
    try h.raw("\">");
}

/// labeledInput mirrors Go labeledInput: a NAMED input for a `<form data-act=…>` modal
/// (the value arrives in m.Form, not via data-act). dl = Go strings.ToLower(label).
pub fn labeledInput(h: *Html, name: []const u8, label: []const u8, dl: []const u8, val: []const u8) !void {
    try h.raw("<div class=pb-field data-label=");
    try h.attrQ(dl);
    try h.raw("><div class=pb-label>");
    try h.esc(label);
    try h.raw("</div><input class=field-input name=\"");
    try h.esc(name);
    try h.raw("\" value=\"");
    try h.esc(val);
    try h.raw("\"></div>");
}

test "choiceDialog: buttons in the footer (confirm shape)" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try choiceDialog(&h, .{
        .title = "Remove capture",
        .msg = "Remove the capture \"set.ogg\" from the library?",
        .msgRaw = true,
        .hasMsg = true,
        .btns = &.{ .{ .label = "Remove", .variant = "outline", .act = "pub-capdel-do:c1" }, .{ .label = "Cancel", .variant = "ghost", .act = "modal-close" } },
    });
    try std.testing.expectEqualStrings("<div class=modal-scrim data-act=modal-close></div>" ++
        "<div class=modal role=dialog><div class=modal-head><h3 class=modal-title>Remove capture</h3>" ++
        "<button class=modal-x data-act=modal-close aria-label=Close>✕</button></div>" ++
        "<div class=modal-body><div class=np-artist>Remove the capture \"set.ogg\" from the library?</div></div>" ++
        "<div class=modal-foot><div class=btn-row>" ++
        "<button class=\"rp-btn rp-btn--outline\" data-act=\"pub-capdel-do:c1\">Remove</button>" ++
        "<button class=\"rp-btn rp-btn--ghost\" data-act=\"modal-close\">Cancel</button></div></div></div>", h.b.items);
}

test "choiceDialog: buttons in the body keep Go's default footer; msg escapes when not raw" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try choiceDialog(&h, .{
        .title = "T",
        .msg = "a&b",
        .hasMsg = true,
        .inBody = true,
        .btns = &.{.{ .label = "Text (.txt)", .variant = "primary", .act = "pub-exportfmt:r1\x1ftxt" }},
    });
    try std.testing.expectEqualStrings("<div class=modal-scrim data-act=modal-close></div>" ++
        "<div class=modal role=dialog><div class=modal-head><h3 class=modal-title>T</h3>" ++
        "<button class=modal-x data-act=modal-close aria-label=Close>✕</button></div>" ++
        "<div class=modal-body><div class=np-artist>a&amp;b</div><div class=btn-row>" ++
        "<button class=\"rp-btn rp-btn--primary\" data-act=\"pub-exportfmt:r1\x1ftxt\">Text (.txt)</button>" ++
        "</div></div><div class=modal-foot>" ++
        "<button class=\"rp-btn rp-btn--outline\" data-act=\"modal-close\">Close</button></div></div>", h.b.items);
}

test "choiceDialog: no message = context-menu shape (btn-row only body)" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try choiceDialog(&h, .{ .title = "track.mp3", .inBody = true, .btns = &.{} });
    try std.testing.expectEqualStrings("<div class=modal-scrim data-act=modal-close></div>" ++
        "<div class=modal role=dialog><div class=modal-head><h3 class=modal-title>track.mp3</h3>" ++
        "<button class=modal-x data-act=modal-close aria-label=Close>✕</button></div>" ++
        "<div class=modal-body><div class=btn-row></div></div><div class=modal-foot>" ++
        "<button class=\"rp-btn rp-btn--outline\" data-act=\"modal-close\">Close</button></div></div>", h.b.items);
}

test "hiddenField + labeledInput match the Go form-modal inputs" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try hiddenField(&h, "id", "r&1");
    try labeledInput(&h, "name", "Set n\"ame", "set n\"ame", "Live & loud");
    try std.testing.expectEqualStrings("<input type=hidden name=\"id\" value=\"r&amp;1\">" ++
        "<div class=pb-field data-label=\"set n&#34;ame\"><div class=pb-label>Set n&#34;ame</div>" ++
        "<input class=field-input name=\"name\" value=\"Live &amp; loud\"></div>", h.b.items);
}

// --- end dialogs-a ---

// --- phaseb-loud ---
// Phase B-1a: THE shared loudness block (Go components.go loudnessFields) as a structured
// component. It used to ride through four state contracts (library encode builder, export
// preset editor, automation transcode step, player export pane) as pre-rendered raw markup.
//
// pbField MOVED here from library_kit.zig (which now aliases both, like it already aliases
// Select/Btn/Tab): the loudness block needs it and components.zig cannot import library_kit.zig
// without a cycle - so the base kit owns the markup and the library kit re-exports it. ONE
// markup source, mirroring Go where pbFieldExDL is the single source both paths call.

/// PBField is a pbFieldEx() call as state (dl = Go strings.ToLower(label)).
pub const PBField = struct {
    label: []const u8 = "",
    dl: []const u8 = "",
    act: []const u8 = "",
    value: []const u8 = "",
    inputType: []const u8 = "",
    ph: []const u8 = "",
    hint: []const u8 = "",
};

/// pbField mirrors Go pbFieldExDL: labelled input with optional hint + placeholder.
pub fn pbField(h: *Html, f: PBField) !void {
    try h.raw("<div class=pb-field data-label=");
    try h.attrQ(f.dl);
    try h.raw("><div class=pb-label>");
    try h.esc(f.label);
    try h.raw("</div><input class=field-input type=\"");
    try h.raw(if (f.inputType.len == 0) "text" else f.inputType);
    try h.raw("\" value=\"");
    try h.esc(f.value);
    try h.raw("\" data-act=\"");
    try h.esc(f.act);
    try h.raw("\"");
    if (f.ph.len != 0) {
        try h.raw(" placeholder=\"");
        try h.esc(f.ph);
        try h.raw("\"");
    }
    try h.raw(">");
    if (f.hint.len != 0) {
        try h.raw("<div class=pb-hint>");
        try h.esc(f.hint);
        try h.raw("</div>");
    }
    try h.raw("</div>");
}

/// LoudChip is one industry-target quick-pick chip (compact layout only). label/val/title are
/// final strings - Go formats the "%g|%g" I|TP payload and compresses the chip text, so no
/// float crosses the ABI for Zig to format.
pub const LoudChip = struct {
    label: []const u8 = "",
    val: []const u8 = "",
    title: []const u8 = "",
    active: bool = false,
};

/// Loud is the shared loudness block as state (Go loudSt). toggle.on gates the whole body -
/// the same single source Go uses (o.vals.On drives both the switch and the branch), so an
/// off block is a bare `.pb-grp` with just the switch. compact = the dense variant: industry
/// quick-pick chips + inline targets + raise chip on one wrap row, instead of full-width
/// stacked builder fields. The tooltip is STRUCTURED since phase B-1b (tipSt, dual-field bridge
/// over the legacy raw tip - the ONE non-append-only edit this shard makes to another topic's
/// block, unavoidable: the field it replaces lives here); extra stays RAW markup, the caller owns
/// extraHTML (the export surface's gain-plan line + pre-listen toggle, which collapse with the
/// switch). hasWarn is explicit - a blank i18n string must not switch arms.
pub const Loud = struct {
    compact: bool = false,
    toggle: Toggle = .{},
    tip: []const u8 = "", // legacy pre-rendered tooltip markup (bridge)
    tipSt: ?Tip = null, // structured tooltip — wins over tip
    chipAct: []const u8 = "",
    chips: []const LoudChip = &.{},
    iField: PBField = .{},
    tpField: PBField = .{},
    raise: Toggle = .{},
    hasWarn: bool = false,
    warn: []const u8 = "",
    extra: []const u8 = "",
};

/// loudnessFields mirrors Go loudSt.html() byte-for-byte.
pub fn loudnessFields(h: *Html, st: Loud) !void {
    try h.raw(if (st.compact) "<div class=\"pb-grp pb-grp--compact\">" else "<div class=\"pb-grp\">");
    // toggleRowTip takes the tooltip as a STRING: render the structured card into a scratch
    // buffer rather than duplicating the switch-row markup here.
    var tb = Html.init(h.a);
    defer tb.deinit();
    try tipOr(&tb, st.tipSt, st.tip);
    try toggleRowTip(h, st.toggle.label, st.toggle.dl, st.toggle.act, st.toggle.on, tb.b.items);
    if (st.toggle.on) {
        if (st.compact) {
            try h.raw("<div class=lt-chips>");
            for (st.chips) |ch| {
                try h.raw(if (ch.active) "<button class=\"lt-chip active\" data-act=" else "<button class=\"lt-chip\" data-act=");
                try h.attrQ(st.chipAct);
                try h.raw(" data-val=");
                try h.attrQ(ch.val);
                try h.raw(" title=");
                try h.attrQ(ch.title);
                try h.raw(">");
                try h.esc(ch.label);
                try h.raw("</button>");
            }
            try h.raw("</div><div class=lt-fields><span class=lt-field>");
            try pbField(h, st.iField);
            try h.raw("</span><span class=lt-field>");
            try pbField(h, st.tpField);
            try h.raw("</span><span class=lt-raise>");
            try toggleOf(h, st.raise);
            try h.raw("</span></div>");
        } else {
            try pbField(h, st.iField);
            try pbField(h, st.tpField);
            try toggleOf(h, st.raise);
        }
        if (st.hasWarn) try hint(h, "warn", st.warn);
        try h.raw(st.extra);
    }
    try h.raw("</div>");
}

test "pbField mirrors Go pbFieldExDL (moved from library_kit)" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try pbField(&h, .{ .label = "CRF", .dl = "crf", .act = "lib-pf:crf", .value = "2\"3", .inputType = "number", .hint = "lower = better" });
    try std.testing.expectEqualStrings("<div class=pb-field data-label=\"crf\"><div class=pb-label>CRF</div>" ++
        "<input class=field-input type=\"number\" value=\"2&#34;3\" data-act=\"lib-pf:crf\">" ++
        "<div class=pb-hint>lower = better</div></div>", h.b.items);
}

test "loudnessFields: switch off = the bare group (nothing behind it)" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try loudnessFields(&h, .{
        .toggle = .{ .label = "Normalize loudness", .dl = "normalize loudness", .act = "lib-pf:loudon" },
        .tip = "<span class=tip>?</span>",
        // a populated body must stay unrendered while the switch is off
        .iField = .{ .label = "Target", .dl = "target", .act = "lib-pf:loudi", .value = "-14", .inputType = "number" },
        .hasWarn = true,
        .warn = "needs a re-encode",
        .extra = "<div class=x></div>",
    });
    try std.testing.expectEqualStrings("<div class=\"pb-grp\">" ++
        "<label class=row data-label=\"normalize loudness\"><span class=row-label>Normalize loudness" ++
        "<span class=tip>?</span></span><span class=switch><input type=checkbox data-act=\"lib-pf:loudon\" " ++
        "data-value=\"false\"><span class=switch-track></span></span></label></div>", h.b.items);
}

test "loudnessFields: stacked builder layout (hint on the target, warn chip, raw extra)" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try loudnessFields(&h, .{
        .toggle = .{ .label = "Normalize loudness", .dl = "normalize loudness", .act = "lib-pf:loudon", .on = true },
        .tip = "<span class=tip data-topic=enc-loudness>?</span>",
        .iField = .{ .label = "Target loudness (LUFS)", .dl = "target loudness (lufs)", .act = "lib-pf:loudi", .value = "-14", .inputType = "number", .hint = "-14 suits streaming" },
        .tpField = .{ .label = "True peak (dBTP)", .dl = "true peak (dbtp)", .act = "lib-pf:loudtp", .value = "-1", .inputType = "number" },
        .raise = .{ .label = "Raise quiet only", .dl = "raise quiet only", .act = "lib-pf:loudraise", .on = true },
        .hasWarn = true,
        .warn = "This codec copies audio & can't normalize",
        .extra = "<span data-x=\"a&b\">raw</span>",
    });
    try std.testing.expectEqualStrings("<div class=\"pb-grp\">" ++
        "<label class=row data-label=\"normalize loudness\"><span class=row-label>Normalize loudness" ++
        "<span class=tip data-topic=enc-loudness>?</span></span><span class=switch>" ++
        "<input type=checkbox checked data-act=\"lib-pf:loudon\" data-value=\"true\">" ++
        "<span class=switch-track></span></span></label>" ++
        "<div class=pb-field data-label=\"target loudness (lufs)\"><div class=pb-label>Target loudness (LUFS)</div>" ++
        "<input class=field-input type=\"number\" value=\"-14\" data-act=\"lib-pf:loudi\">" ++
        "<div class=pb-hint>-14 suits streaming</div></div>" ++
        "<div class=pb-field data-label=\"true peak (dbtp)\"><div class=pb-label>True peak (dBTP)</div>" ++
        "<input class=field-input type=\"number\" value=\"-1\" data-act=\"lib-pf:loudtp\"></div>" ++
        "<label class=row data-label=\"raise quiet only\"><span class=row-label>Raise quiet only</span>" ++
        "<span class=switch><input type=checkbox checked data-act=\"lib-pf:loudraise\" data-value=\"true\">" ++
        "<span class=switch-track></span></span></label>" ++
        "<span class=\"hint hint--warn\">This codec copies audio &amp; can&#39;t normalize</span>" ++
        "<span data-x=\"a&b\">raw</span></div>", h.b.items);
}

test "loudnessFields: compact layout - chips, placeholders, no warn" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try loudnessFields(&h, .{
        .compact = true,
        .toggle = .{ .label = "Normalize", .dl = "normalize", .act = "mp-loud:pub\x1f0\x1floudon", .on = true },
        .tip = "",
        .chipAct = "mp-loud:pub\x1f0\x1floudtarget",
        .chips = &.{
            .{ .label = "-14 Streaming", .val = "-14|-1", .title = "Streaming −14 LUFS (Spotify · YouTube)", .active = true },
            .{ .label = "-8 Club", .val = "-8|-0.3", .title = "Club / DJ master −8 LUFS (hot)" },
        },
        .iField = .{ .label = "Target", .dl = "target", .act = "mp-loud:pub\x1f0\x1floudi", .inputType = "number", .ph = "-14" },
        .tpField = .{ .label = "True peak", .dl = "true peak", .act = "mp-loud:pub\x1f0\x1floudtp", .inputType = "number", .ph = "-1" },
        .raise = .{ .label = "Raise quiet only", .dl = "raise quiet only", .act = "mp-loud:pub\x1f0\x1floudraise" },
    });
    try std.testing.expectEqualStrings("<div class=\"pb-grp pb-grp--compact\">" ++
        "<label class=row data-label=\"normalize\"><span class=row-label>Normalize</span><span class=switch>" ++
        "<input type=checkbox checked data-act=\"mp-loud:pub\x1f0\x1floudon\" data-value=\"true\">" ++
        "<span class=switch-track></span></span></label>" ++
        "<div class=lt-chips>" ++
        "<button class=\"lt-chip active\" data-act=\"mp-loud:pub\x1f0\x1floudtarget\" data-val=\"-14|-1\" " ++
        "title=\"Streaming −14 LUFS (Spotify · YouTube)\">-14 Streaming</button>" ++
        "<button class=\"lt-chip\" data-act=\"mp-loud:pub\x1f0\x1floudtarget\" data-val=\"-8|-0.3\" " ++
        "title=\"Club / DJ master −8 LUFS (hot)\">-8 Club</button></div>" ++
        "<div class=lt-fields><span class=lt-field>" ++
        "<div class=pb-field data-label=\"target\"><div class=pb-label>Target</div>" ++
        "<input class=field-input type=\"number\" value=\"\" data-act=\"mp-loud:pub\x1f0\x1floudi\" placeholder=\"-14\"></div>" ++
        "</span><span class=lt-field>" ++
        "<div class=pb-field data-label=\"true peak\"><div class=pb-label>True peak</div>" ++
        "<input class=field-input type=\"number\" value=\"\" data-act=\"mp-loud:pub\x1f0\x1floudtp\" placeholder=\"-1\"></div>" ++
        "</span><span class=lt-raise>" ++
        "<label class=row data-label=\"raise quiet only\"><span class=row-label>Raise quiet only</span>" ++
        "<span class=switch><input type=checkbox data-act=\"mp-loud:pub\x1f0\x1floudraise\" data-value=\"false\">" ++
        "<span class=switch-track></span></span></label></span></div></div>", h.b.items);
}

// --- end phaseb-loud ---

// --- phaseb-tip ---
// Port of internal/webui/tooltip.go renderTipSt: the shared long-form help card. Everything
// locale- and registry-dependent (helpTopics prose, virtualMIDILinks, the i18n.T per keybind row
// and group header, the kbEmph verb split, the body paragraph split) is resolved Go-side into
// tipSt — this renderer only composes markup. Gate: internal/webui/zigui_golden_tip_test.go.
//
// Help texts are LONG and verbose BY DESIGN (owner directive): never truncate, never elide.

/// TipChip is one combo token of a keybind row (webui tipChipSt): sep = a "+"/"/" separator span
/// (trusted literal, raw like Go), else an escaped key-cap chip.
pub const TipChip = struct {
    text: []const u8 = "",
    sep: bool = false,
};

/// TipKb is one resolved keybind row (webui tipKbSt). hasGroup is EXPLICIT: the section dedup ran
/// on the i18n key Go-side, so an empty resolved header still renders.
pub const TipKb = struct {
    hasGroup: bool = false,
    group: []const u8 = "",
    chips: []const TipChip = &.{},
    verb: []const u8 = "",
    rest: []const u8 = "",
};

/// TipLink is one authoritative-source link at the card's foot (webui tipLinkSt).
pub const TipLink = struct {
    label: []const u8 = "",
    url: []const u8 = "",
};

/// Tip is one resolved tooltip (webui tipSt).
pub const Tip = struct {
    id: []const u8 = "",
    title: []const u8 = "",
    keys: []const TipKb = &.{},
    paras: []const []const u8 = &.{},
    links: []const TipLink = &.{},
};

/// renderTip mirrors Go renderTipSt. Markup is a pure-CSS "checkbox pin"; the hidden checkbox
/// carries the ctl data-label (tt-<id>), so ids stay addressable from `ctl set`.
pub fn renderTip(h: *Html, t: Tip) !void {
    try h.raw("<label class=tt data-label=\"tt-");
    try h.esc(t.id);
    try h.raw("\" aria-label=\"About: ");
    try h.esc(t.title);
    try h.raw("\" tabindex=0>");
    try h.raw("<input type=checkbox class=tt-x tabindex=-1>");
    // lucide-style info glyph; currentColor follows muted/hover/pinned states.
    try h.raw("<svg class=tt-ic viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"2\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\">" ++
        "<circle cx=\"12\" cy=\"12\" r=\"10\"/><path d=\"M12 16v-4\"/><path d=\"M12 8h.01\"/></svg>");
    // tt-card = transparent positioner (+hover bridge), portaled to #__ttlayer while shown;
    // tt-in = visual panel, scrolls internally when tall (~60vh cap).
    try h.raw("<span class=tt-card role=tooltip><span class=tt-in>");
    try h.raw("<b class=tt-title>");
    try h.esc(t.title);
    try h.raw("</b>");
    if (t.keys.len != 0) { // keybind grid: section header + (combo chips -> action) rows
        try h.raw("<span class=tt-kb>");
        for (t.keys) |r| {
            if (r.hasGroup) {
                try h.raw("<span class=tt-kb-group>");
                try h.esc(r.group);
                try h.raw("</span>");
            }
            try h.raw("<span class=tt-kb-keys>");
            for (r.chips) |ch| {
                if (ch.sep) {
                    try h.raw("<span class=tt-kb-sep>");
                    try h.raw(ch.text);
                    try h.raw("</span>");
                    continue;
                }
                try h.raw("<kbd class=tt-kbd>");
                try h.esc(ch.text);
                try h.raw("</kbd>");
            }
            try h.raw("</span><span class=tt-kb-act><b class=tt-kb-verb>");
            try h.esc(r.verb);
            try h.raw("</b>");
            try h.esc(r.rest);
            try h.raw("</span>");
        }
        try h.raw("</span>");
    }
    for (t.paras) |p| {
        try h.raw("<span class=tt-p>");
        try h.esc(p);
        try h.raw("</span>");
    }
    if (t.links.len != 0) {
        try h.raw("<span class=tt-links>");
        for (t.links) |l| {
            try h.raw("<a class=tt-link data-act=open-url data-val=\"");
            try h.esc(l.url);
            try h.raw("\">");
            try h.esc(l.label);
            try h.raw(" \u{2197}</a>");
        }
        try h.raw("</span>");
    }
    try h.raw("</span></span></label>");
}

/// tipOr is the dual-field bridge (Go tipOr): structured state wins, else the pre-rendered raw
/// markup an un-migrated Go builder still ships.
pub fn tipOr(h: *Html, t: ?Tip, raw_tip: []const u8) !void {
    if (t) |s| return renderTip(h, s);
    try h.raw(raw_tip);
}

/// tipBuf resolves the dual-field bridge into a SCRATCH buffer, for the many primitives that take
/// the tooltip as a string (sectionOpenTip, cardOpen, cardLabel, toggleRowTip, fieldEx, ss-label).
/// The caller owns it: `var tb = try c.tipBuf(h, s.tipSt, s.tip); defer tb.deinit();`. One
/// allocation per tooltip - NEVER re-emit a primitive's markup to save it, that is how the two
/// renderers drift.
pub fn tipBuf(h: *Html, t: ?Tip, raw_tip: []const u8) !Html {
    var b = Html.init(h.a);
    errdefer b.deinit();
    try tipOr(&b, t, raw_tip);
    return b;
}

test "tipBuf: caller-owned scratch buffer for string-taking primitives" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    var tb = try tipBuf(&h, .{ .id = "x", .title = "X" }, "RAW");
    defer tb.deinit();
    try std.testing.expect(std.mem.indexOf(u8, tb.b.items, "tt-x") != null);
    var rb = try tipBuf(&h, null, "RAW");
    defer rb.deinit();
    try std.testing.expectEqualStrings("RAW", rb.b.items);
}

/// tt_glyph is the fixed pin-checkbox + info-glyph prologue every card shares (test literal).
const tt_glyph = "<input type=checkbox class=tt-x tabindex=-1>" ++
    "<svg class=tt-ic viewBox=\"0 0 24 24\" fill=\"none\" stroke=\"currentColor\" stroke-width=\"2\" stroke-linecap=\"round\" stroke-linejoin=\"round\" aria-hidden=\"true\">" ++
    "<circle cx=\"12\" cy=\"12\" r=\"10\"/><path d=\"M12 16v-4\"/><path d=\"M12 8h.01\"/></svg>";

test "renderTip: title + paragraphs only (no grid, no links)" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderTip(&h, .{ .id = "icecast", .title = "Set capture", .paras = &.{ "First & only.", "Second <p>." } });
    try std.testing.expectEqualStrings("<label class=tt data-label=\"tt-icecast\" aria-label=\"About: Set capture\" tabindex=0>" ++
        tt_glyph ++
        "<span class=tt-card role=tooltip><span class=tt-in><b class=tt-title>Set capture</b>" ++
        "<span class=tt-p>First &amp; only.</span><span class=tt-p>Second &lt;p&gt;.</span>" ++
        "</span></span></label>", h.b.items);
}

test "renderTip: escapes the id/title into data-label + aria-label" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderTip(&h, .{ .id = "a\"b&c", .title = "T <\"x\">" });
    try std.testing.expectEqualStrings("<label class=tt data-label=\"tt-a&#34;b&amp;c\" aria-label=\"About: T &lt;&#34;x&#34;&gt;\" tabindex=0>" ++
        tt_glyph ++
        "<span class=tt-card role=tooltip><span class=tt-in><b class=tt-title>T &lt;&#34;x&#34;&gt;</b>" ++
        "</span></span></label>", h.b.items);
}

test "renderTip: keybind grid - group header, separators, verb split" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderTip(&h, .{
        .id = "cue-edit",
        .title = "Cue editor",
        .keys = &.{
            .{ .hasGroup = true, .group = "Navigate", .chips = &.{ .{ .text = "\u{2190}" }, .{ .text = "/", .sep = true }, .{ .text = "\u{2192}" } }, .verb = "Step", .rest = " one beat" },
            .{ .chips = &.{ .{ .text = "Shift" }, .{ .text = "+", .sep = true }, .{ .text = "Right-click" } }, .verb = "Remove" },
        },
        .paras = &.{"Body."},
    });
    try std.testing.expectEqualStrings("<label class=tt data-label=\"tt-cue-edit\" aria-label=\"About: Cue editor\" tabindex=0>" ++
        tt_glyph ++
        "<span class=tt-card role=tooltip><span class=tt-in><b class=tt-title>Cue editor</b>" ++
        "<span class=tt-kb><span class=tt-kb-group>Navigate</span>" ++
        "<span class=tt-kb-keys><kbd class=tt-kbd>\u{2190}</kbd><span class=tt-kb-sep>/</span><kbd class=tt-kbd>\u{2192}</kbd></span>" ++
        "<span class=tt-kb-act><b class=tt-kb-verb>Step</b> one beat</span>" ++
        "<span class=tt-kb-keys><kbd class=tt-kbd>Shift</kbd><span class=tt-kb-sep>+</span><kbd class=tt-kbd>Right-click</kbd></span>" ++
        "<span class=tt-kb-act><b class=tt-kb-verb>Remove</b></span></span>" ++
        "<span class=tt-p>Body.</span></span></span></label>", h.b.items);
}

test "renderTip: link list escapes url + label and keeps the arrow glyph" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderTip(&h, .{ .id = "x", .title = "X", .links = &.{
        .{ .label = "EBU R128", .url = "https://tech.ebu.ch/publications/r128" },
        .{ .label = "A & B", .url = "https://x/?a&b=\"c\"" },
    } });
    try std.testing.expectEqualStrings("<label class=tt data-label=\"tt-x\" aria-label=\"About: X\" tabindex=0>" ++
        tt_glyph ++
        "<span class=tt-card role=tooltip><span class=tt-in><b class=tt-title>X</b>" ++
        "<span class=tt-links>" ++
        "<a class=tt-link data-act=open-url data-val=\"https://tech.ebu.ch/publications/r128\">EBU R128 \u{2197}</a>" ++
        "<a class=tt-link data-act=open-url data-val=\"https://x/?a&amp;b=&#34;c&#34;\">A &amp; B \u{2197}</a>" ++
        "</span></span></span></label>", h.b.items);
}

test "tipOr: structured wins, absent falls back to the raw string" {
    var a = Html.init(std.testing.allocator);
    defer a.deinit();
    try tipOr(&a, null, "<span class=raw></span>");
    try std.testing.expectEqualStrings("<span class=raw></span>", a.b.items);

    var b = Html.init(std.testing.allocator);
    defer b.deinit();
    try tipOr(&b, .{ .id = "x", .title = "X" }, "<span class=raw></span>");
    try std.testing.expect(std.mem.indexOf(u8, b.b.items, "tt-x") != null);
}

/// SsLabel is a smart-select ss-label as state (webui ssLabelSt): escaped label text + its
/// structured tooltip. THE one markup source for the label span - every select-with-tooltip
/// surface (settings, library encode builder, automations selraw, midictl) renders through it
/// instead of shipping the span as pre-rendered markup.
pub const SsLabel = struct {
    text: []const u8 = "",
    tip: ?Tip = null,
};

/// ssLabel mirrors Go ssLabelSt.html().
pub fn ssLabel(h: *Html, l: SsLabel) !void {
    try h.raw("<span class=ss-label>");
    try h.esc(l.text);
    if (l.tip) |t| try renderTip(h, t);
    try h.raw("</span>");
}

/// selectBoxTipOf is selectBoxRaw fed from a STRUCTURED ss-label (Go selHTMLRaw + ssLabelSt.html):
/// the label renders into a scratch buffer so the ss-field markup stays single-sourced.
pub fn selectBoxTipOf(h: *Html, s: Select, l: SsLabel) !void {
    var lb = Html.init(h.a);
    defer lb.deinit();
    try ssLabel(&lb, l);
    try selectBoxRaw(h, s, lb.b.items);
}

/// selectBoxTipOr is the ss-label dual-field bridge (Go ssSelHTML): structured label wins, else a
/// legacy pre-rendered one, else the plain label the select state carries.
pub fn selectBoxTipOr(h: *Html, s: Select, lbl: ?SsLabel, raw_label: []const u8) !void {
    if (lbl) |l| return selectBoxTipOf(h, s, l);
    if (raw_label.len != 0) return selectBoxRaw(h, s, raw_label);
    return selectBox(h, s);
}

test "ssLabel: escapes the text and appends the structured card" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try ssLabel(&h, .{ .text = "A & B", .tip = .{ .id = "midi-thru", .title = "T" } });
    try std.testing.expectEqualStrings("<span class=ss-label>A &amp; B" ++
        "<label class=tt data-label=\"tt-midi-thru\" aria-label=\"About: T\" tabindex=0>" ++ tt_glyph ++
        "<span class=tt-card role=tooltip><span class=tt-in><b class=tt-title>T</b></span></span></label>" ++
        "</span>", h.b.items);
}

test "ssLabel: no tooltip renders the bare span" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try ssLabel(&h, .{ .text = "Port" });
    try std.testing.expectEqualStrings("<span class=ss-label>Port</span>", h.b.items);
}

test "Tip parses from JSON with omitted slices (Go omitempty contract)" {
    const js = "{\"id\":\"x\",\"title\":\"T\",\"paras\":[\"a\",\"b\"]," ++
        "\"keys\":[{\"hasGroup\":true,\"group\":\"G\",\"chips\":[{\"text\":\"+\",\"sep\":true}],\"verb\":\"V\",\"rest\":\" w\"}]," ++
        "\"links\":[{\"label\":\"L\",\"url\":\"U\"}]}";
    const p = try std.json.parseFromSlice(Tip, std.testing.allocator, js, .{ .ignore_unknown_fields = true });
    defer p.deinit();
    try std.testing.expectEqual(@as(usize, 2), p.value.paras.len);
    try std.testing.expectEqualStrings("b", p.value.paras[1]);
    try std.testing.expectEqual(@as(usize, 1), p.value.keys.len);
    try std.testing.expect(p.value.keys[0].chips[0].sep);
    try std.testing.expectEqualStrings("U", p.value.links[0].url);

    const bare = try std.json.parseFromSlice(Tip, std.testing.allocator, "{\"id\":\"y\",\"title\":\"\"}", .{ .ignore_unknown_fields = true });
    defer bare.deinit();
    try std.testing.expectEqual(@as(usize, 0), bare.value.paras.len);
    try std.testing.expectEqual(@as(usize, 0), bare.value.keys.len);
}

// --- end phaseb-tip ---
