//! Peers view renderer — byte-exact port of internal/webui/render_peers.go
//! (peersHTML / peersBodyHTML + every section renderer). State arrives fully resolved
//! from Go: peerlink/peerbridge/medialink/webcam/filexfer data, localized strings, and
//! EVERY number already formatted (clock/route telemetry, UVC ranges, the progress-bar
//! fill width) — Zig only walks state → markup.
//!
//! Raw (trusted, un-escaped) fields, matching the Go source literals they replace:
//!   recv row `mark` ("◂ ") · UVC `minS/maxS/stepS/valS` (Go %d) · `barPct` (Go %.1f%%).
//! Golden gate: internal/webui/zigui_golden_peers_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

pub const Deck = struct {
    audible: bool = false,
    line: []const u8 = "",
};

pub const Row = struct {
    dot: []const u8 = "", // "" = no dot prefix
    name: []const u8 = "",
    sub: []const u8 = "",
    btns: []const c.Btn = &.{},
    decks: []const Deck = &.{},
};

pub const List = struct {
    empty: []const u8 = "",
    rows: []const Row = &.{},
};

pub const Banner = struct {
    show: bool = false,
    text: []const u8 = "",
    btn: c.Btn = .{},
};

pub const Route = struct {
    title: []const u8 = "",
    detail: []const u8 = "",
    pipe: []const u8 = "", // "" = omitted
};

pub const RecvRow = struct {
    mark: []const u8 = "", // trusted literal prefix, emitted raw
    line: []const u8 = "",
    btn: c.Btn = .{},
};

pub const Recv = struct {
    show: bool = false,
    head: []const u8 = "",
    rows: []const RecvRow = &.{},
};

pub const Media = struct {
    show: bool = false,
    clockLine: []const u8 = "",
    syncLines: []const []const u8 = &.{},
    hasTc: bool = false,
    tcLine: []const u8 = "",
    noRoutes: []const u8 = "",
    routesHdr: []const u8 = "",
    routes: []const Route = &.{},
    recv: Recv = .{},
};

pub const CamProp = struct {
    label: []const u8 = "",
    minS: []const u8 = "0",
    maxS: []const u8 = "0",
    stepS: []const u8 = "1",
    valS: []const u8 = "0",
    act: []const u8 = "",
    disabled: bool = false,
    canAuto: bool = false,
    auto: bool = false,
    autoAct: []const u8 = "",
    autoLbl: []const u8 = "",
};

pub const CamNode = struct {
    name: []const u8 = "",
    refreshAct: []const u8 = "",
    status: []const u8 = "",
    dev: c.Select = .{},
    mode: c.Select = .{},
    start: c.Btn = .{},
    sender: []const u8 = "", // "" = omit the sender row
    senderLine: []const u8 = "",
    propsHdr: []const u8 = "",
    props: []const CamProp = &.{},
};

pub const Cam = struct {
    show: bool = false,
    gated: bool = false,
    gateHint: []const u8 = "",
    empty: []const u8 = "",
    nodes: []const CamNode = &.{},
};

pub const XferSet = struct {
    show: bool = false,
    enabled: c.Toggle = .{},
    acceptLbl: []const u8 = "",
    mode: []const u8 = "",
    askLbl: []const u8 = "",
    autoLbl: []const u8 = "",
    dir: c.Field = .{},
    defaultDir: []const u8 = "",
};

pub const XferPend = struct {
    line: []const u8 = "",
    btns: []const c.Btn = &.{},
};

pub const XferProg = struct {
    title: []const u8 = "",
    isBadge: bool = false,
    btn: c.Btn = .{},
    badge: []const u8 = "",
    badgeVar: []const u8 = "",
    bar: bool = false,
    barPct: []const u8 = "",
    barCap: []const u8 = "",
    subText: []const u8 = "",
};

pub const Xfer = struct {
    show: bool = false,
    settings: XferSet = .{},
    none: bool = false,
    noneHint: []const u8 = "",
    pend: []const XferPend = &.{},
    rows: []const XferProg = &.{},
};

