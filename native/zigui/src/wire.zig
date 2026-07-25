//! RZW1 state-wire decoder — the binary replacement for the per-render state→JSON→parse round
//! trip (phase B). Encoder: internal/zigui/wire.go (that file documents the layout); generated
//! per-message decoders: wire_gen.zig.
//!
//! Contract: EVERY offset and length is checked against its enclosing slice before use, so a
//! corrupted/hostile document can only produce error.Malformed — never an out-of-bounds read.
//! Strings are slices INTO the caller's buffer (zero copy, valid for the render's lifetime);
//! lists are allocated from one parse arena freed by Parsed.deinit().
//!
//! Bounds discipline that makes the fuzz gate hold:
//!   - a struct/list payload length is checked against the REMAINING bytes of its parent body,
//!     and the child reader sees exactly that slice → a nested body can never out-read its parent
//!   - a list count is checked against its payload length (every element body costs >= 1 byte,
//!     its terminator) → the count cannot drive an allocation larger than the input
//!   - a body must end exactly on its terminator (no trailing garbage, no truncation)

const std = @import("std");

pub const Error = error{ Malformed, OutOfMemory };

pub const magic = "RZW1";
pub const header_len = 14;

pub const wt_varint: u3 = 0;
pub const wt_string: u3 = 1;
pub const wt_struct: u3 = 2;
pub const wt_list: u3 = 3;

pub const Tag = struct {
    field: u32,
    wt: u3,
};

