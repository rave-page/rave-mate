//! Shared WAV/AIFF decoder handle — container state machine + frame math + PCM
//! decode (via convert.zig comptime kernels). Go owns file I/O: feed() requests
//! absolute byte windows (need_off/need_len); Zig owns all parsing. Parse +
//! seek + error semantics replicate internal/audio wav.go/aiff.go exactly
//! (incl. quirks: wav fmt chunk not pad-aligned, aiff sowt 8-bit decoded
//! unsigned). No internal buffering — parses fed windows in place (the Go
//! decoders' body allocs + reuse buffer are GC workarounds, not the feature).
//! Parity gate: internal/audio/dec_zig_test.go.

const std = @import("std");
const convert = @import("convert.zig");
const wavdec = @import("wavdec.zig");
const aiffdec = @import("aiffdec.zig");

pub const Kind = enum { wav, aiff };

pub const Status = enum(i32) { ok = 0, need = 1, err = -1 };

pub const BodyKind = enum { wav_fmt, aiff_comm, aiff_ssnd };

/// C-ABI metadata mirror (RzPcmInfo). Valid only after feed() returned .ok.
pub const CInfo = extern struct {
    sample_rate: i64,
    total_frames: i64,
    data_start: u64,
    channels: i32,
    bits: i32,
    block_align: i32,
    flags: u32, // 1 = float samples, 2 = big-endian samples
};

