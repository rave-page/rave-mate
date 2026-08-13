//! Unified media player/editor renderers — byte-exact port of the STRUCTURAL CHROME in
//! internal/webui/render_player.go (mpFullHTMLOf / mpInnerHTMLOf / mpVidHTMLOf /
//! mpWaveHTMLOf / mpChipHTMLOf / mpHovHTMLOf / mpTpHTMLOf / mpEditHTMLOf / mpROHTMLOf /
//! mpAlignHTMLOf / mpExportHTMLOf / mpSumHTMLOf).
//!
//! NOT ported, rides through the state as trusted RAW markup (Go owns every float):
//!   - `wave.svg` = player.go mpWaveSVG: the waveform/beatgrid/cue/trim geometry and the
//!     `mp-<host>-ph` / `mp-<host>-ph-veil` ids the client rAF runtime (shell.go __rt)
//!     rewrites 30x/s. Same rule as keywheelSVG / the campath viewer.
//!   - `export.medias[].loudExtra` = Go mpLoudExtraHTML (the standalone gain-plan line +
//!     pre-listen toggle shown when the PRESET normalizes without an override). The shared
//!     loudness block itself is STRUCTURED since phase B-1a (components.zig loudnessFields);
//!     only its own extra + tip stay raw.
//!   - tooltips cross as STRUCTURED state since phase B1b (`tipWaveSt` / `tp.tipVideoSt` /
//!     `editBox.tipTrimSt` / `alignRow.tipAlignSt`, components.zig renderTip); the raw `tip*`
//!     strings stay only as the dual-field bridge (Go tipOr) and are empty on this path.
//!   - the <video> element's inline JS handlers (they carry a %.3f volume and drive
//!     shell.go __mse) — plain state values, attrQ'd identically on both sides.
//!
//! Every number is pre-formatted Go-side (clocks, "%.1f%%" percentages, LUFS/kbps/size
//! readouts, the "%.2f" marker offsets that double as smart-select values).
//! ID QUIRK replicated: `mp-<host>-root` ESCAPES the host; every other id splices it RAW.
//! Golden gate: internal/webui/zigui_golden_player_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

// ── leaf state ──────────────────────────────────────────────────────────────────

/// KVRow is one wchip-card detail row (<b>k</b>v).
pub const KVRow = struct {
    k: []const u8 = "",
    v: []const u8 = "",
};

/// Link is one wc-link in the loudness chip card. url is spliced UNESCAPED (a Go source
/// literal), the label is escaped.
pub const Link = struct {
    url: []const u8 = "",
    label: []const u8 = "",
};

/// Chip is one waveform overlay chip. kind: "" none | "dim" (loading pill whose text Go
/// splices UNESCAPED) | "chip" (pinnable label + hover card).
pub const Chip = struct {
    kind: []const u8 = "",
    loud: bool = false,
    dim: []const u8 = "",
    text: []const u8 = "",
    rows: []const KVRow = &.{},
    note: []const u8 = "",
    links: []const Link = &.{},
};

// ── fragment state ──────────────────────────────────────────────────────────────

/// Vid is the #mp-<host>-vid inner. kind: "" | "err" | "nostream" | "video".
pub const Vid = struct {
    host: []const u8 = "",
    kind: []const u8 = "",
    errText: []const u8 = "",
    openExt: c.Btn = .{},
    noStream: []const u8 = "",
    url: []const u8 = "",
    mse: []const u8 = "", // "" = plain src
    muted: bool = false,
    ev: []const u8 = "",
    onmeta: []const u8 = "",
    onerr: []const u8 = "",
    dataIn: []const u8 = "", // trim IN local secs ("" = omit)
    dataOut: []const u8 = "", // trim OUT local secs ("" = none)
    stream: []const u8 = "", // live /ms/ feed URL (realtime fx preview)
    streamMi: []const u8 = "",
    streamAu: bool = false,
    grip: []const u8 = "", // "" = none; else the bottom-edge resize drag act
    boxH: []const u8 = "", // "" = CSS default; else the drag-set box height in px
};

