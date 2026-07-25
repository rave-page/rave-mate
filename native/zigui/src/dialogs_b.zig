//! Wave-4 dialog sweep B — the feature-tab DIALOG families, byte-identical to their pure Go
//! renderers (golden gate: internal/webui/zigui_golden_dialogs_b_test.go):
//!
//!   vg*  — VRChat ▸ Groups dialogs (render_vrchat_groups_modals.go): #vrcg-role-body,
//!          #vrcg-inv-list, the roles + invite shells, kick/ban and post-delete confirms.
//!   ws*  — Worlds dialogs (render_worlds_modals.go): list editor, poster editor, the
//!          friend/group pickers (+ their independently patched lists), role list, GitHub
//!          device-code dialog.
//!   ae/ar/as — Automations dialogs (render_automations_modals.go): the automation editor,
//!          the run-now dialog and the schedule editor.
//!
//! A modal renderer ENDS with the components.zig bracket triple (modalOpen → body →
//! modalFoot → footer → modalClose); a fragment renderer emits only its inner HTML.
//! Trusted raw fields are named in the state doc comments — every one of them replaces a Go
//! source literal that Go itself splices unescaped. Fixed sentence literals that never vary
//! live in BOTH renderers (these files carry no i18n keys, so there is nothing to resolve).

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");

// ══ VRChat ▸ Groups ══

/// RoleRow is one group role with its add/remove button. act is index-derived, spliced by btn.
pub const RoleRow = struct {
    label: []const u8 = "",
    desc: []const u8 = "",
    btnLabel: []const u8 = "",
    btnVar: []const u8 = "",
    act: []const u8 = "",
};

/// RoleBody is #vrcg-role-body: ONE hint or a row per role. hasHint is explicit.
pub const RoleBody = struct {
    hasHint: bool = false,
    hintTone: []const u8 = "",
    hintText: []const u8 = "",
    rows: []const RoleRow = &.{},
};

pub fn renderRoleBody(h: *Html, st: RoleBody) !void {
    if (st.hasHint) {
        try c.hint(h, st.hintTone, st.hintText);
        return;
    }
    for (st.rows) |r| {
        try c.itemRowOpen(h, r.label, r.desc);
        try c.btn(h, r.btnLabel, r.btnVar, r.act, "");
        try c.itemRowClose(h);
    }
}

/// InviteRow is one invitable friend.
pub const InviteRow = struct {
    name: []const u8 = "",
    status: []const u8 = "",
    act: []const u8 = "",
};

/// InviteList is #vrcg-inv-list. Exactly one of loading / empty / rows applies.
/// moreMsg is RAW (Go source literal).
pub const InviteList = struct {
    loading: bool = false,
    loadingMsg: []const u8 = "",
    empty: bool = false,
    emptyMsg: []const u8 = "",
    rows: []const InviteRow = &.{},
    hasMore: bool = false,
    moreMsg: []const u8 = "",
};

pub fn renderInviteList(h: *Html, st: InviteList) !void {
    if (st.loading) {
        try c.hint(h, "info", st.loadingMsg);
        return;
    }
    if (st.empty) {
        try c.emptyState(h, st.emptyMsg);
        return;
    }
    for (st.rows) |f| {
        try c.itemRowOpen(h, f.name, f.status);
        try c.btn(h, "Invite", "primary", f.act, "");
        try c.itemRowClose(h);
    }
    if (st.hasMore) {
        try h.raw("<div class=vrcg-count>");
        try h.raw(st.moreMsg);
        try h.raw("</div>");
    }
}

/// RolesModal is the roles dialog: title + the embedded #vrcg-role-body fragment.
pub const RolesModal = struct {
    title: []const u8 = "",
    body: RoleBody = .{},
};

pub fn renderRolesModal(h: *Html, st: RolesModal) !void {
    try c.modalOpen(h, st.title);
    try h.raw("<div id=vrcg-role-body>");
    try renderRoleBody(h, st.body);
    try h.raw("</div>");
    try c.modalFoot(h);
    try c.modalFootDefault(h);
    try c.modalClose(h);
}

/// InviteModal is the invite picker: filter form, the friends list, invite-by-id form.
pub const InviteModal = struct {
    title: []const u8 = "",
    searchPh: []const u8 = "",
    idPh: []const u8 = "",
    idBtn: []const u8 = "",
    list: InviteList = .{},
};