pub const Dec = struct {
    kind: Kind,
    state: enum { init, magic, chunk_hdr, body, done, failed } = .init,
    body_kind: BodyKind = .wav_fmt,
    // requested window (valid while feed() returns .need)
    need_off: u64 = 0,
    need_len: u64 = 0,
    // parse context
    chunk_off: u64 = 0, // file offset of the current chunk header
    chunk_sz: u64 = 0,
    aifc: bool = false,
    have_fmt: bool = false, // wav "fmt " / aiff COMM
    have_data: bool = false, // wav "data" / aiff SSND
    // format (Go-int-width fields so quirky values — negative aiff channels — survive)
    rate: i64 = 0,
    ch: i64 = 0,
    bits: i64 = 0,
    block_align: i64 = 0,
    is_float: bool = false,
    little: bool = false, // aiff sowt
    data_start: u64 = 0, // first sample byte
    total: i64 = 0, // frames
    cur: i64 = 0, // current frame

    pub fn init(kind: Kind) Dec {
        return .{ .kind = kind };
    }

    /// Drive header parse. First call with empty buf; while .need, feed exactly
    /// the bytes at [need_off, need_off+need_len) (short = truncated file).
    pub fn feed(self: *Dec, buf: []const u8) Status {
        switch (self.state) {
            .done => return .ok,
            .failed => return .err,
            .init => {
                self.state = .magic;
                return self.req(0, 12);
            },
            .magic => {
                if (buf.len < 12) return self.fail();
                if (self.kind == .wav) {
                    if (!std.mem.eql(u8, buf[0..4], "RIFF") or !std.mem.eql(u8, buf[8..12], "WAVE")) return self.fail();
                } else {
                    if (!std.mem.eql(u8, buf[0..4], "FORM")) return self.fail();
                    if (std.mem.eql(u8, buf[8..12], "AIFC")) {
                        self.aifc = true;
                    } else if (!std.mem.eql(u8, buf[8..12], "AIFF")) return self.fail();
                }
                return self.reqChunk(12);
            },
            .chunk_hdr => {
                if (buf.len < 8) return self.fail(); // truncated: Go breaks → missing-chunk error
                const sz: u64 = std.mem.readInt(u32, buf[4..8], if (self.kind == .wav) .little else .big);
                return switch (self.kind) {
                    .wav => wavdec.onChunk(self, buf[0..4], sz),
                    .aiff => aiffdec.onChunk(self, buf[0..4], sz),
                };
            },
            .body => return switch (self.kind) {
                .wav => wavdec.onBody(self, buf),
                .aiff => aiffdec.onBody(self, buf),
            },
        }
    }

    fn req(self: *Dec, off: u64, len: u64) Status {
        self.need_off = off;
        self.need_len = len;
        return .need;
    }

    pub fn reqChunk(self: *Dec, off: u64) Status {
        self.chunk_off = off;
        self.state = .chunk_hdr;
        return self.req(off, 8);
    }

    pub fn reqBody(self: *Dec, kind: BodyKind, off: u64, len: u64, sz: u64) Status {
        self.body_kind = kind;
        self.chunk_sz = sz;
        self.state = .body;
        return self.req(off, len);
    }

    pub fn fail(self: *Dec) Status {
        self.state = .failed;
        return .err;
    }

    /// Post-parse validation — same checks as the Go constructors' tail.
    pub fn finalize(self: *Dec) Status {
        const ok = switch (self.kind) {
            .wav => self.ch > 0 and self.rate > 0,
            .aiff => self.ch > 0 and self.rate > 0 and self.block_align > 0,
        };
        if (!ok) return self.fail();
        self.state = .done;
        return .ok;
    }

    pub fn bigEndian(self: *const Dec) bool {
        return self.kind == .aiff and !self.little;
    }

    pub fn info(self: *const Dec, out: *CInfo) void {
        if (self.state != .done) {
            out.* = std.mem.zeroes(CInfo);
            return;
        }
        out.* = .{
            .sample_rate = self.rate,
            .total_frames = self.total,
            .data_start = self.data_start,
            .channels = @intCast(self.ch),
            .bits = @intCast(self.bits),
            .block_align = @intCast(self.block_align),
            .flags = (@as(u32, if (self.is_float) 1 else 0)) | (@as(u32, if (self.bigEndian()) 2 else 0)),
        };
    }

    /// Clamp frame to [0,total], return absolute byte offset (does NOT move the
    /// cursor — commit via setPos after the caller's file seek succeeds).
    pub fn seekOff(self: *const Dec, frame: i64, clamped: *i64) u64 {
        var f = frame;
        if (f < 0) f = 0;
        if (f > self.total) f = self.total;
        clamped.* = f;
        if (self.state != .done or self.block_align <= 0) return self.data_start;
        return self.data_start + @as(u64, @intCast(f)) * @as(u64, @intCast(self.block_align));
    }

    /// Frames the caller should read next (0 = EOF); need_bytes = byte count.
    pub fn plan(self: *const Dec, dst_cap_samples: usize, need_bytes: *u64) i64 {
        need_bytes.* = 0;
        if (self.state != .done) return 0;
        if (self.cur >= self.total) return 0;
        var want: i64 = @intCast(dst_cap_samples / @as(usize, @intCast(self.ch)));
        const remain = self.total - self.cur;
        if (want > remain) want = remain;
        if (want <= 0) return 0;
        need_bytes.* = @intCast(want * self.block_align);
        return want;
    }

    /// Decode src.len/block_align frames into dst, advance cursor. Caller
    /// guarantees dst capacity via plan (src.len <= planned bytes).
    pub fn decode(self: *Dec, src: []const u8, dst: [*]f32) i64 {
        if (self.state != .done) return 0;
        const ba: usize = @intCast(self.block_align);
        const frames = src.len / ba;
        if (frames == 0) return 0;
        const ch: u32 = @intCast(self.ch);
        convert.pcmToF32(src[0 .. frames * ba], frames, ch, @intCast(self.block_align), @intCast(self.bits), self.is_float, self.bigEndian(), dst[0 .. frames * ch]);
        self.cur += @intCast(frames);
        return @intCast(frames);
    }
};

// ── tests ────────────────────────────────────────────────────────────────────

const testing = std.testing;

fn openSlice(d: *Dec, data: []const u8) Status {
    var st = d.feed(&.{});
    var guard: u32 = 0;
    while (st == .need) {
        guard += 1;
        if (guard > 10_000) return .err; // parser must always make progress
        const off: usize = @intCast(@min(d.need_off, data.len));
        const end: usize = @intCast(@min(d.need_off + d.need_len, data.len));
        st = d.feed(if (end > off) data[off..end] else &.{});
    }
    return st;
}

