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

/// Batched square-cell fill into a 4bpp zero-origin image (R,G,B,A byte order),
/// clipped to w*h. cells = n*4 i32 {x0, y0, size, rgba LE (R = low byte)}.
/// Port of vrslgrid fillCell/fillCellAt/fillMetaCell (per-pixel SetRGBA loops).
pub fn fillCells(pix: []u8, stride: usize, w: usize, h: usize, cells: []const i32) void {
    var k: usize = 0;
    while (k + 4 <= cells.len) : (k += 4) {
        fillCell(pix, stride, w, h, cells[k], cells[k + 1], cells[k + 2], @bitCast(cells[k + 3]));
    }
}

fn fillCell(pix: []u8, stride: usize, w: usize, h: usize, cx: i32, cy: i32, size: i32, rgba: u32) void {
    if (size <= 0) return;
    const xlo = @max(@as(i64, cx), 0);
    const ylo = @max(@as(i64, cy), 0);
    const xhi = @min(@as(i64, cx) + size, @as(i64, @intCast(w)));
    const yhi = @min(@as(i64, cy) + size, @as(i64, @intCast(h)));
    if (xlo >= xhi or ylo >= yhi) return;
    const xa: usize = @intCast(xlo);
    const ya: usize = @intCast(ylo);
    const xb: usize = @intCast(xhi);
    const yb: usize = @intCast(yhi);
    const px = [4]u8{ @truncate(rgba), @truncate(rgba >> 8), @truncate(rgba >> 16), @truncate(rgba >> 24) };
    const seg = (xb - xa) * 4;
    const first = pix[ya * stride + xa * 4 ..][0..seg];
    var x: usize = 0;
    while (x < seg) : (x += 4) first[x..][0..4].* = px;
    var y = ya + 1; // rows are identical: memcpy the first one
    while (y < yb) : (y += 1) {
        @memcpy(pix[y * stride + xa * 4 ..][0..seg], first);
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

test "fillCells clip + colour order" {
    var pix = [_]u8{0} ** (4 * 4 * 4); // 4x4
    const cells = [_]i32{
        1,  1, 2, @bitCast(@as(u32, 0xFF03_0201)), // r=1 g=2 b=3 a=255
        -1, 3, 2, @bitCast(@as(u32, 0xFF00_00FF)), // clipped to (0,3)-(1,4): red
        9,  9, 2, 0, // fully outside
    };
    fillCells(&pix, 16, 4, 4, &cells);
    try testing.expectEqualSlices(u8, &[_]u8{ 1, 2, 3, 255 }, pix[16 + 4 ..][0..4]); // (1,1)
    try testing.expectEqualSlices(u8, &[_]u8{ 1, 2, 3, 255 }, pix[2 * 16 + 8 ..][0..4]); // (2,2)
    try testing.expectEqualSlices(u8, &[_]u8{ 255, 0, 0, 255 }, pix[3 * 16 ..][0..4]); // (0,3)
    try testing.expectEqual(@as(u8, 0), pix[3 * 16 + 8]); // (2,3) untouched
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
