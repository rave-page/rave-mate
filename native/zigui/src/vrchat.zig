//! VRChat tab renderer — byte-exact port of internal/webui/render_vrchat.go (vrchatHTML +
//! the status/editor/emotes/campaths/photos renderers). State arrives fully resolved from Go
//! (session, config, off-thread scan caches, media URLs, i18n, Go-quoted attrs); this file only
//! walks state → markup. The Groups sub-view delegates to vrcgroups.zig.
//! Golden gate: internal/webui/zigui_golden_vrchat_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");
const vg = @import("vrcgroups.zig");

fn eq(a: []const u8, b: []const u8) bool {
    return std.mem.eql(u8, a, b);
}

/// Status: account/pipeline status region (#vrc-status-region). present=false ⇒ empty output.
pub const Status = struct {
    present: bool = false,
    variant: []const u8 = "",
    label: []const u8 = "",
    dl: []const u8 = "",
    line: []const u8 = "",
};

/// Opt is one <option> row.
pub const Opt = struct {
    val: []const u8 = "",
    label: []const u8 = "",
    sel: bool = false,
};

/// PresetSel is a name-picker <select> dispatching act on change.
pub const PresetSel = struct {
    act: []const u8 = "",
    placeholder: []const u8 = "",
    names: []const []const u8 = &.{},
};

/// Editor: status & bio editor (#vrc-editor).
pub const Editor = struct {
    statusTitle: []const u8 = "",
    statusTip: []const u8 = "", // pre-rendered tooltip markup (trusted)
    presenceLabel: []const u8 = "",
    presence: []const Opt = &.{},
    statusMsgLabel: []const u8 = "",
    descCls: []const u8 = "",
    descCount: []const u8 = "",
    descVal: []const u8 = "",
    maxDesc: i64 = 0,
    saveStatus: []const u8 = "",
    statusPreset: PresetSel = .{},
    presetsLabel: []const u8 = "",
    bioTitle: []const u8 = "",
    bioCls: []const u8 = "",
    bioCount: []const u8 = "",
    bioVal: []const u8 = "",
    maxBio: i64 = 0,
    saveBio: []const u8 = "",
    bioHint: []const u8 = "",
    previewLabel: []const u8 = "",
    preview: []const u8 = "",
    hasPreview: bool = false,
    bioPreset: PresetSel = .{},
    varsLabel: []const u8 = "",
    refreshLabel: []const u8 = "",
};

/// FrameOpt is one flipbook tier <option>.
pub const FrameOpt = struct {
    frames: i64 = 0,
    grid: i64 = 0,
    res: i64 = 0,
    sel: bool = false,
};

/// Emotes: animated-emoji flipbook generator card.
pub const Emotes = struct {
    hint: []const u8 = "",
    sourceLabel: []const u8 = "",
    nameLabel: []const u8 = "",
    framesLabel: []const u8 = "",
    fpsLabel: []const u8 = "",
    trimStart: []const u8 = "",
    trimEnd: []const u8 = "",
    outDirLabel: []const u8 = "",
    frameOpts: []const FrameOpt = &.{},
    outDir: []const u8 = "",
    pingpong: []const u8 = "",
    crop: []const u8 = "",
    generate: []const u8 = "",
    openFolder: []const u8 = "",
    openUpload: []const u8 = "",
    uploadUrl: []const u8 = "",
};

pub const PathItem = struct {
    idx: i64 = 0,
    label: []const u8 = "",
    active: bool = false,
};

/// Campaths: camera-paths master/detail (#vrc-campaths). state ∈ {unavailable,loading,empty,detail}.
pub const Campaths = struct {
    state: []const u8 = "",
    msg: []const u8 = "",
    items: []const PathItem = &.{},
    svg: []const u8 = "", // pre-rendered 3-D viewer (trusted)
    playBtn: []const u8 = "", // pre-rendered play/stop control (trusted)
    name: []const u8 = "",
    info: []const u8 = "",
    load: []const u8 = "",
    copy: []const u8 = "",
    copyPath: []const u8 = "",
    organize: []const u8 = "",
    hint: []const u8 = "",
};

pub const PhotoGrp = struct {
    label: []const u8 = "",
    count: i64 = 0,
    active: bool = false,
};