/// Wave is the #mp-<host>-wave inner.
pub const Wave = struct {
    svg: []const u8 = "", // RAW: player.go mpWaveSVG
    hasChips: bool = false,
    enc: Chip = .{},
    loud: Chip = .{},
    seekTab: []const u8 = "",
    captions: []const []const u8 = &.{},
};

/// Hov is the #mp-<host>-hov readout. raw=true = the two Go branches that splice the
/// i18n string UNESCAPED (measuring loudness / hover hint).
pub const Hov = struct {
    text: []const u8 = "",
    raw: bool = false,
};

/// Tp is the #mp-<host>-tp inner.
pub const Tp = struct {
    host: []const u8 = "",
    show: bool = false,
    hasTabs: bool = false,
    tabPrefix: []const u8 = "",
    tabActive: []const u8 = "",
    tabs: []const c.Tab = &.{}, // media-switch subTabs items
    play: c.Btn = .{},
    stop: c.Btn = .{},
    hasPreview: bool = false,
    preview: c.Btn = .{},
    hasTracks: bool = false,
    prev: c.Btn = .{},
    trackSel: c.Select = .{},
    next: c.Btn = .{},
    demoted: bool = false,
    moreSel: c.Select = .{},
    editBtn: c.Btn = .{},
    isVideo: bool = false,
    openExt: c.Btn = .{},
    tipVideo: []const u8 = "", // legacy RAW tooltip markup (bridge)
    tipVideoSt: ?c.Tip = null, // structured tooltip — wins over tipVideo
    timeTx: []const u8 = "",
    seek: c.Slider = .{},
    vol: c.Slider = .{},
};

/// RO is the #mp-<host>-ro trim readout.
pub const RO = struct {
    value: []const u8 = "",
    durLbl: []const u8 = "",
    dur: []const u8 = "",
    inLbl: []const u8 = "",
    in: []const u8 = "",
    outLbl: []const u8 = "",
    out: []const u8 = "",
    keepsLbl: []const u8 = "",
    keeps: []const u8 = "",
};

/// Align is the dual-media alignment row. Exactly-one-of rides as explicit flags.
pub const Align = struct {
    bar: bool = false,
    barPct: []const u8 = "",
    barCap: []const u8 = "",
    err: bool = false,
    errText: []const u8 = "",
    line: []const u8 = "", // "" = no readout span
    lineVal: []const u8 = "",
    alignBtn: c.Btn = .{},
    nudges: []const c.Btn = &.{},
    offField: c.Field = .{},
    tipAlign: []const u8 = "", // legacy RAW tooltip markup (bridge)
    tipAlignSt: ?c.Tip = null, // structured tooltip — wins over tipAlign
    warns: []const []const u8 = &.{},
};

/// Sum is the preset-summary chip (also the edit-preset button).
pub const Sum = struct {
    tx: []const u8 = "",
    act: []const u8 = "",
    title: []const u8 = "",
};

/// ExMedia is one media's export block. loud is the shared loudness block as state
/// (components.zig loudnessFields); loudExtra stays RAW (Go mpLoudExtraHTML).
pub const ExMedia = struct {
    presetSel: c.Select = .{},
    summary: Sum = .{},
    outField: c.Field = .{},
    pickBtn: c.Btn = .{},
    loud: c.Loud = .{},
    loudExtra: []const u8 = "",
};

/// Export is the #mp-<host>-export inner.
pub const Export = struct {
    medias: []const ExMedia = &.{},
    exporting: bool = false,
    runPct: []const u8 = "",
    runLabel: []const u8 = "",
    cancel: c.Btn = .{},
    dual: bool = false,
    scopeSel: c.Select = .{},
    exportBtn: c.Btn = .{},
    est: []const u8 = "",
    loudTx: []const u8 = "",
    msg: []const u8 = "",
};

