//! Settings SUB-VIEW renderers — byte-identical to internal/webui/render_settings_sub_html.go
//! (gfCardHTML / gfVarHTML / gfModelHTML / bridgeCardHTML / bridgeGateHTML / updFlowHTMLOf).
//!
//! These four card bodies are owned by other webui files and used to cross the ABI as trusted raw
//! HTML (the wave-3 seams named in settings.zig's header); they now travel as structured state:
//!   settings_gridfix.go       gridfixCardState()  → card "gridfix" body       (block kind gridfix)
//!   settings_gridfix_model.go gridfixModelState() → card "gridfixmodel" body  (block kind gridfixmodel)
//!   bridge_actions.go         bridgeCardState()   → card "accountbridge" body (block kind bridge)
//!   update_actions.go         updateFlowState()   → #inst-update region       (block kind updregion)
//! Each also has its own export (rz_ui_render_settings_{gridfix,gridfixmodel,bridge,updflow}) so a
//! body can be rendered on its own — the update flow IS patched alone (#inst-update, patchUpd).
//!
//! Go resolves everything impure: config + probe caches + gate/relay snapshots, i18n, every number
//! (progressPct/strconv), smart-select registration + filtering, tooltip markup, and the
//! data-labels (Unicode strings.ToLower stays in Go). Ids spliced unescaped (inst-gridfix-<key>,
//! gfm-live, the region id) are trusted literals, exactly as Go does — ctl addressing needs it.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

/// GfBtn is one engine-variant action (webui gfBtn). gate non-empty = btnGated (disabled +
/// title naming what's missing) instead of a live button.
pub const GfBtn = struct {
    label: []const u8 = "",
    variant: []const u8 = "",
    act: []const u8 = "",
    gate: []const u8 = "",
};

/// GfVar is one gridfix engine variant, cpu|cuda (webui gfVarSt). `line` may already carry
/// entities — the Go original esc()'d the version string INTO the hint text, which hint() then
/// escapes again; replicated here (escape exactly once, same as Go).
pub const GfVar = struct {
    key: []const u8 = "",
    tone: []const u8 = "",
    line: []const u8 = "",
    btns: []const GfBtn = &.{},
    hasNote: bool = false,
    note: []const u8 = "",
};

/// GfCard is the beatgrid-fixer card body (webui gfCardSt).
pub const GfCard = struct {
    leadKind: []const u8 = "",
    leadTone: []const u8 = "",
    lead: []const u8 = "",
    vars: []const GfVar = &.{},
    recheck: c.Btn = .{},
    engine: c.Select = .{},
    python: c.Field = .{},
    browse: c.Btn = .{},
    minq: c.Field = .{},
    thresh: c.Field = .{},
    lock: c.Toggle = .{},
    hasCal: bool = false,
    cal: []const u8 = "",
    calNote: []const u8 = "",
    note: []const u8 = "",
};

/// GfModel is the model/training card body (webui gfModelSt).
pub const GfModel = struct {
    sel: c.Select = .{},
    dataset: []const u8 = "",
    running: bool = false,
    barPct: []const u8 = "",
    barCap: []const u8 = "",
    cancel: c.Btn = .{},
    hasVerdict: bool = false,
    verdictTone: []const u8 = "",
    verdict: []const u8 = "",
    err: []const u8 = "",
    canTrain: bool = false,
    train: c.Btn = .{},
    few: bool = false,
    fewHint: []const u8 = "",
    note: []const u8 = "",
};

/// BridgeSess is one trusted-session row (webui bridgeSessSt).
pub const BridgeSess = struct {
    title: []const u8 = "",
    sub: []const u8 = "",
    revoke: c.Btn = .{},
};

/// BridgeGate is the access-gate section body (webui bridgeGateSt). kind ∈ enrol|enrolled|none.
pub const BridgeGate = struct {
    kind: []const u8 = "",
    help: []const u8 = "",
    secret: []const u8 = "",
    uri: []const u8 = "",
    codeLabel: []const u8 = "",
    codeDL: []const u8 = "",
    confirm: []const u8 = "",
    cancel: c.Btn = .{},
    burn: []const u8 = "",
    rows: []const c.Status = &.{},
    note: []const u8 = "",
    btn: c.Btn = .{},
    sessionsTitle: []const u8 = "",
    empty: []const u8 = "",
    sessions: []const BridgeSess = &.{},
    revokeAll: c.Btn = .{},
};

/// Bridge is the account-bridge card body (webui bridgeSt).
pub const Bridge = struct {
    st: c.Status = .{},
    studio: c.Toggle = .{},
    tip: []const u8 = "",
    hasGate: bool = false,
    gateTitle: []const u8 = "",
    gate: BridgeGate = .{},
};

