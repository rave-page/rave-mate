//! MIDI tab renderer — byte-exact port of internal/webui/render_midictl.go
//! (midiCtlHTML + port/driver/rack/help cards + the #midi-active fragment). The
//! controllers, mappings and monitor cards live in midictl_ctls.zig /
//! midictl_uimap.zig / midimon.zig. Numeric CSS values (--v/--rot) arrive
//! pre-formatted from Go (trimNum) — Zig never re-derives a float here.
//! Golden gate: internal/webui/zigui_golden_midictl_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");
const ctls = @import("midictl_ctls.zig");
const uimap = @import("midictl_uimap.zig");
const midimon = @import("midimon.zig");

/// Active is the #midi-active status line (~1 Hz patch target).
pub const Active = struct {
    variant: []const u8 = "",
    label: []const u8 = "",
    labelDl: []const u8 = "",
    line: []const u8 = "",
};

pub const PortCard = struct {
    card: []const u8 = "",
    sub: []const u8 = "",
    port: c.Select = .{},
    active: Active = .{},
    panic: []const u8 = "",
};

pub const DrvInput = struct {
    variant: []const u8 = "",
    name: []const u8 = "",
    nameDl: []const u8 = "",
    line: []const u8 = "",
    fbHint: []const u8 = "",
    hasBtns: bool = false,
    traceLbl: []const u8 = "",
    traceAct: []const u8 = "",
    fbTest: bool = false,
    fbTestLbl: []const u8 = "",
    fbTestAct: []const u8 = "",
    fbTip: []const u8 = "", // pre-rendered tooltip HTML (raw)
    fbRes: bool = false,
    fbResVar: []const u8 = "",
    fbResLbl: []const u8 = "",
    fbResDl: []const u8 = "",
    fbResLine: []const u8 = "",
};

pub const DrvManaged = struct {
    hdr: []const u8 = "",
    sub: []const u8 = "",
    syncErr: []const u8 = "",
    hasQueryErr: bool = false,
    queryErr: []const u8 = "",
    noneManaged: []const u8 = "",
    inputs: []const DrvInput = &.{},
    showTrace: bool = false,
    trace: midimon.Trace = .{},
    reapply: []const u8 = "",
    reload: []const u8 = "",
};

pub const DrvCard = struct {
    show: bool = false,
    card: []const u8 = "",
    badge: []const u8 = "",
    badgeVar: []const u8 = "",
    why: []const u8 = "",
    stVariant: []const u8 = "",
    stLabel: []const u8 = "",
    stLabelDl: []const u8 = "",
    stLine: []const u8 = "",
    installed: bool = false,
    testSign: []const u8 = "",
    steps: []const u8 = "",
    cmds: []const u8 = "",
    smartScreen: []const u8 = "",
    managed: DrvManaged = .{},
    docs: []const u8 = "",
    docsUrl: []const u8 = "",
};

pub const Knob = struct {
    dl: []const u8 = "",
    v: []const u8 = "", // pre-formatted CSS number — raw
    rot: []const u8 = "",
    val: []const u8 = "", // digits — raw (unquoted attribute)
    act: []const u8 = "",
    tid: []const u8 = "",
    aria: []const u8 = "",
    label: []const u8 = "",
    cc: []const u8 = "",
    sweepAct: []const u8 = "",
    sweepTitle: []const u8 = "",
    sweepAria: []const u8 = "",
    sweepGlyph: []const u8 = "",
};

pub const Mom = struct {
    cls: []const u8 = "",
    act: []const u8 = "",
    tid: []const u8 = "",
    dl: []const u8 = "",
    aria: []const u8 = "",
    label: []const u8 = "",
    cc: []const u8 = "",
};

pub const Strip = struct {
    head: []const u8 = "",
    knobs: []const Knob = &.{},
    faders: []const Knob = &.{},
    btns: []const Mom = &.{},
};

pub const Rack = struct {
    card: []const u8 = "",
    stepLbl: []const u8 = "",
    n: []const u8 = "", // digits — raw
    dec: []const u8 = "",
    inc: []const u8 = "",
    minusOff: bool = false,
    plusOff: bool = false,
    sub: []const u8 = "",
    strips: []const Strip = &.{},
};

pub const SwRow = struct {
    name: []const u8 = "",
    badge: []const u8 = "",
    badgeVar: []const u8 = "",
    note: []const u8 = "",
};

pub const Help = struct {
    card: []const u8 = "",
    badge: []const u8 = "",
    step1: []const u8 = "",
    step2: []const u8 = "",
    step3: []const u8 = "",
    feedback: []const u8 = "",
    caveat: []const u8 = "",
    link: []const u8 = "",
    swHdr: []const u8 = "",
    rows: []const SwRow = &.{},
};

