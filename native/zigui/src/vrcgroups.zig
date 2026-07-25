//! VRChat ▸ Groups sub-tab renderer — byte-exact port of internal/webui/render_vrchat_groups.go
//! (vrcgBodyHTML + view renderers). State arrives fully resolved from Go (session, locks, perms,
//! i18n, timestamps, paging); this file only walks state → markup.
//! Golden gate: internal/webui/zigui_golden_vrchat_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

fn eq(a: []const u8, b: []const u8) bool {
    return std.mem.eql(u8, a, b);
}

pub const Badge = struct {
    text: []const u8 = "",
    variant: []const u8 = "",
};

pub const Btn = struct {
    label: []const u8 = "",
    variant: []const u8 = "",
    act: []const u8 = "",
};

pub const KV = struct {
    label: []const u8 = "",
    dl: []const u8 = "",
    value: []const u8 = "",
};

/// Pager: resolved Load-more / cap footer. mode ∈ {"",loading,more,cap}.
pub const Pager = struct {
    mode: []const u8 = "",
    msg: []const u8 = "",
    label: []const u8 = "",
    act: []const u8 = "",
};

pub const PickerRow = struct {
    idx: i64 = 0,
    name: []const u8 = "",
    meta: []const u8 = "",
};

/// Picker: "my groups" list. state ∈ {loading,none,nomatch,rows}.
pub const Picker = struct {
    title: []const u8 = "",
    refresh: []const u8 = "",
    filter: []const u8 = "",
    state: []const u8 = "",
    msg: []const u8 = "",
    rows: []const PickerRow = &.{},
};

pub const Role = struct {
    name: []const u8 = "",
    tags: []const Badge = &.{},
    order: []const u8 = "",
    desc: []const u8 = "",
    permSum: []const u8 = "",
    perms: []const []const u8 = &.{},
};

pub const Overview = struct {
    cardTitle: []const u8 = "",
    loading: bool = false,
    loadingMsg: []const u8 = "",
    missing: bool = false,
    missingMsg: []const u8 = "",
    aboutTitle: []const u8 = "",
    desc: []const u8 = "",
    kvs: []const KV = &.{},
    rulesTitle: []const u8 = "",
    rules: []const u8 = "",
    permsTitle: []const u8 = "",
    permsMode: []const u8 = "",
    permsMsg: []const u8 = "",
    permBadges: []const Badge = &.{},
    rolesTitle: []const u8 = "",
    rolesEmpty: []const u8 = "",
    roles: []const Role = &.{},
};

pub const MemberRow = struct {
    name: []const u8 = "",
    tags: []const Badge = &.{},
    meta: []const u8 = "",
    acts: []const Btn = &.{},
};

pub const Members = struct {
    cardTitle: []const u8 = "",
    state: []const u8 = "",
    msg: []const u8 = "",
    rows: []const MemberRow = &.{},
    pager: Pager = .{},
};

pub const UserRow = struct {
    name: []const u8 = "",
    sub: []const u8 = "",
    acts: []const Btn = &.{},
};

pub const Users = struct {
    cardTitle: []const u8 = "",
    head: []const Btn = &.{},
    state: []const u8 = "",
    msg: []const u8 = "",
    empty: []const u8 = "",
    rows: []const UserRow = &.{},
    pager: Pager = .{},
};

pub const PostRow = struct {
    title: []const u8 = "",
    meta: []const u8 = "",
    text: []const u8 = "",
    del: []const Btn = &.{},
};

pub const Posts = struct {
    annTitle: []const u8 = "",
    annTip: []const u8 = "", // legacy raw (bridge)
    annTipSt: ?c.Tip = null, // structured tooltip — wins over annTip
    hasAnn: bool = false,
    annHead: []const u8 = "",
    annWhen: []const u8 = "",
    annText: []const u8 = "",
    annEmpty: bool = false,
    annEmptyMsg: []const u8 = "",
    canAnn: bool = false,
    newAnnTitle: []const u8 = "",
    newPostTitle: []const u8 = "",
    fTitle: []const u8 = "",
    fText: []const u8 = "",
    fImage: []const u8 = "",
    fNotify: []const u8 = "",
    annSubmit: []const u8 = "",
    annHint: []const u8 = "",
    postSubmit: []const u8 = "",
    postHint: []const u8 = "",
    cardTitle: []const u8 = "",
    state: []const u8 = "",
    msg: []const u8 = "",
    empty: []const u8 = "",
    rows: []const PostRow = &.{},
    pager: Pager = .{},
};

