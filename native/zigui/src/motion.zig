//! Motion view renderer — byte-exact port of internal/webui/render_motion.go
//! (motionHTML / motionBodyHTML + the camera-paths and motion-studio sections).
//! State arrives fully resolved from Go: data + localized strings + the pre-rendered
//! fragments Go owns (campath viewer SVG, skeleton/mesh preview, render progress,
//! tooltips, play button) which are embedded verbatim. Every number is pre-formatted
//! Go-side — this file never formats a float. Golden gate:
//! internal/webui/zigui_golden_motion_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

/// Toggle is one resolved toggleRow (dl = Go strings.ToLower(label)).
pub const Toggle = struct {
    label: []const u8 = "",
    dl: []const u8 = "",
    act: []const u8 = "",
    on: bool = false,
};

/// CamRow is one camera-path list row; showGroup opens a folder header first.
pub const CamRow = struct {
    group: []const u8 = "",
    showGroup: bool = false,
    act: []const u8 = "",
    sel: bool = false,
    name: []const u8 = "",
    meta: []const u8 = "",
};

pub const Cam = struct {
    unavailable: []const u8 = "", // non-empty → only the emptyState renders
    rows: []const CamRow = &.{},
    empty: []const u8 = "",
    reloadLbl: []const u8 = "",
    organizeLbl: []const u8 = "",
    djLbl: []const u8 = "",
    previewLbl: []const u8 = "",
    tip: []const u8 = "", // raw tipTopic
    view: []const u8 = "", // raw cpvView
    hint: []const u8 = "",
    info: []const u8 = "",
    playBtn: []const u8 = "", // raw cpvPlayBtn
    loadLbl: []const u8 = "",
    copyLbl: []const u8 = "",
};

pub const Avatar = struct {
    label: []const u8 = "",
    sel: c.Select = .{},
    importLbl: []const u8 = "",
    syncLbl: []const u8 = "",
    info: []const u8 = "",
};

pub const RecRow = struct {
    name: []const u8 = "",
    act: []const u8 = "",
    sel: bool = false,
};

pub const Studio = struct {
    recs: []const RecRow = &.{},
    empty: []const u8 = "",
    refreshLbl: []const u8 = "",
    exportLbl: []const u8 = "",
    renderLbl: []const u8 = "",
    pcViewLbl: []const u8 = "",
    renderProg: []const u8 = "", // raw
    avatar: Avatar = .{},
    previewLbl: []const u8 = "",
    tip: []const u8 = "", // raw
    view: []const u8 = "", // raw preview SVG / raster frame
    hint: []const u8 = "",
    time: []const u8 = "",
    scrub: c.Slider = .{},
    playLbl: []const u8 = "",
    stopLbl: []const u8 = "",
    loop: Toggle = .{},
    osc: Toggle = .{},
    vmc: Toggle = .{},
    model: Toggle = .{},
    modelOn: bool = false, // gates every model-only row below
    hasDyn: bool = false,
    physNote: []const u8 = "", // RAW (the Go original emits it unescaped)
    phys: Toggle = .{},
    rest: Toggle = .{},
    marks: Toggle = .{},
    pc: Toggle = .{},
    pcOn: bool = false,
    pcDensity: c.Select = .{},
    pcColor: Toggle = .{},
    pcNote: []const u8 = "",
    pcExportLbl: []const u8 = "",
    vmcHelp: []const u8 = "",
};

pub const State = struct {
    title: []const u8 = "",
    sub: []const u8 = "",
    section: []const u8 = "", // "campaths" | "studio"
    tabCam: []const u8 = "",
    tabStudio: []const u8 = "",
    cam: ?Cam = null,
    studio: ?Studio = null,
};

/// render mirrors Go motionHTML (full tab view).
pub fn render(h: *Html, s: State) !void {
    try h.raw("<h1 class=page-title>");
    try h.esc(s.title);
    try h.raw("</h1><p class=page-sub>");
    try h.esc(s.sub);
    try h.raw("</p><div class=subtabs>");
    try subtabBtn(h, "campaths", s.tabCam, s.section);
    try subtabBtn(h, "studio", s.tabStudio, s.section);
    try h.raw("</div><div id=mo-body>");
    try renderBody(h, s);
    try h.raw("</div>");
}