pub fn renderInviteModal(h: *Html, st: InviteModal) !void {
    try c.modalOpen(h, st.title);
    try h.raw("<form data-act=vrcg-inv-search><input class=field-input name=q placeholder=");
    try h.attrQ(st.searchPh);
    try h.raw("></form><div id=vrcg-inv-list>");
    try renderInviteList(h, st.list);
    try h.raw("</div><form data-act=vrcg-inv-id class=vrcg-invid>" ++
        "<input class=field-input name=userid placeholder=");
    try h.attrQ(st.idPh);
    try h.raw("><button class=\"rp-btn rp-btn--outline\" type=submit>");
    try h.esc(st.idBtn);
    try h.raw("</button></form>");
    try c.modalFoot(h);
    try c.modalFootDefault(h);
    try c.modalClose(h);
}

/// MemberConfirm is the kick/ban confirm. verb is a Go source literal spliced UNESCAPED into
/// the body sentence (btn escapes it for the button label, as Go does).
pub const MemberConfirm = struct {
    title: []const u8 = "",
    verb: []const u8 = "",
    name: []const u8 = "",
    group: []const u8 = "",
    note: []const u8 = "",
    act: []const u8 = "",
    cancel: []const u8 = "",
};

pub fn renderMemberConfirm(h: *Html, st: MemberConfirm) !void {
    try c.modalOpen(h, st.title);
    try h.raw("<p>");
    try h.raw(st.verb);
    try h.raw(" <b>");
    try h.esc(st.name);
    try h.raw("</b> from ");
    try h.esc(st.group);
    try h.raw("? ");
    try h.esc(st.note);
    try h.raw("</p>");
    try c.modalFoot(h);
    try c.btnRowOpen(h);
    try c.btn(h, st.verb, "destructive", st.act, "");
    try c.btn(h, st.cancel, "outline", "modal-close", "");
    try c.btnRowClose(h);
    try c.modalClose(h);
}

/// PostConfirm is the delete-post confirm.
pub const PostConfirm = struct {
    title: []const u8 = "",
    post: []const u8 = "",
    group: []const u8 = "",
    confirm: []const u8 = "",
    cancel: []const u8 = "",
};

pub fn renderPostConfirm(h: *Html, st: PostConfirm) !void {
    try c.modalOpen(h, st.title);
    try h.raw("<p>Delete post <b>");
    try h.esc(st.post);
    try h.raw("</b> from ");
    try h.esc(st.group);
    try h.raw("? This cannot be undone.</p>");
    try c.modalFoot(h);
    try c.btnRowOpen(h);
    try c.btn(h, st.confirm, "destructive", "vrcg-post-del-y", "");
    try c.btn(h, st.cancel, "outline", "modal-close", "");
    try c.btnRowClose(h);
    try c.modalClose(h);
}

test "vg role body: hint arm and role rows" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderRoleBody(&h, .{ .hasHint = true, .hintTone = "warn", .hintText = "Member list changed - close and reopen." });
    try std.testing.expectEqualStrings("<span class=\"hint hint--warn\">Member list changed - close and reopen.</span>", h.b.items);
    h.b.clearRetainingCapacity();
    try renderRoleBody(&h, .{ .rows = &.{.{ .label = "Mod & <crew>", .desc = "d", .btnLabel = "Remove", .btnVar = "warn", .act = "vrcg-role-del:0" }} });
    try std.testing.expectEqualStrings("<div class=irow><div class=irow-main><div class=irow-title>Mod &amp; &lt;crew&gt;</div>" ++
        "<div class=irow-sub>d</div></div><div class=irow-actions>" ++
        "<button class=\"rp-btn rp-btn--warn\" data-act=\"vrcg-role-del:0\">Remove</button></div></div>", h.b.items);
}

test "vg invite list: overflow marker is raw" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderInviteList(&h, .{ .rows = &.{.{ .name = "A", .status = "online", .act = "vrcg-inv-pick:0" }}, .hasMore = true, .moreMsg = "…more matches - filter to narrow down" });
    try std.testing.expect(std.mem.endsWith(u8, h.b.items, "<div class=vrcg-count>…more matches - filter to narrow down</div>"));
}

