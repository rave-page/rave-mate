//! Editor view renderer — byte-exact port of internal/webui/render_editor.go
//! (editorHTML + the CSS-composite preview, layers panel and inspector). State arrives
//! fully resolved from Go: every number is already a trimNum/pct/edCQW string and the two
//! `%q` tokens (font-family, image URL) carry Go strconv.Quote output verbatim. This file
//! owns the STRUCTURE: which declarations appear, the conditionals, escaping, quoting.
//! Golden gate: internal/webui/zigui_golden_editor_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

const Err = std.mem.Allocator.Error; // explicit: the layer walk is mutually recursive

pub const GradStop = struct {
    rgba: []const u8 = "",
    pos: []const u8 = "",
};

/// Paint is a leaf's background CSS. kind "" = no paint.
pub const Paint = struct {
    kind: []const u8 = "", // ""|solid|gradient|image
    rgba: []const u8 = "",
    angle: []const u8 = "",
    stops: []const GradStop = &.{},
    urlq: []const u8 = "", // Go %q of the file:// URL (quotes included)
    size: []const u8 = "",
};

pub const Text = struct {
    content: []const u8 = "",
    famq: []const u8 = "", // Go %q of the font family (quotes included)
    size: []const u8 = "",
    lh: []const u8 = "",
    alignment: []const u8 = "",
    rgba: []const u8 = "",
    ls: []const u8 = "",
};

/// Inner is a leaf's inner HTML source. kind "" = empty.
pub const Inner = struct {
    kind: []const u8 = "", // ""|text|imgph
    text: Text = .{},
    placeholder: []const u8 = "",
};

/// Layer is one composited preview layer (group or leaf).
pub const Layer = struct {
    group: bool = false,
    id: []const u8 = "",
    sel: bool = false,
    blend: []const u8 = "", // "" = normal → no declaration
    opacity: []const u8 = "", // "" = 1 → no declaration

    xform: bool = false, // group: transform is not identity
    tx: []const u8 = "",
    ty: []const u8 = "",
    sx: []const u8 = "",
    sy: []const u8 = "",
    rot: []const u8 = "", // leaf: "" = no rotate()

    left: []const u8 = "",
    top: []const u8 = "",
    w: []const u8 = "",
    h: []const u8 = "",

    paint: Paint = .{},
    inner: Inner = .{},

    children: []const Layer = &.{},
};

pub const Preview = struct {
    aw: []const u8 = "",
    ah: []const u8 = "",
    layers: []const Layer = &.{},
    cap: []const u8 = "",
    hint: []const u8 = "",
};

pub const Row = struct {
    id: []const u8 = "",
    name: []const u8 = "",
    depth: u32 = 0,
    group: bool = false,
    sel: bool = false,
    visible: bool = false,
    locked: bool = false,
};

pub const Actions = struct {
    up: []const u8 = "",
    down: []const u8 = "",
    group: []const u8 = "",
    ungroup: []const u8 = "",
    delete: []const u8 = "",
    hasSel: bool = false,
    noSel: []const u8 = "",
    opacity: c.Slider = .{},
    blend: c.Select = .{},
};

pub const Layers = struct {
    rows: []const Row = &.{},
    empty: []const u8 = "",
    actions: Actions = .{},
};

pub const ColorRow = struct {
    rgba: []const u8 = "",
    field: c.Field = .{},
};

pub const InspText = struct {
    label: []const u8 = "",
    content: []const u8 = "",
    hint: []const u8 = "",
    font: c.Select = .{},
    size: c.Field = .{},
    ls: c.Field = .{},
    lh: c.Field = .{},
    alignment: c.Select = .{},
    color: ColorRow = .{},
};

pub const Insp = struct {
    hasSel: bool = false,
    empty: []const u8 = "",
    name: c.Field = .{},
    x: c.Field = .{},
    y: c.Field = .{},
    showWh: bool = false,
    w: c.Field = .{},
    h: c.Field = .{},
    sx: c.Field = .{},
    sy: c.Field = .{},
    rot: c.Field = .{},

    kind: []const u8 = "", // ""|text|solid|gradient|image
    text: InspText = .{},
    fill: ColorRow = .{},
    angle: c.Field = .{},
    start: ColorRow = .{},
    end: ColorRow = .{},
    path: c.Field = .{},
    fit: c.Select = .{},
};

