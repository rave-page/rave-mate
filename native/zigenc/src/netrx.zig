//! netrx: a decode session's NETWORK source (unit C part 2). The parent (Go) negotiated the route,
//! ran the UDP reachability handshake and derived the per-route keys; it then duplicates the bound UDP
//! socket into this process (WSADuplicateSocketW -> WSAPROTOCOL_INFOW, hex in the "netrx" op) and the
//! zigmedia receiver owns the receive path from here: socket RX -> auth -> reassembly -> adaptive
//! PTS-locked playout -> on the receiver thread, at each frame's playout deadline, Dec.feed -> publish.
//! The decoder is touched ONLY by the receiver thread while attached (frame + idle callbacks); the
//! session thread only polls the receiver's stats/meta and writes the shm telemetry block.
const std = @import("std");
const zm = @import("zigmedia");
const mf = @import("mf.zig");
const dec = @import("dec.zig");

const rxm = zm.receiver;
const sock = zm.sock;
const hrt = zm.hrtimer;
const Hist = zm.histogram.Hist;

extern "ws2_32" fn WSAStartup(ver: u16, data: *[408]u8) callconv(.winapi) i32;
extern "ws2_32" fn WSASocketW(af: i32, typ: i32, proto: i32, info: *const anyopaque, g: u32, flags: u32) callconv(.winapi) usize;
extern "ws2_32" fn ioctlsocket(s: usize, cmd: i32, arg: *u32) callconv(.winapi) i32;
extern "ws2_32" fn WSAIoctl(s: usize, code: u32, in: ?*const anyopaque, in_len: u32, out: ?*anyopaque, out_len: u32, ret: *u32, ov: ?*anyopaque, cr: ?*anyopaque) callconv(.winapi) i32;
extern "ws2_32" fn closesocket(s: usize) callconv(.winapi) i32;

pub const meta_max = rxm.meta_in_max;
pub const presetByName = zm.playout.presetByName;

/// sizeof(WSAPROTOCOL_INFOW) on Windows x64 (= x/sys/windows WSAProtocolInfo).
pub const proto_info_len = 628;
const FROM_PROTOCOL_INFO: i32 = -1;
const WSA_FLAG_OVERLAPPED: u32 = 0x01;
const FIONBIO: i32 = @bitCast(@as(u32, 0x8004667e));
const SIO_UDP_CONNRESET: u32 = 0x9800000C;
const invalid_socket: usize = ~@as(usize, 0);

var wsa_once = std.atomic.Value(bool).init(false);

/// socketFromInfo materializes the parent's duplicated socket: non-blocking, ICMP-reset off, large
/// buffers (the receiver additionally raises SO_RCVBUF to 16 MiB).
pub fn socketFromInfo(info: []const u8) !usize {
    if (info.len != proto_info_len) return error.ProtoInfo;
    if (!wsa_once.swap(true, .acq_rel)) {
        var d: [408]u8 = undefined;
        _ = WSAStartup(0x0202, &d);
    }
    var aligned: [proto_info_len]u8 align(8) = undefined;
    @memcpy(&aligned, info);
    const s = WSASocketW(FROM_PROTOCOL_INFO, FROM_PROTOCOL_INFO, FROM_PROTOCOL_INFO, &aligned, 0, WSA_FLAG_OVERLAPPED);
    if (s == invalid_socket) return error.WSASocket;
    var one: u32 = 1;
    _ = ioctlsocket(s, FIONBIO, &one);
    var off: u32 = 0;
    var ret: u32 = 0;
    _ = WSAIoctl(s, SIO_UDP_CONNRESET, &off, 4, null, 0, &ret, null, null);
    return s;
}

