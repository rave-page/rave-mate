// ravevr — VR-overlay raster executor. C ABI mirror: include/ravevr.h.
// Executes a display list into an NRGBA8 canvas with integer math that exactly
// replicates Go image/draw's RGBA64Image fallback path (NRGBA dst + Uniform src,
// optional *image.Alpha mask, op Over/Src) — the path the Go renderer hits — so
// output is byte-identical to the Go original (parity-tested from Go).
const std = @import("std");

const abi_version: u32 = 1;

export fn rz_vr_abi_version() u32 {
    return abi_version;
}

pub const RzVrOp = extern struct {
    x: i32,
    y: i32,
    w: i32,
    h: i32,
    kind: u32,
    sr: u16,
    sg: u16,
    sb: u16,
    sa: u16,
    mask_off: u32,
};

const k_store: u32 = 0;
const k_over: u32 = 1;
const k_glyph: u32 = 2;

// premul16 = Go color.NRGBA.RGBA() / NRGBA.RGBA64At: byte channel → premult 16-bit.
inline fn premul16(v8: u64, a16: u64) u64 {
    return ((v8 * 0x101) * a16) / 0xffff;
}

// unpremulByte = Go image.NRGBA.SetRGBA64: premult 16-bit → stored NRGBA byte.
inline fn unpremulByte(v16: u64, a16: u64) u8 {
    var v = v16;
    if (a16 != 0 and a16 != 0xffff) v = v * 0xffff / a16;
    return @truncate(v >> 8);
}

// storeRect fills the rect with exact bytes.
fn storeRect(pix: []u8, canvas_w: usize, ox: usize, oy: usize, ow: usize, oh: usize, b: [4]u8) void {
    var row: usize = 0;
    while (row < oh) : (row += 1) {
        var idx = ((oy + row) * canvas_w + ox) * 4;
        var col: usize = 0;
        while (col < ow) : (col += 1) {
            pix[idx] = b[0];
            pix[idx + 1] = b[1];
            pix[idx + 2] = b[2];
            pix[idx + 3] = b[3];
            idx += 4;
        }
    }
}

// overRect = uniform source-over fill (Go DrawMask FALLBACK1.17, mask==nil, op Over):
// out.ch = u16((d.ch * (m - sa)) / m) + s.ch, then unpremultiplied store.
fn overRect(pix: []u8, canvas_w: usize, ox: usize, oy: usize, ow: usize, oh: usize, op: RzVrOp) void {
    const sr: u64 = op.sr;
    const sg: u64 = op.sg;
    const sb: u64 = op.sb;
    const sa: u64 = op.sa;
    if (sa == 0xffff) { // fully-opaque source: out == src (matches the formula exactly)
        storeRect(pix, canvas_w, ox, oy, ow, oh, .{
            @truncate(sr >> 8), @truncate(sg >> 8), @truncate(sb >> 8), @truncate(sa >> 8),
        });
        return;
    }
    const ia: u64 = 0xffff - sa;
    var row: usize = 0;
    while (row < oh) : (row += 1) {
        var idx = ((oy + row) * canvas_w + ox) * 4;
        var col: usize = 0;
        while (col < ow) : (col += 1) {
            const da16: u64 = @as(u64, pix[idx + 3]) * 0x101;
            const dr = premul16(pix[idx], da16);
            const dg = premul16(pix[idx + 1], da16);
            const db = premul16(pix[idx + 2], da16);
            // Go adds in uint16 (wrap) — truncate each term like uint16(...) + srgba.ch.
            const or_: u64 = @as(u16, @truncate(dr * ia / 0xffff)) +% @as(u16, @truncate(sr));
            const og: u64 = @as(u16, @truncate(dg * ia / 0xffff)) +% @as(u16, @truncate(sg));
            const ob: u64 = @as(u16, @truncate(db * ia / 0xffff)) +% @as(u16, @truncate(sb));
            const oa: u64 = @as(u16, @truncate(da16 * ia / 0xffff)) +% @as(u16, @truncate(sa));
            pix[idx] = unpremulByte(or_, oa);
            pix[idx + 1] = unpremulByte(og, oa);
            pix[idx + 2] = unpremulByte(ob, oa);
            pix[idx + 3] = @truncate(oa >> 8);
            idx += 4;
        }
    }
}