pub const State = struct {
    title: []const u8 = "",
    sub: []const u8 = "",
    disabled: bool = false,
    disabledSub: []const u8 = "",
    disabledHint: []const u8 = "",

    secPreview: []const u8 = "",
    secLayers: []const u8 = "",
    secInspector: []const u8 = "",

    row1: []const c.Btn = &.{},
    row2: []const c.Btn = &.{},

    preview: Preview = .{},
    layers: Layers = .{},
    insp: Insp = .{},
};

/// render mirrors Go editorHTML (full tab view).
pub fn render(h: *Html, s: State) !void {
    if (s.disabled) {
        try c.panel(h, s.title, s.disabledSub);
        try c.hint(h, "warn", s.disabledHint);
        return;
    }
    try c.panel(h, s.title, s.sub);
    try h.raw("<div class=ed-toolbar>");
    try c.btnRowOf(h, s.row1);
    try c.btnRowOf(h, s.row2);
    try h.raw("</div><div class=ed-grid><div class=ed-col>");
    try c.sectionOpen(h, s.secPreview);
    try h.raw("<div id=ed-preview>");
    try renderPreview(h, s.preview);
    try h.raw("</div>");
    try c.sectionClose(h);
    try h.raw("</div><div class=ed-col>");
    try c.sectionOpen(h, s.secLayers);
    try renderLayers(h, s.layers);
    try c.sectionClose(h);
    try c.sectionOpen(h, s.secInspector);
    try renderInsp(h, s.insp);
    try c.sectionClose(h);
    try h.raw("</div></div>");
}

/// renderPreview mirrors Go edPreviewHTMLOf (#ed-preview fragment).
pub fn renderPreview(h: *Html, s: Preview) !void {
    try h.raw("<div class=ed-stage style=\"aspect-ratio:");
    try h.raw(s.aw);
    try h.raw("/");
    try h.raw(s.ah);
    try h.raw("\">");
    try renderChildren(h, s.layers);
    try h.raw("</div><div class=ed-cap>");
    try h.esc(s.cap);
    try h.raw("</div>");
    try c.hint(h, "info", s.hint);
}

fn renderChildren(h: *Html, layers: []const Layer) Err!void {
    for (layers) |l| try renderLayer(h, l);
}

/// renderLayer mirrors Go edLayerDivHTML. The whole style string is assembled into a
/// scratch buffer first because Go attrQ-escapes it as one attribute value (the image
/// paint carries `%q` double quotes that must come out as &#34;).
fn renderLayer(h: *Html, l: Layer) Err!void {
    var sb = Html.init(h.a);
    defer sb.deinit();

    if (l.group) {
        try sb.raw("left:0;top:0;width:100%;height:100%;");
        if (l.xform) {
            try sb.raw("transform-origin:0 0;transform:translate(");
            try sb.raw(l.tx);
            try sb.raw("%,");
            try sb.raw(l.ty);
            try sb.raw("%) rotate(");
            try sb.raw(l.rot);
            try sb.raw("deg) scale(");
            try sb.raw(l.sx);
            try sb.raw(",");
            try sb.raw(l.sy);
            try sb.raw(");");
        } else {
            try sb.raw("transform:none;");
        }
        try appendOpBlend(&sb, l);
        try h.raw("<div class=\"ed-group");
        if (l.sel) try h.raw(" ed-sel");
        try h.raw("\" style=");
        try h.attrQ(sb.b.items);
        try h.raw(">");
        try renderChildren(h, l.children);
        try h.raw("</div>");
        return;
    }

    try sb.raw("left:");
    try sb.raw(l.left);
    try sb.raw("%;top:");
    try sb.raw(l.top);
    try sb.raw("%;width:");
    try sb.raw(l.w);
    try sb.raw("%;height:");
    try sb.raw(l.h);
    try sb.raw("%;");
    if (l.rot.len != 0) {
        try sb.raw("transform:rotate(");
        try sb.raw(l.rot);
        try sb.raw("deg);");
    }
    try appendOpBlend(&sb, l);
    try appendPaint(&sb, l.paint);

    try h.raw("<div class=\"ed-layer");
    if (l.sel) try h.raw(" ed-sel");
    try h.raw("\" style=");
    try h.attrQ(sb.b.items);
    try h.raw(" data-act=\"ed-select:");
    try h.esc(l.id);
    try h.raw("\" data-val=");
    try h.attrQ(l.id);
    try h.raw(">");
    try renderInner(h, l.inner);
    try h.raw("</div>");
}

