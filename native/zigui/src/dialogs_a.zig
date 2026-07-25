//! Wave-4 dialog sweep A — the publish/transcode dialog family, mirrored from the pure Go
//! renderers in internal/webui/render_dialogs_a.go (golden reference).
//!
//! Every dialog ENDS with the components.zig modal bracket triple, so the bytes Go's
//! `openModal` sees are identical either way. The shared confirm/picker/context-menu shape
//! lives in components.zig as `Choice`/`choiceDialog` — six call sites across the publish
//! local, publish remote and tracklist flows use it instead of six near-copy ports.
//!
//! Raw (trusted) pass-throughs, matching what Go splices UNESCAPED:
//!  - the shared loudness block (components.zig loudnessFields) inside the preset editor,
//!  - clock/offset readouts (`15:04:05`, pubClock) and row numbers in the time-fix preview,
//!  - the hand-written English message literals that wrap an already-escaped value
//!    (Choice.msgRaw).

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");
const k = @import("library_kit.zig");

// ── shared bits ──

/// copyBtn is the hand-rolled primary "copy this text" button both export dialogs carry:
/// data-val holds the WHOLE payload (Go splices html.EscapeString of it).
fn copyBtn(h: *Html, label: []const u8, content: []const u8) !void {
    try h.raw("<button class=\"rp-btn rp-btn--primary\" data-act=\"copy\" data-val=\"");
    try h.esc(content);
    try h.raw("\">");
    try h.esc(label);
    try h.raw("</button>");
}

/// exportTA is the read-only preview textarea. rows differs per dialog (Go literals).
fn exportTA(h: *Html, rows: []const u8, content: []const u8) !void {
    try h.raw("<textarea class=pub-export-ta readonly rows=");
    try h.raw(rows);
    try h.raw(">");
    try h.esc(content);
    try h.raw("</textarea>");
}

fn setNote(h: *Html, text: []const u8) !void {
    try h.raw("<div class=set-note>");
    try h.esc(text);
    try h.raw("</div>");
}

// ── publish: text-export style dialog (publish_export.go pubTxtOpen) ──

/// TxtExport is the tracklist text-export dialog: style preset select, editable line
/// template, header switch, placeholder legend and the live preview. Every control change
/// re-opens the whole dialog (no fragment patch), so there is no `_frag` export.
pub const TxtExport = struct {
    title: []const u8 = "",
    sel: c.Select = .{},
    tmpl: c.Field = .{},
    header: c.Toggle = .{},
    place: []const u8 = "",
    content: []const u8 = "",
    copyLbl: []const u8 = "",
    closeLbl: []const u8 = "",
};

pub fn renderTxtExport(h: *Html, st: TxtExport) !void {
    try c.modalOpen(h, st.title);
    try h.raw("<div class=pub-txt-opts><span class=pub-txt-presel>");
    try c.selectBox(h, st.sel);
    try h.raw("</span>");
    try c.fieldOf(h, st.tmpl);
    try c.toggleOf(h, st.header);
    try h.raw("</div>");
    try k.pageSub(h, st.place);
    try exportTA(h, "12", st.content);
    try c.modalFoot(h);
    try copyBtn(h, st.copyLbl, st.content);
    try c.btn(h, st.closeLbl, "outline", "modal-close", "");
    try c.modalClose(h);
}

// ── publish: export preview (publish_actions.go pubExportModal; also the remote arm) ──

/// ExportPrev is the CSV/JSON tracklist preview. note/copyLbl/closeLbl are HARDCODED
/// ENGLISH literals in Go (not i18n keys) — carried in state, not re-invented here. note is
/// spliced RAW on the Go side, so it is emitted raw.
pub const ExportPrev = struct {
    title: []const u8 = "",
    note: []const u8 = "",
    content: []const u8 = "",
    copyLbl: []const u8 = "",
    closeLbl: []const u8 = "",
};

pub fn renderExportPrev(h: *Html, st: ExportPrev) !void {
    try c.modalOpen(h, st.title);
    try h.raw("<div class=np-artist>");
    try h.raw(st.note);
    try h.raw("</div>");
    try exportTA(h, "14", st.content);
    try c.modalFoot(h);
    try copyBtn(h, st.copyLbl, st.content);
    try c.btn(h, st.closeLbl, "outline", "modal-close", "");
    try c.modalClose(h);
}

// ── publish: rename set (publish_actions.go pubRenameOpen) ──

/// Rename is the rename-set form dialog. The value travels in the form POST (m.Form), so
/// the input is NAMED rather than data-act'd. Footer = Go's default Close.
pub const Rename = struct {
    title: []const u8 = "",
    id: []const u8 = "",
    nameLbl: []const u8 = "",
    nameDL: []const u8 = "",
    cur: []const u8 = "",
    submit: []const u8 = "",
};