/// Reader walks ONE struct body. buf is exactly that body (terminator included).
pub const Reader = struct {
    buf: []const u8,
    pos: usize = 0,
    arena: []const u8, // the document's strings arena
    a: std.mem.Allocator, // parse arena (lists only)

    fn byte(r: *Reader) Error!u8 {
        if (r.pos >= r.buf.len) return error.Malformed;
        const b = r.buf[r.pos];
        r.pos += 1;
        return b;
    }

    fn uvarint(r: *Reader) Error!u64 {
        var v: u64 = 0;
        var shift: u6 = 0;
        while (true) {
            const b = try r.byte();
            v |= @as(u64, b & 0x7f) << shift;
            if (b & 0x80 == 0) break;
            if (shift > 56) return error.Malformed; // 10th byte: overflows u64
            shift += 7;
        }
        return v;
    }

    fn u32le(r: *Reader) Error!u32 {
        if (r.buf.len - r.pos < 4) return error.Malformed;
        const v = std.mem.readInt(u32, r.buf[r.pos..][0..4], .little);
        r.pos += 4;
        return v;
    }

    /// payload slices out a u32-length-prefixed region, bounds-checked against this body.
    fn payload(r: *Reader) Error![]const u8 {
        const n = try r.u32le();
        if (n > r.buf.len - r.pos) return error.Malformed;
        const s = r.buf[r.pos..][0..n];
        r.pos += n;
        return s;
    }

    /// next returns the next field tag, or null at the body terminator.
    pub fn next(r: *Reader) Error!?Tag {
        if (r.pos < r.buf.len and r.buf[r.pos] == 0) {
            r.pos += 1;
            return null;
        }
        const v = try r.uvarint();
        const f = v >> 3;
        if (f == 0 or f > std.math.maxInt(u32)) return error.Malformed;
        return .{ .field = @intCast(f), .wt = @intCast(v & 7) };
    }

    pub fn str(r: *Reader, t: Tag) Error![]const u8 {
        if (t.wt != wt_string) return error.Malformed;
        const off = try r.uvarint();
        const len = try r.uvarint();
        if (off > r.arena.len or len > r.arena.len - off) return error.Malformed;
        return r.arena[@intCast(off)..][0..@intCast(len)];
    }

    pub fn uint(r: *Reader, t: Tag) Error!u64 {
        if (t.wt != wt_varint) return error.Malformed;
        return r.uvarint();
    }

    pub fn boolean(r: *Reader, t: Tag) Error!bool {
        return (try r.uint(t)) != 0;
    }

    /// sub decodes a nested message from its own bounded body.
    pub fn sub(r: *Reader, comptime T: type, comptime decodeFn: fn (*Reader, *T) Error!void, t: Tag) Error!T {
        if (t.wt != wt_struct) return error.Malformed;
        const s = try r.payload();
        var cr = Reader{ .buf = s, .arena = r.arena, .a = r.a };
        var out: T = .{};
        try decodeFn(&cr, &out);
        if (cr.pos != s.len) return error.Malformed;
        return out;
    }

    /// list decodes count element bodies from one bounded payload into arena-owned memory.
    pub fn list(r: *Reader, comptime T: type, comptime decodeFn: fn (*Reader, *T) Error!void, t: Tag) Error![]const T {
        if (t.wt != wt_list) return error.Malformed;
        const n64 = try r.uvarint();
        const s = try r.payload();
        if (n64 > s.len) return error.Malformed; // every element body is >= 1 byte (its terminator)
        const n: usize = @intCast(n64);
        if (n == 0) return &.{};
        const out = try r.a.alloc(T, n);
        var cr = Reader{ .buf = s, .arena = r.arena, .a = r.a };
        for (out) |*e| {
            e.* = .{};
            try decodeFn(&cr, e);
        }
        if (cr.pos != s.len) return error.Malformed;
        return out;
    }

    /// strList decodes a `[]const []const u8` field: a list whose element bodies each carry the
    /// string as field 1 (encoder: WireWriter.StrList). Same bounds discipline as list() - the
    /// count is checked against the payload and the payload against the parent body - so the
    /// wiretype set stays closed and skip() keeps working unchanged.
    pub fn strList(r: *Reader, t: Tag) Error![]const []const u8 {
        if (t.wt != wt_list) return error.Malformed;
        const n64 = try r.uvarint();
        const s = try r.payload();
        if (n64 > s.len) return error.Malformed; // every element body is >= 1 byte (its terminator)
        const n: usize = @intCast(n64);
        if (n == 0) return &.{};
        const out = try r.a.alloc([]const u8, n);
        var cr = Reader{ .buf = s, .arena = r.arena, .a = r.a };
        for (out) |*e| {
            e.* = "";
            while (try cr.next()) |et| {
                if (et.field == 1) {
                    e.* = try cr.str(et);
                } else {
                    try cr.skip(et);
                }
            }
        }
        if (cr.pos != s.len) return error.Malformed;
        return out;
    }

    /// skip discards an unknown field (every payload is self-delimiting). An unknown WIRETYPE
    /// is not skippable and is rejected - additive schema changes only.
    pub fn skip(r: *Reader, t: Tag) Error!void {
        switch (t.wt) {
            wt_varint => _ = try r.uvarint(),
            wt_string => {
                _ = try r.uvarint();
                _ = try r.uvarint();
            },
            wt_struct => _ = try r.payload(),
            wt_list => {
                _ = try r.uvarint();
                _ = try r.payload();
            },
            else => return error.Malformed,
        }
    }
};

/// Parsed owns the decode arena; value's strings point into the CALLER's buffer.
pub fn Parsed(comptime T: type) type {
    return struct {
        value: T,
        arena: *std.heap.ArenaAllocator,
        gpa: std.mem.Allocator,

        pub fn deinit(p: @This()) void {
            p.arena.deinit();
            p.gpa.destroy(p.arena);
        }
    };
}