/// appendOpBlend appends the opacity then blend declarations (Go: op + blend).
fn appendOpBlend(sb: *Html, l: Layer) Err!void {
    if (l.opacity.len != 0) {
        try sb.raw("opacity:");
        try sb.raw(l.opacity);
        try sb.raw(";");
    }
    if (l.blend.len != 0) {
        try sb.raw("mix-blend-mode:");
        try sb.raw(l.blend);
        try sb.raw(";");
    }
}

/// appendPaint mirrors Go edPaintHTML.
fn appendPaint(sb: *Html, p: Paint) Err!void {
    if (std.mem.eql(u8, p.kind, "solid")) {
        try sb.raw("background:");
        try sb.raw(p.rgba);
        try sb.raw(";");
        return;
    }
    if (std.mem.eql(u8, p.kind, "gradient")) {
        try sb.raw("background:linear-gradient(");
        try sb.raw(p.angle);
        try sb.raw("deg,");
        for (p.stops, 0..) |st, i| {
            if (i != 0) try sb.raw(",");
            try sb.raw(st.rgba);
            try sb.raw(" ");
            try sb.raw(st.pos);
            try sb.raw("%");
        }
        try sb.raw(");");
        return;
    }
    if (std.mem.eql(u8, p.kind, "image")) {
        try sb.raw("background-image:url(");
        try sb.raw(p.urlq);
        try sb.raw(");background-size:");
        try sb.raw(p.size);
        try sb.raw(";background-position:center;background-repeat:no-repeat;");
    }
}

/// renderInner mirrors Go edInnerHTML. The text span's style is NOT attribute-escaped in
/// the Go original (the `%q` quotes land raw inside style="…") — replicated byte-exact.
fn renderInner(h: *Html, in: Inner) Err!void {
    if (std.mem.eql(u8, in.kind, "text")) {
        const t = in.text;
        try h.raw("<span class=ed-txt style=\"font-family:");
        try h.raw(t.famq);
        try h.raw(";font-size:");
        try h.raw(t.size);
        try h.raw("cqw;line-height:");
        try h.raw(t.lh);
        try h.raw(";text-align:");
        try h.raw(t.alignment);
        try h.raw(";color:");
        try h.raw(t.rgba);
        try h.raw(";letter-spacing:");
        try h.raw(t.ls);
        try h.raw("cqw;\">");
        try h.esc(t.content);
        try h.raw("</span>");
        return;
    }
    if (std.mem.eql(u8, in.kind, "imgph")) {
        try h.raw("<span class=ed-imgph>");
        try h.esc(in.placeholder);
        try h.raw("</span>");
    }
}

/// renderLayers mirrors Go edLayersHTML (rows top-first = reverse document order).
fn renderLayers(h: *Html, s: Layers) !void {
    try h.raw("<div class=ed-layers>");
    if (s.rows.len == 0) try c.emptyState(h, s.empty);
    var i: usize = s.rows.len;
    while (i > 0) {
        i -= 1;
        try renderRow(h, s.rows[i]);
    }
    try h.raw("</div>");
    try renderActions(h, s.actions);
}

