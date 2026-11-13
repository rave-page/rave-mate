//! End-to-end native sender -> chaos relay -> native receiver over loopback, on Linux AND Windows
//! (Winsock RX + high-res timer). The relay is a UDP forwarder that injects the owner's failure modes
//! on the media path: receiver-side arrival stalls (datagrams held, then flushed in a burst) and
//! random datagram loss (recovered by NACK -> retransmit). The real stack runs: sender TX queue +
//! pacer, reassembler, NACK, adaptive playout, publish-interval histogram.
const std = @import("std");
const keys = @import("keys.zig");
const sock = @import("sock.zig");
const snd = @import("sender.zig");
const hrtimer = @import("hrtimer.zig");
const rx_mod = @import("receiver.zig");
const po = @import("playout.zig");
const testing = std.testing;

const Fixture = struct { tx: keys.KeySchedule, rx: keys.KeySchedule, token: u32 };

fn fixture() Fixture {
    var master: [32]u8 = undefined;
    for (&master, 0..) |*b, i| b.* = @intCast(i + 1);
    var salt: [40]u8 = undefined;
    @memcpy(salt[0..32], &(([_]u8{0xAA} ** 16) ++ ([_]u8{0xBB} ** 16)));
    @memcpy(salt[32..40], "route-e2");
    return .{ .tx = keys.deriveUdp(&master, &salt, false), .rx = keys.deriveUdp(&master, &salt, true), .token = keys.token(&master, &salt) };
}

fn bindLoop() !struct { fd: sock.Fd, addr: sock.Addr4 } {
    const fd = try sock.udpSocket();
    var a = sock.Addr4.loopback(0);
    try sock.bind(fd, &a);
    return .{ .fd = fd, .addr = try sock.localAddr(fd) };
}

// ── chaos relay ──────────────────────────────────────────────────────────────────────────────────
const hold_cap = 16384;
const Relay = struct {
    fd: sock.Fd,
    addr: sock.Addr4,
    rx_addr: sock.Addr4, // the receiver
    tx_addr: sock.Addr4 = .{}, // the sender (learned)
    have_tx: bool = false,
    loss: f64 = 0,
    stalls: []const [2]i64 = &.{}, // {start offset ns, duration ns} relative to the first media datagram
    t0: i64 = 0,
    held: [][1500]u8,
    held_len: []usize,
    n_held: usize = 0,
    lost: u64 = 0,
    seen: u64 = 0, // media datagrams received from the sender
    fwd: u64 = 0, // media datagrams forwarded to the receiver
    stop: std.atomic.Value(bool) = .init(false),
    thread: ?std.Thread = null,
    prng: std.Random.DefaultPrng = std.Random.DefaultPrng.init(42),

    fn run(r: *Relay) void {
        var buf: [2048]u8 = undefined;
        while (!r.stop.load(.acquire)) {
            var got = false;
            while (true) {
                var from: sock.Addr4 = .{};
                const n = sock.recvFrom(r.fd, &buf, &from) catch break;
                got = true;
                const from_rx = from.port_be == r.rx_addr.port_be;
                if (from_rx) { // control back to the sender: never delayed or lost
                    if (r.have_tx) _ = sock.sendTo(r.fd, buf[0..n], &r.tx_addr);
                    continue;
                }
                r.seen += 1;
                if (!r.have_tx) {
                    r.tx_addr = from;
                    r.have_tx = true;
                    r.t0 = hrtimer.nowMono();
                }
                if (r.loss > 0 and (buf[0] & 0x0f) == 0 and r.prng.random().float(f64) < r.loss) { // media dg only
                    r.lost += 1;
                    continue;
                }
                if (r.inStall(hrtimer.nowMono()) and r.n_held < hold_cap) {
                    @memcpy(r.held[r.n_held][0..n], buf[0..n]);
                    r.held_len[r.n_held] = n;
                    r.n_held += 1;
                    continue;
                }
                r.flush();
                if (sock.sendTo(r.fd, buf[0..n], &r.rx_addr) > 0) r.fwd += 1;
            }
            if (!r.inStall(hrtimer.nowMono())) r.flush();
            if (!got) hrtimer.sleepUntil(hrtimer.nowMono() + 100_000);
        }
    }

    fn inStall(r: *const Relay, now: i64) bool {
        if (!r.have_tx) return false;
        const t = now - r.t0;
        for (r.stalls) |s| if (t >= s[0] and t < s[0] + s[1]) return true;
        return false;
    }

    fn flush(r: *Relay) void {
        for (0..r.n_held) |i| {
            if (sock.sendTo(r.fd, r.held[i][0..r.held_len[i]], &r.rx_addr) > 0) r.fwd += 1;
        }
        r.n_held = 0;
    }
};

