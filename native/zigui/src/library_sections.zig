//! Library tab sections: Browse · Favorites · Collection · Playlists · History · ID Marks ·
//! Queue · Presets. Mirrors the pure renderers in internal/webui/render_library.go byte-for-byte.
//! Every number arrives pre-formatted; every act built from a path/id is prefix++id (identical
//! bytes to Go's html.EscapeString(prefix+id), the prefixes being escape-free literals).

const std = @import("std");
const Html = @import("html.zig").Html;
const c = @import("components.zig");
const k = @import("library_kit.zig");
const f = @import("libfixers.zig");

// ── Browse ──

pub const Seg = struct {
    label: []const u8 = "",
    path: []const u8 = "",
};

/// Fe is one browse entry (list + grid share it).
pub const Fe = struct {
    name: []const u8 = "",
    path: []const u8 = "",
    isDir: bool = false,
    glyph: []const u8 = "",
    gridSub: []const u8 = "",
    sub: []const u8 = "",
    key: k.KeyPill = .{},
    checked: bool = false,
    sel: bool = false,
};

pub const Browse = struct {
    msg: []const u8 = "",
    crumbs: []const Seg = &.{},
    up: []const u8 = "",
    upPath: []const u8 = "",
    gotoLbl: []const u8 = "",
    filter: []const u8 = "",
    filterPh: []const u8 = "",
    kindLbl: []const u8 = "",
    kind: k.Select = .{},
    sortLbl: []const u8 = "",
    sort: k.Select = .{},
    listLbl: []const u8 = "",
    gridLbl: []const u8 = "",
    grid: bool = false,
    keyChip: k.Chip = .{},
    folder: k.Select = .{},
    selAll: bool = false,
    selAllOn: bool = false,
    selAllTitle: []const u8 = "",
    count: []const u8 = "",
    boundNote: []const u8 = "",
    hasBound: bool = false,
    boundActs: k.PlAct = .{},
    entries: []const Fe = &.{},
    more: []const u8 = "",
    batch: k.Batch = .{},
};