test "vg post confirm: fixed sentence + destructive footer" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderPostConfirm(&h, .{ .title = "Delete post", .post = "P&Q", .group = "G", .confirm = "Delete post", .cancel = "Cancel" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "<p>Delete post <b>P&amp;Q</b> from G? This cannot be undone.</p>") != null);
}

// ══ Worlds ══
// Prose fields (ws-help paragraphs, form placeholders, submit labels) are RAW trusted literals,
// exactly as render_worlds_modals.go splices them - they carry apostrophes, so escaping them
// would change the DOM.

/// wsHelp emits one <p class=ws-help> paragraph with RAW (trusted-literal) content.
fn wsHelp(h: *Html, text: []const u8) !void {
    try h.raw("<p class=ws-help>");
    try h.raw(text);
    try h.raw("</p>");
}

/// WsEntryRow is one granted permission entry (friend name or group role).
pub const WsEntryRow = struct {
    label: []const u8 = "",
    act: []const u8 = "",
};

/// WsListEditor is the per-list entry editor dialog.
pub const WsListEditor = struct {
    title: []const u8 = "",
    help: []const u8 = "",
    empty: bool = false,
    emptyMsg: []const u8 = "",
    entries: []const WsEntryRow = &.{},
    delLabel: []const u8 = "",
    addPh: []const u8 = "",
    addBtn: []const u8 = "",
    friendBtn: []const u8 = "",
    friendAct: []const u8 = "",
    groupBtn: []const u8 = "",
    groupAct: []const u8 = "",
};

pub fn renderWsListEditor(h: *Html, st: WsListEditor) !void {
    try c.modalOpen(h, st.title);
    try wsHelp(h, st.help);
    try h.raw("<div class=ws-entries>");
    if (st.empty) try c.emptyState(h, st.emptyMsg);
    for (st.entries) |e| {
        try c.itemRowOpen(h, e.label, "");
        try c.btn(h, st.delLabel, "destructive", e.act, "");
        try c.itemRowClose(h);
    }
    try h.raw("</div><form class=ws-addrow data-act=world-name-add>" ++
        "<input class=field-input name=name placeholder=");
    try h.attrQ(st.addPh);
    try h.raw(" autocomplete=off><button class=\"rp-btn rp-btn--outline\" type=submit>");
    try h.raw(st.addBtn);
    try h.raw("</button></form>");
    try c.btnRowOpen(h);
    try c.btn(h, st.friendBtn, "primary", st.friendAct, "");
    try c.btn(h, st.groupBtn, "outline", st.groupAct, "");
    try c.btnRowClose(h);
    try c.modalFoot(h);
    try c.modalFootDefault(h);
    try c.modalClose(h);
}

/// WsPosterEditor is the poster-slot editor form. idx is a pre-formatted integer.
pub const WsPosterEditor = struct {
    title: []const u8 = "",
    idx: []const u8 = "",
    imgLbl: []const u8 = "",
    img: []const u8 = "",
    imgPh: []const u8 = "",
    capLbl: []const u8 = "",
    caption: []const u8 = "",
    capPh: []const u8 = "",
    linkLbl: []const u8 = "",
    link: []const u8 = "",
    linkPh: []const u8 = "",
    hasWarn: bool = false,
    warn: []const u8 = "",
    save: []const u8 = "",
};

/// wsPosterField mirrors Go wsPosterField: a hand-rolled name=-carrying field (label +
/// placeholder RAW, value escaped) — components.go fieldEx does not fit (no data-act).
fn wsPosterField(h: *Html, label: []const u8, name: []const u8, value: []const u8, ph: []const u8) !void {
    try h.raw("<label class=field><span class=field-label>");
    try h.raw(label);
    try h.raw("</span><input class=field-input name=");
    try h.raw(name);
    try h.raw(" value=\"");
    try h.esc(value);
    try h.raw("\" placeholder=\"");
    try h.raw(ph);
    try h.raw("\" autocomplete=off></label>");
}

pub fn renderWsPosterEditor(h: *Html, st: WsPosterEditor) !void {
    try c.modalOpen(h, st.title);
    try h.raw("<form data-act=world-poster-save><input type=hidden name=idx value=\"");
    try h.raw(st.idx);
    try h.raw("\">");
    try wsPosterField(h, st.imgLbl, "img", st.img, st.imgPh);
    try wsPosterField(h, st.capLbl, "caption", st.caption, st.capPh);
    try wsPosterField(h, st.linkLbl, "link", st.link, st.linkPh);
    if (st.hasWarn) {
        try h.raw("<div class=wsst-line>");
        try c.hint(h, "bad", st.warn);
        try h.raw("</div>");
    }
    try h.raw("<div class=btn-row><button class=\"rp-btn rp-btn--primary\" type=submit>");
    try h.raw(st.save);
    try h.raw("</button></div></form>");
    try c.modalFoot(h);
    try c.modalFootDefault(h);
    try c.modalClose(h);
}

