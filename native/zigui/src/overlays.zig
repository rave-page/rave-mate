//! Overlays view renderer — byte-exact port of internal/webui/render_overlays.go
//! (overlaysHTML + the per-card renderers + the four live-patched fragments). State
//! arrives fully resolved from Go (config + live status + localized strings).
//! Golden gate: internal/webui/zigui_golden_overlays_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

/// Card is one output card's header + optional live-status region (statusId is a
/// trusted literal id, emitted raw like the Go renderer).
pub const Card = struct {
    title: []const u8 = "",
    statusId: []const u8 = "",
    status: c.Status = .{},
};

pub const Appearance = struct {
    card: Card = .{},
    note1: []const u8 = "",
    btns: []const c.Btn = &.{},
    fader: c.Toggle = .{},
    note2: []const u8 = "",
};

pub const Web = struct {
    card: Card = .{},
    port: c.Field = .{},
    btns: []const c.Btn = &.{},
    url: c.KV = .{},
    note1: []const u8 = "",
    autoAdd: c.Toggle = .{},
    scene: c.Field = .{},
    nest: c.Toggle = .{},
    note2: []const u8 = "",
};

pub const Wave = struct {
    card: Card = .{},
    note1: []const u8 = "",
    zoom: c.Select = .{},
    playhead: c.Select = .{},
    waveColor: c.Field = .{},
    waveOpac: c.Slider = .{},
    bgColor: c.Field = .{},
    bgOpac: c.Slider = .{},
    note2: []const u8 = "",
};

pub const Dir = struct {
    card: Card = .{},
    dir: c.Field = .{},
    open: c.Btn = .{},
    note: []const u8 = "",
};

pub const Note = struct {
    card: Card = .{},
    note: []const u8 = "",
};

pub const Spout = struct {
    note: []const u8 = "",
    statusLine: []const u8 = "",
    installLbl: []const u8 = "",
    canInstall: bool = false,
    openSdk: []const u8 = "", // "" = installed (no SDK button)
    sdkUrl: []const u8 = "",
};

pub const VideoShare = struct {
    card: Card = .{},
    note: []const u8 = "",
    scale: c.Select = .{},
    note2: []const u8 = "",
    spout: bool = false,
    spoutCtl: Spout = .{},
};

pub const Strip = struct {
    parts: []const u8 = "",
    hint: []const u8 = "",
    right: []const u8 = "",
};

pub const State = struct {
    title: []const u8 = "",
    sub: []const u8 = "",
    available: bool = false,
    unavailable: []const u8 = "",
    topBtns: []const c.Btn = &.{},
    appearance: Appearance = .{},
    web: Web = .{},
    wave: Wave = .{},
    png: Dir = .{},
    obs: Note = .{},
    vs: VideoShare = .{},
    np: Dir = .{},
    strip: Strip = .{},
};

/// render mirrors Go overlaysHTML (full tab view).
pub fn render(h: *Html, s: State) !void {
    if (!s.available) {
        try c.panel(h, s.title, "");
        try c.emptyState(h, s.unavailable);
        return;
    }
    try c.panel(h, s.title, s.sub);
    try c.btnRowOf(h, s.topBtns);
    try h.raw("<div class=ovl-cards><div id=ovl-appearance>");
    try renderAppearance(h, s.appearance);
    try h.raw("</div>");
    try renderWeb(h, s.web);
    try renderWave(h, s.wave);
    try renderDir(h, s.png);
    try renderNote(h, s.obs);
    try renderVS(h, s.vs);
    try renderDir(h, s.np);
    try h.raw("</div><div id=ovl-strip class=livestrip>");
    try renderStrip(h, s.strip);
    try h.raw("</div>");
}

/// note mirrors Go ovlNote (muted per-card explanation paragraph).
fn note(h: *Html, text: []const u8) !void {
    try h.raw("<p class=ovl-note>");
    try h.esc(text);
    try h.raw("</p>");
}

/// cardOpen mirrors Go ovlCardHTML's prologue: rp-card head + the status region, which
/// precedes the body. Go card() emits the head when title OR trailing is non-empty;
/// overlays never passes a trailing slot, so head = title non-empty.
fn cardOpen(h: *Html, cd: Card) !void {
    try c.cardOpen(h, cd.title, cd.title.len != 0);
    if (cd.title.len != 0) try c.cardHeadClose(h);
    if (cd.statusId.len != 0) {
        try h.raw("<div id=");
        try h.raw(cd.statusId);
        try h.raw(">");
        try c.statusOf(h, cd.status);
        try h.raw("</div>");
    }
}

/// renderAppearance mirrors Go ovlApprHTML (#ovl-appearance fragment).
pub fn renderAppearance(h: *Html, s: Appearance) !void {
    try cardOpen(h, s.card);
    try note(h, s.note1);
    try c.btnRowOf(h, s.btns);
    try c.toggleOf(h, s.fader);
    try note(h, s.note2);
    try c.cardClose(h);
}

