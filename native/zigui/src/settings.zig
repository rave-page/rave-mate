//! Settings tab renderer — byte-identical to internal/webui/render_settings_html.go
//! (settingsHTML / setContentHTML / setCardHTML / setBlockHTML / setStatusHTML).
//!
//! Go resolves EVERYTHING impure into state: config + service snapshots + cached fs/PATH/device
//! probes, i18n strings, `strings.ToLower` data-labels, every number (strconv/trimNum), the
//! smart-select registration + filtering, and the search match (which folds + strips tags off the
//! Go-rendered card, so the query never reaches Zig). Card bodies arrive as BLOCK LISTS; this
//! renderer walks them into the components.zig primitives.
//!
//! Tooltips cross as STRUCTURED state (`tipSt`, components.zig renderTip) since phase B1b; the
//! legacy raw `tip` string stays as the dual-field bridge for state this file shares with
//! un-migrated builders and wins nothing when tipSt is present.
//! Trusted raw markup still passed through verbatim (`raw`/`noteRaw`/`region`). The four card
//! bodies owned by other files (gridfix, gridfix model, account bridge, the #inst-update region)
//! used to ride here as raw HTML too — they now cross as STRUCTURED state and render through
//! settings_sub.zig (block kinds gridfix | gridfixmodel | bridge | updregion).
//! Element ids (stset-<id>, stnav-<id>, set-<sec>, inst-<key>, data-act=toggle:<id>, form acts)
//! are trusted literals spliced unescaped, exactly as Go does — ctl addressing depends on it.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");
const sub = @import("settings_sub.zig");

/// Status is one card's live status region (webui setStatusSt): variant + terse state line.
pub const Status = struct {
    v: []const u8 = "",
    t: []const u8 = "",
};

/// Input is one raw named <input> inside a set-dlgform (webui setInput). Empty type = no
/// type attribute; name/type are trusted literals.
pub const Input = struct {
    type: []const u8 = "",
    name: []const u8 = "",
    ph: []const u8 = "",
};

/// Kid is one child control of a composite block (webui setKid): k ∈ field|select|amenu|btn.
pub const Kid = struct {
    k: []const u8 = "",
    fld: ?c.Field = null,
    tip: []const u8 = "", // legacy pre-rendered tooltip (bridge)
    tipSt: ?c.Tip = null, // structured tooltip — wins over tip
    sel: ?c.Select = null,
    selLbl: []const u8 = "",
    btn: ?c.Btn = null,
};

/// Block is one card-body block (webui setBlock). k selects the renderer; only that kind's
/// fields are read.
pub const Block = struct {
    k: []const u8 = "",
    text: []const u8 = "",
    html: []const u8 = "",
    tone: []const u8 = "",
    id: []const u8 = "",
    title: []const u8 = "",
    sub: []const u8 = "",
    fld: ?c.Field = null,
    tip: []const u8 = "", // legacy pre-rendered tooltip (bridge)
    tipSt: ?c.Tip = null, // structured tooltip — wins over tip
    tgl: ?c.Toggle = null,
    gate: []const u8 = "",
    kv: ?c.KV = null,
    sel: ?c.Select = null,
    selLbl: []const u8 = "",
    btn: ?c.Btn = null,
    kids: []const Kid = &.{},
    inputs: []const Input = &.{},
    submit: []const u8 = "",
    subVar: []const u8 = "",
    // sub-view bodies owned by other webui files (settings_sub.zig)
    gf: ?sub.GfCard = null,
    gfm: ?sub.GfModel = null,
    brg: ?sub.Bridge = null,
    upd: ?sub.UpdFlow = null,
};

/// Switch is a card header's feature switch (webui setSwitchSt). Non-empty gate = the
/// dependency is missing and the feature is off: disabled switch + warn hint.
pub const Switch = struct {
    label: []const u8 = "",
    on: bool = false,
    gate: []const u8 = "",
};

/// Card is one settings card (webui setCardSt).
pub const Card = struct {
    id: []const u8 = "",
    title: []const u8 = "",
    tip: []const u8 = "", // legacy pre-rendered tooltip (bridge)
    tipSt: ?c.Tip = null, // structured tooltip — wins over tip
    desc: []const u8 = "",
    st: Status = .{},
    tgl: ?Switch = null,
    blocks: []const Block = &.{},
};