/// PhotoCell: one thumbnail. titleQ is the Go-quoted (%q) title attribute value, emitted verbatim.
pub const PhotoCell = struct {
    file: []const u8 = "",
    titleQ: []const u8 = "",
    label: []const u8 = "",
    src: []const u8 = "",
};

/// Photos: screenshots browser (#vrc-photos-body). state ∈ {unavailable,loading,empty,detail}.
pub const Photos = struct {
    state: []const u8 = "",
    msg: []const u8 = "",
    groups: []const PhotoGrp = &.{},
    cells: []const PhotoCell = &.{},
    note: []const u8 = "",
    openFolder: []const u8 = "",
    photosDir: []const u8 = "",
};

/// State: the whole VRChat tab.
pub const State = struct {
    available: bool = false,
    title: []const u8 = "",
    sub: []const u8 = "",
    unavailable: []const u8 = "",
    status: Status = .{},
    subActive: []const u8 = "",
    subTabs: []const c.Tab = &.{},
    groups: vg.State = .{},
    loggedIn: bool = false,
    secStatusBio: []const u8 = "",
    signInHint: []const u8 = "",
    editor: Editor = .{},
    secEmotes: []const u8 = "",
    emotes: Emotes = .{},
    hasTools: bool = false,
    secCamPaths: []const u8 = "",
    camPaths: Campaths = .{},
    secPhotos: []const u8 = "",
    photos: Photos = .{},
};

/// render mirrors Go vrchatHTML (full tab).
pub fn render(h: *Html, s: State) !void {
    if (!s.available) {
        try c.panel(h, s.title, "");
        return c.emptyState(h, s.unavailable);
    }
    try c.panel(h, s.title, s.sub);
    try h.raw("<div id=vrc-status-region>");
    try renderStatus(h, s.status);
    try h.raw("</div>");
    try c.subTabs(h, "vrcg-sub:", s.subActive, s.subTabs);

    if (eq(s.subActive, "groups")) {
        try h.raw("<div id=vrcg-body>");
        try vg.render(h, s.groups);
        return h.raw("</div>");
    }

    try c.sectionOpen(h, s.secStatusBio);
    if (s.loggedIn) {
        try h.raw("<div id=vrc-editor>");
        try renderEditor(h, s.editor);
        try h.raw("</div>");
    } else {
        try c.hint(h, "info", s.signInHint);
    }
    try c.sectionClose(h);

    try c.sectionOpen(h, s.secEmotes);
    try renderEmotes(h, s.emotes);
    try c.sectionClose(h);

    if (s.hasTools) {
        try c.sectionOpen(h, s.secCamPaths);
        try h.raw("<div id=vrc-campaths>");
        try renderCampaths(h, s.camPaths);
        try h.raw("</div>");
        try c.sectionClose(h);

        try c.sectionOpen(h, s.secPhotos);
        try h.raw("<div id=vrc-photos-body>");
        try renderPhotos(h, s.photos);
        try h.raw("</div>");
        try c.sectionClose(h);
    }
}

/// renderStatus mirrors Go vrcStatusHTML (#vrc-status-region).
pub fn renderStatus(h: *Html, s: Status) !void {
    if (!s.present) return;
    try h.raw("<div class=\"rp-card\">");
    try c.statusRow(h, s.variant, s.label, s.dl, s.line);
    try h.raw("</div>");
}