// ── receive sink ─────────────────────────────────────────────────────────────────────────────────
const Sink = struct {
    count: std.atomic.Value(u32) = .init(0),
    bad: std.atomic.Value(u32) = .init(0), // payload check failures
    last_seen: std.atomic.Value(u32) = .init(0),
};

fn onFrame(user: ?*anyopaque, ptr: [*]const u8, len: usize, pts: i64, key: bool) callconv(.c) void {
    _ = pts;
    _ = key;
    const s: *Sink = @ptrCast(@alignCast(user.?));
    if (len < 8 or ptr[0] != 0xA5) _ = s.bad.fetchAdd(1, .monotonic);
    _ = s.count.fetchAdd(1, .monotonic);
}

fn fillAU(buf: []u8, i: u32) void {
    buf[0] = 0xA5;
    std.mem.writeInt(u32, buf[1..5], i, .big);
    for (buf[5..], 5..) |*b, j| b.* = @truncate(j *% 131 +% i);
}

const Run = struct {
    frames: u32,
    au_bytes: fn (i: u32) usize,
    loss: f64 = 0,
    stalls: []const [2]i64 = &.{},
    preset: po.Preset = po.balanced,
    rate_bps: f64 = 60e6 / 8.0, // sender target (bytes/s)
    force_pli: bool = false,
};

fn smallAU(_: u32) usize {
    return 3000;
}
fn realisticAU(i: u32) usize {
    if (i % 60 == 0) return 400_000 + (@as(usize, i) * 5003) % 500_001;
    return 80_000 + (@as(usize, i) * 4001) % 70_001;
}

var g_idr = std.atomic.Value(u32).init(0);
fn onIdr(_: ?*anyopaque) callconv(.c) void {
    _ = g_idr.fetchAdd(1, .monotonic);
}

fn runE2E(comptime cfg: Run) !struct { sink: Sink, rx: rx_mod.Stats, tx: snd.Stats, relay_lost: u64, relay_seen: u64, relay_fwd: u64, publish: @import("histogram.zig").Snapshot, idr: u32 } {
    const fx = fixture();
    const rxs = try bindLoop();
    defer sock.close(rxs.fd);
    const rel = try bindLoop();
    defer sock.close(rel.fd);

    const held = try testing.allocator.alloc([1500]u8, hold_cap);
    defer testing.allocator.free(held);
    const held_len = try testing.allocator.alloc(usize, hold_cap);
    defer testing.allocator.free(held_len);
    var relay = Relay{ .fd = rel.fd, .addr = rel.addr, .rx_addr = rxs.addr, .loss = cfg.loss, .stalls = cfg.stalls, .held = held, .held_len = held_len };
    relay.thread = try std.Thread.spawn(.{}, Relay.run, .{&relay});
    defer {
        relay.stop.store(true, .release);
        relay.thread.?.join();
    }

    var sink = Sink{};
    const rx = try rx_mod.Receiver.open(testing.allocator, rxs.fd, .{
        .encrypt = true,
        .token = fx.token,
        .open_key = fx.rx.recv.key,
        .open_prefix = fx.rx.recv.prefix,
        .send_key = fx.rx.send.key,
        .send_prefix = fx.rx.send.prefix,
        .preset = cfg.preset,
    }, onFrame, &sink);
    defer rx.close();

    const txfd = try snd.udpSocket();
    try snd.udpConnect(txfd, &relay.addr, @sizeOf(sock.Addr4));
    const sender = try snd.Sender.open(testing.allocator, .{
        .codec = 5,
        .stream = 1,
        .encrypt = true,
        .token = fx.token,
        .send_key = fx.tx.send.key,
        .send_prefix = fx.tx.send.prefix,
        .recv_key = fx.tx.recv.key,
        .recv_prefix = fx.tx.recv.prefix,
        .rate_bps = cfg.rate_bps,
    }, txfd, true);
    defer sender.close();
    sender.on_idr = onIdr;
    g_idr.store(0, .monotonic);

    const buf = try testing.allocator.alloc(u8, 1 << 20);
    defer testing.allocator.free(buf);
    const t0 = hrtimer.nowMono();
    var i: u32 = 0;
    while (i < cfg.frames) : (i += 1) {
        const n = cfg.au_bytes(i);
        fillAU(buf[0..n], i);
        sender.submit(buf[0..n], @as(i64, i) * 16_666_667, if (i % 60 == 0) snd.flag_keyframe else 0);
        hrtimer.sleepUntil(t0 + @as(i64, i + 1) * 16_666_667); // absolute 60 fps schedule
    }
    const deadline = hrtimer.nowMono() + 3_000_000_000;
    while (sink.count.load(.monotonic) < cfg.frames and hrtimer.nowMono() < deadline) hrtimer.sleepUntil(hrtimer.nowMono() + 10_000_000);
    if (cfg.force_pli) rx.sendPLI(hrtimer.nowMono()); // test-thread call: exercises the PLI wire only
    hrtimer.sleepUntil(hrtimer.nowMono() + 300_000_000); // let the stats snapshot catch up
    return .{ .sink = sink, .rx = rx.stats(), .tx = sender.snapshot(), .relay_lost = relay.lost, .relay_seen = relay.seen, .relay_fwd = relay.fwd, .publish = rx.publish.snapshot(), .idr = g_idr.load(.monotonic) };
}

