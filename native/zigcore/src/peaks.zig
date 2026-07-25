//! Waveform bin kernels: per-bin min/max/rms over interleaved f32 PCM (mono-mixed).
//! Hot path behind rave-mate waveform rendering/analysis; O(n), single pass.

const std = @import("std");

/// Fill bins[0..n_bins] each with {min, max, rms} of the mono mix of its frame span.
/// Layout: out_min/out_max/out_rms are parallel arrays of n_bins. Frames split evenly;
/// remainder frames fold into the last bin.
pub fn bins(in: []const f32, ch: u32, n_bins: usize, out_min: []f32, out_max: []f32, out_rms: []f32) void {
    const frames = in.len / ch;
    if (n_bins == 0 or frames == 0) return;
    const per = frames / n_bins;
    var b: usize = 0;
    while (b < n_bins) : (b += 1) {
        const start = b * per;
        const end = if (b == n_bins - 1) frames else start + per;
        var mn: f32 = std.math.floatMax(f32);
        var mx: f32 = -std.math.floatMax(f32);
        var acc: f64 = 0;
        var f = start;
        while (f < end) : (f += 1) {
            var s: f32 = 0;
            var c: u32 = 0;
            while (c < ch) : (c += 1) s += in[f * ch + c];
            s /= @floatFromInt(ch);
            mn = @min(mn, s);
            mx = @max(mx, s);
            acc += @as(f64, s) * @as(f64, s);
        }
        const n = end - start;
        out_min[b] = if (n == 0) 0 else mn;
        out_max[b] = if (n == 0) 0 else mx;
        out_rms[b] = if (n == 0) 0 else @floatCast(@sqrt(acc / @as(f64, @floatFromInt(n))));
    }
}

/// In-place gain. SIMD-vectorized by the compiler at ReleaseFast.
pub fn applyGain(buf: []f32, gain: f32) void {
    for (buf) |*s| s.* *= gain;
}

/// Interleaved peak (abs max) — cheap clip/meter probe.
pub fn peakAbs(in: []const f32) f32 {
    var p: f32 = 0;
    for (in) |s| p = @max(p, @abs(s));
    return p;
}

const testing = std.testing;

test "bins basic" {
    // 8 frames stereo: L=R ramp 0..7 scaled 0.1
    var in: [16]f32 = undefined;
    for (0..8) |f| {
        in[f * 2] = @as(f32, @floatFromInt(f)) * 0.1;
        in[f * 2 + 1] = @as(f32, @floatFromInt(f)) * 0.1;
    }
    var mn: [2]f32 = undefined;
    var mx: [2]f32 = undefined;
    var rms: [2]f32 = undefined;
    bins(&in, 2, 2, &mn, &mx, &rms);
    try testing.expectApproxEqAbs(@as(f32, 0.0), mn[0], 1e-6);
    try testing.expectApproxEqAbs(@as(f32, 0.3), mx[0], 1e-6);
    try testing.expectApproxEqAbs(@as(f32, 0.4), mn[1], 1e-6);
    try testing.expectApproxEqAbs(@as(f32, 0.7), mx[1], 1e-6);
    try testing.expect(rms[1] > rms[0]);
}

test "gain and peak" {
    var buf = [_]f32{ 0.5, -0.25, 0.75 };
    applyGain(&buf, 2.0);
    try testing.expectApproxEqAbs(@as(f32, 1.5), peakAbs(&buf), 1e-6);
}
