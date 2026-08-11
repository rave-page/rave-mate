//! Editor direct-manipulation geometry — byte-exact port of
//! internal/visualeditor/geometry.go on the flat-box form (hit-test, handles,
//! snap, resize, rotate). Transcendentals are ports of Go's math (Cephes
//! sin/cos/atan2) so results match bit-for-bit; |rad| ≥ Go's trig-reduce
//! threshold (1<<29) falls back to @sin/@cos (unreachable for editor docs).
//! Go path = fallback + golden reference; parity gate:
//! internal/visualeditor/geometry_zig_parity_test.go.

const std = @import("std");

/// Flat leaf placement: {x,y,w,h,sx,sy,rot} — visualeditor.FlatBox sans Locked
/// (lock filtering stays Go-side). Layout-compatible with a 7-f64 row.
pub const Box = extern struct { x: f64, y: f64, w: f64, h: f64, sx: f64, sy: f64, rot: f64 };

/// Handle ints mirror visualeditor.Handle: 0 none, 1..8 NW,N,NE,E,SE,S,SW,W, 9 rotate.
pub const handle_none: i32 = 0;
pub const handle_rotate: i32 = 9;

const min_size_px: f64 = 8;

// ── Go math ports (bit-exact with math.Sin/Cos/Atan2 below reduce threshold) ──

const go_pi: f64 = std.math.pi;
const reduce_threshold: f64 = @floatFromInt(@as(u64, 1) << 29);
const four_over_pi: f64 = @bitCast(@as(u64, 0x3FF45F306DC9C883)); // Go's 4/Pi
// Pi/4 split in three parts + polynomial coefficients: Go's decimal literals
// verbatim (the hex comments in Go's sin.go are stale — decimals govern).
const pi4a: f64 = 7.85398125648498535156e-1;
const pi4b: f64 = 3.77489470793079817668e-8;
const pi4c: f64 = 2.69515142907905952645e-15;

const sin_c = [6]f64{ // math _sin coefficients
    1.58962301576546568060e-10,
    -2.50507477628578072866e-8,
    2.75573136213857245213e-6,
    -1.98412698295895385996e-4,
    8.33333333332211858878e-3,
    -1.66666666666666307295e-1,
};
const cos_c = [6]f64{ // math _cos coefficients
    -1.13585365213876817300e-11,
    2.08757008419747316778e-9,
    -2.75573141792967388112e-7,
    2.48015872888517045348e-5,
    -1.38888888888730564116e-3,
    4.16666666666665929218e-2,
};

fn polySin(z: f64) f64 {
    const zz = z * z;
    return z + z * zz * ((((((sin_c[0] * zz) + sin_c[1]) * zz + sin_c[2]) * zz + sin_c[3]) * zz + sin_c[4]) * zz + sin_c[5]);
}

fn polyCos(z: f64) f64 {
    const zz = z * z;
    return 1.0 - 0.5 * zz + zz * zz * ((((((cos_c[0] * zz) + cos_c[1]) * zz + cos_c[2]) * zz + cos_c[3]) * zz + cos_c[4]) * zz + cos_c[5]);
}

fn goSin(x0: f64) f64 {
    var x = x0;
    if (x == 0 or x != x) return x;
    if (std.math.isInf(x)) return std.math.nan(f64);
    var sign = false;
    if (x < 0) {
        x = -x;
        sign = true;
    }
    if (x >= reduce_threshold) return @sin(x0);
    var j: u64 = @intFromFloat(x * four_over_pi);
    var y: f64 = @floatFromInt(j);
    if (j & 1 == 1) { // map zeros to origin
        j += 1;
        y += 1;
    }
    j &= 7; // octant mod 2Pi
    const z = ((x - y * pi4a) - y * pi4b) - y * pi4c; // extended-precision mod
    if (j > 3) { // reflect in x axis
        sign = !sign;
        j -= 4;
    }
    var r = if (j == 1 or j == 2) polyCos(z) else polySin(z);
    if (sign) r = -r;
    return r;
}

