//! Native receiver (increment 3). Single worker thread: non-blocking socket RX → authenticate/decode
//! datagram (wire core) → reassemble → PTS-locked playout → deliver complete frames to the decoder.
//! Also sends keepalive pings so the sender's liveness holds, and pongs the sender's pings. Mirrors the
//! sender's single-worker model (no locks on reasm/playout). The socket layer here is Linux raw
//! syscalls for the loopback milestone; the Windows build swaps in Winsock + CreateWaitableTimerEx
//! (increment 3 platform wiring) behind the same pipeline. First-byte arrival is timestamped per
//! datagram (the native equivalent of the Go `net` mark).
const std = @import("std");
const linux = std.os.linux;
const keys = @import("keys.zig");
const aead = @import("aead.zig");
const wire = @import("wire.zig");
const Reassembler = @import("reassembler.zig").Reassembler;
const playout_mod = @import("playout.zig");
const Playout = playout_mod.Playout;
const hrtimer = @import("hrtimer.zig");

const SockaddrIn = extern struct {
    family: u16 = linux.AF.INET,
    port: u16 = 0,
    addr: u32 = 0,
    zero: [8]u8 = [_]u8{0} ** 8,
};

pub const RxConfig = struct {
    encrypt: bool,
    token: u32,
    open_key: [32]u8, // opens the sender's media (receiver's recv direction)
    open_prefix: u32,
    send_key: [32]u8, // seals the receiver's control (keepalive/pong) to the sender
    send_prefix: u32,
    body_max: usize = 1200,
    fps: f64 = 60,
    max_frame_bytes: usize = 4 << 20,
};

const keepalive_ns: i64 = 100 * std.time.ns_per_ms;
const poll_ns: i64 = 2 * std.time.ns_per_ms; // max idle before re-polling the socket

