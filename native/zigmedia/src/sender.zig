//! Native UDP sender runtime (increment 2b). ONE worker thread owns the socket, the retransmit ring,
//! the pacer and the seq counters; the encode thread only calls submit() (AU queue, tiny metadata
//! lock). Single-worker = no retx/control races. Allocation-free in steady state (pools sized at open).
//! Worker loop: drain control datagrams (NACK→retransmit, ping→pong, bye/pong→liveness), send paced
//! media, keepalive, start-up-grace liveness. TCP + Windows priority are follow-ups; the UDP path is
//! byte-exact with the Go receiver (goldens).
const std = @import("std");
const builtin = @import("builtin");
const linux = std.os.linux;
const sock = @import("sock.zig");
const hrtimer = @import("hrtimer.zig");
const keys = @import("keys.zig");
const aead = @import("aead.zig");
const wire = @import("wire.zig");
const auqueue = @import("auqueue.zig");
const retxring = @import("retxring.zig");
const Pacer = @import("pacer.zig").Pacer;

pub const Transport = enum(u8) { udp = 0, tcp = 1 };
pub const flag_keyframe = auqueue.flag_keyframe;

pub const Config = struct {
    transport: Transport = .udp,
    codec: u8 = 5, // HEVC
    stream: u16 = 1,
    body_max: usize = 1200,
    encrypt: bool = true,
    token: u32 = 0,
    send_key: [32]u8 = undefined,
    send_prefix: u32 = 0,
    recv_key: [32]u8 = undefined,
    recv_prefix: u32 = 0,
    rate_bps: f64 = 0,
    initial_seq: u64 = 0, // first datagram seq (continues the Go handshake conn's seq space)
    fps: i32 = 0, // nominal frame-rate rung (mirrors NativeTXParams.FPS); not used on the TX path yet
    startup_grace_ns: i64 = default_startup_grace_ns, // liveness grace before the route establishes
    clock_offset_ns: i64 = 0, // mediaClock - this monotonic clock (Go-supplied); 0 = self-rebase to local mono
};

/// RateHint mirrors medialink.RateHint (MetaRate) + a generation counter (0 = none received).
pub const RateHint = struct { gen: u64 = 0, max_bitrate_kbps: i32 = 0, max_fps: i32 = 0, max_height: i32 = 0, quality: i32 = 0 };

// MetaMsg: the receiver meta JSON fields the native sender acts on (unknown fields ignored).
const MetaMsg = struct { t: []const u8 = "", pli: bool = false, br: i32 = 0, fps: i32 = 0, maxh: i32 = 0, q: i32 = 0, id: u32 = 0, t1: i64 = 0 };

// parseMeta extracts MetaMsg from the flat Go-marshaled meta JSON (no nesting, no escapes in these
// keys/values). Hand-rolled: std.json pulls f128 compiler-rt symbols the shim's link lacks.
fn parseMeta(js: []const u8) MetaMsg {
    var m = MetaMsg{};
    m.t = jsonStr(js, "t") orelse "";
    m.pli = std.mem.indexOf(u8, js, "\"pli\":true") != null;
    m.br = @truncate(jsonInt(js, "br") orelse 0);
    m.fps = @truncate(jsonInt(js, "fps") orelse 0);
    m.maxh = @truncate(jsonInt(js, "maxh") orelse 0);
    m.q = @truncate(jsonInt(js, "q") orelse 0);
    m.id = @truncate(@as(u64, @bitCast(jsonInt(js, "id") orelse 0)));
    m.t1 = jsonInt(js, "t1") orelse 0;
    return m;
}

fn jsonValue(js: []const u8, key: []const u8) ?[]const u8 {
    var i: usize = 0;
    while (std.mem.indexOfPos(u8, js, i, key)) |p| : (i = p + 1) {
        if (p == 0 or js[p - 1] != '"' or p + key.len >= js.len or js[p + key.len] != '"') continue;
        var v = p + key.len + 1;
        while (v < js.len and (js[v] == ' ' or js[v] == ':')) v += 1;
        return js[v..];
    }
    return null;
}

fn jsonInt(js: []const u8, key: []const u8) ?i64 {
    const v = jsonValue(js, key) orelse return null;
    var e: usize = 0;
    while (e < v.len and (v[e] == '-' or (v[e] >= '0' and v[e] <= '9'))) e += 1;
    return std.fmt.parseInt(i64, v[0..e], 10) catch null;
}

