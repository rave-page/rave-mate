//! Library inspector (#lib-detail): selection header, actions, player, encode builder,
//! Camelot wheel, tags, playlist membership, works-together, metadata. Mirrors the pure
//! renderers in internal/webui/render_library.go byte-for-byte.
//!
//! Raw seams (pre-rendered Go markup, emitted verbatim exactly as the Go renderer does):
//! cue-edit rail / gridfix cockpit (kind="raw"), the media player, the tag editor, the
//! works-together section and the Camelot wheel SVG (all its
//! float math + %.2f formatting stays Go-side).

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");
const k = @import("library_kit.zig");
const f = @import("libfixers.zig");

pub const TrackPls = struct {
    unavailable: bool = false,
    chips: []const k.Chip = &.{},
    emptyText: []const u8 = "",
    addLbl: []const u8 = "",
    addAct: []const u8 = "",
};

pub const Harm = struct {
    desc: []const u8 = "",
    wheel: []const u8 = "",
    sameLbl: []const u8 = "",
    relLbl: []const u8 = "",
    showLbl: []const u8 = "",
    showAct: []const u8 = "",
    clearLbl: []const u8 = "",
};

pub const EncVideo = struct {
    vcodec: k.SelTip = .{},
    accel: k.Select = .{},
    qualityLbl: []const u8 = "",
    profiles: []const k.Chip = &.{},
    profileHint: []const u8 = "",
    rateMode: k.SelTip = .{},
    rateField: k.PBField = .{},
    res: k.Select = .{},
    fps: k.PBField = .{},
};

pub const Enc = struct {
    preset: k.Select = .{},
    desc: []const u8 = "",
    hints: []const k.Hint = &.{},
    audioOnly: bool = false,
    container: k.SelTip = .{},
    video: EncVideo = .{},
    audioCodec: k.SelTip = .{},
    audioBitrate: k.PBField = .{},
    channels: k.Select = .{},
    sampleRate: k.Select = .{},
    loud: c.Loud = .{},
    trimStart: k.PBField = .{},
    trimEnd: k.PBField = .{},
    outputNote: []const u8 = "",
    startLbl: []const u8 = "",
    saveLbl: []const u8 = "",
    saveAsLbl: []const u8 = "",
};

pub const Detail = struct {
    kind: []const u8 = "",
    raw: []const u8 = "",
    msg: []const u8 = "",
    gf: f.GF = .{},

    eyebrow: []const u8 = "",
    title: []const u8 = "",
    sub: []const u8 = "",

    actionsTitle: []const u8 = "",
    missing: []const u8 = "",
    actBtns: []const k.Btn = &.{},

    hasPlayer: bool = false,
    playerTitle: []const u8 = "",
    player: []const u8 = "",

    hasEnc: bool = false,
    encTitle: []const u8 = "",
    encDemoted: bool = false,
    demotedNote: []const u8 = "",
    showLbl: []const u8 = "",
    enc: Enc = .{},

    hasHarm: bool = false,
    harmTitle: []const u8 = "",
    harm: Harm = .{},

    hasTags: bool = false,
    tagsTitle: []const u8 = "",
    tagsDesc: []const u8 = "",
    writeLbl: []const u8 = "",
    writeAct: []const u8 = "",
    revertLbl: []const u8 = "",
    revertAct: []const u8 = "",
    tagEditor: f.TagEdit = .{},

    hasPls: bool = false,
    plsTitle: []const u8 = "",
    pls: TrackPls = .{},

    hasCompat: bool = false,
    compatTitle: []const u8 = "",
    compat: f.Compat = .{},

    detailsTitle: []const u8 = "",
    meta: []const c.KV = &.{},
};