// glyphRect = alpha-mask source-over (Go DrawMask FALLBACK1.17, *image.Alpha mask, op Over):
// ma = mask8*0x101; a = m - sa*ma/m; out.ch = u16((d.ch*a + s.ch*ma) / m).
fn glyphRect(pix: []u8, canvas_w: usize, ox: usize, oy: usize, ow: usize, oh: usize, op: RzVrOp, mask: []const u8) void {
    const sr: u64 = op.sr;
    const sg: u64 = op.sg;
    const sb: u64 = op.sb;
    const sa: u64 = op.sa;
    var row: usize = 0;
    while (row < oh) : (row += 1) {
        var idx = ((oy + row) * canvas_w + ox) * 4;
        var moff = @as(usize, op.mask_off) + row * ow;
        var col: usize = 0;
        while (col < ow) : (col += 1) {
            const m8 = mask[moff];
            moff += 1;
            if (m8 == 0) { // Go: ma == 0, op Over → no-op
                idx += 4;
                continue;
            }
            const ma: u64 = @as(u64, m8) * 0x101;
            const aa: u64 = 0xffff - (sa * ma) / 0xffff;
            const da16: u64 = @as(u64, pix[idx + 3]) * 0x101;
            const dr = premul16(pix[idx], da16);
            const dg = premul16(pix[idx + 1], da16);
            const db = premul16(pix[idx + 2], da16);
            const or_: u64 = @as(u16, @truncate((dr * aa + sr * ma) / 0xffff));
            const og: u64 = @as(u16, @truncate((dg * aa + sg * ma) / 0xffff));
            const ob: u64 = @as(u16, @truncate((db * aa + sb * ma) / 0xffff));
            const oa: u64 = @as(u16, @truncate((da16 * aa + sa * ma) / 0xffff));
            pix[idx] = unpremulByte(or_, oa);
            pix[idx + 1] = unpremulByte(og, oa);
            pix[idx + 2] = unpremulByte(ob, oa);
            pix[idx + 3] = @truncate(oa >> 8);
            idx += 4;
        }
    }
}

// validate checks every op BEFORE a single pixel is written, so a rejected list leaves the
// canvas untouched and the Go fallback can redraw from scratch. (Ops-only renders — border
// stamps, hover tints — composite onto existing pixels, so a half-executed list followed by
// the Go redraw would double-blend.)
fn validate(cw: usize, ch: usize, ops: []const RzVrOp, mask_len: usize) i32 {
    for (ops) |op| {
        if (op.x < 0 or op.y < 0 or op.w < 0 or op.h < 0) return -2;
        const ox: usize = @intCast(op.x);
        const oy: usize = @intCast(op.y);
        const ow: usize = @intCast(op.w);
        const oh: usize = @intCast(op.h);
        if (ow == 0 or oh == 0) continue;
        if (ox + ow > cw or oy + oh > ch) return -2;
        switch (op.kind) {
            k_store, k_over => {},
            k_glyph => if (@as(usize, op.mask_off) + ow * oh > mask_len) return -2,
            else => return -2,
        }
    }
    return 0;
}

export fn rz_vr_render(canvas: ?[*]u8, w: i32, h: i32, ops_ptr: ?[*]const RzVrOp, n_ops: usize, mask_ptr: ?[*]const u8, mask_len: usize) i32 {
    if (canvas == null or w <= 0 or h <= 0) return -1;
    if (n_ops > 0 and ops_ptr == null) return -1;
    const cw: usize = @intCast(w);
    const ch: usize = @intCast(h);
    const pix = canvas.?[0 .. cw * ch * 4];
    const mask: []const u8 = if (mask_ptr) |mp| mp[0..mask_len] else &[_]u8{};
    const ops: []const RzVrOp = if (n_ops > 0) ops_ptr.?[0..n_ops] else &[_]RzVrOp{};
    const bad = validate(cw, ch, ops, mask.len);
    if (bad != 0) return bad;
    var oi: usize = 0;
    while (oi < n_ops) : (oi += 1) {
        const op = ops[oi];
        const ox: usize = @intCast(op.x);
        const oy: usize = @intCast(op.y);
        const ow: usize = @intCast(op.w);
        const oh: usize = @intCast(op.h);
        if (ow == 0 or oh == 0) continue;
        switch (op.kind) {
            k_store => storeRect(pix, cw, ox, oy, ow, oh, .{
                @truncate(op.sr), @truncate(op.sg), @truncate(op.sb), @truncate(op.sa),
            }),
            k_over => overRect(pix, cw, ox, oy, ow, oh, op),
            k_glyph => glyphRect(pix, cw, ox, oy, ow, oh, op, mask),
            else => unreachable, // validate() rejected unknown kinds
        }
    }
    return 0;
}

