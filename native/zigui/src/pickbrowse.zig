//! In-app file/dir/save picker modal renderer — byte-exact port of the Go golden reference
//! (internal/webui/pick_browser_render.go pkBrowseHTMLOf + sub-renderers). State arrives fully
//! resolved from Go (pick_browser_state.go): i18n strings, every row/chip, thumbnails as loopback
//! URLs, sizes/dates formatted. Zig only walks state → markup, reusing the shared recipe helpers.
//! Golden gate: internal/webui/zigui_golden_pickbrowse_test.go.

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");
const lk = @import("library_kit.zig");

pub const NavRow = struct {
    act: []const u8 = "",
    icon: []const u8 = "",
    label: []const u8 = "",
    on: bool = false,
    unpin: []const u8 = "",
};

pub const Group = struct {
    header: []const u8 = "",
    rows: []const NavRow = &.{},
};

pub const Crumb = struct {
    label: []const u8 = "",
    act: []const u8 = "",
};

pub const Chip = struct {
    label: []const u8 = "",
    act: []const u8 = "",
    active: bool = false,
};

pub const Entry = struct {
    act: []const u8 = "",
    selAct: []const u8 = "",
    glyph: []const u8 = "",
    img: []const u8 = "",
    name: []const u8 = "",
    modified: []const u8 = "",
    size: []const u8 = "",
    typ: []const u8 = "",
    checked: bool = false,
    sel: bool = false,
    hl: bool = false,
};

pub const PkBrowse = struct {
    title: []const u8 = "",
    groups: []const Group = &.{},
    crumbs: []const Crumb = &.{},
    pathVal: []const u8 = "",
    pathPH: []const u8 = "",
    searchVal: []const u8 = "",
    searchPH: []const u8 = "",
    sorts: []const Chip = &.{},
    viewList: Chip = .{},
    viewGrid: Chip = .{},
    hidden: Chip = .{},
    pin: Chip = .{},
    hasFilter: bool = false,
    filterOne: Chip = .{},
    filterAll: Chip = .{},
    grid: bool = false,
    colName: []const u8 = "",
    colMod: []const u8 = "",
    colSize: []const u8 = "",
    colType: []const u8 = "",
    entries: []const Entry = &.{},
    empty: []const u8 = "",
    more: []const u8 = "",
    readout: []const u8 = "",
    saveMode: bool = false,
    saveVal: []const u8 = "",
    savePH: []const u8 = "",
    badge: []const u8 = "",
    sysDialog: []const u8 = "",
    cancel: []const u8 = "",
    primary: []const u8 = "",
};

// render is the full modal (mirrors pkBrowseHTMLOf: modal(title, body, footer)).
pub fn render(h: *Html, s: PkBrowse) !void {
    try c.modalOpen(h, s.title); // scrim + head + <div class=modal-body>
    try h.raw("<div class=pk-wrap><div class=\"pk-side libnav\" id=pk-side>");
    try sidebar(h, s.groups);
    try h.raw("</div><div class=pk-main><div class=pk-bar>");
    try navBtns(h);
    try h.raw("<span id=pk-crumb>");
    try crumb(h, s.crumbs);
    try h.raw("</span></div>");
    try toolbar(h, s);
    try h.raw("<div class=pk-entries id=pk-entries>");
    try entries(h, s);
    try h.raw("</div></div></div>");
    try c.modalFoot(h); // </div><div class=modal-foot>
    try h.raw("<div id=pk-foot class=pk-foot-l>");
    try foot(h, s);
    try h.raw("</div>");
    try c.modalClose(h); // </div></div>
}

fn sidebar(h: *Html, groups: []const Group) !void {
    for (groups) |g| {
        try h.raw("<div class=libnav-hd>");
        try h.esc(g.header);
        try h.raw("</div>");
        for (g.rows) |r| {
            if (r.unpin.len != 0) {
                try h.raw("<div class=\"libnav-it");
                if (r.on) try h.raw(" on");
                try h.raw("\"><span class=libnav-ic data-act=\"");
                try h.esc(r.act);
                try h.raw("\">");
                try h.raw(r.icon); // glyph literal, unescaped (Go parity)
                try h.raw("</span><span class=libnav-t data-act=\"");
                try h.esc(r.act);
                try h.raw("\">");
                try h.esc(r.label);
                try h.raw("</span>");
                try c.btn(h, "✕", "ghost", r.unpin, "");
                try h.raw("</div>");
                continue;
            }
            try navIt(h, r);
        }
    }
}

fn navIt(h: *Html, r: NavRow) !void {
    try h.raw("<div class=\"libnav-it");
    if (r.on) try h.raw(" on");
    try h.raw("\" data-act=\"");
    try h.esc(r.act);
    try h.raw("\"><span class=libnav-ic>");
    try h.raw(r.icon);
    try h.raw("</span><span class=libnav-t>");
    try h.esc(r.label);
    try h.raw("</span></div>");
}

fn navBtns(h: *Html) !void {
    try c.btn(h, "‹", "outline", "pk-back", "");
    try c.btn(h, "›", "outline", "pk-fwd", "");
    try c.btn(h, "↑", "outline", "pk-up", "");
}