pub fn renderBrowse(h: *Html, st: Browse) !void {
    if (st.msg.len != 0) return c.emptyState(h, st.msg);
    // breadcrumb
    try h.raw("<div class=lib-crumb>");
    for (st.crumbs, 0..) |seg, i| {
        try c.btnAct(h, seg.label, "ghost", "lib-nav:", seg.path);
        if (i + 1 < st.crumbs.len) try h.raw("<span class=sep>›</span>");
    }
    try h.raw("</div>");
    // toolbar (quick-access + pinned live in the left nav rail)
    try h.raw("<div class=lib-toolbar>");
    try c.btnAct(h, st.up, "outline", "lib-nav:", st.upPath);
    try c.btn(h, st.gotoLbl, "ghost", "pick-dir:lib-nav-to", "");
    try k.fieldRaw(h, "lib-search", st.filter, st.filterPh);
    // .lib-ctl glues each label to its control across flex-wrap
    try k.ctlLabelOpen(h, st.kindLbl);
    try c.selectBox(h, st.kind);
    try h.raw("</span>");
    try k.ctlLabelOpen(h, st.sortLbl);
    try c.selectBox(h, st.sort);
    try h.raw("</span>");
    // view: segmented mode switch (mutually exclusive)
    try h.raw("<span class=seg>");
    try c.fchip(h, st.listLbl, "", "lib-view:list", !st.grid);
    try c.fchip(h, st.gridLbl, "", "lib-view:grid", st.grid);
    try h.raw("</span>");
    try k.chip(h, st.keyChip);
    try k.amenu(h, st.folder);
    if (st.selAll) {
        try h.raw("<input type=checkbox class=trk-selall data-act=lib-batch-all title=\"");
        try h.esc(st.selAllTitle);
        try h.raw("\"");
        if (st.selAllOn) try h.raw(" checked");
        try h.raw(">");
    }
    try h.raw("<span class=lib-tlabel>");
    try h.esc(st.count);
    try h.raw("</span>");
    try h.raw("</div>");
    // folder bound to a playlist -> its actions live right here
    if (st.hasBound) {
        try h.raw("<p class=page-sub>🎵 ");
        try h.esc(st.boundNote);
        try h.raw("</p>");
        try k.plAct(h, st.boundActs);
    }

    if (st.grid) {
        try h.raw("<div class=lib-grid>");
        for (st.entries) |it| {
            try h.raw("<div class=gcard data-act=\"");
            try h.raw(if (it.isDir) "lib-nav:" else "lib-open:");
            try h.esc(it.path);
            try h.raw("\" data-ctx=\"");
            try h.raw(if (it.isDir) "lib-dirctx:" else "lib-ctx:");
            try h.esc(it.path);
            try h.raw("\"><div class=gcard-ic>");
            try h.raw(it.glyph);
            try h.raw("</div><div class=gcard-t>");
            try h.esc(it.name);
            try h.raw("</div><div class=gcard-s>");
            try h.esc(it.gridSub);
            try h.raw("</div></div>");
        }
        try h.raw("</div>");
    } else {
        try h.raw("<div class=trk-table>");
        for (st.entries) |it| {
            if (it.isDir) {
                try h.raw("<div class=trk-row data-act=\"lib-nav:");
                try h.esc(it.path);
                try h.raw("\" data-ctx=\"lib-dirctx:");
                try h.esc(it.path);
                try h.raw("\"><span class=trk-ic>📁</span><span class=trk-main><span class=trk-title>");
                try h.esc(it.name);
                try h.raw("</span></span></div>");
                continue;
            }
            try h.raw("<div class=\"trk-row");
            if (it.sel) try h.raw(" sel");
            try h.raw("\" data-ctx=\"lib-ctx:");
            try h.esc(it.path);
            try h.raw("\"><input type=checkbox data-act=\"lib-batch:");
            try h.esc(it.path);
            try h.raw("\"");
            if (it.checked) try h.raw(" checked");
            try h.raw("><span class=trk-ic data-act=\"lib-open:");
            try h.esc(it.path);
            try h.raw("\">");
            try h.raw(it.glyph);
            try h.raw("</span><span class=trk-main data-act=\"lib-open:");
            try h.esc(it.path);
            try h.raw("\"><span class=trk-title>");
            try h.esc(it.name);
            try h.raw("</span><span class=trk-sub>");
            try h.raw(it.sub); // byte-sizes + date, inserted unescaped by the Go original
            try h.raw("</span></span>");
            try k.trkKey(h, it.key);
            try c.btnAct(h, "⋯", "ghost", "lib-ctx:", it.path);
            try h.raw("</div>");
        }
        try h.raw("</div>");
    }
    if (st.more.len != 0) try k.pageSub(h, st.more);
    try k.batch(h, st.batch);
}

// ── Favorites ──

pub const FavRow = struct {
    label: []const u8 = "",
    path: []const u8 = "",
};

pub const Fav = struct {
    desc: []const u8 = "",
    empty: []const u8 = "",
    openLbl: []const u8 = "",
    unpinLbl: []const u8 = "",
    rows: []const FavRow = &.{},
};

pub fn renderFav(h: *Html, st: Fav) !void {
    try k.pageSub(h, st.desc);
    if (st.rows.len == 0) return c.emptyState(h, st.empty);
    try h.raw("<div class=\"rp-card\">");
    for (st.rows) |m| {
        try c.itemRowOpen(h, m.label, m.path);
        try c.btnAct(h, st.openLbl, "outline", "lib-nav:", m.path);
        try c.btnAct(h, st.unpinLbl, "ghost", "lib-unpin:", m.path);
        try c.itemRowClose(h);
    }
    try h.raw("</div>");
}

// ── Collection ──

/// CueCell is one row's drops/cues census (also the #ce-cell-<hash> patch target).
pub const CueCell = struct {
    drops: i64 = 0,
    dropsTitle: []const u8 = "",
    noDropsTitle: []const u8 = "",
    cues: i64 = 0,
    cuesTitle: []const u8 = "",
    noCuesTitle: []const u8 = "",
};