// ---- tests (blend math vs hand-checked Go vectors; bounds rejection) ----

fn pxAt(pix: []const u8, w: usize, x: usize, y: usize) [4]u8 {
    const idx = (y * w + x) * 4;
    return .{ pix[idx], pix[idx + 1], pix[idx + 2], pix[idx + 3] };
}

// Go reference for one over-blend pixel (uniform premult src over NRGBA dst bytes).
fn goOverRef(d: [4]u8, s: [4]u16) [4]u8 {
    const sa: u64 = s[3];
    const ia: u64 = 0xffff - sa;
    const da16: u64 = @as(u64, d[3]) * 0x101;
    var out: [4]u8 = undefined;
    const oa: u64 = @as(u16, @truncate(da16 * ia / 0xffff)) +% @as(u16, @truncate(sa));
    inline for (0..3) |c| {
        const dc = premul16(d[c], da16);
        const oc: u64 = @as(u16, @truncate(dc * ia / 0xffff)) +% @as(u16, @truncate(@as(u64, s[c])));
        out[c] = unpremulByte(oc, oa);
    }
    out[3] = @truncate(oa >> 8);
    return out;
}

test "store fills exact bytes" {
    var pix = [_]u8{0} ** (4 * 4 * 4);
    const ops = [_]RzVrOp{.{ .x = 1, .y = 1, .w = 2, .h = 2, .kind = k_store, .sr = 10, .sg = 20, .sb = 30, .sa = 210, .mask_off = 0 }};
    try std.testing.expectEqual(@as(i32, 0), rz_vr_render(&pix, 4, 4, &ops, ops.len, null, 0));
    try std.testing.expectEqual([4]u8{ 10, 20, 30, 210 }, pxAt(&pix, 4, 1, 1));
    try std.testing.expectEqual([4]u8{ 0, 0, 0, 0 }, pxAt(&pix, 4, 0, 0));
    try std.testing.expectEqual([4]u8{ 0, 0, 0, 0 }, pxAt(&pix, 4, 3, 3));
}

test "opaque over equals src bytes" {
    var pix = [_]u8{ 5, 6, 7, 255 } ** 4;
    // premult of NRGBA(247,8,100,255): ch*0x101
    const ops = [_]RzVrOp{.{ .x = 0, .y = 0, .w = 2, .h = 2, .kind = k_over, .sr = 247 * 0x101, .sg = 8 * 0x101, .sb = 100 * 0x101, .sa = 0xffff, .mask_off = 0 }};
    try std.testing.expectEqual(@as(i32, 0), rz_vr_render(&pix, 2, 2, &ops, ops.len, null, 0));
    try std.testing.expectEqual([4]u8{ 247, 8, 100, 255 }, pxAt(&pix, 2, 0, 0));
}

test "translucent over matches Go reference math" {
    const dst: [4]u8 = .{ 40, 50, 60, 255 };
    // src NRGBA(10,10,14,172) premultiplied per Go NRGBA.RGBA(): (v*0x101*a)/0xff
    const s: [4]u16 = .{
        @intCast((10 * 0x101 * 172) / 0xff),
        @intCast((10 * 0x101 * 172) / 0xff),
        @intCast((14 * 0x101 * 172) / 0xff),
        @intCast(172 * 0x101),
    };
    var pix: [4]u8 = dst;
    const ops = [_]RzVrOp{.{ .x = 0, .y = 0, .w = 1, .h = 1, .kind = k_over, .sr = s[0], .sg = s[1], .sb = s[2], .sa = s[3], .mask_off = 0 }};
    try std.testing.expectEqual(@as(i32, 0), rz_vr_render(&pix, 1, 1, &ops, ops.len, null, 0));
    try std.testing.expectEqual(goOverRef(dst, s), pxAt(&pix, 1, 0, 0));
}