pub const AuditRow = struct {
    when: []const u8 = "",
    event: []const u8 = "",
    actor: []const u8 = "",
    desc: []const u8 = "",
    raw: []const u8 = "",
};

pub const Audit = struct {
    cardTitle: []const u8 = "",
    noPerm: bool = false,
    noPermMsg: []const u8 = "",
    state: []const u8 = "",
    msg: []const u8 = "",
    empty: []const u8 = "",
    rawSummary: []const u8 = "",
    rows: []const AuditRow = &.{},
    pager: Pager = .{},
};

pub const Workspace = struct {
    title: []const u8 = "",
    refresh: []const u8 = "",
    back: []const u8 = "",
    badges: []const Badge = &.{},
    view: []const u8 = "",
    tabs: []const c.Tab = &.{},
    overview: Overview = .{},
    members: Members = .{},
    users: Users = .{},
    posts: Posts = .{},
    audit: Audit = .{},
};

/// State: the Groups sub-tab root (#vrcg-body). mode ∈ {picker,workspace}.
pub const State = struct {
    available: bool = false,
    unavailable: []const u8 = "",
    signedIn: bool = false,
    signInTitle: []const u8 = "",
    signInHint: []const u8 = "",
    mode: []const u8 = "",
    picker: Picker = .{},
    ws: Workspace = .{},
};

/// render mirrors Go vrcgBodyHTML.
pub fn render(h: *Html, s: State) !void {
    if (!s.available) return c.emptyState(h, s.unavailable);
    if (!s.signedIn) {
        try c.cardOpen(h, s.signInTitle, s.signInTitle.len != 0);
        if (s.signInTitle.len != 0) try c.cardHeadClose(h);
        try c.hint(h, "info", s.signInHint);
        return c.cardClose(h);
    }
    if (eq(s.mode, "picker")) return renderPicker(h, s.picker);
    return renderWorkspace(h, s.ws);
}

fn renderPicker(h: *Html, p: Picker) !void {
    try c.cardOpen(h, p.title, true);
    try c.btn(h, p.refresh, "ghost", "vrcg-refresh-groups", "");
    try c.cardHeadClose(h);
    try h.raw("<form data-act=vrcg-filter><input class=field-input name=q value=\"");
    try h.esc(p.filter);
    try h.raw("\" placeholder=\"Filter my groups… (Enter)\"></form>");
    if (eq(p.state, "loading")) {
        try c.hint(h, "info", p.msg);
    } else if (eq(p.state, "none") or eq(p.state, "nomatch")) {
        try c.emptyState(h, p.msg);
    } else {
        try h.raw("<div class=vrc-glist>");
        for (p.rows) |g| {
            try h.raw("<button class=vrc-glist-item data-act=\"vrcg-open:");
            try c.num(h, g.idx);
            try h.raw("\"><span>");
            try h.esc(g.name);
            try h.raw("</span><span class=vrc-gcount>");
            try h.esc(g.meta);
            try h.raw("</span></button>");
        }
        try h.raw("</div>");
    }
    try c.cardClose(h);
}

fn renderWorkspace(h: *Html, ws: Workspace) !void {
    try h.raw("<div class=\"rp-card vrcg-head\"><div class=vrcg-head-top><div class=vrcg-title>");
    try h.esc(ws.title);
    try h.raw("</div>");
    try c.btnRowOpen(h);
    try c.btn(h, ws.refresh, "ghost", "vrcg-reload", "");
    try c.btn(h, ws.back, "outline", "vrcg-back", "");
    try c.btnRowClose(h);
    try h.raw("</div>");
    if (ws.badges.len != 0) {
        try h.raw("<div class=vrcg-badges>");
        for (ws.badges) |x| try c.badge(h, x.text, x.variant);
        try h.raw("</div>");
    }
    try c.subTabs(h, "vrcg-view:", ws.view, ws.tabs);
    try h.raw("</div>");

    if (eq(ws.view, "members")) {
        try renderMembers(h, ws.members);
    } else if (eq(ws.view, "requests") or eq(ws.view, "invites") or eq(ws.view, "bans")) {
        try renderUsers(h, ws.users);
    } else if (eq(ws.view, "posts")) {
        try renderPosts(h, ws.posts);
    } else if (eq(ws.view, "audit")) {
        try renderAudit(h, ws.audit);
    } else {
        try renderOverview(h, ws.overview);
    }
}