fn appendInt(comptime T: type, list: *std.ArrayList(u8), a: std.mem.Allocator, v: T, endian: std.builtin.Endian) !void {
    var b: [@sizeOf(T)]u8 = undefined;
    std.mem.writeInt(T, &b, v, endian);
    try list.appendSlice(a, &b);
}

// 2ch s16le 48k WAV; sample = 3*i-50 per slot.
fn buildWav16(a: std.mem.Allocator, frames: u32) ![]u8 {
    var b: std.ArrayList(u8) = .empty;
    errdefer b.deinit(a);
    const data_size = frames * 4;
    try b.appendSlice(a, "RIFF");
    try appendInt(u32, &b, a, 36 + data_size, .little);
    try b.appendSlice(a, "WAVEfmt ");
    try appendInt(u32, &b, a, 16, .little);
    try appendInt(u16, &b, a, 1, .little); // PCM
    try appendInt(u16, &b, a, 2, .little);
    try appendInt(u32, &b, a, 48000, .little);
    try appendInt(u32, &b, a, 48000 * 4, .little);
    try appendInt(u16, &b, a, 4, .little);
    try appendInt(u16, &b, a, 16, .little);
    try b.appendSlice(a, "data");
    try appendInt(u32, &b, a, data_size, .little);
    var i: i32 = 0;
    while (i < frames * 2) : (i += 1) {
        try appendInt(i16, &b, a, @intCast(@rem(3 * i - 50, 30000)), .little);
    }
    return b.toOwnedSlice(a);
}

test "wav16 parse + decode + seek math" {
    const a = testing.allocator;
    const raw = try buildWav16(a, 64);
    defer a.free(raw);
    var d = Dec.init(.wav);
    try testing.expectEqual(Status.ok, openSlice(&d, raw));
    try testing.expectEqual(@as(i64, 48000), d.rate);
    try testing.expectEqual(@as(i64, 2), d.ch);
    try testing.expectEqual(@as(i64, 64), d.total);
    try testing.expectEqual(@as(u64, 44), d.data_start);

    var need: u64 = 0;
    const want = d.plan(1000, &need);
    try testing.expectEqual(@as(i64, 64), want);
    try testing.expectEqual(@as(u64, 256), need);
    var out: [128]f32 = undefined;
    const n = d.decode(raw[44 .. 44 + 256], &out);
    try testing.expectEqual(@as(i64, 64), n);
    try testing.expectEqual(@as(f32, -50.0 / 32768.0), out[0]);
    try testing.expectEqual(@as(f32, -47.0 / 32768.0), out[1]);
    try testing.expectEqual(@as(i64, 0), d.plan(1000, &need)); // EOF

    var cl: i64 = 0;
    try testing.expectEqual(@as(u64, 44 + 10 * 4), d.seekOff(10, &cl));
    try testing.expectEqual(@as(i64, 10), cl);
    _ = d.seekOff(-3, &cl);
    try testing.expectEqual(@as(i64, 0), cl);
    _ = d.seekOff(999, &cl);
    try testing.expectEqual(@as(i64, 64), cl);
    d.cur = 10;
    try testing.expectEqual(@as(i64, 54), d.plan(1 << 20, &need));
}

