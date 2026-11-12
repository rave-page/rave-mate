//! Bounded retransmit ring: keeps recently-sent UDP datagrams so a receiver NACK can be answered
//! (matches Go's dgRing). Fixed capacity (frames AND bytes), drop-oldest. Allocation-free after init.
const std = @import("std");

const Entry = struct {
    buf: []u8, // backing capacity
    len: usize = 0,
    stream: u16 = 0,
    frame_seq: u32 = 0,
    frag_idx: u16 = 0,
    used: bool = false,
};

pub const RetxRing = struct {
    alloc: std.mem.Allocator,
    entries: []Entry,
    head: usize = 0, // next write slot (ring)

    pub fn init(alloc: std.mem.Allocator, n: usize, dg_cap: usize) !RetxRing {
        const e = try alloc.alloc(Entry, n);
        errdefer alloc.free(e);
        var made: usize = 0;
        errdefer for (e[0..made]) |x| alloc.free(x.buf);
        for (e) |*x| {
            x.* = .{ .buf = try alloc.alloc(u8, dg_cap) };
            made += 1;
        }
        return .{ .alloc = alloc, .entries = e };
    }

    pub fn deinit(r: *RetxRing) void {
        for (r.entries) |x| r.alloc.free(x.buf);
        r.alloc.free(r.entries);
    }

    /// add copies one sent datagram into the ring (drop-oldest). dg must fit dg_cap or it is skipped.
    pub fn add(r: *RetxRing, dg: []const u8, stream: u16, frame_seq: u32, frag_idx: u16) void {
        const e = &r.entries[r.head];
        if (dg.len > e.buf.len) return; // oversized (jumbo) — not retransmittable from this ring
        @memcpy(e.buf[0..dg.len], dg);
        e.len = dg.len;
        e.stream = stream;
        e.frame_seq = frame_seq;
        e.frag_idx = frag_idx;
        e.used = true;
        r.head = (r.head + 1) % r.entries.len;
    }

    /// get returns the stored datagram for (stream, frame_seq, frag_idx) if still in the ring.
    pub fn get(r: *RetxRing, stream: u16, frame_seq: u32, frag_idx: u16) ?[]const u8 {
        for (r.entries) |*e| {
            if (e.used and e.stream == stream and e.frame_seq == frame_seq and e.frag_idx == frag_idx) {
                return e.buf[0..e.len];
            }
        }
        return null;
    }
};

test "retx ring stores and retrieves, drops oldest" {
    var r = try RetxRing.init(std.testing.allocator, 2, 64);
    defer r.deinit();
    r.add("dgA", 1, 10, 0);
    r.add("dgB", 1, 10, 1);
    try std.testing.expectEqualSlices(u8, "dgA", r.get(1, 10, 0).?);
    try std.testing.expectEqualSlices(u8, "dgB", r.get(1, 10, 1).?);
    r.add("dgC", 1, 11, 0); // evicts the oldest (dgA)
    try std.testing.expectEqual(@as(?[]const u8, null), r.get(1, 10, 0));
    try std.testing.expectEqualSlices(u8, "dgC", r.get(1, 11, 0).?);
}