fn renderRow(h: *Html, r: Row) !void {
    try h.raw("<div class=ed-layer-row><button class=");
    try h.attrQ(if (r.sel) "ed-lr-name ed-lr-sel" else "ed-lr-name");
    try h.raw(" data-act=\"ed-select:");
    try h.esc(r.id);
    try h.raw("\" data-val=");
    try h.attrQ(r.id);
    try h.raw(">");
    // prefix: one ideographic space per depth level, then a group marker
    var d: u32 = 0;
    while (d < r.depth) : (d += 1) try h.raw("　");
    if (r.group) try h.raw("▸ ");
    try h.esc(r.name);
    try h.raw("</button><span class=ed-lr-toggles>");
    if (r.visible) try c.btnAct(h, "👁", "ghost", "ed-vis:", r.id) else try c.btnAct(h, "🙈", "warn", "ed-vis:", r.id);
    if (r.locked) try c.btnAct(h, "🔒", "warn", "ed-lock:", r.id) else try c.btnAct(h, "🔓", "ghost", "ed-lock:", r.id);
    try h.raw("</span></div>");
}

fn renderActions(h: *Html, s: Actions) !void {
    try c.btnRowOpen(h);
    try c.btn(h, s.up, "ghost", "ed-up", "");
    try c.btn(h, s.down, "ghost", "ed-down", "");
    try c.btn(h, s.group, "ghost", "ed-group", "");
    try c.btn(h, s.ungroup, "ghost", "ed-ungroup", "");
    try c.btn(h, s.delete, "destructive", "ed-del", "");
    try c.btnRowClose(h);
    if (!s.hasSel) {
        try c.hint(h, "info", s.noSel);
        return;
    }
    try h.raw("<div class=ed-selctl>");
    try c.slider(h, s.opacity);
    try c.selectBox(h, s.blend);
    try h.raw("</div>");
}

/// renderInsp mirrors Go edInspHTML.
fn renderInsp(h: *Html, s: Insp) !void {
    if (!s.hasSel) {
        try c.emptyState(h, s.empty);
        return;
    }
    try h.raw("<div class=ed-insp>");
    try c.fieldOf(h, s.name);
    try h.raw("<div class=ed-row2>");
    try c.fieldOf(h, s.x);
    try c.fieldOf(h, s.y);
    try h.raw("</div>");
    if (s.showWh) {
        try h.raw("<div class=ed-row2>");
        try c.fieldOf(h, s.w);
        try c.fieldOf(h, s.h);
        try h.raw("</div>");
    }
    try h.raw("<div class=ed-row2>");
    try c.fieldOf(h, s.sx);
    try c.fieldOf(h, s.sy);
    try h.raw("</div>");
    try c.fieldOf(h, s.rot);
    if (std.mem.eql(u8, s.kind, "text")) {
        try renderInspText(h, s.text);
    } else if (std.mem.eql(u8, s.kind, "solid")) {
        try renderColorRow(h, s.fill);
    } else if (std.mem.eql(u8, s.kind, "gradient")) {
        try c.fieldOf(h, s.angle);
        try renderColorRow(h, s.start);
        try renderColorRow(h, s.end);
    } else if (std.mem.eql(u8, s.kind, "image")) {
        try c.fieldOf(h, s.path);
        try c.selectBox(h, s.fit);
    }
    try h.raw("</div>");
}

fn renderInspText(h: *Html, s: InspText) !void {
    try h.raw("<label class=\"field ed-ta\"><span class=field-label>");
    try h.esc(s.label);
    try h.raw("</span><textarea class=field-input rows=3 data-act=\"ed-txt:content\" data-value=\"\">");
    try h.esc(s.content);
    try h.raw("</textarea></label>");
    try c.hint(h, "info", s.hint);
    try c.selectBox(h, s.font);
    try h.raw("<div class=ed-row2>");
    try c.fieldOf(h, s.size);
    try c.fieldOf(h, s.ls);
    try h.raw("</div>");
    try c.fieldOf(h, s.lh);
    try c.selectBox(h, s.alignment);
    try renderColorRow(h, s.color);
}

/// renderColorRow mirrors Go edColorRowHTML (swatch + hex field).
fn renderColorRow(h: *Html, s: ColorRow) !void {
    try h.raw("<div class=ed-color-row><span class=ed-swatch style=\"background:");
    try h.raw(s.rgba);
    try h.raw("\"></span>");
    try c.fieldOf(h, s.field);
    try h.raw("</div>");
}