fn crumb(h: *Html, crumbs: []const Crumb) !void {
    try h.raw("<span class=lib-crumb>");
    for (crumbs, 0..) |seg, i| {
        try c.btn(h, seg.label, "ghost", seg.act, "");
        if (i < crumbs.len - 1) try h.raw("<span class=sep>›</span>");
    }
    try h.raw("</span>");
}

fn toolbar(h: *Html, s: PkBrowse) !void {
    try h.raw("<div class=pk-bar><span class=pk-path>");
    try lk.fieldRaw(h, "pk-goto", s.pathVal, s.pathPH);
    try h.raw("</span>");
    try lk.fieldRaw(h, "pk-search", s.searchVal, s.searchPH);
    try h.raw("</div><div class=pk-bar>");
    for (s.sorts) |ch| try c.fchip(h, ch.label, "", ch.act, ch.active);
    try h.raw("<span class=seg>");
    try c.fchip(h, s.viewList.label, "", s.viewList.act, s.viewList.active);
    try c.fchip(h, s.viewGrid.label, "", s.viewGrid.act, s.viewGrid.active);
    try h.raw("</span>");
    try c.fchip(h, s.hidden.label, "", s.hidden.act, s.hidden.active);
    try c.fchip(h, s.pin.label, "", s.pin.act, s.pin.active);
    if (s.hasFilter) {
        try h.raw("<span class=seg>");
        try c.fchip(h, s.filterOne.label, "", s.filterOne.act, s.filterOne.active);
        try c.fchip(h, s.filterAll.label, "", s.filterAll.act, s.filterAll.active);
        try h.raw("</span>");
    }
    try h.raw("</div>");
}

fn entries(h: *Html, s: PkBrowse) !void {
    if (s.empty.len != 0) {
        try c.emptyState(h, s.empty);
        return;
    }
    if (s.grid) try grid(h, s.entries) else try list(h, s);
    if (s.more.len != 0) {
        try h.raw("<p class=page-sub>");
        try h.esc(s.more);
        try h.raw("</p>");
    }
}

fn list(h: *Html, s: PkBrowse) !void {
    try h.raw("<div class=pk-cols><span>");
    try h.esc(s.colName);
    try h.raw("</span><span>");
    try h.esc(s.colMod);
    try h.raw("</span><span>");
    try h.esc(s.colSize);
    try h.raw("</span><span>");
    try h.esc(s.colType);
    try h.raw("</span></div>");
    for (s.entries) |e| {
        try h.raw("<div class=\"pk-row");
        if (e.sel) try h.raw(" sel");
        if (e.hl) try h.raw(" hl");
        try h.raw("\" data-act=\"");
        try h.esc(e.act);
        try h.raw("\"");
        if (e.hl) try h.raw(" id=pk-hl aria-selected=true");
        try h.raw("><span class=pk-row-n>");
        if (e.selAct.len != 0) {
            try h.raw("<input type=checkbox data-act=\"");
            try h.esc(e.selAct);
            try h.raw("\"");
            if (e.checked) try h.raw(" checked");
            try h.raw(">");
        }
        try h.raw("<span>");
        try h.raw(e.glyph);
        try h.raw("</span><span class=t>");
        try h.esc(e.name);
        try h.raw("</span></span><span class=pk-row-m>");
        try h.esc(e.modified);
        try h.raw("</span><span class=pk-row-s>");
        try h.esc(e.size);
        try h.raw("</span><span class=pk-row-k>");
        try h.esc(e.typ);
        try h.raw("</span></div>");
    }
}

fn grid(h: *Html, es: []const Entry) !void {
    try h.raw("<div class=lib-grid>");
    for (es) |e| {
        try h.raw("<div class=\"gcard");
        if (e.sel) try h.raw(" pk-tile sel");
        if (e.hl) try h.raw(" hl");
        try h.raw("\" data-act=\"");
        try h.esc(e.act);
        try h.raw("\"");
        if (e.hl) try h.raw(" id=pk-hl aria-selected=true");
        try h.raw("><div class=gcard-ic>");
        if (e.img.len != 0) {
            try h.raw("<img src=\"");
            try h.esc(e.img);
            try h.raw("\" loading=lazy alt=\"\">");
        } else {
            try h.raw(e.glyph);
        }
        try h.raw("</div><div class=gcard-t>");
        try h.esc(e.name);
        try h.raw("</div></div>");
    }
    try h.raw("</div>");
}

fn foot(h: *Html, s: PkBrowse) !void {
    try h.raw("<span class=pk-read>");
    try h.esc(s.readout);
    try h.raw("</span>");
    if (s.saveMode) try lk.fieldRaw(h, "pk-name", s.saveVal, s.savePH);
    if (s.badge.len != 0) try c.badge(h, s.badge, "warning");
    if (s.sysDialog.len != 0) try c.btn(h, s.sysDialog, "ghost", "pk-sys", "");
    try c.btn(h, s.cancel, "outline", "modal-close", "");
    try c.btn(h, s.primary, "primary", "pk-choose", "");
}
