//! Waveform analysis kernels over mono s16le PCM — byte-exact ports of
//! internal/worker/probe.go bucketPeaks + bands.go bucketBands (RBJ biquads).
//! Byte-exactness is the migration contract: Go golden tests diff Zig vs Go output.

const std = @import("std");

/// Direct-form-1 second-order IIR, stateful across the whole stream.
const Biquad = struct {
    b0: f64,
    b1: f64,
    b2: f64,
    a1: f64,
    a2: f64,
    x1: f64 = 0,
    x2: f64 = 0,
    y1: f64 = 0,
    y2: f64 = 0,

    inline fn process(f: *Biquad, x: f64) f64 {
        const y = f.b0 * x + f.b1 * f.x1 + f.b2 * f.x2 - f.a1 * f.y1 - f.a2 * f.y2;
        f.x2 = f.x1;
        f.x1 = x;
        f.y2 = f.y1;
        f.y1 = y;
        return y;
    }
};

fn lowpass(fc: f64, fs: f64, q: f64) Biquad {
    const w0 = 2 * std.math.pi * fc / fs;
    const c = @cos(w0);
    const s = @sin(w0);
    const al = s / (2 * q);
    const a0 = 1 + al;
    return .{ .b0 = (1 - c) / 2 / a0, .b1 = (1 - c) / a0, .b2 = (1 - c) / 2 / a0, .a1 = -2 * c / a0, .a2 = (1 - al) / a0 };
}

fn highpass(fc: f64, fs: f64, q: f64) Biquad {
    const w0 = 2 * std.math.pi * fc / fs;
    const c = @cos(w0);
    const s = @sin(w0);
    const al = s / (2 * q);
    const a0 = 1 + al;
    return .{ .b0 = (1 + c) / 2 / a0, .b1 = -(1 + c) / a0, .b2 = (1 + c) / 2 / a0, .a1 = -2 * c / a0, .a2 = (1 - al) / a0 };
}

fn bandpass(fc: f64, fs: f64, q: f64) Biquad { // constant 0dB peak gain
    const w0 = 2 * std.math.pi * fc / fs;
    const c = @cos(w0);
    const s = @sin(w0);
    const al = s / (2 * q);
    const a0 = 1 + al;
    return .{ .b0 = al / a0, .b1 = 0, .b2 = -al / a0, .a1 = -2 * c / a0, .a2 = (1 - al) / a0 };
}

inline fn s16At(pcm: []const u8, i: usize) i32 {
    const lo: u16 = pcm[2 * i];
    const hi: u16 = pcm[2 * i + 1];
    return @as(i32, @as(i16, @bitCast(lo | (hi << 8))));
}

/// Max-abs amplitude peak per bucket, s16 magnitude >> 7 → u8. n clamped to sample count.
/// Returns buckets written (= min(n, samples)).
pub fn bucketPeaks(pcm: []const u8, n_req: usize, out: []u8) usize {
    const samples = pcm.len / 2;
    const n = @min(n_req, samples);
    var b: usize = 0;
    while (b < n) : (b += 1) {
        const lo = b * samples / n;
        const hi = (b + 1) * samples / n;
        var peak: i32 = 0;
        var i = lo;
        while (i < hi) : (i += 1) {
            var v = s16At(pcm, i);
            if (v < 0) v = -v;
            if (v > peak) peak = v;
        }
        if (peak > 32767) peak = 32767; // |-32768|
        out[b] = @intCast(@as(u32, @intCast(peak)) >> 7);
    }
    return n;
}

fn scale8(v: f64) u8 {
    var x = v;
    if (x > 32767) x = 32767;
    // Go: byte(int(v) >> 7) — truncate toward zero, then arithmetic shift.
    return @intCast(@as(u32, @intCast(@as(i32, @intFromFloat(x)))) >> 7);
}

/// 3-band max-abs peaks per bucket, interleaved [low,mid,high] u8 (3*n bytes). Streaming:
/// every sample filtered exactly once so IIR state stays valid across buckets. Byte-exact
/// with Go bucketBands. Returns buckets written.
pub fn bucketBands(pcm: []const u8, n_req: usize, fs: u32, out: []u8) usize {
    const samples = pcm.len / 2;
    const n = @min(n_req, samples);
    if (n == 0) return 0;
    const fsf: f64 = @floatFromInt(fs);
    var lp = lowpass(250, fsf, 0.707); // sub/bass/kick
    var bp = bandpass(1200, fsf, 0.60); // vocals/snares/synths (broad)
    var hp = highpass(4000, fsf, 0.707); // hats/cymbals/air
    var b: usize = 0;
    while (b < n) : (b += 1) {
        const lo = b * samples / n;
        const hi = (b + 1) * samples / n;
        var l_pk: f64 = 0;
        var m_pk: f64 = 0;
        var h_pk: f64 = 0;
        var i = lo;
        while (i < hi) : (i += 1) {
            const s: f64 = @floatFromInt(s16At(pcm, i));
            const lv = @abs(lp.process(s));
            if (lv > l_pk) l_pk = lv;
            const mv = @abs(bp.process(s));
            if (mv > m_pk) m_pk = mv;
            const hv = @abs(hp.process(s));
            if (hv > h_pk) h_pk = hv;
        }
        out[3 * b] = scale8(l_pk);
        out[3 * b + 1] = scale8(m_pk);
        out[3 * b + 2] = scale8(h_pk);
    }
    return n;
}

const testing = std.testing;

test "bucketPeaks basic" {
    // 4 samples: 0, 16384, -32768, 128
    var pcm: [8]u8 = undefined;
    std.mem.writeInt(i16, pcm[0..2], 0, .little);
    std.mem.writeInt(i16, pcm[2..4], 16384, .little);
    std.mem.writeInt(i16, pcm[4..6], -32768, .little);
    std.mem.writeInt(i16, pcm[6..8], 128, .little);
    var out: [2]u8 = undefined;
    const n = bucketPeaks(&pcm, 2, &out);
    try testing.expectEqual(@as(usize, 2), n);
    try testing.expectEqual(@as(u8, 128), out[0]); // 16384>>7
    try testing.expectEqual(@as(u8, 255), out[1]); // clamped 32767>>7
}

test "bucketBands shapes" {
    // 1s 100Hz sine @8k: energy lands in low band, not high.
    var pcm: [16000]u8 = undefined;
    for (0..8000) |i| {
        const v: i16 = @intFromFloat(20000.0 * @sin(2.0 * std.math.pi * 100.0 * @as(f64, @floatFromInt(i)) / 8000.0));
        std.mem.writeInt(i16, pcm[2 * i ..][0..2], v, .little);
    }
    var out: [3 * 4]u8 = undefined;
    const n = bucketBands(&pcm, 4, 8000, &out);
    try testing.expectEqual(@as(usize, 4), n);
    try testing.expect(out[3] > 100); // low band hot (skip bucket 0 filter warmup)
    try testing.expect(out[5] < 20); // high band cold
}
