//! Fragment-tick scheduler (phase B3) — ONE call per tick per surface.
//!
//! The ~1 Hz tick used to cross the ABI once per FRAGMENT: Go marshalled each fragment's state
//! to JSON, called its render export, and deduped the returned string against its own cache.
//! Here the whole surface's state crosses ONCE (RZW1 document), every fragment is rendered on
//! this side, hashed (Wyhash-64) and compared against the hash Go last pushed for that id, and
//! only the CHANGED fragments come back — packed into one buffer Go turns into one batched Eval.
//!
//! Exports stay STATELESS: the previous hashes travel IN the document (LiveBatch.prev /
//! LogsBatch.prev). Nothing about a UI instance lives in this lib, so two windows, a headless
//! mirror and a test all use the same export with no cross-talk (see .devnotes/ZIG_UI_GUIDE.md
//! "Phase B — B3 fragment scheduler" for why that beat a Zig-side cache).
//!
//! Dedup semantics replicate Go `tickPatch` exactly: same bytes → suppressed; an id Go has no
//! hash for → always emitted (Go drops its cache on patchMain, so a DOM replace resends
//! everything). Fragment ORDER is the order live_ticks.go patched them in — the golden gate
//! compares the ordered __patch calls old path vs new.

const std = @import("std");
const Html = @import("html.zig").Html;
const live = @import("live.zig");
const logs = @import("logs.zig");

// The decoded batch feeds the EXISTING renderers: wave B-2's schema rows already describe
// live.State / logs.Lines (and their tooltip fields), so this module names those types directly
// instead of mirroring them. Pre-merge it re-exported them under tick.* for its own duplicate
// schema rows; those rows are gone.

/// Prev is one fragment's last-pushed hash (0 = unknown → always emit).
pub const Prev = struct {
    id: []const u8 = "",
    hash: u64 = 0,
};

/// LiveBatch is the Live cockpit's tick surface: every fragment's state + the timecode text
/// fragment (#live-tc carries raw text, not a renderer's output) + the prev hashes.
pub const LiveBatch = struct {
    live: live.State = .{},
    tc: []const u8 = "", // raw timecode text; escaped here (Go pushed htmlEscape(tcText()))
    prev: []const Prev = &.{},
};

/// LogsBatch is the #log-view tick surface (one fragment, hit ~1 Hz with a 400-line tail).
pub const LogsBatch = struct {
    lines: logs.Lines = .{},
    prev: []const Prev = &.{},
};

// ── RZF1: the packed changed-fragment list ──
//
//   "RZF1"   4 B  magic
//   count    u16  number of CHANGED fragments (0 = nothing to patch; header only)
//   entries  count x { id_len u16, id, hash u64, html_len u32, html }
//
// Little-endian. Every length is exact, so the Go decoder validates by walking the buffer;
// a short/over-long entry is a refusal, never a partial apply.

pub const out_magic = "RZF1";
pub const out_header_len = 6;

/// Batch renders, hashes and packs one surface's fragments.
const Batch = struct {
    a: std.mem.Allocator,
    out: std.ArrayList(u8) = .empty,
    n: u16 = 0,

    fn init(a: std.mem.Allocator) Batch {
        return .{ .a = a };
    }

    fn deinit(b: *Batch) void {
        b.out.deinit(b.a);
    }

    fn start(b: *Batch) !void {
        try b.out.appendSlice(b.a, out_magic);
        try b.out.appendSlice(b.a, &.{ 0, 0 }); // count placeholder
    }

    /// hashOf is the dedup key over the fragment's rendered bytes. Wyhash, not FNV-1a: FNV
    /// consumes ONE byte per round, which cost ~50 us on the 51 kB log tail - more than the
    /// render it guards. Wyhash reads 64 bits at a time and is ~7 us there. The value is opaque
    /// (Go only ever compares what this returned last tick), so the choice is free.
    fn hashOf(body: []const u8) u64 {
        return std.hash.Wyhash.hash(0, body);
    }

    /// prevOf finds id's last-pushed hash. Linear scan: a surface has <= a dozen fragments and
    /// this runs once per fragment per SECOND — a map would cost more to build than it saves.
    fn prevOf(prev: []const Prev, id: []const u8) ?u64 {
        for (prev) |p| if (std.mem.eql(u8, p.id, id)) return p.hash;
        return null;
    }

    /// emit appends id's fragment unless its bytes are unchanged (Go tickPatch semantics).
    fn emit(b: *Batch, id: []const u8, prev: []const Prev, body: []const u8) !void {
        const h = hashOf(body);
        if (prevOf(prev, id)) |p| {
            if (p == h) return; // same bytes → suppressed, exactly as tickPatch did
        }
        if (b.n == std.math.maxInt(u16)) return error.TooManyFragments;
        var hdr: [2]u8 = undefined;
        std.mem.writeInt(u16, &hdr, @intCast(id.len), .little);
        try b.out.appendSlice(b.a, &hdr);
        try b.out.appendSlice(b.a, id);
        var h8: [8]u8 = undefined;
        std.mem.writeInt(u64, &h8, h, .little);
        try b.out.appendSlice(b.a, &h8);
        var l4: [4]u8 = undefined;
        std.mem.writeInt(u32, &l4, @intCast(body.len), .little);
        try b.out.appendSlice(b.a, &l4);
        try b.out.appendSlice(b.a, body);
        b.n += 1;
    }

    /// frag renders one fragment through its existing renderer, then emits it.
    fn frag(
        b: *Batch,
        id: []const u8,
        prev: []const Prev,
        comptime T: type,
        comptime renderFn: fn (*Html, T) anyerror!void,
        s: T,
    ) !void {
        var h = Html.init(b.a);
        defer h.deinit();
        try renderFn(&h, s);
        try b.emit(id, prev, h.b.items);
    }

    /// text emits an escaped TEXT fragment (#live-tc / #live-rec-state hold no markup; Go
    /// pushed htmlEscape(...) for them, so the escape has to happen here to match byte for byte).
    fn text(b: *Batch, id: []const u8, prev: []const Prev, s: []const u8) !void {
        var h = Html.init(b.a);
        defer h.deinit();
        try h.esc(s);
        try b.emit(id, prev, h.b.items);
    }

    fn finish(b: *Batch) ![]u8 {
        std.mem.writeInt(u16, b.out.items[4..][0..2], b.n, .little);
        return b.out.toOwnedSlice(b.a);
    }
};