pub const Body = struct {
    strip: []const u8 = "",
    banner: Banner = .{},
    connsTitle: []const u8 = "",
    conns: List = .{},
    mediaTitle: []const u8 = "",
    media: Media = .{},
    camTitle: []const u8 = "",
    cam: Cam = .{},
    xferTitle: []const u8 = "",
    xfer: Xfer = .{},
    netTitle: []const u8 = "",
    discovered: List = .{},
    rememberedTitle: []const u8 = "",
    remembered: List = .{},
};

pub const State = struct {
    title: []const u8 = "",
    sub: []const u8 = "",
    available: bool = false,
    unavailable: []const u8 = "",
    body: Body = .{},
};

/// render mirrors Go peersHTML (full tab view).
pub fn render(h: *Html, s: State) !void {
    if (!s.available) {
        try c.panel(h, s.title, "");
        try c.emptyState(h, s.unavailable);
        return;
    }
    try c.panel(h, s.title, s.sub);
    try h.raw("<div id=peers-body>");
    try renderBody(h, s.body);
    try h.raw("</div>");
}

/// renderBody mirrors Go peersBodyHTML (#peers-body inner, the ~1 Hz tick patch).
pub fn renderBody(h: *Html, s: Body) !void {
    try h.raw("<div id=peers-strip class=peers-strip>");
    try renderStrip(h, s.strip);
    try h.raw("</div>");
    try renderBanner(h, s.banner);
    try c.sectionOpen(h, s.connsTitle);
    try renderList(h, s.conns);
    try c.sectionClose(h);
    if (s.media.show) {
        try c.sectionOpen(h, s.mediaTitle);
        try renderMedia(h, s.media);
        try c.sectionClose(h);
    }
    if (s.cam.show) {
        try c.sectionOpen(h, s.camTitle);
        try renderCam(h, s.cam);
        try c.sectionClose(h);
    }
    if (s.xfer.show) {
        try c.sectionOpen(h, s.xferTitle);
        try renderXfer(h, s.xfer);
        try c.sectionClose(h);
    }
    // two sibling lists share a row ≥1100px (.peers-2col)
    try h.raw("<div class=peers-2col>");
    try c.sectionOpen(h, s.netTitle);
    try renderList(h, s.discovered);
    try c.sectionClose(h);
    try c.sectionOpen(h, s.rememberedTitle);
    try renderList(h, s.remembered);
    try c.sectionClose(h);
    try h.raw("</div>");
}

/// renderStrip mirrors Go peerStripHTML (data-label is a Go source literal).
fn renderStrip(h: *Html, txt: []const u8) !void {
    try h.raw("<span data-label=\"peer counts\" data-value=\"");
    try h.esc(txt);
    try h.raw("\">");
    try h.esc(txt);
    try h.raw("</span>");
}

fn renderBanner(h: *Html, s: Banner) !void {
    if (!s.show) return;
    try h.raw("<div class=ctl-banner data-label=\"controlling\"><span class=ctl-banner-tx>🎛 ");
    try h.esc(s.text);
    try h.raw("</span>");
    try c.btnOf(h, s.btn);
    try h.raw("</div>");
}

fn renderList(h: *Html, s: List) !void {
    if (s.rows.len == 0) {
        try c.emptyState(h, s.empty);
        return;
    }
    try h.raw("<div class=\"rp-card\">");
    for (s.rows) |r| try renderRow(h, r);
    try h.raw("</div>");
}

fn renderRow(h: *Html, r: Row) !void {
    try h.raw("<div class=row><span class=row-label>");
    if (r.dot.len != 0) {
        try c.dot(h, r.dot);
        try h.raw(" ");
    }
    try h.esc(r.name);
    try h.raw(" <span class=np-artist>");
    try h.esc(r.sub);
    try h.raw("</span></span>");
    try c.btnRowOf(h, r.btns);
    try h.raw("</div>");
    for (r.decks) |d| {
        try h.raw("<div class=\"");
        try h.raw(if (d.audible) "peer-np" else "peer-np peer-np--quiet");
        try h.raw("\">");
        try h.raw(if (d.audible) "▶ " else "▷ ");
        try h.esc(d.line);
        try h.raw("</div>");
    }
}

// renderMedia wraps the media plane in its own patch target (#peers-media): the route counters
// must keep advancing while the general ~1 Hz tick is withheld by the activity governor, and that
// exemption patches this fragment alone. Byte-identical to Go's peerMediaHTML.
fn renderMedia(h: *Html, s: Media) !void {
    try h.raw("<div id=peers-media>");
    try renderMediaInner(h, s);
    try h.raw("</div>");
}