pub fn renderRename(h: *Html, st: Rename) !void {
    try c.modalOpen(h, st.title);
    try h.raw("<form data-act=pub-rename-do class=mform>");
    try c.hiddenField(h, "id", st.id);
    try c.labeledInput(h, "name", st.nameLbl, st.nameDL, st.cur);
    try h.raw("<button class=\"rp-btn rp-btn--primary\" type=submit>");
    try h.esc(st.submit);
    try h.raw("</button></form>");
    try c.modalFoot(h);
    try c.modalFootDefault(h);
    try c.modalClose(h);
}

// ── publish: capture-aligned time fix preview (publish_export.go pubFixModal) ──

/// FixRow is one previewed tracklist row. num/off/newOff are Go-formatted (fmt.Sprint of an
/// int, pubClock) and spliced RAW both sides. removed swaps in the strike-through arm.
pub const FixRow = struct {
    num: []const u8 = "",
    off: []const u8 = "",
    newOff: []const u8 = "",
    removed: bool = false,
    label: []const u8 = "",
};

/// Fix is the "Fix start times" preview. hasOpener = the heuristic plan offered >1 candidate
/// opener (a fader-history plan is exact and shows no picker). startT/newT are Go
/// time.Format("15:04:05") strings, raw.
pub const Fix = struct {
    title: []const u8 = "",
    desc: []const u8 = "",
    hasOpener: bool = false,
    opener: c.Select = .{},
    setStartLbl: []const u8 = "",
    startT: []const u8 = "",
    newT: []const u8 = "",
    rows: []const FixRow = &.{},
    removedTx: []const u8 = "",
    applyLbl: []const u8 = "",
    applyAct: []const u8 = "",
    cancelLbl: []const u8 = "",
};

pub fn renderFix(h: *Html, st: Fix) !void {
    try c.modalOpen(h, st.title);
    try h.raw("<div class=np-artist>");
    try h.esc(st.desc);
    try h.raw("</div>");
    if (st.hasOpener) {
        try h.raw("<div class=pub-fix-opener>");
        try c.selectBox(h, st.opener);
        try h.raw("</div>");
    }
    try h.raw("<div class=pub-fix-rows><div class=pub-fix-row><span class=pub-track-l>");
    try h.esc(st.setStartLbl);
    try h.raw("</span><span class=pub-track-o>");
    try h.raw(st.startT);
    try h.raw(" → ");
    try h.raw(st.newT);
    try h.raw("</span></div>");
    for (st.rows) |r| {
        if (r.removed) {
            try h.raw("<div class=\"pub-fix-row pub-fix-removed\"><span class=pub-track-n>");
            try h.raw(r.num);
            try h.raw(".</span><span class=pub-track-o>[");
            try h.raw(r.off);
            try h.raw("] ✕</span><span class=pub-track-l>");
            try h.esc(r.label);
            try h.raw(" · ");
            try h.esc(st.removedTx);
            try h.raw("</span></div>");
            continue;
        }
        try h.raw("<div class=pub-fix-row><span class=pub-track-n>");
        try h.raw(r.num);
        try h.raw(".</span><span class=pub-track-o>[");
        try h.raw(r.off);
        try h.raw("] → [");
        try h.raw(r.newOff);
        try h.raw("]</span><span class=pub-track-l>");
        try h.esc(r.label);
        try h.raw("</span></div>");
    }
    try h.raw("</div>");
    try c.modalFoot(h);
    try c.btnRowOf(h, &.{
        .{ .label = st.applyLbl, .variant = "primary", .act = st.applyAct },
        .{ .label = st.cancelLbl, .variant = "ghost", .act = "modal-close" },
    });
    try c.modalClose(h);
}

// ── export preset editor (pbuilder.go mpPresetModal) ──