pub const State = struct {
    title: []const u8 = "",
    sub: []const u8 = "",
    ctls: ctls.State = .{},
    uimap: uimap.State = .{},
    showMon: bool = false,
    mon: midimon.State = .{},
    port: PortCard = .{},
    driver: DrvCard = .{},
    rack: Rack = .{},
    bridge: ctls.Bridge = .{},
    help: Help = .{},
};

/// render mirrors Go midiCtlHTML (the whole MIDI tab).
pub fn render(h: *Html, s: State) !void {
    try c.panel(h, s.title, s.sub);
    try ctls.render(h, s.ctls);
    try uimap.render(h, s.uimap);
    if (s.showMon) try midimon.render(h, s.mon);
    try h.raw("<div class=midi-2col>");
    try renderPort(h, s.port);
    try renderDriver(h, s.driver);
    try h.raw("</div>");
    try renderRack(h, s.rack);
    try h.raw("<div class=midi-2col>");
    try ctls.renderBridge(h, s.bridge);
    try renderHelp(h, s.help);
    try h.raw("</div>");
}

/// renderPort mirrors Go midiPortCardHTML.
fn renderPort(h: *Html, s: PortCard) !void {
    try c.cardOpen(h, s.card, true); // Go card(): title non-empty ⇒ head, trailing empty
    try c.cardTrailClose(h);
    try h.raw("<p class=page-sub>");
    try h.esc(s.sub);
    try h.raw("</p>");
    try c.selectBox(h, s.port);
    try h.raw("<div id=midi-active>");
    try renderActive(h, s.active);
    try h.raw("</div>");
    try c.btnRowOpen(h);
    try c.btn(h, s.panic, "warn", "midi-panic", "");
    try c.btnRowClose(h);
    try c.cardClose(h);
}

/// renderActive mirrors Go midiActiveRowHTML (#midi-active inner).
pub fn renderActive(h: *Html, s: Active) !void {
    try c.statusRow(h, s.variant, s.label, s.labelDl, s.line);
}

/// renderDriver mirrors Go midiDrvCardHTML.
fn renderDriver(h: *Html, s: DrvCard) !void {
    if (!s.show) return;
    try c.cardOpen(h, s.card, true);
    try c.badge(h, s.badge, s.badgeVar);
    try c.cardTrailClose(h);
    try h.raw("<p class=page-sub>");
    try h.esc(s.why);
    try h.raw("</p>");
    try c.statusRow(h, s.stVariant, s.stLabel, s.stLabelDl, s.stLine);
    if (!s.installed) {
        try c.hint(h, "info", s.testSign);
        try h.raw("<p class=midi-help-note>");
        try h.esc(s.steps);
        try h.raw("</p><pre class=midi-cmds>");
        try h.esc(s.cmds);
        try h.raw("</pre>");
        try c.hint(h, "warn", s.smartScreen);
    } else {
        try renderManaged(h, s.managed);
    }
    try c.btnRowOpen(h);
    try c.btn(h, s.docs, "outline", "open-url", s.docsUrl);
    try c.btnRowClose(h);
    try c.cardClose(h);
}

/// renderManaged mirrors Go midiDrvManagedHTML.
fn renderManaged(h: *Html, s: DrvManaged) !void {
    try h.raw("<div class=pb-label>");
    try h.esc(s.hdr);
    try h.raw("</div><p class=page-sub>");
    try h.esc(s.sub);
    try h.raw("</p>");
    if (s.syncErr.len != 0) try c.hint(h, "warn", s.syncErr);
    if (s.hasQueryErr) {
        try c.hint(h, "warn", s.queryErr);
    } else if (s.inputs.len == 0) {
        try h.raw("<p class=page-sub>");
        try h.esc(s.noneManaged);
        try h.raw("</p>");
    } else {
        for (s.inputs) |in| {
            try c.statusRow(h, in.variant, in.name, in.nameDl, in.line);
            if (in.fbHint.len != 0) try c.hint(h, "info", in.fbHint);
            if (in.hasBtns) {
                try c.btnRowOpen(h);
                try c.btn(h, in.traceLbl, "ghost", in.traceAct, "");
                if (in.fbTest) {
                    try c.btn(h, in.fbTestLbl, "ghost", in.fbTestAct, "");
                    try h.raw(in.fbTip);
                }
                try c.btnRowClose(h);
                if (in.fbRes) try c.statusRow(h, in.fbResVar, in.fbResLbl, in.fbResDl, in.fbResLine);
            }
        }
    }
    if (s.showTrace) try midimon.renderTrace(h, s.trace);
    try c.btnRowOpen(h);
    try c.btn(h, s.reapply, "outline", "midi-drv-sync", "");
    try c.btn(h, s.reload, "ghost", "midi-drv-reload", "");
    try c.btnRowClose(h);
}

