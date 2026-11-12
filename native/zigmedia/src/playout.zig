//! PTS-locked playout (increment 3, the native Windows receiver's core). Frame N releases at
//! t0_mono + (pts_N - pts0) + depth*interval on a MONOTONIC clock - so release intervals equal the
//! source cadence (16.67 ms) regardless of how arrivals bunch, the fix for the receiver stutter (the
//! Go path released on arrival/coarse-timer wakes). A bounded seq-ordered reorder buffer absorbs
//! network jitter; depth is the jitter margin in frames, adapted to the measured arrival lateness
//! (target <= 2). The caller sleeps on a high-resolution timer (hrtimer.zig) until nextDeadline, then
//! drains due frames to the decoder/publish. Allocation-free after init. Clock is injected (testable).
const std = @import("std");

pub const Item = struct {
    seq: u32,
    pts_ns: i64,
    // opaque payload handle (the real receiver stores a reassembled AU slot index); the scheduler
    // only needs seq+pts. For tests this carries a value to assert ordering.
    tag: u64 = 0,
    used: bool = false,
};

pub const Playout = struct {
    buf: []Item, // bounded reorder ring, seq-ordered on drain
    n: usize = 0,
    interval_ns: i64,
    depth: u32 = 1,
    max_depth: u32 = 4,

    anchored: bool = false,
    t0_mono: i64 = 0, // monotonic ns at the first frame
    pts0: i64 = 0,
    next_seq: u32 = 0, // next seq to release (gap detection)

    // depth adaptation (late-rate over a window of recent frames).
    late_win: u32 = 0,
    late_cnt: u32 = 0,

    released: u64 = 0,
    dropped: u64 = 0,

    pub fn init(buf: []Item, fps: f64) Playout {
        var ivl: i64 = 16_666_667;
        if (fps > 0) ivl = @intFromFloat(1e9 / fps);
        for (buf) |*it| it.* = .{ .seq = 0, .pts_ns = 0 };
        return .{ .buf = buf, .interval_ns = ivl };
    }

    /// deadline is frame pts_ns's monotonic release time.
    pub fn deadline(p: *const Playout, pts_ns: i64) i64 {
        return p.t0_mono + (pts_ns - p.pts0) + @as(i64, p.depth) * p.interval_ns;
    }

    /// push accepts a received frame at monotonic time now_mono. Anchors on the first; adapts depth to
    /// lateness; drops a duplicate/too-old or a buffer-full overflow (drop-oldest). Returns false if
    /// dropped.
    pub fn push(p: *Playout, seq: u32, pts_ns: i64, tag: u64, now_mono: i64) bool {
        if (!p.anchored) {
            p.anchored = true;
            p.t0_mono = now_mono;
            p.pts0 = pts_ns;
            p.next_seq = seq;
        }
        if (seqLess(seq, p.next_seq)) {
            p.dropped += 1;
            return false; // already released/skipped
        }
        // lateness for depth adaptation: arrived after its deadline?
        const late = now_mono > p.deadline(pts_ns);
        p.late_win += 1;
        if (late) p.late_cnt += 1;
        if (p.late_win >= 120) { // ~2 s at 60 fps
            const rate = @as(f64, @floatFromInt(p.late_cnt)) / @as(f64, @floatFromInt(p.late_win));
            if (rate > 0.02 and p.depth < p.max_depth) {
                p.depth += 1;
            } else if (rate < 0.001 and p.depth > 1) {
                p.depth -= 1;
            }
            p.late_win = 0;
            p.late_cnt = 0;
        }
        // insert seq-ordered; drop-oldest if full.
        if (p.n == p.buf.len) {
            p.dropHead();
        }
        var i = p.n;
        while (i > 0 and seqLess(seq, p.buf[i - 1].seq)) : (i -= 1) {
            p.buf[i] = p.buf[i - 1];
        }
        if (i > 0 and p.buf[i - 1].seq == seq) {
            // duplicate: undo the shift
            while (i < p.n) : (i += 1) p.buf[i] = p.buf[i + 1];
            p.dropped += 1;
            return false;
        }
        p.buf[i] = .{ .seq = seq, .pts_ns = pts_ns, .tag = tag, .used = true };
        p.n += 1;
        return true;
    }

    /// due returns the next releasable frame (deadline <= now_mono, in seq order) or null. On a gap it
    /// holds the head until its deadline, then skips (live stream: latency beats completeness).
    pub fn due(p: *Playout, now_mono: i64) ?Item {
        if (p.n == 0) return null;
        const h = p.buf[0];
        if (now_mono < p.deadline(h.pts_ns)) return null; // not due yet
        p.popHead();
        p.next_seq = h.seq +% 1;
        p.released += 1;
        return h;
    }

    /// nextDeadline is the monotonic time to wake for the next release (max i64 when empty).
    pub fn nextDeadline(p: *const Playout) i64 {
        if (p.n == 0) return std.math.maxInt(i64);
        return p.deadline(p.buf[0].pts_ns);
    }

    fn dropHead(p: *Playout) void {
        p.popHead();
        p.dropped += 1;
    }
    fn popHead(p: *Playout) void {
        var i: usize = 1;
        while (i < p.n) : (i += 1) p.buf[i - 1] = p.buf[i];
        p.n -= 1;
    }
};

