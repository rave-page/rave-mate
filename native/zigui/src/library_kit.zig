//! Library-tab local render helpers: the components that live in render_library.go rather
//! than components.go (key pill, chip wrapper, pb-builder field, bare filter input, inspector
//! section, actionMenu wrapper) plus the leaf state types every section shares.
//!
//! Markup is byte-exact with the Go originals. Trusted raw strings (pre-rendered markup from
//! other renderers: nav rail, cue-edit, gridfix/tagfix, compat, player, loudness, key wheel,
//! tooltips) are emitted verbatim - the Go renderer inserts them unescaped too.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

pub const Select = c.Select;
pub const Btn = c.Btn;
pub const Tab = c.Tab;

/// KeyPill is a resolved Camelot pill (Go libKeyPillSt). ok=false + empty text = nothing;
/// ok=false + text = an unparsable key rendered verbatim in the plain pill.
pub const KeyPill = struct {
    text: []const u8 = "",
    cls: []const u8 = "",
    ok: bool = false,
};

pub fn keyPill(h: *Html, p: KeyPill) !void {
    if (!p.ok and p.text.len == 0) return;
    if (!p.ok) {
        try h.raw("<span class=keypill>");
        try h.esc(p.text);
        try h.raw("</span>");
        return;
    }
    try h.raw("<span class=\"keypill");
    try h.raw(p.cls);
    try h.raw("\">");
    try h.esc(p.text);
    try h.raw("</span>");
}

/// Chip is an fchip() call as state (Go libChipSt).
pub const Chip = struct {
    label: []const u8 = "",
    val: []const u8 = "",
    act: []const u8 = "",
    active: bool = false,
};

pub fn chip(h: *Html, ch: Chip) !void {
    try c.fchip(h, ch.label, ch.val, ch.act, ch.active);
}

/// Hint is a hint() call as state.
pub const Hint = struct {
    tone: []const u8 = "",
    text: []const u8 = "",
};

pub fn hints(h: *Html, hs: []const Hint) !void {
    for (hs) |x| try c.hint(h, x.tone, x.text);
}

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

/// fieldRaw mirrors Go fieldRaw: a bare filter input dispatching act on change.
pub fn fieldRaw(h: *Html, act: []const u8, value: []const u8, ph: []const u8) !void {
    try h.raw("<input class=field-input type=text value=\"");
    try h.esc(value);
    try h.raw("\" placeholder=\"");
    try h.esc(ph);
    try h.raw("\" data-act=\"");
    try h.esc(act);
    try h.raw("\" style=\"min-width:160px\">");
}

/// inspSecOpen/inspSecClose bracket one inspector section (Go inspSec, streaming form).
pub fn inspSecOpen(h: *Html, title: []const u8) !void {
    try h.raw("<div class=insp-sec><div class=insp-sec-h>");
    try h.esc(title);
    try h.raw("</div>");
}

pub fn inspSecClose(h: *Html) !void {
    try h.raw("</div>");
}

/// amenu wraps a resolved actionMenu select (Go actionMenuOf).
pub fn amenu(h: *Html, s: Select) !void {
    try h.raw("<span class=amenu>");
    try c.selectBox(h, s);
    try h.raw("</span>");
}

/// SelTip pairs a resolved select with its pre-rendered ss-label (label text + tooltip
/// markup, resolved Go-side because tooltip.go owns it).
pub const SelTip = struct {
    sel: Select = .{},
    labelHtml: []const u8 = "",
};

pub fn selTip(h: *Html, t: SelTip) !void {
    try c.selectBoxRaw(h, t.sel, t.labelHtml);
}

/// Batch is a batchbar (browse + collection multi-select bars).
pub const Batch = struct {
    on: bool = false,
    count: []const u8 = "",
    btns: []const Btn = &.{},
};

pub fn batch(h: *Html, b: Batch) !void {
    if (!b.on) return;
    try h.raw("<div class=batchbar><span class=cnt>");
    try h.esc(b.count);
    try h.raw("</span>");
    for (b.btns) |x| try c.btnOf(h, x);
    try h.raw("</div>");
}

