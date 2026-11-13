//! Receiver-side reassembly (Zig port of the Go reassembler). Collects a frame's fragments until all
//! FragCount arrive, then yields the contiguous frame wire bytes. Bounded (fixed entry pool, sized at
//! open) - allocation-free in steady state; drop-oldest on overflow; a bounded done-set rejects late
//! stragglers of completed/evicted frames (never resurrects one). The sender fragments at a fixed
//! body_max, so fragment i lands at offset i*body_max (contiguous), the last fragment shorter.
const std = @import("std");

const max_frags = 8192;
const done_ring = 512;

const Entry = struct {
    active: bool = false,
    key: u64 = 0,
    count: u16 = 0,
    have: u16 = 0,
    max_frag: i32 = -1, // highest fragment index received (within-frame hole proof)
    last_len: usize = 0,
    order: u64 = 0,
    last_nack: i64 = 0, // rate-limit NACKs for this frame
    last_rx: i64 = 0, // last fragment arrival (tail-loss proof)
    buf: []u8, // backing (max frame bytes)
    present: []u64, // fragment-present bitmap (max_frags bits)
};

/// NackItem names one missing fragment to request retransmission of (wire: stream, frame_seq, frag_idx).
pub const NackItem = struct { stream: u16, frame_seq: u32, frag_idx: u16 };

const max_streams = 8;
const StreamMax = struct { id: u16 = 0, max: u32 = 0, used: bool = false };

// Ghost: a frame of which NO datagram arrived although a later frame did (a burst loss / overflow
// took the whole frame, so there is no entry to prove a hole in). NACKed as fragment 0: the
// retransmit carries frag_count, then the ordinary overtaken proof NACKs the rest.
const tail_proof_ns: i64 = 4_000_000;
const max_ghosts = 16;
const ghost_ttl_ns: i64 = 1_000_000_000;
const ghost_max_nacks = 20; // the sender's retransmit ring no longer has it after this: stop asking
const Ghost = struct { stream: u16 = 0, fs: u32 = 0, born: i64 = 0, last_nack: i64 = 0, nacks: u8 = 0, active: bool = false };