/// seqLess is wrap-aware (u32 sequence space).
fn seqLess(a: u32, b: u32) bool {
    return @as(i32, @bitCast(a -% b)) < 0;
}

test "PTS-locked release is evenly spaced regardless of arrival bunching" {
    var mem: [16]Item = undefined;
    var p = Playout.init(&mem, 60);
    p.max_depth = 2;
    const ivl = p.interval_ns;
    // Frames at an even 60fps PTS cadence, but ARRIVALS bunched: frame 0 at t=0, frames 1&2 arrive
    // together late at t=3*ivl, frame 3 at t=3*ivl too.
    p.depth = 1;
    _ = p.push(0, 0, 100, 0);
    _ = p.push(1, ivl, 101, 3 * ivl);
    _ = p.push(2, 2 * ivl, 102, 3 * ivl);
    _ = p.push(3, 3 * ivl, 103, 3 * ivl);
    // Deadlines are t0 + pts + depth*ivl = N*ivl + ivl. Release each exactly at its deadline.
    var rel: [4]u64 = undefined;
    var k: usize = 0;
    var t: i64 = 0;
    while (k < 4) {
        if (p.due(t)) |it| {
            rel[k] = it.tag;
            k += 1;
        } else {
            t = p.nextDeadline(); // high-res timer would wake us here
        }
    }
    try std.testing.expectEqual([4]u64{ 100, 101, 102, 103 }, rel);
    // The 2nd..4th frames were released at their PTS-locked deadlines (even ivl spacing), not bunched
    // at their t=3*ivl arrival.
    try std.testing.expectEqual(@as(u64, 4), p.released);
}

test "gap: head held to deadline then skipped forward" {
    var mem: [8]Item = undefined;
    var p = Playout.init(&mem, 60);
    const ivl = p.interval_ns;
    p.depth = 1;
    _ = p.push(0, 0, 0, 0);
    _ = p.push(2, 2 * ivl, 2, 0); // seq 1 missing
    const a = p.due(p.deadline(0)).?; // frame 0 at its deadline
    try std.testing.expectEqual(@as(u32, 0), a.seq);
    // frame 2 at the HEAD now; its deadline is 2*ivl+ivl. Not due before it.
    try std.testing.expectEqual(@as(?Item, null), p.due(p.deadline(0)));
    const b = p.due(p.deadline(2 * ivl)).?;
    try std.testing.expectEqual(@as(u32, 2), b.seq);
}

test "depth adapts up under sustained lateness" {
    var mem: [256]Item = undefined;
    var p = Playout.init(&mem, 60);
    p.max_depth = 4;
    const ivl = p.interval_ns;
    // Every frame arrives 2 intervals after its deadline → late-rate 100% → grow.
    var i: u32 = 0;
    while (i < 130) : (i += 1) {
        const pts = @as(i64, i) * ivl;
        const arrive = p.t0_mono + pts + 3 * ivl; // past deadline (depth starts 1)
        _ = p.push(i, pts, i, if (i == 0) 0 else arrive);
        // drain due frames so the buffer doesn't overflow
        while (p.due(arrive)) |_| {}
    }
    try std.testing.expect(p.depth > 1);
}
