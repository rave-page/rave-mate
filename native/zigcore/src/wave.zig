//! Waveform-display kernels over u8 peak buckets — byte-exact ports of
//! internal/giokit/wave.go WaveColumns and internal/deckcard/deckcard.go buildEnv.
//! Strict float mode; Go originals stay authoritative (parity tests diff outputs).

const std = @import("std");

/// Per-column maxima: column x covers buckets [x·n/cols, (x+1)·n/cols), at least one.
/// Port of giokit.WaveColumns (n==0 / cols<=0 handled Go-side).
pub fn waveColumns(peaks: []const u8, cols: usize, out: []u8) void {
    const n = peaks.len;
    if (n == 0 or cols == 0) return;
    for (0..cols) |x| {
        const b0 = x * n / cols;
        var b1 = (x + 1) * n / cols;
        if (b1 <= b0) b1 = b0 + 1;
        if (b1 > n) b1 = n;
        out[x] = std.mem.max(u8, peaks[b0..b1]); // b1 > b0 guaranteed above
    }
}

/// Truncate-toward-zero f64→i64 with clamp to [lo,hi] (Go int() conversion is
/// truncation; clamp keeps pathological inputs memory-safe where Go relies on
/// its post-conversion clamps).
inline fn truncClamp(v: f64, lo: i64, hi: i64) i64 {
    if (!(v > @as(f64, @floatFromInt(lo)))) return lo; // also catches NaN
    if (v >= @as(f64, @floatFromInt(hi))) return hi;
    return @intFromFloat(v);
}

/// Max-aggregated, 3-pass-binomial-smoothed 0..1 envelope at img_pps columns/sec.
/// out.len = iw = int(dur*img_pps)+1, allocated by the caller (Go handles the
/// degenerate iw<1 / n==0 / dur<=0 → {0} case). Port of deckcard.buildEnv.
pub fn waveEnv(peaks: []const u8, dur: f64, img_pps: f64, out: []f64) void {
    const n = peaks.len;
    const iw = out.len;
    if (iw == 0 or n == 0 or dur <= 0) return;
    const nf: f64 = @floatFromInt(n);
    const last: i64 = @intCast(n - 1);
    const pk_per_col = (nf / dur) / img_pps;
    const span = 0.5 / img_pps;
    for (0..iw) |x| {
        const t = @as(f64, @floatFromInt(x)) / img_pps;
        if (pk_per_col >= 1) { // zoomed out: max-abs over the column's time span
            const ia = truncClamp((t - span) / dur * nf, 0, @intCast(n));
            var ib = truncClamp(@ceil((t + span) / dur * nf), 0, @intCast(n));
            if (ib > last) ib = last;
            var m: i64 = 0;
            var i = ia;
            while (i <= ib) : (i += 1) {
                const p: i64 = peaks[@intCast(i)];
                if (p > m) m = p;
            }
            out[x] = @as(f64, @floatFromInt(m)) / 255;
        } else { // zoomed in: interpolate between buckets
            const f = t / dur * @as(f64, @floatFromInt(n - 1));
            const i = truncClamp(f, 0, last);
            var j = i + 1;
            if (j > last) j = last;
            const fi: f64 = @floatFromInt(i);
            const pi: f64 = @floatFromInt(peaks[@intCast(i)]);
            const pj: f64 = @floatFromInt(peaks[@intCast(j)]);
            out[x] = (pi * (1 - (f - fi)) + pj * (f - fi)) / 255;
        }
    }
    for (0..3) |_| { // 3 binomial passes → soft, low-fidelity envelope (no shimmer)
        var prev = out[0];
        for (0..iw) |x| {
            var nx = out[x];
            if (x < iw - 1) nx = out[x + 1];
            const cur = out[x];
            out[x] = (prev + 2 * cur + nx) * 0.25;
            prev = cur;
        }
    }
}

const testing = std.testing;

test "waveColumns fold + upsample" {
    const peaks = [_]u8{ 1, 9, 2, 3, 8, 4, 0, 5 };
    var out: [4]u8 = undefined;
    waveColumns(&peaks, 4, &out);
    try testing.expectEqualSlices(u8, &[_]u8{ 9, 3, 8, 5 }, &out);
    const two = [_]u8{ 10, 20 };
    var out4: [4]u8 = undefined;
    waveColumns(&two, 4, &out4);
    try testing.expectEqualSlices(u8, &[_]u8{ 10, 10, 20, 20 }, &out4);
}

test "waveEnv smooth bounds" {
    const peaks = [_]u8{ 0, 255, 0, 255, 0, 255, 0, 255 };
    var out: [21]f64 = undefined; // dur=2s, pps=10 → iw=21
    waveEnv(&peaks, 2, 10, &out);
    for (out) |v| try testing.expect(v >= 0 and v <= 1);
}
