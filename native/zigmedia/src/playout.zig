//! PTS-locked playout (increment 3, native receiver core). A frame releases when the monotonic clock
//! reaches `pts + base + margin`, where:
//!   base   = windowed-MIN transit (transit = arrival_mono - pts). The min cancels the sender↔receiver
//!            clock-epoch offset AND ignores cold-start stragglers (the ~500ms first-second encoder-open
//!            gap), so the playout epoch settles to the steady-state cadence, not the very first frame
//!            (coordinator 2026-11-12). It also slow-tracks clock drift.
//!   margin = windowed-MAX transit SPREAD (transit - base) + headroom, i.e. the delay is sized from the
//!            MEASURED arrival-jitter distribution (≈ p99.9 of net lateness), NOT a frame count - a
//!            42 Mbps stream whose large frames arrive burstier gets a bigger margin than an even bar;
//!            it adapts down when the link calms. Clamped [1 frame, cap].
//! Release intervals then equal the source cadence regardless of arrival bunching - the fix for the
//! receiver stutter (the Go path released on arrival / a coarse Windows timer). The caller sleeps on a
//! high-resolution timer (hrtimer.zig) until nextDeadline, then drains due frames. Allocation-free;
//! monotonic clock injected (no synced clock needed - the offset cancels in base).
const std = @import("std");

pub const Item = struct {
    seq: u32,
    pts_ns: i64,
    tag: u64 = 0, // opaque payload handle (real receiver: reassembled AU slot); tests assert ordering
    used: bool = false,
};

const win_ns: i64 = 2_000_000_000; // base/spread high-watermark window (~2 s); two windows combined

