//! Lock-free inter-event interval histogram for the native child's telemetry (the PUBLISH-interval
//! histogram is the real acceptance metric: publish p99 <= 18 ms, zero > 25 ms on the bar over 10 min).
//! mark() runs on the realtime publish thread (atomics only, allocation-free); snapshot() is off-thread
//! (the 1 Hz stats sampler). Mirrors the Go StageHist (framestages.go), 1 ms buckets around one frame.
const std = @import("std");

const bucket_upper_ms = [_]f64{
    4, 8,
    10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30,
    33, 40, 50, 75, 100, 150, 200, 500, 1e12,
};

pub const Snapshot = struct {
    count: u64 = 0,
    mean_ms: f64 = 0,
    p50_ms: f64 = 0,
    p99_ms: f64 = 0,
    max_ms: f64 = 0,
    over25: u64 = 0,
    over50: u64 = 0,
    over100: u64 = 0,
};

pub const Hist = struct {
    last: std.atomic.Value(i64) = .init(0),
    count: std.atomic.Value(u64) = .init(0),
    sum_ns: std.atomic.Value(i64) = .init(0),
    max_ns: std.atomic.Value(i64) = .init(0),
    over25: std.atomic.Value(u64) = .init(0),
    over50: std.atomic.Value(u64) = .init(0),
    over100: std.atomic.Value(u64) = .init(0),
    buckets: [bucket_upper_ms.len]std.atomic.Value(u64) = [_]std.atomic.Value(u64){.init(0)} ** bucket_upper_ms.len,

    pub fn mark(h: *Hist, now_ns: i64) void {
        const prev = h.last.swap(now_ns, .monotonic);
        if (prev == 0 or now_ns <= prev) return;
        const d = now_ns - prev;
        _ = h.count.fetchAdd(1, .monotonic);
        _ = h.sum_ns.fetchAdd(d, .monotonic);
        var m = h.max_ns.load(.monotonic);
        while (d > m) : (m = h.max_ns.load(.monotonic)) {
            if (h.max_ns.cmpxchgWeak(m, d, .monotonic, .monotonic) == null) break;
        }
        const ms = @as(f64, @floatFromInt(d)) / 1e6;
        if (ms > 25) _ = h.over25.fetchAdd(1, .monotonic);
        if (ms > 50) _ = h.over50.fetchAdd(1, .monotonic);
        if (ms > 100) _ = h.over100.fetchAdd(1, .monotonic);
        for (bucket_upper_ms, 0..) |ub, i| {
            if (ms <= ub) {
                _ = h.buckets[i].fetchAdd(1, .monotonic);
                break;
            }
        }
    }

    pub fn snapshot(h: *Hist) Snapshot {
        const n = h.count.load(.monotonic);
        var s = Snapshot{ .count = n, .max_ms = @as(f64, @floatFromInt(h.max_ns.load(.monotonic))) / 1e6,
            .over25 = h.over25.load(.monotonic), .over50 = h.over50.load(.monotonic), .over100 = h.over100.load(.monotonic) };
        if (n == 0) return s;
        s.mean_ms = @as(f64, @floatFromInt(h.sum_ns.load(.monotonic))) / 1e6 / @as(f64, @floatFromInt(n));
        s.p50_ms = h.percentileMs(n, 0.50);
        s.p99_ms = h.percentileMs(n, 0.99);
        return s;
    }

    fn percentileMs(h: *Hist, n: u64, q: f64) f64 {
        const target: u64 = @intFromFloat(q * @as(f64, @floatFromInt(n)));
        var cum: u64 = 0;
        for (bucket_upper_ms, 0..) |ub, i| {
            cum += h.buckets[i].load(.monotonic);
            if (cum >= target) return ub;
        }
        return bucket_upper_ms[bucket_upper_ms.len - 1];
    }
};

test "histogram intervals + tail" {
    var h = Hist{};
    const ms = 1_000_000;
    // 200 frames at 16ms, one 120ms hang.
    var t: i64 = 10 * ms;
    var i: usize = 0;
    while (i < 200) : (i += 1) {
        t += 16 * ms;
        h.mark(t);
    }
    t += 120 * ms;
    h.mark(t);
    const s = h.snapshot();
    try std.testing.expect(s.p50_ms <= 20);
    try std.testing.expect(s.max_ms >= 120);
    try std.testing.expectEqual(@as(u64, 1), s.over100);
}