/// UpdFlow is the #inst-update region (webui updFlowSt). Empty kind renders NOTHING ⇒ the export
/// returns NULL ⇒ the Go fallback renders the same "".
pub const UpdFlow = struct {
    kind: []const u8 = "",
    tone: []const u8 = "",
    text: []const u8 = "",
    hasNotes: bool = false,
    notes: []const u8 = "",
    err: []const u8 = "",
    pct: []const u8 = "",
    cap: []const u8 = "",
    hasBtn: bool = false,
    btn: c.Btn = .{},
};

/// note is the muted settings help line (Go setNote; same markup as settings.zig's private twin).
fn note(h: *Html, text: []const u8) !void {
    try h.raw("<div class=set-note>");
    try h.esc(text);
    try h.raw("</div>");
}

/// gridfixVar mirrors Go gfVarHTML: status line, actions, the variant's progress target.
pub fn gridfixVar(h: *Html, v: GfVar) !void {
    try c.hint(h, v.tone, v.line);
    if (v.btns.len != 0) {
        try c.btnRowOpen(h);
        for (v.btns) |b| {
            if (b.gate.len != 0) {
                try c.btnGated(h, b.label, b.gate);
            } else {
                try c.btn(h, b.label, b.variant, b.act, "");
            }
        }
        try c.btnRowClose(h);
    }
    if (v.hasNote) try note(h, v.note);
    try h.raw("<div id=inst-gridfix-");
    try h.raw(v.key);
    try h.raw("></div>");
}

/// renderGridfix mirrors Go gfCardHTML.
pub fn renderGridfix(h: *Html, s: GfCard) !void {
    if (std.mem.eql(u8, s.leadKind, "hint")) {
        try c.hint(h, s.leadTone, s.lead);
    } else if (std.mem.eql(u8, s.leadKind, "note")) {
        try note(h, s.lead);
    }
    for (s.vars) |v| try gridfixVar(h, v);
    try c.btnRowOpen(h);
    try c.btnOf(h, s.recheck);
    try c.btnRowClose(h);
    try c.selectBox(h, s.engine);
    try h.raw("<div class=set-pathrow>");
    try c.fieldOf(h, s.python);
    try c.btnOf(h, s.browse);
    try h.raw("</div>");
    try c.fieldOf(h, s.minq);
    try c.fieldOf(h, s.thresh);
    try c.toggleOf(h, s.lock);
    if (s.hasCal) try note(h, s.cal);
    try note(h, s.calNote);
    try note(h, s.note);
}

/// renderGridfixModel mirrors Go gfModelHTML.
pub fn renderGridfixModel(h: *Html, s: GfModel) !void {
    try c.selectBox(h, s.sel);
    try note(h, s.dataset);
    if (s.running) {
        try h.raw("<div id=gfm-live>");
        try c.progressBar(h, s.barPct, s.barCap);
        try h.raw("</div>");
        try c.btnRowOpen(h);
        try c.btnOf(h, s.cancel);
        try c.btnRowClose(h);
    } else {
        if (s.hasVerdict) try c.hint(h, s.verdictTone, s.verdict);
        if (s.err.len != 0) try c.hint(h, "bad", s.err);
        if (s.canTrain) {
            try c.btnRowOpen(h);
            try c.btnOf(h, s.train);
            try c.btnRowClose(h);
        }
        if (s.few) try note(h, s.fewHint);
    }
    try note(h, s.note);
}

/// renderBridge mirrors Go bridgeCardHTML: relay state, the Local Studio sub-toggle, the gate.
pub fn renderBridge(h: *Html, s: Bridge) !void {
    try c.statusOf(h, s.st);
    try c.toggleRowTip(h, s.studio.label, s.studio.dl, s.studio.act, s.studio.on, s.tip);
    if (!s.hasGate) return;
    try c.sectionOpen(h, s.gateTitle);
    try renderBridgeGate(h, s.gate);
    try c.sectionClose(h);
}