/// renderBody mirrors Go motionBodyHTML (#mo-body inner, the section patch target).
pub fn renderBody(h: *Html, s: State) !void {
    if (std.mem.eql(u8, s.section, "studio")) {
        if (s.studio) |st| try renderStudio(h, st);
        return;
    }
    if (s.cam) |st| try renderCam(h, st);
}

/// subtabBtn mirrors Go subtabBtn (act = "mo-section:<id>"; id is a trusted literal).
fn subtabBtn(h: *Html, id: []const u8, label: []const u8, cur: []const u8) !void {
    try h.raw("<button class=\"subtab");
    if (std.mem.eql(u8, id, cur)) try h.raw(" active");
    try h.raw("\" data-act=\"mo-section:");
    try h.raw(id);
    try h.raw("\">");
    try h.esc(label);
    try h.raw("</button>");
}

/// toggle renders one resolved switch row.
fn toggle(h: *Html, t: Toggle) !void {
    try c.toggleRow(h, t.label, t.dl, t.act, t.on);
}

/// renderCam mirrors Go moCamPathsHTML.
fn renderCam(h: *Html, s: Cam) !void {
    if (s.unavailable.len != 0) {
        try c.emptyState(h, s.unavailable);
        return;
    }
    try c.masterDetailOpen(h);
    try h.raw("<div class=mo-list>");
    for (s.rows) |r| {
        if (r.showGroup) {
            try h.raw("<div class=mo-group>");
            try h.esc(r.group);
            try h.raw("</div>");
        }
        try h.raw("<div class=\"irow");
        if (r.sel) try h.raw(" selected");
        try h.raw("\" data-act=\"");
        try h.esc(r.act);
        try h.raw("\"><div class=irow-main><div class=irow-title>");
        try h.esc(r.name);
        try h.raw("</div><div class=irow-sub>");
        try h.esc(r.meta);
        try h.raw("</div></div></div>");
    }
    if (s.rows.len == 0) try c.emptyState(h, s.empty);
    try h.raw("</div>");
    try c.btnRowOpen(h);
    try c.btn(h, s.reloadLbl, "ghost", "mo-cp-refresh", "");
    try c.btn(h, s.organizeLbl, "outline", "mo-cp-organize", "");
    try c.btn(h, s.djLbl, "outline", "mo-cp-dj", "");
    try c.btnRowClose(h);
    try c.masterDetailMid(h);
    try cardLabel(h, s.previewLbl, s.tip);
    try h.raw(s.view);
    try h.raw("<div class=mo-hint>");
    try h.esc(s.hint);
    try h.raw("</div><div id=mo-cp-info class=mo-info>");
    try h.esc(s.info);
    try h.raw("</div>");
    try c.btnRowOpen(h);
    try h.raw(s.playBtn);
    try c.btn(h, s.loadLbl, "primary", "mo-cp-load", "");
    try c.btn(h, s.copyLbl, "outline", "mo-cp-copy", "");
    try c.btnRowClose(h);
    try c.masterDetailClose(h);
}

/// cardLabel: card-label head with a pre-rendered tooltip (Go inline markup).
fn cardLabel(h: *Html, label: []const u8, tip_html: []const u8) !void {
    try h.raw("<div class=card-label>");
    try h.esc(label);
    try h.raw(tip_html);
    try h.raw("</div>");
}

