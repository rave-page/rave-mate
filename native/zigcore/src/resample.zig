//! Streaming polyphase windowed-sinc sample-rate converter (interleaved f32).
//! Replaces the Go linear resampler for playback quality (44.1<->48k). Kaiser window,
//! ~90dB stopband. Zero added latency: output position p maps to input position p
//! (startup window is zero-padded; first ~taps/2 frames ramp in).

const std = @import("std");

pub const taps = 32; // per-phase FIR length (even)
pub const phases = 256; // phase table resolution; frac interpolated between rows

const half = taps / 2;

pub const Resampler = struct {
    in_rate: u32,
    out_rate: u32,
    ch: u32,
    step: f64, // in_rate / out_rate
    // Filter bank: (phases+1) rows so row j+1 is always addressable for interpolation.
    table: []f32, // (phases+1) * taps
    // Sliding window: hist (taps-1 frames from previous blocks) ++ new block, deinterleaved
    // per channel into scratch. hist cap = (taps-1)*ch floats — bounded, never grows.
    hist: []f32,
    hist_frames: u32, // frames currently in hist (< taps after first block)
    p: f64, // fractional read position in the current window, frame units
    scratch: []f32, // deinterleaved window, grown to fit largest block seen (bounded by caller block size)
    alloc: std.mem.Allocator,

    pub fn init(alloc: std.mem.Allocator, in_rate: u32, out_rate: u32, ch: u32) !*Resampler {
        if (in_rate == 0 or out_rate == 0 or ch == 0 or ch > 32) return error.BadArgs;
        const r = try alloc.create(Resampler);
        errdefer alloc.destroy(r);
        const table = try alloc.alloc(f32, (phases + 1) * taps);
        errdefer alloc.free(table);
        const hist = try alloc.alloc(f32, (taps - 1) * ch);
        errdefer alloc.free(hist);
        r.* = .{
            .in_rate = in_rate,
            .out_rate = out_rate,
            .ch = ch,
            .step = @as(f64, @floatFromInt(in_rate)) / @as(f64, @floatFromInt(out_rate)),
            .table = table,
            .hist = hist,
            .hist_frames = 0,
            .p = 0,
            .scratch = &.{},
            .alloc = alloc,
        };
        buildTable(table, in_rate, out_rate);
        @memset(hist, 0);
        return r;
    }

    pub fn deinit(r: *Resampler) void {
        r.alloc.free(r.table);
        r.alloc.free(r.hist);
        if (r.scratch.len > 0) r.alloc.free(r.scratch);
        r.alloc.destroy(r);
    }

    pub fn reset(r: *Resampler) void {
        r.hist_frames = 0;
        r.p = 0;
        @memset(r.hist, 0);
    }

    /// Max output frames process() can emit for in_frames input.
    pub fn outCap(r: *const Resampler, in_frames: usize) usize {
        return @as(usize, @intFromFloat(@as(f64, @floatFromInt(in_frames)) / r.step)) + 2;
    }

    /// Resample one interleaved block. Returns frames written to out (interleaved, ch channels).
    /// out must hold >= outCap(in_frames) frames.
    pub fn process(r: *Resampler, in: []const f32, out: []f32) !usize {
        const ch = r.ch;
        const in_frames: u32 = @intCast(in.len / ch);
        if (in_frames == 0) return 0;
        const win_frames = r.hist_frames + in_frames;
        const need = @as(usize, win_frames) * ch;
        if (r.scratch.len < need) {
            if (r.scratch.len > 0) r.alloc.free(r.scratch);
            r.scratch = try r.alloc.alloc(f32, need);
        }
        // Window layout: per-channel planar [ch][win_frames] for cache-friendly FIR.
        const win = r.scratch[0..need];
        var c: u32 = 0;
        while (c < ch) : (c += 1) {
            const dst = win[@as(usize, c) * win_frames ..][0..win_frames];
            var f: u32 = 0;
            while (f < r.hist_frames) : (f += 1) dst[f] = r.hist[f * ch + c];
            f = 0;
            while (f < in_frames) : (f += 1) dst[r.hist_frames + f] = in[f * ch + c];
        }
        // Output loop: center j = floor(p); needs win[j-half+1 .. j+half] inclusive.
        // First blocks: p starts at 0 with zero-padded implicit history (hist zeroed).
        var written: usize = 0;
        const limit = @as(f64, @floatFromInt(win_frames)) - @as(f64, half); // need j+half <= win_frames-1
        while (r.p < limit) {
            const j: i64 = @intFromFloat(@floor(r.p));
            const frac = r.p - @floor(r.p);
            const ph_f = frac * @as(f64, phases);
            const ph: usize = @intFromFloat(ph_f);
            const ph_frac: f32 = @floatCast(ph_f - @floor(ph_f));
            const row0 = r.table[ph * taps ..][0..taps];
            const row1 = r.table[(ph + 1) * taps ..][0..taps];
            const start = j - half + 1;
            c = 0;
            while (c < ch) : (c += 1) {
                const plane = win[@as(usize, c) * win_frames ..][0..win_frames];
                var acc: f32 = 0;
                var k: usize = 0;
                while (k < taps) : (k += 1) {
                    const idx = start + @as(i64, @intCast(k));
                    const s = if (idx < 0) 0.0 else plane[@intCast(idx)];
                    acc += s * (row0[k] + (row1[k] - row0[k]) * ph_frac);
                }
                out[written * ch + c] = acc;
            }
            written += 1;
            r.p += r.step;
        }
        // Slide window: keep last taps-1 frames as history for the next block.
        const keep: u32 = @min(win_frames, taps - 1);
        const from = win_frames - keep;
        c = 0;
        while (c < ch) : (c += 1) {
            const plane = win[@as(usize, c) * win_frames ..][0..win_frames];
            var f: u32 = 0;
            while (f < keep) : (f += 1) r.hist[f * ch + c] = plane[from + f];
        }
        r.hist_frames = keep;
        r.p -= @as(f64, @floatFromInt(from));
        if (r.p < 0) r.p = 0;
        return written;
    }
};

