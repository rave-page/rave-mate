//! rave-mate-txtest - TEST-ONLY native sender (zigmedia sender.zig) with an optional chaos relay, used
//! by the Go tests to drive the encoder child's network receive (netrx.zig) end to end without a GPU.
//! Never embedded or shipped.
//!
//! stdin line 1 (JSON): {"peer":"127.0.0.1:p","token":..,"skey":<hex32>,"spfx":..,"rkey":<hex32>,
//!   "rpfx":..,"enc":true,"iseq":..,"stream":1,"codec":4,"frames":N (0 = until quit),"au":"small"|
//!   "realistic"|"pipe","loss":0.0003,"stalls":[[start_ms,dur_ms],..],"seed":1}
//! then "quit" (or EOF) ends: the sender closes with a bye. au "pipe": stdin instead carries REAL access
//! units from the test's encoder, little-endian records [u32 len][u32 flags(bit0 key)][i64 pts ns][data];
//! len 0 (or EOF) ends. The producer paces; each AU is submitted on arrival.
//! stdout: {"ev":"up","local":"ip:port"} (the address the receiver will see datagrams from), then
//!   {"ev":"stats",...} every 250 ms, {"ev":"done",...} at exit.
const std = @import("std");
const zm = @import("zigmedia");
const snd = zm.sender;
const sock = zm.sock;
const hrt = zm.hrtimer;

const Params = struct {
    peer: []const u8 = "",
    token: u32 = 0,
    skey: []const u8 = "",
    spfx: u32 = 0,
    rkey: []const u8 = "",
    rpfx: u32 = 0,
    enc: bool = true,
    iseq: u64 = 0,
    stream: u16 = 1,
    codec: u8 = 4,
    frames: u32 = 0,
    au: []const u8 = "small",
    loss: f64 = 0,
    stalls: []const [2]i64 = &.{},
    seed: u64 = 1,
};

var out_mu: std.atomic.Mutex = .unlocked;
var g_out: *std.Io.Writer = undefined;

fn emit(v: anytype) void {
    while (!out_mu.tryLock()) std.atomic.spinLoopHint();
    defer out_mu.unlock();
    var s: std.json.Stringify = .{ .writer = g_out };
    s.write(v) catch return;
    g_out.writeByte('\n') catch return;
    g_out.flush() catch return;
}

var g_quit = std.atomic.Value(bool).init(false);
var g_idr = std.atomic.Value(u32).init(0);
var g_force_key = std.atomic.Value(bool).init(false);

fn onIdr(_: ?*anyopaque) callconv(.c) void {
    _ = g_idr.fetchAdd(1, .monotonic);
    g_force_key.store(true, .release); // PLI / queue drop: next AU is a keyframe
}

fn hex32(out: *[32]u8, h: []const u8) bool {
    if (h.len != 64) return false;
    for (out, 0..) |*b, i| b.* = std.fmt.parseInt(u8, h[i * 2 .. i * 2 + 2], 16) catch return false;
    return true;
}

fn parseAddr4(s: []const u8) ?sock.Addr4 {
    const colon = std.mem.lastIndexOfScalar(u8, s, ':') orelse return null;
    const port = std.fmt.parseInt(u16, s[colon + 1 ..], 10) catch return null;
    var a = sock.Addr4{ .port_be = std.mem.nativeToBig(u16, port) };
    var it = std.mem.splitScalar(u8, s[0..colon], '.');
    var i: usize = 0;
    while (it.next()) |p| : (i += 1) {
        if (i >= 4) return null;
        a.addr[i] = std.fmt.parseInt(u8, p, 10) catch return null;
    }
    return if (i == 4) a else null;
}

fn fmtAddr(buf: []u8, a: sock.Addr4) []const u8 {
    return std.fmt.bufPrint(buf, "{d}.{d}.{d}.{d}:{d}", .{ a.addr[0], a.addr[1], a.addr[2], a.addr[3], a.port() }) catch "";
}