/// renderCueCell: compact drops/cues census - ◆n = drop markers, ⚑n = cues; dim glyphs
/// mark absence so prepared vs unprepared scans at a glance.
pub fn renderCueCell(h: *Html, st: CueCell) !void {
    if (st.drops > 0) {
        try h.raw("<span class=trk-drops title=\"");
        try h.esc(st.dropsTitle);
        try h.raw("\">◆");
        try c.num(h, st.drops);
        try h.raw("</span>");
    } else {
        try h.raw("<span class=\"trk-drops none\" title=\"");
        try h.esc(st.noDropsTitle);
        try h.raw("\">◇</span>");
    }
    if (st.cues > 0) {
        try h.raw("<span class=trk-cuen title=\"");
        try h.esc(st.cuesTitle);
        try h.raw("\">⚑");
        try c.num(h, st.cues);
        try h.raw("</span>");
    } else {
        try h.raw("<span class=\"trk-cuen none\" title=\"");
        try h.esc(st.noCuesTitle);
        try h.raw("\">⚑</span>");
    }
}

pub const CollHdr = struct {
    cls: []const u8 = "",
    key: []const u8 = "",
    label: []const u8 = "",
    arrow: []const u8 = "",
};

pub const CollHead = struct {
    selAllTitle: []const u8 = "",
    selAllOn: bool = false,
    main: CollHdr = .{},
    cueLbl: []const u8 = "",
    bpm: CollHdr = .{},
    timeLbl: []const u8 = "",
    key: CollHdr = .{},
};

pub const CollRow = struct {
    path: []const u8 = "",
    checked: bool = false,
    warn: bool = false,
    selCls: []const u8 = "",
    title: []const u8 = "",
    sub: []const u8 = "",
    verified: bool = false,
    cellId: []const u8 = "",
    cue: CueCell = .{},
    bpm: []const u8 = "",
    dur: []const u8 = "",
    key: k.KeyPill = .{},
};

pub const Coll = struct {
    msg: []const u8 = "",
    importLbl: []const u8 = "",
    djsyncLbl: []const u8 = "",
    gridFix: bool = false,
    gridFixLbl: []const u8 = "",
    moreLbl: []const u8 = "",
    moreOpen: bool = false,
    moreItems: []const k.Tab = &.{},
    search: []const u8 = "",
    searchPh: []const u8 = "",
    genre: k.Select = .{},
    label: k.Select = .{},
    hasPlFacet: bool = false,
    plFacet: k.Select = .{},
    keyChip: k.Chip = .{},
    noDropsLbl: []const u8 = "",
    noDrops: bool = false,
    clear: bool = false,
    clearLbl: []const u8 = "",
    prep: c.Select = .{},
    chips: []const k.Chip = &.{},
    hasInline: bool = false,
    inlineActs: k.PlAct = .{},
    hasResults: bool = false,
    results: f.Results = .{},
    head: CollHead = .{},
    rows: []const CollRow = &.{},
    verifiedTitle: []const u8 = "",
    empty: []const u8 = "",
    isEmpty: bool = false,
    more: []const u8 = "",
    batch: k.Batch = .{},
};

