//! Retained-doc delta channel (phase B7 increment ii) — the ONE stateful corner of this lib.
//!
//! B3 decided every `rz_ui_render_*` export is a pure fn(state)→html with zero cross-call state,
//! and that stays true for all of them. This module is a parallel, opt-in tier for high-cadence
//! patch sites: Zig keeps the last-decoded state per slot, Go sends only the field trees that
//! changed (RZD1), the merge happens here and the render runs off the merged state. Stateless is
//! still the default AND the fallback - every doubt declines back to it.
//!
//! Why it cannot corrupt anything:
//!   - state lives in an explicit SLOT TABLE with a fixed cap (`max_slots`), never a global or a
//!     singleton, so the app UI, a headless mirror and parallel tests never alias
//!   - a handle is {index:32, gen:32}; freeing bumps gen, so a stale handle is DETECTED
//!     (gen mismatch → decline), never used
//!   - a slot is bound to ONE root message id; a document for another message declines before
//!     the type-erased state pointer is cast (the cast is the reason that check is not optional)
//!   - patch-then-swap: the merge runs into a CLONE in a scratch arena and only replaces the
//!     retained state when merge + hash + render all succeeded. A failed merge drops the slot;
//!     it can never leave a half-merged state behind (the lost-patch render race's discipline)
//!   - the merged state's fingerprint must equal the one Go computed for the same state. A
//!     Go/Zig codec divergence therefore declines on the spot instead of drifting
//!   - each slot owns its arena; the per-slot cap is measured on the state's logical size and a
//!     breach DROPS the slot (Go's counter turns three breaches into sticky-stateless)

const std = @import("std");
const wire = @import("wire.zig");

/// Status codes returned through the ABI. Contract with internal/zigui PatchStatus - append only.
pub const st_ok: u8 = 0;
pub const st_malformed: u8 = 1;
pub const st_desync: u8 = 2;
pub const st_cap: u8 = 3;
pub const st_error: u8 = 4;

/// Slot-table cap. 64 = one handle per (UI instance × patch target) with room for a headless
/// mirror and the test suite; past it `retainNew` refuses and the caller stays stateless.
pub const max_slots = 64;

/// Per-slot cap on the retained state's LOGICAL size (string bytes + 8 per scalar, as measured by
/// wire.Hasher). The biggest real candidate is the 400-line log tail at ~55 kB; 512 KiB leaves an
/// order of magnitude of headroom while keeping the table's worst case bounded at 32 MiB of
/// retained state. A breach drops the slot → full-doc resend (and Go stops retrying that surface).
pub const max_slot_bytes: u64 = 512 * 1024;

const Slot = struct {
    used: bool = false,
    gen: u32 = 1, // starts at 1: a zeroed handle can never match a live slot
    msg_id: u16 = 0,
    state: ?*anyopaque = null, // type-erased; msg_id is what makes the cast safe
    arena: ?*std.heap.ArenaAllocator = null,
    hash: u64 = 0, // fingerprint of the retained state (a delta's base_hash must match)
    bytes: u64 = 0, // logical size, against max_slot_bytes
    locale_gen: u32 = 0, // i18n generation the slot was seeded under
};

var slots: [max_slots]Slot = @splat(.{});

/// pack/unpack the {index:32, gen:32} handle. 0 is never a valid handle.
fn pack(idx: usize, gen: u32) u64 {
    return (@as(u64, @intCast(idx)) << 32) | gen;
}

fn slotOf(h: u64) ?*Slot {
    if (h == 0) return null;
    const idx: usize = @intCast(h >> 32);
    const gen: u32 = @truncate(h);
    if (idx >= max_slots) return null;
    const s = &slots[idx];
    if (!s.used or s.gen != gen) return null;
    return s;
}

/// retainNew claims a slot for root message msg_id. 0 = table full (caller stays stateless).
pub fn retainNew(msg_id: u16) u64 {
    for (&slots, 0..) |*s, i| {
        if (s.used) continue;
        s.used = true;
        s.msg_id = msg_id;
        s.state = null;
        s.arena = null;
        s.hash = 0;
        s.bytes = 0;
        s.locale_gen = 0;
        return pack(i, s.gen);
    }
    return 0;
}