test "glyph mask blends per-pixel; ma=0 no-op; ma=255 equals over" {
    var pix = [_]u8{ 40, 50, 60, 255 } ** 3;
    const mask = [_]u8{ 0, 255, 128 };
    const s: [4]u16 = .{ 250 * 0x101, 250 * 0x101, 250 * 0x101, 0xffff }; // opaque white-ish
    const ops = [_]RzVrOp{.{ .x = 0, .y = 0, .w = 3, .h = 1, .kind = k_glyph, .sr = s[0], .sg = s[1], .sb = s[2], .sa = s[3], .mask_off = 0 }};
    try std.testing.expectEqual(@as(i32, 0), rz_vr_render(&pix, 3, 1, &ops, ops.len, &mask, mask.len));
    try std.testing.expectEqual([4]u8{ 40, 50, 60, 255 }, pxAt(&pix, 3, 0, 0)); // ma=0 untouched
    try std.testing.expectEqual([4]u8{ 250, 250, 250, 255 }, pxAt(&pix, 3, 1, 0)); // ma=255, sa=1 → src
    const mid = pxAt(&pix, 3, 2, 0); // ma=128 partial blend, alpha stays 255
    try std.testing.expectEqual(@as(u8, 255), mid[3]);
    try std.testing.expect(mid[0] > 40 and mid[0] < 250);
}

test "bounds + kind rejection" {
    var pix = [_]u8{0} ** (2 * 2 * 4);
    const oob = [_]RzVrOp{.{ .x = 1, .y = 0, .w = 2, .h = 1, .kind = k_store, .sr = 0, .sg = 0, .sb = 0, .sa = 0, .mask_off = 0 }};
    try std.testing.expectEqual(@as(i32, -2), rz_vr_render(&pix, 2, 2, &oob, oob.len, null, 0));
    const neg = [_]RzVrOp{.{ .x = -1, .y = 0, .w = 1, .h = 1, .kind = k_store, .sr = 0, .sg = 0, .sb = 0, .sa = 0, .mask_off = 0 }};
    try std.testing.expectEqual(@as(i32, -2), rz_vr_render(&pix, 2, 2, &neg, neg.len, null, 0));
    const badkind = [_]RzVrOp{.{ .x = 0, .y = 0, .w = 1, .h = 1, .kind = 9, .sr = 0, .sg = 0, .sb = 0, .sa = 0, .mask_off = 0 }};
    try std.testing.expectEqual(@as(i32, -2), rz_vr_render(&pix, 2, 2, &badkind, badkind.len, null, 0));
    const badmask = [_]RzVrOp{.{ .x = 0, .y = 0, .w = 2, .h = 2, .kind = k_glyph, .sr = 0, .sg = 0, .sb = 0, .sa = 0, .mask_off = 1 }};
    var m = [_]u8{ 1, 2, 3, 4 }; // needs 4 bytes at off 1 → 5 > 4
    try std.testing.expectEqual(@as(i32, -2), rz_vr_render(&pix, 2, 2, &badmask, badmask.len, &m, m.len));
    try std.testing.expectEqual(@as(i32, -1), rz_vr_render(null, 2, 2, &oob, oob.len, null, 0));
}

test "rejected list is atomic — no partial writes" {
    var pix = [_]u8{0} ** (2 * 2 * 4);
    const ops = [_]RzVrOp{
        .{ .x = 0, .y = 0, .w = 1, .h = 1, .kind = k_store, .sr = 9, .sg = 9, .sb = 9, .sa = 9, .mask_off = 0 }, // valid
        .{ .x = 1, .y = 1, .w = 5, .h = 1, .kind = k_store, .sr = 1, .sg = 1, .sb = 1, .sa = 1, .mask_off = 0 }, // OOB
    };
    try std.testing.expectEqual(@as(i32, -2), rz_vr_render(&pix, 2, 2, &ops, ops.len, null, 0));
    try std.testing.expectEqual([4]u8{ 0, 0, 0, 0 }, pxAt(&pix, 2, 0, 0)); // op 0 never ran
}