pub fn renderColl(h: *Html, st: Coll) !void {
    if (st.msg.len != 0) return c.emptyState(h, st.msg);
    // actions: the two everyday operations + the fixer up front; everything
    // occasional lives behind Maintenance so the list gets the vertical space
    try h.raw("<div class=lib-toolbar>");
    try c.btn(h, st.importLbl, "primary", "lib-import", "");
    try c.btn(h, st.djsyncLbl, "primary", "lib-djsync", "");
    if (st.gridFix) try c.btn(h, st.gridFixLbl, "outline", "gf-open", "");
    try h.raw("<span class=lib-more>");
    try c.btn(h, st.moreLbl, "ghost", "lib-more", "");
    try renderMoreMenu(h, st);
    try h.raw("</span>");
    // filters flow in the SAME wrap row: search + facet dropdowns; active facets
    // render as removable chips (one toolbar = one less stacked row above the list)
    try k.fieldRaw(h, "lib-coll-search", st.search, st.searchPh);
    try c.selectBox(h, st.genre);
    try c.selectBox(h, st.label);
    if (st.hasPlFacet) try c.selectBox(h, st.plFacet);
    try k.chip(h, st.keyChip);
    try c.fchip(h, st.noDropsLbl, "", "lib-nodrops", st.noDrops);
    if (st.clear) try c.btn(h, st.clearLbl, "ghost", "lib-clearfilters", "");
    try f.renderPrep(h, st.prep); // P-key target (library_prep.go)
    try h.raw("</div>");
    for (st.chips) |ch| try k.chip(h, ch);
    // exactly one playlist facet active -> the collection IS that playlist's view
    if (st.hasInline) try k.plAct(h, st.inlineActs);
    // batch results replace the list while a fixer's results view is on
    if (st.hasResults) return f.renderResults(h, st.results);

    try renderCollHead(h, st.head);
    try h.raw("<div class=trk-table>");
    for (st.rows) |r| {
        try h.raw("<div class=\"trk-row");
        try h.raw(r.selCls);
        try h.raw("\" data-ctx=\"lib-ctx:");
        try h.esc(r.path);
        try h.raw("\"><input type=checkbox data-act=\"lib-collsel:");
        try h.esc(r.path);
        try h.raw("\"");
        if (r.checked) try h.raw(" checked");
        try h.raw(">");
        try k.trkIcon(h, r.warn);
        try h.raw("<span class=trk-main data-act=\"lib-track:");
        try h.esc(r.path);
        try h.raw("\"><span class=trk-title>");
        try h.esc(r.title);
        try h.raw("</span><span class=trk-sub>");
        try h.esc(r.sub);
        try h.raw("</span></span>");
        if (r.verified) {
            try h.raw("<span class=trk-verified title=\"");
            try h.esc(st.verifiedTitle);
            try h.raw("\">✓</span>");
        }
        try h.raw("<span class=trk-cell-ce id=");
        try h.raw(r.cellId);
        try h.raw(">");
        try renderCueCell(h, r.cue);
        try h.raw("</span><span class=trk-bpm>");
        try h.raw(r.bpm);
        try h.raw("</span><span class=trk-dur>");
        try h.raw(r.dur);
        try h.raw("</span>");
        try k.trkKey(h, r.key);
        try h.raw("</div>");
    }
    try h.raw("</div>");
    if (st.isEmpty) {
        try c.emptyState(h, st.empty);
    } else if (st.more.len != 0) {
        try k.pageSub(h, st.more);
    }
    // selection bar: playlist add + verified-grid marking; in cue-edit mode the checked
    // rows are the mass-apply set for the assigned patterns
    try k.batch(h, st.batch);
}

/// renderMoreMenu is the Maintenance popover (occasional collection operations). Item acts are
/// literal constants, spliced raw exactly like the Go original.
fn renderMoreMenu(h: *Html, st: Coll) !void {
    if (!st.moreOpen) return;
    try h.raw("<div class=lib-popmenu>");
    for (st.moreItems) |it| {
        try h.raw("<button class=lib-popitem data-act=\"lib-morego:");
        try h.raw(it.val);
        try h.raw("\">");
        try h.esc(it.label);
        try h.raw("</button>");
    }
    try h.raw("</div>");
}

fn renderCollHead(h: *Html, st: CollHead) !void {
    const hdr = struct {
        fn f(hh: *Html, x: CollHdr) !void {
            try hh.raw("<span class=\"");
            try hh.raw(x.cls);
            try hh.raw(" trk-sortable\" data-act=\"lib-coll-hsort:");
            try hh.raw(x.key);
            try hh.raw("\">");
            try hh.esc(x.label);
            try hh.raw(x.arrow);
            try hh.raw("</span>");
        }
    }.f;
    try h.raw("<div class=trk-h><input type=checkbox class=trk-selall data-act=lib-collsel-all title=\"");
    try h.esc(st.selAllTitle);
    try h.raw("\"");
    if (st.selAllOn) try h.raw(" checked");
    try h.raw(">");
    try hdr(h, st.main);
    try h.raw("<span class=trk-cell-ce>");
    try h.esc(st.cueLbl);
    try h.raw("</span>");
    try hdr(h, st.bpm);
    try h.raw("<span class=trk-dur>");
    try h.esc(st.timeLbl);
    try h.raw("</span>");
    try hdr(h, st.key);
    try h.raw("</div>");
}

// ── Playlists ──

pub const PlRow = struct {
    id: []const u8 = "",
    icon: []const u8 = "",
    name: []const u8 = "",
    sub: []const u8 = "",
    sel: bool = false,
};