fn goCos(x0: f64) f64 {
    if (x0 != x0 or std.math.isInf(x0)) return std.math.nan(f64);
    var sign = false;
    const x = @abs(x0);
    if (x >= reduce_threshold) return @cos(x0);
    var j: u64 = @intFromFloat(x * four_over_pi);
    var y: f64 = @floatFromInt(j);
    if (j & 1 == 1) {
        j += 1;
        y += 1;
    }
    j &= 7;
    const z = ((x - y * pi4a) - y * pi4b) - y * pi4c;
    if (j > 3) {
        j -= 4;
        sign = !sign;
    }
    if (j > 1) sign = !sign;
    var r = if (j == 1 or j == 2) polySin(z) else polyCos(z);
    if (sign) r = -r;
    return r;
}

fn xatan(x: f64) f64 { // Cephes atan on [0, 0.66]
    const p0 = -8.750608600031904122785e-01;
    const p1 = -1.615753718733365076637e+01;
    const p2 = -7.500855792314704667340e+01;
    const p3 = -1.228866684490136173410e+02;
    const p4 = -6.485021904942025371773e+01;
    const q0 = 2.485846490142306297962e+01;
    const q1 = 1.650270098316988542046e+02;
    const q2 = 4.328810604912902668951e+02;
    const q3 = 4.853903996359136964868e+02;
    const q4 = 1.945506571482613964425e+02;
    var z = x * x;
    z = z * ((((p0 * z + p1) * z + p2) * z + p3) * z + p4) / (((((z + q0) * z + q1) * z + q2) * z + q3) * z + q4);
    z = x * z + x;
    return z;
}

fn satan(x: f64) f64 { // positive-arg range reduction
    const morebits = 6.123233995736765886130e-17; // pi/2 = PIO2 + morebits
    const tan3pio8 = 2.41421356237309504880;
    if (x <= 0.66) return xatan(x);
    if (x > tan3pio8) return go_pi / 2 - xatan(1 / x) + morebits;
    return go_pi / 4 + xatan((x - 1) / (x + 1)) + 0.5 * morebits;
}

fn goAtan(x: f64) f64 {
    if (x == 0) return x;
    if (x > 0) return satan(x);
    return -satan(-x);
}

fn goAtan2(y: f64, x: f64) f64 {
    const cs = std.math.copysign;
    if (y != y or x != x) return std.math.nan(f64);
    if (y == 0) {
        if (x >= 0 and !std.math.signbit(x)) return cs(@as(f64, 0), y);
        return cs(go_pi, y);
    }
    if (x == 0) return cs(go_pi / 2, y);
    if (std.math.isInf(x)) {
        if (x > 0) {
            if (std.math.isInf(y)) return cs(go_pi / 4, y);
            return cs(@as(f64, 0), y);
        }
        if (std.math.isInf(y)) return cs(3 * go_pi / 4, y);
        return cs(go_pi, y);
    }
    if (std.math.isInf(y)) return cs(go_pi / 2, y);
    const q = goAtan(y / x);
    if (x < 0) {
        if (q <= 0) return q + go_pi;
        return q - go_pi;
    }
    return q;
}

fn gMin(x: f64, y: f64) f64 { // math.Min semantics (-0 preferred)
    if (x != x or y != y) return std.math.nan(f64);
    if (x == 0 and x == y) return if (std.math.signbit(x)) x else y;
    return if (x < y) x else y;
}

fn gMax(x: f64, y: f64) f64 { // math.Max semantics (+0 preferred)
    if (x != x or y != y) return std.math.nan(f64);
    if (x == 0 and x == y) return if (std.math.signbit(x)) y else x;
    return if (x > y) x else y;
}

// ── affine model (scale → rotate about the box center) ───────────────────────

fn mat(b: Box) [4]f64 { // content→doc rotation·scale coefficients {ma,mb,mc,md}
    const rad = b.rot * go_pi / 180;
    const c = goCos(rad);
    const s = goSin(rad);
    return .{ b.sx * c, -b.sy * s, b.sx * s, b.sy * c };
}

fn map(b: Box, cx: f64, cy: f64) [2]f64 { // content → doc
    const m = mat(b);
    const dx = cx - b.w / 2;
    const dy = cy - b.h / 2;
    return .{ b.x + b.w / 2 + m[0] * dx + m[1] * dy, b.y + b.h / 2 + m[2] * dx + m[3] * dy };
}

fn invMap(b: Box, px: f64, py: f64, out: *[2]f64) bool { // doc → content
    const m = mat(b);
    const det = m[0] * m[3] - m[1] * m[2];
    if (@abs(det) < 1e-9) return false;
    const dx = px - (b.x + b.w / 2);
    const dy = py - (b.y + b.h / 2);
    out.* = .{ b.w / 2 + (m[3] * dx - m[1] * dy) / det, b.h / 2 + (-m[2] * dx + m[0] * dy) / det };
    return true;
}