/// parse validates the header (magic, message id, schema hash, arena bounds) and decodes the
/// root body. Any inconsistency → error.Malformed; the Go caller then uses the v1 JSON path.
pub fn parse(
    comptime T: type,
    comptime decodeFn: fn (*Reader, *T) Error!void,
    gpa: std.mem.Allocator,
    msg_id: u16,
    schema_hash: u32,
    buf: []const u8,
) Error!Parsed(T) {
    if (buf.len < header_len) return error.Malformed;
    if (!std.mem.eql(u8, buf[0..4], magic)) return error.Malformed;
    if (std.mem.readInt(u16, buf[4..][0..2], .little) != msg_id) return error.Malformed;
    if (std.mem.readInt(u32, buf[6..][0..4], .little) != schema_hash) return error.Malformed;
    const alen = std.mem.readInt(u32, buf[10..][0..4], .little);
    if (alen > buf.len - header_len) return error.Malformed;
    const arena_bytes = buf[header_len..][0..alen];
    const body = buf[header_len + alen ..];

    const ar = try gpa.create(std.heap.ArenaAllocator);
    ar.* = std.heap.ArenaAllocator.init(gpa);
    errdefer {
        ar.deinit();
        gpa.destroy(ar);
    }
    var r = Reader{ .buf = body, .arena = arena_bytes, .a = ar.allocator() };
    var out: T = .{};
    try decodeFn(&r, &out);
    if (r.pos != body.len) return error.Malformed;
    return .{ .value = out, .arena = ar, .gpa = gpa };
}

// ── tests: hand-built documents (the Go encoder is pinned by the three-way golden gate) ──

const TestKid = struct { a: []const u8 = "", n: bool = false };
const TestRoot = struct {
    s: []const u8 = "",
    flag: bool = false,
    kid: TestKid = .{},
    kids: []const TestKid = &.{},
    opt: ?TestKid = null, // presence matters: null renders differently from a zero-value kid
    tags: []const []const u8 = &.{},
    dflt: []const u8 = "1", // non-zero default: only StrAlways keeps v1 and v2 identical
};

fn decodeKid(r: *Reader, out: *TestKid) Error!void {
    while (try r.next()) |t| switch (t.field) {
        1 => out.a = try r.str(t),
        2 => out.n = try r.boolean(t),
        else => try r.skip(t),
    };
}

fn decodeRoot(r: *Reader, out: *TestRoot) Error!void {
    while (try r.next()) |t| switch (t.field) {
        1 => out.s = try r.str(t),
        2 => out.flag = try r.boolean(t),
        3 => out.kid = try r.sub(TestKid, decodeKid, t),
        4 => out.kids = try r.list(TestKid, decodeKid, t),
        5 => out.opt = try r.sub(TestKid, decodeKid, t),
        6 => out.tags = try r.strList(t),
        7 => out.dflt = try r.str(t),
        else => try r.skip(t),
    };
}

const hash: u32 = 0xDEADBEEF;

/// Doc builder mirroring internal/zigui/wire.go (tests only).
const Doc = struct {
    arena: std.ArrayList(u8) = .empty,
    body: std.ArrayList(u8) = .empty,
    a: std.mem.Allocator,

    fn init(a: std.mem.Allocator) Doc {
        return .{ .a = a };
    }
    fn deinit(d: *Doc) void {
        d.arena.deinit(d.a);
        d.body.deinit(d.a);
    }
    fn uv(d: *Doc, v0: u64) !void {
        var v = v0;
        while (v >= 0x80) : (v >>= 7) try d.body.append(d.a, @as(u8, @truncate(v)) | 0x80);
        try d.body.append(d.a, @truncate(v));
    }
    fn tag(d: *Doc, num: u32, wt: u3) !void {
        try d.uv(@as(u64, num) << 3 | wt);
    }
    fn str(d: *Doc, num: u32, s: []const u8) !void {
        const off = d.arena.items.len;
        try d.arena.appendSlice(d.a, s);
        try d.tag(num, wt_string);
        try d.uv(off);
        try d.uv(s.len);
    }
    fn boolean(d: *Doc, num: u32) !void {
        try d.tag(num, wt_varint);
        try d.uv(1);
    }
    fn u32at(d: *Doc, at: usize, v: u32) void {
        std.mem.writeInt(u32, d.body.items[at..][0..4], v, .little);
    }
    fn open(d: *Doc, num: u32, wt: u3) !usize {
        try d.tag(num, wt);
        const lp = d.body.items.len;
        try d.body.appendSlice(d.a, &.{ 0, 0, 0, 0 });
        return lp;
    }
    fn close(d: *Doc, lp: usize) !void {
        d.u32at(lp, @intCast(d.body.items.len - lp - 4));
    }
    fn finish(d: *Doc, msg_id: u16, h: u32) ![]u8 {
        var out: std.ArrayList(u8) = .empty;
        try out.appendSlice(d.a, magic);
        var hdr: [10]u8 = undefined;
        std.mem.writeInt(u16, hdr[0..2], msg_id, .little);
        std.mem.writeInt(u32, hdr[2..6], h, .little);
        std.mem.writeInt(u32, hdr[6..10], @intCast(d.arena.items.len), .little);
        try out.appendSlice(d.a, &hdr);
        try out.appendSlice(d.a, d.arena.items);
        try out.appendSlice(d.a, d.body.items);
        try out.append(d.a, 0);
        return out.toOwnedSlice(d.a);
    }
};