fn renderOverview(h: *Html, ov: Overview) !void {
    if (ov.loading or ov.missing) {
        try c.cardOpen(h, ov.cardTitle, ov.cardTitle.len != 0);
        if (ov.cardTitle.len != 0) try c.cardHeadClose(h);
        try c.hint(h, if (ov.loading) "info" else "warn", if (ov.loading) ov.loadingMsg else ov.missingMsg);
        return c.cardClose(h);
    }

    // about
    try c.cardOpen(h, ov.aboutTitle, ov.aboutTitle.len != 0);
    if (ov.aboutTitle.len != 0) try c.cardHeadClose(h);
    if (ov.desc.len != 0) {
        try h.raw("<div class=vrcg-desc>");
        try h.esc(ov.desc);
        try h.raw("</div>");
    }
    for (ov.kvs) |r| try c.kv(h, r.label, r.dl, r.value);
    if (ov.rules.len != 0) {
        try h.raw("<details class=vrcg-det><summary>");
        try h.esc(ov.rulesTitle);
        try h.raw("</summary><div class=vrcg-desc>");
        try h.esc(ov.rules);
        try h.raw("</div></details>");
    }
    try c.cardClose(h);

    // my permissions
    try c.cardOpen(h, ov.permsTitle, ov.permsTitle.len != 0);
    if (ov.permsTitle.len != 0) try c.cardHeadClose(h);
    if (eq(ov.permsMode, "owner")) {
        try c.badge(h, ov.permsMsg, "success");
    } else if (eq(ov.permsMode, "none")) {
        try c.hint(h, "info", ov.permsMsg);
    } else {
        try h.raw("<div class=vrcg-badges>");
        for (ov.permBadges) |x| try c.badge(h, x.text, x.variant);
        try h.raw("</div>");
    }
    try c.cardClose(h);

    // roles
    try c.cardOpen(h, ov.rolesTitle, ov.rolesTitle.len != 0);
    if (ov.rolesTitle.len != 0) try c.cardHeadClose(h);
    if (ov.roles.len == 0) try c.emptyState(h, ov.rolesEmpty);
    for (ov.roles) |r| {
        try h.raw("<div class=vrcg-rolerow><div class=vrcg-mname><b>");
        try h.esc(r.name);
        try h.raw("</b>");
        for (r.tags) |t| try c.badge(h, t.text, t.variant);
        try h.raw("<span class=vrcg-count>");
        try h.esc(r.order);
        try h.raw("</span></div>");
        if (r.desc.len != 0) {
            try h.raw("<div class=vrcg-mmeta>");
            try h.esc(r.desc);
            try h.raw("</div>");
        }
        if (r.permSum.len != 0) {
            try h.raw("<details class=vrcg-det><summary>");
            try h.esc(r.permSum);
            try h.raw("</summary><div class=vrcg-badges>");
            for (r.perms) |p| try c.badge(h, p, "secondary");
            try h.raw("</div></details>");
        }
        try h.raw("</div>");
    }
    try c.cardClose(h);
}

fn renderMembers(h: *Html, ms: Members) !void {
    try c.cardOpen(h, ms.cardTitle, ms.cardTitle.len != 0);
    if (ms.cardTitle.len != 0) try c.cardHeadClose(h);
    if (eq(ms.state, "loading")) {
        try c.hint(h, "info", ms.msg);
    } else if (eq(ms.state, "notloaded")) {
        try c.hint(h, "warn", ms.msg);
    } else if (eq(ms.state, "empty")) {
        try c.emptyState(h, ms.msg);
    } else {
        for (ms.rows) |m| {
            try h.raw("<div class=vrcg-mrow><div class=vrcg-mmain><div class=vrcg-mname>");
            try h.esc(m.name);
            for (m.tags) |t| try c.badge(h, t.text, t.variant);
            try h.raw("</div><div class=vrcg-mmeta>");
            try h.esc(m.meta);
            try h.raw("</div></div><div class=vrcg-macts>");
            for (m.acts) |a| try c.btn(h, a.label, a.variant, a.act, "");
            try h.raw("</div></div>");
        }
    }
    try pager(h, ms.pager);
    try c.cardClose(h);
}