fn renderWeb(h: *Html, s: Web) !void {
    try cardOpen(h, s.card);
    try c.fieldOf(h, s.port);
    try c.btnRowOf(h, s.btns);
    try c.kvOf(h, s.url);
    try note(h, s.note1);
    try h.raw("<hr class=ovl-sep>");
    try c.toggleOf(h, s.autoAdd);
    try c.fieldOf(h, s.scene);
    try c.toggleOf(h, s.nest);
    try note(h, s.note2);
    try c.cardClose(h);
}

fn renderWave(h: *Html, s: Wave) !void {
    try cardOpen(h, s.card);
    try note(h, s.note1);
    try c.fpairOpen(h);
    try c.selectBox(h, s.zoom);
    try c.selectBox(h, s.playhead);
    try c.fpairClose(h);
    try c.fpairOpen(h);
    try c.fieldOf(h, s.waveColor);
    try c.slider(h, s.waveOpac);
    try c.fpairClose(h);
    try c.fpairOpen(h);
    try c.fieldOf(h, s.bgColor);
    try c.slider(h, s.bgOpac);
    try c.fpairClose(h);
    try note(h, s.note2);
    try c.cardClose(h);
}

fn renderDir(h: *Html, s: Dir) !void {
    try cardOpen(h, s.card);
    try c.fieldOf(h, s.dir);
    try c.btnRowOpen(h);
    try c.btnOf(h, s.open);
    try c.btnRowClose(h);
    try note(h, s.note);
    try c.cardClose(h);
}

fn renderNote(h: *Html, s: Note) !void {
    try cardOpen(h, s.card);
    try note(h, s.note);
    try c.cardClose(h);
}

fn renderVS(h: *Html, s: VideoShare) !void {
    try cardOpen(h, s.card);
    try note(h, s.note);
    try c.selectBox(h, s.scale);
    try note(h, s.note2);
    if (s.spout) {
        try h.raw("<hr class=ovl-sep><div id=ovl-spout>");
        try renderSpout(h, s.spoutCtl);
        try h.raw("</div>");
    }
    try c.cardClose(h);
}

/// renderSpout mirrors Go ovlSpoutHTML (#ovl-spout fragment).
pub fn renderSpout(h: *Html, s: Spout) !void {
    try note(h, s.note);
    try h.raw("<div class=ovl-note>");
    try h.esc(s.statusLine);
    try h.raw("</div>");
    try c.btnRowOpen(h);
    if (s.canInstall) {
        try c.btn(h, s.installLbl, "outline", "ovl-spout-install", "");
    } else {
        try h.raw("<button class=\"rp-btn rp-btn--outline\" disabled>");
        try h.esc(s.installLbl);
        try h.raw("</button>");
    }
    if (s.openSdk.len != 0) try c.btn(h, s.openSdk, "ghost", "open-url", s.sdkUrl);
    try c.btnRowClose(h);
    try h.raw("<div id=ovl-spout-prog></div>");
}

/// renderStrip mirrors Go ovlStripHTMLOf (#ovl-strip fragment).
pub fn renderStrip(h: *Html, s: Strip) !void {
    try h.raw("<span>");
    try h.esc(s.parts);
    try h.raw("</span><span>");
    try h.esc(s.hint);
    try h.raw("</span><span>");
    try h.esc(s.right);
    try h.raw("</span>");
}

/// renderStatus mirrors Go uiStatus.html (#ovl-st-<kind> fragment).
pub fn renderStatus(h: *Html, s: c.Status) !void {
    try c.statusOf(h, s);
}

test "unavailable" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .title = "Overlays", .sub = "ignored", .unavailable = "no cfg" });
    try std.testing.expectEqualStrings("<h1 class=page-title>Overlays</h1>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">no cfg</div></div>", h.b.items);
}

test "strip" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderStrip(&h, .{ .parts = "Web ✓ · PNG -", .hint = "h&t", .right = "OBS off" });
    try std.testing.expectEqualStrings("<span>Web ✓ · PNG -</span><span>h&amp;t</span><span>OBS off</span>", h.b.items);
}

test "spout installed hides the sdk button" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderSpout(&h, .{ .note = "n", .statusLine = "found at C:\\x", .installLbl = "Reinstall", .canInstall = true });
    try std.testing.expectEqualStrings("<p class=ovl-note>n</p><div class=ovl-note>found at C:\\x</div>" ++
        "<div class=btn-row><button class=\"rp-btn rp-btn--outline\" data-act=\"ovl-spout-install\">Reinstall</button></div>" ++
        "<div id=ovl-spout-prog></div>", h.b.items);
}

test "spout not installable renders a plain disabled button" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderSpout(&h, .{ .installLbl = "In&stall", .canInstall = false, .openSdk = "SDK", .sdkUrl = "https://x/?a&b" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<button class=\"rp-btn rp-btn--outline\" disabled>In&amp;stall</button>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-val=\"https://x/?a&amp;b\"") != null);
}

test "appearance card has no status region" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderAppearance(&h, .{ .card = .{ .title = "Appearance" }, .note1 = "a", .fader = .{ .label = "F", .dl = "f", .act = "ovl-fader" }, .note2 = "b" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div id=") == null);
    try std.testing.expect(std.mem.startsWith(u8, h.b.items, "<div class=\"rp-card\"><div class=card-head><span class=card-h>Appearance</span>"));
}