pub const Playout = struct {
    buf: []Item,
    n: usize = 0,
    interval_ns: i64,

    // transit statistics (transit = arrival_mono - pts).
    base: i64 = 0, // windowed-min transit (epoch offset + min delay); the playout anchor
    cur_min: i64 = 0,
    prev_min: i64 = 0,
    cur_spread: i64 = 0, // max(transit - base) this window
    prev_spread: i64 = 0,
    win_start: i64 = 0,

    margin_ns: i64,
    min_margin_ns: i64,
    max_margin_ns: i64,
    headroom_ns: i64,

    anchored: bool = false,
    next_seq: u32 = 0,
    released: u64 = 0,
    dropped: u64 = 0,

    pub fn init(buf: []Item, fps: f64) Playout {
        var ivl: i64 = 16_666_667;
        if (fps > 0) ivl = @intFromFloat(1e9 / fps);
        for (buf) |*it| it.* = .{ .seq = 0, .pts_ns = 0 };
        return .{
            .buf = buf,
            .interval_ns = ivl,
            .min_margin_ns = ivl, // 1 frame floor
            .max_margin_ns = 8 * ivl, // latency cap
            .headroom_ns = @divTrunc(ivl, 2),
            .margin_ns = ivl,
        };
    }

    /// deadline is frame pts_ns's monotonic release time (epoch cancels: transit = arrival_mono - pts).
    pub fn deadline(p: *const Playout, pts_ns: i64) i64 {
        return pts_ns + p.base + p.margin_ns;
    }

    /// push accepts a received frame at monotonic time now_mono. Updates base/margin from its transit;
    /// drops a duplicate / too-old / overflow (drop-oldest).
    pub fn push(p: *Playout, seq: u32, pts_ns: i64, tag: u64, now_mono: i64) bool {
        const transit = now_mono - pts_ns;
        if (!p.anchored) {
            p.anchored = true;
            p.base = transit;
            p.cur_min = transit;
            p.win_start = now_mono;
            p.next_seq = seq;
        }
        if (seqLess(seq, p.next_seq)) {
            p.dropped += 1;
            return false;
        }
        p.observe(transit, now_mono);

        if (p.n == p.buf.len) p.dropHead();
        var i = p.n;
        while (i > 0 and seqLess(seq, p.buf[i - 1].seq)) : (i -= 1) {
            p.buf[i] = p.buf[i - 1];
        }
        if (i > 0 and p.buf[i - 1].seq == seq) {
            while (i < p.n) : (i += 1) p.buf[i] = p.buf[i + 1];
            p.dropped += 1;
            return false;
        }
        p.buf[i] = .{ .seq = seq, .pts_ns = pts_ns, .tag = tag, .used = true };
        p.n += 1;
        return true;
    }

    // observe folds a transit sample into the windowed min (base) + max-spread (margin).
    fn observe(p: *Playout, transit: i64, now_mono: i64) void {
        if (transit < p.cur_min) p.cur_min = transit;
        const spread = transit - p.base;
        if (spread > p.cur_spread) p.cur_spread = spread;
        if (now_mono - p.win_start >= win_ns) {
            p.prev_min = p.cur_min;
            p.prev_spread = p.cur_spread;
            p.cur_min = transit;
            p.cur_spread = 0;
            p.win_start = now_mono;
        }
        p.base = @min(p.cur_min, p.prev_min); // re-anchor to the windowed-min transit (settle + drift)
        var want = @max(p.cur_spread, p.prev_spread) + p.headroom_ns;
        if (want < p.min_margin_ns) want = p.min_margin_ns;
        if (want > p.max_margin_ns) want = p.max_margin_ns;
        p.margin_ns = want;
    }

    /// due returns the next releasable frame (deadline <= now_mono, seq order) or null. On a gap below
    /// the head (lower-seq frames missing, e.g. a dropped fragment still being NACK-recovered) it HOLDS
    /// the head until the first missing frame's would-be deadline (the NACK recovery window), so a
    /// fragment recovered within the margin is still released IN ORDER - not released-late-then-dropped.
    /// Past that deadline it skips the gap forward (live stream: latency beats completeness).
    pub fn due(p: *Playout, now_mono: i64) ?Item {
        if (p.n == 0) return null;
        const h = p.buf[0];
        if (h.seq != p.next_seq) {
            const gap: i64 = @intCast(h.seq -% p.next_seq);
            const miss_pts = h.pts_ns - gap * p.interval_ns; // first missing frame's estimated pts
            if (now_mono < miss_pts + p.base + p.margin_ns) return null; // hold for NACK recovery
            p.next_seq = h.seq; // recovery window expired: drop the gap, release the head
        }
        if (now_mono < p.deadline(h.pts_ns)) return null;
        p.popHead();
        p.next_seq = h.seq +% 1;
        p.released += 1;
        return h;
    }

    /// nextDeadline accounts for a gap-held head (wake at the missing frame's recovery deadline).
    pub fn nextDeadlineGap(p: *const Playout) i64 {
        if (p.n == 0) return std.math.maxInt(i64);
        const h = p.buf[0];
        if (h.seq != p.next_seq) {
            const gap: i64 = @intCast(h.seq -% p.next_seq);
            return (h.pts_ns - gap * p.interval_ns) + p.base + p.margin_ns;
        }
        return p.deadline(h.pts_ns);
    }

    /// nextDeadline is the monotonic time to wake for the next release (max i64 when empty).
    pub fn nextDeadline(p: *const Playout) i64 {
        if (p.n == 0) return std.math.maxInt(i64);
        return p.deadline(p.buf[0].pts_ns);
    }

    pub fn marginMs(p: *const Playout) f64 {
        return @as(f64, @floatFromInt(p.margin_ns)) / 1e6;
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

test "PTS-locked release is evenly spaced under bunched arrivals" {
    var mem: [16]Item = undefined;
    var p = Playout.init(&mem, 60);
    const ivl = p.interval_ns;
    // Even PTS cadence; arrivals bunched: frames 1,2,3 all arrive at now=3*ivl.
    _ = p.push(0, 0, 100, 0);
    _ = p.push(1, ivl, 101, 3 * ivl);
    _ = p.push(2, 2 * ivl, 102, 3 * ivl);
    _ = p.push(3, 3 * ivl, 103, 3 * ivl);
    var rel: [4]u64 = undefined;
    var prev_t: i64 = -1;
    var k: usize = 0;
    var t: i64 = 0;
    while (k < 4) {
        if (p.due(t)) |it| {
            rel[k] = it.tag;
            if (prev_t >= 0) try std.testing.expectEqual(ivl, t - prev_t); // releases one interval apart
            prev_t = t;
            k += 1;
        } else {
            t = p.nextDeadline();
        }
    }
    try std.testing.expectEqual([4]u64{ 100, 101, 102, 103 }, rel);
}

test "base settles to min transit, ignoring a cold-start straggler" {
    var mem: [64]Item = undefined;
    var p = Playout.init(&mem, 60);
    const ivl = p.interval_ns;
    // Frame 0 is a cold-start straggler: huge transit (arrived 500ms after its pts slot).
    _ = p.push(0, 0, 0, 500_000_000);
    // Steady frames with a small, even transit (~1ms).
    var i: u32 = 1;
    while (i <= 40) : (i += 1) {
        const pts = @as(i64, i) * ivl;
        _ = p.push(i, pts, i, pts + 1_000_000); // transit ≈ 1ms
    }
    // base must settle to the small steady transit, not the 500ms straggler.
    try std.testing.expect(p.base <= 2_000_000);
    // margin floored at ~1 frame for an even stream (tiny spread).
    try std.testing.expect(p.margin_ns <= 2 * ivl);
}

test "margin sizes to the transit spread (burstier arrival → bigger margin)" {
    var mem: [64]Item = undefined;
    var p = Playout.init(&mem, 60);
    const ivl = p.interval_ns;
    _ = p.push(0, 0, 0, 0); // transit 0 → base 0
    _ = p.push(1, ivl, 1, ivl + 3 * ivl); // transit 3*ivl → spread 3*ivl
    try std.testing.expect(p.margin_ns >= 3 * ivl);
    try std.testing.expect(p.margin_ns <= p.max_margin_ns);
}

test "gap: head released on time, next in order" {
    var mem: [8]Item = undefined;
    var p = Playout.init(&mem, 60);
    const ivl = p.interval_ns;
    _ = p.push(0, 0, 0, 0);
    _ = p.push(2, 2 * ivl, 2, 0); // seq 1 missing
    const a = p.due(p.deadline(0)).?;
    try std.testing.expectEqual(@as(u32, 0), a.seq);
    try std.testing.expectEqual(@as(?Item, null), p.due(p.deadline(0)));
    const b = p.due(p.deadline(2 * ivl)).?;
    try std.testing.expectEqual(@as(u32, 2), b.seq);
}