pub const Reassembler = struct {
    alloc: std.mem.Allocator,
    entries: []Entry,
    body_max: usize,
    done: [done_ring]u64 = [_]u64{0} ** done_ring,
    done_used: [done_ring]bool = [_]bool{false} ** done_ring,
    done_pos: usize = 0,
    next_order: u64 = 1,
    dropped: u64 = 0,
    smax: [max_streams]StreamMax = [_]StreamMax{.{}} ** max_streams, // per-stream high-water frame_seq
    ghosts: [max_ghosts]Ghost = [_]Ghost{.{}} ** max_ghosts,
    now_hint: i64 = 0, // last scanMissing time (ghost birth stamp)

    pub fn init(alloc: std.mem.Allocator, n_entries: usize, body_max: usize, max_frame_bytes: usize) !Reassembler {
        const es = try alloc.alloc(Entry, n_entries);
        errdefer alloc.free(es);
        var made: usize = 0;
        errdefer for (es[0..made]) |e| {
            alloc.free(e.buf);
            alloc.free(e.present);
        };
        for (es) |*e| {
            e.* = .{ .buf = try alloc.alloc(u8, max_frame_bytes), .present = try alloc.alloc(u64, max_frags / 64) };
            made += 1;
        }
        return .{ .alloc = alloc, .entries = es, .body_max = body_max };
    }

    pub fn deinit(r: *Reassembler) void {
        for (r.entries) |e| {
            r.alloc.free(e.buf);
            r.alloc.free(e.present);
        }
        r.alloc.free(r.entries);
    }

    /// accept folds one media fragment in; returns the complete frame's contiguous wire bytes (valid
    /// until the next accept reuses the entry) when the last fragment arrives, else null.
    pub fn accept(r: *Reassembler, stream: u16, frame_seq: u32, frag_idx: u16, frag_count: u16, chunk: []const u8) ?[]const u8 {
        if (frag_count == 0 or frag_count > max_frags or frag_idx >= frag_count) return null;
        const key = (@as(u64, stream) << 32) | frame_seq;
        r.noteFrame(stream, frame_seq); // high-water for overtaken-frame NACK detection
        if (r.isDone(key)) return null;

        var e = r.find(key) orelse r.alloc_entry(key, frag_count);
        if (e.count != frag_count) return null; // inconsistent metadata
        if (bitGet(e.present, frag_idx)) return null; // duplicate

        const off = @as(usize, frag_idx) * r.body_max;
        if (frag_idx == frag_count - 1) {
            if (off + chunk.len > e.buf.len) return null; // oversized frame for the pool
            e.last_len = chunk.len;
        } else {
            if (chunk.len > r.body_max or off + chunk.len > e.buf.len) return null;
        }
        @memcpy(e.buf[off..][0..chunk.len], chunk);
        e.last_rx = r.now_hint;
        bitSet(e.present, frag_idx);
        e.have += 1;
        if (frag_idx > e.max_frag) e.max_frag = frag_idx;

        if (e.have < e.count) return null;
        const total = @as(usize, e.count - 1) * r.body_max + e.last_len;
        r.markDone(key);
        e.active = false; // free the entry; buf stays valid until the next accept reuses it
        return e.buf[0..total];
    }

    /// incompleteBefore reports whether a partially received frame with frame_seq in [lo, hi) is held
    /// (a retransmit can still complete it).
    pub fn incompleteBefore(r: *const Reassembler, lo: u32, hi: u32) bool {
        for (r.ghosts) |g| {
            if (g.active and !seqGreater(lo, g.fs) and seqGreater(hi, g.fs)) return true;
        }
        for (r.entries) |e| {
            if (!e.active or e.have >= e.count) continue;
            const fs: u32 = @truncate(e.key);
            if (!seqGreater(lo, fs) and seqGreater(hi, fs)) return true;
        }
        return false;
    }

    pub fn droppedCount(r: *const Reassembler) u64 {
        return r.dropped;
    }

    fn find(r: *Reassembler, key: u64) ?*Entry {
        for (r.entries) |*e| if (e.active and e.key == key) return e;
        return null;
    }

    fn alloc_entry(r: *Reassembler, key: u64, count: u16) *Entry {
        var pick: ?*Entry = null;
        var oldest: u64 = std.math.maxInt(u64);
        for (r.entries) |*e| {
            if (!e.active) {
                pick = e;
                break;
            }
            if (e.order < oldest) {
                oldest = e.order;
                pick = e;
            }
        }
        const e = pick.?;
        if (e.active) { // evicting an in-flight frame (drop-oldest)
            r.markDone(e.key);
            r.dropped += 1;
        }
        e.active = true;
        e.key = key;
        e.count = count;
        e.have = 0;
        e.max_frag = -1;
        e.last_nack = 0;
        e.last_len = 0;
        e.order = r.next_order;
        r.next_order += 1;
        @memset(e.present, 0);
        return e;
    }

    fn noteFrame(r: *Reassembler, stream: u16, frame_seq: u32) void {
        for (&r.smax) |*s| {
            if (s.used and s.id == stream) {
                if (seqGreater(frame_seq, s.max)) {
                    var g = s.max +% 1;
                    var k: usize = 0;
                    while (seqGreater(frame_seq, g) and k < 8) : ({
                        g +%= 1;
                        k += 1;
                    }) r.addGhost(stream, g);
                    s.max = frame_seq;
                }
                return;
            }
        }
        for (&r.smax) |*s| {
            if (!s.used) {
                s.* = .{ .id = stream, .max = frame_seq, .used = true };
                return;
            }
        }
    }

    fn addGhost(r: *Reassembler, stream: u16, fs: u32) void {
        var pick: usize = 0;
        for (r.ghosts, 0..) |g, i| {
            if (!g.active) {
                pick = i;
                break;
            }
            if (g.born < r.ghosts[pick].born) pick = i; // full: replace the oldest
        }
        r.ghosts[pick] = .{ .stream = stream, .fs = fs, .born = r.now_hint, .active = true };
    }

    fn streamMax(r: *const Reassembler, stream: u16) ?u32 {
        for (r.smax) |s| if (s.used and s.id == stream) return s.max;
        return null;
    }

    /// scanMissing fills out with the missing fragments of incomplete frames that are PROVEN lost,
    /// rate-limited per frame. A hole is proven two ways on a switched LAN (the sender transmits a
    /// frame's fragments in order and a frame fully before the next):
    ///   - WITHIN-FRAME: a later fragment of the SAME frame arrived, so any missing fragment below
    ///     that highest-seen index (max_frag) is a real hole - NACK it immediately, without waiting
    ///     for the next frame. This is the fast path that keeps recovery inside the playout margin.
    ///   - OVERTAKEN: a later frame on the same stream has started, so the whole tail of this frame
    ///     (including fragments above max_frag that never arrived) is proven lost too.
    /// Returns how many items. The caller NACKs them immediately so recovery completes within the
    /// playout margin (one LAN RTT).
    pub fn scanMissing(r: *Reassembler, out: []NackItem, now: i64, min_interval_ns: i64) usize {
        r.now_hint = now;
        var k: usize = 0;
        for (&r.ghosts) |*g| {
            if (!g.active) continue;
            const key = (@as(u64, g.stream) << 32) | g.fs;
            if (r.find(key) != null or r.isDone(key) or g.nacks >= ghost_max_nacks or (g.born != 0 and now - g.born > ghost_ttl_ns)) {
                g.active = false; // arrived (ordinary proof takes over) / completed / given up
                continue;
            }
            if (g.born == 0) g.born = now;
            if (g.last_nack != 0 and now - g.last_nack < min_interval_ns) continue;
            if (k >= out.len) return k;
            out[k] = .{ .stream = g.stream, .frame_seq = g.fs, .frag_idx = 0 };
            k += 1;
            g.last_nack = now;
            g.nacks += 1;
        }
        for (r.entries) |*e| {
            if (!e.active or e.have >= e.count) continue;
            const stream: u16 = @intCast(e.key >> 32);
            const fseq: u32 = @truncate(e.key);
            const mx = r.streamMax(stream) orelse fseq;
            // TAIL proof: the sender paces a frame's fragments back to back; silence on this frame for
            // tail_proof_ns means its missing tail is lost (no need to wait for the next frame, ~1 frame).
            const overtaken = seqGreater(mx, fseq) or (e.last_rx != 0 and now - e.last_rx >= tail_proof_ns);
            // Proven-lost ceiling: the whole frame if overtaken, else only fragments BELOW the highest
            // index seen for this frame (a later fragment of the same frame proves the gap under it).
            const limit: u16 = if (overtaken) e.count else (if (e.max_frag > 0) @intCast(e.max_frag) else 0);
            if (limit == 0) continue; // nothing proven lost yet (newest in-flight frame, no later fragment)
            if (e.last_nack != 0 and now - e.last_nack < min_interval_ns) continue;
            var i: u16 = 0;
            while (i < limit) : (i += 1) {
                if (!bitGet(e.present, i)) {
                    out[k] = .{ .stream = stream, .frame_seq = fseq, .frag_idx = i };
                    k += 1;
                    if (k >= out.len) {
                        e.last_nack = now;
                        return k;
                    }
                }
            }
            e.last_nack = now;
        }
        return k;
    }

    fn isDone(r: *const Reassembler, key: u64) bool {
        for (r.done, 0..) |d, i| if (r.done_used[i] and d == key) return true;
        return false;
    }
    fn markDone(r: *Reassembler, key: u64) void {
        r.done[r.done_pos] = key;
        r.done_used[r.done_pos] = true;
        r.done_pos = (r.done_pos + 1) % done_ring;
    }
};

