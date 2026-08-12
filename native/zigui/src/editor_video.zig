//! Editor video-mode renderer — byte-exact port of
//! internal/webui/render_editor_video.go (editorVideoHTML + the #edv-frame and
//! #edv-export fragments). The mp component markup arrives RAW in state.player
//! (player.go owns that surface and its own golden gate). Golden gate:
//! internal/webui/zigui_golden_editor_video_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");
const editor = @import("editor.zig");

pub const KfRow = struct {
    time: []const u8 = "",
    pos: []const u8 = "",
    goAct: []const u8 = "",
    delAct: []const u8 = "",
    delLb: []const u8 = "",
};

pub const Frame = struct {
    show: bool = false,
    aw: []const u8 = "",
    ah: []const u8 = "",
    imgUrl: []const u8 = "",
    busy: []const u8 = "",
    hasCrop: bool = false,
    cropL: []const u8 = "",
    cropT: []const u8 = "",
    cropW: []const u8 = "",
    cropH: []const u8 = "",
};

pub const FxParam = struct {
    isBool: bool = false,
    isColor: bool = false,
    slider: c.Slider = .{},
    toggle: c.Toggle = .{},
    swatch: []const u8 = "", // color: css rgb() of the current value
    field: c.Field = .{}, // color: hex input
};

pub const FxRow = struct {
    name: []const u8 = "",
    missing: bool = false,
    missLb: []const u8 = "",
    off: bool = false,
    btns: []const c.Btn = &.{},
    params: []const FxParam = &.{},
};

pub const Export = struct {
    preset: c.Select = .{},
    out: c.Field = .{},
    outBrowse: c.Btn = .{},
    @"export": c.Btn = .{},
    running: bool = false,
    pct: []const u8 = "",
    stage: []const u8 = "",
    cancel: c.Btn = .{},
    hasResult: bool = false,
    result: []const u8 = "",
    hasErr: bool = false,
    err: []const u8 = "",
    trimInfo: []const u8 = "",
};

pub const State = struct {
    title: []const u8 = "",
    sub: []const u8 = "",
    modes: []const editor.ModeTab = &.{},

    srcBtn: c.Btn = .{},
    hasSrc: bool = false,
    srcName: []const u8 = "",
    srcInfo: []const u8 = "",
    noSrc: []const u8 = "",

    viewTitle: []const u8 = "",
    inspTitle: []const u8 = "",
    layout: c.Select = .{},
    hasBlur: bool = false,
    blur: c.Slider = .{},
    hasZoom: bool = false,
    zoom: c.Slider = .{},

    player: []const u8 = "", // RAW mp markup
    playerCls: []const u8 = "", // " edv-reframe"/" edv-reframe-fit" = live reframe preview
    playerVars: []const u8 = "", // CSS vars driving the preview crop
    noMedia: []const u8 = "",
    editHint: []const u8 = "",

    secReframe: []const u8 = "",
    showRef: bool = false,
    aspect: c.Select = .{},
    reframeBtn: c.Btn = .{}, // opens the reframe/area-select modal

    secFx: []const u8 = "",
    showFx: bool = false,
    fxAdd: c.Select = .{},
    fxNone: []const u8 = "",
    fxRows: []const FxRow = &.{},
    prevRes: c.Select = .{}, // realtime preview render-height cap
    fxHint: []const u8 = "",
    fxSrc: []const c.Btn = &.{},

    secExport: []const u8 = "",
    @"export": Export = .{},
};

/// Reframe is the reframe/area-select modal body state.
pub const Reframe = struct {
    title: []const u8 = "",
    frame: Frame = .{},
    frameBtn: c.Btn = .{},
    kfAdd: c.Btn = .{},
    kfClear: c.Btn = .{},
    hasKfs: bool = false,
    kfs: []const KfRow = &.{},
    refHint: []const u8 = "",
};

/// render mirrors Go editorVideoHTML (viewer = the one player preview; inspector
/// body in #edv-insp so non-media edits never rebuild the playing <video>).
pub fn render(h: *Html, s: State) !void {
    try c.panel(h, s.title, s.sub);
    try editor.renderModes(h, s.modes);
    try h.raw("<div class=edv-nle>");

    // viewer pane: the mp player is THE preview
    try h.raw("<div class=\"edv-pane edv-pane-view\"><div class=edv-pane-title>");
    try h.esc(s.viewTitle);
    try h.raw("</div>");
    if (s.player.len != 0) {
        try h.raw("<div class=\"edv-player");
        try h.raw(s.playerCls);
        try h.raw("\"");
        if (s.playerVars.len != 0) {
            try h.raw(" style=");
            try h.attrQ(s.playerVars);
        }
        try h.raw(">");
        try h.raw(s.player);
        try h.raw("</div>");
        try c.hint(h, "info", s.editHint);
    } else if (s.hasSrc) {
        try c.emptyState(h, s.noMedia);
    } else {
        try c.emptyState(h, s.noSrc);
    }
    try h.raw("</div>");

    // inspector pane
    try h.raw("<div class=\"edv-pane edv-pane-insp\"><div class=edv-pane-title>");
    try h.esc(s.inspTitle);
    try h.raw("</div><div id=edv-insp>");
    try renderInsp(h, s);
    try h.raw("</div></div>");

    try h.raw("</div>");
}

