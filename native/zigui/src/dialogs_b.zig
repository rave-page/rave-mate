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
