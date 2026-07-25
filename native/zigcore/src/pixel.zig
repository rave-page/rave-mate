//! Video pixel kernels (P3) — byte-exact ports of Go per-pixel loops on the video
//! plane. Integer-only; parity tests diff vs the Go originals (which stay as
//! fallback + golden reference).

const std = @import("std");

/// Strided RGBA→packed RGB24 (alpha lane dropped; dst stride = w*3).
/// Port of mocapnode.frameFromNRGBA's copy loop (videoshare receiver consumer).
pub fn rgbaToRgb24(src: []const u8, src_stride: usize, w: usize, h: usize, dst: []u8) void {
    for (0..h) |y| {
        const row = src[y * src_stride ..][0 .. w * 4];
        const out = dst[y * w * 3 ..][0 .. w * 3];
        for (0..w) |x| {
            out[x * 3 ..][0..3].* = row[x * 4 ..][0..3].*;
        }
    }
}

const testing = std.testing;

test "rgbaToRgb24 strided" {
    // 2x2, stride 12 (one pad pixel per row)
    const src = [_]u8{
        1, 2, 3, 255, 4,  5,  6,  255, 9, 9, 9, 9,
        7, 8, 9, 255, 10, 11, 12, 255, 9, 9, 9, 9,
    };
    var dst: [12]u8 = undefined;
    rgbaToRgb24(&src, 12, 2, 2, &dst);
    try testing.expectEqualSlices(u8, &[_]u8{ 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12 }, &dst);
}