test "disabled view" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .title = "Editor", .sub = "ignored", .disabled = true, .disabledSub = "off", .disabledHint = "enable it" });
    try std.testing.expectEqualStrings("<h1 class=page-title>Editor</h1><p class=page-sub>off</p>" ++
        "<span class=\"hint hint--warn\">enable it</span>", h.b.items);
}

test "leaf layer folds placement, rotation, opacity, blend and paint into one attr" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const ls = [_]Layer{.{
        .id = "l1",
        .sel = true,
        .blend = "screen",
        .opacity = "0.5",
        .left = "10",
        .top = "20",
        .w = "30",
        .h = "40",
        .rot = "15",
        .paint = .{ .kind = "solid", .rgba = "rgba(1,2,3,1)" },
    }};
    try renderChildren(&h, &ls);
    try std.testing.expectEqualStrings("<div class=\"ed-layer ed-sel\" style=\"left:10%;top:20%;width:30%;height:40%;" ++
        "transform:rotate(15deg);opacity:0.5;mix-blend-mode:screen;background:rgba(1,2,3,1);\"" ++
        " data-act=\"ed-select:l1\" data-val=\"l1\"></div>", h.b.items);
}

test "image paint escapes the %q quotes inside the style attribute" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const ls = [_]Layer{.{ .id = "i", .left = "0", .top = "0", .w = "100", .h = "100", .paint = .{ .kind = "image", .urlq = "\"file:///c:/a.png\"", .size = "cover" } }};
    try renderChildren(&h, &ls);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "background-image:url(&#34;file:///c:/a.png&#34;);background-size:cover;") != null);
}

test "gradient paint joins stops with commas" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    var sb = Html.init(std.testing.allocator);
    defer sb.deinit();
    const stops = [_]GradStop{ .{ .rgba = "rgba(0,0,0,1)", .pos = "0" }, .{ .rgba = "rgba(255,255,255,1)", .pos = "100" } };
    try appendPaint(&sb, .{ .kind = "gradient", .angle = "180", .stops = &stops });
    try std.testing.expectEqualStrings("background:linear-gradient(180deg,rgba(0,0,0,1) 0%,rgba(255,255,255,1) 100%);", sb.b.items);
}

test "group layer wraps children and keeps transform:none when identity" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const kids = [_]Layer{.{ .id = "k", .left = "0", .top = "0", .w = "1", .h = "1" }};
    const ls = [_]Layer{.{ .group = true, .id = "g", .children = &kids }};
    try renderChildren(&h, &ls);
    try std.testing.expect(std.mem.startsWith(u8, h.b.items, "<div class=\"ed-group\" style=\"left:0;top:0;width:100%;height:100%;transform:none;\">"));
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-val=\"k\"") != null);
}

test "layer rows render top-first with depth prefix and group marker" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const rows = [_]Row{
        .{ .id = "g", .name = "Grp", .depth = 0, .group = true, .visible = true },
        .{ .id = "l", .name = "Leaf", .depth = 1, .locked = true },
    };
    try renderLayers(&h, .{ .rows = &rows, .empty = "none", .actions = .{ .up = "↑", .down = "↓", .group = "G", .ungroup = "U", .delete = "D", .noSel = "pick one" } });
    const leafAt = std.mem.indexOf(u8, h.b.items, "data-val=\"l\"").?;
    const grpAt = std.mem.indexOf(u8, h.b.items, "data-val=\"g\"").?;
    try std.testing.expect(leafAt < grpAt); // deepest/last row first
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, ">　Leaf</button>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, ">▸ Grp</button>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-act=\"ed-lock:l\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<span class=\"hint hint--info\">pick one</span>") != null);
}

test "inspector without selection is an empty state" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderInsp(&h, .{ .empty = "nothing selected" });
    try std.testing.expectEqualStrings("<div class=\"rp-empty\"><div class=\"rp-empty__title\">nothing selected</div></div>", h.b.items);
}