/// runLive renders the Live cockpit's tick fragments IN THE ORDER live_ticks.go patched them.
/// The per-fragment presence conditions are the state's has* flags (Go sets each from the same
/// `u.svc.X != nil` check the old tick branched on).
pub fn runLive(a: std.mem.Allocator, s: LiveBatch) ![]u8 {
    var b = Batch.init(a);
    errdefer b.deinit();
    try b.start();
    const p = s.prev;
    try b.text("live-tc", p, s.tc);
    if (s.live.transport.hasRec) try b.text("live-rec-state", p, s.live.transport.recState);
    try b.frag("live-np", p, live.NP, live.renderNP, s.live.np);
    try b.frag("live-status", p, live.Status, live.renderStatus, s.live.status);
    try b.frag("live-decks", p, live.Decks, live.renderDecks, s.live.decks);
    if (s.live.hasSignals) try b.frag("live-signals", p, live.Signals, live.renderSignals, s.live.signals);
    if (s.live.hasCockpit) try b.frag("live-cockpit", p, live.Cockpit, live.renderCockpit, s.live.cockpit);
    if (s.live.hasLink) try b.frag("live-ablelink", p, live.Link, live.renderLink, s.live.link);
    if (s.live.hasNet) {
        try b.frag("live-net", p, live.Graph, live.renderGraph, s.live.net);
        try b.frag("live-tim", p, live.Graph, live.renderGraph, s.live.tim);
    }
    if (s.live.hasPerf) try b.frag("live-perf2", p, live.Perf, live.renderPerf, s.live.perf);
    try b.frag("live-strip", p, live.Strip, live.renderStrip, s.live.strip);
    return b.finish();
}

/// runLogs renders the #log-view tail. One fragment, but the expensive one: a 400-line tail is
/// ~50 kB of HTML that used to be marshalled, rendered, quoted and pushed even when identical.
pub fn runLogs(a: std.mem.Allocator, s: LogsBatch) ![]u8 {
    var b = Batch.init(a);
    errdefer b.deinit();
    try b.start();
    try b.frag("log-view", s.prev, logs.Lines, logs.renderLines, s.lines);
    return b.finish();
}

// ── tests ──

/// entries walks a packed batch (mirror of the Go decoder) for assertions.
const Entry = struct { id: []const u8, hash: u64, html: []const u8 };

fn parseBatch(a: std.mem.Allocator, buf: []const u8) ![]Entry {
    try std.testing.expect(buf.len >= out_header_len);
    try std.testing.expectEqualStrings(out_magic, buf[0..4]);
    const n = std.mem.readInt(u16, buf[4..][0..2], .little);
    const out = try a.alloc(Entry, n);
    var pos: usize = out_header_len;
    for (out) |*e| {
        const il = std.mem.readInt(u16, buf[pos..][0..2], .little);
        pos += 2;
        e.id = buf[pos..][0..il];
        pos += il;
        e.hash = std.mem.readInt(u64, buf[pos..][0..8], .little);
        pos += 8;
        const hl = std.mem.readInt(u32, buf[pos..][0..4], .little);
        pos += 4;
        e.html = buf[pos..][0..hl];
        pos += hl;
    }
    try std.testing.expectEqual(buf.len, pos);
    return out;
}

fn liveTestState() live.State {
    return .{
        .transport = .{ .hasRec = true, .recState = "● manual · set.flac" },
        .np = .{ .line1 = "Artist", .line2 = "Title" },
        .status = .{ .rows = &.{.{ .k = "API", .kl = "api", .v = "up" }} },
        .decks = .{ .decks = &.{.{ .cls = "deckbig", .name = "DECK A", .title = "–", .meta = "-" }} },
        .strip = .{ .left = "L", .center = "C", .right = "R" },
    };
}