fn renderUsers(h: *Html, us: Users) !void {
    try c.cardOpen(h, us.cardTitle, us.cardTitle.len != 0);
    if (us.cardTitle.len != 0) try c.cardHeadClose(h);
    if (us.head.len != 0) {
        try c.btnRowOpen(h);
        for (us.head) |x| try c.btn(h, x.label, x.variant, x.act, "");
        try c.btnRowClose(h);
    }
    if (eq(us.state, "loading")) {
        try c.hint(h, "info", us.msg);
    } else if (eq(us.state, "notloaded")) {
        try c.hint(h, "warn", us.msg);
    } else if (eq(us.state, "empty")) {
        try c.emptyState(h, us.empty);
    } else {
        for (us.rows) |r| {
            try c.itemRowOpen(h, r.name, r.sub);
            for (r.acts) |a| try c.btn(h, a.label, a.variant, a.act, "");
            try c.itemRowClose(h);
        }
    }
    try pager(h, us.pager);
    try c.cardClose(h);
}

fn renderPosts(h: *Html, ps: Posts) !void {
    if (ps.hasAnn or ps.annEmpty) {
        // head follows Go card(): shown when the title OR the RESOLVED tooltip is non-empty,
        // so it must be decided on the rendered card, not on the raw bridge string.
        var tb = try c.tipBuf(h, ps.annTipSt, ps.annTip);
        defer tb.deinit();
        const head = ps.annTitle.len != 0 or tb.b.items.len != 0;
        try c.cardOpen(h, ps.annTitle, head);
        if (head) {
            try h.raw(tb.b.items);
            try c.cardHeadClose(h);
        }
        if (ps.hasAnn) {
            try h.raw("<div class=vrcg-post><div class=vrcg-mname><b>");
            try h.esc(ps.annHead);
            try h.raw("</b></div><div class=vrcg-mmeta>");
            try h.esc(ps.annWhen);
            try h.raw("</div><div class=vrcg-post-text>");
            try h.esc(ps.annText);
            try h.raw("</div></div>");
        } else {
            try c.emptyState(h, ps.annEmptyMsg);
        }
        try c.cardClose(h);
    }

    if (ps.canAnn) {
        try c.cardOpen(h, ps.newAnnTitle, ps.newAnnTitle.len != 0);
        if (ps.newAnnTitle.len != 0) try c.cardHeadClose(h);
        try h.raw("<form data-act=vrcg-ann><label class=field data-label=ann-title><span class=field-label>");
        try h.esc(ps.fTitle);
        try h.raw("</span><input class=field-input name=title maxlength=100></label>" ++
            "<label class=field data-label=ann-text><span class=field-label>");
        try h.esc(ps.fText);
        try h.raw("</span><textarea class=field-input name=text rows=3 maxlength=5000></textarea></label>" ++
            "<label class=field data-label=ann-imageid><span class=field-label>");
        try h.esc(ps.fImage);
        try h.raw("</span><input class=field-input name=imageid placeholder=\"file_…\"></label>" ++
            "<label class=row><span class=row-label>");
        try h.esc(ps.fNotify);
        try h.raw("</span><span class=switch><input type=checkbox name=notify value=1><span class=switch-track></span></span></label>" ++
            "<button class=\"rp-btn rp-btn--go\" type=submit>");
        try h.esc(ps.annSubmit);
        try h.raw("</button></form>");
        try c.hint(h, "info", ps.annHint);
        try c.cardClose(h);

        try c.cardOpen(h, ps.newPostTitle, ps.newPostTitle.len != 0);
        if (ps.newPostTitle.len != 0) try c.cardHeadClose(h);
        try h.raw("<form data-act=vrcg-post><label class=field data-label=post-title><span class=field-label>");
        try h.esc(ps.fTitle);
        try h.raw("</span><input class=field-input name=title maxlength=100></label>" ++
            "<label class=field data-label=post-text><span class=field-label>");
        try h.esc(ps.fText);
        try h.raw("</span><textarea class=field-input name=text rows=3 maxlength=5000></textarea></label>" ++
            "<label class=row><span class=row-label>");
        try h.esc(ps.fNotify);
        try h.raw("</span><span class=switch><input type=checkbox name=notify value=1><span class=switch-track></span></span></label>" ++
            "<button class=\"rp-btn rp-btn--go\" type=submit>");
        try h.esc(ps.postSubmit);
        try h.raw("</button></form>");
        try c.hint(h, "info", ps.postHint);
        try c.cardClose(h);
    }

    try c.cardOpen(h, ps.cardTitle, ps.cardTitle.len != 0);
    if (ps.cardTitle.len != 0) try c.cardHeadClose(h);
    if (eq(ps.state, "loading")) {
        try c.hint(h, "info", ps.msg);
    } else if (eq(ps.state, "notloaded")) {
        try c.hint(h, "warn", ps.msg);
    } else if (eq(ps.state, "empty")) {
        try c.emptyState(h, ps.empty);
    } else {
        for (ps.rows) |p| {
            try h.raw("<div class=vrcg-post><div class=vrcg-mname><b>");
            try h.esc(p.title);
            try h.raw("</b></div><div class=vrcg-mmeta>");
            try h.esc(p.meta);
            try h.raw("</div><div class=vrcg-post-text>");
            try h.esc(p.text);
            try h.raw("</div>");
            if (p.del.len != 0) {
                try h.raw("<div class=vrcg-post-actions>");
                for (p.del) |d| try c.btn(h, d.label, d.variant, d.act, "");
                try h.raw("</div>");
            }
            try h.raw("</div>");
        }
    }
    try pager(h, ps.pager);
    try c.cardClose(h);
}