pub const PlItem = struct {
    pos: []const u8 = "",
    idx: []const u8 = "",
    path: []const u8 = "",
    title: []const u8 = "",
    key: k.KeyPill = .{},
    manual: bool = false,
};

pub const PlOpen = struct {
    title: []const u8 = "",
    smartNote: []const u8 = "",
    acts: k.PlAct = .{},
    items: []const PlItem = &.{},
    empty: []const u8 = "",
};

pub const Pls = struct {
    msg: []const u8 = "",
    newLbl: []const u8 = "",
    newSmartLbl: []const u8 = "",
    hasCloud: bool = false,
    cloud: k.Select = .{},
    rows: []const PlRow = &.{},
    empty: []const u8 = "",
    hasOpen: bool = false,
    open: PlOpen = .{},
};

pub fn renderPls(h: *Html, st: Pls) !void {
    if (st.msg.len != 0) return c.emptyState(h, st.msg);
    try h.raw("<div class=lib-toolbar>");
    try c.btn(h, st.newLbl, "primary", "lib-pl-new", "");
    try c.btn(h, st.newSmartLbl, "outline", "lib-pl-newsmart", "");
    // cloud ops are occasional - one ⋯ menu instead of three toolbar buttons
    if (st.hasCloud) try k.amenu(h, st.cloud);
    try h.raw("</div>");
    if (st.rows.len == 0) try c.emptyState(h, st.empty);
    // dense rows: the row itself opens (no per-row Open button)
    try h.raw("<div class=trk-table>");
    for (st.rows) |p| {
        try h.raw("<div class=\"trk-row");
        if (p.sel) try h.raw(" sel");
        try h.raw("\" data-act=\"lib-pl:");
        try h.raw(p.id);
        try h.raw("\"><span class=trk-ic>");
        try h.raw(p.icon);
        try h.raw("</span><span class=trk-main><span class=trk-title>");
        try h.esc(p.name);
        try h.raw("</span><span class=trk-sub>");
        try h.esc(p.sub);
        try h.raw("</span></span></div>");
    }
    try h.raw("</div>");
    if (st.hasOpen) try renderPlOpen(h, st.open);
}

fn renderPlOpen(h: *Html, st: PlOpen) !void {
    try c.sectionOpen(h, st.title);
    try c.sectionClose(h);
    if (st.smartNote.len != 0) try k.pageSub(h, st.smartNote);
    try k.plAct(h, st.acts);
    try h.raw("<div class=trk-table>");
    for (st.items) |it| {
        try h.raw("<div class=trk-row><span class=trk-pos>");
        try h.raw(it.pos);
        try h.raw("</span><span class=trk-main ");
        if (it.path.len != 0) {
            try h.raw("data-act=\"lib-track:");
            try h.esc(it.path);
            try h.raw("\"");
        }
        try h.raw("><span class=trk-title>");
        try h.esc(it.title);
        try h.raw("</span></span>");
        try k.trkKey(h, it.key);
        if (it.manual) {
            try c.btnAct(h, "↑", "ghost", "lib-pl-up:", it.idx);
            try c.btnAct(h, "↓", "ghost", "lib-pl-down:", it.idx);
            try c.btnAct(h, "✕", "ghost", "lib-pl-rm:", it.path);
        }
        try h.raw("</div>");
    }
    try h.raw("</div>");
    if (st.items.len == 0) try c.emptyState(h, st.empty);
}

// ── History ──

pub const Sess = struct {
    idx: []const u8 = "",
    date: []const u8 = "",
    sub: []const u8 = "",
    sel: bool = false,
};

pub const Played = struct {
    path: []const u8 = "",
    warn: bool = false,
    title: []const u8 = "",
    meta: []const u8 = "",
    key: k.KeyPill = .{},
};

pub const Hist = struct {
    loadLbl: []const u8 = "",
    src: k.Select = .{},
    desc: []const u8 = "",
    empty: []const u8 = "",
    isEmpty: bool = false,
    sessions: []const Sess = &.{},
    hasPlayed: bool = false,
    playedLbl: []const u8 = "",
    sortLbl: []const u8 = "",
    sort: k.Select = .{},
    dirLbl: []const u8 = "",
    played: []const Played = &.{},
};