/// release frees a slot's retained state and bumps its generation, so every handle that named it
/// is now stale. Idempotent: an unknown/stale handle is a no-op.
pub fn release(h: u64) void {
    const s = slotOf(h) orelse return;
    dropState(s);
    s.used = false;
    s.gen +%= 1;
    if (s.gen == 0) s.gen = 1;
}

fn dropState(s: *Slot) void {
    if (s.arena) |ar| {
        ar.deinit();
        std.heap.c_allocator.destroy(ar);
    }
    s.arena = null;
    s.state = null;
    s.hash = 0;
    s.bytes = 0;
    s.locale_gen = 0;
}

/// Stats is what `rz_ui_retain_stats` reports (live slot accounting for `ctl perf`).
pub const Stats = struct {
    live: u32 = 0,
    seeded: u32 = 0,
    bytes: u64 = 0,
};

pub fn stats() Stats {
    var out = Stats{};
    for (&slots) |*s| {
        if (!s.used) continue;
        out.live += 1;
        if (s.state != null) {
            out.seeded += 1;
            out.bytes += s.bytes;
        }
    }
    return out;
}

/// Result of one patch call: the rendered bytes (owned by the caller's allocator) + a status.
pub fn Result(comptime Out: type) type {
    return struct { out: ?Out = null, status: u8 };
}

/// patch drives one retained surface. StateT is the root message's state type; the four generated
/// walkers (merge/clone/hash) plus the surface's own producer come in as comptime fns, so this
/// single body serves every opted-in surface and there is exactly ONE copy of the state machine.
///
/// produceFn renders (or schedules) off the MERGED state and returns an owned buffer from `a`.
pub fn patch(
    comptime StateT: type,
    comptime mergeFn: fn (*wire.Reader, *StateT) wire.Error!void,
    comptime cloneFn: fn (std.mem.Allocator, StateT) wire.Error!StateT,
    comptime hashFn: fn (*wire.Hasher, StateT) void,
    comptime produceFn: fn (std.mem.Allocator, StateT) anyerror![]u8,
    comptime msg_id: u16,
    schema_hash: u32,
    a: std.mem.Allocator,
    buf: []const u8,
) Result([]u8) {
    const hdr = wire.parseDeltaHeader(msg_id, schema_hash, buf) catch
        return .{ .status = st_malformed };
    const s = slotOf(hdr.handle) orelse return .{ .status = st_desync };
    if (s.msg_id != msg_id) return .{ .status = st_desync }; // the cast below relies on this

    const seeding = hdr.kind == wire.kind_seed;
    if (!seeding) {
        // Never delta from an unseeded slot, across a locale switch, or off a state Go does not
        // believe we hold. Each of these drops the slot so the next send is a full-doc reseed.
        if (s.state == null or s.hash != hdr.base_hash or s.locale_gen != hdr.locale_gen) {
            dropState(s);
            return .{ .status = st_desync };
        }
    }

    // Scratch arena: the merge target. Nothing about the retained state changes until the swap.
    const ar = std.heap.c_allocator.create(std.heap.ArenaAllocator) catch
        return .{ .status = st_error };
    ar.* = std.heap.ArenaAllocator.init(std.heap.c_allocator);
    var keep = false;
    defer if (!keep) {
        ar.deinit();
        std.heap.c_allocator.destroy(ar);
    };
    const sa = ar.allocator();

    var next: StateT = .{};
    if (!seeding) {
        const cur: *StateT = @ptrCast(@alignCast(s.state.?));
        next = cloneFn(sa, cur.*) catch |e| return .{ .status = errStatus(e) };
    }
    var r = wire.Reader{ .buf = hdr.body, .arena = hdr.arena, .a = sa };
    mergeFn(&r, &next) catch |e| {
        dropState(s);
        return .{ .status = errStatus(e) };
    };
    if (r.pos != hdr.body.len) { // a body must end exactly on its terminator
        dropState(s);
        return .{ .status = st_malformed };
    }

    var h = wire.Hasher{};
    hashFn(&h, next);
    if (h.sum() != hdr.new_hash) { // Go and Zig disagree about the merged state - refuse it
        dropState(s);
        return .{ .status = st_desync };
    }
    if (h.n > max_slot_bytes) {
        dropState(s);
        return .{ .status = st_cap };
    }

    const out = produceFn(a, next) catch |e| {
        dropState(s);
        return .{ .status = errStatus(e) };
    };

    // Commit: the retained state becomes the merged one and the old arena goes away wholesale.
    const held = sa.create(StateT) catch {
        a.free(out);
        dropState(s);
        return .{ .status = st_error };
    };
    held.* = next;
    dropState(s);
    s.state = held;
    s.arena = ar;
    s.hash = h.sum();
    s.bytes = h.n;
    s.locale_gen = hdr.locale_gen;
    keep = true;
    return .{ .out = out, .status = st_ok };
}

