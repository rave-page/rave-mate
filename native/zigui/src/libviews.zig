//! Library ALTERNATE bodies + the Library modals — byte-identical to the pure Go renderers
//! in internal/webui/{library_mirror.go,library_remotecue.go,render_library.go}
//! (golden gate: internal/webui/zigui_golden_libviews_test.go).
//!
//! Four surfaces:
//!   Mirror      — #lib-body while a paired peer is targeted: status banner + the peer's
//!                 document iframe. The banner is its own patch target (#rmirror-banner).
//!   RceBody     — #lib-body while remote-cue-editing: full-width waveform + info pane +
//!                 the shared inspector. Its #rce-info pane is patched on its own.
//!   RceSave     — the save/write-back section spliced into the cue-editor rail.
//!   SmartModal / RelocModal — the two Library dialogs (fleet's FIRST modal ports).
//!
//! Raw seams (trusted pre-rendered Go markup, emitted verbatim exactly as the Go renderer
//! splices it): the mirror banner's tipTopic tooltip, and RceBody.wave = ceWaveHTML() from
//! library_cueedit.go — the cue editor's own markup is NOT ported here. Nothing in this file
//! touches remote-cue-edit state or the peer StateSHA contract; it renders resolved state.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");
const k = @import("library_kit.zig");
const d = @import("library_detail.zig");

// ── mirror ──

/// MirrorBanner is the #rmirror-banner strip. hasNote / isErr are explicit flags (an empty
/// i18n string must not flip the branch); status is spliced into the class UNESCAPED, as Go
/// does — it is one of the four session-status constants.
pub const MirrorBanner = struct {
    status: []const u8 = "",
    title: []const u8 = "",
    tip: []const u8 = "", // legacy raw (bridge)
    tipSt: ?c.Tip = null, // structured tooltip — wins over tip
    hasNote: bool = false,
    note: []const u8 = "",
    isErr: bool = false,
    err: []const u8 = "",
    reconnect: []const u8 = "",
};

pub const Mirror = struct {
    noLink: bool = false,
    noLinkMsg: []const u8 = "",
    banner: MirrorBanner = .{},
};

pub fn renderMirror(h: *Html, st: Mirror) !void {
    if (st.noLink) {
        try h.raw("<div class=rp-card>");
        try c.emptyState(h, st.noLinkMsg);
        try h.raw("</div>");
        return;
    }
    try h.raw("<div id=rmirror-banner>");
    try renderMirrorBanner(h, st.banner);
    try h.raw("</div><div class=rmirror-frame><iframe id=__rmirror title=\"remote library\"></iframe></div>");
}

pub fn renderMirrorBanner(h: *Html, st: MirrorBanner) !void {
    try h.raw("<div class=\"rmirror-bar rmirror-");
    try h.raw(st.status);
    try h.raw("\"><span class=rmirror-dot></span><span class=rmirror-title>");
    try h.esc(st.title);
    try h.raw("</span>");
    try c.tipOr(h, st.tipSt, st.tip);
    if (st.hasNote) {
        try h.raw("<span class=rmirror-note>");
        try h.esc(st.note);
        try h.raw("</span>");
    }
    if (st.isErr) {
        try h.raw("<span class=\"rmirror-note rmirror-err\">");
        try h.esc(st.err);
        try h.raw("</span>");
        try c.btn(h, st.reconnect, "outline", "rmirror-reconnect", "");
    }
    try h.raw("</div>");
}

// ── remote cue edit ──

/// RceNav is one set prev/next control: plain button, or the disabled gated one at the edge.
pub const RceNav = struct {
    label: []const u8 = "",
    act: []const u8 = "",
    gated: bool = false,
    why: []const u8 = "",
};

fn rceNavBtn(h: *Html, n: RceNav) !void {
    if (n.gated) return c.btnGated(h, n.label, n.why);
    try c.btn(h, n.label, "outline", n.act, "");
}

/// RceInfo is the #rce-info left pane. show=false renders nothing (the export then returns
/// NULL and the Go fallback renders the same empty string).
pub const RceInfo = struct {
    show: bool = false,
    eyebrow: []const u8 = "",
    title: []const u8 = "",
    path: []const u8 = "",
    hasSet: bool = false,
    setLine: []const u8 = "",
    prev: RceNav = .{},
    next: RceNav = .{},
    localNote: []const u8 = "",
    hints: []const k.Hint = &.{},
    back: []const u8 = "",
};