/// parseAddr4 parses "a.b.c.d:port" (IPv4 only; the parent gates IPv6 routes to the Go path).
pub fn parseAddr4(s: []const u8) ?sock.Addr4 {
    const colon = std.mem.lastIndexOfScalar(u8, s, ':') orelse return null;
    const port = std.fmt.parseInt(u16, s[colon + 1 ..], 10) catch return null;
    var a = sock.Addr4{ .port_be = std.mem.nativeToBig(u16, port) };
    var it = std.mem.splitScalar(u8, s[0..colon], '.');
    var i: usize = 0;
    while (it.next()) |p| : (i += 1) {
        if (i >= 4) return null;
        a.addr[i] = std.fmt.parseInt(u8, p, 10) catch return null;
    }
    if (i != 4) return null;
    return a;
}

/// hexDecode decodes lowercase/uppercase hex into out (len must be exactly 2*out.len).
pub fn hexDecode(out: []u8, hex: []const u8) bool {
    if (hex.len != out.len * 2) return false;
    for (out, 0..) |*b, i| b.* = std.fmt.parseInt(u8, hex[i * 2 .. i * 2 + 2], 16) catch return false;
    return true;
}

/// Params is one attach request (parsed from the "netrx" op).
pub const Params = struct {
    info: [proto_info_len]u8 = undefined,
    peer: ?sock.Addr4 = null,
    cfg: rxm.RxConfig,
};

// ── shm telemetry block (after the inbound ring: off = 256 + ring_size, 512 B) ─────────────────
// u64 slots (f64 as bits). Slot 0 is a seqlock (odd while writing). Mirrors internal/mfenc
// procnet_windows.go netSlot* - change BOTH ends together.
pub const blk_size = 512;
pub const Slot = enum(u8) {
    seqlock = 0, datagrams, delivered, published, nacks_sent, nack_recovered, nack_expired, plis_sent,
    reasm_drops, slot_drops, metas_sent, loss_pct, owd_ns, owd_jitter_ns, rtt_ns, playout_ms, target_ms,
    max_ms, misses, misses10m, repeats, repeats10m, rebases, gap_drops, grows, decays, pub_count,
    pub_p50, pub_p99, pub_max, pub_over25, pub_over50, pub_over100, last_rx_ns, ctl_seq, key_waits,
    rebinds, media_bytes, keyframes, rel_p99, rel_max, rel_over25, meta_in_drops, buffered, dec_errors,
    now_ns, pub_mean, bye, state, rx_errors,
};
comptime {
    std.debug.assert(@as(usize, @intFromEnum(Slot.rx_errors)) * 8 + 8 <= blk_size);
}

/// Net is the attached network source of one decode session. Heap-owned by the session.
pub const Net = struct {
    gpa: std.mem.Allocator,
    rx: *rxm.Receiver = undefined,
    fd: usize = invalid_socket,
    d: ?*dec.Dec, // null = null_dec (GPU-free tests): a delivered frame counts as published
    // receiver-thread state
    com_init: bool = false,
    need_key: bool = true,
    last_pub_n: u64 = 0,
    // cross-thread (receiver thread writes, session thread reads)
    published: std.atomic.Value(u64) = .init(0),
    key_waits: std.atomic.Value(u64) = .init(0),
    dec_errors: std.atomic.Value(u64) = .init(0),
    dead: std.atomic.Value(bool) = .init(false), // decoder/publish hard error: session emits dstgone
    pub_hist: Hist = .{}, // PUBLISH interval (decoded frame landed in the destination) - acceptance metric
    last_pub_ns: std.atomic.Value(i64) = .init(0),

    /// attach opens the receiver on the duplicated socket. On error nothing is left running.
    pub fn attach(gpa: std.mem.Allocator, d: ?*dec.Dec, p: *const Params) !*Net {
        const n = try gpa.create(Net);
        errdefer gpa.destroy(n);
        n.* = .{ .gpa = gpa, .d = d };
        n.fd = try socketFromInfo(&p.info);
        errdefer _ = closesocket(n.fd);
        n.rx = try rxm.Receiver.openPeerIdle(gpa, n.fd, p.cfg, p.peer, onFrame, onIdle, n);
        return n;
    }

    /// close: stop the RX thread (joined: no callback after) -> close the socket. The decoder is the
    /// session's and is closed by it afterwards (teardown order: RX -> decoder -> destination).
    pub fn close(n: *Net, blk: ?[*]u8) void {
        n.rx.halt();
        if (blk) |b| writeBlock(b, n, 0); // final numbers incl. the last control seq (re-attach floor)
        n.rx.close();
        _ = closesocket(n.fd);
        n.gpa.destroy(n);
    }

    fn notePublished(n: *Net, d: *dec.Dec) void {
        if (d.pub_n == n.last_pub_n) return;
        const now = hrt.nowMono();
        n.pub_hist.mark(now); // one interval per wake: a pump that lands 2 frames is a burst, not 2 intervals
        n.last_pub_n = d.pub_n;
        n.published.store(d.pub_n, .release);
        n.last_pub_ns.store(now, .release);
    }

    fn fail(n: *Net) void {
        _ = n.dec_errors.fetchAdd(1, .monotonic);
        n.dead.store(true, .release);
    }
};