/// Edit is the #mp-<host>-edit inner (empty while edit mode is OFF).
pub const Edit = struct {
    host: []const u8 = "",
    show: bool = false,
    inField: c.Field = .{},
    outField: c.Field = .{},
    setIn: c.Btn = .{},
    setOut: c.Btn = .{},
    autoSel: c.Select = .{},
    tipTrim: []const u8 = "", // legacy RAW tooltip markup (bridge)
    tipTrimSt: ?c.Tip = null, // structured tooltip — wins over tipTrim
    ro: RO = .{},
    dual: bool = false,
    alignRow: Align = .{},
    exportPane: Export = .{},
};

/// Inner is the #mp-<host>-root inner (the whole component).
pub const Inner = struct {
    host: []const u8 = "",
    title: []const u8 = "", // "" = no mp-title row
    vid: Vid = .{},
    dual: bool = false,
    edit: bool = false,
    wave: Wave = .{},
    laneIn: []const u8 = "",
    laneMid: []const u8 = "",
    laneOut: []const u8 = "",
    laneFull: []const u8 = "",
    zin: c.Btn = .{},
    zout: c.Btn = .{},
    fit: c.Btn = .{},
    zinfo: []const u8 = "", // "" = fit view
    hov: Hov = .{},
    tipWave: []const u8 = "", // legacy RAW tooltip markup (bridge)
    tipWaveSt: ?c.Tip = null, // structured tooltip — wins over tipWave
    tp: Tp = .{},
    editBox: Edit = .{},
};

/// State is mpHTML's state: the .mplayer root wrapper + the inner component.
pub const State = struct {
    host: []const u8 = "",
    inner: Inner = .{},
};

// ── renderers ───────────────────────────────────────────────────────────────────

/// attrComposite writes `"` ++ esc(prefix) ++ esc(host) ++ `"` — Go attrQ over a
/// concatenation. esc is per-byte, so splitting it is byte-exact and needs no scratch.
fn attrComposite(h: *Html, prefix: []const u8, host: []const u8) !void {
    try h.raw("\"");
    try h.esc(prefix);
    try h.esc(host);
    try h.raw("\"");
}

/// render mirrors Go mpFullHTMLOf. NOTE the quirk: only this id escapes the host.
pub fn render(h: *Html, s: State) !void {
    try h.raw("<div id=mp-");
    try h.esc(s.host);
    try h.raw("-root class=mplayer>");
    try renderInner(h, s.inner);
    try h.raw("</div>");
}