test "wav extensible + junk + odd fmt quirk" {
    const a = testing.allocator;
    var b: std.ArrayList(u8) = .empty;
    defer b.deinit(a);
    try b.appendSlice(a, "RIFF");
    try appendInt(u32, &b, a, 0, .little); // Go ignores RIFF size
    try b.appendSlice(a, "WAVE");
    // odd-sized junk chunk → pad byte
    try b.appendSlice(a, "JUNK");
    try appendInt(u32, &b, a, 3, .little);
    try b.appendSlice(a, &.{ 1, 2, 3, 0 });
    // extensible fmt with ODD size 41 (40 + 1 stray) — Go does NOT pad fmt.
    try b.appendSlice(a, "fmt ");
    try appendInt(u32, &b, a, 41, .little);
    try appendInt(u16, &b, a, 0xFFFE, .little);
    try appendInt(u16, &b, a, 2, .little);
    try appendInt(u32, &b, a, 44100, .little);
    try appendInt(u32, &b, a, 44100 * 6, .little);
    try appendInt(u16, &b, a, 6, .little); // block align
    try appendInt(u16, &b, a, 24, .little);
    try appendInt(u16, &b, a, 22, .little); // cbSize
    try appendInt(u16, &b, a, 24, .little); // valid bits
    try appendInt(u32, &b, a, 3, .little); // channel mask
    try appendInt(u16, &b, a, 1, .little); // SubFormat GUID leads with real tag (PCM)
    try b.appendSlice(a, &.{ 0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xAA, 0, 0x38, 0x9B, 0x71 });
    try b.appendSlice(a, &.{0xEE}); // stray odd byte, NOT padded
    try b.appendSlice(a, "data");
    try appendInt(u32, &b, a, 12, .little); // 2 frames of 6
    try b.appendSlice(a, &(.{0} ** 12));
    var d = Dec.init(.wav);
    try testing.expectEqual(Status.ok, openSlice(&d, b.items));
    try testing.expectEqual(@as(i64, 44100), d.rate);
    try testing.expectEqual(@as(i64, 24), d.bits);
    try testing.expectEqual(@as(i64, 6), d.block_align);
    try testing.expectEqual(@as(i64, 2), d.total);
    try testing.expect(!d.is_float);
}

test "wav malformed" {
    const a = testing.allocator;
    const raw = try buildWav16(a, 8);
    defer a.free(raw);
    // truncations across every parse state
    for ([_]usize{ 0, 3, 11, 12, 19, 20, 27, 43 }) |n| {
        var d = Dec.init(.wav);
        try testing.expectEqual(Status.err, openSlice(&d, raw[0..n]));
    }
    // data before fmt
    var d1 = Dec.init(.wav);
    try testing.expectEqual(Status.err, openSlice(&d1, "RIFF\x00\x00\x00\x00WAVEdata\x04\x00\x00\x00abcd"));
    // unsupported tag (ALAW)
    var bad = try a.dupe(u8, raw);
    defer a.free(bad);
    bad[20] = 6;
    var d2 = Dec.init(.wav);
    try testing.expectEqual(Status.err, openSlice(&d2, bad));
    // unsupported PCM depth
    bad[20] = 1;
    bad[34] = 12;
    var d3 = Dec.init(.wav);
    try testing.expectEqual(Status.err, openSlice(&d3, bad));
}

// mono s16be 44.1k AIFF/AIFC.
fn buildAiff16(a: std.mem.Allocator, frames: u32, comp: ?*const [4]u8) ![]u8 {
    var b: std.ArrayList(u8) = .empty;
    errdefer b.deinit(a);
    const comm_sz: u32 = if (comp != null) 22 else 18;
    const data_size = frames * 2;
    try b.appendSlice(a, "FORM");
    try appendInt(u32, &b, a, 4 + 8 + comm_sz + 8 + 8 + data_size, .big);
    try b.appendSlice(a, if (comp != null) "AIFC" else "AIFF");
    try b.appendSlice(a, "COMM");
    try appendInt(u32, &b, a, comm_sz, .big);
    try appendInt(u16, &b, a, 1, .big); // channels
    try appendInt(u32, &b, a, frames, .big);
    try appendInt(u16, &b, a, 16, .big); // bits
    // 80-bit extended 44100: exp 0x400E, mant 0xAC44 << 48
    try b.appendSlice(a, &.{ 0x40, 0x0E, 0xAC, 0x44, 0, 0, 0, 0, 0, 0 });
    if (comp) |c| try b.appendSlice(a, c);
    try b.appendSlice(a, "SSND");
    try appendInt(u32, &b, a, 8 + data_size, .big);
    try appendInt(u32, &b, a, 0, .big); // offset
    try appendInt(u32, &b, a, 0, .big); // blockSize
    var i: i32 = 0;
    while (i < frames) : (i += 1) {
        try appendInt(i16, &b, a, @intCast(@rem(7 * i - 100, 30000)), if (comp != null and std.mem.eql(u8, comp.?, "sowt")) .little else .big);
    }
    return b.toOwnedSlice(a);
}