fn buildFull(a: std.mem.Allocator) ![]u8 {
    var d = Doc.init(a);
    defer d.deinit();
    try d.str(1, "hello");
    try d.boolean(2);
    {
        const lp = try d.open(3, wt_struct);
        try d.str(1, "kid");
        try d.body.append(d.a, 0);
        try d.close(lp);
    }
    {
        try d.tag(4, wt_list);
        try d.uv(2);
        const lp = d.body.items.len;
        try d.body.appendSlice(d.a, &.{ 0, 0, 0, 0 });
        try d.str(1, "a");
        try d.body.append(d.a, 0);
        try d.boolean(2);
        try d.body.append(d.a, 0);
        try d.close(lp);
    }
    return d.finish(7, hash);
}

test "round trip: strings, bool, nested struct, list" {
    const a = std.testing.allocator;
    const buf = try buildFull(a);
    defer a.free(buf);
    const p = try parse(TestRoot, decodeRoot, a, 7, hash, buf);
    defer p.deinit();
    try std.testing.expectEqualStrings("hello", p.value.s);
    try std.testing.expect(p.value.flag);
    try std.testing.expectEqualStrings("kid", p.value.kid.a);
    try std.testing.expectEqual(@as(usize, 2), p.value.kids.len);
    try std.testing.expectEqualStrings("a", p.value.kids[0].a);
    try std.testing.expect(p.value.kids[1].n);
    try std.testing.expect(!p.value.kids[0].n);
}

test "absent fields decode to zero values (no null representable)" {
    const a = std.testing.allocator;
    var d = Doc.init(a);
    defer d.deinit();
    const buf = try d.finish(7, hash);
    defer a.free(buf);
    const p = try parse(TestRoot, decodeRoot, a, 7, hash, buf);
    defer p.deinit();
    try std.testing.expectEqualStrings("", p.value.s);
    try std.testing.expectEqual(@as(usize, 0), p.value.kids.len); // empty, never null
    try std.testing.expectEqualStrings("", p.value.kid.a);
}

test "optional struct: absent is null, empty body is present" {
    const a = std.testing.allocator;
    {
        var d = Doc.init(a);
        defer d.deinit();
        const buf = try d.finish(7, hash);
        defer a.free(buf);
        const p = try parse(TestRoot, decodeRoot, a, 7, hash, buf);
        defer p.deinit();
        try std.testing.expect(p.value.opt == null);
    }
    { // OptStruct emits the tag even with no fields → present, all-zero
        var d = Doc.init(a);
        defer d.deinit();
        const lp = try d.open(5, wt_struct);
        try d.body.append(d.a, 0);
        try d.close(lp);
        const buf = try d.finish(7, hash);
        defer a.free(buf);
        const p = try parse(TestRoot, decodeRoot, a, 7, hash, buf);
        defer p.deinit();
        try std.testing.expect(p.value.opt != null);
        try std.testing.expectEqualStrings("", p.value.opt.?.a);
    }
}

