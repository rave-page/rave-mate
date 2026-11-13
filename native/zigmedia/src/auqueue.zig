//! Bounded access-unit queue between the encode thread (producer) and the TX thread (consumer):
//! a byte ring (contiguous AU regions) + a FIFO metadata ring. Sized to absorb >= 150 ms of TX stall
//! at the route bitrate (default 16 MiB / 64 AUs ~ 2 s of a 60 Mbps 4K60 stream) so a hiccup never
//! drops; bounded in BYTES and FRAMES. Allocation-free after init. A short spin lock guards only
//! metadata; the AU memcpy (producer) and the send (consumer) run OUTSIDE the lock.
//! Overflow policy: drop the OLDEST queued AU (FIFO head; never one being sent) and report it so the
//! caller requests an IDR - dropping any AU of an inter-coded GOP breaks the rest of it anyway. If the
//! head is in flight and still no room, the NEW AU is dropped (also reported).
const std = @import("std");

pub const flag_keyframe: u32 = 0x01;

/// spinLock acquires a std.atomic.Mutex (tryLock/unlock only in 0.16). Critical sections are tiny
/// metadata updates (no I/O, no memcpy under the lock).
pub fn spinLock(m: *std.atomic.Mutex) void {
    while (!m.tryLock()) std.atomic.spinLoopHint();
}

const State = enum { free, writing, ready, reading };

pub const Slot = struct {
    buf: []u8 = &.{}, // view into the byte ring (valid while the slot is ready/reading)
    off: usize = 0,
    len: usize = 0,
    pts: i64 = 0,
    flags: u32 = 0,
    state: State = .free,
};

