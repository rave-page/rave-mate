//! Token-bucket send pacer, matching Go's sendPacer (pacer.go): spread a frame's datagrams over the
//! wire instead of bursting a 4K keyframe into shallow switch buffers. Pure + clock-injectable;
//! reserve() returns how long to wait before sending n bytes. rate 0 = unpaced.
const std = @import("std");

pub const Pacer = struct {
    rate: f64 = 0, // bytes/sec (0 = unpaced)
    burst: f64 = 0,
    tokens: f64 = 0,
    last_ns: i64 = 0,

    const min_burst: f64 = 2 * 1200; // a single MTU datagram always fits (never a deadlock)

    pub fn init(bytes_per_sec: f64, now_ns: i64) Pacer {
        var p = Pacer{ .last_ns = now_ns };
        p.setRate(bytes_per_sec);
        p.tokens = p.burst;
        return p;
    }

    pub fn setRate(p: *Pacer, bytes_per_sec: f64) void {
        p.rate = bytes_per_sec;
        if (bytes_per_sec <= 0) {
            p.burst = 0;
            return;
        }
        var b = bytes_per_sec * 0.005; // ~5ms of rate
        if (b < min_burst) b = min_burst;
        p.burst = b;
        if (p.tokens > p.burst) p.tokens = p.burst;
    }

    /// reserve accrues tokens since now_ns, spends n, and returns ns to wait before sending (0 when
    /// tokens already cover n). Balance may go negative by one send so an oversized datagram still goes.
    pub fn reserve(p: *Pacer, n: usize, now_ns: i64) i64 {
        if (p.rate <= 0) return 0;
        const elapsed: f64 = @floatFromInt(now_ns - p.last_ns);
        if (elapsed > 0) {
            p.tokens += (elapsed / 1e9) * p.rate;
            if (p.tokens > p.burst) p.tokens = p.burst;
            p.last_ns = now_ns;
        }
        var wait: i64 = 0;
        const fn_: f64 = @floatFromInt(n);
        if (p.tokens < fn_) {
            const deficit = fn_ - p.tokens;
            wait = @intFromFloat(deficit / p.rate * 1e9);
        }
        p.tokens -= fn_;
        return wait;
    }
};

test "pacer unpaced returns zero" {
    var p = Pacer.init(0, 0);
    try std.testing.expectEqual(@as(i64, 0), p.reserve(10_000, 0));
}

test "pacer throttles past burst" {
    var p = Pacer.init(1000, 0); // 1000 B/s, burst floored at 2400
    // Drain the burst, then a further send must wait ~ (n/rate).
    _ = p.reserve(2400, 0);
    const wait = p.reserve(1000, 0); // need 1000 tokens at 1000 B/s ≈ 1s
    try std.testing.expect(wait > 0);
    try std.testing.expect(wait <= 1_100_000_000);
}