test "strList: strings, empty elements, absent, bounds" {
    const a = std.testing.allocator;
    { // three elements, the middle one empty (absent field 1)
        var d = Doc.init(a);
        defer d.deinit();
        try d.tag(6, wt_list);
        try d.uv(3);
        const lp = d.body.items.len;
        try d.body.appendSlice(d.a, &.{ 0, 0, 0, 0 });
        try d.str(1, "aa");
        try d.body.append(d.a, 0);
        try d.body.append(d.a, 0); // empty element
        try d.str(1, "cc");
        try d.body.append(d.a, 0);
        try d.close(lp);
        const buf = try d.finish(7, hash);
        defer a.free(buf);
        const p = try parse(TestRoot, decodeRoot, a, 7, hash, buf);
        defer p.deinit();
        try std.testing.expectEqual(@as(usize, 3), p.value.tags.len);
        try std.testing.expectEqualStrings("aa", p.value.tags[0]);
        try std.testing.expectEqualStrings("", p.value.tags[1]);
        try std.testing.expectEqualStrings("cc", p.value.tags[2]);
    }
    { // count beyond the payload = allocation bomb, must be refused
        var d = Doc.init(a);
        defer d.deinit();
        try d.tag(6, wt_list);
        try d.uv(0xFFFFFFFF);
        const lp = d.body.items.len;
        try d.body.appendSlice(d.a, &.{ 0, 0, 0, 0 });
        try d.body.append(d.a, 0);
        try d.close(lp);
        const buf = try d.finish(7, hash);
        defer a.free(buf);
        try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 7, hash, buf));
    }
    { // string offset past the arena inside an element body
        var d = Doc.init(a);
        defer d.deinit();
        try d.tag(6, wt_list);
        try d.uv(1);
        const lp = d.body.items.len;
        try d.body.appendSlice(d.a, &.{ 0, 0, 0, 0 });
        try d.tag(1, wt_string);
        try d.uv(9999);
        try d.uv(1);
        try d.body.append(d.a, 0);
        try d.close(lp);
        const buf = try d.finish(7, hash);
        defer a.free(buf);
        try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 7, hash, buf));
    }
}

test "StrAlways: an explicit empty string beats a non-zero default" {
    const a = std.testing.allocator;
    { // absent → the Zig default survives (why StrAlways exists)
        var d = Doc.init(a);
        defer d.deinit();
        const buf = try d.finish(7, hash);
        defer a.free(buf);
        const p = try parse(TestRoot, decodeRoot, a, 7, hash, buf);
        defer p.deinit();
        try std.testing.expectEqualStrings("1", p.value.dflt);
    }
    { // present-but-empty (off 0, len 0, empty arena) → ""
        var d = Doc.init(a);
        defer d.deinit();
        try d.str(7, "");
        const buf = try d.finish(7, hash);
        defer a.free(buf);
        const p = try parse(TestRoot, decodeRoot, a, 7, hash, buf);
        defer p.deinit();
        try std.testing.expectEqualStrings("", p.value.dflt);
    }
}

test "unknown field numbers are skipped" {
    const a = std.testing.allocator;
    var d = Doc.init(a);
    defer d.deinit();
    try d.str(99, "ignored");
    try d.str(1, "kept");
    try d.tag(98, wt_varint);
    try d.uv(12345);
    {
        const lp = try d.open(97, wt_struct);
        try d.str(1, "x");
        try d.body.append(d.a, 0);
        try d.close(lp);
    }
    const buf = try d.finish(7, hash);
    defer a.free(buf);
    const p = try parse(TestRoot, decodeRoot, a, 7, hash, buf);
    defer p.deinit();
    try std.testing.expectEqualStrings("kept", p.value.s);
}