// onFrame: receiver thread, at the frame's playout deadline.
fn onFrame(user: ?*anyopaque, ptr: [*]const u8, len: usize, pts: i64, key: bool) callconv(.c) void {
    const n: *Net = @ptrCast(@alignCast(user.?));
    if (n.need_key) {
        if (!key) {
            _ = n.key_waits.fetchAdd(1, .monotonic);
            n.rx.sendPLI(hrt.nowMono()); // fresh decoder: ask for an IDR (rate-limited 250 ms)
            return;
        }
        n.need_key = false;
    }
    const d = n.d orelse {
        const now = hrt.nowMono();
        n.pub_hist.mark(now);
        _ = n.published.fetchAdd(1, .release);
        n.last_pub_ns.store(now, .release);
        return;
    };
    if (n.dead.load(.acquire)) return;
    if (!n.com_init) {
        mf.coInitMTA(); // MF on this thread from now on (MTA, same as the session threads)
        n.com_init = true;
    }
    const rc = d.feed(ptr[0..len], @divTrunc(pts, 100));
    if (rc < 0) return n.fail();
    n.notePublished(d);
}

// onIdle: receiver thread, every worker iteration (<= 2 ms): async decoder output lands promptly.
fn onIdle(user: ?*anyopaque) callconv(.c) void {
    const n: *Net = @ptrCast(@alignCast(user.?));
    const d = n.d orelse return;
    if (!n.com_init or n.dead.load(.acquire)) return;
    if (d.pump() < 0) return n.fail();
    n.notePublished(d);
}

