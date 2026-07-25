//! Worlds tab renderer — byte-exact port of internal/webui/render_worlds.go (worldsHTML + the
//! linkhint/github/status/unity-rows fragments). State arrives fully resolved from Go (config,
//! GitHub session, publish outcomes, off-thread Unity inspects, federation memo); this file only
//! walks state → markup.
//!
//! Prose fields (ws-help paragraphs, card titles, the add-list placeholder/submit label) are
//! trusted Go-source literals the Go renderer emits UNESCAPED — they are written with raw() here
//! for byte parity (escaping them would change the DOM: they carry apostrophes).
//! Golden gate: internal/webui/zigui_golden_worlds_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

fn eq(a: []const u8, b: []const u8) bool {
    return std.mem.eql(u8, a, b);
}

/// Hint: bare hint chip (#world-linkhint).
pub const Hint = struct {
    tone: []const u8 = "",
    text: []const u8 = "",
};

/// GitHub: link control (#world-gh). mode ∈ {unavailable,linked,unlinked}.
pub const GitHub = struct {
    mode: []const u8 = "",
    msg: []const u8 = "",
    linkedLabel: []const u8 = "",
    linkedDl: []const u8 = "",
    login: []const u8 = "",
    linkedHelp: []const u8 = "",
    unlinkLabel: []const u8 = "",
    unlinkedHelp: []const u8 = "",
    deviceLabel: []const u8 = "",
    patLabel: []const u8 = "",
};

/// Status: one publish target's live status (#world-st-<key>).
pub const Status = struct {
    tone: []const u8 = "",
    line: []const u8 = "",
    url: []const u8 = "",
    copyLabel: []const u8 = "",
    openLabel: []const u8 = "",
    htmlUrl: []const u8 = "",
};

pub const ListRow = struct {
    key: []const u8 = "", // status-region id suffix (emitted raw, Go parity)
    name: []const u8 = "",
    entries: []const u8 = "",
    editAct: []const u8 = "",
    pubAct: []const u8 = "",
    delAct: []const u8 = "",
    status: Status = .{},
};

pub const Lists = struct {
    help: []const u8 = "",
    empty: []const u8 = "",
    rows: []const ListRow = &.{},
    editLabel: []const u8 = "",
    pubLabel: []const u8 = "",
    delLabel: []const u8 = "",
    addPlaceholder: []const u8 = "",
    addLabel: []const u8 = "",
};

pub const PosterRow = struct {
    title: []const u8 = "",
    sub: []const u8 = "",
    editAct: []const u8 = "",
    delAct: []const u8 = "",
};

pub const Posters = struct {
    cardTitle: []const u8 = "",
    addLabel: []const u8 = "",
    pubLabel: []const u8 = "",
    toggleLabel: []const u8 = "",
    toggleDl: []const u8 = "",
    toggleOn: bool = false,
    help: []const u8 = "",
    empty: []const u8 = "",
    rows: []const PosterRow = &.{},
    editLabel: []const u8 = "",
    delLabel: []const u8 = "",
    status: Status = .{},
};

pub const Events = struct {
    cardTitle: []const u8 = "",
    pubLabel: []const u8 = "",
    toggleLabel: []const u8 = "",
    toggleDl: []const u8 = "",
    toggleOn: bool = false,
    help: []const u8 = "",
    status: Status = .{},
};

pub const NowPlaying = struct {
    cardTitle: []const u8 = "",
    pubLabel: []const u8 = "",
    toggleLabel: []const u8 = "",
    toggleDl: []const u8 = "",
    toggleOn: bool = false,
    linkLabel: []const u8 = "",
    linkDl: []const u8 = "",
    link: []const u8 = "",
    imgLabel: []const u8 = "",
    imgDl: []const u8 = "",
    img: []const u8 = "",
    imgWarn: []const u8 = "",
    help: []const u8 = "",
    status: Status = .{},
};

pub const UnityRow = struct {
    name: []const u8 = "",
    dir: []const u8 = "",
    act: []const u8 = "",
};