fn jsonStr(js: []const u8, key: []const u8) ?[]const u8 {
    const v = jsonValue(js, key) orelse return null;
    if (v.len == 0 or v[0] != '"') return null;
    const end = std.mem.indexOfScalarPos(u8, v, 1, '"') orelse return null;
    return v[1..end];
}

test "parseMeta reads Go meta JSON" {
    const r = parseMeta("{\"t\":\"rate\",\"stream\":0,\"br\":18000,\"fps\":30,\"maxh\":1080,\"q\":70}");
    try std.testing.expectEqualStrings("rate", r.t);
    try std.testing.expect(r.br == 18000 and r.fps == 30 and r.maxh == 1080 and r.q == 70);
    const s = parseMeta("{\"t\":\"sync\",\"id\":7,\"t1\":-123456789012}");
    try std.testing.expect(s.id == 7 and s.t1 == -123456789012);
    const n = parseMeta("{\"t\":\"nack\",\"stream\":1,\"from\":1,\"to\":0,\"pli\":true}");
    try std.testing.expect(n.pli and std.mem.eql(u8, n.t, "nack"));
}

pub const Stats = struct {
    aus_submitted: u64 = 0,
    aus_sent: u64 = 0,
    aus_dropped: u64 = 0,
    datagrams_sent: u64 = 0,
    bytes_sent: u64 = 0,
    retransmits: u64 = 0,
    seq: u64 = 0,
    queue_depth: i32 = 0,
    dead: i32 = 0,
    nacks_recv: u64 = 0,
};

// TX queue: absorbs >= 150 ms of TX stall (4K60 ~60 Mbps: 150 ms ~ 1.2 MB) with wide margin;
// bounded in frames AND bytes (auqueue.zig: drop-oldest + IDR request beyond it).
const tx_queue_frames = 64;
const tx_queue_bytes = 16 << 20;
const au_slot_cap = 4 << 20; // max single AU (4K keyframe headroom); larger = dropped + IDR
const retx_slots = 8192; // ~10 MB: >= 1 s of datagrams at 4K60 rates (stall + NACK round trip)
const retx_dg_cap = 1500;
const keepalive_ns: i64 = 100 * std.time.ns_per_ms;
const liveness_ns: i64 = 2 * std.time.ns_per_s;
pub const default_startup_grace_ns: i64 = 15 * std.time.ns_per_s;

// Pacing mirrors the Go udpConn (pacingRateFor): spread a frame's datagrams at a MULTIPLE of the
// target bitrate, NOT the bare rate. At the bare rate a 4K keyframe (hundreds of kB) stretches over
// many frame intervals and is dropped at the receiver's reassembly deadline; the gain finishes each
// frame well inside one interval. The high floor keeps a low — or wrong-unit — bitrate bursting fast
// on a LAN (the field bug: the bare target starved every multi-fragment frame; the Go path never did
// because of this floor). initial_rate_bytes_per_sec is thus the TARGET media bitrate, not the wire cap.
const pacing_gain: f64 = 8;
const pacer_min_rate_bytes: f64 = 32 << 20; // ≈256 Mbps floor (effectively unpaced on GbE)
fn pacedRate(target_bps: f64) f64 {
    if (target_bps <= 0) return pacer_min_rate_bytes; // unknown budget → floor (matches Go)
    const r = target_bps * pacing_gain;
    return if (r < pacer_min_rate_bytes) pacer_min_rate_bytes else r;
}