/// Nav is one sub-tab pill (webui setNavSt): aggregate status dot + title.
pub const Nav = struct {
    id: []const u8 = "",
    title: []const u8 = "",
    agg: []const u8 = "",
    active: bool = false,
};

/// Sec is one rendered section (webui setSecSt): title in search mode, id+desc in sub-tab mode.
pub const Sec = struct {
    id: []const u8 = "",
    title: []const u8 = "",
    desc: []const u8 = "",
    cards: []const Card = &.{},
};

/// Content is the #set-content pane (webui setContentSt).
pub const Content = struct {
    searching: bool = false,
    noResults: []const u8 = "",
    nav: []const Nav = &.{},
    secs: []const Sec = &.{},
};

/// State is the whole Settings view (webui setState).
pub const State = struct {
    title: []const u8 = "",
    sub: []const u8 = "",
    available: bool = false,
    unavailable: []const u8 = "",
    query: []const u8 = "",
    placeholder: []const u8 = "",
    content: Content = .{},
};

/// render mirrors Go settingsHTML: header + global search box + the patchable content pane.
pub fn render(h: *Html, st: State) !void {
    if (!st.available) {
        try c.panel(h, st.title, "");
        try c.emptyState(h, st.unavailable);
        return;
    }
    try h.raw("<div id=settings-body>");
    try c.panel(h, st.title, st.sub);
    try h.raw("<div class=set-search data-label=\"settings-search\"><input id=set-q class=field-input type=search value=");
    try h.attrQ(st.query);
    try h.raw(" placeholder=");
    try h.attrQ(st.placeholder);
    try h.raw(" data-actinput=settings-search autocomplete=off spellcheck=false></div><div id=set-content>");
    try renderContent(h, st.content);
    try h.raw("</div></div>");
}

/// renderContent mirrors Go setContentHTML (#set-content): search results grouped by section,
/// or the sub-tab pills + the active section's cards.
pub fn renderContent(h: *Html, ct: Content) !void {
    if (ct.searching) {
        for (ct.secs) |s| {
            try c.sectionOpen(h, s.title);
            try h.raw("<div class=set-sec>");
            try cards(h, s.cards);
            try h.raw("</div>");
            try c.sectionClose(h);
        }
        if (ct.secs.len == 0) try c.emptyState(h, ct.noResults);
        return;
    }
    try h.raw("<nav class=set-nav>");
    for (ct.nav) |n| {
        try h.raw("<button class=\"set-navpill");
        if (n.active) try h.raw(" active");
        try h.raw("\" data-act=settings-sec data-val=\"");
        try h.raw(n.id);
        try h.raw("\"><span id=stnav-");
        try h.raw(n.id);
        try h.raw("><span class=\"dot dot--");
        try h.raw(n.agg);
        try h.raw("\"></span></span>");
        try h.esc(n.title);
        try h.raw("</button>");
    }
    try h.raw("</nav>");
    for (ct.secs) |s| {
        try h.raw("<p class=page-sub>");
        try h.esc(s.desc);
        try h.raw("</p><div id=set-");
        try h.raw(s.id);
        try h.raw(" class=set-sec>");
        try cards(h, s.cards);
        try h.raw("</div>");
    }
}

/// renderStatus mirrors Go setStatusHTML (#stset-<id> tick fragment).
pub fn renderStatus(h: *Html, s: Status) !void {
    try h.raw("<span class=\"dot dot--");
    try h.raw(s.v);
    try h.raw("\"></span><span data-value=\"");
    try h.esc(s.t);
    try h.raw("\">");
    try h.esc(s.t);
    try h.raw("</span>");
}

fn cards(h: *Html, list: []const Card) !void {
    for (list) |cd| try card(h, cd);
}

