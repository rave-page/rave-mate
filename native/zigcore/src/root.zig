//! ravezig — rave-mate native core (Zig). C ABI consumed by Go via cgo
//! (internal/zignative). Every export is prefixed rz_. ABI contract: include/ravezig.h.
//! Allocation via libc malloc-compatible c_allocator (mingw runtime provides it).

const std = @import("std");
const resample = @import("resample.zig");
const peaks = @import("peaks.zig");
const bands = @import("bands.zig");
const convert = @import("convert.zig");
const wave = @import("wave.zig");
const pcmdec = @import("pcmdec.zig");

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

// ── sample-format conversion (byte-exact ports of internal/audio loops) ──────

/// Serialize n interleaved f32 samples to LE bytes (out: 4*n) with pre-gain + ±1
/// clamp; gain 0/1 = unity raw-bits passthrough. Port of source.writeBytes.
export fn rz_f32_to_le(in: [*]const f32, n: usize, gain: f32, out: [*]u8) void {
    convert.f32ToLe(in[0..n], gain, out[0 .. n * 4]);
}

/// Fold interleaved ch-channel f32 (frames*ch) to stereo (out: frames*2). Mono
/// duplicates; >2 ch take the first two. Port of source.toDeviceStereo.
export fn rz_fold_stereo(in: [*]const f32, frames: usize, ch: u32, out: [*]f32) void {
    convert.foldStereo(in[0 .. frames * ch], ch, out[0 .. frames * 2]);
}

/// Batch-convert packed PCM frames to interleaved f32 [-1,1] (out: frames*ch).
/// src holds frames*block_align bytes. Ports wav decodeSample / aiff decodeSampleBE.
export fn rz_pcm_to_f32(src: [*]const u8, frames: usize, ch: u32, block_align: u32, bits: u32, is_float: u32, big_endian: u32, out: [*]f32) void {
    convert.pcmToF32(src[0 .. frames * block_align], frames, ch, block_align, bits, is_float != 0, big_endian != 0, out[0 .. frames * ch]);
}

// ── waveform-display kernels over u8 peak buckets ────────────────────────────

/// Per-column maxima of n peak buckets into cols columns. Port of giokit.WaveColumns.
export fn rz_wave_columns(peaks_in: [*]const u8, n: usize, cols: usize, out: [*]u8) void {
    wave.waveColumns(peaks_in[0..n], cols, out[0..cols]);
}

/// Smoothed 0..1 amplitude envelope at img_pps columns/sec (out: out_len f64,
/// = int(dur*img_pps)+1). Port of deckcard.buildEnv.
export fn rz_wave_env(peaks_in: [*]const u8, n: usize, dur: f64, img_pps: f64, out: [*]f64, out_len: usize) void {
    wave.waveEnv(peaks_in[0..n], dur, img_pps, out[0..out_len]);
}

// ── WAV/AIFF container decoders (P2) — Go owns file I/O, Zig owns parsing ────
// Open protocol: rz_{wav,aiff}dec_new → feed(NULL,0) → while ret==1 read
// need_len bytes at file offset need_off and feed them; 0 = header parsed
// (info valid), -1 = malformed. Then plan/read/decode per block; seek via
// seek_off (pure) + set_pos (commit after the caller's file seek succeeded).

export fn rz_wavdec_new() ?*pcmdec.Dec {
    const d = alloc.create(pcmdec.Dec) catch return null;
    d.* = pcmdec.Dec.init(.wav);
    return d;
}

export fn rz_aiffdec_new() ?*pcmdec.Dec {
    const d = alloc.create(pcmdec.Dec) catch return null;
    d.* = pcmdec.Dec.init(.aiff);
    return d;
}

export fn rz_pcmdec_free(d: *pcmdec.Dec) void {
    alloc.destroy(d);
}

export fn rz_pcmdec_feed(d: *pcmdec.Dec, buf: ?[*]const u8, len: usize, need_off: *u64, need_len: *u64) i32 {
    const slice: []const u8 = if (buf) |p| p[0..len] else &.{};
    const st = d.feed(slice);
    need_off.* = d.need_off;
    need_len.* = d.need_len;
    return @intFromEnum(st);
}

export fn rz_pcmdec_info(d: *const pcmdec.Dec, out: *pcmdec.CInfo) void {
    d.info(out);
}

export fn rz_pcmdec_seek_off(d: *const pcmdec.Dec, frame: i64, clamped: *i64) u64 {
    return d.seekOff(frame, clamped);
}

export fn rz_pcmdec_set_pos(d: *pcmdec.Dec, frame: i64) void {
    d.cur = frame;
}

export fn rz_pcmdec_plan(d: *const pcmdec.Dec, dst_cap_samples: usize, need_bytes: *u64) i64 {
    return d.plan(dst_cap_samples, need_bytes);
}

export fn rz_pcmdec_decode(d: *pcmdec.Dec, buf: ?[*]const u8, len: usize, dst: [*]f32) i64 {
    const slice: []const u8 = if (buf) |p| p[0..len] else &.{};
    return d.decode(slice, dst);
}

test {
    std.testing.refAllDecls(@This());
    _ = resample;
    _ = peaks;
    _ = bands;
    _ = convert;
    _ = wave;
    _ = pcmdec;
}