/// renderInner mirrors Go mpInnerHTMLOf.
pub fn renderInner(h: *Html, s: Inner) !void {
    // what's loaded (publish: the set / loose capture name)
    if (s.title.len != 0) {
        try h.raw("<div class=mp-title data-label=\"player media\" data-value=");
        try h.attrQ(s.title);
        try h.raw(">");
        try h.esc(s.title);
        try h.raw("</div>");
    }

    // embedded video (own patch target: the async fMP4-index resolve swaps src → MSE)
    try h.raw("<div id=mp-");
    try h.raw(s.host);
    try h.raw("-vid>");
    try renderVid(h, s.vid);
    try h.raw("</div>");

    // wavebox: patched SVG inside, interaction lanes on top (lanes stay OUTSIDE the
    // patched region so pointer capture survives repaints)
    try h.raw(if (s.dual) "<div class=\"mp-wavebox mp-wavebox--dual\" data-actwheel=" else "<div class=\"mp-wavebox\" data-actwheel=");
    try attrComposite(h, "mp-zoomw:", s.host);
    try h.raw("><div id=mp-");
    try h.raw(s.host);
    try h.raw("-wave class=mp-wave>");
    try renderWave(h, s.wave);
    try h.raw("</div>");
    if (s.edit) {
        try h.raw("<div class=\"mp-lane mp-lane--in\" data-actpos=");
        try attrComposite(h, "mp-hin:", s.host);
        try h.raw(" title=");
        try h.attrQ(s.laneIn);
        try h.raw("></div>");
        try h.raw("<div class=\"mp-lane mp-lane--mid\" data-actpos=");
        try attrComposite(h, "mp-surf:", s.host);
        try h.raw(" data-acthover=");
        try attrComposite(h, "mp-hov:", s.host);
        try h.raw(" title=");
        try h.attrQ(s.laneMid);
        try h.raw("></div>");
        try h.raw("<div class=\"mp-lane mp-lane--out\" data-actpos=");
        try attrComposite(h, "mp-hout:", s.host);
        try h.raw(" title=");
        try h.attrQ(s.laneOut);
        try h.raw("></div>");
    } else {
        try h.raw("<div class=\"mp-lane mp-lane--full\" data-actpos=");
        try attrComposite(h, "mp-surf:", s.host);
        try h.raw(" data-acthover=");
        try attrComposite(h, "mp-hov:", s.host);
        try h.raw(" title=");
        try h.attrQ(s.laneFull);
        try h.raw("></div>");
    }
    try h.raw("</div>");

    // zoom + hover readout row (compact; how-to lives in the tooltip)
    try h.raw("<div class=mp-zoom>");
    try c.btnOf(h, s.zin);
    try c.btnOf(h, s.zout);
    try c.btnOf(h, s.fit);
    if (s.zinfo.len != 0) {
        try h.raw("<span class=mp-zinfo>");
        try h.esc(s.zinfo);
        try h.raw("</span>");
    }
    try h.raw("<span id=mp-");
    try h.raw(s.host);
    try h.raw("-hov class=mp-hovline>");
    try renderHov(h, s.hov);
    try h.raw("</span>");
    try c.tipOr(h, s.tipWaveSt, s.tipWave);
    try h.raw("</div>");

    // transport
    try h.raw("<div id=mp-");
    try h.raw(s.host);
    try h.raw("-tp>");
    try renderTp(h, s.tp);
    try h.raw("</div>");

    // edit strip (empty container while OFF so the toggle patch has a target)
    try h.raw("<div id=mp-");
    try h.raw(s.host);
    try h.raw("-edit>");
    try renderEdit(h, s.editBox);
    try h.raw("</div>");
}

/// renderVid mirrors Go mpVidHTMLOf.
pub fn renderVid(h: *Html, s: Vid) !void {
    if (std.mem.eql(u8, s.kind, "err")) {
        try h.raw("<div class=mp-viderr>");
        try c.hint(h, "warn", s.errText);
        try c.btnRowOpen(h);
        try c.btnOf(h, s.openExt);
        try c.btnRowClose(h);
        try h.raw("</div>");
        return;
    }
    if (std.mem.eql(u8, s.kind, "nostream")) {
        try c.hint(h, "warn", s.noStream);
        return;
    }
    if (!std.mem.eql(u8, s.kind, "video")) return;

    try h.raw("<div class=mp-videobox");
    if (s.boxH.len != 0) {
        try h.raw(" style=\"height:");
        try h.esc(s.boxH);
        try h.raw("px;max-height:none\"");
    }
    try h.raw("><video id=");
    try attrComposite(h, "mp-vid-", s.host);
    try h.raw(" class=mp-video");
    if (s.stream.len != 0) {
        try h.raw(" data-msestream=");
        try h.attrQ(s.stream);
        try h.raw(" data-msestream-mime=");
        try h.attrQ(s.streamMi);
        if (s.streamAu) try h.raw(" autoplay");
    } else if (s.mse.len != 0) {
        try h.raw(" data-mse=");
        try h.attrQ(s.mse);
        try h.raw(" data-mse-src=");
        try h.attrQ(s.url);
    } else {
        try h.raw(" src=");
        try h.attrQ(s.url);
    }
    try h.raw(" preload=none playsinline");
    if (s.muted) try h.raw(" muted");
    if (s.dataIn.len != 0) {
        try h.raw(" data-in=");
        try h.attrQ(s.dataIn);
    }
    if (s.dataOut.len != 0) {
        try h.raw(" data-out=");
        try h.attrQ(s.dataOut);
    }
    try h.raw(" ontimeupdate=");
    try h.attrQ(s.ev);
    try h.raw(" onplay=");
    try h.attrQ(s.ev);
    try h.raw(" onpause=");
    try h.attrQ(s.ev);
    try h.raw(" onseeked=");
    try h.attrQ(s.ev);
    try h.raw(" onended=");
    try h.attrQ(s.ev);
    try h.raw(" onloadedmetadata=");
    try h.attrQ(s.onmeta);
    try h.raw(" onerror=");
    try h.attrQ(s.onerr);
    try h.raw("></video>");
    if (s.grip.len != 0) {
        try h.raw("<div class=mp-vgrip data-actsize=");
        try h.attrQ(s.grip);
        try h.raw("></div>");
    }
    try h.raw("</div>");
}