/// card mirrors Go setCardHTML: header (title + tooltip + switch), status region, gate hint,
/// description, body blocks.
pub fn card(h: *Html, cd: Card) !void {
    try h.raw("<div class=\"rp-card\"><div class=set-cardhead><span class=set-title>");
    try h.esc(cd.title);
    try h.raw("</span>");
    try c.tipOr(h, cd.tipSt, cd.tip);
    var gate: []const u8 = "";
    if (cd.tgl) |t| {
        if (t.gate.len != 0) {
            // dependency missing: grey the enable switch + name what to install
            try h.raw("<label class=switch title=");
            try h.attrQ(t.gate);
            try h.raw("><input type=checkbox disabled><span class=switch-track></span></label>");
            gate = t.gate;
        } else {
            try h.raw("<label class=switch title=");
            try h.attrQ(t.label);
            try h.raw("><input type=checkbox");
            if (t.on) try h.raw(" checked");
            try h.raw(" data-act=\"toggle:");
            try h.raw(cd.id);
            try h.raw("\" data-value=\"");
            try h.raw(if (t.on) "true" else "false");
            try h.raw("\"><span class=switch-track></span></label>");
        }
    }
    try h.raw("</div><div class=set-st id=stset-");
    try h.raw(cd.id);
    try h.raw(">");
    try renderStatus(h, cd.st);
    try h.raw("</div>");
    if (gate.len != 0) {
        try h.raw("<div class=set-gate>");
        try c.hint(h, "warn", gate);
        try h.raw("</div>");
    }
    if (cd.desc.len != 0) try note(h, cd.desc);
    for (cd.blocks) |b| try block(h, b);
    try h.raw("</div>");
}

/// note is the muted help/notes line every settings card uses (Go setNote).
fn note(h: *Html, text: []const u8) !void {
    try h.raw("<div class=set-note>");
    try h.esc(text);
    try h.raw("</div>");
}

/// block mirrors Go setBlockHTML: one body block through the shared primitives.
pub fn block(h: *Html, b: Block) !void {
    if (eq(b.k, "note")) return note(h, b.text);
    if (eq(b.k, "noteRaw")) {
        try h.raw("<div class=set-note>");
        try h.raw(b.html);
        try h.raw("</div>");
        return;
    }
    if (eq(b.k, "hint")) return c.hint(h, b.tone, b.text);
    if (eq(b.k, "empty")) return c.emptyState(h, b.text);
    if (eq(b.k, "field")) return field(h, b.fld, b.tip, b.tipSt);
    if (eq(b.k, "toggle")) {
        const t = b.tgl orelse return;
        if (b.gate.len != 0) return c.toggleRowGated(h, t.label, t.dl, t.on, b.gate);
        var tb = Html.init(h.a);
        defer tb.deinit();
        try c.tipOr(&tb, b.tipSt, b.tip);
        return c.toggleRowTip(h, t.label, t.dl, t.act, t.on, tb.b.items);
    }
    if (eq(b.k, "select")) return select(h, b.sel, b.selLbl);
    if (eq(b.k, "amenu")) return amenu(h, b.sel);
    if (eq(b.k, "kv")) {
        if (b.kv) |k| try c.kvOf(h, k);
        return;
    }
    if (eq(b.k, "fpair")) {
        try c.fpairOpen(h);
        try kids(h, b.kids);
        try c.fpairClose(h);
        return;
    }
    if (eq(b.k, "btnrow")) {
        try c.btnRowOpen(h);
        try kids(h, b.kids);
        try c.btnRowClose(h);
        return;
    }
    if (eq(b.k, "pathrow")) {
        try h.raw("<div class=set-pathrow>");
        try field(h, b.fld, "", null);
        if (b.btn) |bt| try c.btnOf(h, bt);
        try h.raw("</div>");
        return;
    }
    if (eq(b.k, "itemrow")) {
        try c.itemRowOpen(h, b.title, b.sub);
        try kids(h, b.kids);
        try c.itemRowClose(h);
        return;
    }
    if (eq(b.k, "install")) {
        try h.raw("<div class=set-install>");
        try note(h, b.text);
        try c.btnRowOpen(h);
        try kids(h, b.kids);
        try c.btnRowClose(h);
        try h.raw("<div id=inst-");
        try h.raw(b.id);
        try h.raw("></div></div>");
        return;
    }
    if (eq(b.k, "installNote")) {
        try h.raw("<div class=set-install>");
        try note(h, b.text);
        try h.raw("</div>");
        return;
    }
    if (eq(b.k, "region")) {
        try h.raw("<div id=");
        try h.raw(b.id);
        try h.raw(">");
        try h.raw(b.html);
        try h.raw("</div>");
        return;
    }
    if (eq(b.k, "form")) return form(h, b);
    if (eq(b.k, "raw")) return h.raw(b.html);
    // sub-view bodies (settings_sub.zig); a missing payload renders nothing, like Go's nil guard
    if (eq(b.k, "gridfix")) {
        if (b.gf) |s| try sub.renderGridfix(h, s);
        return;
    }
    if (eq(b.k, "gridfixmodel")) {
        if (b.gfm) |s| try sub.renderGridfixModel(h, s);
        return;
    }
    if (eq(b.k, "bridge")) {
        if (b.brg) |s| try sub.renderBridge(h, s);
        return;
    }
    if (eq(b.k, "updregion")) {
        try h.raw("<div id=");
        try h.raw(b.id);
        try h.raw(">");
        if (b.upd) |s| try sub.renderUpdFlow(h, s);
        try h.raw("</div>");
        return;
    }
}