/// WsPickRow is one picker row (label + a single trailing action).
pub const WsPickRow = struct {
    label: []const u8 = "",
    act: []const u8 = "",
};

/// WsFriendList is #world-fr-list. loadingMsg / moreMsg are RAW ws-help literals.
pub const WsFriendList = struct {
    loading: bool = false,
    loadingMsg: []const u8 = "",
    rows: []const WsPickRow = &.{},
    addLabel: []const u8 = "",
    hasMore: bool = false,
    moreMsg: []const u8 = "",
    empty: bool = false,
    emptyMsg: []const u8 = "",
};

pub fn renderWsFriendList(h: *Html, st: WsFriendList) !void {
    if (st.loading) {
        try wsHelp(h, st.loadingMsg);
        return;
    }
    for (st.rows) |r| {
        try c.itemRowOpen(h, r.label, "");
        try c.btn(h, st.addLabel, "primary", r.act, "");
        try c.itemRowClose(h);
    }
    if (st.hasMore) try wsHelp(h, st.moreMsg);
    if (st.empty) try c.emptyState(h, st.emptyMsg);
}

/// WsFriendPicker is the friend-picker dialog shell around #world-fr-list.
pub const WsFriendPicker = struct {
    title: []const u8 = "",
    searchPh: []const u8 = "",
    backLbl: []const u8 = "",
    backAct: []const u8 = "",
    list: WsFriendList = .{},
};

pub fn renderWsFriendPicker(h: *Html, st: WsFriendPicker) !void {
    try c.modalOpen(h, st.title);
    try h.raw("<form class=ws-search data-act=world-fr-search><input class=field-input name=q placeholder=");
    try h.attrQ(st.searchPh);
    try h.raw(" autocomplete=off></form><div class=ws-picklist id=world-fr-list>");
    try renderWsFriendList(h, st.list);
    try h.raw("</div>");
    try c.modalFoot(h);
    try c.btn(h, st.backLbl, "outline", st.backAct, "");
    try c.modalClose(h);
}

/// WsGroupRow is one group row: label + pin/unpin + the roles button.
pub const WsGroupRow = struct {
    label: []const u8 = "",
    favLabel: []const u8 = "",
    favAct: []const u8 = "",
    rolesAct: []const u8 = "",
};

/// WsGroupSec is one captioned section of the group list.
pub const WsGroupSec = struct {
    caption: []const u8 = "",
    rows: []const WsGroupRow = &.{},
};

/// WsGroupList is #world-grp-list. loading is a LEADING paragraph, not a branch — whatever
/// sections already resolved still render underneath it (Go parity).
pub const WsGroupList = struct {
    loading: bool = false,
    loadingMsg: []const u8 = "",
    sections: []const WsGroupSec = &.{},
    rolesLabel: []const u8 = "",
    empty: bool = false,
    emptyMsg: []const u8 = "",
};

pub fn renderWsGroupList(h: *Html, st: WsGroupList) !void {
    if (st.loading) try wsHelp(h, st.loadingMsg);
    for (st.sections) |sec| {
        try h.raw("<div class=ws-caps>");
        try h.raw(sec.caption);
        try h.raw("</div>");
        for (sec.rows) |r| {
            try c.itemRowOpen(h, r.label, "");
            try c.btnRowOpen(h);
            try c.btn(h, r.favLabel, "ghost", r.favAct, "");
            try c.btn(h, st.rolesLabel, "primary", r.rolesAct, "");
            try c.btnRowClose(h);
            try c.itemRowClose(h);
        }
    }
    if (st.empty) try c.emptyState(h, st.emptyMsg);
}

/// WsGroupPicker is the group-picker dialog shell around #world-grp-list.
pub const WsGroupPicker = struct {
    title: []const u8 = "",
    searchPh: []const u8 = "",
    searchBtn: []const u8 = "",
    help: []const u8 = "",
    backLbl: []const u8 = "",
    backAct: []const u8 = "",
    list: WsGroupList = .{},
};