/// renderBridgeGate mirrors Go bridgeGateHTML: enrolment + the trusted sessions.
pub fn renderBridgeGate(h: *Html, g: BridgeGate) !void {
    if (std.mem.eql(u8, g.kind, "enrol")) {
        try note(h, g.help);
        try h.raw("<div class=\"bridge-secret mono\">");
        try h.esc(g.secret);
        try h.raw("</div><div class=\"bridge-uri mono\">");
        try h.esc(g.uri);
        // hand-rolled form: field() emits no name attribute, so parseForm would see nothing
        try h.raw("</div><form data-act=bridge-confirm class=bridge-confirm><label class=field data-label=");
        try h.attrQ(g.codeDL);
        try h.raw("><span class=field-label>");
        try h.esc(g.codeLabel);
        try h.raw("</span><input class=field-input type=text name=code data-act=bridge-code data-label=");
        try h.attrQ(g.codeDL);
        try h.raw(" inputmode=numeric autocomplete=one-time-code maxlength=6 value=\"\"></label><div class=btn-row>");
        try h.raw("<button class=\"rp-btn rp-btn--primary\" type=submit>");
        try h.esc(g.confirm);
        try h.raw("</button>");
        try c.btnOf(h, g.cancel);
        try h.raw("</div></form>");
        try note(h, g.burn);
    } else if (std.mem.eql(u8, g.kind, "enrolled")) {
        for (g.rows) |r| try c.statusOf(h, r);
        try c.btnRowOpen(h);
        try c.btnOf(h, g.btn);
        try c.btnRowClose(h);
    } else {
        try note(h, g.note);
        try c.btnRowOpen(h);
        try c.btnOf(h, g.btn);
        try c.btnRowClose(h);
    }
    try h.raw("<div class=set-sub>");
    try h.esc(g.sessionsTitle);
    try h.raw("</div>");
    if (g.sessions.len == 0) return c.emptyState(h, g.empty);
    for (g.sessions) |s| {
        try c.listRowOpen(h, s.title, s.sub);
        try c.btnOf(h, s.revoke);
        try c.listRowClose(h);
    }
    try c.btnRowOpen(h);
    try c.btnOf(h, g.revokeAll);
    try c.btnRowClose(h);
}

/// renderUpdFlow mirrors Go updFlowHTMLOf: the updater verdict + its ONE action.
pub fn renderUpdFlow(h: *Html, s: UpdFlow) !void {
    if (std.mem.eql(u8, s.kind, "idle")) return c.hint(h, s.tone, s.text);
    if (std.mem.eql(u8, s.kind, "dl")) return c.progressBar(h, s.pct, s.cap);
    if (!std.mem.eql(u8, s.kind, "avail") and !std.mem.eql(u8, s.kind, "ready") and
        !std.mem.eql(u8, s.kind, "staged")) return;
    try c.hint(h, s.tone, s.text);
    if (s.hasNotes) try note(h, s.notes);
    if (s.err.len != 0) try c.hint(h, "bad", s.err);
    if (s.hasBtn) {
        try c.btnRowOpen(h);
        try c.btnOf(h, s.btn);
        try c.btnRowClose(h);
    }
}

test "gridfix variant: ready + gated install + remove" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try gridfixVar(&h, .{ .key = "cpu", .tone = "ok", .line = "CPU engine ready (beat-this 0.1, torch 2.4)" });
    try std.testing.expectEqualStrings("<span class=\"hint hint--ok\">CPU engine ready (beat-this 0.1, torch 2.4)</span>" ++
        "<div id=inst-gridfix-cpu></div>", h.b.items);

    h.b.clearRetainingCapacity();
    const btns = [_]GfBtn{
        .{ .label = "Install CUDA", .gate = "No NVIDIA GPU detected" },
        .{ .label = "Remove CUDA", .act = "gridfix-uninstall:cuda" },
    };
    try gridfixVar(&h, .{ .key = "cuda", .line = "CUDA engine not installed", .btns = &btns, .hasNote = true, .note = "n&ote" });
    try std.testing.expectEqualStrings("<span class=\"hint hint--info\">CUDA engine not installed</span>" ++
        "<div class=btn-row><button class=\"rp-btn rp-btn--outline\" disabled title=\"No NVIDIA GPU detected\">Install CUDA</button>" ++
        "<button class=\"rp-btn rp-btn--outline\" data-act=\"gridfix-uninstall:cuda\">Remove CUDA</button></div>" ++
        "<div class=set-note>n&amp;ote</div><div id=inst-gridfix-cuda></div>", h.b.items);
}

test "gridfix model: running bar + cancel" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderGridfixModel(&h, .{
        .sel = .{ .id = "gfmodel", .label = "Active model", .curLabel = "Built-in" },
        .dataset = "3 verified grids",
        .running = true,
        .barPct = "0.0%",
        .barCap = "Epoch 2 — loss 0.1234 · F 0.900",
        .cancel = .{ .label = "Stop", .variant = "outline", .act = "gfm-cancel" },
        .note = "note",
    });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div id=gfm-live><div class=pbar>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<span class=pbar-cap>Epoch 2 — loss 0.1234 · F 0.900</span>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-act=\"gfm-cancel\"") != null);
}