test "aiff16 parse + extended80 rate + decode" {
    const a = testing.allocator;
    const raw = try buildAiff16(a, 32, null);
    defer a.free(raw);
    var d = Dec.init(.aiff);
    try testing.expectEqual(Status.ok, openSlice(&d, raw));
    try testing.expectEqual(@as(i64, 44100), d.rate);
    try testing.expectEqual(@as(i64, 1), d.ch);
    try testing.expectEqual(@as(i64, 32), d.total);
    try testing.expectEqual(@as(i64, 2), d.block_align);
    try testing.expect(d.bigEndian());
    const ds: usize = @intCast(d.data_start);
    var out: [32]f32 = undefined;
    try testing.expectEqual(@as(i64, 32), d.decode(raw[ds .. ds + 64], &out));
    try testing.expectEqual(@as(f32, -100.0 / 32768.0), out[0]);
    try testing.expectEqual(@as(f32, -93.0 / 32768.0), out[1]);
}

test "aifc sowt + fl32 override + unsupported comp" {
    const a = testing.allocator;
    const sowt = try buildAiff16(a, 8, "sowt");
    defer a.free(sowt);
    var d = Dec.init(.aiff);
    try testing.expectEqual(Status.ok, openSlice(&d, sowt));
    try testing.expect(d.little);
    try testing.expect(!d.bigEndian());
    const ds: usize = @intCast(d.data_start);
    var out: [8]f32 = undefined;
    try testing.expectEqual(@as(i64, 8), d.decode(sowt[ds .. ds + 16], &out));
    try testing.expectEqual(@as(f32, -100.0 / 32768.0), out[0]);

    const fl = try buildAiff16(a, 8, "fl32");
    defer a.free(fl);
    var d2 = Dec.init(.aiff);
    try testing.expectEqual(Status.ok, openSlice(&d2, fl));
    try testing.expect(d2.is_float);
    try testing.expectEqual(@as(i64, 32), d2.bits); // fl32 forces depth
    try testing.expectEqual(@as(i64, 4), d2.block_align);

    const bad = try buildAiff16(a, 8, "ima4");
    defer a.free(bad);
    var d3 = Dec.init(.aiff);
    try testing.expectEqual(Status.err, openSlice(&d3, bad));
}

test "aiff malformed + fuzz no-crash" {
    const a = testing.allocator;
    const raw = try buildAiff16(a, 16, null);
    defer a.free(raw);
    for ([_]usize{ 0, 5, 11, 12, 19, 20, 30, 45 }) |n| {
        var d = Dec.init(.aiff);
        try testing.expectEqual(Status.err, openSlice(&d, raw[0..n]));
    }
    var prng = std.Random.DefaultPrng.init(42);
    const rng = prng.random();
    var buf = try a.dupe(u8, raw);
    defer a.free(buf);
    for (0..300) |_| {
        @memcpy(buf, raw);
        for (0..1 + rng.uintLessThan(usize, 8)) |_| {
            buf[rng.uintLessThan(usize, buf.len)] = rng.int(u8);
        }
        var d = Dec.init(.aiff);
        const st = openSlice(&d, buf);
        try testing.expect(st == .ok or st == .err);
    }
    // wav fuzz too
    const wraw = try buildWav16(a, 16);
    defer a.free(wraw);
    var wbuf = try a.dupe(u8, wraw);
    defer a.free(wbuf);
    for (0..300) |_| {
        @memcpy(wbuf, wraw);
        for (0..1 + rng.uintLessThan(usize, 8)) |_| {
            wbuf[rng.uintLessThan(usize, wbuf.len)] = rng.int(u8);
        }
        var d = Dec.init(.wav);
        const st = openSlice(&d, wbuf);
        try testing.expect(st == .ok or st == .err);
    }
}

test {
    _ = wavdec;
    _ = aiffdec;
}