/// renderRack mirrors Go midiRackHTML (stepper in the card head + the channel rack).
fn renderRack(h: *Html, s: Rack) !void {
    try c.cardOpen(h, s.card, true);
    try renderStepper(h, s);
    try c.cardTrailClose(h);
    try h.raw("<p class=page-sub>");
    try h.esc(s.sub);
    try h.raw("</p><div class=\"midi-mixer\" data-testid=midi-mixer>");
    for (s.strips) |st| try renderStrip(h, st);
    try h.raw("</div>");
    try c.cardClose(h);
}

/// renderStepper mirrors Go midiStepperHTML.
fn renderStepper(h: *Html, s: Rack) !void {
    try h.raw("<span class=midi-stepper><span class=midi-steplbl>");
    try h.esc(s.stepLbl);
    try h.raw("</span>");
    if (s.minusOff) {
        try h.raw("<button class=\"rp-btn rp-btn--outline\" disabled>-</button>");
    } else {
        try c.btn(h, "-", "outline", "midi-channels", s.dec);
    }
    try h.raw("<span class=midi-chcount data-testid=midi-channels data-label=");
    try h.attrQ(s.stepLbl);
    try h.raw(" data-value=");
    try h.attrQ(s.n);
    try h.raw(">");
    try h.raw(s.n);
    try h.raw("</span>");
    if (s.plusOff) {
        try h.raw("<button class=\"rp-btn rp-btn--outline\" disabled>+</button>");
    } else {
        try c.btn(h, "+", "outline", "midi-channels", s.inc);
    }
    try h.raw("</span>");
}

/// renderStrip mirrors Go midiStripHTML.
fn renderStrip(h: *Html, s: Strip) !void {
    try h.raw("<div class=midi-strip><div class=midi-striphead>");
    try h.esc(s.head);
    try h.raw("</div><div class=midi-knobs>");
    for (s.knobs) |k| try renderKnob(h, k);
    try h.raw("</div>");
    for (s.faders) |f| try renderFader(h, f);
    try h.raw("<div class=midi-btns>");
    for (s.btns) |m| try renderMom(h, m);
    try h.raw("</div></div>");
}

/// knob_oninput / fader_oninput mirror the Go consts (display-only drag sync).
const knob_oninput = "oninput=\"var l=this.closest('.midi-knob');var v=this.value/127;" ++
    "l.style.setProperty('--v',v);l.style.setProperty('--rot',(v*270-135)+'deg')\"";
const fader_oninput = "oninput=\"this.closest('.midi-vfader').style.setProperty('--v',this.value/127)\"";

/// renderKnob mirrors Go midiKnobHTML.
fn renderKnob(h: *Html, k: Knob) !void {
    try h.raw("<label class=midi-knob data-label=");
    try h.attrQ(k.dl);
    try h.raw(" style=\"--v:");
    try h.raw(k.v);
    try h.raw(";--rot:");
    try h.raw(k.rot);
    try h.raw("deg\"><span class=mk-dial aria-hidden=true><span class=mk-ptr></span></span>");
    try renderRangeIn(h, k, "mk-in", knob_oninput);
    try h.raw("<span class=mk-cap>");
    try h.esc(k.label);
    try h.raw("</span><span class=mk-cc>");
    try h.esc(k.cc);
    try h.raw("</span>");
    try renderSweep(h, k, "mk-sweep");
    try h.raw("</label>");
}

/// renderFader mirrors Go midiFaderHTML.
fn renderFader(h: *Html, k: Knob) !void {
    try h.raw("<label class=midi-vfader data-label=");
    try h.attrQ(k.dl);
    try h.raw(" style=\"--v:");
    try h.raw(k.v);
    try h.raw("\"><span class=mf-track aria-hidden=true><span class=mf-fill></span></span>");
    try renderRangeIn(h, k, "mf-in", fader_oninput);
    try h.raw("<span class=mf-cap>");
    try h.esc(k.label);
    try h.raw("</span><span class=mf-cc>");
    try h.esc(k.cc);
    try h.raw("</span>");
    try renderSweep(h, k, "mf-sweep");
    try h.raw("</label>");
}

fn renderRangeIn(h: *Html, k: Knob, cls: []const u8, oninput: []const u8) !void {
    try h.raw("<input class=");
    try h.raw(cls);
    try h.raw(" type=range min=0 max=127 step=1 value=");
    try h.raw(k.val);
    try h.raw(" data-value=");
    try h.raw(k.val);
    try h.raw(" data-actinput=");
    try h.attrQ(k.act);
    try h.raw(" data-testid=");
    try h.attrQ(k.tid);
    try h.raw(" aria-label=");
    try h.attrQ(k.aria);
    try h.raw(" ");
    try h.raw(oninput);
    try h.raw(">");
}