/// Kaiser-windowed sinc bank. Cutoff 0.45*min(in,out)/in of input Nyquist pair keeps a
/// transition band clear of aliasing for both up- and downsampling.
fn buildTable(table: []f32, in_rate: u32, out_rate: u32) void {
    const ratio = @as(f64, @floatFromInt(out_rate)) / @as(f64, @floatFromInt(in_rate));
    const cutoff: f64 = 0.9 * @min(1.0, ratio); // fraction of input Nyquist
    const beta: f64 = 8.6; // ~90dB stopband
    const norm = besselI0(beta);
    var ph: usize = 0;
    while (ph <= phases) : (ph += 1) {
        const frac = @as(f64, @floatFromInt(ph)) / @as(f64, phases);
        var sum: f64 = 0;
        var k: usize = 0;
        var row: [taps]f64 = undefined;
        while (k < taps) : (k += 1) {
            // Tap k reads input frame (j - half + 1 + k); center offset from j+frac:
            const t = @as(f64, @floatFromInt(@as(i64, @intCast(k)) - half + 1)) - frac;
            const x = std.math.pi * cutoff * t;
            const sinc: f64 = if (@abs(x) < 1e-12) 1.0 else @sin(x) / x;
            const wt = t / @as(f64, half); // window argument in [-1,1]
            const w: f64 = if (@abs(wt) >= 1.0) 0.0 else besselI0(beta * @sqrt(1.0 - wt * wt)) / norm;
            row[k] = cutoff * sinc * w;
            sum += row[k];
        }
        // Normalize DC gain to exactly 1 per phase (flat passband, no level drift).
        k = 0;
        while (k < taps) : (k += 1) {
            table[ph * taps + k] = @floatCast(row[k] / sum);
        }
    }
}

fn besselI0(x: f64) f64 {
    // Power series; converges fast for |x| <= ~20 (our beta range).
    var sum: f64 = 1.0;
    var term: f64 = 1.0;
    var k: f64 = 1.0;
    while (k < 64) : (k += 1) {
        term *= (x / (2.0 * k)) * (x / (2.0 * k));
        sum += term;
        if (term < 1e-18 * sum) break;
    }
    return sum;
}

// ── tests ─────────────────────────────────────────────────────────────────────

const testing = std.testing;

test "dc passthrough" {
    const r = try Resampler.init(testing.allocator, 44100, 48000, 2);
    defer r.deinit();
    var in: [1024]f32 = undefined;
    @memset(&in, 1.0);
    var out: [2048]f32 = undefined;
    var total: usize = 0;
    var settled_min: f32 = 1;
    var settled_max: f32 = -1;
    var block: usize = 0;
    while (block < 8) : (block += 1) {
        const n = try r.process(&in, &out);
        // Skip the first block (zero-history ramp-in), then DC must hold at 1.0.
        if (block > 0) {
            for (out[0 .. n * 2]) |s| {
                settled_min = @min(settled_min, s);
                settled_max = @max(settled_max, s);
            }
        }
        total += n;
    }
    try testing.expect(total > 0);
    try testing.expect(settled_min > 0.999);
    try testing.expect(settled_max < 1.001);
}

test "sine snr 44k1 to 48k" {
    const in_rate = 44100;
    const out_rate = 48000;
    const freq = 1000.0;
    const r = try Resampler.init(testing.allocator, in_rate, out_rate, 1);
    defer r.deinit();
    const blocks = 40;
    const bsz = 512;
    var in: [bsz]f32 = undefined;
    var out: [bsz * 2]f32 = undefined;
    var n_in: usize = 0;
    var n_out: usize = 0;
    var sig: f64 = 0;
    var err: f64 = 0;
    var b: usize = 0;
    while (b < blocks) : (b += 1) {
        for (&in) |*s| {
            s.* = @floatCast(@sin(2.0 * std.math.pi * freq * @as(f64, @floatFromInt(n_in)) / in_rate));
            n_in += 1;
        }
        const n = try r.process(&in, &out);
        for (out[0..n]) |s| {
            // Output frame n_out sits at input position n_out*step → t = n_out/out_rate
            // (converter adds no delay; startup is zero-padded).
            const t = @as(f64, @floatFromInt(n_out)) / out_rate;
            const want = @sin(2.0 * std.math.pi * freq * t);
            // Ignore ramp-in.
            if (n_out > 2 * taps) {
                sig += want * want;
                const d = @as(f64, s) - want;
                err += d * d;
            }
            n_out += 1;
        }
    }
    const snr = 10.0 * std.math.log10(sig / err);
    try testing.expect(snr > 70.0); // linear interp is ~35dB here; sinc must clear 70dB
}

test "output count tracks ratio" {
    const r = try Resampler.init(testing.allocator, 48000, 44100, 2);
    defer r.deinit();
    var in: [960 * 2]f32 = undefined;
    @memset(&in, 0.5);
    var out: [2048]f32 = undefined;
    var total: usize = 0;
    var b: usize = 0;
    while (b < 50) : (b += 1) total += try r.process(&in, &out);
    const expect_total = 50 * 960 * 44100 / 48000;
    const diff = @as(i64, @intCast(total)) - @as(i64, expect_total);
    try testing.expect(diff > -taps and diff < taps);
}