pub fn renderWsGroupPicker(h: *Html, st: WsGroupPicker) !void {
    try c.modalOpen(h, st.title);
    try h.raw("<form class=ws-search data-act=world-grp-search><input class=field-input name=q placeholder=");
    try h.attrQ(st.searchPh);
    try h.raw(" autocomplete=off><button class=\"rp-btn rp-btn--outline\" type=submit>");
    try h.raw(st.searchBtn);
    try h.raw("</button></form><div class=ws-picklist id=world-grp-list>");
    try renderWsGroupList(h, st.list);
    try h.raw("</div>");
    try wsHelp(h, st.help);
    try c.modalFoot(h);
    try c.btn(h, st.backLbl, "outline", st.backAct, "");
    try c.modalClose(h);
}

/// WsRoleList is #world-role-list: an "All members" row plus one row per group role.
pub const WsRoleList = struct {
    loading: bool = false,
    loadingMsg: []const u8 = "",
    allLabel: []const u8 = "",
    grantLabel: []const u8 = "",
    rows: []const WsPickRow = &.{},
};

pub fn renderWsRoleList(h: *Html, st: WsRoleList) !void {
    if (st.loading) {
        try wsHelp(h, st.loadingMsg);
        return;
    }
    try c.itemRowOpen(h, st.allLabel, "");
    try c.btn(h, st.grantLabel, "primary", "world-role-pick:all", "");
    try c.itemRowClose(h);
    for (st.rows) |r| {
        try c.itemRowOpen(h, r.label, "");
        try c.btn(h, st.grantLabel, "primary", r.act, "");
        try c.itemRowClose(h);
    }
}

/// WsRolePicker is the role-grant dialog shell around #world-role-list.
pub const WsRolePicker = struct {
    title: []const u8 = "",
    backLbl: []const u8 = "",
    backAct: []const u8 = "",
    list: WsRoleList = .{},
};

pub fn renderWsRolePicker(h: *Html, st: WsRolePicker) !void {
    try c.modalOpen(h, st.title);
    try h.raw("<div id=world-role-list>");
    try renderWsRoleList(h, st.list);
    try h.raw("</div>");
    try c.modalFoot(h);
    try c.btn(h, st.backLbl, "outline", st.backAct, "");
    try c.modalClose(h);
}

/// WsDevice is the GitHub device-code dialog. help is RAW; code/uri are escaped (the code also
/// rides as a data-val, which btn escapes).
pub const WsDevice = struct {
    title: []const u8 = "",
    help: []const u8 = "",
    code: []const u8 = "",
    copyLbl: []const u8 = "",
    openLbl: []const u8 = "",
    uri: []const u8 = "",
};

pub fn renderWsDevice(h: *Html, st: WsDevice) !void {
    try c.modalOpen(h, st.title);
    try wsHelp(h, st.help);
    try h.raw("<div class=ws-devcode>");
    try h.esc(st.code);
    try h.raw("</div>");
    try c.btnRowOpen(h);
    try c.btn(h, st.copyLbl, "ghost", "copy", st.code);
    try c.btn(h, st.openLbl, "outline", "open-url", st.uri);
    try c.btnRowClose(h);
    try c.modalFoot(h);
    try c.modalFootDefault(h);
    try c.modalClose(h);
}

test "ws friend list: loading and empty arms" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderWsFriendList(&h, .{ .empty = true, .emptyMsg = "No match" });
    try std.testing.expectEqualStrings("<div class=\"rp-empty\"><div class=\"rp-empty__title\">No match</div></div>", h.b.items);
    h.b.clearRetainingCapacity();
    try renderWsFriendList(&h, .{ .loading = true, .loadingMsg = "Loading friends…" });
    try std.testing.expectEqualStrings("<p class=ws-help>Loading friends…</p>", h.b.items);
}

test "ws poster field: label/placeholder raw, value escaped" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try wsPosterField(&h, "Image", "img", "https://x/?a=1&b=2", "https://i.imgur.com/…");
    try std.testing.expectEqualStrings("<label class=field><span class=field-label>Image</span>" ++
        "<input class=field-input name=img value=\"https://x/?a=1&amp;b=2\" " ++
        "placeholder=\"https://i.imgur.com/…\" autocomplete=off></label>", h.b.items);
}