/// renderInsp mirrors Go editorVideoInspHTML (#edv-insp inner).
pub fn renderInsp(h: *Html, s: State) !void {
    if (s.hasSrc) {
        try h.raw("<div class=edv-src><span class=edv-srcname>");
        try h.esc(s.srcName);
        try h.raw("</span>");
        if (s.srcInfo.len != 0) {
            try h.raw("<span class=edv-srcinfo>");
            try h.esc(s.srcInfo);
            try h.raw("</span>");
        }
        try h.raw("</div>");
    } else {
        try c.hint(h, "info", s.noSrc);
    }
    try c.btnRowOpen(h);
    try c.btnOf(h, s.srcBtn);
    try c.btnRowClose(h);
    if (s.showRef) {
        try h.raw("<div class=edv-insp-sec>");
        try h.esc(s.secReframe);
        try h.raw("</div>");
        try c.selectBox(h, s.aspect);
        if (s.hasZoom) {
            try h.raw("<div id=edv-zoomrow>");
            try c.slider(h, s.zoom);
            try h.raw("</div>");
        }
        try c.selectBox(h, s.layout);
        if (s.hasBlur) {
            try c.slider(h, s.blur);
        }
        try c.btnRowOpen(h);
        try c.btnOf(h, s.reframeBtn);
        try c.btnRowClose(h);
    }
    if (s.showFx) {
        try h.raw("<div class=edv-insp-sec>");
        try h.esc(s.secFx);
        try h.raw("</div>");
        try c.selectBox(h, s.fxAdd);
        if (s.fxNone.len != 0) {
            try c.hint(h, "info", s.fxNone);
        }
        try h.raw("<div class=edv-fx-list>");
        for (s.fxRows) |r| {
            try renderFxRow(h, r);
        }
        try h.raw("</div>");
        try c.selectBox(h, s.prevRes);
        try c.hint(h, "info", s.fxHint);
        if (s.fxSrc.len != 0) {
            try c.btnRowOpen(h);
            for (s.fxSrc) |sb| try c.btnOf(h, sb);
            try c.btnRowClose(h);
        }
    }
    try h.raw("<div class=edv-insp-sec>");
    try h.esc(s.secExport);
    try h.raw("</div><div id=edv-export>");
    try renderExport(h, s.@"export");
    try h.raw("</div>");
}

/// renderReframe mirrors Go edvReframeHTML (reframe modal body).
pub fn renderReframe(h: *Html, s: Reframe) !void {
    try h.raw("<div id=edv-frame>");
    try renderFrame(h, s.frame);
    try h.raw("</div><div id=edv-kfbox>");
    try renderKfBox(h, s);
    try h.raw("</div>");
    try c.hint(h, "info", s.refHint);
}

/// renderKfBox mirrors Go edvKfBoxHTML (#edv-kfbox inner).
pub fn renderKfBox(h: *Html, s: Reframe) !void {
    try c.btnRowOpen(h);
    try c.btnOf(h, s.frameBtn);
    try c.btnOf(h, s.kfAdd);
    try c.btnOf(h, s.kfClear);
    try c.btnRowClose(h);
    if (s.hasKfs) {
        try h.raw("<div class=edv-kfs>");
        for (s.kfs) |k| {
            try h.raw("<span class=edv-kf><button class=edv-kf-go data-act=");
            try h.attrQ(k.goAct);
            try h.raw(">");
            try h.esc(k.time);
            try h.raw(" · ");
            try h.raw(k.pos);
            try h.raw("%</button><button class=edv-kf-del data-act=");
            try h.attrQ(k.delAct);
            try h.raw(">");
            try h.esc(k.delLb);
            try h.raw("</button></span>");
        }
        try h.raw("</div>");
    }
}

