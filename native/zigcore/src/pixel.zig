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

/// Per-pixel multi-target colour classification: labels[y*w+x] = t+1 for the FIRST
/// target whose RGB matches all channels within tol, else 0. targets = n*3 RGB bytes;
/// bgra swaps the in-pixel R/B order; bpp = 3 (RGB24) or 4 (BGRA/RGBA).
/// Port of mocapnode.scanBlobs' labeling pass (blob BFS stays Go-side).
pub fn pxLabel(pix: []const u8, stride: usize, w: usize, h: usize, bpp: usize, bgra: bool, targets: []const u8, tol: u8, labels: []u8) void {
    const n = targets.len / 3;
    for (0..h) |y| {
        const row = y * stride;
        for (0..w) |x| {
            const i = row + x * bpp;
            var r = pix[i];
            const g = pix[i + 1];
            var b = pix[i + 2];
            if (bgra) {
                const tmp = r;
                r = b;
                b = tmp;
            }
            var lab: u8 = 0;
            for (0..n) |t| {
                const c = targets[t * 3 ..][0..3];
                if (absDiff(r, c[0]) <= tol and absDiff(g, c[1]) <= tol and absDiff(b, c[2]) <= tol) {
                    lab = @intCast(t + 1);
                    break;
                }
            }
            labels[y * w + x] = lab;
        }
    }
}

fn absDiff(a: u8, b: u8) u8 {
    return if (a > b) a - b else b - a;
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

test "pxLabel first-match + bgra swap" {
    // 2 px BGRA: (r=30,g=20,b=10) and (r=200,g=0,b=0)
    const pix = [_]u8{ 10, 20, 30, 255, 0, 0, 200, 255 };
    const targets = [_]u8{ 30, 20, 10, 30, 20, 11, 199, 1, 0 }; // t0 + t1 both match px0 -> first wins
    var labels: [2]u8 = undefined;
    pxLabel(&pix, 8, 2, 1, 4, true, &targets, 2, &labels);
    try testing.expectEqual(@as(u8, 1), labels[0]);
    try testing.expectEqual(@as(u8, 3), labels[1]);
}