fn contains(b: Box, px: f64, py: f64) bool {
    var c: [2]f64 = undefined;
    return invMap(b, px, py, &c) and c[0] >= 0 and c[0] <= b.w and c[1] >= 0 and c[1] <= b.h;
}

const B4 = struct { min_x: f64, min_y: f64, max_x: f64, max_y: f64 };

fn bounds(b: Box) B4 { // doc-space AABB of the transformed box
    const inf = std.math.inf(f64);
    var r = B4{ .min_x = inf, .min_y = inf, .max_x = -inf, .max_y = -inf };
    for ([4][2]f64{ .{ 0, 0 }, .{ b.w, 0 }, .{ 0, b.h }, .{ b.w, b.h } }) |p| {
        const xy = map(b, p[0], p[1]);
        r.min_x = gMin(r.min_x, xy[0]);
        r.max_x = gMax(r.max_x, xy[0]);
        r.min_y = gMin(r.min_y, xy[1]);
        r.max_y = gMax(r.max_y, xy[1]);
    }
    return r;
}

// ── kernels ──────────────────────────────────────────────────────────────────

/// Topmost box index containing (px,py), or -1. Port of HitTest.
pub fn hitTest(boxes: []const Box, px: f64, py: f64) i32 {
    var i: usize = boxes.len;
    while (i > 0) {
        i -= 1;
        if (contains(boxes[i], px, py)) return @intCast(i);
    }
    return -1;
}

/// Handle within tol doc px of the point (rotate anchor rot_off above the top
/// edge); ties keep the later handle (Go `<=`). Port of HandleAt.
pub fn handleAt(b: Box, px: f64, py: f64, tol: f64, rot_off: f64) i32 {
    var best: i32 = handle_none;
    var best_d = tol * tol;
    const pts = [8][2]f64{
        .{ 0, 0 },     .{ b.w / 2, 0 },   .{ b.w, 0 }, .{ b.w, b.h / 2 },
        .{ b.w, b.h }, .{ b.w / 2, b.h }, .{ 0, b.h }, .{ 0, b.h / 2 },
    };
    for (pts, 0..) |p, i| {
        const xy = map(b, p[0], p[1]);
        const d = (px - xy[0]) * (px - xy[0]) + (py - xy[1]) * (py - xy[1]);
        if (d <= best_d) {
            best = @intCast(i + 1);
            best_d = d;
        }
    }
    const asy = @abs(b.sy);
    if (asy > 1e-9) {
        const xy = map(b, b.w / 2, -rot_off / asy);
        const d = (px - xy[0]) * (px - xy[0]) + (py - xy[1]) * (py - xy[1]);
        if (d <= best_d) best = handle_rotate;
    }
    return best;
}

const AxisSnap = struct { best: f64, adj: f64 = 0, line: f64 = 0, ok: bool = false };

// One candidate against the moving triple — strict < keeps the FIRST candidate
// on ties, matching snapAxis's candidate-major loop.
fn snapTriple(s: *AxisSnap, lo: f64, mid: f64, hi: f64, c: f64) void {
    for ([3]f64{ lo, mid, hi }) |v| {
        const d = @abs(v - c);
        if (d < s.best) {
            s.best = d;
            s.adj = c - v;
            s.line = c;
            s.ok = true;
        }
    }
}