/// renderWave mirrors Go mpWaveHTMLOf.
pub fn renderWave(h: *Html, s: Wave) !void {
    try h.raw("<div class=mp-wrap>");
    try h.raw(s.svg);
    if (s.hasChips) {
        try h.raw("<div class=wchips>");
        try renderChip(h, s.enc);
        try renderChip(h, s.loud);
        if (s.seekTab.len != 0) {
            try h.raw("<span class=\"wchip dim\">");
            try h.esc(s.seekTab);
            try h.raw("</span>");
        }
        try h.raw("</div>");
    }
    try h.raw("</div>");
    for (s.captions) |cap| {
        try h.raw("<p class=page-sub>");
        try h.esc(cap);
        try h.raw("</p>");
    }
}

/// renderChip mirrors Go mpChipHTMLOf. The "dim" pill's text is spliced UNESCAPED.
pub fn renderChip(h: *Html, s: Chip) !void {
    if (std.mem.eql(u8, s.kind, "dim")) {
        try h.raw("<span class=\"wchip dim\">");
        try h.raw(s.dim);
        try h.raw("</span>");
        return;
    }
    if (!std.mem.eql(u8, s.kind, "chip")) return;

    // click/tap pins the card (checkbox pin, same pattern as the tooltip primitive)
    try h.raw(if (s.loud)
        "<label class=\"wchip loud\" data-label=\"lufs-chip\">"
    else
        "<label class=wchip data-label=\"enc-chip\">");
    try h.raw("<input type=checkbox class=wchip-x tabindex=-1>");
    try h.esc(s.text);
    try h.raw("<span class=wchip-card>");
    for (s.rows) |r| {
        try h.raw("<span class=wc-row><b>");
        try h.esc(r.k);
        try h.raw("</b>");
        try h.esc(r.v);
        try h.raw("</span>");
    }
    try h.raw("<span class=wc-note>");
    try h.esc(s.note);
    try h.raw("</span>");
    for (s.links) |l| {
        try h.raw("<a class=wc-link data-act=open-url data-val=\"");
        try h.raw(l.url);
        try h.raw("\">");
        try h.esc(l.label);
        try h.raw("</a>");
    }
    try h.raw("</span></label>");
}

/// renderHov mirrors Go mpHovHTMLOf.
pub fn renderHov(h: *Html, s: Hov) !void {
    if (s.raw) {
        try h.raw(s.text);
        return;
    }
    try h.esc(s.text);
}