test "live batch: fragment set + order, no prev hashes" {
    const a = std.testing.allocator;
    const buf = try runLive(a, .{ .live = liveTestState(), .tc = "01:02:03:04" });
    defer a.free(buf);
    const es = try parseBatch(a, buf);
    defer a.free(es);
    const want = [_][]const u8{ "live-tc", "live-rec-state", "live-np", "live-status", "live-decks", "live-strip" };
    try std.testing.expectEqual(want.len, es.len);
    for (want, es) |w, e| try std.testing.expectEqualStrings(w, e.id);
    try std.testing.expectEqualStrings("01:02:03:04", es[0].html);
    try std.testing.expectEqualStrings("● manual · set.flac", es[1].html);
}

test "live batch: every optional section adds its fragment, in tick order" {
    const a = std.testing.allocator;
    var st = liveTestState();
    st.hasSignals = true;
    st.hasCockpit = true;
    st.hasLink = true;
    st.hasNet = true;
    st.hasPerf = true;
    const buf = try runLive(a, .{ .live = st });
    defer a.free(buf);
    const es = try parseBatch(a, buf);
    defer a.free(es);
    const want = [_][]const u8{
        "live-tc",      "live-rec-state", "live-np",  "live-status", "live-decks", "live-signals",
        "live-cockpit", "live-ablelink",  "live-net", "live-tim",    "live-perf2", "live-strip",
    };
    try std.testing.expectEqual(want.len, es.len);
    for (want, es) |w, e| try std.testing.expectEqualStrings(w, e.id);
}

test "dedup: matching prev hash suppresses, changed bytes come back" {
    const a = std.testing.allocator;
    const st = liveTestState();
    const first = try runLive(a, .{ .live = st, .tc = "00:00:00:00" });
    defer a.free(first);
    const es1 = try parseBatch(a, first);
    defer a.free(es1);

    // feed every hash back: nothing changed → an empty batch (header only)
    const prev = try a.alloc(Prev, es1.len);
    defer a.free(prev);
    for (es1, prev) |e, *p| p.* = .{ .id = e.id, .hash = e.hash };
    const second = try runLive(a, .{ .live = st, .tc = "00:00:00:00", .prev = prev });
    defer a.free(second);
    try std.testing.expectEqual(@as(usize, out_header_len), second.len);
    const es2 = try parseBatch(a, second);
    defer a.free(es2);
    try std.testing.expectEqual(@as(usize, 0), es2.len);

    // one fragment's bytes change → exactly that fragment comes back
    const third = try runLive(a, .{ .live = st, .tc = "00:00:00:01", .prev = prev });
    defer a.free(third);
    const es3 = try parseBatch(a, third);
    defer a.free(es3);
    try std.testing.expectEqual(@as(usize, 1), es3.len);
    try std.testing.expectEqualStrings("live-tc", es3[0].id);
    try std.testing.expectEqualStrings("00:00:00:01", es3[0].html);
}

test "dedup: an unknown id is always emitted (Go dropped its cache)" {
    const a = std.testing.allocator;
    const prev = [_]Prev{.{ .id = "live-np", .hash = 0xDEAD }};
    const buf = try runLive(a, .{ .live = liveTestState(), .prev = &prev });
    defer a.free(buf);
    const es = try parseBatch(a, buf);
    defer a.free(es);
    try std.testing.expectEqual(@as(usize, 6), es.len); // np's stale hash != its render → all 6
}

test "text fragments are escaped like Go htmlEscape" {
    const a = std.testing.allocator;
    const buf = try runLive(a, .{ .live = liveTestState(), .tc = "a<b&c" });
    defer a.free(buf);
    const es = try parseBatch(a, buf);
    defer a.free(es);
    try std.testing.expectEqualStrings("a&lt;b&amp;c", es[0].html);
}

test "logs batch: one fragment, suppressed on an unchanged tail" {
    const a = std.testing.allocator;
    const st: logs.Lines = .{ .wired = true, .entries = &.{
        .{ .time = "10:00:00", .lvl = "INFO", .cls = "i", .src = "app", .msg = "hello" },
    } };
    const first = try runLogs(a, .{ .lines = st });
    defer a.free(first);
    const es1 = try parseBatch(a, first);
    defer a.free(es1);
    try std.testing.expectEqual(@as(usize, 1), es1.len);
    try std.testing.expectEqualStrings("log-view", es1[0].id);

    const prev = [_]Prev{.{ .id = "log-view", .hash = es1[0].hash }};
    const second = try runLogs(a, .{ .lines = st, .prev = &prev });
    defer a.free(second);
    const es2 = try parseBatch(a, second);
    defer a.free(es2);
    try std.testing.expectEqual(@as(usize, 0), es2.len);
}

test "empty fragment html is a real entry, not a refusal" {
    const a = std.testing.allocator;
    const buf = try runLive(a, .{ .live = .{}, .tc = "" }); // no rec, no sections
    defer a.free(buf);
    const es = try parseBatch(a, buf);
    defer a.free(es);
    try std.testing.expectEqualStrings("live-tc", es[0].id);
    try std.testing.expectEqual(@as(usize, 0), es[0].html.len);
}
