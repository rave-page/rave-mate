//! Native TCP sender tests + transport soaks (Linux + Windows). The TCP reader here is an independent
//! implementation of the legacy medialink Conn wire (u32 BE len + AES-GCM counter nonce) - the Go
//! cross-check is internal/medialink/nativesender_interop_linux_test.go (real medialink.Conn).
//! Soaks run the REAL sender (and, for UDP, the real native receiver) at 4K60-realistic sizes under an
//! armed failing allocator: any allocation after open fails loudly (zero steady-state allocations).
//! Duration: -Dsoak-seconds=N (default 5; acceptance run 600).
const std = @import("std");
const builtin = @import("builtin");
const keys = @import("keys.zig");
const aead = @import("aead.zig");
const wire = @import("wire.zig");
const sock = @import("sock.zig");
const snd = @import("sender.zig");
const tcp_tx = @import("tcp_tx.zig");
const hrtimer = @import("hrtimer.zig");
const rx_mod = @import("receiver.zig");
const po = @import("playout.zig");
const linux = std.os.linux;
const is_windows = builtin.os.tag == .windows;
const testing = std.testing;

const soak_seconds: i64 = @import("build_options").soak_seconds;

// ── failing-allocator guard ──────────────────────────────────────────────────────────────────────
const Guard = struct {
    backing: std.mem.Allocator,
    armed: std.atomic.Value(bool) = .init(false),
    hits: std.atomic.Value(u32) = .init(0),

    fn allocator(g: *Guard) std.mem.Allocator {
        return .{ .ptr = g, .vtable = &vt };
    }
    const vt = std.mem.Allocator.VTable{ .alloc = galloc, .resize = gresize, .remap = gremap, .free = gfree };
    fn galloc(ctx: *anyopaque, len: usize, a: std.mem.Alignment, ra: usize) ?[*]u8 {
        const g: *Guard = @ptrCast(@alignCast(ctx));
        if (g.armed.load(.monotonic)) {
            _ = g.hits.fetchAdd(1, .monotonic);
            return null; // fail loudly: steady state must never allocate
        }
        return g.backing.rawAlloc(len, a, ra);
    }
    fn gresize(ctx: *anyopaque, m: []u8, a: std.mem.Alignment, n: usize, ra: usize) bool {
        const g: *Guard = @ptrCast(@alignCast(ctx));
        if (g.armed.load(.monotonic) and n > m.len) {
            _ = g.hits.fetchAdd(1, .monotonic);
            return false;
        }
        return g.backing.rawResize(m, a, n, ra);
    }
    fn gremap(ctx: *anyopaque, m: []u8, a: std.mem.Alignment, n: usize, ra: usize) ?[*]u8 {
        const g: *Guard = @ptrCast(@alignCast(ctx));
        if (g.armed.load(.monotonic)) {
            _ = g.hits.fetchAdd(1, .monotonic);
            return null;
        }
        return g.backing.rawRemap(m, a, n, ra);
    }
    fn gfree(ctx: *anyopaque, m: []u8, a: std.mem.Alignment, ra: usize) void {
        const g: *Guard = @ptrCast(@alignCast(ctx));
        g.backing.rawFree(m, a, ra);
    }
};

// ── loopback TCP pair (test-only socket calls) ───────────────────────────────────────────────────
const win = struct {
    extern "ws2_32" fn socket(af: i32, typ: i32, proto: i32) callconv(.winapi) usize;
    extern "ws2_32" fn bind(s: usize, addr: *const anyopaque, len: i32) callconv(.winapi) i32;
    extern "ws2_32" fn listen(s: usize, backlog: i32) callconv(.winapi) i32;
    extern "ws2_32" fn accept(s: usize, addr: ?*anyopaque, len: ?*i32) callconv(.winapi) usize;
    extern "ws2_32" fn connect(s: usize, addr: *const anyopaque, len: i32) callconv(.winapi) i32;
    extern "ws2_32" fn getsockname(s: usize, addr: *anyopaque, len: *i32) callconv(.winapi) i32;
    extern "ws2_32" fn recv(s: usize, buf: [*]u8, len: i32, flags: i32) callconv(.winapi) i32;
    extern "ws2_32" fn send(s: usize, buf: [*]const u8, len: i32, flags: i32) callconv(.winapi) i32;
};

fn tcpSocket() !sock.Fd {
    _ = try sock.udpSocket(); // ensures WSAStartup on Windows (socket closed below)
    if (is_windows) {
        const s = win.socket(2, 1, 6);
        if (s == sock.invalid) return error.Socket;
        return s;
    }
    const r = linux.socket(linux.AF.INET, linux.SOCK.STREAM, 0);
    if (linux.errno(r) != .SUCCESS) return error.Socket;
    return @intCast(r);
}