test "e2e: frames delivered through playout (AEAD, small AUs)" {
    const r = try runE2E(.{ .frames = 30, .au_bytes = smallAU, .preset = po.low_latency, .force_pli = true });
    try testing.expectEqual(@as(u32, 30), r.sink.count.load(.monotonic));
    try testing.expectEqual(@as(u32, 1), r.idr); // receiver PLI (meta NACK pli=true) -> sender on_need_idr
    try testing.expectEqual(@as(u32, 0), r.sink.bad.load(.monotonic));
}

test "e2e: random loss recovered by NACK -> retransmit (every frame delivered, no PLI)" {
    const r = try runE2E(.{ .frames = 60, .au_bytes = smallAU, .loss = 0.05, .preset = po.low_latency });
    try testing.expectEqual(@as(u32, 60), r.sink.count.load(.monotonic));
    try testing.expect(r.relay_lost > 0);
    try testing.expect(r.tx.retransmits > 0);
    try testing.expect(r.rx.nacks_sent > 0);
    try testing.expectEqual(@as(u64, 0), r.rx.plis_sent);
}

test "e2e chaos: 4K60 sizes, 10 s, 0.1% loss + 2 arrival stalls (60/130 ms): every frame, no drop, no PLI" {
    const stalls = [_][2]i64{ .{ 4_000_000_000, 60_000_000 }, .{ 7_000_000_000, 130_000_000 } };
    const r = try runE2E(.{ .frames = 600, .au_bytes = realisticAU, .loss = 0.001, .stalls = &stalls });
    const p = r.publish;
    std.debug.print("e2e chaos 4K60: delivered={d}/600 relayLost={d} nacks={d} retx={d} txDropped={d} plis={d} playout={d:.1}ms misses={d} repeats={d} publish p50/p99/max={d:.1}/{d:.1}/{d:.1}ms over25={d} reasmDrops={d} slotDrops={d} gapDrops={d} dgrams={d} txDg={d} relaySeen={d} relayFwd={d}\n", .{
        r.sink.count.load(.monotonic), r.relay_lost,      r.rx.nacks_sent,     r.tx.retransmits,
        r.tx.aus_dropped,              r.rx.plis_sent,    r.rx.playout.playout_ms, r.rx.playout.misses,
        r.rx.playout.repeats,          p.p50_ms,          p.p99_ms,            p.max_ms,
        p.over25,                      r.rx.reasm_drops,  r.rx.slot_drops,     r.rx.playout.gap_drops,
        r.rx.datagrams,                r.tx.datagrams_sent, r.relay_seen,     r.relay_fwd,
    });
    try testing.expectEqual(@as(u32, 600), r.sink.count.load(.monotonic));
    try testing.expectEqual(@as(u32, 0), r.sink.bad.load(.monotonic));
    try testing.expectEqual(@as(u64, 0), r.tx.aus_dropped);
    try testing.expectEqual(@as(u64, 0), r.rx.plis_sent);
    // The 130 ms stall lands on an IDR (frame 420, 500 kB) that also loses a fragment: stall + IDR
    // serialization + one NACK round trip ~165 ms - inside the 170 ms balanced bound. Linux caps SO_RCVBUF at net.core.rmem_max (~200 KB by
    // default): the flushed stall burst overflows it, NACK recovers every datagram but may cost one
    // more miss there; the Windows receiver honours the 16 MiB buffer.
    try testing.expect(r.rx.playout.misses <= if (@import("builtin").os.tag == .windows) 0 else 2);
    try testing.expectEqual(@as(u64, 0), r.rx.playout.gap_drops);
    try testing.expect(p.over25 <= 2); // 0 measured; slack for a shared dev PC's scheduler
}