// ── chaos relay (receiver-side stalls + random media loss; control never delayed) ────────────────
const hold_cap = 16384;
const Relay = struct {
    fd: sock.Fd,
    rx_addr: sock.Addr4,
    tx_addr: sock.Addr4 = .{},
    have_tx: bool = false,
    loss: f64 = 0,
    stalls: []const [2]i64 = &.{}, // ms, relative to the first media datagram
    t0: i64 = 0,
    held: [][1500]u8,
    held_len: []usize,
    n_held: usize = 0,
    lost: u64 = 0,
    stop: std.atomic.Value(bool) = .init(false),
    prng: std.Random.DefaultPrng,

    fn run(r: *Relay) void {
        var buf: [2048]u8 = undefined;
        while (!r.stop.load(.acquire)) {
            var got = false;
            while (true) {
                var from: sock.Addr4 = .{};
                const n = sock.recvFrom(r.fd, &buf, &from) catch break;
                got = true;
                if (from.port_be == r.rx_addr.port_be and std.mem.eql(u8, &from.addr, &r.rx_addr.addr)) {
                    if (r.have_tx) _ = sock.sendTo(r.fd, buf[0..n], &r.tx_addr);
                    continue;
                }
                if (!r.have_tx) {
                    r.tx_addr = from;
                    r.have_tx = true;
                    r.t0 = hrt.nowMono();
                }
                if (r.loss > 0 and (buf[0] & 0x0f) == 0 and r.prng.random().float(f64) < r.loss) {
                    r.lost += 1;
                    continue;
                }
                if (r.inStall(hrt.nowMono()) and r.n_held < hold_cap) {
                    @memcpy(r.held[r.n_held][0..n], buf[0..n]);
                    r.held_len[r.n_held] = n;
                    r.n_held += 1;
                    continue;
                }
                r.flush();
                _ = sock.sendTo(r.fd, buf[0..n], &r.rx_addr);
            }
            if (!r.inStall(hrt.nowMono())) r.flush();
            if (!got) hrt.sleepUntil(hrt.nowMono() + 100_000);
        }
    }

    fn inStall(r: *const Relay, now: i64) bool {
        if (!r.have_tx) return false;
        const t = now - r.t0;
        for (r.stalls) |s| if (t >= s[0] * 1_000_000 and t < (s[0] + s[1]) * 1_000_000) return true;
        return false;
    }

    fn flush(r: *Relay) void {
        for (0..r.n_held) |i| _ = sock.sendTo(r.fd, r.held[i][0..r.held_len[i]], &r.rx_addr);
        r.n_held = 0;
    }
};

fn auBytes(realistic: bool, i: u32) usize {
    if (!realistic) return 3000;
    if (i % 60 == 0) return 400_000 + (@as(usize, i) * 5003) % 300_001;
    return 80_000 + (@as(usize, i) * 4001) % 70_001;
}

fn statsLoop(s: *snd.Sender, relay: ?*Relay) void {
    while (!g_quit.load(.acquire)) {
        report(s, relay, "stats");
        hrt.sleepUntil(hrt.nowMono() + 250_000_000);
    }
}

fn report(s: *snd.Sender, relay: ?*Relay, ev: []const u8) void {
    const st = s.snapshot();
    const h = s.rateHint();
    emit(.{ .ev = ev, .submitted = st.aus_submitted, .sent = st.aus_sent, .dropped = st.aus_dropped, .retx = st.retransmits, .nacks = st.nacks_recv, .dead = st.dead, .gen = h.gen, .br = h.max_bitrate_kbps, .fps = h.max_fps, .maxh = h.max_height, .idr = g_idr.load(.monotonic), .relay_lost = if (relay) |r| r.lost else 0 });
}