pub fn renderRceInfo(h: *Html, st: RceInfo) !void {
    if (!st.show) return;
    try h.raw("<div class=rp-card><div class=insp-hd><div class=insp-eyebrow>");
    try h.esc(st.eyebrow);
    try h.raw("</div><div class=insp-title>");
    try h.esc(st.title);
    try h.raw("</div><div class=insp-sub>");
    try h.esc(st.path);
    try h.raw("</div></div>");
    if (st.hasSet) { // set header: position + prev/next (mirrors the ↑/↓ key nav)
        try h.raw("<div class=set-note>");
        try h.esc(st.setLine);
        try h.raw("</div>");
        try c.btnRowOpen(h);
        try rceNavBtn(h, st.prev);
        try rceNavBtn(h, st.next);
        try c.btnRowClose(h);
    }
    try k.pageSub(h, st.localNote);
    try k.hints(h, st.hints);
    try k.btnRowOf1(h, st.back, "outline", "ce-close");
    try h.raw("</div>");
}

/// RceBody is the whole #lib-body surface while remote-editing. wave = ceWaveHTML (raw).
pub const RceBody = struct {
    wave: []const u8 = "",
    info: RceInfo = .{},
    detail: d.Detail = .{},
};

pub fn renderRceBody(h: *Html, st: RceBody) !void {
    try h.raw("<div class=ce-fullwave>");
    try h.raw(st.wave);
    try h.raw("</div>");
    try c.mdWideOpen(h);
    try h.raw("<div id=rce-info>");
    try renderRceInfo(h, st.info);
    try h.raw("</div>");
    try c.mdSplit(h);
    try h.raw("<div id=lib-detail>");
    try d.render(h, st.detail);
    try h.raw("</div>");
    try c.mdClose(h);
}

/// RceWrite is one peer DJ-software write-back row: done hint, live button, or gated button.
pub const RceWrite = struct {
    done: bool = false,
    text: []const u8 = "",
    act: []const u8 = "",
    gated: bool = false,
    why: []const u8 = "",
};

/// RceSave is the save/write-back rail section. status ∈ {busy,dirty,saved,clean}.
pub const RceSave = struct {
    show: bool = false,
    header: []const u8 = "",
    moved: bool = false,
    movedText: []const u8 = "",
    reloadLbl: []const u8 = "",
    hasErr: bool = false,
    errText: []const u8 = "",
    status: []const u8 = "",
    statusText: []const u8 = "",
    unsavedText: []const u8 = "",
    saveLbl: []const u8 = "",
    hasWrites: bool = false,
    writeHeader: []const u8 = "",
    writes: []const RceWrite = &.{},
};

fn pbLabel(h: *Html, text: []const u8) !void {
    try h.raw("<div class=pb-label>");
    try h.esc(text);
    try h.raw("</div>");
}

fn setNote(h: *Html, text: []const u8) !void {
    try h.raw("<div class=set-note>");
    try h.esc(text);
    try h.raw("</div>");
}

pub fn renderRceSave(h: *Html, st: RceSave) !void {
    if (!st.show) return;
    try pbLabel(h, st.header);
    if (st.moved) {
        try c.hint(h, "warn", st.movedText);
        try k.btnRowOf1(h, st.reloadLbl, "outline", "rce-reload");
    }
    if (st.hasErr) try c.hint(h, "bad", st.errText);
    const eql = std.mem.eql;
    if (eql(u8, st.status, "busy")) {
        try setNote(h, st.statusText);
    } else if (eql(u8, st.status, "dirty")) {
        try c.hint(h, "warn", st.unsavedText);
        try h.raw("<div class=btn-col>");
        try c.btn(h, st.saveLbl, "primary", "rce-save", "");
        try h.raw("</div>");
    } else if (eql(u8, st.status, "saved")) {
        try c.hint(h, "ok", st.statusText);
    } else {
        try setNote(h, st.statusText);
    }
    if (!st.hasWrites) return;
    try pbLabel(h, st.writeHeader);
    for (st.writes) |w| {
        if (w.done) {
            try c.hint(h, "ok", w.text);
        } else if (w.gated) {
            try c.btnRowOpen(h);
            try c.btnGated(h, w.text, w.why);
            try c.btnRowClose(h);
        } else {
            try k.btnRowOf1(h, w.text, "outline", w.act);
        }
    }
}

// ── modals ──

/// SmartModal is the smart-rules editor dialog. hasDepth = an anchor track is picked, so the
/// direct/depth-2 chips show.
pub const SmartModal = struct {
    title: []const u8 = "",
    desc: []const u8 = "",
    name: k.PBField = .{},
    genresLbl: []const u8 = "",
    genres: []const k.Chip = &.{},
    feel: k.Select = .{},
    bpmMin: k.PBField = .{},
    bpmMax: k.PBField = .{},
    keyField: k.PBField = .{},
    rating: k.Select = .{},
    plays: k.PBField = .{},
    search: k.PBField = .{},
    compatLbl: []const u8 = "",
    compat: k.Select = .{},
    hasDepth: bool = false,
    depth: []const k.Chip = &.{},
    compatHint: []const u8 = "",
    count: []const u8 = "",
    confirm: []const u8 = "",
    cancel: []const u8 = "",
};