const Pair = struct { server: sock.Fd, client: sock.Fd };

fn tcpPair() !Pair {
    const l = try tcpSocket();
    defer sock.close(l);
    var a = sock.Addr4.loopback(0);
    if (is_windows) {
        if (win.bind(l, &a, @sizeOf(sock.Addr4)) != 0 or win.listen(l, 1) != 0) return error.Listen;
        var al: i32 = @sizeOf(sock.Addr4);
        _ = win.getsockname(l, &a, &al);
        const c = try tcpSocket();
        if (win.connect(c, &a, @sizeOf(sock.Addr4)) != 0) return error.Connect;
        const s = win.accept(l, null, null);
        if (s == sock.invalid) return error.Accept;
        return .{ .server = s, .client = c };
    }
    if (linux.errno(linux.bind(l, @ptrCast(&a), @sizeOf(sock.Addr4))) != .SUCCESS) return error.Listen;
    if (linux.errno(linux.listen(l, 1)) != .SUCCESS) return error.Listen;
    a = try sock.localAddr(l);
    const c = try tcpSocket();
    if (linux.errno(linux.connect(c, @ptrCast(&a), @sizeOf(sock.Addr4))) != .SUCCESS) return error.Connect;
    const r = linux.accept(l, null, null);
    if (linux.errno(r) != .SUCCESS) return error.Accept;
    return .{ .server = @intCast(r), .client = c };
}

fn readFull(fd: sock.Fd, buf: []u8) bool {
    var off: usize = 0;
    while (off < buf.len) {
        var n: isize = 0;
        if (is_windows) {
            n = win.recv(fd, buf[off..].ptr, @intCast(buf.len - off), 0);
        } else {
            const r = linux.recvfrom(fd, buf[off..].ptr, buf.len - off, 0, null, null);
            if (linux.errno(r) != .SUCCESS) return false;
            n = @intCast(r);
        }
        if (n <= 0) return false;
        off += @intCast(n);
    }
    return true;
}

fn writeAll(fd: sock.Fd, buf: []const u8) void {
    if (is_windows) {
        _ = win.send(fd, buf.ptr, @intCast(buf.len), 0);
    } else {
        _ = linux.sendto(fd, buf.ptr, buf.len, 0, null, 0);
    }
}

// TcpReader is the receiver end: reads + opens the sender's frames, verifies the AU pattern.
const TcpReader = struct {
    fd: sock.Fd,
    key: [32]u8,
    buf: []u8,
    pt: []u8,
    ctr: u64 = 0,
    frames: std.atomic.Value(u32) = .init(0),
    bad: std.atomic.Value(u32) = .init(0),
    bytes: std.atomic.Value(u64) = .init(0),

    fn run(r: *TcpReader) void {
        var lp: [4]u8 = undefined;
        while (readFull(r.fd, &lp)) {
            const n = std.mem.readInt(u32, &lp, .big);
            if (n > r.buf.len or !readFull(r.fd, r.buf[0..n])) return;
            const pl = aead.open(r.pt, r.buf[0..n], &.{}, keys.nonce(0, r.ctr), r.key) catch {
                _ = r.bad.fetchAdd(1, .monotonic);
                return;
            };
            r.ctr += 1;
            const f = wire.parseFrameForTest(r.pt[0..pl]) catch {
                _ = r.bad.fetchAdd(1, .monotonic);
                return;
            };
            if (f.kind == 1) {
                const want = r.frames.load(.monotonic);
                if (f.seq != want or f.payload.len < 5 or f.payload[0] != 0xA5 or std.mem.readInt(u32, f.payload[1..5], .big) != want) _ = r.bad.fetchAdd(1, .monotonic);
                _ = r.frames.fetchAdd(1, .monotonic);
                _ = r.bytes.fetchAdd(f.payload.len, .monotonic);
            }
        }
    }
};

fn fixture() struct { master: [32]u8, salt: [16]u8 } {
    var m: [32]u8 = undefined;
    for (&m, 0..) |*b, i| b.* = @intCast(i * 3 + 1);
    return .{ .master = m, .salt = "tcp-route-salt!!".* };
}

fn realisticAU(i: u32) usize {
    if (i % 60 == 0) return 400_000 + (@as(usize, i) * 5003) % 500_001;
    return 80_000 + (@as(usize, i) * 4001) % 70_001;
}