// ── socket + clock (sock.zig / hrtimer.zig: Linux syscalls or Winsock + QPC) ─────────────────────
pub fn udpSocket() !sock.Fd {
    return sock.udpSocket();
}
pub fn udpConnect(fd: sock.Fd, addr: *const anyopaque, len: u32) !void {
    return sock.connect(fd, addr, len);
}
fn sysSend(fd: sock.Fd, buf: []const u8) usize {
    return sock.send(fd, buf);
}
fn sysRecv(fd: sock.Fd, buf: []u8) !usize {
    return sock.recvFrom(fd, buf, null);
}
pub fn closeFd(fd: sock.Fd) void {
    sock.close(fd);
}
pub const Sender = struct {
    alloc: std.mem.Allocator,
    cfg: Config,
    fd: sock.Fd,
    own_fd: bool,

    queue: auqueue.AuQueue,
    ring: retxring.RetxRing,
    pacer: Pacer,

    // worker-owned
    seq: u64 = 0,
    frame_seq: u32 = 0,
    frame_buf: []u8, // marshaled frame (26 + AU)
    dg_buf: []u8, // one datagram (24 + body_max + tag)
    rx_buf: []u8, // control recv buffer
    last_keepalive_ns: i64 = 0,
    last_recv_ns: i64 = 0,
    established: bool = false,
    pts_rebased: bool = false, // worker-owned: the one-time PTS rebase has been computed
    pts_shift: i64 = 0, // added to every submitted PTS (media-clock domain)

    thread: ?std.Thread = null,
    stop: std.atomic.Value(bool) = .init(false),

    // stats (atomics; sampled by Go at ~1 Hz)
    s_submitted: std.atomic.Value(u64) = .init(0),
    s_dropped: std.atomic.Value(u64) = .init(0),
    s_aus_sent: std.atomic.Value(u64) = .init(0),
    s_dgrams: std.atomic.Value(u64) = .init(0),
    s_bytes: std.atomic.Value(u64) = .init(0),
    s_retx: std.atomic.Value(u64) = .init(0),
    s_seq: std.atomic.Value(u64) = .init(0),
    s_dead: std.atomic.Value(i32) = .init(0),
    s_nacks: std.atomic.Value(u64) = .init(0),
    s_plis: std.atomic.Value(u64) = .init(0), // receiver keyframe requests honoured
    hint_gen: std.atomic.Value(u64) = .init(0),
    hint_br: std.atomic.Value(i32) = .init(0),
    hint_fps: std.atomic.Value(i32) = .init(0),
    hint_maxh: std.atomic.Value(i32) = .init(0),
    hint_q: std.atomic.Value(i32) = .init(0),
    meta_seq: u32 = 0, // worker-owned: stream-0 meta frame seq
    last_pli_ns: i64 = 0,

    on_idr: ?*const fn (?*anyopaque) callconv(.c) void = null,
    idr_user: ?*anyopaque = null,

    pub fn open(alloc: std.mem.Allocator, cfg: Config, fd: sock.Fd, own_fd: bool) !*Sender {
        const s = try alloc.create(Sender);
        errdefer alloc.destroy(s);
        s.* = .{
            .alloc = alloc,
            .cfg = cfg,
            .fd = fd,
            .own_fd = own_fd,
            .queue = try auqueue.AuQueue.init(alloc, tx_queue_frames, tx_queue_bytes),
            .ring = try retxring.RetxRing.init(alloc, retx_slots, retx_dg_cap),
            .pacer = Pacer.init(pacedRate(cfg.rate_bps), nowNs()),
            .frame_buf = try alloc.alloc(u8, au_slot_cap + wire.frame_header_len),
            .dg_buf = try alloc.alloc(u8, wire.header_len + cfg.body_max + aead.tag_len + 64),
            .rx_buf = try alloc.alloc(u8, 2048),
        };
        s.seq = cfg.initial_seq; // continue the Go conn's datagram seq space (strictly-newest at the receiver)
        s.s_seq.store(cfg.initial_seq, .monotonic);
        s.last_recv_ns = nowNs();
        s.thread = try std.Thread.spawn(.{}, worker, .{s});
        return s;
    }

    pub fn close(s: *Sender) void {
        s.sendBye();
        s.stop.store(true, .release);
        if (s.thread) |t| t.join();
        if (s.own_fd) sock.close(s.fd);
        s.queue.deinit();
        s.ring.deinit();
        s.alloc.free(s.frame_buf);
        s.alloc.free(s.dg_buf);
        s.alloc.free(s.rx_buf);
        s.alloc.destroy(s);
    }

    pub fn submit(s: *Sender, au: []const u8, pts: i64, flags: u32) void {
        _ = s.s_submitted.fetchAdd(1, .monotonic);
        const dropped = au.len > au_slot_cap or s.queue.submit(au, pts, flags);
        if (dropped) {
            _ = s.s_dropped.fetchAdd(1, .monotonic);
            if (s.on_idr) |cb| cb(s.idr_user);
        }
    }

    pub fn setRate(s: *Sender, bps: f64) void {
        // Pacer is worker-owned; a benign racy write of the rate is fine (next reserve() picks it up).
        s.pacer.setRate(pacedRate(bps));
    }

    pub fn snapshot(s: *Sender) Stats {
        return .{
            .aus_submitted = s.s_submitted.load(.monotonic),
            .aus_sent = s.s_aus_sent.load(.monotonic),
            .aus_dropped = s.s_dropped.load(.monotonic),
            .datagrams_sent = s.s_dgrams.load(.monotonic),
            .bytes_sent = s.s_bytes.load(.monotonic),
            .retransmits = s.s_retx.load(.monotonic),
            .seq = s.s_seq.load(.monotonic),
            .dead = s.s_dead.load(.monotonic),
            .queue_depth = @intCast(s.queue.depth()),
            .nacks_recv = s.s_nacks.load(.monotonic),
        };
    }

    // ── worker ──────────────────────────────────────────────────────────────────
    fn worker(s: *Sender) void {
        setThisThreadRealtime();
        while (!s.stop.load(.acquire)) {
            const now = nowNs();
            s.drainControl(now);
            if (s.livenessDead(now)) {
                s.s_dead.store(1, .monotonic);
                // keep looping (drain/close handled by close()); stop sending media
            }
            s.sendOneAu();
            s.keepalive(now);
            sleepNs(500 * std.time.ns_per_us); // 0.5ms poll; pacing is per-datagram
        }
    }

    fn sendOneAu(s: *Sender) void {
        const idx = s.queue.pop() orelse return;
        defer s.queue.release(idx);
        const slot = s.queue.slot(idx);
        const m = wire.FrameMeta{ .stream = s.cfg.stream, .kind = 1, .codec = s.cfg.codec, .flags = @truncate(slot.flags & 0x07), .seq = s.frame_seq, .pts = s.rebasePts(slot.pts) };
        const wlen = wire.marshalFrame(s.frame_buf, m, slot.buf[0..slot.len]);
        const frame_wire = s.frame_buf[0..wlen];
        const count: u16 = @intCast(wire.numFrags(wlen, s.cfg.body_max));
        var i: u16 = 0;
        while (i < count) : (i += 1) {
            const lo = @as(usize, i) * s.cfg.body_max;
            const hi = @min(lo + s.cfg.body_max, wlen);
            const dglen = s.buildDatagram(wire.dg_type_media, s.cfg.stream, s.frame_seq, i, count, frame_wire[lo..hi]);
            const fnow = nowNs(); // refresh per fragment: the pacer must accrue tokens ACROSS a big keyframe
            s.pace(dglen, fnow);
            s.sendRaw(s.dg_buf[0..dglen]);
            s.ring.add(s.dg_buf[0..dglen], s.cfg.stream, s.frame_seq, i); // ring copies → dg_buf reusable now
            // Answer a NACK mid-keyframe, not after it: bounds retransmit latency on a many-fragment 4K
            // frame so recovery lands inside the receiver's reassembly deadline. ring.add already copied.
            if ((i & 0x0f) == 0x0f) s.drainControl(fnow);
        }
        s.frame_seq +%= 1;
        _ = s.s_aus_sent.fetchAdd(1, .monotonic);
    }

    // rebasePts shifts a submitted (capture-clock) PTS into the media-clock domain, replicating the Go
    // router's one-time rebase (runSend: ptsShift = mediaClock.Now() - firstPts, applied to every frame
    // so inter-frame deltas survive). The first frame maps to the media-clock instant of its send; the
    // shift uses the Go-supplied clock_offset (mediaClock - this monotonic clock, sampled on the same
    // host at attach). Without it the receiver sees PTS in a foreign epoch → every frame "late" → the
    // PTS-paced playout is bypassed (the field owd=3650ms / lateRate=1.0).
    fn rebasePts(s: *Sender, pts: i64) i64 {
        if (!s.pts_rebased) {
            s.pts_shift = (nowNs() + s.cfg.clock_offset_ns) - pts;
            s.pts_rebased = true;
        }
        return pts + s.pts_shift;
    }

    // buildDatagram writes header+sealed/clear body into dg_buf, advances seq, returns total length.
    fn buildDatagram(s: *Sender, typ: u8, stream: u16, frame_seq: u32, frag_idx: u16, frag_count: u16, body: []const u8) usize {
        const flags: u8 = if (s.cfg.encrypt) wire.flag_aead else 0;
        const h = wire.Header{ .typ = typ, .flags = flags, .token = s.cfg.token, .seq = s.seq, .stream = stream, .frame_seq = frame_seq, .frag_idx = frag_idx, .frag_count = frag_count };
        h.encode(s.dg_buf[0..wire.header_len]);
        var total: usize = wire.header_len;
        if (s.cfg.encrypt) {
            total += aead.seal(s.dg_buf[wire.header_len..], body, s.dg_buf[0..wire.header_len], keys.nonce(s.cfg.send_prefix, s.seq), s.cfg.send_key);
        } else {
            @memcpy(s.dg_buf[wire.header_len..][0..body.len], body);
            total += body.len;
        }
        s.seq +%= 1;
        s.s_seq.store(s.seq, .monotonic);
        return total;
    }

    fn pace(s: *Sender, n: usize, now: i64) void {
        const w = s.pacer.reserve(n, now);
        if (w > 0) sleepNs(w);
    }

    fn sendRaw(s: *Sender, dg: []const u8) void {
        const sent = sysSend(s.fd, dg);
        if (sent == 0) return;
        _ = s.s_dgrams.fetchAdd(1, .monotonic);
        _ = s.s_bytes.fetchAdd(sent, .monotonic);
    }

    fn keepalive(s: *Sender, now: i64) void {
        if (now - s.last_keepalive_ns < keepalive_ns) return;
        s.last_keepalive_ns = now;
        var ts: [8]u8 = undefined;
        std.mem.writeInt(u64, &ts, @bitCast(now), .big);
        const dglen = s.buildDatagram(wire.dg_type_ping, 0, 0, 0, 0, &ts);
        s.sendRaw(s.dg_buf[0..dglen]);
    }

    fn sendBye(s: *Sender) void {
        const dglen = s.buildDatagram(wire.dg_type_bye, 0, 0, 0, 0, &[_]u8{});
        s.sendRaw(s.dg_buf[0..dglen]);
    }

    fn livenessDead(s: *Sender, now: i64) bool {
        const window: i64 = if (s.established) liveness_ns else s.cfg.startup_grace_ns;
        return now - s.last_recv_ns > window;
    }

    // drainControl reads any pending control datagrams (non-blocking) and handles them.
    fn drainControl(s: *Sender, now: i64) void {
        while (true) {
            const n = sysRecv(s.fd, s.rx_buf) catch return;
            if (n == 0) return;
            s.handleControl(s.rx_buf[0..n], now);
        }
    }

    fn handleControl(s: *Sender, dg: []const u8, now: i64) void {
        const h = wire.Header.decode(dg) catch return;
        if (h.token != s.cfg.token) return; // off-path / cross-route
        var body_buf: [2048]u8 = undefined;
        var body: []const u8 = dg[wire.header_len..];
        if (s.cfg.encrypt) {
            const got = aead.open(&body_buf, dg[wire.header_len..], dg[0..wire.header_len], keys.nonce(s.cfg.recv_prefix, h.seq), s.cfg.recv_key) catch return;
            body = body_buf[0..got];
        }
        // authenticated: refresh liveness + establish on media/ping
        s.last_recv_ns = now;
        switch (h.typ) {
            wire.dg_type_ping => {
                s.established = true;
                const dglen = s.buildDatagram(wire.dg_type_pong, 0, 0, 0, 0, body);
                s.sendRaw(s.dg_buf[0..dglen]);
            },
            wire.dg_type_pong => {},
            wire.dg_type_nack => s.handleNack(body),
            wire.dg_type_bye => s.stop.store(true, .release),
            wire.dg_type_media => s.handleMeta(h, body, now),
            else => {},
        }
    }

    // handleMeta handles the receiver's stream-0 meta frames on a native route (the Go supervisor does
    // not read meta there): PLI (NACK pli=true) -> on_need_idr (rate-limited); MetaRate -> the polled
    // rate hint (Go applies it to the encoder, rm_tx_rate_hint); MetaSync -> SyncPong in the media-
    // clock domain (pairwise clock estimate + RTT on the receiver). Reports are ignored.
    fn handleMeta(s: *Sender, h: wire.Header, body: []const u8, now: i64) void {
        if (h.stream != 0 or h.frag_count != 1) return;
        const f = wire.parseFrameForTest(body) catch return;
        if (f.kind != 0) return;
        const m = parseMeta(f.payload);
        if (std.mem.eql(u8, m.t, "nack")) {
            if (!m.pli) return;
            if (s.last_pli_ns != 0 and now - s.last_pli_ns < 250 * std.time.ns_per_ms) return;
            s.last_pli_ns = now;
            _ = s.s_plis.fetchAdd(1, .monotonic);
            if (s.on_idr) |cb| cb(s.idr_user);
        } else if (std.mem.eql(u8, m.t, "rate")) {
            s.hint_br.store(m.br, .monotonic);
            s.hint_fps.store(m.fps, .monotonic);
            s.hint_maxh.store(m.maxh, .monotonic);
            s.hint_q.store(m.q, .monotonic);
            _ = s.hint_gen.fetchAdd(1, .release); // publish after the fields
        } else if (std.mem.eql(u8, m.t, "sync")) {
            const t2 = now + s.cfg.clock_offset_ns;
            var js: [160]u8 = undefined;
            const t3 = nowNs() + s.cfg.clock_offset_ns;
            const json = std.fmt.bufPrint(&js, "{{\"t\":\"syncr\",\"id\":{d},\"t1\":{d},\"t2\":{d},\"t3\":{d}}}", .{ m.id, m.t1, t2, t3 }) catch return;
            s.sendMeta(json, t3);
        }
    }

    // sendMeta sends one single-fragment stream-0 meta frame (own seq space, not the media frame_seq).
    fn sendMeta(s: *Sender, json: []const u8, pts: i64) void {
        var fr: [wire.frame_header_len + 192]u8 = undefined;
        if (json.len > 192) return;
        const n = wire.marshalFrame(&fr, .{ .stream = 0, .kind = 0, .codec = 0, .flags = 0, .seq = s.meta_seq, .pts = pts }, json);
        const dglen = s.buildDatagram(wire.dg_type_media, 0, s.meta_seq, 0, 1, fr[0..n]);
        s.sendRaw(s.dg_buf[0..dglen]);
        s.meta_seq +%= 1;
    }

    /// rateHint returns the last receiver rate hint (gen 0 = none yet). Lock-free; any thread.
    pub fn rateHint(s: *Sender) RateHint {
        while (true) {
            const g = s.hint_gen.load(.acquire);
            const r = RateHint{ .gen = g, .max_bitrate_kbps = s.hint_br.load(.monotonic), .max_fps = s.hint_fps.load(.monotonic), .max_height = s.hint_maxh.load(.monotonic), .quality = s.hint_q.load(.monotonic) };
            if (s.hint_gen.load(.acquire) == g) return r;
        }
    }
    fn handleNack(s: *Sender, body: []const u8) void {
        _ = s.s_nacks.fetchAdd(1, .monotonic);
        if (body.len < 2) return;
        var cnt = std.mem.readInt(u16, body[0..2], .big);
        if (cnt > 85) cnt = 85;
        var p: usize = 2;
        var k: u16 = 0;
        while (k < cnt and p + 8 <= body.len) : (k += 1) {
            const stream = std.mem.readInt(u16, body[p..][0..2], .big);
            const frame_seq = std.mem.readInt(u32, body[p + 2 ..][0..4], .big);
            const frag_idx = std.mem.readInt(u16, body[p + 6 ..][0..2], .big);
            p += 8;
            if (s.ring.get(stream, frame_seq, frag_idx)) |orig| {
                s.sendRaw(orig); // resend the exact datagram (same seq → receiver dedups replays)
                _ = s.s_retx.fetchAdd(1, .monotonic);
            }
        }
    }
};

pub fn nowNs() i64 {
    return hrtimer.nowMono();
}

fn sleepNs(ns: i64) void {
    if (ns <= 0) return;
    hrtimer.sleepUntil(hrtimer.nowMono() + ns);
}

// setThisThreadRealtime best-effort elevates the worker (Linux SCHED_RR; Windows TIME_CRITICAL).
fn setThisThreadRealtime() void {
    if (builtin.os.tag == .linux) {
        const param = linux.sched_param{ .priority = 10 };
        _ = linux.sched_setscheduler(0, .{ .mode = .RR }, &param);
    } else if (builtin.os.tag == .windows) {
        _ = win.SetThreadPriority(win.GetCurrentThread(), 15);
    }
}

const win = struct {
    extern "kernel32" fn GetCurrentThread() callconv(.winapi) *anyopaque;
    extern "kernel32" fn SetThreadPriority(t: *anyopaque, prio: i32) callconv(.winapi) i32;
};