fn errStatus(e: anyerror) u8 {
    return switch (e) {
        error.Malformed => st_malformed,
        error.OutOfMemory => st_error,
        else => st_error,
    };
}

// ── tests: the slot machine on its own (the codec is pinned by the Go-side sequence goldens) ──

const TState = struct { s: []const u8 = "", n: bool = false };

fn tMerge(r: *wire.Reader, out: *TState) wire.Error!void {
    while (try r.next()) |t| switch (t.field) {
        wire.clear_field => switch (try r.uint(t)) {
            1 => out.s = "",
            2 => out.n = false,
            else => {},
        },
        1 => out.s = try wire.strDup(r, t),
        2 => out.n = try r.boolean(t),
        else => try r.skip(t),
    };
}

fn tClone(a: std.mem.Allocator, v: TState) wire.Error!TState {
    var out = v;
    out.s = try a.dupe(u8, v.s);
    return out;
}

fn tHash(h: *wire.Hasher, v: TState) void {
    h.str(1, v.s);
    h.boolean(2, v.n);
}

fn tProduce(a: std.mem.Allocator, v: TState) anyerror![]u8 {
    return std.fmt.allocPrint(a, "{s}/{}", .{ v.s, v.n });
}

const t_msg: u16 = 4242;
const t_hash: u32 = 0xABCD1234;

/// Doc builder for RZD1 (mirrors internal/zigui NewDeltaWriter; tests only).
const DDoc = struct {
    arena: std.ArrayList(u8) = .empty,
    body: std.ArrayList(u8) = .empty,
    a: std.mem.Allocator,

    fn init(a: std.mem.Allocator) DDoc {
        return .{ .a = a };
    }
    fn deinit(d: *DDoc) void {
        d.arena.deinit(d.a);
        d.body.deinit(d.a);
    }
    fn uv(d: *DDoc, v0: u64) !void {
        var v = v0;
        while (v >= 0x80) : (v >>= 7) try d.body.append(d.a, @as(u8, @truncate(v)) | 0x80);
        try d.body.append(d.a, @truncate(v));
    }
    fn tag(d: *DDoc, num: u32, wt: u3) !void {
        try d.uv(@as(u64, num) << 3 | wt);
    }
    fn str(d: *DDoc, num: u32, s: []const u8) !void {
        const off = d.arena.items.len;
        try d.arena.appendSlice(d.a, s);
        try d.tag(num, wire.wt_string);
        try d.uv(off);
        try d.uv(s.len);
    }
    fn boolean(d: *DDoc, num: u32) !void {
        try d.tag(num, wire.wt_varint);
        try d.uv(1);
    }
    fn clear(d: *DDoc, num: u32) !void {
        try d.tag(wire.clear_field, wire.wt_varint);
        try d.uv(num);
    }
    fn finish(d: *DDoc, kind: u8, handle: u64, base: u64, new: u64, locale: u32) ![]u8 {
        var out: std.ArrayList(u8) = .empty;
        try out.appendSlice(d.a, wire.delta_magic);
        var hdr: [39]u8 = undefined;
        std.mem.writeInt(u16, hdr[0..2], t_msg, .little);
        std.mem.writeInt(u32, hdr[2..6], t_hash, .little);
        std.mem.writeInt(u32, hdr[6..10], @intCast(d.arena.items.len), .little);
        hdr[10] = kind;
        std.mem.writeInt(u64, hdr[11..19], handle, .little);
        std.mem.writeInt(u64, hdr[19..27], base, .little);
        std.mem.writeInt(u64, hdr[27..35], new, .little);
        std.mem.writeInt(u32, hdr[35..39], locale, .little);
        try out.appendSlice(d.a, &hdr);
        try out.appendSlice(d.a, d.arena.items);
        try out.appendSlice(d.a, d.body.items);
        try out.append(d.a, 0);
        return out.toOwnedSlice(d.a);
    }
};