/// Preset is the export preset editor over the unified player's export block. Every "is it
/// shown" branch rides as an explicit flag (never "empty means the other arm"): hasSrc,
/// hasVideo (not an audio-only media), hasVEnc (the codec really re-encodes), hasLadder /
/// hasVbrTgl / hasVbrq / hasChips (the audio-bitrate arms), hasLossless.
/// loud = the SHARED loudness block (components.zig loudnessFields) as structured state —
/// same as the library encode builder.
pub const Preset = struct {
    title: []const u8 = "",
    idField: k.PBField = .{},
    labelField: k.PBField = .{},
    hasSrc: bool = false,
    srcHint: []const u8 = "",
    container: k.SelTip = .{},
    hasVideo: bool = false,
    vcodec: k.SelTip = .{},
    hasVEnc: bool = false,
    accel: c.Select = .{},
    rateMode: k.SelTip = .{},
    rateField: k.PBField = .{},
    res: c.Select = .{},
    fps: k.PBField = .{},
    acodec: k.SelTip = .{},
    hasLadder: bool = false,
    hasVbrTgl: bool = false,
    vbr: c.Toggle = .{},
    hasVbrq: bool = false,
    vbrq: c.Select = .{},
    hasChips: bool = false,
    bitrateLbl: []const u8 = "",
    chips: []const k.Chip = &.{},
    maxHint: []const u8 = "",
    hasLossless: bool = false,
    losslessTx: []const u8 = "",
    channels: c.Select = .{},
    samplerate: c.Select = .{},
    loud: c.Loud = .{},
    warns: []const k.Hint = &.{},
    foot: []const c.Btn = &.{},
};

pub fn renderPreset(h: *Html, st: Preset) !void {
    try c.modalOpen(h, st.title);
    try h.raw("<div class=pedit>");
    try c.fpairOpen(h);
    try k.pbField(h, st.idField);
    try k.pbField(h, st.labelField);
    try c.fpairClose(h);
    if (st.hasSrc) try c.hint(h, "info", st.srcHint);
    try k.selTip(h, st.container);
    if (st.hasVideo) {
        try h.raw("<div class=pb-grp>");
        try k.selTip(h, st.vcodec);
        if (st.hasVEnc) {
            try c.selectBox(h, st.accel);
            try k.selTip(h, st.rateMode);
            try k.pbField(h, st.rateField);
            try c.selectBox(h, st.res);
            try k.pbField(h, st.fps);
        }
        try h.raw("</div>");
    }
    try h.raw("<div class=pb-grp>");
    try k.selTip(h, st.acodec);
    if (st.hasLadder) {
        if (st.hasVbrTgl) try c.toggleOf(h, st.vbr);
        if (st.hasVbrq) {
            try c.selectBox(h, st.vbrq);
        } else if (st.hasChips) {
            try h.raw("<div class=pb-field><div class=pb-label>");
            try h.esc(st.bitrateLbl);
            try h.raw("</div><div class=lt-chips>");
            for (st.chips) |ch| try k.chip(h, ch);
            try h.raw("</div><div class=pb-hint>");
            try h.esc(st.maxHint);
            try h.raw("</div></div>");
        }
    } else if (st.hasLossless) {
        try h.raw("<div class=pb-hint>");
        try h.esc(st.losslessTx);
        try h.raw("</div>");
    }
    try c.fpairOpen(h);
    try c.selectBox(h, st.channels);
    try c.selectBox(h, st.samplerate);
    try c.fpairClose(h);
    try h.raw("</div>");
    try c.loudnessFields(h, st.loud);
    try k.hints(h, st.warns);
    try h.raw("</div>");
    try c.modalFoot(h);
    try c.btnRowOf(h, st.foot);
    try c.modalClose(h);
}

// ── cue editor: saved-pattern manager (library_cueedit.go cePatternManagerHTML) ──

/// PatRow is one stored cue pattern. The rename form's act and the delete button's act are
/// `ce-pat-rename:`/`ce-pat-del:` ++ id (the prefix carries nothing escapable, so
/// concatenating then escaping matches Go's attrQ of the joined string).
pub const PatRow = struct {
    id: []const u8 = "",
    name: []const u8 = "",
    meta: []const u8 = "",
    owGated: bool = false,
    owLbl: []const u8 = "",
    owWhy: []const u8 = "",
    delLbl: []const u8 = "",
};

/// PatMgr is the manage-patterns dialog. gone = the pattern store is unavailable (one bad
/// hint, nothing else); hasEmpty = the store is there but holds no patterns.
pub const PatMgr = struct {
    title: []const u8 = "",
    gone: bool = false,
    goneTx: []const u8 = "",
    hasEmpty: bool = false,
    emptyTx: []const u8 = "",
    pats: []const PatRow = &.{},
    renameLbl: []const u8 = "",
    note: []const u8 = "",
};