pub const Receiver = struct {
    alloc: std.mem.Allocator,
    fd: i32,
    cfg: RxConfig,
    reasm: Reassembler,
    playout: Playout,
    pbuf: []playout_mod.Item,

    // frame slots: hold a reassembled AU from reasm-complete until playout release.
    slot_buf: [][]u8,
    slot_len: []usize,
    slot_free: []bool,

    peer: SockaddrIn = .{},
    have_peer: bool = false,
    seq: u64 = 0,
    last_ping: i64 = 0,
    rx_buf: []u8,
    dg_buf: []u8,

    thread: ?std.Thread = null,
    stop: std.atomic.Value(bool) = .init(false),
    delivered: std.atomic.Value(u64) = .init(0),
    net_marks: std.atomic.Value(u64) = .init(0),

    on_frame: ?*const fn (user: ?*anyopaque, ptr: [*]const u8, len: usize, pts: i64) callconv(.c) void = null,
    user: ?*anyopaque = null,

    const n_slots = 48;

    pub fn open(alloc: std.mem.Allocator, fd: i32, cfg: RxConfig) !*Receiver {
        const r = try alloc.create(Receiver);
        errdefer alloc.destroy(r);
        const pbuf = try alloc.alloc(playout_mod.Item, n_slots);
        const sbuf = try alloc.alloc([]u8, n_slots);
        const slen = try alloc.alloc(usize, n_slots);
        const sfree = try alloc.alloc(bool, n_slots);
        for (sbuf, 0..) |*s, i| {
            s.* = try alloc.alloc(u8, cfg.max_frame_bytes);
            slen[i] = 0;
            sfree[i] = true;
        }
        r.* = .{
            .alloc = alloc,
            .fd = fd,
            .cfg = cfg,
            .reasm = try Reassembler.init(alloc, 16, cfg.body_max, cfg.max_frame_bytes),
            .playout = Playout.init(pbuf, cfg.fps),
            .pbuf = pbuf,
            .slot_buf = sbuf,
            .slot_len = slen,
            .slot_free = sfree,
            .rx_buf = try alloc.alloc(u8, 2048),
            .dg_buf = try alloc.alloc(u8, 64),
        };
        r.thread = try std.Thread.spawn(.{}, worker, .{r});
        return r;
    }

    pub fn close(r: *Receiver) void {
        r.stop.store(true, .release);
        if (r.thread) |t| t.join();
        r.reasm.deinit();
        for (r.slot_buf) |s| r.alloc.free(s);
        r.alloc.free(r.slot_buf);
        r.alloc.free(r.slot_len);
        r.alloc.free(r.slot_free);
        r.alloc.free(r.pbuf);
        r.alloc.free(r.rx_buf);
        r.alloc.free(r.dg_buf);
        r.alloc.destroy(r);
    }

    pub fn deliveredCount(r: *Receiver) u64 {
        return r.delivered.load(.monotonic);
    }

    fn worker(r: *Receiver) void {
        while (!r.stop.load(.acquire)) {
            const now = hrtimer.nowMono();
            r.drainSocket(now);
            r.drainPlayout(now);
            r.keepalive(now);
            // Sleep until the next event: the playout deadline, the next keepalive, or a short poll.
            var wake = now + poll_ns;
            const nd = r.playout.nextDeadline();
            if (nd < wake) wake = nd;
            const ka = r.last_ping + keepalive_ns;
            if (ka < wake) wake = ka;
            hrtimer.sleepUntil(wake);
        }
    }

    fn drainSocket(r: *Receiver, now: i64) void {
        while (true) {
            var src: SockaddrIn = undefined;
            var slen: linux.socklen_t = @sizeOf(SockaddrIn);
            const rn = linux.recvfrom(r.fd, r.rx_buf.ptr, r.rx_buf.len, linux.MSG.DONTWAIT, @ptrCast(&src), &slen);
            switch (linux.errno(rn)) {
                .SUCCESS => {},
                .AGAIN => return,
                else => return,
            }
            _ = r.net_marks.fetchAdd(1, .monotonic); // first bytes of this datagram off the socket
            if (!r.have_peer) {
                r.peer = src;
                r.have_peer = true;
            }
            r.handleDatagram(r.rx_buf[0..rn], now);
        }
    }

    fn handleDatagram(r: *Receiver, dg: []const u8, now: i64) void {
        const h = wire.Header.decode(dg) catch return;
        if (h.token != r.cfg.token) return;
        const body: []const u8 = dg[wire.header_len..];
        if (r.cfg.encrypt) {
            var pt: [2048]u8 = undefined;
            const n = aead.open(&pt, dg[wire.header_len..], dg[0..wire.header_len], keys.nonce(r.cfg.open_prefix, h.seq), r.cfg.open_key) catch return;
            r.handleBody(h, pt[0..n], now);
        } else {
            r.handleBody(h, body, now);
        }
    }

    fn handleBody(r: *Receiver, h: wire.Header, body: []const u8, now: i64) void {
        switch (h.typ) {
            wire.dg_type_media => {
                const whole = r.reasm.accept(h.stream, h.frame_seq, h.frag_idx, h.frag_count, body) orelse return;
                const f = wire.parseFrameForTest(whole) catch return;
                // copy the AU into a free slot, push to playout keyed by the slot index.
                const si = r.freeSlot() orelse return; // no slot: drop (bounded)
                @memcpy(r.slot_buf[si][0..f.payload.len], f.payload);
                r.slot_len[si] = f.payload.len;
                r.slot_free[si] = false;
                if (!r.playout.push(f.seq, f.pts, si, now)) {
                    r.slot_free[si] = true; // dropped by playout (dup/too-old)
                }
            },
            wire.dg_type_ping => r.sendCtrl(wire.dg_type_pong, body),
            else => {},
        }
    }

    fn drainPlayout(r: *Receiver, now: i64) void {
        while (r.playout.due(now)) |it| {
            const si: usize = @intCast(it.tag);
            if (r.on_frame) |cb| cb(r.user, r.slot_buf[si].ptr, r.slot_len[si], it.pts_ns);
            r.slot_free[si] = true;
            _ = r.delivered.fetchAdd(1, .monotonic);
        }
    }

    fn freeSlot(r: *Receiver) ?usize {
        for (r.slot_free, 0..) |fr, i| if (fr) return i;
        return null;
    }

    fn keepalive(r: *Receiver, now: i64) void {
        if (!r.have_peer or now - r.last_ping < keepalive_ns) return;
        r.last_ping = now;
        var ts: [8]u8 = undefined;
        std.mem.writeInt(u64, &ts, @bitCast(now), .big);
        r.sendCtrl(wire.dg_type_ping, &ts);
    }

    fn sendCtrl(r: *Receiver, typ: u8, payload: []const u8) void {
        if (!r.have_peer) return;
        const flags: u8 = if (r.cfg.encrypt) wire.flag_aead else 0;
        const hh = wire.Header{ .typ = typ, .flags = flags, .token = r.cfg.token, .seq = r.seq };
        hh.encode(r.dg_buf[0..wire.header_len]);
        var total: usize = wire.header_len;
        if (r.cfg.encrypt) {
            total += aead.seal(r.dg_buf[wire.header_len..], payload, r.dg_buf[0..wire.header_len], keys.nonce(r.cfg.send_prefix, r.seq), r.cfg.send_key);
        } else {
            @memcpy(r.dg_buf[wire.header_len..][0..payload.len], payload);
            total += payload.len;
        }
        r.seq +%= 1;
        _ = linux.sendto(r.fd, r.dg_buf.ptr, total, 0, @ptrCast(&r.peer), @sizeOf(SockaddrIn));
    }
};