/// Snap a proposed move delta to canvas + other boxes' edges/centers.
/// io: dx/dy adjusted in place; guides = up to 2×{vert(0/1),pos}; returns the
/// guide count (X guide first). Port of SnapMove.
pub fn snapMove(boxes: []const Box, move_idx: usize, dx: *f64, dy: *f64, thresh: f64, doc_w: f64, doc_h: f64, guides: *[4]f64) u32 {
    if (move_idx >= boxes.len) return 0;
    var m = boxes[move_idx];
    m.x += dx.*;
    m.y += dy.*;
    const mb = bounds(m);
    const mid_x = (mb.min_x + mb.max_x) / 2;
    const mid_y = (mb.min_y + mb.max_y) / 2;
    var ax = AxisSnap{ .best = thresh };
    var ay = AxisSnap{ .best = thresh };
    // candidate order matches Go's vc/hc appends: canvas lines, then each other
    // box's {min, mid, max} in document order
    for ([3]f64{ 0, doc_w / 2, doc_w }) |c| snapTriple(&ax, mb.min_x, mid_x, mb.max_x, c);
    for ([3]f64{ 0, doc_h / 2, doc_h }) |c| snapTriple(&ay, mb.min_y, mid_y, mb.max_y, c);
    for (boxes, 0..) |b, i| {
        if (i == move_idx) continue;
        const bb = bounds(b);
        for ([3]f64{ bb.min_x, (bb.min_x + bb.max_x) / 2, bb.max_x }) |c| snapTriple(&ax, mb.min_x, mid_x, mb.max_x, c);
        for ([3]f64{ bb.min_y, (bb.min_y + bb.max_y) / 2, bb.max_y }) |c| snapTriple(&ay, mb.min_y, mid_y, mb.max_y, c);
    }
    var n: u32 = 0;
    if (ax.ok) {
        dx.* += ax.adj;
        guides[0] = 1;
        guides[1] = ax.line;
        n += 1;
    }
    if (ay.ok) {
        dy.* += ay.adj;
        guides[n * 2] = 0;
        guides[n * 2 + 1] = ay.line;
        n += 1;
    }
    return n;
}

/// New {w,h,x,y} for dragging handle to doc point (px,py), opposite edge/corner
/// anchored; uniform locks aspect. Port of ResizeBox.
pub fn resizeBox(b: Box, handle: i32, px: f64, py: f64, uniform: bool, out: *[4]f64) void {
    out.* = .{ b.w, b.h, b.x, b.y };
    var inv: [2]f64 = undefined;
    if (!invMap(b, px, py, &inv)) return;
    const cx = inv[0];
    const cy = inv[1];
    var nw = b.w;
    var nh = b.h;
    var q: [2]f64 = undefined; // anchor in orig content space
    switch (handle) {
        1 => { // NW
            nw = b.w - cx;
            nh = b.h - cy;
            q = .{ b.w, b.h };
        },
        2 => { // N
            nh = b.h - cy;
            q = .{ b.w / 2, b.h };
        },
        3 => { // NE
            nw = cx;
            nh = b.h - cy;
            q = .{ 0, b.h };
        },
        4 => { // E
            nw = cx;
            q = .{ 0, b.h / 2 };
        },
        5 => { // SE
            nw = cx;
            nh = cy;
            q = .{ 0, 0 };
        },
        6 => { // S
            nh = cy;
            q = .{ b.w / 2, 0 };
        },
        7 => { // SW
            nw = b.w - cx;
            nh = cy;
            q = .{ b.w, 0 };
        },
        8 => { // W
            nw = b.w - cx;
            q = .{ b.w, b.h / 2 };
        },
        else => return,
    }
    if (uniform and b.w > 0 and b.h > 0) {
        const kw = nw / b.w;
        const kh = nh / b.h;
        var k = kw;
        if (@abs(kh - 1) > @abs(kw - 1)) k = kh;
        if (k < min_size_px / gMax(b.w, b.h)) k = min_size_px / gMax(b.w, b.h);
        nw = b.w * k;
        nh = b.h * k;
    }
    nw = gMax(nw, min_size_px);
    nh = gMax(nh, min_size_px);

    // re-anchor: the anchor's doc position must survive the size change
    const a = map(b, q[0], q[1]);
    var qx = q[0]; // anchor in NEW content space (same relative corner/edge)
    var qy = q[1];
    switch (handle) {
        1 => {
            qx = nw;
            qy = nh;
        },
        2 => {
            qx = nw / 2;
            qy = nh;
        },
        3 => {
            qx = 0;
            qy = nh;
        },
        4 => {
            qx = 0;
            qy = nh / 2;
        },
        5 => {
            qx = 0;
            qy = 0;
        },
        6 => {
            qx = nw / 2;
            qy = 0;
        },
        7 => {
            qx = nw;
            qy = 0;
        },
        8 => {
            qx = nw;
            qy = nh / 2;
        },
        else => {},
    }
    const m = mat(b);
    const dxq = qx - nw / 2;
    const dyq = qy - nh / 2;
    out.* = .{ nw, nh, a[0] - (m[0] * dxq + m[1] * dyq) - nw / 2, a[1] - (m[2] * dxq + m[3] * dyq) - nh / 2 };
}

