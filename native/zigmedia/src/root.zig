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
const sock = @import("sock.zig");

pub const abi_version: u32 = 5;

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
    struct_size: u32,
    transport: c_int,
    codec: c_int,
    stream: u16,
    body_max: i32,
    socket_fd: i64,
    peer_addr: ?[*]const u8,
    peer_addr_len: i32,
    keys: RmKeys,
    initial_rate_bytes_per_sec: f64,
    initial_seq: u64,
    fps: i32,
    startup_grace_ns: i64,
    media_clock_ns: i64,
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
    nacks_recv: u64,
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
    if (params.abi_version != abi_version) return RM_ERR_ARGS; // version skew: reject before any field use
    if (params.struct_size != @sizeOf(RmOpenParams)) return RM_ERR_ARGS; // layout skew (same-version build drift)
    if (params.transport != 0 and params.transport != 1) return RM_ERR_ARGS;
    const is_tcp = params.transport == 1;
    if (is_tcp and params.socket_fd < 0) return RM_ERR_ARGS; // TCP: Go hands the accepted route connection
    const k = params.keys;
    const cfg = snd.Config{
        .transport = if (is_tcp) .tcp else .udp,
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
        .initial_seq = params.initial_seq, // continue the Go conn's seq space (strictly-newest at the receiver)
        .fps = params.fps,
        .startup_grace_ns = if (params.startup_grace_ns > 0) params.startup_grace_ns else snd.default_startup_grace_ns,
        .clock_offset_ns = if (params.media_clock_ns != 0) params.media_clock_ns - snd.nowNs() else 0,
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

const FdRes = struct { fd: sock.Fd, own: bool };

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
        .nacks_recv = st.nacks_recv,
    };
}
const RmRateHint = extern struct { gen: u64, max_bitrate_kbps: i32, max_fps: i32, max_height: i32, quality: i32 };

export fn rm_tx_rate_hint(s: ?*snd.Sender, out: ?*RmRateHint) void {
    const sender = s orelse return;
    const o = out orelse return;
    const h = sender.rateHint();
    o.* = .{ .gen = h.gen, .max_bitrate_kbps = h.max_bitrate_kbps, .max_fps = h.max_fps, .max_height = h.max_height, .quality = h.quality };
}

// ── receiver (ABI v5) ─────────────────────────────────────────────────────────────────────────────
const rx = @import("receiver.zig");
const po = @import("playout.zig");

const RmRxParams = extern struct {
    abi_version: u32,
    struct_size: u32,
    socket_fd: i64,
    peer_addr: ?[*]const u8,
    peer_addr_len: i32,
    keys: RmKeys,
    body_max: i32,
    fps: i32,
    preset: i32,
    stream: u16,
    initial_seq: u64,
    max_frame_bytes: i32,
};
const RmRxFrameCb = *const fn (?*anyopaque, [*]const u8, usize, i64, i32) callconv(.c) void;
const RmRxStats = extern struct {
    datagrams: u64,
    delivered: u64,
    nacks_sent: u64,
    plis_sent: u64,
    reasm_drops: u64,
    slot_drops: u64,
    metas_sent: u64,
    loss_pct: f64,
    owd_ns: i64,
    owd_jitter_ns: i64,
    rtt_ns: i64,
    playout_ms: f64,
    target_ms: f64,
    max_ms: f64,
    misses: u64,
    misses10m: u64,
    repeats: u64,
    repeats10m: u64,
    grows: u64,
    decays: u64,
    rebases: u64,
    gap_drops: u64,
    pub_count: u64,
    pub_p50_ms: f64,
    pub_p99_ms: f64,
    pub_max_ms: f64,
    pub_over25: u64,
    pub_over50: u64,
};

// RxHandle adapts the C frame callback (int key) to the receiver's callback.
const RxHandle = struct { r: *rx.Receiver = undefined, cb: ?RmRxFrameCb, user: ?*anyopaque, fd: sock.Fd };

fn rxDeliver(user: ?*anyopaque, ptr: [*]const u8, len: usize, pts: i64, key: bool) callconv(.c) void {
    const h: *RxHandle = @ptrCast(@alignCast(user.?));
    if (h.cb) |cb| cb(h.user, ptr, len, pts, @intFromBool(key));
}