fn renderAudit(h: *Html, as: Audit) !void {
    try c.cardOpen(h, as.cardTitle, as.cardTitle.len != 0);
    if (as.cardTitle.len != 0) try c.cardHeadClose(h);
    if (as.noPerm) try c.hint(h, "info", as.noPermMsg);
    if (eq(as.state, "loading")) {
        try c.hint(h, "info", as.msg);
    } else if (eq(as.state, "notloaded")) {
        try c.hint(h, "warn", as.msg);
    } else if (eq(as.state, "empty")) {
        try c.emptyState(h, as.empty);
    } else {
        for (as.rows) |a| {
            try h.raw("<div class=vrcg-arow><div class=vrcg-mname><span class=vrcg-atime>");
            try h.esc(a.when);
            try h.raw("</span>");
            try c.badge(h, a.event, "info");
            try h.raw("<span>");
            try h.esc(a.actor);
            try h.raw("</span></div>");
            if (a.desc.len != 0) {
                try h.raw("<div class=vrcg-mmeta>");
                try h.esc(a.desc);
                try h.raw("</div>");
            }
            try h.raw("<details class=vrcg-det><summary>");
            try h.esc(as.rawSummary);
            try h.raw("</summary><pre class=vrcg-json>");
            try h.esc(a.raw);
            try h.raw("</pre></details></div>");
        }
    }
    try pager(h, as.pager);
    try c.cardClose(h);
}

/// pager mirrors Go vgPagerHTML.
fn pager(h: *Html, p: Pager) !void {
    if (eq(p.mode, "loading")) {
        try h.raw("<div class=btn-row>");
        try c.hint(h, "info", p.msg);
        try h.raw("</div>");
    } else if (eq(p.mode, "more")) {
        try c.btnRowOpen(h);
        try c.btn(h, p.label, "outline", p.act, "");
        try c.btnRowClose(h);
    } else if (eq(p.mode, "cap")) {
        try h.raw("<div class=btn-row>");
        try c.hint(h, "warn", p.msg);
        try h.raw("</div>");
    }
}

test "unavailable + signed-out" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try render(&h, .{ .unavailable = "no vrchat" });
    try std.testing.expectEqualStrings("<div class=\"rp-empty\"><div class=\"rp-empty__title\">no vrchat</div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try render(&h, .{ .available = true, .signInTitle = "Groups", .signInHint = "sign in" });
    try std.testing.expectEqualStrings("<div class=\"rp-card\"><div class=card-head><span class=card-h>Groups</span>" ++
        "<span class=card-trail></span></div><span class=\"hint hint--info\">sign in</span></div>", h.b.items);
}

test "picker rows" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    const rows = [_]PickerRow{.{ .idx = 1, .name = "A&B", .meta = "abc · 3 members" }};
    try render(&h, .{ .available = true, .signedIn = true, .mode = "picker", .picker = .{
        .title = "My groups",
        .refresh = "Refresh",
        .state = "rows",
        .rows = &rows,
    } });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<button class=vrc-glist-item data-act=\"vrcg-open:1\"><span>A&amp;B</span><span class=vrc-gcount>abc · 3 members</span></button>") != null);
}

test "pager modes" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try pager(&h, .{ .mode = "more", .label = "Load more", .act = "vrcg-more:members" });
    try std.testing.expectEqualStrings("<div class=btn-row><button class=\"rp-btn rp-btn--outline\" data-act=\"vrcg-more:members\">Load more</button></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try pager(&h, .{ .mode = "cap", .msg = "Showing first 400." });
    try std.testing.expectEqualStrings("<div class=btn-row><span class=\"hint hint--warn\">Showing first 400.</span></div>", h.b.items);
}
