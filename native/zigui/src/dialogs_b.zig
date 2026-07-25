//! Wave-4 dialog sweep B — the feature-tab DIALOG families, byte-identical to their pure Go
//! renderers (golden gate: internal/webui/zigui_golden_dialogs_b_test.go):
//!
//!   vg*  — VRChat ▸ Groups dialogs (render_vrchat_groups_modals.go): #vrcg-role-body,
//!          #vrcg-inv-list, the roles + invite shells, kick/ban and post-delete confirms.
//!   ws*  — Worlds dialogs (render_worlds_modals.go): list editor, poster editor, the
//!          friend/group pickers (+ their independently patched lists), role list, GitHub
//!          device-code dialog.
//!   ae/ar/as — Automations dialogs (render_automations_{ed,run,sch}.go): the automation editor,
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

// ══ Automations ▸ editor ══
// The form body is a BLOCK LIST (settings-port shape): each block renders exactly one
// components.zig primitive, so layout cannot drift between the renderers. Depth is 1 by
// construction — a block carries at most two fields plus one button, so the JSON stays a plain
// tree. Tooltips (field, pbhint, and the selraw ss-label) cross as STRUCTURED state since phase
// B1b — components.zig renderTip; the `tip`/`labelHtml` strings stay as the dual-field bridge for
// the state automations_runnow.go shares and still ships pre-rendered. A step's `loud` block is
// the shared loudness override as STRUCTURED state too (components.zig loudnessFields, phase
// B-1a); only ITS own tip + extra stay raw. No `raw` block kind is left.

/// DlgField is webui dlgFieldSt: components.Field plus a structured tooltip. Local twin (rather
/// than a components.Field change) because Field is shared with every other migrated tab.
pub const DlgField = struct {
    label: []const u8 = "",
    dl: []const u8 = "",
    act: []const u8 = "",
    value: []const u8 = "",
    inputType: []const u8 = "",
    ph: []const u8 = "",
    tip: []const u8 = "", // legacy pre-rendered tooltip markup (bridge)
    tipSt: ?c.Tip = null, // structured tooltip — wins over tip
};

/// renderDlgField feeds components.fieldEx, which takes the tooltip as a STRING: the structured
/// card renders into a scratch buffer so fieldEx's markup stays single-sourced.
fn renderDlgField(h: *Html, f: DlgField) !void {
    var tb = Html.init(h.a);
    defer tb.deinit();
    try c.tipOr(&tb, f.tipSt, f.tip);
    try c.fieldEx(h, f.label, f.dl, f.act, f.value, f.inputType, f.ph, tb.b.items);
}

/// AeLabel is a selraw's ss-label as state (webui aeLabelSt). ALIAS of components.SsLabel, which
/// B-1b shard 2 lifted into the base kit for every select-with-tooltip surface - one markup source.
pub const AeLabel = c.SsLabel;

/// AeBlock is one form block. Only the fields its kind names are read.
/// kind ∈ field|fpair|toolbar|toggle|select|selraw|fpairsel|hint|pbhint|loud.
pub const AeBlock = struct {
    kind: []const u8 = "",
    field: DlgField = .{},
    field2: DlgField = .{},
    btn: c.Btn = .{},
    toggle: c.Toggle = .{},
    sel: c.Select = .{},
    sel2: c.Select = .{},
    labelHtml: []const u8 = "", // legacy pre-rendered ss-label (bridge)
    labelSt: ?AeLabel = null, // structured ss-label — wins over labelHtml
    tone: []const u8 = "",
    text: []const u8 = "",
    tip: []const u8 = "", // legacy pre-rendered tooltip markup (bridge)
    tipSt: ?c.Tip = null, // structured tooltip — wins over tip
    loud: c.Loud = .{}, // the shared loudness block, structured
};

pub fn renderAeBlock(h: *Html, b: AeBlock) !void {
    const k = b.kind;
    if (std.mem.eql(u8, k, "field")) {
        try renderDlgField(h, b.field);
    } else if (std.mem.eql(u8, k, "fpair")) {
        try c.fpairOpen(h);
        try renderDlgField(h, b.field);
        try renderDlgField(h, b.field2);
        try c.fpairClose(h);
    } else if (std.mem.eql(u8, k, "toolbar")) {
        try h.raw("<div class=lib-toolbar>");
        try renderDlgField(h, b.field);
        try c.btnOf(h, b.btn);
        try h.raw("</div>");
    } else if (std.mem.eql(u8, k, "toggle")) {
        try c.toggleOf(h, b.toggle);
    } else if (std.mem.eql(u8, k, "select")) {
        try c.selectBox(h, b.sel);
    } else if (std.mem.eql(u8, k, "selraw")) {
        if (b.labelSt) |l| {
            try c.selectBoxTipOf(h, b.sel, l);
        } else {
            try c.selectBoxRaw(h, b.sel, b.labelHtml);
        }
    } else if (std.mem.eql(u8, k, "fpairsel")) {
        try c.fpairOpen(h);
        try c.selectBox(h, b.sel);
        try c.selectBox(h, b.sel2);
        try c.fpairClose(h);
    } else if (std.mem.eql(u8, k, "hint")) {
        try c.hint(h, b.tone, b.text);
    } else if (std.mem.eql(u8, k, "pbhint")) {
        try h.raw("<div class=pb-hint>");
        try h.esc(b.text);
        try c.tipOr(h, b.tipSt, b.tip);
        try h.raw("</div>");
    } else if (std.mem.eql(u8, k, "loud")) {
        try c.loudnessFields(h, b.loud);
    }
}