/// renderTp mirrors Go mpTpHTMLOf.
pub fn renderTp(h: *Html, s: Tp) !void {
    if (!s.show) return;
    try h.raw("<div class=mp-tp>");
    if (s.hasTabs) {
        // subTabs items are [val,label] pairs; the act = tabPrefix ++ val (Go composes
        // "mp-media:<host>\x1f" Go-side because it carries a control byte).
        var buf: [16]c.Tab = undefined;
        var n: usize = 0;
        for (s.tabs) |t| {
            if (n == buf.len) break;
            buf[n] = .{ .val = t.val, .label = t.label };
            n += 1;
        }
        try c.subTabs(h, s.tabPrefix, s.tabActive, buf[0..n]);
    }
    try c.btnOf(h, s.play);
    try c.btnOf(h, s.stop);
    if (s.hasPreview) try c.btnOf(h, s.preview);
    if (s.hasTracks) {
        try c.btnOf(h, s.prev);
        try h.raw("<span class=mp-trksel>");
        try c.selectBox(h, s.trackSel);
        try h.raw("</span>");
        try c.btnOf(h, s.next);
    }
    if (s.demoted) {
        try h.raw("<span class=mp-moresel>");
        try c.selectBox(h, s.moreSel);
        try h.raw("</span>");
    } else {
        try c.btnOf(h, s.editBtn);
    }
    if (s.isVideo) {
        try c.btnOf(h, s.openExt);
        try c.tipOr(h, s.tipVideoSt, s.tipVideo);
    }
    try h.raw("<span class=\"mp-time\" id=mp-");
    try h.raw(s.host);
    try h.raw("-time data-label=\"player time\" data-value=");
    try h.attrQ(s.timeTx);
    try h.raw(">");
    try h.esc(s.timeTx);
    try h.raw("</span></div>");
    try c.slider(h, s.seek);
    try h.raw("<div class=mp-volrow>");
    try c.slider(h, s.vol);
    try h.raw("</div>");
}

/// renderEdit mirrors Go mpEditHTMLOf.
pub fn renderEdit(h: *Html, s: Edit) !void {
    if (!s.show) return;
    try h.raw("<div class=mp-editbox>");

    // row 1: trim range - fields + set-at-playhead + auto menu + live readout
    try h.raw("<div class=mp-erow><span class=mp-tfield>");
    try c.fieldOf(h, s.inField);
    try h.raw("</span><span class=mp-tfield>");
    try c.fieldOf(h, s.outField);
    try h.raw("</span>");
    try c.btnOf(h, s.setIn);
    try c.btnOf(h, s.setOut);
    try h.raw("<span class=mp-autosel>");
    try c.selectBox(h, s.autoSel);
    try h.raw("</span>");
    try c.tipOr(h, s.tipTrimSt, s.tipTrim);
    try h.raw("</div>");

    try h.raw("<div id=mp-");
    try h.raw(s.host);
    try h.raw("-ro>");
    try renderRO(h, s.ro);
    try h.raw("</div>");

    if (s.dual) try renderAlign(h, s.alignRow);

    try h.raw("<div id=mp-");
    try h.raw(s.host);
    try h.raw("-export>");
    try renderExport(h, s.exportPane);
    try h.raw("</div>");
    try h.raw("</div>");
}

/// renderRO mirrors Go mpROHTMLOf.
pub fn renderRO(h: *Html, s: RO) !void {
    try h.raw("<div class=mp-rol data-label=\"trim readout\" data-value=\"");
    try h.esc(s.value);
    try h.raw("\">");
    const pairs = [_][2][]const u8{
        .{ s.durLbl, s.dur }, .{ s.inLbl, s.in }, .{ s.outLbl, s.out }, .{ s.keepsLbl, s.keeps },
    };
    for (pairs) |p| {
        try h.raw("<span>");
        try h.esc(p[0]);
        try h.raw(" <b>");
        try h.esc(p[1]);
        try h.raw("</b></span>");
    }
    try h.raw("</div>");
}

/// renderAlign mirrors Go mpAlignHTMLOf.
pub fn renderAlign(h: *Html, s: Align) !void {
    try h.raw("<div class=mp-align>");
    if (s.bar) {
        try c.progressBar(h, s.barPct, s.barCap);
    } else if (s.err) {
        try c.hint(h, "bad", s.errText);
    }
    if (s.line.len != 0) {
        try h.raw("<span class=mp-align-line data-label=\"align offset\" data-value=");
        try h.attrQ(s.lineVal);
        try h.raw(">");
        try h.esc(s.line);
        try h.raw("</span>");
    }
    try c.btnOf(h, s.alignBtn);
    for (s.nudges) |n| try c.btnOf(h, n);
    try h.raw("<span class=mp-aoff>");
    try c.fieldOf(h, s.offField);
    try h.raw("</span>");
    try c.tipOr(h, s.tipAlignSt, s.tipAlign);
    for (s.warns) |w| try c.hint(h, "warn", w);
    try h.raw("</div>");
}

