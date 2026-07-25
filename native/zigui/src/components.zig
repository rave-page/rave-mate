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