/// Unity: hand-off rows (#world-unity-rows). mode ∈ {empty,loading,rows}.
pub const Unity = struct {
    mode: []const u8 = "",
    msg: []const u8 = "",
    writeLabel: []const u8 = "",
    rows: []const UnityRow = &.{},
};

/// State: the whole Worlds tab.
pub const State = struct {
    available: bool = false,
    title: []const u8 = "",
    sub: []const u8 = "",
    unavailable: []const u8 = "",
    linkHint: Hint = .{},
    secGitHub: []const u8 = "",
    gh: GitHub = .{},
    secLists: []const u8 = "",
    lists: Lists = .{},
    secPosters: []const u8 = "",
    posters: Posters = .{},
    secEvents: []const u8 = "",
    events: Events = .{},
    secNp: []const u8 = "",
    np: NowPlaying = .{},
    secUnity: []const u8 = "",
    unityHelp: []const u8 = "",
    unity: Unity = .{},
};

/// render mirrors Go worldsHTML (full tab).
pub fn render(h: *Html, s: State) !void {
    if (!s.available) {
        try c.panel(h, s.title, "");
        return c.emptyState(h, s.unavailable);
    }
    try c.panel(h, s.title, s.sub);
    try h.raw("<div id=world-linkhint>");
    try renderHint(h, s.linkHint);
    try h.raw("</div>");

    try c.sectionOpen(h, s.secGitHub);
    try h.raw("<div id=world-gh>");
    try renderGitHub(h, s.gh);
    try h.raw("</div>");
    try c.sectionClose(h);

    try c.sectionOpen(h, s.secLists);
    try renderLists(h, s.lists);
    try c.sectionClose(h);

    try c.sectionOpen(h, s.secPosters);
    try renderPosters(h, s.posters);
    try c.sectionClose(h);

    try c.sectionOpen(h, s.secEvents);
    try renderEvents(h, s.events);
    try c.sectionClose(h);

    try c.sectionOpen(h, s.secNp);
    try renderNowPlaying(h, s.np);
    try c.sectionClose(h);

    try c.sectionOpen(h, s.secUnity);
    try h.raw("<div class=\"rp-card\"><p class=ws-help>");
    try h.raw(s.unityHelp);
    try h.raw("</p><div id=world-unity-rows>"); // stable id: the async inspect cache re-patches it
    try renderUnityRows(h, s.unity);
    try h.raw("</div></div>");
    try c.sectionClose(h);
}

/// renderHint mirrors Go wsHintHTML (#world-linkhint).
pub fn renderHint(h: *Html, s: Hint) !void {
    try c.hint(h, s.tone, s.text);
}

/// renderGitHub mirrors Go wsGitHubHTML (#world-gh).
pub fn renderGitHub(h: *Html, s: GitHub) !void {
    if (eq(s.mode, "unavailable")) {
        try c.cardOpen(h, "", false);
        try c.hint(h, "bad", s.msg);
        return c.cardClose(h);
    }
    if (eq(s.mode, "linked")) {
        try c.cardOpen(h, "", true); // empty title + non-empty trailing ⇒ card head
        try c.btnRowOpen(h);
        try c.btn(h, s.unlinkLabel, "outline", "world-gh-unlink", "");
        try c.btnRowClose(h);
        try c.cardHeadClose(h);
        try c.kv(h, s.linkedLabel, s.linkedDl, s.login);
        try h.raw("<p class=ws-help>");
        try h.raw(s.linkedHelp);
        try h.raw("</p>");
        return c.cardClose(h);
    }
    try c.cardOpen(h, "", false);
    try c.hint(h, "warn", s.msg);
    try h.raw("<p class=ws-help>");
    try h.raw(s.unlinkedHelp);
    try h.raw("</p>");
    try c.btnRowOpen(h);
    try c.btn(h, s.deviceLabel, "primary", "world-gh-device", "");
    try c.btn(h, s.patLabel, "outline", "world-gh-pat", "");
    try c.btnRowClose(h);
    try c.cardClose(h);
}