/// renderExport mirrors Go mpExportHTMLOf.
pub fn renderExport(h: *Html, s: Export) !void {
    try h.raw("<div class=mp-export>");
    for (s.medias) |m| {
        try h.raw("<div class=mp-exmedia>");
        // one dense row: preset · summary · output path · picker (wraps when narrow)
        try h.raw("<div class=\"mp-erow mp-erow--preset\"><span class=mp-presel>");
        try c.selectBox(h, m.presetSel);
        try h.raw("</span>");
        try renderSum(h, m.summary);
        try h.raw("<span class=mp-outwrap><span class=mp-outfield>");
        try c.fieldOf(h, m.outField);
        try h.raw("</span>");
        try c.btnOf(h, m.pickBtn);
        try h.raw("</span></div>");
        try c.loudnessFields(h, m.loud); // the shared block (its extraHTML rides inside)
        try h.raw(m.loudExtra);
        try h.raw("</div>");
    }
    if (s.exporting) {
        try h.raw("<div class=mp-exrun>");
        try c.progressBar(h, s.runPct, s.runLabel);
        try c.btnOf(h, s.cancel);
        try h.raw("</div>");
    } else {
        try h.raw("<div class=\"mp-erow mp-erow--go\">");
        if (s.dual) {
            try h.raw("<span class=mp-scopesel>");
            try c.selectBox(h, s.scopeSel);
            try h.raw("</span>");
        }
        try c.btnOf(h, s.exportBtn);
        if (s.est.len != 0) {
            try h.raw("<span class=mp-est data-label=\"export estimate\" data-value=");
            try h.attrQ(s.est);
            try h.raw(">");
            try h.esc(s.est);
            try h.raw("</span>");
        }
        try h.raw("</div>");
    }
    if (s.loudTx.len != 0) {
        try h.raw("<div class=mp-exloud>");
        try h.esc(s.loudTx);
        try h.raw("</div>");
    }
    if (s.msg.len != 0) {
        try h.raw("<div class=mp-exmsg>");
        try h.esc(s.msg);
        try h.raw("</div>");
    }
    try h.raw("</div>");
}

/// renderSum mirrors Go mpSumHTMLOf: the chip IS the edit-preset button.
pub fn renderSum(h: *Html, s: Sum) !void {
    try h.raw("<button class=mp-sum data-label=\"preset summary\" data-value=");
    try h.attrQ(s.tx);
    try h.raw(" data-act=");
    try h.attrQ(s.act);
    try h.raw(" title=");
    try h.attrQ(s.title);
    try h.raw(">");
    try h.esc(s.tx);
    try h.raw(" ✎</button>");
}

// ── tests ───────────────────────────────────────────────────────────────────────

test "root wrapper escapes the host, inner ids splice it raw" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .host = "a&b", .inner = .{ .host = "a&b" } });
    try std.testing.expect(std.mem.startsWith(u8, h.b.items, "<div id=mp-a&amp;b-root class=mplayer>"));
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div id=mp-a&b-vid></div>") != null);
}

test "wavebox dual class + lane set flips with edit" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderInner(&h, .{ .host = "library", .dual = true, .edit = true, .laneIn = "in", .laneMid = "mid", .laneOut = "out" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=\"mp-wavebox mp-wavebox--dual\" data-actwheel=\"mp-zoomw:library\">") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "mp-lane--in\" data-actpos=\"mp-hin:library\" title=\"in\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "mp-lane--full") == null);
    h.b.clearRetainingCapacity();
    try renderInner(&h, .{ .host = "library", .laneFull = "full" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=\"mp-wavebox\" data-actwheel=\"mp-zoomw:library\">") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "mp-lane--full\" data-actpos=\"mp-surf:library\" data-acthover=\"mp-hov:library\" title=\"full\"") != null);
}