pub fn render(h: *Html, st: Detail) !void {
    if (std.mem.eql(u8, st.kind, "raw")) return h.raw(st.raw); // cue-edit rail
    if (std.mem.eql(u8, st.kind, "gf")) return f.renderGF(h, st.gf); // beatgrid-fixer rail
    if (std.mem.eql(u8, st.kind, "msg")) return c.emptyState(h, st.msg);

    try h.raw("<div class=insp-hd><div class=insp-eyebrow>");
    try h.esc(st.eyebrow);
    try h.raw("</div><div class=insp-title>");
    try h.esc(st.title);
    try h.raw("</div><div class=insp-sub>");
    try h.esc(st.sub);
    try h.raw("</div></div>");

    // ACTIONS (a missing file prefixes the row with its note)
    try k.inspSecOpen(h, st.actionsTitle);
    if (st.missing.len != 0) try k.pageSub(h, st.missing);
    try c.btnRowOpen(h);
    for (st.actBtns) |b| try c.btnOf(h, b);
    try c.btnRowClose(h);
    try k.inspSecClose(h);

    // PLAYER + waveform (audio on disk) - the unified media player/editor (player.go)
    if (st.hasPlayer) {
        try k.inspSecOpen(h, st.playerTitle);
        try h.raw(st.player);
        try k.inspSecClose(h);
    }
    // ENCODE builder (audio + video); demoted in collection/playlist context
    if (st.hasEnc) {
        try k.inspSecOpen(h, st.encTitle);
        if (st.encDemoted) {
            try k.pageSub(h, st.demotedNote);
            try k.btnRowOf1(h, st.showLbl, "ghost", "lib-enc-open");
        } else {
            try renderEnc(h, st.enc);
        }
        try k.inspSecClose(h);
    }
    // HARMONIC key-wheel (audio with a key)
    if (st.hasHarm) {
        try k.inspSecOpen(h, st.harmTitle);
        try renderHarm(h, st.harm);
        try k.inspSecClose(h);
    }
    // TAGS (collection audio): library→file sync buttons + the manual tag editor
    if (st.hasTags) {
        try k.inspSecOpen(h, st.tagsTitle);
        try k.pageSub(h, st.tagsDesc);
        try c.btnRowOpen(h);
        try c.btn(h, st.writeLbl, "primary", st.writeAct, "");
        try c.btn(h, st.revertLbl, "ghost", st.revertAct, "");
        try c.btnRowClose(h);
        try f.renderTagEdit(h, st.tagEditor);
        try k.inspSecClose(h);
    }
    // PLAYLISTS membership
    if (st.hasPls) {
        try k.inspSecOpen(h, st.plsTitle);
        try renderTrackPls(h, st.pls);
        try k.inspSecClose(h);
    }
    // WORKS WELL TOGETHER (compat marks + discovery)
    if (st.hasCompat) {
        try k.inspSecOpen(h, st.compatTitle);
        try f.renderCompat(h, st.compat);
        try k.inspSecClose(h);
    }
    // DETAILS
    try k.inspSecOpen(h, st.detailsTitle);
    for (st.meta) |kvr| try c.kvOf(h, kvr);
    try k.inspSecClose(h);
}

fn renderHarm(h: *Html, st: Harm) !void {
    try k.pageSub(h, st.desc);
    try h.raw(st.wheel); // Go-built SVG (all float math stays Go-side)
    try h.raw("<div class=kw-legend><span><i style=\"background:#08F79B\"></i>");
    try h.esc(st.sameLbl);
    try h.raw("</span><span><i style=\"background:#7C3AED\"></i>");
    try h.esc(st.relLbl);
    try h.raw("</span><span><i style=\"background:#FF3E8A\"></i>+1</span>" ++
        "<span><i style=\"background:#FFB547\"></i>−1</span></div>");
    try c.btnRowOpen(h);
    try c.btn(h, st.showLbl, "outline", st.showAct, "");
    try c.btn(h, st.clearLbl, "ghost", "lib-key-clear", "");
    try c.btnRowClose(h);
}