pub fn renderHist(h: *Html, st: Hist) !void {
    // source picker: every DJ software with a play-history model (Traktor NML history dir,
    // Rekordbox master.db djmdHistory). VirtualDJ keeps no session history.
    try h.raw("<div class=lib-toolbar>");
    try c.btn(h, st.loadLbl, "primary", "lib-hist-load", "");
    try c.selectBox(h, st.src);
    try h.raw("</div>");
    try k.pageSub(h, st.desc);
    if (st.isEmpty) {
        try c.emptyState(h, st.empty);
    } else {
        // dense rows: the row itself opens the session (no per-row Open button)
        try h.raw("<div class=trk-table>");
        for (st.sessions) |sm| {
            try h.raw("<div class=\"trk-row");
            if (sm.sel) try h.raw(" sel");
            try h.raw("\" data-act=\"lib-session:");
            try h.raw(sm.idx);
            try h.raw("\"><span class=trk-ic>🗓</span><span class=trk-main><span class=trk-title>");
            try h.esc(sm.date);
            try h.raw("</span><span class=trk-sub>");
            try h.esc(sm.sub);
            try h.raw("</span></span></div>");
        }
        try h.raw("</div>");
    }
    if (!st.hasPlayed) return;
    // sort: one dropdown + direction chip (was a 9-chip wall)
    try h.raw("<div class=lib-toolbar><span class=lib-tlabel>");
    try h.esc(st.playedLbl);
    try h.raw("</span>");
    try k.ctlLabelOpen(h, st.sortLbl);
    try c.selectBox(h, st.sort);
    try h.raw("</span>");
    try c.fchip(h, st.dirLbl, "", "lib-play-dir", false);
    try h.raw("</div>");
    try h.raw("<div class=trk-table>");
    for (st.played) |p| {
        try h.raw("<div class=trk-row>");
        try k.trkIcon(h, p.warn);
        try h.raw("<span class=trk-main data-act=\"lib-track:");
        try h.esc(p.path);
        try h.raw("\"><span class=trk-title>");
        try h.esc(p.title);
        try h.raw("</span><span class=trk-sub>");
        try h.esc(p.meta);
        try h.raw("</span></span>");
        try k.trkKey(h, p.key);
        try h.raw("</div>");
    }
    try h.raw("</div>");
}

// ── ID Marks ──

pub const IDMRow = struct {
    path: []const u8 = "",
    artist: bool = false,
    artistAct: []const u8 = "",
    label: bool = false,
    labelAct: []const u8 = "",
    delAct: []const u8 = "",
};

pub const IDM = struct {
    msg: []const u8 = "",
    markFileLbl: []const u8 = "",
    markFolderLbl: []const u8 = "",
    typePathLbl: []const u8 = "",
    desc: []const u8 = "",
    empty: []const u8 = "",
    artistLbl: []const u8 = "",
    artistDl: []const u8 = "",
    labelLbl: []const u8 = "",
    labelDl: []const u8 = "",
    removeLbl: []const u8 = "",
    rows: []const IDMRow = &.{},
};

pub fn renderIDM(h: *Html, st: IDM) !void {
    if (st.msg.len != 0) return c.emptyState(h, st.msg);
    try h.raw("<div class=lib-toolbar>");
    try c.btn(h, st.markFileLbl, "primary", "pick-file:lib-id-addpath", "");
    try c.btn(h, st.markFolderLbl, "outline", "pick-dir:lib-id-addpath", "");
    try c.btn(h, st.typePathLbl, "ghost", "lib-id-manual", "");
    try h.raw("</div>");
    try k.pageSub(h, st.desc);
    if (st.rows.len == 0) return c.emptyState(h, st.empty);
    try h.raw("<div class=\"rp-card\">");
    for (st.rows) |e| {
        try h.raw("<div class=row><span class=row-label>");
        try h.esc(e.path);
        try h.raw("</span>");
        try c.toggleRow(h, st.artistLbl, st.artistDl, e.artistAct, e.artist);
        try c.toggleRow(h, st.labelLbl, st.labelDl, e.labelAct, e.label);
        try c.btn(h, st.removeLbl, "ghost", e.delAct, "");
        try h.raw("</div>");
    }
    try h.raw("</div>");
}

// ── Queue ──