pub fn renderPatMgr(h: *Html, st: PatMgr) !void {
    try c.modalOpen(h, st.title);
    if (st.gone) {
        try c.hint(h, "bad", st.goneTx);
        try c.modalFoot(h);
        try c.modalFootDefault(h);
        try c.modalClose(h);
        return;
    }
    if (st.hasEmpty) try setNote(h, st.emptyTx);
    for (st.pats) |p| {
        try h.raw("<div class=pb-label>");
        try h.esc(p.name);
        try h.raw("</div>");
        try setNote(h, p.meta);
        try h.raw("<form data-act=\"ce-pat-rename:");
        try h.esc(p.id);
        try h.raw("\" class=lib-toolbar>");
        try c.labeledInput(h, "name", "", "", p.name);
        try h.raw("<button class=\"rp-btn rp-btn--outline\" type=submit>");
        try h.esc(st.renameLbl);
        try h.raw("</button></form>");
        try c.btnRowOpen(h);
        if (p.owGated) {
            try c.btnGated(h, p.owLbl, p.owWhy);
        } else {
            try c.btnAct(h, p.owLbl, "outline", "ce-pat-ow:", p.id);
        }
        try c.btnAct(h, p.delLbl, "destructive", "ce-pat-del:", p.id);
        try c.btnRowClose(h);
    }
    try setNote(h, st.note);
    try c.modalFoot(h);
    try c.modalFootDefault(h);
    try c.modalClose(h);
}

test "text-export dialog: controls + preview + copy footer" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderTxtExport(&h, .{
        .title = "Export as text",
        .sel = .{ .id = "pub-txt-preset", .label = "Style", .curLabel = "Classic" },
        .tmpl = .{ .label = "Line", .dl = "line", .act = "pub-txt-line:r1", .value = "{n}. {track}", .inputType = "text" },
        .header = .{ .label = "Header", .dl = "header", .act = "pub-txt-header:r1", .on = true },
        .place = "Placeholders: {n}",
        .content = "1. A & B",
        .copyLbl = "Copy",
        .closeLbl = "Close",
    });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=pub-txt-opts><span class=pub-txt-presel><div class=ss-field>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<textarea class=pub-export-ta readonly rows=12>1. A &amp; B</textarea>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-act=\"copy\" data-val=\"1. A &amp; B\">Copy</button>") != null);
}

test "export preview: note raw, content escaped twice (body + copy payload)" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderExportPrev(&h, .{ .title = "Export - csv", .note = "Select all + copy.", .content = "a,\"b\"", .copyLbl = "Copy", .closeLbl = "Close" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=np-artist>Select all + copy.</div>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "rows=14>a,&#34;b&#34;</textarea>") != null);
}

test "time-fix preview: removed arm, offset row, no opener picker" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderFix(&h, .{
        .title = "Fix start times",
        .desc = "set.ogg · 0:07 lead",
        .setStartLbl = "Set start",
        .startT = "21:00:00",
        .newT = "21:00:07",
        .rows = &.{
            .{ .num = "1", .off = "0:00", .removed = true, .label = "A & B" },
            .{ .num = "2", .off = "0:30", .newOff = "0:23", .label = "C" },
        },
        .removedTx = "removed",
        .applyLbl = "Apply",
        .applyAct = "pub-fixtimes-do:r1",
        .cancelLbl = "Cancel",
    });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<span class=pub-track-o>21:00:00 → 21:00:07</span>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<span class=pub-track-o>[0:00] ✕</span><span class=pub-track-l>A &amp; B · removed</span>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<span class=pub-track-o>[0:30] → [0:23]</span>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "pub-fix-opener") == null);
}

test "pattern manager: store gone renders one bad hint and stops" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderPatMgr(&h, .{ .title = "Patterns", .gone = true, .goneTx = "Store unavailable" });
    try std.testing.expectEqualStrings("<div class=modal-scrim data-act=modal-close></div>" ++
        "<div class=modal role=dialog><div class=modal-head><h3 class=modal-title>Patterns</h3>" ++
        "<button class=modal-x data-act=modal-close aria-label=Close>✕</button></div>" ++
        "<div class=modal-body><span class=\"hint hint--bad\">Store unavailable</span></div>" ++
        "<div class=modal-foot><button class=\"rp-btn rp-btn--outline\" data-act=\"modal-close\">Close</button>" ++
        "</div></div>", h.b.items);
}

test "pattern manager: gated overwrite vs live overwrite" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderPatMgr(&h, .{
        .title = "Patterns",
        .pats = &.{
            .{ .id = "p1", .name = "Intro", .meta = "4 cues", .owGated = true, .owLbl = "Overwrite", .owWhy = "Select cues first", .delLbl = "Delete" },
            .{ .id = "p2", .name = "Drop", .meta = "2 cues", .owLbl = "Overwrite (2)", .delLbl = "Delete" },
        },
        .renameLbl = "Rename",
        .note = "Patterns are shared across tracks",
    });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<button class=\"rp-btn rp-btn--outline\" disabled title=\"Select cues first\">Overwrite</button>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-act=\"ce-pat-ow:p2\">Overwrite (2)</button>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<form data-act=\"ce-pat-rename:p1\" class=lib-toolbar>") != null);
}