fn renderMediaInner(h: *Html, s: Media) !void {
    try h.raw("<div class=\"rp-card\">");
    // Clock: active tier/lock/offset + per-peer sync estimates.
    try h.raw("<div class=media-clock>");
    try h.esc(s.clockLine);
    try h.raw("</div>");
    for (s.syncLines) |ln| {
        try h.raw("<div class=media-sub>");
        try h.esc(ln);
        try h.raw("</div>");
    }
    // Timecode master state.
    if (s.hasTc) {
        try h.raw("<div class=media-clock>");
        try h.esc(s.tcLine);
        try h.raw("</div>");
    }
    // Routes.
    if (s.routes.len == 0) {
        try h.raw("<div class=np-artist>");
        try h.esc(s.noRoutes);
        try h.raw("</div>");
    } else {
        try h.raw("<div class=media-sub>");
        try h.esc(s.routesHdr);
        try h.raw("</div>");
        for (s.routes) |r| {
            try h.raw("<div class=media-route>");
            try h.esc(r.title);
            try h.raw("</div><div class=media-sub>");
            try h.esc(r.detail);
            try h.raw("</div>");
            if (r.pipe.len != 0) {
                try h.raw("<div class=media-sub>");
                try h.esc(r.pipe);
                try h.raw("</div>");
            }
        }
    }
    try h.raw("</div>");
    if (s.recv.show) try renderRecv(h, s.recv);
}

fn renderRecv(h: *Html, s: Recv) !void {
    try h.raw("<div class=media-recv-head>");
    try h.esc(s.head);
    try h.raw("</div><div class=\"rp-card\">");
    for (s.rows) |r| {
        try h.raw("<div class=row><span class=row-label>");
        try h.raw(r.mark);
        try h.esc(r.line);
        try h.raw("</span>");
        try c.btnRowOpen(h);
        try c.btnOf(h, r.btn);
        try c.btnRowClose(h);
        try h.raw("</div>");
    }
    try h.raw("</div>");
}

fn renderCam(h: *Html, s: Cam) !void {
    if (s.gated) {
        try c.hint(h, "info", s.gateHint);
        return;
    }
    if (s.nodes.len == 0) {
        try c.emptyState(h, s.empty);
        return;
    }
    for (s.nodes) |n| try renderCamNode(h, n);
}

fn renderCamNode(h: *Html, n: CamNode) !void {
    try h.raw("<div class=\"rp-card cam-node\"><div class=cam-head><span class=cam-title>");
    try h.esc(n.name);
    try h.raw("</span>");
    try c.btn(h, "↻", "ghost", n.refreshAct, "");
    try h.raw("</div><div class=cam-status>");
    try h.esc(n.status);
    try h.raw("</div><div class=cam-ctls>");
    try c.selectBox(h, n.dev);
    try c.selectBox(h, n.mode);
    try c.btnOf(h, n.start);
    try h.raw("</div>");
    if (n.sender.len != 0) {
        try h.raw("<div class=cam-sender data-label=\"spout sender\" data-value=\"");
        try h.esc(n.sender);
        try h.raw("\">");
        try h.esc(n.senderLine);
        try h.raw("</div>");
    }
    if (n.props.len != 0) {
        try h.raw("<div class=cam-props-h>");
        try h.esc(n.propsHdr);
        try h.raw("</div>");
        for (n.props) |p| try renderCamProp(h, p);
    }
    try h.raw("</div>");
}