/// writeBlock publishes the telemetry block (session thread, ~4 Hz). Seqlock-framed.
pub fn writeBlock(base: [*]u8, n: *Net, state: u64) void {
    var s = n.rx.stats();
    // Lifecycle + control fields LIVE (the receiver's snapshot is up to 250 ms old): liveness, bye,
    // the control seq floor for a re-attach and the meta counters must not lag.
    const r = n.rx;
    s.last_rx_ns = r.last_rx.load(.monotonic);
    s.bye = r.bye.load(.acquire);
    s.ctl_seq = r.seq_pub.load(.monotonic);
    s.metas_sent = r.metas_sent.load(.monotonic);
    s.delivered = r.delivered.load(.monotonic);
    s.datagrams = r.net_marks.load(.monotonic);
    const rel = n.rx.publish.snapshot();
    const pb = n.pub_hist.snapshot();
    const pl = s.playout;
    const sl: *volatile u64 = @ptrCast(@alignCast(base));
    const seq0 = sl.*;
    @atomicStore(u64, @volatileCast(sl), seq0 +% 1, .release);
    const vals = [_]struct { Slot, u64 }{
        .{ .datagrams, s.datagrams },                .{ .delivered, s.delivered },
        .{ .published, n.published.load(.acquire) }, .{ .nacks_sent, s.nacks_sent },
        .{ .nack_recovered, s.nack_recovered },      .{ .nack_expired, s.nack_expired },
        .{ .plis_sent, s.plis_sent },                .{ .reasm_drops, s.reasm_drops },
        .{ .slot_drops, s.slot_drops },              .{ .metas_sent, s.metas_sent },
        .{ .loss_pct, fb(s.loss_pct) },               .{ .owd_ns, ib(s.owd_ns) },
        .{ .owd_jitter_ns, ib(s.owd_jitter_ns) },     .{ .rtt_ns, ib(s.rtt_ns) },
        .{ .playout_ms, fb(pl.playout_ms) },          .{ .target_ms, fb(pl.target_ms) },
        .{ .max_ms, fb(pl.max_ms) },                  .{ .misses, pl.misses },
        .{ .misses10m, pl.misses10m },               .{ .repeats, pl.repeats },
        .{ .repeats10m, pl.repeats10m },             .{ .rebases, pl.rebases },
        .{ .gap_drops, pl.gap_drops },               .{ .grows, pl.grows },
        .{ .decays, pl.decays },                     .{ .pub_count, pb.count },
        .{ .pub_p50, fb(pb.p50_ms) },                 .{ .pub_p99, fb(pb.p99_ms) },
        .{ .pub_max, fb(pb.max_ms) },                 .{ .pub_over25, pb.over25 },
        .{ .pub_over50, pb.over50 },                 .{ .pub_over100, pb.over100 },
        .{ .last_rx_ns, ib(s.last_rx_ns) },           .{ .ctl_seq, s.ctl_seq },
        .{ .key_waits, n.key_waits.load(.monotonic) }, .{ .rebinds, s.rebinds },
        .{ .media_bytes, s.media_bytes },            .{ .keyframes, s.keyframes },
        .{ .rel_p99, fb(rel.p99_ms) },                .{ .rel_max, fb(rel.max_ms) },
        .{ .rel_over25, rel.over25 },                .{ .meta_in_drops, s.meta_in_drops },
        .{ .buffered, pl.buffered },                 .{ .dec_errors, n.dec_errors.load(.monotonic) },
        .{ .now_ns, ib(hrt.nowMono()) },              .{ .pub_mean, fb(pb.mean_ms) },
        .{ .bye, @intFromBool(s.bye) },              .{ .state, state },
        .{ .rx_errors, s.rx_errors },
    };
    for (vals) |v| {
        const p: *volatile u64 = @ptrCast(@alignCast(base + @as(usize, @intFromEnum(v[0])) * 8));
        p.* = v[1];
    }
    @atomicStore(u64, @volatileCast(sl), seq0 +% 2, .release);
}

fn fb(x: f64) u64 {
    return @bitCast(x);
}
fn ib(x: i64) u64 {
    return @bitCast(x);
}

test "parseAddr4 + hexDecode" {
    const a = parseAddr4("192.168.120.3:47640").?;
    try std.testing.expectEqual(@as(u16, 47640), a.port());
    try std.testing.expectEqualSlices(u8, &.{ 192, 168, 120, 3 }, &a.addr);
    try std.testing.expect(parseAddr4("[::1]:5") == null);
    try std.testing.expect(parseAddr4("1.2.3:5") == null);
    var out: [3]u8 = undefined;
    try std.testing.expect(hexDecode(&out, "0aFF10"));
    try std.testing.expectEqualSlices(u8, &.{ 0x0a, 0xff, 0x10 }, &out);
    try std.testing.expect(!hexDecode(&out, "0aFF1"));
}

test "telemetry block layout fits + slot ids are stable (Go mirror)" {
    try std.testing.expectEqual(@as(u8, 0), @intFromEnum(Slot.seqlock));
    try std.testing.expectEqual(@as(u8, 3), @intFromEnum(Slot.published));
    try std.testing.expectEqual(@as(u8, 34), @intFromEnum(Slot.ctl_seq));
    try std.testing.expectEqual(@as(u8, 48), @intFromEnum(Slot.state));
}