fn pbFieldLabelOpen(h: *Html, label: []const u8) !void {
    try h.raw("<div class=pb-field><div class=pb-label>");
    try h.esc(label);
    try h.raw("</div>");
}

pub fn renderSmartModal(h: *Html, st: SmartModal) !void {
    try c.modalOpen(h, st.title);
    try k.pageSub(h, st.desc);
    try h.raw("<div class=mform>");
    try k.pbField(h, st.name);
    try pbFieldLabelOpen(h, st.genresLbl); // genre chips from the collection
    try h.raw("<div class=seg>");
    for (st.genres) |g| try k.chip(h, g);
    try h.raw("</div></div>");
    try c.selectBox(h, st.feel);
    try h.raw("<div class=sr-band>");
    try k.pbField(h, st.bpmMin);
    try k.pbField(h, st.bpmMax);
    try h.raw("</div>");
    try k.pbField(h, st.keyField);
    try h.raw("<div class=sr-band>");
    try c.selectBox(h, st.rating);
    try k.pbField(h, st.plays);
    try h.raw("</div>");
    try k.pbField(h, st.search);
    try pbFieldLabelOpen(h, st.compatLbl);
    try c.selectBox(h, st.compat);
    if (st.hasDepth) {
        try h.raw("<div class=seg>");
        for (st.depth) |x| try k.chip(h, x);
        try h.raw("</div>");
    }
    try k.pageSub(h, st.compatHint);
    try h.raw("</div>"); // closes the compat pb-field
    try h.raw("<div id=lib-sr-count class=sr-count>");
    try h.esc(st.count);
    try h.raw("</div>");
    try c.btnRowOpen(h);
    try c.btn(h, st.confirm, "primary", "lib-sr-save", "");
    try c.btn(h, st.cancel, "outline", "modal-close", "");
    try c.btnRowClose(h);
    try h.raw("</div>"); // closes mform
    try c.modalFoot(h);
    try c.modalFootDefault(h);
    try c.modalClose(h);
}

/// RelocRow is one relocate candidate. act is spliced UNESCAPED (index-derived, like Go).
pub const RelocRow = struct {
    act: []const u8 = "",
    checked: bool = false,
    old: []const u8 = "",
    newPath: []const u8 = "",
    conf: []const u8 = "",
    confVar: []const u8 = "",
};

/// RelocModal is the relocate-missing dialog. hasMore = the candidate list hit the 200 cap.
pub const RelocModal = struct {
    title: []const u8 = "",
    desc: []const u8 = "",
    missing: []const u8 = "",
    root: []const u8 = "",
    rootPh: []const u8 = "",
    browseLbl: []const u8 = "",
    findLbl: []const u8 = "",
    hasMsg: bool = false,
    msg: []const u8 = "",
    hasRows: bool = false,
    rows: []const RelocRow = &.{},
    hasMore: bool = false,
    more: []const u8 = "",
    applyLbl: []const u8 = "",
};

pub fn renderRelocModal(h: *Html, st: RelocModal) !void {
    try c.modalOpen(h, st.title);
    try k.pageSub(h, st.desc);
    try k.pageSub(h, st.missing);
    try h.raw("<div class=lib-toolbar>");
    try k.fieldRaw(h, "lib-reloc-root", st.root, st.rootPh);
    try c.btn(h, st.browseLbl, "ghost", "pick-dir:lib-reloc-root", "");
    try h.raw("</div>");
    try k.btnRowOf1(h, st.findLbl, "outline", "lib-reloc-find");
    if (st.hasMsg) try c.hint(h, "info", st.msg);
    if (st.hasRows) {
        try h.raw("<div class=reloc-list>");
        for (st.rows) |r| {
            try h.raw("<div class=reloc-row><input type=checkbox data-act=\"");
            try h.raw(r.act);
            try h.raw("\"");
            if (r.checked) try h.raw(" checked");
            try h.raw("><span class=reloc-paths><span class=reloc-old>");
            try h.esc(r.old);
            try h.raw("</span><span class=reloc-new>→ ");
            try h.esc(r.newPath);
            try h.raw("</span></span>");
            try c.badge(h, r.conf, r.confVar);
            try h.raw("</div>");
        }
        if (st.hasMore) try k.pageSub(h, st.more);
        try h.raw("</div>");
        try k.btnRowOf1(h, st.applyLbl, "primary", "lib-reloc-apply");
    }
    try c.modalFoot(h);
    try c.modalFootDefault(h);
    try c.modalClose(h);
}

test "mirror: no link renders one card" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderMirror(&h, .{ .noLink = true, .noLinkMsg = "No paired peer & no link" });
    try std.testing.expectEqualStrings("<div class=rp-card><div class=\"rp-empty\">" ++
        "<div class=\"rp-empty__title\">No paired peer &amp; no link</div></div></div>", h.b.items);
}