fn renderSweep(h: *Html, k: Knob, cls: []const u8) !void {
    try h.raw("<button class=\"");
    try h.raw(cls);
    try h.raw(" rp-btn rp-btn--ghost\" data-act=");
    try h.attrQ(k.sweepAct);
    try h.raw(" title=");
    try h.attrQ(k.sweepTitle);
    try h.raw(" aria-label=");
    try h.attrQ(k.sweepAria);
    try h.raw(">");
    try h.esc(k.sweepGlyph);
    try h.raw("</button>");
}

/// renderMom mirrors Go midiMomBtnHTML.
fn renderMom(h: *Html, m: Mom) !void {
    try h.raw("<button class=");
    try h.attrQ(m.cls);
    try h.raw(" data-act=");
    try h.attrQ(m.act);
    try h.raw(" data-testid=");
    try h.attrQ(m.tid);
    try h.raw(" data-label=");
    try h.attrQ(m.dl);
    try h.raw(" aria-label=");
    try h.attrQ(m.aria);
    try h.raw("><span class=midi-btn-lbl>");
    try h.esc(m.label);
    try h.raw("</span><span class=midi-btn-cc>");
    try h.esc(m.cc);
    try h.raw("</span></button>");
}

/// renderHelp mirrors Go midiHelpHTML. The mapping-FAQ href is a trusted literal on
/// both sides (Go renders the same constant inline).
fn renderHelp(h: *Html, s: Help) !void {
    try c.cardOpen(h, s.card, true);
    try c.badge(h, s.badge, "info");
    try c.cardTrailClose(h);
    try h.raw("<ol class=midi-help><li>");
    try h.esc(s.step1);
    try h.raw("</li><li>");
    try h.esc(s.step2);
    try h.raw("</li><li>");
    try h.esc(s.step3);
    try h.raw("</li></ol><p class=midi-help-note>");
    try h.esc(s.feedback);
    try h.raw("</p><p class=midi-help-note>");
    try h.esc(s.caveat);
    try h.raw(" <a href=\"https://rekordbox.com/en/support/faq/mapping-6/\" target=_blank rel=noopener>");
    try h.esc(s.link);
    try h.raw("</a></p><div class=pb-label>");
    try h.esc(s.swHdr);
    try h.raw("</div>");
    for (s.rows) |r| {
        try h.raw("<div class=midi-sw><span class=midi-sw-name>");
        try h.esc(r.name);
        try h.raw("</span>");
        try c.badge(h, r.badge, r.badgeVar);
        try h.raw("<span class=midi-sw-note>");
        try h.esc(r.note);
        try h.raw("</span></div>");
    }
    try c.cardClose(h);
}

test "active row" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderActive(&h, .{ .variant = "off", .label = "Active port", .labelDl = "active port", .line = "not open" });
    try std.testing.expectEqualStrings("<div class=strow><span class=\"dot dot--off\"></span>" ++
        "<div class=strow-tx><div class=strow-l data-label=\"active port\">Active port</div>" ++
        "<div class=strow-s data-value=\"not open\">not open</div></div></div>", h.b.items);
}

test "stepper disables at bounds" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderStepper(&h, .{ .stepLbl = "Channels", .n = "1", .dec = "0", .inc = "2", .minusOff = true });
    try std.testing.expectEqualStrings("<span class=midi-stepper><span class=midi-steplbl>Channels</span>" ++
        "<button class=\"rp-btn rp-btn--outline\" disabled>-</button>" ++
        "<span class=midi-chcount data-testid=midi-channels data-label=\"Channels\" data-value=\"1\">1</span>" ++
        "<button class=\"rp-btn rp-btn--outline\" data-act=\"midi-channels\" data-val=\"2\">+</button></span>", h.b.items);
}

test "knob markup shape" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderKnob(&h, .{
        .dl = "ch1 hi",
        .v = "0.5039370078740157",
        .rot = "1.0629921259842407",
        .val = "64",
        .act = "midi-send:0:24",
        .tid = "midi-ch1-eqHigh",
        .aria = "Hi CC24·ch1",
        .label = "Hi",
        .cc = "CC24·ch1",
        .sweepAct = "midi-sweep:0:24",
        .sweepTitle = "Sweep",
        .sweepAria = "Sweep Hi",
        .sweepGlyph = "↯",
    });
    try std.testing.expect(std.mem.startsWith(u8, h.b.items, "<label class=midi-knob data-label=\"ch1 hi\" style=\"--v:0.5039370078740157;--rot:1.0629921259842407deg\">"));
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "value=64 data-value=64 data-actinput=\"midi-send:0:24\"") != null);
    try std.testing.expect(std.mem.endsWith(u8, h.b.items, "aria-label=\"Sweep Hi\">↯</button></label>"));
}

test "empty tab still emits the 2col frames" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .title = "MIDI", .sub = "S" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=midi-2col>") != null);
}
