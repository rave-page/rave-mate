//! zigmedia — native realtime media transport (Zig). C ABI consumed by the encode shims/children.
//! ABI contract: include/ravemedia.h. Owner directive 2026-11-12: the realtime path is native, no Go
//! runtime in the per-frame loop.
//!
//! Increment status: the WIRE CORE (keys/aead/wire) is complete + byte-exact with the Go
//! implementation (goldens_test.zig). The SENDER runtime (rm_tx_*: TX thread, pacer, UDP/TCP socket,
//! NACK retransmit) is the next increment — the exports below are honest stubs (RM_ERR_UNIMPL) so the
//! header is stable and the Linux shim can link + wire its call sequence now.
const std = @import("std");
const builtin = @import("builtin");
const keys = @import("keys.zig");
const aead = @import("aead.zig");
const wire = @import("wire.zig");
const snd = @import("sender.zig");

pub const abi_version: u32 = 1;

const alloc = std.heap.c_allocator;

// Mirrors rm_status in ravemedia.h.
const RM_OK: c_int = 0;
const RM_ERR_ARGS: c_int = -1;
const RM_ERR_CRYPTO: c_int = -2;
const RM_ERR_SOCKET: c_int = -3;
const RM_ERR_UNIMPL: c_int = -6;

// extern layout of ravemedia.h (C ABI). Field order/types MUST match the header.
const RmKeys = extern struct {
    send_key: [32]u8,
    send_nonce_prefix: u32,
    recv_key: [32]u8,
    recv_nonce_prefix: u32,
    token: u32,
    encrypt: i32,
    initiator: i32,
};
const RmOpenParams = extern struct {
    abi_version: u32,
    transport: c_int,
    codec: c_int,
    stream: u16,
    body_max: i32,
    socket_fd: i64,
    peer_addr: ?[*]const u8,
    peer_addr_len: i32,
    keys: RmKeys,
    initial_rate_bytes_per_sec: f64,
};
const RmTxStats = extern struct {
    aus_submitted: u64,
    aus_sent: u64,
    aus_dropped: u64,
    datagrams_sent: u64,
    bytes_sent: u64,
    retransmits: u64,
    seq: u64,
    queue_depth: i32,
    dead: i32,
};

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

// ── sender runtime (increment 2b) ─────────────────────────────────────────────────────────────────
// UDP implemented (sender.zig); TCP framing is the next follow-up (returns RM_ERR_UNIMPL until wired).

const IdrCb = *const fn (?*anyopaque) callconv(.c) void;

export fn rm_tx_open(p: ?*const RmOpenParams, on_need_idr: ?IdrCb, user: ?*anyopaque, out: ?*?*snd.Sender) c_int {
    const params = p orelse return RM_ERR_ARGS;
    const o = out orelse return RM_ERR_ARGS;
    o.* = null;
    if (params.abi_version != abi_version) return RM_ERR_ARGS;
    if (params.transport != 0) return RM_ERR_UNIMPL; // TCP: next follow-up
    const k = params.keys;
    const cfg = snd.Config{
        .transport = .udp,
        .codec = @intCast(params.codec),
        .stream = params.stream,
        .body_max = if (params.body_max > 0) @intCast(params.body_max) else 1200,
        .encrypt = k.encrypt != 0,
        .token = k.token,
        .send_key = k.send_key,
        .send_prefix = k.send_nonce_prefix,
        .recv_key = k.recv_key,
        .recv_prefix = k.recv_nonce_prefix,
        .rate_bps = params.initial_rate_bytes_per_sec,
    };
    const fd_res = openSocket(params) catch return RM_ERR_SOCKET;
    const s = snd.Sender.open(alloc, cfg, fd_res.fd, fd_res.own) catch {
        if (fd_res.own) snd.closeFd(fd_res.fd);
        return RM_ERR_CRYPTO;
    };
    s.on_idr = on_need_idr;
    s.idr_user = user;
    o.* = s;
    return RM_OK;
}

const FdRes = struct { fd: i32, own: bool };

fn openSocket(p: *const RmOpenParams) !FdRes {
    if (p.socket_fd >= 0) return .{ .fd = @intCast(p.socket_fd), .own = false }; // Go handed a connected socket
    const addr_ptr = p.peer_addr orelse return error.NoAddr;
    const fd = try snd.udpSocket();
    errdefer snd.closeFd(fd);
    try snd.udpConnect(fd, @ptrCast(addr_ptr), @intCast(p.peer_addr_len));
    return .{ .fd = fd, .own = true };
}

export fn rm_tx_submit_au(s: ?*snd.Sender, au: ?[*]const u8, len: usize, pts_ns: i64, flags: u32) c_int {
    const sender = s orelse return RM_ERR_ARGS;
    const ptr = au orelse return RM_ERR_ARGS;
    sender.submit(ptr[0..len], pts_ns, flags);
    return RM_OK;
}
export fn rm_tx_set_rate(s: ?*snd.Sender, bytes_per_sec: f64) void {
    if (s) |sender| sender.setRate(bytes_per_sec);
}
export fn rm_tx_stats_get(s: ?*snd.Sender, out: ?*RmTxStats) void {
    const sender = s orelse return;
    const o = out orelse return;
    const st = sender.snapshot();
    o.* = .{
        .aus_submitted = st.aus_submitted,
        .aus_sent = st.aus_sent,
        .aus_dropped = st.aus_dropped,
        .datagrams_sent = st.datagrams_sent,
        .bytes_sent = st.bytes_sent,
        .retransmits = st.retransmits,
        .seq = st.seq,
        .queue_depth = st.queue_depth,
        .dead = st.dead,
    };
}
export fn rm_tx_close(s: ?*snd.Sender) void {
    if (s) |sender| sender.close();
}

test {
    _ = @import("goldens_test.zig");
    _ = keys;
    _ = aead;
    _ = wire;
    _ = @import("pacer.zig");
    _ = @import("auqueue.zig");
    _ = @import("retxring.zig");
    _ = @import("playout.zig");
    _ = @import("hrtimer.zig");
    _ = @import("reassembler.zig");
    _ = @import("histogram.zig");
    _ = snd;
    if (builtin.os.tag == .linux) _ = @import("interop_test.zig"); // socket loopback: Linux only
}