fn fillAU(buf: []u8, i: u32) void {
    buf[0] = 0xA5;
    std.mem.writeInt(u32, buf[1..5], i, .big);
}

test "native TCP sender: legacy Conn wire, every frame in order, receiver meta (MetaRate) honoured" {
    const fx = fixture();
    const kS = tcp_tx.deriveTcp(&fx.master, &fx.salt, false); // sender = accepting end
    const kR = tcp_tx.deriveTcp(&fx.master, &fx.salt, true);
    const p = try tcpPair();
    defer sock.close(p.client);
    defer sock.close(p.server);
    const s = try snd.Sender.open(testing.allocator, .{ .transport = .tcp, .send_key = kS.send, .recv_key = kS.recv }, p.server, false);
    var rd = TcpReader{ .fd = p.client, .key = kR.recv, .buf = try testing.allocator.alloc(u8, 2 << 20), .pt = try testing.allocator.alloc(u8, 2 << 20) };
    defer testing.allocator.free(rd.buf);
    defer testing.allocator.free(rd.pt);
    const th = try std.Thread.spawn(.{}, TcpReader.run, .{&rd});
    var au: [20000]u8 = undefined;
    var i: u32 = 0;
    while (i < 50) : (i += 1) {
        fillAU(&au, i);
        s.submit(&au, @as(i64, i) * 16_666_667, if (i == 0) snd.flag_keyframe else 0);
    }
    // receiver -> sender: one sealed meta frame (c2s counter 0) carrying a MetaRate
    const json = "{\"t\":\"rate\",\"stream\":0,\"br\":9000,\"fps\":30,\"maxh\":720}";
    var fr: [128]u8 = undefined;
    const fl = wire.marshalFrame(&fr, .{ .stream = 0, .kind = 0, .codec = 0, .flags = 0, .seq = 0, .pts = 1 }, json);
    var out: [160]u8 = undefined;
    const cl = aead.seal(out[4..], fr[0..fl], &.{}, keys.nonce(0, 0), kR.send);
    std.mem.writeInt(u32, out[0..4], @intCast(cl), .big);
    writeAll(p.client, out[0 .. 4 + cl]);
    const dl = hrtimer.nowMono() + 3_000_000_000;
    while ((rd.frames.load(.monotonic) < 50 or s.rateHint().gen == 0) and hrtimer.nowMono() < dl) hrtimer.sleepUntil(hrtimer.nowMono() + 2_000_000);
    s.close(); // caller-owned socket stays open; reader exits on our close below
    sock.close(p.client);
    th.join();
    try testing.expectEqual(@as(u32, 50), rd.frames.load(.monotonic));
    try testing.expectEqual(@as(u32, 0), rd.bad.load(.monotonic));
}

test "soak TCP: 4K60 sizes, zero steady-state allocations (failing allocator armed), no drops" {
    const fx = fixture();
    const kS = tcp_tx.deriveTcp(&fx.master, &fx.salt, false);
    const kR = tcp_tx.deriveTcp(&fx.master, &fx.salt, true);
    const p = try tcpPair();
    defer sock.close(p.server);
    var g = Guard{ .backing = testing.allocator };
    const s = try snd.Sender.open(g.allocator(), .{ .transport = .tcp, .send_key = kS.send, .recv_key = kS.recv }, p.server, false);
    var rd = TcpReader{ .fd = p.client, .key = kR.recv, .buf = try testing.allocator.alloc(u8, 2 << 20), .pt = try testing.allocator.alloc(u8, 2 << 20) };
    defer testing.allocator.free(rd.buf);
    defer testing.allocator.free(rd.pt);
    const th = try std.Thread.spawn(.{}, TcpReader.run, .{&rd});
    const buf = try testing.allocator.alloc(u8, 1 << 20);
    defer testing.allocator.free(buf);
    g.armed.store(true, .monotonic);
    const n: u32 = @intCast(soak_seconds * 60);
    const t0 = hrtimer.nowMono();
    var i: u32 = 0;
    while (i < n) : (i += 1) {
        const sz = realisticAU(i);
        fillAU(buf[0..sz], i);
        s.submit(buf[0..sz], @as(i64, i) * 16_666_667, if (i % 60 == 0) snd.flag_keyframe else 0);
        hrtimer.sleepUntil(t0 + @as(i64, i + 1) * 16_666_667);
    }
    const dl = hrtimer.nowMono() + 3_000_000_000;
    while (rd.frames.load(.monotonic) < n and hrtimer.nowMono() < dl) hrtimer.sleepUntil(hrtimer.nowMono() + 5_000_000);
    const st = s.snapshot();
    g.armed.store(false, .monotonic);
    s.close();
    sock.close(p.client);
    th.join();
    if (@import("build_options").chaos_report) std.debug.print("soak TCP {d}s: frames={d}/{d} bad={d} bytes={d}MB txDropped={d} allocHits={d}\n", .{ soak_seconds, rd.frames.load(.monotonic), n, rd.bad.load(.monotonic), rd.bytes.load(.monotonic) >> 20, st.aus_dropped, g.hits.load(.monotonic) });
    try testing.expectEqual(@as(u32, 0), g.hits.load(.monotonic));
    try testing.expectEqual(n, rd.frames.load(.monotonic));
    try testing.expectEqual(@as(u32, 0), rd.bad.load(.monotonic));
    try testing.expectEqual(@as(u64, 0), st.aus_dropped);
}