fn seqGreater(a: u32, b: u32) bool {
    return @as(i32, @bitCast(a -% b)) > 0;
}
fn bitGet(bm: []const u64, i: u16) bool {
    return bm[i / 64] & (@as(u64, 1) << @intCast(i % 64)) != 0;
}
fn bitSet(bm: []u64, i: u16) void {
    bm[i / 64] |= @as(u64, 1) << @intCast(i % 64);
}

test "single-fragment frame" {
    var r = try Reassembler.init(std.testing.allocator, 4, 1200, 4096);
    defer r.deinit();
    const out = r.accept(1, 7, 0, 1, "hello").?;
    try std.testing.expectEqualSlices(u8, "hello", out);
}

test "multi-fragment in order + out of order" {
    var r = try Reassembler.init(std.testing.allocator, 4, 4, 64); // body_max 4
    defer r.deinit();
    // frame of 10 bytes → 3 frags at body_max 4: [4][4][2].
    try std.testing.expectEqual(@as(?[]const u8, null), r.accept(1, 1, 0, 3, "AAAA"));
    try std.testing.expectEqual(@as(?[]const u8, null), r.accept(1, 1, 2, 3, "CC")); // last, out of order
    const out = r.accept(1, 1, 1, 3, "BBBB").?;
    try std.testing.expectEqualSlices(u8, "AAAABBBBCC", out);
    // Straggler of the completed frame is ignored.
    try std.testing.expectEqual(@as(?[]const u8, null), r.accept(1, 1, 0, 3, "AAAA"));
}

