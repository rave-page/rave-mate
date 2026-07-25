//! ravezig — rave-mate native core (Zig). C ABI consumed by Go via cgo
//! (internal/zignative). Every export is prefixed rz_. ABI contract: include/ravezig.h.
//! Allocation via libc malloc-compatible c_allocator (mingw runtime provides it).

const std = @import("std");
const resample = @import("resample.zig");
const peaks = @import("peaks.zig");
const bands = @import("bands.zig");

const alloc = std.heap.c_allocator;

/// ABI version — bump on any breaking export change; Go side asserts at init.
pub const abi_version: u32 = 1;

export fn rz_abi_version() u32 {
    return abi_version;
}

// ── resampler ────────────────────────────────────────────────────────────────

export fn rz_resampler_new(in_rate: u32, out_rate: u32, channels: u32) ?*resample.Resampler {
    return resample.Resampler.init(alloc, in_rate, out_rate, channels) catch null;
}

export fn rz_resampler_free(r: *resample.Resampler) void {
    r.deinit();
}

export fn rz_resampler_reset(r: *resample.Resampler) void {
    r.reset();
}

/// Max output frames one process() call can emit for in_frames of input.
export fn rz_resampler_out_cap(r: *const resample.Resampler, in_frames: usize) usize {
    return r.outCap(in_frames);
}

/// Returns frames written, or ~0 (max usize) on error (out too small / alloc fail).
export fn rz_resampler_process(r: *resample.Resampler, in: [*]const f32, in_frames: usize, out: [*]f32, out_cap_frames: usize) usize {
    if (out_cap_frames < r.outCap(in_frames)) return std.math.maxInt(usize);
    const ch: usize = r.ch;
    const n = r.process(in[0 .. in_frames * ch], out[0 .. out_cap_frames * ch]) catch return std.math.maxInt(usize);
    return n;
}

// ── waveform / gain kernels ──────────────────────────────────────────────────

export fn rz_wave_bins(in: [*]const f32, frames: usize, channels: u32, n_bins: usize, out_min: [*]f32, out_max: [*]f32, out_rms: [*]f32) void {
    peaks.bins(in[0 .. frames * channels], channels, n_bins, out_min[0..n_bins], out_max[0..n_bins], out_rms[0..n_bins]);
}

export fn rz_apply_gain(buf: [*]f32, n: usize, gain: f32) void {
    peaks.applyGain(buf[0..n], gain);
}

export fn rz_peak_abs(in: [*]const f32, n: usize) f32 {
    return peaks.peakAbs(in[0..n]);
}

// ── s16le analysis kernels (byte-exact ports of worker bucketPeaks/bucketBands) ──

/// out needs n bytes; returns buckets written (min(n, samples)).
export fn rz_bucket_peaks(pcm: [*]const u8, pcm_len: usize, n: usize, out: [*]u8) usize {
    return bands.bucketPeaks(pcm[0..pcm_len], n, out[0..n]);
}

/// out needs 3*n bytes; returns buckets written.
export fn rz_bucket_bands(pcm: [*]const u8, pcm_len: usize, n: usize, fs: u32, out: [*]u8) usize {
    return bands.bucketBands(pcm[0..pcm_len], n, fs, out[0 .. 3 * n]);
}

test {
    std.testing.refAllDecls(@This());
    _ = resample;
    _ = peaks;
    _ = bands;
}