pub const AuQueue = struct {
    alloc: std.mem.Allocator,
    data: []u8,
    meta: []Slot,
    mu: std.atomic.Mutex = .unlocked,
    head: usize = 0, // meta index of the oldest entry
    count: usize = 0,
    tail_off: usize = 0, // next write offset in data
    dropped: u64 = 0,

    /// init: max_frames AUs and cap_bytes of payload in flight.
    pub fn init(alloc: std.mem.Allocator, max_frames: usize, cap_bytes: usize) !AuQueue {
        const data = try alloc.alloc(u8, cap_bytes);
        errdefer alloc.free(data);
        const meta = try alloc.alloc(Slot, max_frames);
        for (meta) |*m| m.* = .{};
        return .{ .alloc = alloc, .data = data, .meta = meta };
    }

    pub fn deinit(q: *AuQueue) void {
        q.alloc.free(q.data);
        q.alloc.free(q.meta);
    }

    /// depth is the number of queued AUs (incl. one in flight).
    pub fn depth(q: *AuQueue) usize {
        spinLock(&q.mu);
        defer q.mu.unlock();
        return q.count;
    }

    fn headOff(q: *const AuQueue) usize {
        return q.meta[q.head].off;
    }

    // reserve finds a contiguous region of n bytes at the ring tail. Caller holds mu.
    fn reserve(q: *AuQueue, n: usize) ?usize {
        if (q.count == 0) {
            q.tail_off = 0;
            return if (n <= q.data.len) 0 else null;
        }
        const h = q.headOff();
        if (q.tail_off >= h) {
            if (q.tail_off + n <= q.data.len) return q.tail_off;
            if (n < h) return 0; // wrap (strictly below head: tail==head means empty)
            return null;
        }
        if (q.tail_off + n < h) return q.tail_off;
        return null;
    }

    fn dropHeadLocked(q: *AuQueue) bool {
        if (q.count == 0) return false;
        const m = &q.meta[q.head];
        if (m.state != .ready) return false; // in flight / being written: never evicted
        m.state = .free;
        q.head = (q.head + 1) % q.meta.len;
        q.count -= 1;
        q.dropped += 1;
        return true;
    }

    /// submit copies one AU in. Non-blocking. Returns true if an AU was DROPPED (the caller fires its
    /// IDR callback).
    pub fn submit(q: *AuQueue, au: []const u8, pts: i64, flags: u32) bool {
        spinLock(&q.mu);
        var dropped = false;
        var off: ?usize = null;
        while (true) {
            if (q.count < q.meta.len) {
                off = q.reserve(au.len);
                if (off != null) break;
            }
            if (!q.dropHeadLocked()) break; // head in flight: cannot make room
            dropped = true;
        }
        const o = off orelse {
            q.dropped += 1; // the NEW AU is dropped
            q.mu.unlock();
            return true;
        };
        const idx = (q.head + q.count) % q.meta.len;
        q.meta[idx] = .{ .buf = q.data[o..][0..au.len], .off = o, .len = au.len, .pts = pts, .flags = flags, .state = .writing };
        q.count += 1;
        q.tail_off = o + au.len;
        q.mu.unlock();

        @memcpy(q.data[o..][0..au.len], au); // outside the lock

        spinLock(&q.mu);
        q.meta[idx].state = .ready;
        q.mu.unlock();
        return dropped;
    }

    /// pop claims the oldest ready AU for sending (null if none). Caller MUST release() it after.
    pub fn pop(q: *AuQueue) ?usize {
        spinLock(&q.mu);
        defer q.mu.unlock();
        if (q.count == 0 or q.meta[q.head].state != .ready) return null;
        q.meta[q.head].state = .reading;
        return q.head;
    }

    pub fn slot(q: *AuQueue, i: usize) *Slot {
        return &q.meta[i];
    }

    /// release frees the AU pop() returned (always the FIFO head).
    pub fn release(q: *AuQueue, i: usize) void {
        spinLock(&q.mu);
        defer q.mu.unlock();
        q.meta[i].state = .free;
        if (i == q.head and q.count > 0) {
            q.head = (q.head + 1) % q.meta.len;
            q.count -= 1;
        }
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

test "absorbs 150 ms of TX stall at 4K60 bitrate without a drop" {
    // 60 Mbps, 60 fps: 150 ms = 9 AUs (one IDR 900 kB + P 150 kB) ~ 2.2 MB << 16 MiB / 64 AUs.
    var q = try AuQueue.init(std.testing.allocator, 64, 16 << 20);
    defer q.deinit();
    const big = try std.testing.allocator.alloc(u8, 900_000);
    defer std.testing.allocator.free(big);
    var i: usize = 0;
    while (i < 30) : (i += 1) { // 500 ms stall: consumer never pops
        const n: usize = if (i % 60 == 0) 900_000 else 150_000;
        try std.testing.expect(!q.submit(big[0..n], @intCast(i), if (i % 60 == 0) flag_keyframe else 0));
    }
    try std.testing.expectEqual(@as(usize, 30), q.depth());
    // Drain in order.
    i = 0;
    while (q.pop()) |s| : (i += 1) {
        try std.testing.expectEqual(@as(i64, @intCast(i)), q.slot(s).pts);
        q.release(s);
    }
    try std.testing.expectEqual(@as(usize, 30), i);
}

test "overflow: drop oldest queued AU; if the oldest is in flight, drop the new one" {
    var q = try AuQueue.init(std.testing.allocator, 8, 100);
    defer q.deinit();
    const blk = [_]u8{7} ** 40;
    try std.testing.expect(!q.submit(&blk, 0, flag_keyframe));
    try std.testing.expect(!q.submit(&blk, 1, 0));
    const inflight = q.pop().?; // AU 0 being sent
    try std.testing.expect(q.submit(&blk, 2, 0)); // no room, head in flight: AU 2 (new) dropped
    q.release(inflight);
    const s1 = q.pop().?;
    try std.testing.expectEqual(@as(i64, 1), q.slot(s1).pts);
    q.release(s1);
    try std.testing.expect(!q.submit(&blk, 3, 0)); // [0,40)
    try std.testing.expect(!q.submit(blk[0..30], 4, 0)); // [40,70)
    try std.testing.expect(q.submit(blk[0..35], 5, 0)); // no room: oldest ready (3) dropped, 5 wraps to [0,35)
    const a = q.pop().?;
    try std.testing.expectEqual(@as(i64, 4), q.slot(a).pts);
    q.release(a);
    const b = q.pop().?;
    try std.testing.expectEqual(@as(i64, 5), q.slot(b).pts);
    q.release(b);
    try std.testing.expectEqual(@as(u64, 2), q.dropped);
}

test "ring wraps to offset 0 below the head" {
    var q = try AuQueue.init(std.testing.allocator, 8, 100);
    defer q.deinit();
    _ = q.submit(&([_]u8{1} ** 40), 0, 0);
    _ = q.submit(&([_]u8{2} ** 40), 1, 0);
    q.release(q.pop().?); // head now AU 1 at [40,80)
    try std.testing.expect(!q.submit(&([_]u8{3} ** 30), 2, 0)); // wraps to [0,30)
    try std.testing.expectEqual(@as(usize, 0), q.slot((q.head + 1) % 8).off);
    q.release(q.pop().?);
    const c = q.pop().?;
    try std.testing.expectEqualSlices(u8, &([_]u8{3} ** 30), q.slot(c).buf);
    q.release(c);
}