export fn rm_rx_open(p: ?*const RmRxParams, cb: ?RmRxFrameCb, user: ?*anyopaque, out: ?*?*RxHandle) c_int {
    const params = p orelse return RM_ERR_ARGS;
    const o = out orelse return RM_ERR_ARGS;
    o.* = null;
    if (params.abi_version != abi_version or params.struct_size != @sizeOf(RmRxParams)) return RM_ERR_ARGS;
    if (params.socket_fd < 0) return RM_ERR_SOCKET;
    const k = params.keys;
    const h = alloc.create(RxHandle) catch return RM_ERR_ARGS;
    h.* = .{ .cb = cb, .user = user, .fd = @intCast(params.socket_fd) };
    const cfg = rx.RxConfig{
        .encrypt = k.encrypt != 0,
        .token = k.token,
        .open_key = k.recv_key,
        .open_prefix = k.recv_nonce_prefix,
        .send_key = k.send_key,
        .send_prefix = k.send_nonce_prefix,
        .body_max = if (params.body_max > 0) @intCast(params.body_max) else 1200,
        .fps = if (params.fps > 0) @floatFromInt(params.fps) else 60,
        .preset = switch (params.preset) {
            1 => po.low_latency,
            2 => po.smooth,
            else => po.balanced,
        },
        .stream = params.stream,
        .initial_seq = params.initial_seq,
        .max_frame_bytes = if (params.max_frame_bytes > 0) @intCast(params.max_frame_bytes) else 4 << 20,
    };
    var peer: ?sock.Addr4 = null;
    if (params.peer_addr) |pa| {
        if (params.peer_addr_len >= @sizeOf(sock.Addr4)) peer = @as(*align(1) const sock.Addr4, @ptrCast(pa)).*;
    }
    h.r = rx.Receiver.openPeer(alloc, h.fd, cfg, peer, rxDeliver, h) catch {
        alloc.destroy(h);
        return RM_ERR_CRYPTO;
    };
    o.* = h;
    return RM_OK;
}

export fn rm_rx_stats_get(hp: ?*RxHandle, out: ?*RmRxStats) void {
    const h = hp orelse return;
    const o = out orelse return;
    const s = h.r.stats();
    const pb = h.r.publish.snapshot();
    const pl = s.playout;
    o.* = .{
        .datagrams = s.datagrams, .delivered = s.delivered, .nacks_sent = s.nacks_sent, .plis_sent = s.plis_sent,
        .reasm_drops = s.reasm_drops, .slot_drops = s.slot_drops, .metas_sent = s.metas_sent,
        .loss_pct = s.loss_pct, .owd_ns = s.owd_ns, .owd_jitter_ns = s.owd_jitter_ns, .rtt_ns = s.rtt_ns,
        .playout_ms = pl.playout_ms, .target_ms = pl.target_ms, .max_ms = pl.max_ms,
        .misses = pl.misses, .misses10m = pl.misses10m, .repeats = pl.repeats, .repeats10m = pl.repeats10m,
        .grows = pl.grows, .decays = pl.decays, .rebases = pl.rebases, .gap_drops = pl.gap_drops,
        .pub_count = pb.count, .pub_p50_ms = pb.p50_ms, .pub_p99_ms = pb.p99_ms, .pub_max_ms = pb.max_ms,
        .pub_over25 = pb.over25, .pub_over50 = pb.over50,
    };
}

export fn rm_rx_send_meta(hp: ?*RxHandle, json: ?[*]const u8, len: usize) c_int {
    const h = hp orelse return RM_ERR_ARGS;
    const j = json orelse return RM_ERR_ARGS;
    return if (h.r.sendMeta(j[0..len])) RM_OK else RM_ERR_ARGS;
}

export fn rm_rx_close(hp: ?*RxHandle) void {
    const h = hp orelse return;
    h.r.close();
    sock.close(h.fd);
    alloc.destroy(h);
}

export fn rm_tx_close(s: ?*snd.Sender) void {
    if (s) |sender| sender.close();
}

test "rm_tx_open rejects ABI version + struct-size skew before opening a socket" {
    var out: ?*snd.Sender = null;
    var p = RmOpenParams{
        .abi_version = 1, // a stale v1 caller
        .struct_size = @sizeOf(RmOpenParams),
        .transport = 0,
        .codec = 5,
        .stream = 1,
        .body_max = 1200,
        .socket_fd = -1,
        .peer_addr = null,
        .peer_addr_len = 0,
        .keys = std.mem.zeroes(RmKeys),
        .initial_rate_bytes_per_sec = 0,
        .initial_seq = 0,
        .fps = 60,
        .startup_grace_ns = 0,
        .media_clock_ns = 0,
    };
    try std.testing.expectEqual(RM_ERR_ARGS, rm_tx_open(&p, null, null, &out));
    try std.testing.expectEqual(@as(?*snd.Sender, null), out);
    // Correct version, wrong size (same-version build drift) is caught too.
    p.abi_version = abi_version;
    p.struct_size = @sizeOf(RmOpenParams) - 8;
    try std.testing.expectEqual(RM_ERR_ARGS, rm_tx_open(&p, null, null, &out));
    try std.testing.expectEqual(@as(?*snd.Sender, null), out);
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
    _ = @import("chaos_test.zig");
    _ = @import("hrtimer.zig");
    _ = @import("sock.zig");
    _ = @import("e2e_test.zig");
    _ = @import("tcp_test.zig");
    _ = @import("reassembler.zig");
    _ = @import("histogram.zig");
    _ = snd;
    if (builtin.os.tag == .linux) _ = @import("interop_test.zig"); // socket loopback: Linux only
}