fn renderTrackPls(h: *Html, st: TrackPls) !void {
    if (st.unavailable) return h.raw("<p class=page-sub>-</p>");
    if (st.chips.len == 0) {
        try h.raw("<span class=page-sub>");
        try h.esc(st.emptyText);
        try h.raw("</span>");
    } else {
        for (st.chips) |ch| try k.chip(h, ch);
    }
    try h.raw("<div class=btn-row>");
    try c.btn(h, st.addLbl, "outline", st.addAct, "");
    try h.raw("</div>");
}

/// renderEnc mirrors libEncHTML: preset picker, source-aware hints, then the pbuilder
/// (container · video group · audio group · loudness · trim) and the run/save row.
pub fn renderEnc(h: *Html, st: Enc) !void {
    try c.selectBox(h, st.preset);
    if (st.desc.len != 0) {
        try h.raw("<div class=pb-hint>");
        try h.esc(st.desc);
        try h.raw("</div>");
    }
    if (st.hints.len != 0) {
        try h.raw("<div class=mediahints>");
        try k.hints(h, st.hints);
        try h.raw("</div>");
    }
    try h.raw("<div class=pbuilder>");
    try k.selTip(h, st.container);
    if (!st.audioOnly) {
        const v = st.video;
        try h.raw("<div class=pb-grp>");
        // container-compatible codecs only - the builder can't describe an unencodable combo
        try k.selTip(h, v.vcodec);
        try c.selectBox(h, v.accel);
        // quality profiles
        try h.raw("<div class=pb-field><div class=pb-label>");
        try h.esc(v.qualityLbl);
        try h.raw("</div><div class=seg>");
        for (v.profiles) |ch| try k.chip(h, ch);
        try h.raw("</div><div class=pb-hint>");
        try h.esc(v.profileHint);
        try h.raw("</div></div>");
        try k.selTip(h, v.rateMode);
        try k.pbField(h, v.rateField);
        try c.selectBox(h, v.res);
        try k.pbField(h, v.fps);
        try h.raw("</div>");
    }
    // audio section
    try h.raw("<div class=pb-grp>");
    try k.selTip(h, st.audioCodec);
    try k.pbField(h, st.audioBitrate);
    try c.selectBox(h, st.channels);
    try c.selectBox(h, st.sampleRate);
    try h.raw("</div>");
    // loudness - the shared block (components.zig loudnessFields), resolved Go-side
    try c.loudnessFields(h, st.loud);
    // trim + start
    try k.pbField(h, st.trimStart);
    try k.pbField(h, st.trimEnd);
    try h.raw("</div>");
    try h.raw("<div class=pb-hint>");
    try h.esc(st.outputNote);
    try h.raw("</div>");
    try h.raw("<div class=btn-row>");
    try c.btn(h, st.startLbl, "primary", "lib-transcode", "");
    try c.btn(h, st.saveLbl, "outline", "lib-pset-save", "");
    try c.btn(h, st.saveAsLbl, "ghost", "lib-pset-saveas", "");
    try h.raw("</div>");
}

test "detail raw + msg passthrough" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .kind = "raw", .raw = "<div>rail</div>" });
    try std.testing.expectEqualStrings("<div>rail</div>", h.b.items);
    h.b.clearRetainingCapacity();
    try render(&h, .{ .kind = "msg", .msg = "Nothing selected" });
    try std.testing.expectEqualStrings("<div class=\"rp-empty\"><div class=\"rp-empty__title\">Nothing selected</div></div>", h.b.items);
}

test "track playlists empty line" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderTrackPls(&h, .{ .emptyText = "Not in a playlist", .addLbl = "Add", .addAct = "lib-track-addto:x" });
    try std.testing.expectEqualStrings("<span class=page-sub>Not in a playlist</span>" ++
        "<div class=btn-row><button class=\"rp-btn rp-btn--outline\" data-act=\"lib-track-addto:x\">Add</button></div>", h.b.items);
}