/// renderFxRow mirrors Go edvFxRowHTML.
pub fn renderFxRow(h: *Html, r: FxRow) !void {
    if (r.off) {
        try h.raw("<div class=\"edv-fx edv-fx-off\">");
    } else {
        try h.raw("<div class=edv-fx>");
    }
    try h.raw("<div class=edv-fx-head><span class=edv-fx-name>");
    try h.esc(r.name);
    try h.raw("</span>");
    if (r.missing) {
        try h.raw("<span class=edv-fx-miss>");
        try h.esc(r.missLb);
        try h.raw("</span>");
    }
    try c.btnRowOf(h, r.btns);
    try h.raw("</div>");
    for (r.params) |p| {
        if (p.isColor) {
            try h.raw("<div class=edv-fx-color><span class=edv-fx-swatch style=");
            var buf: [64]u8 = undefined;
            const style = std.fmt.bufPrint(&buf, "background:{s}", .{p.swatch}) catch "background:";
            try h.attrQ(style);
            try h.raw("></span>");
            try c.fieldOf(h, p.field);
            try h.raw("</div>");
        } else if (p.isBool) {
            try c.toggleOf(h, p.toggle);
        } else {
            try c.slider(h, p.slider);
        }
    }
    try h.raw("</div>");
}

/// renderFrame mirrors Go edvFrameHTML (#edv-frame fragment).
pub fn renderFrame(h: *Html, s: Frame) !void {
    if (!s.show) return;
    try h.raw("<div class=edv-fbox data-actpos=edv-pan data-actwheel=edv-zoom style=\"aspect-ratio:");
    try h.raw(s.aw);
    try h.raw("/");
    try h.raw(s.ah);
    try h.raw("\">");
    if (s.imgUrl.len != 0) {
        try h.raw("<img class=edv-fimg src=");
        try h.attrQ(s.imgUrl);
        try h.raw(" alt=\"\">");
    } else {
        try h.raw("<span class=edv-fbusy>");
        try h.esc(s.busy);
        try h.raw("</span>");
    }
    try h.raw("<div id=edv-fovl>");
    if (s.hasCrop) {
        // four shades frame the window on every side (zoom slack on both axes)
        try h.raw("<div class=edv-shade style=\"left:0;right:0;top:0;height:");
        try h.raw(s.cropT);
        try h.raw("%\"></div><div class=edv-shade style=\"left:0;right:0;top:calc(");
        try h.raw(s.cropT);
        try h.raw("% + ");
        try h.raw(s.cropH);
        try h.raw("%);bottom:0\"></div>");
        try h.raw("<div class=edv-shade style=\"left:0;width:");
        try h.raw(s.cropL);
        try h.raw("%;top:");
        try h.raw(s.cropT);
        try h.raw("%;height:");
        try h.raw(s.cropH);
        try h.raw("%\"></div><div class=edv-shade style=\"left:calc(");
        try h.raw(s.cropL);
        try h.raw("% + ");
        try h.raw(s.cropW);
        try h.raw("%);right:0;top:");
        try h.raw(s.cropT);
        try h.raw("%;height:");
        try h.raw(s.cropH);
        try h.raw("%\"></div>");
        try h.raw("<div class=edv-crop style=\"left:");
        try h.raw(s.cropL);
        try h.raw("%;top:");
        try h.raw(s.cropT);
        try h.raw("%;width:");
        try h.raw(s.cropW);
        try h.raw("%;height:");
        try h.raw(s.cropH);
        try h.raw("%\"></div>");
    }
    try h.raw("</div></div>");
}

/// renderExport mirrors Go edvExportHTML (#edv-export fragment).
pub fn renderExport(h: *Html, s: Export) !void {
    try c.selectBox(h, s.preset);
    try h.raw("<div class=edv-outrow>");
    try c.fieldOf(h, s.out);
    try c.btnOf(h, s.outBrowse);
    try h.raw("</div>");
    if (s.trimInfo.len != 0) {
        try h.raw("<div class=edv-triminfo>");
        try h.esc(s.trimInfo);
        try h.raw("</div>");
    }
    if (s.running) {
        try c.progressBar(h, s.pct, s.stage);
        try c.btnRowOpen(h);
        try c.btnOf(h, s.cancel);
        try c.btnRowClose(h);
    } else {
        try c.btnRowOpen(h);
        try c.btnOf(h, s.@"export");
        try c.btnRowClose(h);
    }
    if (s.hasResult) {
        try c.hint(h, "ok", s.result);
    }
    if (s.hasErr) {
        try c.hint(h, "bad", s.err);
    }
}

test "frame with horizontal crop renders shades left+right of the window" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderFrame(&h, .{ .show = true, .aw = "1920", .ah = "1080", .imgUrl = "http://127.0.0.1:1/img/x", .hasCrop = true, .cropL = "34.219", .cropT = "0", .cropW = "31.563", .cropH = "100" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-actpos=edv-pan") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "left:calc(34.219% + 31.563%);right:0") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=edv-crop style=\"left:34.219%;top:0%;width:31.563%;height:100%\">") != null);
}

test "hidden frame renders nothing" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderFrame(&h, .{});
    try std.testing.expectEqualStrings("", h.b.items);
}