/// renderStudio mirrors Go moStudioHTML.
fn renderStudio(h: *Html, s: Studio) !void {
    try c.masterDetailOpen(h);
    try h.raw("<div class=mo-list>");
    for (s.recs) |r| {
        try h.raw("<div class=\"irow");
        if (r.sel) try h.raw(" selected");
        try h.raw("\" data-act=\"");
        try h.esc(r.act);
        try h.raw("\"><div class=irow-main><div class=irow-title>");
        try h.esc(r.name);
        try h.raw("</div></div></div>");
    }
    if (s.recs.len == 0) try c.emptyState(h, s.empty);
    try h.raw("</div>");
    try c.btnRowOpen(h);
    try c.btn(h, s.refreshLbl, "ghost", "mo-rec-refresh", "");
    try c.btn(h, s.exportLbl, "outline", "pick-save:anim:mo-export", "");
    try c.btn(h, s.renderLbl, "outline", "mo-render", "");
    try c.btn(h, s.pcViewLbl, "outline", "pick-file:mo-pc-view", "");
    try c.btnRowClose(h);
    try h.raw("<div id=mo-render-prog>");
    try h.raw(s.renderProg);
    try h.raw("</div>");
    try renderAvatar(h, s.avatar);
    try c.masterDetailMid(h);
    try cardLabel(h, s.previewLbl, s.tip);
    try h.raw("<div id=mo-view data-actpos=\"mo-orbit\" data-actwheel=\"mo-zoom\">");
    try h.raw(s.view);
    try h.raw("</div><div class=mo-hint>");
    try h.esc(s.hint);
    try h.raw("</div><div id=mo-time class=mo-info>");
    try h.esc(s.time);
    try h.raw("</div>");
    try c.slider(h, s.scrub);
    try c.btnRowOpen(h);
    try c.btn(h, s.playLbl, "go", "mo-play", "");
    try c.btn(h, s.stopLbl, "outline", "mo-stop", "");
    try c.btnRowClose(h);
    try h.raw("<div class=mo-toggles>");
    try toggle(h, s.loop);
    try toggle(h, s.osc);
    try toggle(h, s.vmc);
    try toggle(h, s.model);
    if (s.modelOn) {
        // physics row: no chains → an info line naming the missing sidecar instead
        if (s.hasDyn) {
            try toggle(h, s.phys);
        } else {
            try h.raw("<div class=mo-info>");
            try h.raw(s.physNote);
            try h.raw("</div>");
        }
        try toggle(h, s.rest);
        try toggle(h, s.marks);
        try toggle(h, s.pc);
        if (s.pcOn) {
            try c.selectBox(h, s.pcDensity);
            try toggle(h, s.pcColor);
            try h.raw("<div class=mo-info>");
            try h.esc(s.pcNote);
            try h.raw("</div>");
            try c.btnRowOpen(h);
            try c.btn(h, s.pcExportLbl, "primary", "pick-save:rmpc:mo-pc-export", "");
            try c.btnRowClose(h);
        }
    }
    try h.raw("</div><p class=page-sub>");
    try h.esc(s.vmcHelp);
    try h.raw("</p>");
    try c.masterDetailClose(h);
}

/// renderAvatar mirrors Go moAvatarHTML.
fn renderAvatar(h: *Html, s: Avatar) !void {
    try h.raw("<div class=mo-avatars>");
    try cardLabel(h, s.label, "");
    try c.selectBox(h, s.sel);
    try c.btnRowOpen(h);
    try c.btn(h, s.importLbl, "outline", "pick-file:mo-avatar-import", "");
    try c.btn(h, s.syncLbl, "ghost", "mo-avatar-sync", "");
    try c.btnRowClose(h);
    try h.raw("<div class=mo-info>");
    try h.esc(s.info);
    try h.raw("</div></div>");
}

test "campaths unavailable" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderBody(&h, .{ .section = "campaths", .cam = .{ .unavailable = "VRChat tools off" } });
    try std.testing.expectEqualStrings(
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">VRChat tools off</div></div>",
        h.b.items,
    );
}

test "full view frames the body + marks the active subtab" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .title = "Motion", .sub = "S", .section = "studio", .tabCam = "Camera paths", .tabStudio = "Studio" });
    try std.testing.expectEqualStrings("<h1 class=page-title>Motion</h1><p class=page-sub>S</p><div class=subtabs>" ++
        "<button class=\"subtab\" data-act=\"mo-section:campaths\">Camera paths</button>" ++
        "<button class=\"subtab active\" data-act=\"mo-section:studio\">Studio</button>" ++
        "</div><div id=mo-body></div>", h.b.items);
}

test "campath row with folder group" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const rows = [_]CamRow{.{ .group = "DJ", .showGroup = true, .act = "mo-cp-sel:0", .sel = true, .name = "orbit", .meta = "12 pts" }};
    try renderCam(&h, .{ .rows = &rows, .previewLbl = "Preview", .hint = "h", .info = "i" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=mo-group>DJ</div>" ++
        "<div class=\"irow selected\" data-act=\"mo-cp-sel:0\"><div class=irow-main><div class=irow-title>orbit</div>" ++
        "<div class=irow-sub>12 pts</div></div></div>") != null);
}

test "studio model-off hides physics/compare/cloud rows" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderStudio(&h, .{
        .loop = .{ .label = "Loop", .dl = "loop", .act = "mo-loop" },
        .physNote = "no chains",
        .pcNote = "n",
    });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "no chains") == null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=mo-toggles><label class=row data-label=\"loop\">") != null);
}
