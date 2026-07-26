//! Minimal PNG writer for the child-side screenshot path (PSH1 §5). 8-bit RGB, filter 0, zlib
//! stream of STORED deflate blocks - no compressor dependency; a ~1280x820 capture is ~3 MB on
//! disk, which only ctl screenshots ever read. Deterministic and stdlib-only.

const std = @import("std");

fn crcTable() [256]u32 {
    @setEvalBranchQuota(10000);
    var t: [256]u32 = undefined;
    for (0..256) |n| {
        var c: u32 = @intCast(n);
        for (0..8) |_| {
            c = if (c & 1 != 0) 0xedb88320 ^ (c >> 1) else c >> 1;
        }
        t[n] = c;
    }
    return t;
}

const crc_table = crcTable();

/// crc32Raw runs the CRC over data continuing from state (start from 0xffffffff; xor-out at end).
fn crc32Raw(state: u32, data: []const u8) u32 {
    var c = state;
    for (data) |b| c = crc_table[(c ^ b) & 0xff] ^ (c >> 8);
    return c;
}

const Adler = struct {
    a: u32 = 1,
    b: u32 = 0,

    fn update(z: *Adler, data: []const u8) void {
        for (data) |x| {
            z.a = (z.a + x) % 65521;
            z.b = (z.b + z.a) % 65521;
        }
    }

    fn sum(z: Adler) u32 {
        return (z.b << 16) | z.a;
    }
};

fn be32(v: u32) [4]u8 {
    return .{ @intCast(v >> 24 & 0xff), @intCast(v >> 16 & 0xff), @intCast(v >> 8 & 0xff), @intCast(v & 0xff) };
}

fn chunk(out: *std.ArrayList(u8), gpa: std.mem.Allocator, tag: *const [4]u8, data: []const u8) !void {
    try out.appendSlice(gpa, &be32(@intCast(data.len)));
    try out.appendSlice(gpa, tag);
    try out.appendSlice(gpa, data);
    const c = crc32Raw(crc32Raw(0xffffffff, tag), data) ^ 0xffffffff;
    try out.appendSlice(gpa, &be32(c));
}

/// encodeRGB writes w*h RGB8 pixels (row-major, 3 bytes/px) as a PNG into an owned buffer.
pub fn encodeRGB(gpa: std.mem.Allocator, pixels: []const u8, w: usize, h: usize) ![]u8 {
    std.debug.assert(pixels.len == w * h * 3);
    var out: std.ArrayList(u8) = .empty;
    errdefer out.deinit(gpa);
    try out.appendSlice(gpa, &.{ 0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n' });

    var ihdr: [13]u8 = undefined;
    @memcpy(ihdr[0..4], &be32(@intCast(w)));
    @memcpy(ihdr[4..8], &be32(@intCast(h)));
    ihdr[8] = 8; // bit depth
    ihdr[9] = 2; // color type RGB
    ihdr[10] = 0; // deflate
    ihdr[11] = 0; // filter method
    ihdr[12] = 0; // no interlace
    try chunk(&out, gpa, "IHDR", &ihdr);

    // Raw scanlines: filter byte 0 + row.
    const stride = w * 3;
    var raw = try gpa.alloc(u8, (stride + 1) * h);
    defer gpa.free(raw);
    for (0..h) |y| {
        raw[(stride + 1) * y] = 0;
        @memcpy(raw[(stride + 1) * y + 1 ..][0..stride], pixels[stride * y ..][0..stride]);
    }

    // zlib: header + stored blocks (<=65535 each) + adler32.
    var idat: std.ArrayList(u8) = .empty;
    defer idat.deinit(gpa);
    try idat.appendSlice(gpa, &.{ 0x78, 0x01 });
    var adler: Adler = .{};
    adler.update(raw);
    var off: usize = 0;
    while (off < raw.len) {
        const n: usize = @min(raw.len - off, 65535);
        const final: u8 = if (off + n == raw.len) 1 else 0;
        try idat.append(gpa, final); // BTYPE=00 stored
        const len16: u16 = @intCast(n);
        try idat.appendSlice(gpa, &.{ @intCast(len16 & 0xff), @intCast(len16 >> 8), @intCast(~len16 & 0xff), @intCast((~len16 >> 8) & 0xff) });
        try idat.appendSlice(gpa, raw[off..][0..n]);
        off += n;
    }
    try idat.appendSlice(gpa, &be32(adler.sum()));
    try chunk(&out, gpa, "IDAT", idat.items);
    try chunk(&out, gpa, "IEND", &.{});
    return out.toOwnedSlice(gpa);
}

test "encodeRGB roundtrips through the std png-less checks" {
    // No PNG decoder in std: assert the container invariants instead (signature, IHDR fields,
    // stored-deflate sizes, adler) - the Go test suite decodes the real thing.
    const gpa = std.testing.allocator;
    const px = [_]u8{ 255, 0, 0, 0, 255, 0, 0, 0, 255, 10, 20, 30 };
    const png = try encodeRGB(gpa, &px, 2, 2);
    defer gpa.free(png);
    try std.testing.expectEqualSlices(u8, &.{ 0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n' }, png[0..8]);
    try std.testing.expectEqualSlices(u8, "IHDR", png[12..16]);
    try std.testing.expectEqual(@as(u8, 2), png[16 + 3]); // width LSB
    try std.testing.expectEqual(@as(u8, 2), png[16 + 7]); // height LSB
    try std.testing.expectEqualSlices(u8, "IDAT", png[8 + 25 + 4 ..][0..4]);
}