test "mirror banner: status class raw, error arm adds the reconnect button" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderMirrorBanner(&h, .{ .status = "live", .title = "Mirroring \"studio\"", .tip = "<span class=tip></span>", .hasNote = true, .note = "Audio plays on the peer" });
    try std.testing.expectEqualStrings("<div class=\"rmirror-bar rmirror-live\"><span class=rmirror-dot></span>" ++
        "<span class=rmirror-title>Mirroring &#34;studio&#34;</span><span class=tip></span>" ++
        "<span class=rmirror-note>Audio plays on the peer</span></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try renderMirrorBanner(&h, .{ .status = "error", .title = "T", .isErr = true, .err = "timed out", .reconnect = "Reconnect" });
    try std.testing.expectEqualStrings("<div class=\"rmirror-bar rmirror-error\"><span class=rmirror-dot></span>" ++
        "<span class=rmirror-title>T</span><span class=\"rmirror-note rmirror-err\">timed out</span>" ++
        "<button class=\"rp-btn rp-btn--outline\" data-act=\"rmirror-reconnect\">Reconnect</button></div>", h.b.items);
}

test "rce info: gated edge buttons + hint order" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderRceInfo(&h, .{
        .show = true,
        .eyebrow = "Editing on studio",
        .title = "A & B",
        .path = "D:\\m\\a.flac",
        .hasSet = true,
        .setLine = "2 of 9",
        .prev = .{ .label = "Prev", .act = "rce-set-prev" },
        .next = .{ .label = "Next", .gated = true, .why = "End of set" },
        .localNote = "Audio runs here",
        .hints = &.{.{ .tone = "ok", .text = "In sync" }},
        .back = "Back",
    });
    try std.testing.expectEqualStrings("<div class=rp-card><div class=insp-hd><div class=insp-eyebrow>Editing on studio</div>" ++
        "<div class=insp-title>A &amp; B</div><div class=insp-sub>D:\\m\\a.flac</div></div>" ++
        "<div class=set-note>2 of 9</div><div class=btn-row>" ++
        "<button class=\"rp-btn rp-btn--outline\" data-act=\"rce-set-prev\">Prev</button>" ++
        "<button class=\"rp-btn rp-btn--outline\" disabled title=\"End of set\">Next</button></div>" ++
        "<p class=page-sub>Audio runs here</p><span class=\"hint hint--ok\">In sync</span>" ++
        "<div class=btn-row><button class=\"rp-btn rp-btn--outline\" data-act=\"ce-close\">Back</button></div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try renderRceInfo(&h, .{});
    try std.testing.expectEqualStrings("", h.b.items);
}

test "rce save: dirty arm + write-back rows" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderRceSave(&h, .{
        .show = true,
        .header = "Save to studio",
        .status = "dirty",
        .unsavedText = "Unsaved edits",
        .saveLbl = "Save to studio",
        .hasWrites = true,
        .writeHeader = "Write on studio",
        .writes = &.{
            .{ .done = true, .text = "Wrote 1 to Traktor" },
            .{ .text = "Write to Rekordbox", .gated = true, .why = "Save first" },
        },
    });
    try std.testing.expectEqualStrings("<div class=pb-label>Save to studio</div>" ++
        "<span class=\"hint hint--warn\">Unsaved edits</span>" ++
        "<div class=btn-col><button class=\"rp-btn rp-btn--primary\" data-act=\"rce-save\">Save to studio</button></div>" ++
        "<div class=pb-label>Write on studio</div>" ++
        "<span class=\"hint hint--ok\">Wrote 1 to Traktor</span>" ++
        "<div class=btn-row><button class=\"rp-btn rp-btn--outline\" disabled title=\"Save first\">Write to Rekordbox</button></div>", h.b.items);
}

test "reloc modal: rows, cap note, default footer" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderRelocModal(&h, .{
        .title = "Relocate",
        .desc = "d",
        .missing = "3 missing",
        .root = "D:\\m",
        .rootPh = "Search root",
        .browseLbl = "Browse",
        .findLbl = "Find",
        .hasRows = true,
        .applyLbl = "Apply",
        .rows = &.{.{ .act = "lib-reloc-skip:0", .checked = true, .old = "a&b.flac", .newPath = "D:\\m\\a&b.flac", .conf = "Unique", .confVar = "success" }},
        .hasMore = true,
        .more = "Showing 200 of 900",
    });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=reloc-row><input type=checkbox data-act=\"lib-reloc-skip:0\" checked>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<span class=reloc-new>→ D:\\m\\a&amp;b.flac</span>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<p class=page-sub>Showing 200 of 900</p></div>") != null);
    try std.testing.expect(std.mem.endsWith(u8, h.b.items, "data-act=\"modal-close\">Close</button></div></div>"));
}
