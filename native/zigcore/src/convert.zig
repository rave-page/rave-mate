//! Sample-format conversion kernels — byte-exact ports of the Go per-sample loops in
//! internal/audio (source.go writeBytes/toDeviceStereo, wav.go decodeSample,
//! aiff.go decodeSampleBE). Strict float mode (default) — parity tests diff vs Go.

const std = @import("std");
const builtin = @import("builtin");

/// Serialize interleaved f32 to little-endian bytes with pre-gain + ±1 clamp.
/// gain 0 or 1 = unity passthrough (raw bits). Port of source.writeBytes.
pub fn f32ToLe(in: []const f32, gain: f32, out: []u8) void {
    if (gain == 0 or gain == 1) {
        if (comptime builtin.cpu.arch.endian() == .little) {
            @memcpy(out[0 .. in.len * 4], std.mem.sliceAsBytes(in));
            return;
        }
        for (in, 0..) |v, i| std.mem.writeInt(u32, out[4 * i ..][0..4], @bitCast(v), .little);
        return;
    }
    for (in, 0..) |v0, i| {
        var v = v0 * gain;
        if (v > 1) {
            v = 1;
        } else if (v < -1) {
            v = -1;
        }
        std.mem.writeInt(u32, out[4 * i ..][0..4], @bitCast(v), .little);
    }
}

/// Fold interleaved `ch`-channel f32 to interleaved stereo: mono duplicates, >2 ch
/// take the first two. Port of source.toDeviceStereo (ch==2 stays Go-side, zero-copy).
pub fn foldStereo(in: []const f32, ch: u32, out: []f32) void {
    if (ch == 0) return;
    const frames = in.len / ch;
    if (ch == 1) {
        for (0..frames) |i| {
            const v = in[i];
            out[2 * i] = v;
            out[2 * i + 1] = v;
        }
        return;
    }
    for (0..frames) |i| {
        out[2 * i] = in[i * ch];
        out[2 * i + 1] = in[i * ch + 1];
    }
}

/// One frame-batch PCM→f32 conversion, comptime-specialized per format.
/// LE 8-bit is unsigned (WAV), BE 8-bit signed (AIFF) — mirrors the Go decoders.
fn convLoop(comptime bits: u32, comptime is_float: bool, comptime be: bool, src: []const u8, frames: usize, ch: u32, block_align: u32, out: []f32) void {
    const bps = bits / 8;
    const endian: std.builtin.Endian = if (be) .big else .little;
    for (0..frames) |i| {
        const base = i * block_align;
        for (0..ch) |c| {
            const b = src[base + c * bps ..][0..bps];
            out[i * ch + c] = blk: {
                if (is_float) {
                    if (bits == 32) break :blk @bitCast(std.mem.readInt(u32, b[0..4], endian));
                    if (bits == 64) break :blk @floatCast(@as(f64, @bitCast(std.mem.readInt(u64, b[0..8], endian))));
                    break :blk 0;
                }
                switch (bits) {
                    8 => {
                        if (be) break :blk @as(f32, @floatFromInt(@as(i8, @bitCast(b[0])))) / 128.0;
                        break :blk (@as(f32, @floatFromInt(b[0])) - 128.0) / 128.0;
                    },
                    16 => break :blk @as(f32, @floatFromInt(@as(i16, @bitCast(std.mem.readInt(u16, b[0..2], endian))))) / 32768.0,
                    24 => {
                        var v: i32 = if (be)
                            @as(i32, b[2]) | (@as(i32, b[1]) << 8) | (@as(i32, b[0]) << 16)
                        else
                            @as(i32, b[0]) | (@as(i32, b[1]) << 8) | (@as(i32, b[2]) << 16);
                        if (v & 0x800000 != 0) v |= @bitCast(@as(u32, 0xFF000000)); // sign-extend
                        break :blk @as(f32, @floatFromInt(v)) / 8388608.0;
                    },
                    32 => break :blk @as(f32, @floatFromInt(@as(i32, @bitCast(std.mem.readInt(u32, b[0..4], endian))))) / 2147483648.0,
                    else => break :blk 0,
                }
            };
        }
    }
}