/// statusRow wraps a target's status under its stable id (Go wsStatusRow; key raw, Go parity).
fn statusRow(h: *Html, key: []const u8, s: Status) !void {
    try h.raw("<div class=wsst id=\"world-st-");
    try h.raw(key);
    try h.raw("\">");
    try renderStatus(h, s);
    try h.raw("</div>");
}

/// renderStatus mirrors Go wsStatusHTML (#world-st-<key> inner).
pub fn renderStatus(h: *Html, s: Status) !void {
    try h.raw("<div class=wsst-line>");
    try c.hint(h, s.tone, s.line);
    try h.raw("</div>");
    if (s.url.len == 0) return;
    try h.raw("<div class=wsst-url>");
    try h.esc(s.url);
    try h.raw("</div>");
    try c.btnRowOpen(h);
    try c.btn(h, s.copyLabel, "ghost", "copy", s.url);
    if (s.htmlUrl.len != 0) try c.btn(h, s.openLabel, "outline", "open-url", s.htmlUrl);
    try c.btnRowClose(h);
}

fn renderLists(h: *Html, s: Lists) !void {
    try h.raw("<div class=\"rp-card\"><p class=ws-help>");
    try h.raw(s.help);
    try h.raw("</p>");
    if (s.empty.len != 0) try c.emptyState(h, s.empty);
    for (s.rows) |l| {
        try h.raw("<div class=ws-listrow>");
        try c.itemRowOpen(h, l.name, l.entries);
        try c.btnRowOpen(h);
        try c.btn(h, s.editLabel, "outline", l.editAct, "");
        try c.btn(h, s.pubLabel, "explore", l.pubAct, "");
        try c.btn(h, s.delLabel, "destructive", l.delAct, "");
        try c.btnRowClose(h);
        try c.itemRowClose(h);
        try statusRow(h, l.key, l.status);
        try h.raw("</div>");
    }
    try h.raw("<form class=ws-addrow data-act=world-list-add><input class=field-input name=name placeholder=\"");
    try h.raw(s.addPlaceholder);
    try h.raw("\" autocomplete=off><button class=\"rp-btn rp-btn--primary\" type=submit>");
    try h.raw(s.addLabel);
    try h.raw("</button></form></div>");
}

fn renderPosters(h: *Html, s: Posters) !void {
    try h.raw("<div class=\"rp-card\"><div class=card-head><span class=card-h>");
    try h.raw(s.cardTitle);
    try h.raw("</span><span class=card-trail>");
    try c.btnRowOpen(h);
    try c.btn(h, s.addLabel, "outline", "world-poster-add", "");
    try c.btn(h, s.pubLabel, "explore", "ws-pub-posters", "");
    try c.btnRowClose(h);
    try h.raw("</span></div>");
    try c.toggleRow(h, s.toggleLabel, s.toggleDl, "world-posters-on", s.toggleOn);
    try h.raw("<p class=ws-help>");
    try h.raw(s.help);
    try h.raw("</p>");
    if (s.empty.len != 0) try c.emptyState(h, s.empty);
    for (s.rows) |p| {
        try c.itemRowOpen(h, p.title, p.sub);
        try c.btnRowOpen(h);
        try c.btn(h, s.editLabel, "outline", p.editAct, "");
        try c.btn(h, s.delLabel, "destructive", p.delAct, "");
        try c.btnRowClose(h);
        try c.itemRowClose(h);
    }
    try statusRow(h, "posters", s.status);
    try h.raw("</div>");
}

fn renderEvents(h: *Html, s: Events) !void {
    try h.raw("<div class=\"rp-card\"><div class=card-head><span class=card-h>");
    try h.raw(s.cardTitle);
    try h.raw("</span><span class=card-trail>");
    try c.btn(h, s.pubLabel, "explore", "ws-pub-events", "");
    try h.raw("</span></div>");
    try c.toggleRow(h, s.toggleLabel, s.toggleDl, "world-events-on", s.toggleOn);
    try h.raw("<p class=ws-help>");
    try h.raw(s.help);
    try h.raw("</p>");
    try statusRow(h, "events", s.status);
    try h.raw("</div>");
}