/// renderCamProp mirrors Go camPropHTML: label + range + live value + optional auto box.
/// The oninput handler is a Go source literal (display-only, no dispatch).
fn renderCamProp(h: *Html, p: CamProp) !void {
    try h.raw("<div class=cam-prop><span class=cam-prop-l>");
    try h.esc(p.label);
    try h.raw("</span><input class=\"slider-input cam-prop-s\" type=range min=");
    try h.raw(p.minS);
    try h.raw(" max=");
    try h.raw(p.maxS);
    try h.raw(" step=");
    try h.raw(p.stepS);
    try h.raw(" value=");
    try h.raw(p.valS);
    try h.raw(" data-act=");
    try h.attrQ(p.act);
    try h.raw(" data-value=");
    try h.raw(p.valS);
    if (p.disabled) try h.raw(" disabled");
    try h.raw(" oninput=\"var v=this.parentNode.querySelector('.cam-prop-v');if(v)v.textContent=this.value\">");
    try h.raw("<span class=cam-prop-v>");
    try h.raw(p.valS);
    try h.raw("</span>");
    if (p.canAuto) {
        try h.raw("<label class=cam-prop-auto><input type=checkbox");
        if (p.auto) try h.raw(" checked");
        try h.raw(" data-act=");
        try h.attrQ(p.autoAct);
        try h.raw(" data-value=");
        try h.attrQ(if (p.auto) "true" else "false");
        try h.raw(">");
        try h.esc(p.autoLbl);
        try h.raw("</label>");
    }
    try h.raw("</div>");
}

fn renderXfer(h: *Html, s: Xfer) !void {
    try renderXferSet(h, s.settings);
    if (s.none) {
        try c.hint(h, "info", s.noneHint);
        return;
    }
    try h.raw("<div class=\"rp-card\">");
    // Pending incoming accepts first - they need a decision.
    for (s.pend) |p| {
        try h.raw("<div class=row><span class=row-label>");
        try h.esc(p.line);
        try h.raw("</span>");
        try c.btnRowOf(h, p.btns);
        try h.raw("</div>");
    }
    for (s.rows) |r| try renderXferRow(h, r);
    try h.raw("</div>");
}

fn renderXferSet(h: *Html, s: XferSet) !void {
    if (!s.show) return;
    try h.raw("<div class=\"rp-card cam-node\">");
    try c.toggleOf(h, s.enabled);
    try h.raw("<div class=xfer-mode><span class=field-label>");
    try h.esc(s.acceptLbl);
    try h.raw("</span>");
    const modes = [_]c.Tab{ .{ .val = "ask", .label = s.askLbl }, .{ .val = "auto", .label = s.autoLbl } };
    try c.subTabs(h, "peers-xfer-mode:", s.mode, &modes);
    try h.raw("</div>");
    try c.fieldOf(h, s.dir);
    try h.raw("<div class=np-artist>");
    try h.esc(s.defaultDir);
    try h.raw("</div></div>");
}

fn renderXferRow(h: *Html, r: XferProg) !void {
    try h.raw("<div class=xfer-row><div class=row><span class=row-label>");
    try h.esc(r.title);
    try h.raw("</span>");
    try c.btnRowOpen(h);
    if (r.isBadge) try c.badge(h, r.badge, r.badgeVar) else try c.btnOf(h, r.btn);
    try c.btnRowClose(h);
    try h.raw("</div><div class=xfer-sub>");
    if (r.bar) {
        try c.progressBar(h, r.barPct, r.barCap);
    } else {
        try h.raw("<span class=np-artist>");
        try h.esc(r.subText);
        try h.raw("</span>");
    }
    try h.raw("</div></div>");
}

test "unavailable" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .title = "Peers", .sub = "ignored", .unavailable = "no peer link" });
    try std.testing.expectEqualStrings("<h1 class=page-title>Peers</h1>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">no peer link</div></div>", h.b.items);
}

test "strip + banner" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderStrip(&h, "1 connected · node ab&c");
    try std.testing.expectEqualStrings("<span data-label=\"peer counts\" data-value=\"1 connected · node ab&amp;c\">" ++
        "1 connected · node ab&amp;c</span>", h.b.items);
    h.b.clearRetainingCapacity();
    try renderBanner(&h, .{ .show = true, .text = "Controlling Studio", .btn = .{ .label = "Stop", .variant = "warn", .act = "peers-control:n1", .val = "0" } });
    try std.testing.expectEqualStrings("<div class=ctl-banner data-label=\"controlling\"><span class=ctl-banner-tx>🎛 " ++
        "Controlling Studio</span><button class=\"rp-btn rp-btn--warn\" data-act=\"peers-control:n1\" data-val=\"0\">Stop</button></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try renderBanner(&h, .{});
    try std.testing.expectEqualStrings("", h.b.items);
}