/// Doc-space angle (deg) from the box center to the point. Port of AngleAt.
pub fn angleAt(b: Box, px: f64, py: f64) f64 {
    return goAtan2(py - (b.y + b.h / 2), px - (b.x + b.w / 2)) * 180 / go_pi;
}

/// Rotation for a rotate-drag (snap = 15° steps), normalized to (-180,180].
/// Port of RotateFrom.
pub fn rotateFrom(orig_rot: f64, down_angle: f64, now_angle: f64, snap: bool) f64 {
    var r = orig_rot + now_angle - down_angle;
    if (snap) r = @round(r / 15) * 15;
    while (r > 180) r -= 360;
    while (r <= -180) r += 360;
    return r;
}

// ── tests ────────────────────────────────────────────────────────────────────

const testing = std.testing;

fn approx(a: f64, b: f64) !void {
    try testing.expect(@abs(a - b) < 1e-9);
}

test "goSin/goCos vs builtins" {
    var deg: f64 = -720;
    while (deg <= 720) : (deg += 7.3) {
        const rad = deg * go_pi / 180;
        try testing.expect(@abs(goSin(rad) - @sin(rad)) < 1e-12);
        try testing.expect(@abs(goCos(rad) - @cos(rad)) < 1e-12);
    }
}

test "hitTest topmost + rotated" {
    const boxes = [_]Box{
        .{ .x = 0, .y = 0, .w = 100, .h = 100, .sx = 1, .sy = 1, .rot = 0 },
        .{ .x = 50, .y = 50, .w = 100, .h = 100, .sx = 1, .sy = 1, .rot = 0 },
    };
    try testing.expectEqual(@as(i32, 1), hitTest(&boxes, 75, 75)); // overlap → topmost
    try testing.expectEqual(@as(i32, 0), hitTest(&boxes, 10, 10));
    try testing.expectEqual(@as(i32, -1), hitTest(&boxes, 300, 300));
    // 45°-rotated box: former corner now outside
    const rot = [_]Box{.{ .x = 0, .y = 0, .w = 100, .h = 100, .sx = 1, .sy = 1, .rot = 45 }};
    try testing.expectEqual(@as(i32, -1), hitTest(&rot, 2, 2));
    try testing.expectEqual(@as(i32, 0), hitTest(&rot, 50, 2));
}

test "handleAt corners + rotate anchor" {
    const b = Box{ .x = 0, .y = 0, .w = 100, .h = 100, .sx = 1, .sy = 1, .rot = 0 };
    try testing.expectEqual(@as(i32, 1), handleAt(b, 1, 1, 8, 20)); // NW
    try testing.expectEqual(@as(i32, 5), handleAt(b, 99, 99, 8, 20)); // SE
    try testing.expectEqual(@as(i32, 9), handleAt(b, 50, -20, 8, 20)); // rotate
    try testing.expectEqual(@as(i32, 0), handleAt(b, 50, 50, 8, 20)); // body ≠ handle
}

test "snapMove canvas edge guide" {
    const boxes = [_]Box{.{ .x = 4, .y = 40, .w = 100, .h = 100, .sx = 1, .sy = 1, .rot = 0 }};
    var dx: f64 = -2; // proposed → left edge at 2, within thresh 5 of canvas 0
    var dy: f64 = 0;
    var guides: [4]f64 = undefined;
    const n = snapMove(&boxes, 0, &dx, &dy, 5, 1000, 1000, &guides);
    try testing.expectEqual(@as(u32, 1), n);
    try approx(dx, -4); // snapped onto x=0
    try approx(guides[0], 1);
    try approx(guides[1], 0);
}

test "resizeBox SE keeps NW anchor" {
    const b = Box{ .x = 10, .y = 20, .w = 100, .h = 50, .sx = 1, .sy = 1, .rot = 0 };
    var out: [4]f64 = undefined;
    resizeBox(b, 5, 150, 100, false, &out); // drag SE to (150,100)
    try approx(out[0], 140); // nw
    try approx(out[1], 80); // nh
    try approx(out[2], 10); // NW corner unchanged
    try approx(out[3], 20);
}

test "rotateFrom snap + normalize" {
    try approx(rotateFrom(0, 10, 27, true), 15);
    try approx(rotateFrom(170, 0, 20, false), -170); // wraps past 180
    try approx(rotateFrom(0, 0, 0, false), 0);
}