fn renderNowPlaying(h: *Html, s: NowPlaying) !void {
    try h.raw("<div class=\"rp-card\"><div class=card-head><span class=card-h>");
    try h.raw(s.cardTitle);
    try h.raw("</span><span class=card-trail>");
    try c.btn(h, s.pubLabel, "explore", "ws-pub-nowplaying", "");
    try h.raw("</span></div>");
    try c.toggleRow(h, s.toggleLabel, s.toggleDl, "world-np-on", s.toggleOn);
    try c.fieldEx(h, s.linkLabel, s.linkDl, "world-np-link", s.link, "text", "", "");
    try c.fieldEx(h, s.imgLabel, s.imgDl, "world-np-img", s.img, "text", "", "");
    if (s.imgWarn.len != 0) {
        try h.raw("<div class=wsst-line>");
        try c.hint(h, "bad", s.imgWarn);
        try h.raw("</div>");
    }
    try h.raw("<p class=ws-help>");
    try h.raw(s.help);
    try h.raw("</p>");
    try statusRow(h, "nowplaying", s.status);
    try h.raw("</div>");
}

/// renderUnityRows mirrors Go wsUnityRowsHTML (#world-unity-rows).
pub fn renderUnityRows(h: *Html, s: Unity) !void {
    if (!eq(s.mode, "rows")) return c.emptyState(h, s.msg); // none configured / none valid / loading
    for (s.rows) |r| {
        try c.itemRowOpen(h, r.name, r.dir);
        try c.btn(h, s.writeLabel, "explore", r.act, "");
        try c.itemRowClose(h);
    }
}

test "unavailable tab" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .title = "Worlds", .sub = "s", .unavailable = "World Sync unavailable" });
    try std.testing.expectEqualStrings("<h1 class=page-title>Worlds</h1>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">World Sync unavailable</div></div>", h.b.items);
}

test "github linked keeps help prose unescaped" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderGitHub(&h, .{ .mode = "linked", .linkedLabel = "Linked as", .linkedDl = "linked as", .login = "dymattic", .linkedHelp = "Token sealed (gist scope). Writes to your gists.", .unlinkLabel = "Unlink" });
    try std.testing.expectEqualStrings("<div class=\"rp-card\"><div class=card-head><span class=card-h></span>" ++
        "<span class=card-trail><div class=btn-row><button class=\"rp-btn rp-btn--outline\" data-act=\"world-gh-unlink\">Unlink</button></div>" ++
        "</span></div><div class=kv><span class=kv-k>Linked as</span>" ++
        "<span class=kv-v data-label=\"linked as\" data-value=\"dymattic\">dymattic</span></div>" ++
        "<p class=ws-help>Token sealed (gist scope). Writes to your gists.</p></div>", h.b.items);
}

test "status without url renders only the line" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderStatus(&h, .{ .tone = "info", .line = "Not published yet." });
    try std.testing.expectEqualStrings("<div class=wsst-line><span class=\"hint hint--info\">Not published yet.</span></div>", h.b.items);
}

test "unity rows" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const rows = [_]UnityRow{.{ .name = "MyWorld", .dir = "C:\\u\\MyWorld", .act = "world-unity-write:0" }};
    try renderUnityRows(&h, .{ .mode = "rows", .writeLabel = "Write source URLs", .rows = &rows });
    try std.testing.expectEqualStrings("<div class=irow><div class=irow-main><div class=irow-title>MyWorld</div>" ++
        "<div class=irow-sub>C:\\u\\MyWorld</div></div><div class=irow-actions>" ++
        "<button class=\"rp-btn rp-btn--explore\" data-act=\"world-unity-write:0\">Write source URLs</button></div></div>", h.b.items);
}
