//! zigmedia — native realtime media transport (Zig). C ABI consumed by the encode shims/children.
//! ABI contract: include/ravemedia.h. Owner directive 2026-11-12: the realtime path is native, no Go
//! runtime in the per-frame loop.
//!
//! Increment status: the WIRE CORE (keys/aead/wire) is complete + byte-exact with the Go
//! implementation (goldens_test.zig). The SENDER runtime (rm_tx_*: TX thread, pacer, UDP/TCP socket,
//! NACK retransmit) is the next increment — the exports below are honest stubs (RM_ERR_UNIMPL) so the
//! header is stable and the Linux shim can link + wire its call sequence now.
const std = @import("std");
const keys = @import("keys.zig");
const aead = @import("aead.zig");
const wire = @import("wire.zig");

pub const abi_version: u32 = 1;

// Mirrors rm_status in ravemedia.h.
const RM_OK: c_int = 0;
const RM_ERR_ARGS: c_int = -1;
const RM_ERR_UNIMPL: c_int = -6;

export fn rm_abi_version() u32 {
    return abi_version;
}

/// rm_wire_selftest runs the wire core against its built-in golden vectors (C-callable integration
/// sanity for the shim). Returns 0 on pass, non-zero on mismatch.
export fn rm_wire_selftest() c_int {
    var master: [32]u8 = undefined;
    for (&master, 0..) |*b, i| b.* = @intCast(i + 1);
    var sb: [64]u8 = undefined;
    var n: usize = 0;
    for (0..16) |i| {
        sb[n] = @intCast(0xA0 + i);
        n += 1;
    }
    for (0..16) |i| {
        sb[n] = @intCast(0xB0 + i);
        n += 1;
    }
    @memcpy(sb[n..][0..9], "sess-1234");
    const salt = sb[0 .. n + 9];
    if (keys.token(&master, salt) != 0xf61ab158) return 1;
    const ks = keys.deriveUdp(&master, salt, true);
    if (ks.send.prefix != 0x893e3a8c or ks.recv.prefix != 0xb398b7ce) return 2;
    // Round-trip a sealed datagram.
    var fbuf: [64]u8 = undefined;
    const m = wire.FrameMeta{ .stream = 1, .kind = 1, .codec = 5, .flags = 1, .seq = 7, .pts = 42 };
    const pt = fbuf[0..wire.marshalFrame(&fbuf, m, "au")];
    var dg: [128]u8 = undefined;
    const h = wire.Header{ .typ = wire.dg_type_media, .flags = wire.flag_aead, .token = keys.token(&master, salt), .seq = 3, .stream = 1, .frame_seq = 7, .frag_idx = 0, .frag_count = 1 };
    h.encode(dg[0..wire.header_len]);
    const sealed = aead.seal(dg[wire.header_len..], pt, dg[0..wire.header_len], keys.nonce(ks.send.prefix, 3), ks.send.key);
    var ptout: [64]u8 = undefined;
    const got = aead.open(&ptout, dg[wire.header_len .. wire.header_len + sealed], dg[0..wire.header_len], keys.nonce(ks.send.prefix, 3), ks.send.key) catch return 3;
    if (!std.mem.eql(u8, pt, ptout[0..got])) return 4;
    return 0;
}

// ── sender runtime (increment 2b: TX thread + pacer + UDP/TCP socket + NACK) ──────────────────────
// Declared stable in ravemedia.h; implemented next. Stubs return RM_ERR_UNIMPL so the shim links.

const Sender = opaque {};

export fn rm_tx_open(p: ?*const anyopaque, on_need_idr: ?*const anyopaque, user: ?*anyopaque, out: ?*?*Sender) c_int {
    _ = p;
    _ = on_need_idr;
    _ = user;
    if (out) |o| o.* = null;
    return RM_ERR_UNIMPL;
}
export fn rm_tx_submit_au(s: ?*Sender, au: ?[*]const u8, len: usize, pts_ns: i64, flags: u32) c_int {
    _ = s;
    _ = au;
    _ = len;
    _ = pts_ns;
    _ = flags;
    return RM_ERR_UNIMPL;
}
export fn rm_tx_set_rate(s: ?*Sender, bytes_per_sec: f64) void {
    _ = s;
    _ = bytes_per_sec;
}
export fn rm_tx_stats_get(s: ?*Sender, out: ?*anyopaque) void {
    _ = s;
    _ = out;
}
export fn rm_tx_close(s: ?*Sender) void {
    _ = s;
}

test {
    _ = @import("goldens_test.zig");
    _ = keys;
    _ = aead;
    _ = wire;
}