test "row with dot + decks" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const decks = [_]Deck{ .{ .audible = true, .line = "Deck A · X - Y" }, .{ .line = "Deck B · Q" } };
    const btns = [_]c.Btn{.{ .label = "Forget", .variant = "ghost", .act = "peer-forget:n1" }};
    try renderRow(&h, .{ .dot = "success", .name = "Stu&dio", .sub = "connected", .btns = &btns, .decks = &decks });
    try std.testing.expectEqualStrings("<div class=row><span class=row-label>" ++
        "<span class=\"dot dot--success\"></span> Stu&amp;dio <span class=np-artist>connected</span></span>" ++
        "<div class=btn-row><button class=\"rp-btn rp-btn--ghost\" data-act=\"peer-forget:n1\">Forget</button></div></div>" ++
        "<div class=\"peer-np\">▶ Deck A · X - Y</div>" ++
        "<div class=\"peer-np peer-np--quiet\">▷ Deck B · Q</div>", h.b.items);
}

test "empty list renders the empty state" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderList(&h, .{ .empty = "no peers" });
    try std.testing.expectEqualStrings("<div class=\"rp-empty\"><div class=\"rp-empty__title\">no peers</div></div>", h.b.items);
}

test "media plane: no routes + recv block" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const syncs = [_][]const u8{"Studio +0.42 ms"};
    const rows = [_]RecvRow{
        .{ .mark = "◂ ", .line = "cam @ Studio", .btn = .{ .label = "Stop", .variant = "destructive", .act = "media-stop:s1" } },
        .{ .line = "cam2 @ Studio · 1280x720@30", .btn = .{ .label = "Receive", .variant = "go", .act = "media-recv:n1\x1fd2" } },
    };
    try renderMedia(&h, .{
        .show = true,
        .clockLine = "Clock tier 1 · locked",
        .syncLines = &syncs,
        .hasTc = true,
        .tcLine = "TC master: this instance",
        .noRoutes = "No active media routes",
        .recv = .{ .show = true, .head = "Receive video", .rows = &rows },
    });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=np-artist>No active media routes</div></div>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<span class=row-label>◂ cam @ Studio</span>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-act=\"media-recv:n1\x1fd2\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=media-clock>TC master: this instance</div>") != null);
}

test "media plane: routes with pipeline line" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const routes = [_]Route{
        .{ .title = "→ Studio · s1", .detail = "loss 0", .pipe = "nvenc tier 2" },
        .{ .title = "← Studio · s2", .detail = "loss 3" },
    };
    try renderMedia(&h, .{ .show = true, .clockLine = "c", .routesHdr = "Routes: 2", .routes = &routes });
    // The #peers-media wrapper is load-bearing: it is the patch target of the governor-exempt
    // route-counter tick (webui livePush). Losing it silently re-freezes the panel while streaming.
    try std.testing.expectEqualStrings("<div id=peers-media><div class=\"rp-card\"><div class=media-clock>c</div>" ++
        "<div class=media-sub>Routes: 2</div>" ++
        "<div class=media-route>→ Studio · s1</div><div class=media-sub>loss 0</div><div class=media-sub>nvenc tier 2</div>" ++
        "<div class=media-route>← Studio · s2</div><div class=media-sub>loss 3</div></div></div>", h.b.items);
}

test "cam node: gated, empty, full" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderCam(&h, .{ .show = true, .gated = true, .gateHint = "Webcam off" });
    try std.testing.expectEqualStrings("<span class=\"hint hint--info\">Webcam off</span>", h.b.items);
    h.b.clearRetainingCapacity();
    try renderCam(&h, .{ .show = true, .empty = "No cameras" });
    try std.testing.expectEqualStrings("<div class=\"rp-empty\"><div class=\"rp-empty__title\">No cameras</div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    const props = [_]CamProp{.{
        .label = "Zoom",
        .minS = "0",
        .maxS = "100",
        .stepS = "2",
        .valS = "40",
        .act = "peers-cam-prop:n1\x1fzoom",
        .disabled = true,
        .canAuto = true,
        .auto = true,
        .autoAct = "peers-cam-auto:n1\x1fzoom",
        .autoLbl = "Auto",
    }};
    const nodes = [_]CamNode{.{
        .name = "This instance",
        .refreshAct = "peers-cam-refresh:n1",
        .status = "Ready",
        .dev = .{ .id = "peers-cam-device-n1", .label = "Device", .curLabel = "C920" },
        .mode = .{ .id = "peers-cam-mode-n1", .label = "Mode", .curLabel = "1280x720 @ 30" },
        .start = .{ .label = "Start", .variant = "go", .act = "peers-cam-start:n1", .val = "start" },
        .sender = "rave-cam",
        .senderLine = "Spout sender: rave-cam",
        .propsHdr = "Lens / image",
        .props = &props,
    }};
    try renderCam(&h, .{ .show = true, .nodes = &nodes });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=\"rp-card cam-node\"><div class=cam-head><span class=cam-title>This instance</span>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "value=40 data-act=\"peers-cam-prop:n1\x1fzoom\" data-value=40 disabled oninput=") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<span class=cam-prop-v>40</span>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<input type=checkbox checked data-act=\"peers-cam-auto:n1\x1fzoom\" data-value=\"true\">Auto</label>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=cam-sender data-label=\"spout sender\" data-value=\"rave-cam\">Spout sender: rave-cam</div>") != null);
}

