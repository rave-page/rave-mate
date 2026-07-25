//! AIFF/AIFF-C container parsing — byte-exact port of internal/audio/aiff.go
//! (COMM: i16 channels/bits, 80-bit extended sample rate, AIFC compression
//! codes NONE/twos/sowt/fl32/FL32/fl64/FL64; SSND offset). Rate truncation
//! mirrors Go amd64 int(f64): trunc toward zero, NaN/±Inf/overflow → min i64
//! (negative → rejected by validation, same as Go).

const std = @import("std");
const pcmdec = @import("pcmdec.zig");

pub fn onChunk(d: *pcmdec.Dec, id: *const [4]u8, sz: u64) pcmdec.Status {
    const body = d.chunk_off + 8;
    if (std.mem.eql(u8, id, "COMM")) return d.reqBody(.aiff_comm, body, sz, sz);
    if (std.mem.eql(u8, id, "SSND")) return d.reqBody(.aiff_ssnd, body, 8, sz); // Go reads offset+blockSize only
    return d.reqChunk(body + sz + (sz & 1));
}

pub fn onBody(d: *pcmdec.Dec, buf: []const u8) pcmdec.Status {
    switch (d.body_kind) {
        .aiff_comm => {
            if (buf.len < d.chunk_sz) return d.fail();
            if (!parseComm(d, buf[0..@intCast(d.chunk_sz)])) return d.fail();
            d.have_fmt = true;
        },
        .aiff_ssnd => {
            if (buf.len < 8) return d.fail();
            const sound_off: u64 = std.mem.readInt(u32, buf[0..4], .big);
            d.data_start = d.chunk_off + 16 + sound_off; // hdr(8) + offset/blockSize(8)
            d.have_data = true;
        },
        .wav_fmt => return d.fail(),
    }
    if (d.have_fmt and d.have_data) return d.finalize();
    return d.reqChunk(d.chunk_off + 8 + d.chunk_sz + (d.chunk_sz & 1));
}

fn parseComm(d: *pcmdec.Dec, b: []const u8) bool {
    if (b.len < 18) return false;
    d.ch = @as(i16, @bitCast(std.mem.readInt(u16, b[0..2], .big))); // signed, like Go
    d.total = std.mem.readInt(u32, b[2..6], .big);
    d.bits = @as(i16, @bitCast(std.mem.readInt(u16, b[6..8], .big)));
    d.rate = goTruncI64(extended80ToF64(b[8..18]));
    d.is_float = false;
    if (d.aifc and b.len >= 22) {
        const c = b[18..22];
        if (std.mem.eql(u8, c, "NONE") or std.mem.eql(u8, c, "twos")) {
            d.little = false;
        } else if (std.mem.eql(u8, c, "sowt")) {
            d.little = true;
        } else if (std.mem.eql(u8, c, "fl32") or std.mem.eql(u8, c, "FL32")) {
            d.is_float = true;
            d.bits = 32; // compression code overrides COMM depth
        } else if (std.mem.eql(u8, c, "fl64") or std.mem.eql(u8, c, "FL64")) {
            d.is_float = true;
            d.bits = 64;
        } else return false; // unsupported AIFC compression — ffmpeg path
    }
    switch (d.bits) {
        8, 16, 24, 32, 64 => {},
        else => return false,
    }
    d.block_align = @divTrunc(d.ch * d.bits, 8); // trunc division, like Go int math
    return true;
}

/// 80-bit IEEE extended → f64. scalbn == Go's mant*math.Pow(2,k) for exact
/// power-of-two k (single rounding on the multiply, same over/underflow).
fn extended80ToF64(b: *const [10]u8) f64 {
    const sign: f64 = if (b[0] & 0x80 != 0) -1 else 1;
    const exp: i32 = @as(u16, std.mem.readInt(u16, b[0..2], .big) & 0x7FFF);
    const mant = std.mem.readInt(u64, b[2..10], .big);
    if (exp == 0 and mant == 0) return 0;
    return sign * std.math.scalbn(@as(f64, @floatFromInt(mant)), exp - 16383 - 63);
}

/// Mirror Go amd64 int(f64) (CVTTSD2SI): trunc; NaN/±Inf/out-of-range → min i64.
fn goTruncI64(f: f64) i64 {
    if (std.math.isNan(f)) return std.math.minInt(i64);
    if (f >= 9223372036854775808.0) return std.math.minInt(i64);
    if (f < -9223372036854775808.0) return std.math.minInt(i64);
    return @intFromFloat(f);
}

const testing = std.testing;

test "extended80 common rates" {
    // 44100 / 48000 / 96000
    try testing.expectEqual(@as(f64, 44100), extended80ToF64(&.{ 0x40, 0x0E, 0xAC, 0x44, 0, 0, 0, 0, 0, 0 }));
    try testing.expectEqual(@as(f64, 48000), extended80ToF64(&.{ 0x40, 0x0E, 0xBB, 0x80, 0, 0, 0, 0, 0, 0 }));
    try testing.expectEqual(@as(f64, 96000), extended80ToF64(&.{ 0x40, 0x0F, 0xBB, 0x80, 0, 0, 0, 0, 0, 0 }));
    try testing.expectEqual(@as(f64, 0), extended80ToF64(&(.{0} ** 10)));
    // huge exponent → inf → min-i64 → negative (Go parity)
    try testing.expect(goTruncI64(extended80ToF64(&.{ 0x7F, 0xFF, 0xFF, 0, 0, 0, 0, 0, 0, 0 })) < 0);
    try testing.expectEqual(std.math.minInt(i64), goTruncI64(std.math.nan(f64)));
}