test "duplicate fragment ignored" {
    var r = try Reassembler.init(std.testing.allocator, 4, 4, 64);
    defer r.deinit();
    _ = r.accept(2, 5, 0, 2, "XXXX");
    _ = r.accept(2, 5, 0, 2, "XXXX"); // dup
    const out = r.accept(2, 5, 1, 2, "YY").?;
    try std.testing.expectEqualSlices(u8, "XXXXYY", out);
}

test "scanMissing NACKs a within-frame hole immediately (later fragment proves it), rate-limited" {
    var r = try Reassembler.init(std.testing.allocator, 4, 4, 64);
    defer r.deinit();
    var items: [8]NackItem = undefined;
    _ = r.accept(1, 1, 0, 3, "AAAA"); // only frag 0 of frame 1 so far
    try std.testing.expectEqual(@as(usize, 0), r.scanMissing(&items, 1000, 0)); // nothing below frag 0 → nothing proven
    _ = r.accept(1, 1, 2, 3, "CC"); // frag 2 arrives → frag 1 (below the highest seen) is a proven hole
    const k = r.scanMissing(&items, 2000, 0);
    try std.testing.expectEqual(@as(usize, 1), k); // frag 1 only (frag 2 present; frag 2 is the ceiling, not proven-above)
    try std.testing.expectEqual(@as(u32, 1), items[0].frame_seq);
    try std.testing.expectEqual(@as(u16, 1), items[0].frag_idx);
    try std.testing.expectEqual(@as(usize, 0), r.scanMissing(&items, 2001, 1_000_000)); // rate-limited
}

test "scanMissing NACKs an overtaken frame's whole tail" {
    var r = try Reassembler.init(std.testing.allocator, 4, 4, 64);
    defer r.deinit();
    var items: [8]NackItem = undefined;
    _ = r.accept(1, 1, 0, 3, "AAAA"); // frame 1: only frag 0; no later fragment of frame 1 → tail not yet proven
    try std.testing.expectEqual(@as(usize, 0), r.scanMissing(&items, 1000, 0));
    _ = r.accept(1, 2, 0, 2, "DDDD"); // frame 2 starts → frame 1 overtaken → frags 1 AND 2 proven lost
    const k = r.scanMissing(&items, 2000, 0);
    try std.testing.expectEqual(@as(usize, 2), k);
    try std.testing.expectEqual(@as(u16, 1), items[0].frag_idx);
    try std.testing.expectEqual(@as(u16, 2), items[1].frag_idx);
}

test "drop-oldest on pool overflow" {
    var r = try Reassembler.init(std.testing.allocator, 2, 4, 64);
    defer r.deinit();
    _ = r.accept(1, 1, 0, 2, "AAAA"); // entry 1 (incomplete)
    _ = r.accept(1, 2, 0, 2, "BBBB"); // entry 2 (incomplete)
    _ = r.accept(1, 3, 0, 2, "CCCC"); // evicts the oldest (frame 1)
    try std.testing.expect(r.droppedCount() >= 1);
    // frame 1 was dropped → its completion now starts fresh but is marked done (evicted) → ignored.
    try std.testing.expectEqual(@as(?[]const u8, null), r.accept(1, 1, 1, 2, "ZZ"));
}

test "wholly-missing frame (no datagram) is NACKed as fragment 0" {
    var r = try Reassembler.init(std.testing.allocator, 4, 4, 64);
    defer r.deinit();
    var items: [8]NackItem = undefined;
    _ = r.accept(1, 1, 0, 1, "AA"); // frame 1 complete
    _ = r.accept(1, 3, 0, 2, "CCCC"); // frame 3 starts; frame 2 never seen at all
    const k = r.scanMissing(&items, 1000, 0);
    var saw2 = false;
    for (items[0..k]) |it| if (it.frame_seq == 2 and it.frag_idx == 0) {
        saw2 = true;
    };
    try std.testing.expect(saw2);
    try std.testing.expect(r.incompleteBefore(2, 3));
    _ = r.accept(1, 2, 0, 1, "BB"); // retransmit completes frame 2 -> ghost retired
    try std.testing.expectEqual(@as(usize, 0), r.scanMissing(&items, 2000, 0) - (if (r.scanMissing(&items, 2000, 0) > 0) @as(usize, 0) else 0));
    try std.testing.expect(!r.incompleteBefore(2, 3));
}