fn renderAeBlocks(h: *Html, bs: []const AeBlock) !void {
    for (bs) |b| try renderAeBlock(h, b);
}

/// AeStep is one chain-step card: header (order + type label + reorder/remove) then its body.
pub const AeStep = struct {
    title: []const u8 = "",
    trail: []const c.Btn = &.{},
    desc: []const u8 = "",
    blocks: []const AeBlock = &.{},
};

fn renderAeStep(h: *Html, st: AeStep) !void {
    // Go card(title, trailing, body): the head shows whenever title OR trailing is non-empty —
    // a step always carries the remove button, so the head is unconditional here.
    try c.cardOpen(h, st.title, st.title.len != 0 or st.trail.len != 0);
    for (st.trail) |t| try c.btnOf(h, t);
    if (st.title.len != 0 or st.trail.len != 0) try c.cardHeadClose(h);
    try h.raw("<div class=np-artist>");
    try h.esc(st.desc);
    try h.raw("</div>");
    try renderAeBlocks(h, st.blocks);
    try c.cardClose(h);
}

/// AeModal is the whole automation-editor dialog.
pub const AeModal = struct {
    title: []const u8 = "",
    hasErr: bool = false,
    err: []const u8 = "",
    ident: []const AeBlock = &.{},
    secMatch: []const u8 = "",
    match: []const AeBlock = &.{},
    secActions: []const u8 = "",
    noSteps: bool = false,
    noStepsMsg: []const u8 = "",
    steps: []const AeStep = &.{},
    add: []const c.Btn = &.{},
    hasVerdict: bool = false,
    verdict: []const u8 = "",
    save: []const u8 = "",
    cancel: []const u8 = "",
};

pub fn renderAeModal(h: *Html, st: AeModal) !void {
    try c.modalOpen(h, st.title);
    if (st.hasErr) {
        try h.raw("<div class=ae-err>");
        try c.hint(h, "bad", st.err);
        try h.raw("</div>");
    }
    try renderAeBlocks(h, st.ident);
    try c.sectionOpen(h, st.secMatch);
    try renderAeBlocks(h, st.match);
    try c.sectionClose(h);
    try c.sectionOpen(h, st.secActions);
    if (st.noSteps) try c.emptyState(h, st.noStepsMsg);
    for (st.steps) |s| try renderAeStep(h, s);
    try c.btnRowOf(h, st.add);
    if (st.hasVerdict) try c.hint(h, "bad", st.verdict);
    try c.sectionClose(h);
    try c.modalFoot(h);
    try c.btnRowOpen(h);
    try c.btn(h, st.save, "primary", "auto-ed-save", "");
    try c.btn(h, st.cancel, "ghost", "modal-close", "");
    try c.btnRowClose(h);
    try c.modalClose(h);
}

test "ae block: toolbar wraps field + browse button" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderAeBlock(&h, .{ .kind = "toolbar", .field = .{ .label = "Watch folder", .dl = "watch folder", .act = "auto-ed:watch", .value = "D:\\in" }, .btn = .{ .label = "Browse", .variant = "ghost", .act = "pick-dir:auto-ed:watch" } });
    try std.testing.expect(std.mem.startsWith(u8, h.b.items, "<div class=lib-toolbar><label class=field"));
    try std.testing.expect(std.mem.endsWith(u8, h.b.items, "</button></div>"));
}

test "ae block: pbhint escapes text and raws the tip" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderAeBlock(&h, .{ .kind = "pbhint", .text = "Deletes the file & stops", .tip = "<span class=tip></span>" });
    try std.testing.expectEqualStrings("<div class=pb-hint>Deletes the file &amp; stops<span class=tip></span></div>", h.b.items);
}

// ══ Automations ▸ run now ══
// erases is explicit (Go autoChainDeletes) and gates BOTH the acknowledgement block and the
// footer wording. deleteTip is RAW tipTopic markup.