/// Batch-convert `frames` packed PCM frames to interleaved f32 in [-1,1].
/// src holds frames*block_align bytes; out holds frames*ch. Unsupported depth → zeros
/// (Go decoders validate earlier; mirrors decodeSample's 0 default).
pub fn pcmToF32(src: []const u8, frames: usize, ch: u32, block_align: u32, bits: u32, is_float: bool, big_endian: bool, out: []f32) void {
    if (ch == 0 or frames == 0) return;
    if (is_float) {
        switch (bits) {
            32 => if (big_endian) convLoop(32, true, true, src, frames, ch, block_align, out) else convLoop(32, true, false, src, frames, ch, block_align, out),
            64 => if (big_endian) convLoop(64, true, true, src, frames, ch, block_align, out) else convLoop(64, true, false, src, frames, ch, block_align, out),
            else => @memset(out[0 .. frames * ch], 0),
        }
        return;
    }
    switch (bits) {
        8 => if (big_endian) convLoop(8, false, true, src, frames, ch, block_align, out) else convLoop(8, false, false, src, frames, ch, block_align, out),
        16 => if (big_endian) convLoop(16, false, true, src, frames, ch, block_align, out) else convLoop(16, false, false, src, frames, ch, block_align, out),
        24 => if (big_endian) convLoop(24, false, true, src, frames, ch, block_align, out) else convLoop(24, false, false, src, frames, ch, block_align, out),
        32 => if (big_endian) convLoop(32, false, true, src, frames, ch, block_align, out) else convLoop(32, false, false, src, frames, ch, block_align, out),
        else => @memset(out[0 .. frames * ch], 0),
    }
}

const testing = std.testing;

test "f32ToLe unity + gain clamp" {
    const in = [_]f32{ 0.5, -0.25, 2.0 };
    var out: [12]u8 = undefined;
    f32ToLe(&in, 1, &out);
    try testing.expectEqual(@as(u32, @bitCast(@as(f32, 0.5))), std.mem.readInt(u32, out[0..4], .little));
    f32ToLe(&in, 2, &out);
    try testing.expectEqual(@as(u32, @bitCast(@as(f32, 1.0))), std.mem.readInt(u32, out[0..4], .little)); // 0.5*2
    try testing.expectEqual(@as(u32, @bitCast(@as(f32, -0.5))), std.mem.readInt(u32, out[4..8], .little));
    try testing.expectEqual(@as(u32, @bitCast(@as(f32, 1.0))), std.mem.readInt(u32, out[8..12], .little)); // clamped
}

test "foldStereo mono + 6ch" {
    const mono = [_]f32{ 0.1, 0.2 };
    var st: [4]f32 = undefined;
    foldStereo(&mono, 1, &st);
    try testing.expectEqual(@as(f32, 0.1), st[1]);
    const six = [_]f32{ 1, 2, 3, 4, 5, 6 };
    var st2: [2]f32 = undefined;
    foldStereo(&six, 6, &st2);
    try testing.expectEqual(@as(f32, 2), st2[1]);
}

test "pcmToF32 s16le + s24be" {
    var b: [4]u8 = undefined;
    std.mem.writeInt(i16, b[0..2], -32768, .little);
    std.mem.writeInt(i16, b[2..4], 16384, .little);
    var out: [2]f32 = undefined;
    pcmToF32(&b, 1, 2, 4, 16, false, false, &out);
    try testing.expectEqual(@as(f32, -1.0), out[0]);
    try testing.expectEqual(@as(f32, 0.5), out[1]);
    const b24 = [_]u8{ 0x40, 0x00, 0x00 }; // BE 0x400000 = +0.5
    var o1: [1]f32 = undefined;
    pcmToF32(&b24, 1, 1, 3, 24, false, true, &o1);
    try testing.expectEqual(@as(f32, 0.5), o1[0]);
}