var g_rx_frames = std.atomic.Value(u32).init(0);
fn testRxCb(_: ?*anyopaque, au: [*]const u8, len: usize, _: i64, _: i32) callconv(.c) void {
    if (len > 0 and au[0] == 0x5A) _ = g_rx_frames.fetchAdd(1, .monotonic);
}

test "ABI v5: rm_rx_* receives a native sender's frames; rm_rx_send_meta MetaRate reaches the sender" {
    const hrt = @import("hrtimer.zig");
    var master: [32]u8 = undefined;
    for (&master, 0..) |*b, i| b.* = @intCast(i + 7);
    const salt = "abi-v5-receiver-salt-0123456789abcdef";
    const txk = keys.deriveUdp(&master, salt, false);
    const rxk = keys.deriveUdp(&master, salt, true);
    const tok = keys.token(&master, salt);

    const rfd = try sock.udpSocket();
    var la = sock.Addr4.loopback(0);
    try sock.bind(rfd, &la);
    const raddr = try sock.localAddr(rfd);
    var p = RmRxParams{ .abi_version = abi_version, .struct_size = @sizeOf(RmRxParams), .socket_fd = @intCast(rfd), .peer_addr = null, .peer_addr_len = 0, .keys = .{ .send_key = rxk.send.key, .send_nonce_prefix = rxk.send.prefix, .recv_key = rxk.recv.key, .recv_nonce_prefix = rxk.recv.prefix, .token = tok, .encrypt = 1, .initiator = 1 }, .body_max = 1200, .fps = 60, .preset = 1, .stream = 1, .initial_seq = 5000, .max_frame_bytes = 0 };
    var h: ?*RxHandle = null;
    try std.testing.expectEqual(RM_OK, rm_rx_open(&p, testRxCb, null, &h));
    defer rm_rx_close(h);
    p.abi_version = 4;
    var bad: ?*RxHandle = null;
    try std.testing.expectEqual(RM_ERR_ARGS, rm_rx_open(&p, testRxCb, null, &bad)); // stale caller

    const tfd = try snd.udpSocket();
    try snd.udpConnect(tfd, &raddr, @sizeOf(sock.Addr4));
    const s = try snd.Sender.open(alloc, .{ .token = tok, .send_key = txk.send.key, .send_prefix = txk.send.prefix, .recv_key = txk.recv.key, .recv_prefix = txk.recv.prefix }, tfd, true);
    defer s.close();
    var au = [_]u8{0x5A} ** 5000;
    var i: u32 = 0;
    const t0 = hrt.nowMono();
    while (i < 30) : (i += 1) {
        s.submit(&au, @as(i64, i) * 16_666_667, if (i == 0) snd.flag_keyframe else 0);
        hrt.sleepUntil(t0 + @as(i64, i + 1) * 16_666_667);
    }
    const json = "{\"t\":\"rate\",\"stream\":0,\"br\":12000,\"fps\":30}";
    try std.testing.expectEqual(RM_OK, rm_rx_send_meta(h, json, json.len));
    const dl = hrt.nowMono() + 2_000_000_000;
    while ((g_rx_frames.load(.monotonic) < 30 or s.rateHint().gen == 0) and hrt.nowMono() < dl) hrt.sleepUntil(hrt.nowMono() + 5_000_000);
    try std.testing.expectEqual(@as(u32, 30), g_rx_frames.load(.monotonic));
    const rh = s.rateHint();
    try std.testing.expect(rh.gen == 1 and rh.max_bitrate_kbps == 12000 and rh.max_fps == 30);
    hrt.sleepUntil(hrt.nowMono() + 300_000_000);
    var st: RmRxStats = undefined;
    rm_rx_stats_get(h, &st);
    try std.testing.expect(st.delivered == 30 and st.metas_sent == 1 and st.rtt_ns > 0 and st.pub_count > 0);
}