/// ArFoot is the resolved footer: gated = disabled Run button whose title names the missing
/// precondition (Go btnGated), else a live button in `variant`.
pub const ArFoot = struct {
    gated: bool = false,
    label: []const u8 = "",
    why: []const u8 = "",
    variant: []const u8 = "",
    cancel: []const u8 = "",
};

/// ArModal is the run-now dialog.
pub const ArModal = struct {
    title: []const u8 = "",
    hasErr: bool = false,
    err: []const u8 = "",
    auto: c.KV = .{},
    watch: c.KV = .{},
    chain: c.KV = .{},
    ignoresMatch: []const u8 = "",
    file: DlgField = .{}, // webui dlgFieldSt — carries the structured tooltip (B-1b)
    browse: c.Btn = .{},
    erases: bool = false,
    deleteWarn: []const u8 = "",
    deleteScope: []const u8 = "",
    deleteTip: []const u8 = "", // legacy raw (bridge)
    deleteTipSt: ?c.Tip = null, // structured tooltip — wins over deleteTip
    ack: c.Toggle = .{},
    foot: ArFoot = .{},
};

pub fn renderArModal(h: *Html, st: ArModal) !void {
    try c.modalOpen(h, st.title);
    if (st.hasErr) {
        try h.raw("<div class=ae-err>");
        try c.hint(h, "bad", st.err);
        try h.raw("</div>");
    }
    try c.kvOf(h, st.auto);
    try c.kvOf(h, st.watch);
    try c.kvOf(h, st.chain);
    try c.hint(h, "info", st.ignoresMatch);
    try h.raw("<div class=lib-toolbar>");
    try renderDlgField(h, st.file);
    try c.btnOf(h, st.browse);
    try h.raw("</div>");
    if (st.erases) {
        try c.hint(h, "bad", st.deleteWarn);
        try h.raw("<div class=pb-hint>");
        try h.esc(st.deleteScope);
        try c.tipOr(h, st.deleteTipSt, st.deleteTip);
        try h.raw("</div>");
        try c.toggleOf(h, st.ack);
    }
    try c.modalFoot(h);
    try c.btnRowOpen(h);
    if (st.foot.gated) {
        try c.btnGated(h, st.foot.label, st.foot.why);
    } else {
        try c.btn(h, st.foot.label, st.foot.variant, "auto-run-go", "");
    }
    try c.btn(h, st.foot.cancel, "ghost", "modal-close", "");
    try c.btnRowClose(h);
    try c.modalClose(h);
}

// ══ Automations ▸ schedule editor ══
// Three block lists (the aeBlock kit): head (label · automation picker · enabled · warnings),
// trigger (kind picker + only the fields that kind reads) and the any-kind gates.

/// AsModal is the schedule-editor dialog.
pub const AsModal = struct {
    title: []const u8 = "",
    hasErr: bool = false,
    err: []const u8 = "",
    head: []const AeBlock = &.{},
    secTrigger: []const u8 = "",
    trigger: []const AeBlock = &.{},
    secGates: []const u8 = "",
    gates: []const AeBlock = &.{},
    save: []const u8 = "",
    cancel: []const u8 = "",
};

pub fn renderAsModal(h: *Html, st: AsModal) !void {
    try c.modalOpen(h, st.title);
    if (st.hasErr) {
        try h.raw("<div class=ae-err>");
        try c.hint(h, "bad", st.err);
        try h.raw("</div>");
    }
    try renderAeBlocks(h, st.head);
    try c.sectionOpen(h, st.secTrigger);
    try renderAeBlocks(h, st.trigger);
    try c.sectionClose(h);
    try c.sectionOpen(h, st.secGates);
    try renderAeBlocks(h, st.gates);
    try c.sectionClose(h);
    try c.modalFoot(h);
    try c.btnRowOpen(h);
    try c.btn(h, st.save, "primary", "auto-sch-save", "");
    try c.btn(h, st.cancel, "ghost", "modal-close", "");
    try c.btnRowClose(h);
    try c.modalClose(h);
}

test "ar footer: gated arm renders btnGated, live arm the variant button" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderArModal(&h, .{ .title = "Run now", .foot = .{ .gated = true, .label = "Run", .why = "Pick a file first", .cancel = "Cancel" } });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "disabled title=\"Pick a file first\"") != null);
    h.b.clearRetainingCapacity();
    try renderArModal(&h, .{ .title = "Run now", .foot = .{ .label = "Run", .variant = "destructive", .cancel = "Cancel" } });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "rp-btn--destructive\" data-act=\"auto-run-go\"") != null);
}