/// renderEditor mirrors Go vrcEditorRenderHTML (#vrc-editor).
pub fn renderEditor(h: *Html, s: Editor) !void {
    // Status card.
    try h.raw("<div class=\"rp-card vrc-card\"><div class=vrc-h>");
    try h.esc(s.statusTitle);
    try h.raw(s.statusTip);
    try h.raw("</div><form data-act=vrc-status><label class=field><span class=field-label>");
    try h.esc(s.presenceLabel);
    try h.raw("</span><select class=\"field-input select-input\" name=status>");
    try options(h, s.presence);
    try h.raw("</select></label><label class=field><span class=field-label>");
    try h.esc(s.statusMsgLabel);
    try h.raw(" <b class=\"");
    try h.raw(s.descCls);
    try h.raw("\" id=vrc-desc-count>");
    try h.raw(s.descCount);
    try h.raw("</b></span><input class=field-input name=desc maxlength=32 value=\"");
    try h.esc(s.descVal);
    try h.raw("\" ");
    try countOn(h, "vrc-desc-count", s.maxDesc);
    try h.raw("></label><button class=\"rp-btn rp-btn--go\" type=submit>");
    try h.esc(s.saveStatus);
    try h.raw("</button></form><div class=btn-row>");
    try presetSelect(h, s.statusPreset);
    try c.btn(h, s.presetsLabel, "outline", "vrc-status-presets", "");
    try h.raw("</div></div>");

    // Bio card.
    try h.raw("<div class=\"rp-card vrc-card\"><div class=vrc-h>");
    try h.esc(s.bioTitle);
    try h.raw("</div><form data-act=vrc-bio><label class=field><span class=field-label>");
    try h.esc(s.bioTitle);
    try h.raw(" <b class=\"");
    try h.raw(s.bioCls);
    try h.raw("\" id=vrc-bio-count>");
    try h.raw(s.bioCount);
    try h.raw("</b></span><textarea class=field-input name=bio rows=4 ");
    try countOn(h, "vrc-bio-count", s.maxBio);
    try h.raw(">");
    try h.esc(s.bioVal);
    try h.raw("</textarea></label><button class=\"rp-btn rp-btn--go\" type=submit>");
    try h.esc(s.saveBio);
    try h.raw("</button></form>");
    try c.hint(h, "info", s.bioHint);
    if (s.hasPreview) {
        try h.raw("<div class=vrc-preview-wrap>");
        try h.esc(s.previewLabel);
        try h.raw("<div class=vrc-preview>");
        try h.esc(s.preview);
        try h.raw("</div></div>");
    }
    try h.raw("<div class=btn-row>");
    try presetSelect(h, s.bioPreset);
    try c.btn(h, s.presetsLabel, "outline", "vrc-bio-presets", "");
    try c.btn(h, s.varsLabel, "outline", "vrc-bio-vars", "");
    try c.btn(h, s.refreshLabel, "ghost", "vrc-events-refresh", "");
    try h.raw("</div></div>");
}

/// countOn mirrors Go vrcCountOn (inline rune counter; display only).
fn countOn(h: *Html, id: []const u8, max: i64) !void {
    try h.raw("oninput='var c=document.getElementById(\"");
    try h.raw(id);
    try h.raw("\");if(c){c.textContent=[...this.value].length+\" / ");
    try c.num(h, max);
    try h.raw("\";c.className=\"vrc-count\"+([...this.value].length>");
    try c.num(h, max);
    try h.raw("?\" over\":\"\")}'");
}

fn options(h: *Html, opts: []const Opt) !void {
    for (opts) |op| {
        try h.raw("<option value=");
        try h.attrQ(op.val);
        if (op.sel) try h.raw(" selected");
        try h.raw(">");
        try h.esc(op.label);
        try h.raw("</option>");
    }
}

fn presetSelect(h: *Html, s: PresetSel) !void {
    try h.raw("<select class=\"field-input select-input\" data-act=");
    try h.attrQ(s.act);
    try h.raw("><option value=\"\">");
    try h.esc(s.placeholder);
    try h.raw("</option>");
    for (s.names) |n| {
        try h.raw("<option value=");
        try h.attrQ(n);
        try h.raw(">");
        try h.esc(n);
        try h.raw("</option>");
    }
    try h.raw("</select>");
}

/// pathBtn mirrors Go vrcPathBtn (data-val is a filesystem path: real quotes + HTML escape).
fn pathBtn(h: *Html, label: []const u8, variant: []const u8, act: []const u8, path: []const u8) !void {
    try h.raw("<button class=\"rp-btn rp-btn--");
    try h.raw(variant);
    try h.raw("\" data-act=");
    try h.attrQ(act);
    try h.raw(" data-val=\"");
    try h.esc(path);
    try h.raw("\">");
    try h.esc(label);
    try h.raw("</button>");
}