test "header rejections" {
    const a = std.testing.allocator;
    const buf = try buildFull(a);
    defer a.free(buf);
    try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 8, hash, buf)); // msg id
    try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 7, hash + 1, buf)); // schema
    try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 7, hash, buf[0..10])); // short
    const bad = try a.dupe(u8, buf);
    defer a.free(bad);
    bad[0] = 'X';
    try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 7, hash, bad));
}

test "body rejections: bounds, truncation, trailing garbage, wrong wiretype" {
    const a = std.testing.allocator;
    const buf = try buildFull(a);
    defer a.free(buf);

    // arena length beyond the buffer
    {
        const bad = try a.dupe(u8, buf);
        defer a.free(bad);
        std.mem.writeInt(u32, bad[10..][0..4], 0xFFFF, .little);
        try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 7, hash, bad));
    }
    // truncated body (terminator gone)
    try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 7, hash, buf[0 .. buf.len - 1]));
    // trailing garbage after the terminator
    {
        const bad = try a.alloc(u8, buf.len + 1);
        defer a.free(bad);
        @memcpy(bad[0..buf.len], buf);
        bad[buf.len] = 0x42;
        try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 7, hash, bad));
    }
    // string offset past the arena
    {
        var d = Doc.init(a);
        defer d.deinit();
        try d.tag(1, wt_string);
        try d.uv(9999);
        try d.uv(3);
        const bad = try d.finish(7, hash);
        defer a.free(bad);
        try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 7, hash, bad));
    }
    // string length past the arena end
    {
        var d = Doc.init(a);
        defer d.deinit();
        try d.arena.appendSlice(d.a, "ab");
        try d.tag(1, wt_string);
        try d.uv(1);
        try d.uv(9);
        const bad = try d.finish(7, hash);
        defer a.free(bad);
        try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 7, hash, bad));
    }
    // wrong wiretype for a known field
    {
        var d = Doc.init(a);
        defer d.deinit();
        try d.boolean(1); // field 1 is a string
        const bad = try d.finish(7, hash);
        defer a.free(bad);
        try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 7, hash, bad));
    }
    // field number 0 is the terminator, never a tag
    {
        var d = Doc.init(a);
        defer d.deinit();
        try d.body.append(d.a, 0x01); // field 0, wiretype 1
        const bad = try d.finish(7, hash);
        defer a.free(bad);
        try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 7, hash, bad));
    }
    // struct payload longer than the parent body
    {
        var d = Doc.init(a);
        defer d.deinit();
        const lp = try d.open(3, wt_struct);
        d.u32at(lp, 0xFFFF);
        const bad = try d.finish(7, hash);
        defer a.free(bad);
        try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 7, hash, bad));
    }
    // list count larger than its payload can hold (allocation bomb)
    {
        var d = Doc.init(a);
        defer d.deinit();
        try d.tag(4, wt_list);
        try d.uv(0xFFFFFFFF);
        const lp = d.body.items.len;
        try d.body.appendSlice(d.a, &.{ 0, 0, 0, 0 });
        try d.body.append(d.a, 0);
        try d.close(lp);
        const bad = try d.finish(7, hash);
        defer a.free(bad);
        try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 7, hash, bad));
    }
    // list count smaller than the bodies present (unconsumed payload)
    {
        var d = Doc.init(a);
        defer d.deinit();
        try d.tag(4, wt_list);
        try d.uv(1);
        const lp = d.body.items.len;
        try d.body.appendSlice(d.a, &.{ 0, 0, 0, 0 });
        try d.body.append(d.a, 0);
        try d.body.append(d.a, 0);
        try d.close(lp);
        const bad = try d.finish(7, hash);
        defer a.free(bad);
        try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 7, hash, bad));
    }
    // unterminated varint
    {
        var d = Doc.init(a);
        defer d.deinit();
        try d.tag(2, wt_varint);
        try d.body.appendSlice(d.a, &.{ 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80 });
        const bad = try d.finish(7, hash);
        defer a.free(bad);
        try std.testing.expectError(error.Malformed, parse(TestRoot, decodeRoot, a, 7, hash, bad));
    }
}