fn tStateHash(v: TState) u64 {
    var h = wire.Hasher{};
    tHash(&h, v);
    return h.sum();
}

fn runPatch(a: std.mem.Allocator, buf: []const u8) Result([]u8) {
    return patch(TState, tMerge, tClone, tHash, tProduce, t_msg, t_hash, a, buf);
}

test "retain: seed then delta, absent keeps, clear resets" {
    const a = std.testing.allocator;
    const h = retainNew(t_msg);
    defer release(h);
    try std.testing.expect(h != 0);

    { // seed: full state
        var d = DDoc.init(a);
        defer d.deinit();
        try d.str(1, "hello");
        try d.boolean(2);
        const buf = try d.finish(wire.kind_seed, h, 0, tStateHash(.{ .s = "hello", .n = true }), 7);
        defer a.free(buf);
        const res = runPatch(a, buf);
        try std.testing.expectEqual(st_ok, res.status);
        defer a.free(res.out.?);
        try std.testing.expectEqualStrings("hello/true", res.out.?);
    }
    { // delta: only field 1 - the bool must SURVIVE (absent = keep)
        var d = DDoc.init(a);
        defer d.deinit();
        try d.str(1, "world");
        const base = tStateHash(.{ .s = "hello", .n = true });
        const buf = try d.finish(wire.kind_delta, h, base, tStateHash(.{ .s = "world", .n = true }), 7);
        defer a.free(buf);
        const res = runPatch(a, buf);
        try std.testing.expectEqual(st_ok, res.status);
        defer a.free(res.out.?);
        try std.testing.expectEqualStrings("world/true", res.out.?);
    }
    { // clear: field 2 back to false, field 1 untouched
        var d = DDoc.init(a);
        defer d.deinit();
        try d.clear(2);
        const base = tStateHash(.{ .s = "world", .n = true });
        const buf = try d.finish(wire.kind_delta, h, base, tStateHash(.{ .s = "world", .n = false }), 7);
        defer a.free(buf);
        const res = runPatch(a, buf);
        try std.testing.expectEqual(st_ok, res.status);
        defer a.free(res.out.?);
        try std.testing.expectEqualStrings("world/false", res.out.?);
    }
}