/// renderEmotes mirrors Go vrcEmotesRenderHTML.
pub fn renderEmotes(h: *Html, s: Emotes) !void {
    try h.raw("<div class=\"rp-card vrc-card\">");
    try c.hint(h, "info", s.hint);
    try h.raw("<form data-act=vrc-emote-gen><label class=field><span class=field-label>");
    try h.esc(s.sourceLabel);
    try h.raw("</span><input class=field-input name=source placeholder=\"C:\\path\\clip.mp4\"></label>" ++
        "<label class=field><span class=field-label>");
    try h.esc(s.nameLabel);
    try h.raw("</span><input class=field-input name=name placeholder=\"emoji name\"></label>");
    try c.fpairOpen(h);
    try h.raw("<label class=field><span class=field-label>");
    try h.esc(s.framesLabel);
    try h.raw("</span><select class=\"field-input select-input\" name=frames>");
    for (s.frameOpts) |t| {
        try h.raw("<option value=");
        try c.num(h, t.frames);
        if (t.sel) try h.raw(" selected");
        try h.raw(">");
        try c.num(h, t.frames);
        try h.raw(" frames (");
        try c.num(h, t.grid);
        try h.raw("×");
        try c.num(h, t.grid);
        try h.raw(", ");
        try c.num(h, t.res);
        try h.raw("px)</option>");
    }
    try h.raw("</select></label><label class=field><span class=field-label>");
    try h.esc(s.fpsLabel);
    try h.raw("</span><input class=field-input name=fps type=number value=20 min=1 max=120></label>");
    try c.fpairClose(h);
    try c.fpairOpen(h);
    try h.raw("<label class=field><span class=field-label>");
    try h.esc(s.trimStart);
    try h.raw("</span><input class=field-input name=trimStart placeholder=\"optional\"></label>" ++
        "<label class=field><span class=field-label>");
    try h.esc(s.trimEnd);
    try h.raw("</span><input class=field-input name=trimEnd placeholder=\"optional\"></label>");
    try c.fpairClose(h);
    try h.raw("<label class=field><span class=field-label>");
    try h.esc(s.outDirLabel);
    try h.raw("</span><input class=field-input name=outdir value=\"");
    try h.esc(s.outDir);
    try h.raw("\"></label><label class=row><span class=row-label>");
    try h.esc(s.pingpong);
    try h.raw("</span><span class=switch><input type=checkbox name=pingpong value=1><span class=switch-track></span></span></label>" ++
        "<label class=row><span class=row-label>");
    try h.esc(s.crop);
    try h.raw("</span><span class=switch><input type=checkbox name=crop value=1><span class=switch-track></span></span></label>" ++
        "<div class=btn-row><input class=field-input name=cropx placeholder=\"x\" style=\"width:70px\">" ++
        "<input class=field-input name=cropy placeholder=\"y\" style=\"width:70px\">" ++
        "<input class=field-input name=cropw placeholder=\"w\" style=\"width:70px\">" ++
        "<input class=field-input name=croph placeholder=\"h\" style=\"width:70px\"></div>" ++
        "<button class=\"rp-btn rp-btn--go\" type=submit>");
    try h.esc(s.generate);
    try h.raw("</button></form><div id=vrc-emote-result></div><div class=btn-row>");
    try pathBtn(h, s.openFolder, "outline", "open-url", s.outDir);
    try c.btn(h, s.openUpload, "explore", "open-url", s.uploadUrl);
    try h.raw("</div></div>");
}

/// renderCampaths mirrors Go vrcCampathsHTML (#vrc-campaths).
pub fn renderCampaths(h: *Html, s: Campaths) !void {
    if (eq(s.state, "unavailable") or eq(s.state, "empty")) return c.emptyState(h, s.msg);
    if (eq(s.state, "loading")) return c.hint(h, "info", s.msg);

    try c.mdOpen(h);
    try h.raw("<div class=vrc-plist>");
    for (s.items) |it| {
        try h.raw("<button class=\"");
        try h.raw(if (it.active) "vrc-plist-item active" else "vrc-plist-item");
        try h.raw("\" data-act=\"vrc-campath:");
        try c.num(h, it.idx);
        try h.raw("\">");
        try h.esc(it.label);
        try h.raw("</button>");
    }
    try h.raw("</div>");
    try c.mdSplit(h);
    try h.raw(s.svg);
    try h.raw("<div class=vrc-cp-info><b>");
    try h.esc(s.name);
    try h.raw("</b><br>");
    try h.esc(s.info);
    try h.raw("</div>");
    try c.btnRowOpen(h);
    try h.raw(s.playBtn);
    try c.btn(h, s.load, "primary", "vrc-campath-load", "");
    try pathBtn(h, s.copy, "ghost", "copy", s.copyPath);
    try c.btn(h, s.organize, "outline", "vrc-campath-organize", "");
    try c.btnRowClose(h);
    try c.hint(h, "info", s.hint);
    try c.mdClose(h);
}