pub const Job = struct {
    label: []const u8 = "",
    cancel: bool = false,
    cancelLbl: []const u8 = "",
    cancelAct: []const u8 = "",
    status: []const u8 = "",
    statusVar: []const u8 = "",
    width: []const u8 = "",
    caption: []const u8 = "",
    msg: []const u8 = "",
};

pub const Queue = struct {
    desc: []const u8 = "",
    empty: []const u8 = "",
    jobs: []const Job = &.{},
};

pub fn renderQueue(h: *Html, st: Queue) !void {
    try k.pageSub(h, st.desc);
    if (st.jobs.len == 0) return c.emptyState(h, st.empty);
    for (st.jobs) |j| {
        try h.raw("<div class=qjob><div class=qjob-h><span class=qjob-t>");
        try h.esc(j.label);
        try h.raw("</span>");
        if (j.cancel) {
            try c.btn(h, j.cancelLbl, "ghost", j.cancelAct, "");
        } else {
            try c.badge(h, j.status, j.statusVar);
        }
        try h.raw("</div>");
        try c.progressBar(h, j.width, j.caption);
        if (j.msg.len != 0) try k.pageSub(h, j.msg);
        try h.raw("</div>");
    }
}

// ── Presets catalog ──

pub const Preset = struct {
    id: []const u8 = "",
    label: []const u8 = "",
    desc: []const u8 = "",
};

pub const Presets = struct {
    newLbl: []const u8 = "",
    yoursTitle: []const u8 = "",
    emptyCustom: []const u8 = "",
    builtinsTitle: []const u8 = "",
    customBadge: []const u8 = "",
    builtinBadge: []const u8 = "",
    editLbl: []const u8 = "",
    dupLbl: []const u8 = "",
    delLbl: []const u8 = "",
    dupEditLbl: []const u8 = "",
    custom: []const Preset = &.{},
    builtins: []const Preset = &.{},
};

pub fn renderPresets(h: *Html, st: Presets) !void {
    try h.raw("<div class=lib-toolbar>");
    try c.btn(h, st.newLbl, "primary", "lib-pset-new", "");
    try h.raw("</div>");
    try c.sectionOpen(h, st.yoursTitle);
    try c.sectionClose(h);
    if (st.custom.len == 0) {
        try c.emptyState(h, st.emptyCustom);
    } else {
        try h.raw("<div class=pcards>");
        for (st.custom) |p| {
            try c.cardOpen(h, p.label, true);
            try c.badge(h, st.customBadge, "info");
            try c.cardHeadClose(h);
            try k.pageSub(h, p.desc);
            try c.btnRowOpen(h);
            try c.btnAct(h, st.editLbl, "outline", "lib-pset-edit:", p.id);
            try c.btnAct(h, st.dupLbl, "ghost", "lib-pset-dup:", p.id);
            try c.btnAct(h, st.delLbl, "destructive", "lib-pset-del:", p.id);
            try c.btnRowClose(h);
            try c.cardClose(h);
        }
        try h.raw("</div>");
    }
    try c.sectionOpen(h, st.builtinsTitle);
    try c.sectionClose(h);
    try h.raw("<div class=pcards>");
    for (st.builtins) |p| {
        try c.cardOpen(h, p.label, true);
        try c.badge(h, st.builtinBadge, "secondary");
        try c.cardHeadClose(h);
        try k.pageSub(h, p.desc);
        try c.btnAct(h, st.dupEditLbl, "outline", "lib-pset-dup:", p.id);
        try c.cardClose(h);
    }
    try h.raw("</div>");
}

test "cue cell census" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderCueCell(&h, .{ .drops = 2, .dropsTitle = "2 drops", .cues = 0, .noCuesTitle = "no cues" });
    try std.testing.expectEqualStrings("<span class=trk-drops title=\"2 drops\">◆2</span>" ++
        "<span class=\"trk-cuen none\" title=\"no cues\">⚑</span>", h.b.items);
}

test "favorites empty + populated" {
    var h = Html.init(std.testing.allocator);
    defer h.deinit();
    try renderFav(&h, .{ .desc = "Pinned", .empty = "none" });
    try std.testing.expectEqualStrings("<p class=page-sub>Pinned</p>" ++
        "<div class=\"rp-empty\"><div class=\"rp-empty__title\">none</div></div>", h.b.items);
}