fn kids(h: *Html, list: []const Kid) !void {
    for (list) |k| {
        if (eq(k.k, "field")) {
            try field(h, k.fld, k.tip, k.tipSt);
        } else if (eq(k.k, "select")) {
            try select(h, k.sel, k.selLbl);
        } else if (eq(k.k, "amenu")) {
            try amenu(h, k.sel);
        } else if (eq(k.k, "btn")) {
            if (k.btn) |bt| try c.btnOf(h, bt);
        }
    }
}

/// field feeds components.fieldEx, which takes the tooltip as a STRING: render the structured
/// tip into a scratch buffer rather than duplicating fieldEx's markup here.
fn field(h: *Html, f: ?c.Field, tip: []const u8, tipSt: ?c.Tip) !void {
    const fl = f orelse return;
    var tb = Html.init(h.a);
    defer tb.deinit();
    try c.tipOr(&tb, tipSt, tip);
    try c.fieldEx(h, fl.label, fl.dl, fl.act, fl.value, fl.inputType, fl.ph, tb.b.items);
}

/// select renders a resolved smart select; non-empty label_html = pre-rendered ss-label
/// (selectBoxTip, Go selHTMLRaw).
fn select(h: *Html, s: ?c.Select, label_html: []const u8) !void {
    const sel = s orelse return;
    if (label_html.len != 0) return c.selectBoxRaw(h, sel, label_html);
    return c.selectBox(h, sel);
}

/// amenu mirrors Go actionMenu: a label-as-current smart select in an amenu span.
fn amenu(h: *Html, s: ?c.Select) !void {
    const sel = s orelse return;
    try h.raw("<span class=amenu>");
    try c.selectBox(h, sel);
    try h.raw("</span>");
}

/// form mirrors Go setFormHTML: raw named inputs, action buttons, optional literal submit.
fn form(h: *Html, b: Block) !void {
    try h.raw("<form class=set-dlgform data-act=");
    try h.raw(b.id);
    try h.raw(">");
    for (b.inputs) |in| {
        try h.raw("<input class=field-input");
        if (in.type.len != 0) {
            try h.raw(" type=");
            try h.raw(in.type);
        }
        try h.raw(" name=");
        try h.raw(in.name);
        try h.raw(" placeholder=");
        try h.attrQ(in.ph);
        try h.raw("autocomplete=off>");
    }
    try kids(h, b.kids);
    if (b.submit.len != 0) {
        try h.raw("<button class=\"rp-btn rp-btn--");
        try h.raw(b.subVar);
        try h.raw("\" type=submit>");
        try h.esc(b.submit);
        try h.raw("</button>");
    }
    try h.raw("</form>");
}

fn eq(a: []const u8, b: []const u8) bool {
    return std.mem.eql(u8, a, b);
}

test "unavailable view" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .title = "Settings", .unavailable = "Config unavailable" });
    try std.testing.expectEqualStrings("<h1 class=page-title>Settings</h1>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">Config unavailable</div></div>", h.b.items);
}

test "card with gated switch + note block" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const blocks = [_]Block{.{ .k = "note", .text = "n&ote" }};
    try card(&h, .{
        .id = "stt",
        .title = "Speech",
        .st = .{ .v = "off", .t = "not installed" },
        .tgl = .{ .label = "STT", .on = false, .gate = "Install whisper.cpp" },
        .blocks = &blocks,
    });
    try std.testing.expectEqualStrings("<div class=\"rp-card\"><div class=set-cardhead>" ++
        "<span class=set-title>Speech</span>" ++
        "<label class=switch title=\"Install whisper.cpp\"><input type=checkbox disabled>" ++
        "<span class=switch-track></span></label></div>" ++
        "<div class=set-st id=stset-stt><span class=\"dot dot--off\"></span>" ++
        "<span data-value=\"not installed\">not installed</span></div>" ++
        "<div class=set-gate><span class=\"hint hint--warn\">Install whisper.cpp</span></div>" ++
        "<div class=set-note>n&amp;ote</div></div>", h.b.items);
}

