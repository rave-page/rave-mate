//! Bounded access-unit queue between the encode thread (producer) and the TX thread (consumer). This
//! replaces the single-slot AU hand-off that DROPS today (the 2026-11-12 finding: the Go side
//! overwrote the one slot whenever packetize/pace/send stalled past a frame period). Fixed pools sized
//! at open — allocation-free in steady state. A short mutex guards only the slot METADATA; the AU
//! memcpy (producer) and the send (consumer) run OUTSIDE the lock, so neither realtime thread blocks
//! on the other's data work. Full queue drops the OLDEST NON-KEYFRAME (keeps the decodable keyframe)
//! and signals the caller to request an IDR.
const std = @import("std");

pub const flag_keyframe: u32 = 0x01;

/// spinLock acquires a std.atomic.Mutex (tryLock/unlock only in 0.16). Critical sections here are tiny
/// O(N) metadata scans (no I/O, no memcpy under the lock), so a spin is cheaper than a futex.
pub fn spinLock(m: *std.atomic.Mutex) void {
    while (!m.tryLock()) std.atomic.spinLoopHint();
}

const State = enum { free, writing, ready, reading };

const Slot = struct {
    buf: []u8,
    len: usize = 0,
    pts: i64 = 0,
    flags: u32 = 0,
    order: u64 = 0,
    state: State = .free,
};

pub const AuQueue = struct {
    alloc: std.mem.Allocator,
    slots: []Slot,
    mu: std.atomic.Mutex = .unlocked,
    next_order: u64 = 0,
    dropped: u64 = 0,

    pub fn init(alloc: std.mem.Allocator, n_slots: usize, slot_cap: usize) !AuQueue {
        const slots = try alloc.alloc(Slot, n_slots);
        errdefer alloc.free(slots);
        var made: usize = 0;
        errdefer for (slots[0..made]) |s| alloc.free(s.buf);
        for (slots) |*s| {
            s.* = .{ .buf = try alloc.alloc(u8, slot_cap) };
            made += 1;
        }
        return .{ .alloc = alloc, .slots = slots };
    }

    pub fn deinit(q: *AuQueue) void {
        for (q.slots) |s| q.alloc.free(s.buf);
        q.alloc.free(q.slots);
    }

    /// submit copies one AU into a slot. Non-blocking. Returns true if an AU was DROPPED to make room
    /// (the caller fires its IDR callback). An AU larger than slot_cap is truncated (sizing bug — size
    /// slot_cap from the bitrate at open); never overflows.
    pub fn submit(q: *AuQueue, au: []const u8, pts: i64, flags: u32) bool {
        spinLock(&q.mu);
        var dropped = false;
        var idx = q.findFree();
        if (idx == null) {
            idx = q.evictOldest(); // prefer non-keyframe
            dropped = true;
            q.dropped += 1;
        }
        const i = idx.?;
        q.slots[i].state = .writing;
        q.mu.unlock();

        const n = @min(au.len, q.slots[i].buf.len);
        @memcpy(q.slots[i].buf[0..n], au[0..n]); // outside the lock
        q.slots[i].len = n;
        q.slots[i].pts = pts;
        q.slots[i].flags = flags;

        spinLock(&q.mu);
        q.slots[i].order = q.next_order;
        q.next_order += 1;
        q.slots[i].state = .ready;
        q.mu.unlock();
        return dropped;
    }

    /// pop claims the oldest ready slot for sending (null if none). Caller MUST release() it after.
    pub fn pop(q: *AuQueue) ?usize {
        spinLock(&q.mu);
        defer q.mu.unlock();
        var best: ?usize = null;
        var best_order: u64 = std.math.maxInt(u64);
        for (q.slots, 0..) |s, i| {
            if (s.state == .ready and s.order < best_order) {
                best = i;
                best_order = s.order;
            }
        }
        if (best) |i| q.slots[i].state = .reading;
        return best;
    }

    pub fn slot(q: *AuQueue, i: usize) *Slot {
        return &q.slots[i];
    }

    pub fn release(q: *AuQueue, i: usize) void {
        spinLock(&q.mu);
        q.slots[i].state = .free;
        q.mu.unlock();
    }

    fn findFree(q: *AuQueue) ?usize {
        for (q.slots, 0..) |s, i| if (s.state == .free) return i;
        return null;
    }

    // evictOldest frees the oldest READY slot, preferring a non-keyframe; returns its index. Reading/
    // writing slots are never evicted (in use). Caller holds mu.
    fn evictOldest(q: *AuQueue) ?usize {
        var pick: ?usize = null;
        var pick_order: u64 = std.math.maxInt(u64);
        var pick_kf = true;
        for (q.slots, 0..) |s, i| {
            if (s.state != .ready) continue;
            const kf = s.flags & flag_keyframe != 0;
            // Prefer non-keyframe; among same class, the oldest.
            const better = (pick == null) or (!kf and pick_kf) or (kf == pick_kf and s.order < pick_order);
            if (better) {
                pick = i;
                pick_order = s.order;
                pick_kf = kf;
            }
        }
        return pick;
    }
};

test "submit/pop FIFO" {
    var q = try AuQueue.init(std.testing.allocator, 4, 64);
    defer q.deinit();
    _ = q.submit("a", 1, 0);
    _ = q.submit("bb", 2, 0);
    const s0 = q.pop().?;
    try std.testing.expectEqualSlices(u8, "a", q.slot(s0).buf[0..q.slot(s0).len]);
    q.release(s0);
    const s1 = q.pop().?;
    try std.testing.expectEqualSlices(u8, "bb", q.slot(s1).buf[0..q.slot(s1).len]);
    q.release(s1);
    try std.testing.expectEqual(@as(?usize, null), q.pop());
}

test "full queue drops oldest non-keyframe, keeps keyframe" {
    var q = try AuQueue.init(std.testing.allocator, 2, 16);
    defer q.deinit();
    try std.testing.expect(!q.submit("K", 1, flag_keyframe)); // keyframe
    try std.testing.expect(!q.submit("p1", 2, 0)); // delta
    // Full. Next submit must drop the delta (p1), keep the keyframe, and signal a drop.
    try std.testing.expect(q.submit("p2", 3, 0));
    try std.testing.expectEqual(@as(u64, 1), q.dropped);
    // The two survivors are the keyframe + p2 (p1 gone).
    const a = q.pop().?;
    const sa = q.slot(a).buf[0..q.slot(a).len];
    q.release(a);
    const b = q.pop().?;
    const sb = q.slot(b).buf[0..q.slot(b).len];
    q.release(b);
    try std.testing.expect(std.mem.eql(u8, sa, "K") or std.mem.eql(u8, sb, "K"));
    try std.testing.expect(!std.mem.eql(u8, sa, "p1") and !std.mem.eql(u8, sb, "p1"));
}