test "as block: fpairsel pairs two selects, selraw raws the label" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderAeBlock(&h, .{ .kind = "selraw", .sel = .{ .id = "auto-sch-kind", .curLabel = "Interval" }, .labelHtml = "<span class=ss-label>Trigger<i></i></span>" });
    try std.testing.expect(std.mem.startsWith(u8, h.b.items, "<div class=ss-field><span class=ss-label>Trigger<i></i></span>"));
    h.b.clearRetainingCapacity();
    try renderAeBlock(&h, .{ .kind = "fpairsel", .sel = .{ .id = "a" }, .sel2 = .{ .id = "b" } });
    try std.testing.expect(std.mem.startsWith(u8, h.b.items, "<div class=fpair><div class=ss-field>"));
}

// ══ Motion ▸ point-cloud viewer ══
// STRUCTURAL chrome only: everything inside #pcv-canvas is pc_viewer.js (THREE.js), and the
// transport controls deliberately carry NO data-act (the JS owns them, so a frame never costs a
// Go round-trip). Both dialogs hand-roll their chrome — the dialog carries an extra `pcv-modal`
// class and every close control dispatches pcv-close (dispose GL), not modal-close — so the
// bracket is local to this pair rather than components.zig modalOpen.

fn pcvModalOpen(h: *Html, title: []const u8) !void {
    try h.raw("<div class=modal-scrim data-act=pcv-close></div>" ++
        "<div class=\"modal pcv-modal\" role=dialog><div class=modal-head><h3 class=modal-title>");
    try h.esc(title);
    try h.raw("</h3><button class=modal-x data-act=pcv-close aria-label=Close>✕</button></div>" ++
        "<div class=modal-body>");
}

fn pcvModalFoot(h: *Html) !void {
    try h.raw("</div><div class=modal-foot>");
}

fn pcvModalClose(h: *Html) !void {
    try h.raw("</div></div>");
}

/// PCViewer is the viewer shell. maxFrame is a pre-formatted integer spliced into an UNQUOTED
/// attribute, as Go does.
pub const PCViewer = struct {
    title: []const u8 = "",
    playLabel: []const u8 = "",
    maxFrame: []const u8 = "",
    hint: []const u8 = "",
    close: []const u8 = "",
};

pub fn renderPCViewer(h: *Html, st: PCViewer) !void {
    try pcvModalOpen(h, st.title);
    try h.raw("<div class=pcv-wrap><div id=pcv-stage class=pcv-stage>" ++
        "<canvas id=pcv-canvas class=pcv-canvas></canvas></div><div class=pcv-transport>" ++
        "<button id=pcv-play class=\"rp-btn rp-btn--go pcv-play\">▶ ");
    try h.esc(st.playLabel);
    try h.raw("</button><input id=pcv-scrub class=slider-input type=range min=0 max=");
    try h.raw(st.maxFrame);
    try h.raw(" step=1 value=0><span id=pcv-time class=pcv-time data-label=\"pcv-time\"></span></div>" ++
        "<div id=pcv-info class=pcv-info data-label=\"pcv-info\"></div><div class=pcv-hint>");
    try h.esc(st.hint);
    try h.raw("</div></div>");
    try pcvModalFoot(h);
    try c.btn(h, st.close, "outline", "pcv-close", "");
    try pcvModalClose(h);
}

/// PCGpu is the "viewer needs GPU" prompt. enabled = the flag was just flipped, so the card
/// confirms + asks for a restart instead of offering the one-click enable.
pub const PCGpu = struct {
    title: []const u8 = "",
    msg: []const u8 = "",
    enabled: bool = false,
    enableLabel: []const u8 = "",
    close: []const u8 = "",
};

pub fn renderPCGpu(h: *Html, st: PCGpu) !void {
    try pcvModalOpen(h, st.title);
    try h.raw("<p class=pcv-gpu-msg>");
    try h.esc(st.msg);
    try h.raw("</p>");
    try pcvModalFoot(h);
    if (!st.enabled) try c.btn(h, st.enableLabel, "go", "pcv-enablegpu", "");
    try c.btn(h, st.close, "outline", "pcv-close", "");
    try pcvModalClose(h);
}

test "pcv gpu prompt: enable button only on the not-yet-enabled arm" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderPCGpu(&h, .{ .title = "GPU", .msg = "off by default", .enableLabel = "Enable GPU", .close = "Close" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "data-act=\"pcv-enablegpu\"") != null);
    h.b.clearRetainingCapacity();
    try renderPCGpu(&h, .{ .title = "GPU", .msg = "restart to apply", .enabled = true, .close = "Close" });
    try std.testing.expect(std.mem.indexOf(u8, h.b.items, "pcv-enablegpu") == null);
}