test "retain: guard rejections drop the slot and force a reseed" {
    const a = std.testing.allocator;
    const h = retainNew(t_msg);
    defer release(h);

    const seed = blk: {
        var d = DDoc.init(a);
        defer d.deinit();
        try d.str(1, "a");
        break :blk try d.finish(wire.kind_seed, h, 0, tStateHash(.{ .s = "a" }), 3);
    };
    defer a.free(seed);

    { // delta before any seed → desync
        var d = DDoc.init(a);
        defer d.deinit();
        try d.str(1, "b");
        const buf = try d.finish(wire.kind_delta, h, tStateHash(.{ .s = "a" }), tStateHash(.{ .s = "b" }), 3);
        defer a.free(buf);
        try std.testing.expectEqual(st_desync, runPatch(a, buf).status);
    }
    { // seed, then a WRONG base hash → desync + slot dropped
        const r0 = runPatch(a, seed);
        try std.testing.expectEqual(st_ok, r0.status);
        a.free(r0.out.?);
        var d = DDoc.init(a);
        defer d.deinit();
        try d.str(1, "b");
        const buf = try d.finish(wire.kind_delta, h, 0xDEAD, tStateHash(.{ .s = "b" }), 3);
        defer a.free(buf);
        try std.testing.expectEqual(st_desync, runPatch(a, buf).status);
        // slot dropped: a correctly-based delta now also declines
        var d2 = DDoc.init(a);
        defer d2.deinit();
        try d2.str(1, "b");
        const buf2 = try d2.finish(wire.kind_delta, h, tStateHash(.{ .s = "a" }), tStateHash(.{ .s = "b" }), 3);
        defer a.free(buf2);
        try std.testing.expectEqual(st_desync, runPatch(a, buf2).status);
    }
    { // locale generation moved on → desync
        const r0 = runPatch(a, seed);
        try std.testing.expectEqual(st_ok, r0.status);
        a.free(r0.out.?);
        var d = DDoc.init(a);
        defer d.deinit();
        try d.str(1, "b");
        const buf = try d.finish(wire.kind_delta, h, tStateHash(.{ .s = "a" }), tStateHash(.{ .s = "b" }), 4);
        defer a.free(buf);
        try std.testing.expectEqual(st_desync, runPatch(a, buf).status);
    }
    { // Go/Zig fingerprint disagreement → desync (a codec divergence declines on the spot)
        const r0 = runPatch(a, seed);
        try std.testing.expectEqual(st_ok, r0.status);
        a.free(r0.out.?);
        var d = DDoc.init(a);
        defer d.deinit();
        try d.str(1, "b");
        const buf = try d.finish(wire.kind_delta, h, tStateHash(.{ .s = "a" }), 0x1234, 3);
        defer a.free(buf);
        try std.testing.expectEqual(st_desync, runPatch(a, buf).status);
    }
}

test "retain: stale + foreign handles decline, never UB" {
    const a = std.testing.allocator;
    const h = retainNew(t_msg);
    var d = DDoc.init(a);
    defer d.deinit();
    try d.str(1, "a");
    const buf = try d.finish(wire.kind_seed, h, 0, tStateHash(.{ .s = "a" }), 1);
    defer a.free(buf);
    const r0 = runPatch(a, buf);
    try std.testing.expectEqual(st_ok, r0.status);
    a.free(r0.out.?);

    release(h); // the handle is now stale (generation bumped)
    try std.testing.expectEqual(st_desync, runPatch(a, buf).status);
    release(h); // idempotent
    // handle 0, an out-of-range index and a foreign message id all decline
    var d2 = DDoc.init(a);
    defer d2.deinit();
    const zero = try d2.finish(wire.kind_seed, 0, 0, tStateHash(.{}), 1);
    defer a.free(zero);
    try std.testing.expectEqual(st_desync, runPatch(a, zero).status);
    var d3 = DDoc.init(a);
    defer d3.deinit();
    const far = try d3.finish(wire.kind_seed, pack(max_slots + 5, 1), 0, tStateHash(.{}), 1);
    defer a.free(far);
    try std.testing.expectEqual(st_desync, runPatch(a, far).status);
}

test "retain: slot table is capped, and freeing makes room again" {
    var hs: [max_slots]u64 = undefined;
    for (&hs) |*x| {
        x.* = retainNew(t_msg);
        try std.testing.expect(x.* != 0);
    }
    try std.testing.expectEqual(@as(u64, 0), retainNew(t_msg)); // full
    try std.testing.expectEqual(@as(u32, max_slots), stats().live);
    release(hs[7]);
    const again = retainNew(t_msg);
    try std.testing.expect(again != 0);
    try std.testing.expect(again != hs[7]); // reused index, bumped generation
    release(again);
    for (&hs, 0..) |x, i| if (i != 7) release(x);
    try std.testing.expectEqual(@as(u32, 0), stats().live);
}

test "retain: an RZW1 document is refused by the patch path" {
    const a = std.testing.allocator;
    const h = retainNew(t_msg);
    defer release(h);
    var buf: [64]u8 = @splat(0);
    @memcpy(buf[0..4], wire.magic); // RZW1, not RZD1
    std.mem.writeInt(u16, buf[4..6], t_msg, .little);
    std.mem.writeInt(u32, buf[6..10], t_hash, .little);
    try std.testing.expectEqual(st_malformed, runPatch(a, &buf).status);
}