test "path row + install block + form" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try block(&h, .{
        .k = "pathrow",
        .fld = .{ .label = "Folder", .dl = "folder", .act = "set:ar-dir", .value = "C:\\R", .inputType = "text" },
        .btn = .{ .label = "Browse…", .variant = "ghost", .act = "pick-dir:set:ar-dir" },
    });
    try std.testing.expectEqualStrings("<div class=set-pathrow>" ++
        "<label class=field data-label=\"folder\"><span class=field-label>Folder</span>" ++
        "<input class=field-input type=text value=\"C:\\R\" data-value=\"C:\\R\" data-act=\"set:ar-dir\"></label>" ++
        "<button class=\"rp-btn rp-btn--ghost\" data-act=\"pick-dir:set:ar-dir\">Browse…</button></div>", h.b.items);

    h.b.clearRetainingCapacity();
    const bs = [_]Kid{.{ .k = "btn", .btn = .{ .label = "Download", .variant = "primary", .act = "settings-install:mpv" } }};
    try block(&h, .{ .k = "install", .id = "mpv", .text = "Not found", .kids = &bs });
    try std.testing.expectEqualStrings("<div class=set-install><div class=set-note>Not found</div>" ++
        "<div class=btn-row><button class=\"rp-btn rp-btn--primary\" data-act=\"settings-install:mpv\">Download</button></div>" ++
        "<div id=inst-mpv></div></div>", h.b.items);

    h.b.clearRetainingCapacity();
    const ins = [_]Input{.{ .type = "password", .name = "key", .ph = "Paste the key" }};
    try block(&h, .{ .k = "form", .id = "settings-rbkey-save", .inputs = &ins, .submit = "Save", .subVar = "primary" });
    try std.testing.expectEqualStrings("<form class=set-dlgform data-act=settings-rbkey-save" ++
        "><input class=field-input type=password name=key placeholder=\"Paste the key\"autocomplete=off>" ++
        "<button class=\"rp-btn rp-btn--primary\" type=submit>Save</button></form>", h.b.items);
}

test "content: nav pills + section grid, search mode" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const nav = [_]Nav{ .{ .id = "account", .title = "Account", .agg = "ok", .active = true }, .{ .id = "system", .title = "Sys&tem", .agg = "off" } };
    const cds = [_]Card{.{ .id = "api", .title = "API", .st = .{ .v = "ok", .t = "https://x/" } }};
    const secs = [_]Sec{.{ .id = "account", .desc = "Sign in & keys", .cards = &cds }};
    try renderContent(&h, .{ .nav = &nav, .secs = &secs });
    try std.testing.expectEqualStrings("<nav class=set-nav>" ++
        "<button class=\"set-navpill active\" data-act=settings-sec data-val=\"account\">" ++
        "<span id=stnav-account><span class=\"dot dot--ok\"></span></span>Account</button>" ++
        "<button class=\"set-navpill\" data-act=settings-sec data-val=\"system\">" ++
        "<span id=stnav-system><span class=\"dot dot--off\"></span></span>Sys&amp;tem</button></nav>" ++
        "<p class=page-sub>Sign in &amp; keys</p><div id=set-account class=set-sec>" ++
        "<div class=\"rp-card\"><div class=set-cardhead><span class=set-title>API</span></div>" ++
        "<div class=set-st id=stset-api><span class=\"dot dot--ok\"></span>" ++
        "<span data-value=\"https://x/\">https://x/</span></div></div></div>", h.b.items);

    h.b.clearRetainingCapacity();
    try renderContent(&h, .{ .searching = true, .noResults = "No settings match “zz”" });
    try std.testing.expectEqualStrings("<div class=\"rp-empty\">" ++
        "<div class=\"rp-empty__title\">No settings match “zz”</div></div>", h.b.items);
}