test "bridge gate: no authenticator, no sessions" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderBridgeGate(&h, .{
        .kind = "none",
        .note = "No authenticator enrolled",
        .btn = .{ .label = "Enrol", .variant = "primary", .act = "bridge-enrol" },
        .sessionsTitle = "Trusted sessions",
        .empty = "No trusted sessions",
    });
    try std.testing.expectEqualStrings("<div class=set-note>No authenticator enrolled</div>" ++
        "<div class=btn-row><button class=\"rp-btn rp-btn--primary\" data-act=\"bridge-enrol\">Enrol</button></div>" ++
        "<div class=set-sub>Trusted sessions</div>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">No trusted sessions</div></div>", h.b.items);
}

test "bridge gate: pending enrolment form + session rows" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const sess = [_]BridgeSess{.{
        .title = "Chrome on Studio",
        .sub = "bridge · expires 2026-08-01 12:00",
        .revoke = .{ .label = "Revoke", .variant = "destructive", .act = "bridge-revoke:peer&1" },
    }};
    try renderBridgeGate(&h, .{
        .kind = "enrol",
        .help = "Scan or type the secret",
        .secret = "ABC&DEF",
        .uri = "otpauth://totp/x?secret=ABC&issuer=rave",
        .codeLabel = "Code",
        .codeDL = "code",
        .confirm = "Confirm",
        .cancel = .{ .label = "Cancel", .variant = "ghost", .act = "bridge-enrol-cancel" },
        .burn = "Shown once",
        .sessionsTitle = "Sessions",
        .sessions = &sess,
        .revokeAll = .{ .label = "Revoke all", .variant = "outline", .act = "bridge-revoke-all" },
    });
    try std.testing.expectEqualStrings("<div class=set-note>Scan or type the secret</div>" ++
        "<div class=\"bridge-secret mono\">ABC&amp;DEF</div>" ++
        "<div class=\"bridge-uri mono\">otpauth://totp/x?secret=ABC&amp;issuer=rave</div>" ++
        "<form data-act=bridge-confirm class=bridge-confirm><label class=field data-label=\"code\">" ++
        "<span class=field-label>Code</span>" ++
        "<input class=field-input type=text name=code data-act=bridge-code data-label=\"code\"" ++
        " inputmode=numeric autocomplete=one-time-code maxlength=6 value=\"\"></label><div class=btn-row>" ++
        "<button class=\"rp-btn rp-btn--primary\" type=submit>Confirm</button>" ++
        "<button class=\"rp-btn rp-btn--ghost\" data-act=\"bridge-enrol-cancel\">Cancel</button></div></form>" ++
        "<div class=set-note>Shown once</div><div class=set-sub>Sessions</div>" ++
        "<div class=set-listrow><div class=set-listmain>Chrome on Studio" ++
        "<div class=set-listsub>bridge · expires 2026-08-01 12:00</div></div>" ++
        "<div class=irow-actions><button class=\"rp-btn rp-btn--destructive\" data-act=\"bridge-revoke:peer&amp;1\">Revoke</button>" ++
        "</div></div><div class=btn-row>" ++
        "<button class=\"rp-btn rp-btn--outline\" data-act=\"bridge-revoke-all\">Revoke all</button></div>", h.b.items);
}

test "update flow: idle verdict, download, empty" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderUpdFlow(&h, .{ .kind = "idle", .tone = "ok", .text = "Up to date" });
    try std.testing.expectEqualStrings("<span class=\"hint hint--ok\">Up to date</span>", h.b.items);

    h.b.clearRetainingCapacity();
    try renderUpdFlow(&h, .{
        .kind = "avail",
        .tone = "warn",
        .text = "1.2.3 available",
        .hasNotes = true,
        .notes = "fixes & things",
        .err = "download failed: net",
        .hasBtn = true,
        .btn = .{ .label = "Download 1.2.3", .variant = "primary", .act = "upd-download" },
    });
    try std.testing.expectEqualStrings("<span class=\"hint hint--warn\">1.2.3 available</span>" ++
        "<div class=set-note>fixes &amp; things</div>" ++
        "<span class=\"hint hint--bad\">download failed: net</span>" ++
        "<div class=btn-row><button class=\"rp-btn rp-btn--primary\" data-act=\"upd-download\">Download 1.2.3</button></div>", h.b.items);

    h.b.clearRetainingCapacity();
    try renderUpdFlow(&h, .{ .kind = "dl", .pct = "42.5%", .cap = "Downloading…" });
    try std.testing.expectEqualStrings("<div class=pbar><div class=pbar-fill style=\"width:42.5%\"></div>" ++
        "<span class=pbar-cap>Downloading…</span></div>", h.b.items);

    h.b.clearRetainingCapacity();
    try renderUpdFlow(&h, .{});
    try std.testing.expectEqualStrings("", h.b.items);
}