/// PlAct is one playlist's action row: leading buttons + the demoted ⋯ actionMenu.
pub const PlAct = struct {
    btns: []const Btn = &.{},
    menu: Select = .{},
};

pub fn plAct(h: *Html, a: PlAct) !void {
    try h.raw("<div class=lib-toolbar>");
    for (a.btns) |x| try c.btnOf(h, x);
    try amenu(h, a.menu);
    try h.raw("</div>");
}

/// btnRowOf1 emits a one-button btn-row (Go btnRow(btn(...))).
pub fn btnRowOf1(h: *Html, label: []const u8, variant: []const u8, act: []const u8) !void {
    try c.btnRowOpen(h);
    try c.btn(h, label, variant, act, "");
    try c.btnRowClose(h);
}

/// pageSub emits one muted paragraph (the `<p class=page-sub>` line used all over the tab).
pub fn pageSub(h: *Html, text: []const u8) !void {
    try h.raw("<p class=page-sub>");
    try h.esc(text);
    try h.raw("</p>");
}

/// trkKey wraps a key pill in its table cell.
pub fn trkKey(h: *Html, p: KeyPill) !void {
    try h.raw("<span class=trk-key>");
    try keyPill(h, p);
    try h.raw("</span>");
}

/// trkIcon emits the row glyph cell: 🎵 normally, a warn ⚠ when the file is missing.
pub fn trkIcon(h: *Html, warn: bool) !void {
    try h.raw(if (warn) "<span class=\"trk-ic warn\">⚠</span>" else "<span class=trk-ic>🎵</span>");
}

/// ctlLabel opens a `.lib-ctl` group with its glued label (closed by the caller's `</span>`).
pub fn ctlLabelOpen(h: *Html, label: []const u8) !void {
    try h.raw("<span class=lib-ctl><span class=lib-tlabel>");
    try h.esc(label);
    try h.raw("</span>");
}

test "keyPill variants" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try keyPill(&h, .{});
    try std.testing.expectEqualStrings("", h.b.items);
    try keyPill(&h, .{ .text = "Am&b" });
    try std.testing.expectEqualStrings("<span class=keypill>Am&amp;b</span>", h.b.items);
    h.b.clearRetainingCapacity();
    try keyPill(&h, .{ .text = "8A", .cls = " k-same", .ok = true });
    try std.testing.expectEqualStrings("<span class=\"keypill k-same\">8A</span>", h.b.items);
}

test "pbField + fieldRaw" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try pbField(&h, .{ .label = "CRF", .dl = "crf", .act = "lib-pf:crf", .value = "2\"3", .inputType = "number", .hint = "lower = better" });
    try std.testing.expectEqualStrings("<div class=pb-field data-label=\"crf\"><div class=pb-label>CRF</div>" ++
        "<input class=field-input type=\"number\" value=\"2&#34;3\" data-act=\"lib-pf:crf\">" ++
        "<div class=pb-hint>lower = better</div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try fieldRaw(&h, "lib-search", "a&b", "Filter");
    try std.testing.expectEqualStrings("<input class=field-input type=text value=\"a&amp;b\" " ++
        "placeholder=\"Filter\" data-act=\"lib-search\" style=\"min-width:160px\">", h.b.items);
}

test "inspSec + amenu brackets" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try inspSecOpen(&h, "Act<ions");
    try h.raw("B");
    try inspSecClose(&h);
    try std.testing.expectEqualStrings("<div class=insp-sec><div class=insp-sec-h>Act&lt;ions</div>B</div>", h.b.items);
    h.b.clearRetainingCapacity();
    try amenu(&h, .{ .id = "plmenu-1", .curLabel = "⋯ More" });
    try std.testing.expect(std.mem.startsWith(u8, h.b.items, "<span class=amenu><div class=ss-field>"));
    try std.testing.expect(std.mem.endsWith(u8, h.b.items, "</div></div></span>"));
}