pub fn main(init: std.process.Init) !void {
    const gpa = init.gpa;
    var out_buf: [8 * 1024]u8 = undefined;
    var stdout_writer = std.Io.File.stdout().writerStreaming(init.io, &out_buf);
    g_out = &stdout_writer.interface;
    var in_buf: [16 * 1024]u8 = undefined;
    var stdin_reader = std.Io.File.stdin().readerStreaming(init.io, &in_buf);
    const in = &stdin_reader.interface;

    const line = (in.takeDelimiter('\n') catch return error.Stdin) orelse return error.Stdin;
    var arena: std.heap.ArenaAllocator = .init(gpa);
    defer arena.deinit();
    const p = try std.json.parseFromSliceLeaky(Params, arena.allocator(), std.mem.trim(u8, line, " \t\r"), .{ .ignore_unknown_fields = true });
    var skey: [32]u8 = undefined;
    var rkey: [32]u8 = undefined;
    if (!hex32(&skey, p.skey) or !hex32(&rkey, p.rkey)) return error.Keys;
    const peer = parseAddr4(p.peer) orelse return error.Peer;

    // Optional relay between the sender and the receiver.
    var relay_store: ?Relay = null;
    var relay_thread: ?std.Thread = null;
    var target = peer;
    if (p.loss > 0 or p.stalls.len > 0) {
        const rfd = try sock.udpSocket();
        var la = sock.Addr4.loopback(0);
        try sock.bind(rfd, &la);
        relay_store = Relay{ .fd = rfd, .rx_addr = peer, .loss = p.loss, .stalls = p.stalls, .held = try gpa.alloc([1500]u8, hold_cap), .held_len = try gpa.alloc(usize, hold_cap), .prng = std.Random.DefaultPrng.init(p.seed) };
        target = try sock.localAddr(rfd);
        relay_thread = try std.Thread.spawn(.{}, Relay.run, .{&relay_store.?});
    }
    const relay: ?*Relay = if (relay_store != null) &relay_store.? else null;

    const fd = try snd.udpSocket();
    try snd.udpConnect(fd, &target, @sizeOf(sock.Addr4));
    const s = try snd.Sender.open(gpa, .{ .codec = p.codec, .stream = p.stream, .encrypt = p.enc, .token = p.token, .send_key = skey, .send_prefix = p.spfx, .recv_key = rkey, .recv_prefix = p.rpfx, .initial_seq = p.iseq, .rate_bps = 60e6 / 8.0 }, fd, true);
    s.on_idr = onIdr;
    var ab: [64]u8 = undefined;
    // The receiver sees our datagrams from the relay's socket when one runs, else from ours.
    const seen_from = if (relay) |r| blk: {
        var a = sock.localAddr(r.fd) catch break :blk "";
        a.addr = .{ 127, 0, 0, 1 };
        break :blk fmtAddr(&ab, a);
    } else blk: {
        var a = sock.localAddr(fd) catch break :blk "";
        a.addr = .{ 127, 0, 0, 1 };
        break :blk fmtAddr(&ab, a);
    };
    emit(.{ .ev = "up", .local = seen_from });

    const st_thread = try std.Thread.spawn(.{}, statsLoop, .{ s, relay });
    if (std.mem.eql(u8, p.au, "pipe")) {
        pipeLoop(gpa, in, s) catch {};
        g_quit.store(true, .release);
        st_thread.join();
        report(s, relay, "done");
        s.close();
        if (relay) |r| {
            hrt.sleepUntil(hrt.nowMono() + 50_000_000);
            r.stop.store(true, .release);
            relay_thread.?.join();
        }
        return;
    }
    const quit_thread = try std.Thread.spawn(.{}, readQuit, .{in});
    quit_thread.detach(); // EOF/quit sets g_quit; a blocked stdin read must not delay exit

    const realistic = std.mem.eql(u8, p.au, "realistic");
    const buf = try gpa.alloc(u8, 1 << 20);
    defer gpa.free(buf);
    const t0 = hrt.nowMono();
    var i: u32 = 0;
    while (!g_quit.load(.acquire)) : (i += 1) {
        if (p.frames > 0 and i >= p.frames) {
            hrt.sleepUntil(hrt.nowMono() + 10_000_000);
            continue;
        }
        const n = auBytes(realistic, i);
        buf[0] = 0xA5;
        std.mem.writeInt(u32, buf[1..5], i, .big);
        for (buf[5..n], 5..) |*b, j| b.* = @truncate(j *% 131 +% i);
        const key = i % 60 == 0 or g_force_key.swap(false, .acq_rel);
        s.submit(buf[0..n], @as(i64, i) * 16_666_667, if (key) snd.flag_keyframe else 0);
        hrt.sleepUntil(t0 + @as(i64, i + 1) * 16_666_667);
    }
    st_thread.join();
    report(s, relay, "done");
    s.close(); // clean bye
    if (relay) |r| {
        hrt.sleepUntil(hrt.nowMono() + 50_000_000); // let the relay forward the bye
        r.stop.store(true, .release);
        relay_thread.?.join();
    }
}

// pipeLoop submits AUs streamed on stdin (bounded: one AU buffer, 4 MiB cap = the sender's slot cap).
fn pipeLoop(gpa: std.mem.Allocator, in: *std.Io.Reader, s: *snd.Sender) !void {
    const buf = try gpa.alloc(u8, 4 << 20);
    defer gpa.free(buf);
    while (true) {
        const len = try in.takeInt(u32, .little);
        if (len == 0) return;
        const flags = try in.takeInt(u32, .little);
        const pts = try in.takeInt(i64, .little);
        if (len > buf.len) {
            try in.discardAll(len);
            continue; // oversized: dropped (the receiver PLIs, the encoder re-keys)
        }
        try in.readSliceAll(buf[0..len]);
        const key = (flags & 1) != 0 or g_force_key.swap(false, .acq_rel);
        s.submit(buf[0..len], pts, if (key) snd.flag_keyframe else 0);
    }
}

fn readQuit(in: *std.Io.Reader) void {
    while (true) {
        const l = (in.takeDelimiter('\n') catch break) orelse break;
        if (std.mem.indexOf(u8, l, "quit") != null) break;
    }
    g_quit.store(true, .release);
}