test "video element: MSE variant replaces plain src" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderVid(&h, .{ .host = "publish", .kind = "video", .url = "http://x/m?p=1&q=2", .ev = "e()", .onmeta = "m()", .onerr = "r()" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<video id=\"mp-vid-publish\" class=mp-video src=\"http://x/m?p=1&amp;q=2\" preload=none playsinline ontimeupdate=\"e()\"") != null);
    h.b.clearRetainingCapacity();
    try renderVid(&h, .{ .host = "publish", .kind = "video", .url = "u", .mse = "i", .muted = true });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, " data-mse=\"i\" data-mse-src=\"u\" preload=none playsinline muted ") != null);
    h.b.clearRetainingCapacity();
    try renderVid(&h, .{ .host = "e", .kind = "video", .url = "u", .dataIn = "1.000", .dataOut = "5.500", .ev = "e()" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, " data-in=\"1.000\" data-out=\"5.500\" ontimeupdate=") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, " onseeked=\"e()\"") != null);
    h.b.clearRetainingCapacity();
    try renderVid(&h, .{ .host = "e", .kind = "video", .url = "u", .stream = "http://s/ms/t", .streamMi = "video/mp4", .streamAu = true });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, " data-msestream=\"http://s/ms/t\" data-msestream-mime=\"video/mp4\" autoplay preload=none") != null);
    h.b.clearRetainingCapacity();
    try renderVid(&h, .{ .host = "editor", .kind = "video", .url = "u", .grip = "edv-vsize", .boxH = "620" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=mp-videobox style=\"height:620px;max-height:none\">") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "</video><div class=mp-vgrip data-actsize=\"edv-vsize\"></div></div>") != null);
    h.b.clearRetainingCapacity();
    try renderVid(&h, .{ .kind = "" });
    try std.testing.expectEqualStrings("", h.b.items);
}

test "dim chip text is raw, chip face is escaped" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderChip(&h, .{ .kind = "dim", .dim = "Probing<b>" });
    try std.testing.expectEqualStrings("<span class=\"wchip dim\">Probing<b></span>", h.b.items);
    h.b.clearRetainingCapacity();
    const rows = [_]KVRow{.{ .k = "Codec", .v = "FLAC&x" }};
    const links = [_]Link{.{ .url = "https://x/?a=1&b=2", .label = "EBU R128" }};
    try renderChip(&h, .{ .kind = "chip", .loud = true, .text = "-9.1 LUFS", .rows = &rows, .note = "n&", .links = &links });
    try std.testing.expectEqualStrings("<label class=\"wchip loud\" data-label=\"lufs-chip\">" ++
        "<input type=checkbox class=wchip-x tabindex=-1>-9.1 LUFS<span class=wchip-card>" ++
        "<span class=wc-row><b>Codec</b>FLAC&amp;x</span><span class=wc-note>n&amp;</span>" ++
        "<a class=wc-link data-act=open-url data-val=\"https://x/?a=1&b=2\">EBU R128</a>" ++
        "</span></label>", h.b.items);
}

test "hov line: raw vs escaped branches" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderHov(&h, .{ .text = "a&b" });
    try std.testing.expectEqualStrings("a&amp;b", h.b.items);
    h.b.clearRetainingCapacity();
    try renderHov(&h, .{ .text = "a&b", .raw = true });
    try std.testing.expectEqualStrings("a&b", h.b.items);
}

test "trim readout emits four label/value spans" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderRO(&h, .{ .value = "in=0:00 out=end keeps=1:00", .durLbl = "Duration", .dur = "1:00", .inLbl = "IN", .in = "0:00", .outLbl = "OUT", .out = "end", .keepsLbl = "Keeps", .keeps = "1:00" });
    try std.testing.expectEqualStrings("<div class=mp-rol data-label=\"trim readout\" data-value=\"in=0:00 out=end keeps=1:00\">" ++
        "<span>Duration <b>1:00</b></span><span>IN <b>0:00</b></span>" ++
        "<span>OUT <b>end</b></span><span>Keeps <b>1:00</b></span></div>", h.b.items);
}

test "empty fragments render nothing" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderTp(&h, .{});
    try renderEdit(&h, .{});
    try std.testing.expectEqualStrings("", h.b.items);
}