var g_udp_frames = std.atomic.Value(u32).init(0);
fn onUdpFrame(_: ?*anyopaque, ptr: [*]const u8, len: usize, _: i64, _: bool) callconv(.c) void {
    if (len >= 5 and ptr[0] == 0xA5) _ = g_udp_frames.fetchAdd(1, .monotonic);
}

test "soak UDP: native sender -> native receiver, 4K60 sizes, zero steady-state allocations on both" {
    var master: [32]u8 = undefined;
    for (&master, 0..) |*b, i| b.* = @intCast(i + 11);
    const salt = "udp-soak-salt-0123456789";
    const tx = keys.deriveUdp(&master, salt, false);
    const rxk = keys.deriveUdp(&master, salt, true);
    const tok = keys.token(&master, salt);
    const rfd = try sock.udpSocket();
    defer sock.close(rfd);
    var la = sock.Addr4.loopback(0);
    try sock.bind(rfd, &la);
    const raddr = try sock.localAddr(rfd);
    var gr = Guard{ .backing = testing.allocator };
    var gs = Guard{ .backing = testing.allocator };
    g_udp_frames.store(0, .monotonic);
    const r = try rx_mod.Receiver.open(gr.allocator(), rfd, .{ .encrypt = true, .token = tok, .open_key = rxk.recv.key, .open_prefix = rxk.recv.prefix, .send_key = rxk.send.key, .send_prefix = rxk.send.prefix, .preset = po.low_latency }, onUdpFrame, null);
    const tfd = try snd.udpSocket();
    try snd.udpConnect(tfd, &raddr, @sizeOf(sock.Addr4));
    const s = try snd.Sender.open(gs.allocator(), .{ .token = tok, .send_key = tx.send.key, .send_prefix = tx.send.prefix, .recv_key = tx.recv.key, .recv_prefix = tx.recv.prefix, .rate_bps = 60e6 / 8.0 }, tfd, true);
    const buf = try testing.allocator.alloc(u8, 1 << 20);
    defer testing.allocator.free(buf);
    gr.armed.store(true, .monotonic);
    gs.armed.store(true, .monotonic);
    const n: u32 = @intCast(soak_seconds * 60);
    const t0 = hrtimer.nowMono();
    var i: u32 = 0;
    while (i < n) : (i += 1) {
        const sz = realisticAU(i);
        fillAU(buf[0..sz], i);
        s.submit(buf[0..sz], @as(i64, i) * 16_666_667, if (i % 60 == 0) snd.flag_keyframe else 0);
        hrtimer.sleepUntil(t0 + @as(i64, i + 1) * 16_666_667);
    }
    const dl = hrtimer.nowMono() + 3_000_000_000;
    while (g_udp_frames.load(.monotonic) < n and hrtimer.nowMono() < dl) hrtimer.sleepUntil(hrtimer.nowMono() + 5_000_000);
    const st = s.snapshot();
    gr.armed.store(false, .monotonic);
    gs.armed.store(false, .monotonic);
    s.close();
    r.close();
    if (@import("build_options").chaos_report) std.debug.print("soak UDP {d}s: frames={d}/{d} txDropped={d} retx={d} allocHits tx={d} rx={d}\n", .{ soak_seconds, g_udp_frames.load(.monotonic), n, st.aus_dropped, st.retransmits, gs.hits.load(.monotonic), gr.hits.load(.monotonic) });
    try testing.expectEqual(@as(u32, 0), gs.hits.load(.monotonic));
    try testing.expectEqual(@as(u32, 0), gr.hits.load(.monotonic));
    try testing.expectEqual(n, g_udp_frames.load(.monotonic));
    try testing.expectEqual(@as(u64, 0), st.aus_dropped);
}