/// renderPhotos mirrors Go vrcPhotosHTML (#vrc-photos-body).
pub fn renderPhotos(h: *Html, s: Photos) !void {
    if (eq(s.state, "unavailable") or eq(s.state, "empty")) return c.emptyState(h, s.msg);
    if (eq(s.state, "loading")) return c.hint(h, "info", s.msg);

    try c.mdOpen(h);
    try h.raw("<div class=vrc-glist>");
    for (s.groups) |g| {
        try h.raw("<button class=\"");
        try h.raw(if (g.active) "vrc-glist-item active" else "vrc-glist-item");
        try h.raw("\" data-act=\"vrc-photos-group:");
        try h.esc(g.label);
        try h.raw("\"><span>");
        try h.esc(g.label);
        try h.raw("</span><span class=vrc-gcount>");
        try c.num(h, g.count);
        try h.raw("</span></button>");
    }
    try h.raw("</div>");
    try c.mdSplit(h);
    try h.raw("<div class=vrc-grid-photos>");
    for (s.cells) |ph| {
        try h.raw("<button class=vrc-cell data-act=\"vrc-photo-view:");
        try h.esc(ph.file);
        try h.raw("\" title=");
        try h.raw(ph.titleQ);
        try h.raw(">");
        // Cached resized-image endpoint: the browser lazy-loads + caches by URL (no base64 in
        // patches). onerror falls back to the placeholder tile if decode fails.
        if (ph.src.len == 0) {
            try h.raw("<div class=\"vrc-thumb vrc-thumb-ph\"></div>");
        } else {
            try h.raw("<img class=vrc-thumb loading=lazy src=");
            try h.attrQ(ph.src);
            try h.raw(" onerror=\"this.className='vrc-thumb vrc-thumb-broken'\">");
        }
        try h.raw("<span class=vrc-cap>");
        try h.esc(ph.label);
        try h.raw("</span></button>");
    }
    try h.raw("</div>");
    if (s.note.len != 0) {
        try h.raw("<div class=vrc-note>");
        try h.esc(s.note);
        try h.raw("</div>");
    }
    try h.raw("<div class=btn-row>");
    try pathBtn(h, s.openFolder, "outline", "open-url", s.photosDir);
    try h.raw("</div>");
    try c.mdClose(h);
}

test "unavailable tab" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .title = "VRChat", .sub = "s", .unavailable = "no vrchat" });
    try std.testing.expectEqualStrings("<h1 class=page-title>VRChat</h1>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">no vrchat</div></div>", h.b.items);
}

test "status region present + absent" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderStatus(&h, .{});
    try std.testing.expectEqualStrings("", h.b.items);
    try renderStatus(&h, .{ .present = true, .variant = "muted", .label = "VRChat", .dl = "vrchat", .line = "Not signed in" });
    try std.testing.expectEqualStrings("<div class=\"rp-card\"><div class=strow><span class=\"dot dot--muted\"></span>" ++
        "<div class=strow-tx><div class=strow-l data-label=\"vrchat\">VRChat</div>" ++
        "<div class=strow-s data-value=\"Not signed in\">Not signed in</div></div></div></div>", h.b.items);
}

test "countOn renders the max twice" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try countOn(&h, "vrc-desc-count", 32);
    try std.testing.expectEqualStrings("oninput='var c=document.getElementById(\"vrc-desc-count\");" ++
        "if(c){c.textContent=[...this.value].length+\" / 32\";c.className=\"vrc-count\"+([...this.value].length>32?\" over\":\"\")}'", h.b.items);
}

test "campaths loading + empty" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderCampaths(&h, .{ .state = "loading", .msg = "Loading…" });
    try std.testing.expectEqualStrings("<span class=\"hint hint--info\">Loading…</span>", h.b.items);
    h.b.clearRetainingCapacity();
    try renderPhotos(&h, .{ .state = "empty", .msg = "none" });
    try std.testing.expectEqualStrings("<div class=\"rp-empty\"><div class=\"rp-empty__title\">none</div></div>", h.b.items);
}