test "xfer: settings + none hint" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderXfer(&h, .{
        .show = true,
        .none = true,
        .noneHint = "No transfers yet",
        .settings = .{
            .show = true,
            .enabled = .{ .label = "Receive files", .dl = "receive files", .act = "peers-xfer-enabled", .on = true },
            .acceptLbl = "Accept",
            .mode = "auto",
            .askLbl = "Ask",
            .autoLbl = "Automatic",
            .dir = .{ .label = "Save to", .dl = "save to", .act = "peers-xfer-dir", .value = "D:\\in", .inputType = "text" },
            .defaultDir = "Default: D:\\in",
        },
    });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<div class=xfer-mode><span class=field-label>Accept</span><div class=subtabs>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<button class=\"subtab active\" data-act=\"peers-xfer-mode:auto\" data-val=\"auto\">Automatic</button>") != null);
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<span class=\"hint hint--info\">No transfers yet</span>") != null);
}

test "xfer rows: bar vs badge" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderXferRow(&h, .{ .title = "⇩ set.wav from Studio", .bar = true, .barPct = "42.5%", .barCap = "4.2 MB / 10.0 MB · 1.1 MB/s", .btn = .{ .label = "Cancel", .variant = "ghost", .act = "xfer-cancel:t1" } });
    try std.testing.expectEqualStrings("<div class=xfer-row><div class=row><span class=row-label>⇩ set.wav from Studio</span>" ++
        "<div class=btn-row><button class=\"rp-btn rp-btn--ghost\" data-act=\"xfer-cancel:t1\">Cancel</button></div></div>" ++
        "<div class=xfer-sub><div class=pbar><div class=pbar-fill style=\"width:42.5%\"></div>" ++
        "<span class=pbar-cap>4.2 MB / 10.0 MB · 1.1 MB/s</span></div></div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try renderXferRow(&h, .{ .title = "t", .isBadge = true, .badge = "Done", .badgeVar = "success", .subText = "2 files · 10.0 MB" });
    try std.testing.expectEqualStrings("<div class=xfer-row><div class=row><span class=row-label>t</span>" ++
        "<div class=btn-row><span class=\"rp-badge rp-badge--success\">Done</span></div></div>" ++
        "<div class=xfer-sub><span class=np-artist>2 files · 10.0 MB</span></div></div>", h.b.items);
}

test "body section order" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderBody(&h, .{
        .strip = "0 connected",
        .connsTitle = "Connections",
        .conns = .{ .empty = "none" },
        .netTitle = "On this network",
        .discovered = .{ .empty = "off" },
        .rememberedTitle = "Remembered",
        .remembered = .{ .empty = "no peers" },
    });
    try std.testing.expectEqualStrings("<div id=peers-strip class=peers-strip>" ++
        "<span data-label=\"peer counts\" data-value=\"0 connected\">0 connected</span></div>" ++
        "<section class=sec><h2 class=sec-title>Connections</h2>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">none</div></div></section>" ++
        "<div class=peers-2col>" ++
        "<section class=sec><h2 class=sec-title>On this network</h2>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">off</div></div></section>" ++
        "<section class=sec><h2 class=sec-title>Remembered</h2>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">no peers</div></div></section></div>", h.b.items);
}
