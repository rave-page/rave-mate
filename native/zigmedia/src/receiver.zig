//! Native receiver. Single worker thread: non-blocking socket RX (Linux syscalls / Winsock, large
//! SO_RCVBUF) with a first-byte monotonic timestamp per datagram -> authenticate/decode (wire core)
//! -> reassemble -> adaptive PTS-locked playout (playout.zig) -> deliver complete frames in cadence.
//! Deadline-aware recovery: a PROVEN hole is NACKed at once (retransmit lands inside the playout
//! margin); a frame still missing past the max margin is skipped, an inter-coded stream waits for a
//! keyframe and a PLI (stream-0 meta NACK, pli=true - same wire as the Go jitter buffer) is sent,
//! rate-limited; never a keyframe request while the frame can still arrive. Keepalive pings keep the
//! sender's liveness; the sender's pings are ponged. Allocation-free after open; every pool bounded.
const std = @import("std");
const keys = @import("keys.zig");
const aead = @import("aead.zig");
const wire = @import("wire.zig");
const sock = @import("sock.zig");
const reasm_mod = @import("reassembler.zig");
const Reassembler = reasm_mod.Reassembler;
const playout_mod = @import("playout.zig");
const Playout = playout_mod.Playout;
const hrtimer = @import("hrtimer.zig");
const Hist = @import("histogram.zig").Hist;
const spinLock = @import("auqueue.zig").spinLock;

pub const RxConfig = struct {
    encrypt: bool,
    token: u32,
    open_key: [32]u8, // opens the sender's media (receiver's recv direction)
    open_prefix: u32,
    send_key: [32]u8, // seals the receiver's control (keepalive/pong/NACK/PLI) to the sender
    send_prefix: u32,
    body_max: usize = 1200,
    fps: f64 = 60,
    max_frame_bytes: usize = 4 << 20,
    preset: playout_mod.Preset = playout_mod.balanced,
    stream: u16 = 1, // media stream the PLI names
    initial_seq: u64 = 0, // first control datagram seq: CONTINUE the Go dialer conn's seq space (a Go
    //                        sender's replay window rejects a restart at 0)
};

const keepalive_ns: i64 = 100 * std.time.ns_per_ms;
const poll_ns: i64 = 2 * std.time.ns_per_ms; // max idle before re-polling the socket
const nack_interval_ns: i64 = 6 * std.time.ns_per_ms; // re-NACK a frame no faster than ~1 LAN RTT
const pli_min_gap_ns: i64 = 250 * std.time.ns_per_ms;
const nack_max = 85;
const rx_batch = 64; // datagrams per drain before releases/NACKs get a turn

/// Stats is the published snapshot (Go samples it at 1 Hz, off the realtime thread).
pub const Stats = struct {
    datagrams: u64 = 0,
    delivered: u64 = 0,
    nacks_sent: u64 = 0,
    plis_sent: u64 = 0,
    reasm_drops: u64 = 0,
    slot_drops: u64 = 0,
    // congestion-controller inputs (same semantics as medialink.UDPStats):
    loss_pct: f64 = 0, // datagram loss over the last ~1 s window (seq gaps; retransmits refill)
    owd_ns: i64 = 0, // smoothed queuing delay above the windowed-min transit
    owd_jitter_ns: i64 = 0, // |transit delta| EWMA
    rtt_ns: i64 = 0, // keepalive ping -> pong
    metas_sent: u64 = 0,
    playout: playout_mod.Stats = undefined,
};

pub const FrameCb = *const fn (user: ?*anyopaque, ptr: [*]const u8, len: usize, pts: i64, key: bool) callconv(.c) void;

pub const Receiver = struct {
    alloc: std.mem.Allocator,
    fd: sock.Fd,
    cfg: RxConfig,
    reasm: Reassembler,
    playout: Playout,
    pbuf: []playout_mod.Item,

    // frame slots: hold a reassembled AU from reasm-complete until playout release.
    slot_buf: [][]u8,
    slot_len: []usize,
    slot_free: []bool,

    peer: sock.Addr4 = .{},
    have_peer: bool = false,
    seq: u64 = 0,
    meta_seq: u32 = 0,
    // transport telemetry (worker-owned)
    dg_hi: u64 = 0,
    dg_have: bool = false,
    win_hi0: u64 = 0,
    win_got: u64 = 0,
    win_start: i64 = 0,
    loss_pct: f64 = 0,
    owd_min_cur: i64 = 0,
    owd_min_prev: i64 = 0,
    owd_bucket_at: i64 = 0,
    owd_have: bool = false,
    owd_ns: i64 = 0,
    owd_jit: i64 = 0,
    last_transit: i64 = 0,
    rtt_ns: i64 = 0,
    // meta mailbox: Go-originated control (MetaRate, receiver reports, sync pings) sent by the worker
    // so every control datagram uses ONE seq/nonce counter. Bounded: 16 x 256 B, newest dropped if full.
    mbox_mu: std.atomic.Mutex = .unlocked,
    mbox: [16][256]u8 = undefined,
    mbox_len: [16]usize = [_]usize{0} ** 16,
    mbox_n: usize = 0,
    metas_sent: std.atomic.Value(u64) = .init(0),
    last_ping: i64 = 0,
    last_pli: i64 = 0,
    rx_buf: []u8,
    dg_buf: []u8,

    thread: ?std.Thread = null,
    stop: std.atomic.Value(bool) = .init(false),
    delivered: std.atomic.Value(u64) = .init(0),
    net_marks: std.atomic.Value(u64) = .init(0),
    nacks_sent: std.atomic.Value(u64) = .init(0),
    plis_sent: std.atomic.Value(u64) = .init(0),
    slot_drops: std.atomic.Value(u64) = .init(0),
    publish: Hist = .{}, // release (publish) interval histogram - the acceptance metric
    stats_mu: std.atomic.Mutex = .unlocked,
    stats_snap: Stats = .{},

    on_frame: ?FrameCb = null,
    user: ?*anyopaque = null,

    // test hooks: drop the datagram (drop_frame, drop_frag) ONCE (NIC loss; the retransmit passes);
    // stall_at_frame: the worker sleeps stall_ns once after reassembling that frame (host stall).
    drop_frame: i64 = -1,
    drop_frag: i32 = -1,
    stall_at_frame: i64 = -1,
    stall_ns: i64 = 0,

    const n_slots = 48;

    /// open starts the receiver worker on an already-bound UDP socket (ownership stays with the
    /// caller). cb is installed before the worker starts.
    pub fn open(alloc: std.mem.Allocator, fd: sock.Fd, cfg: RxConfig, cb: ?FrameCb, user: ?*anyopaque) !*Receiver {
        return openPeer(alloc, fd, cfg, null, cb, user);
    }

    /// openPeer is open with the sender's address known up front (control can flow before media).
    pub fn openPeer(alloc: std.mem.Allocator, fd: sock.Fd, cfg: RxConfig, peer: ?sock.Addr4, cb: ?FrameCb, user: ?*anyopaque) !*Receiver {
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
            .playout = Playout.initPreset(pbuf, cfg.fps, cfg.preset),
            .pbuf = pbuf,
            .slot_buf = sbuf,
            .slot_len = slen,
            .slot_free = sfree,
            .seq = cfg.initial_seq,
            .rx_buf = try alloc.alloc(u8, 2048),
            .dg_buf = try alloc.alloc(u8, 2 + nack_max * 8 + wire.header_len + 128), // fits a full NACK / PLI
            .on_frame = cb,
            .user = user,
        };
        if (peer) |pa| {
            r.peer = pa;
            r.have_peer = true;
        }
        sock.setBufs(fd, 16 << 20, 0); // large SO_RCVBUF: a receiver-host stall must not overflow it
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

    /// stats returns the last snapshot the worker published (every 250 ms).
    pub fn stats(r: *Receiver) Stats {
        spinLock(&r.stats_mu);
        defer r.stats_mu.unlock();
        return r.stats_snap;
    }

    fn worker(r: *Receiver) void {
        var last_stats: i64 = 0;
        while (!r.stop.load(.acquire)) {
            const now = hrtimer.nowMono();
            const more = r.drainSocket();
            r.scanAndNack(now);
            r.drainPlayout();
            r.keepalive(now);
            r.drainMailbox(now);
            if (now - last_stats >= 250 * std.time.ns_per_ms) {
                last_stats = now;
                r.publishStats(now);
            }
            if (more) continue; // backlog (post-stall burst): keep draining, releases interleaved
            var wake = now + poll_ns;
            wake = @min(wake, r.playout.nextWake());
            wake = @min(wake, r.last_ping + keepalive_ns);
            hrtimer.sleepUntil(wake);
        }
        r.publishStats(hrtimer.nowMono());
    }

    fn publishStats(r: *Receiver, now: i64) void {
        const s = Stats{
            .datagrams = r.net_marks.load(.monotonic),
            .delivered = r.delivered.load(.monotonic),
            .nacks_sent = r.nacks_sent.load(.monotonic),
            .plis_sent = r.plis_sent.load(.monotonic),
            .reasm_drops = r.reasm.droppedCount(),
            .slot_drops = r.slot_drops.load(.monotonic),
            .loss_pct = r.loss_pct,
            .owd_ns = r.owd_ns,
            .owd_jitter_ns = r.owd_jit,
            .rtt_ns = r.rtt_ns,
            .metas_sent = r.metas_sent.load(.monotonic),
            .playout = r.playout.stats(now),
        };
        spinLock(&r.stats_mu);
        r.stats_snap = s;
        r.stats_mu.unlock();
    }

    // drainSocket reads up to rx_batch datagrams; true = more may be pending. Bounded so a post-stall
    // burst (thousands of datagrams) never delays a due release by its whole decrypt time.
    fn drainSocket(r: *Receiver) bool {
        var k: usize = 0;
        while (k < rx_batch) : (k += 1) {
            var src: sock.Addr4 = .{};
            const n = sock.recvFrom(r.fd, r.rx_buf, &src) catch return false;
            const t = hrtimer.nowMono(); // first bytes of this datagram off the socket
            _ = r.net_marks.fetchAdd(1, .monotonic);
            if (!r.have_peer) {
                r.peer = src;
                r.have_peer = true;
            }
            r.handleDatagram(r.rx_buf[0..n], t);
        }
        return true;
    }

    fn handleDatagram(r: *Receiver, dg: []const u8, now: i64) void {
        const h = wire.Header.decode(dg) catch return;
        if (h.token != r.cfg.token) return;
        if (r.cfg.encrypt) {
            var pt: [2048]u8 = undefined;
            const n = aead.open(&pt, dg[wire.header_len..], dg[0..wire.header_len], keys.nonce(r.cfg.open_prefix, h.seq), r.cfg.open_key) catch return;
            r.handleBody(h, pt[0..n], now);
        } else {
            r.handleBody(h, dg[wire.header_len..], now);
        }
    }

    fn handleBody(r: *Receiver, h: wire.Header, body: []const u8, now: i64) void {
        r.noteSeq(h.seq, now);
        switch (h.typ) {
            wire.dg_type_media => {
                if (r.drop_frame >= 0 and h.frame_seq == @as(u32, @intCast(r.drop_frame)) and h.frag_idx == @as(u16, @intCast(r.drop_frag))) {
                    r.drop_frame = -1; // simulated single NIC drop; the NACK-driven retransmit passes
                    return;
                }
                r.reasm.now_hint = now;
                const whole = r.reasm.accept(h.stream, h.frame_seq, h.frag_idx, h.frag_count, body) orelse return;
                const f = wire.parseFrameForTest(whole) catch return;
                if (f.kind != 1) return; // video only
                r.noteTransit(now - f.pts, now);
                if (r.stall_at_frame >= 0 and f.seq == @as(u32, @intCast(r.stall_at_frame))) {
                    r.stall_at_frame = -1; // simulated receiver-host stall: nothing is read meanwhile
                    hrtimer.sleepUntil(hrtimer.nowMono() + r.stall_ns);
                }
                const si = r.freeSlot() orelse {
                    _ = r.slot_drops.fetchAdd(1, .monotonic); // bounded: no slot -> drop
                    return;
                };
                @memcpy(r.slot_buf[si][0..f.payload.len], f.payload);
                r.slot_len[si] = f.payload.len;
                r.slot_free[si] = false;
                if (!r.playout.push(f.seq, f.pts, (f.flags & 0x01) != 0, si, now)) r.slot_free[si] = true;
            },
            wire.dg_type_ping => r.sendCtrl(wire.dg_type_pong, 0, 0, body),
            wire.dg_type_pong => if (body.len >= 8) { // our keepalive echoed: RTT
                const sent: i64 = @bitCast(std.mem.readInt(u64, body[0..8], .big));
                const rtt = hrtimer.nowMono() - sent;
                if (rtt >= 0 and rtt < 5_000_000_000) r.rtt_ns = if (r.rtt_ns == 0) rtt else r.rtt_ns + @divTrunc(rtt - r.rtt_ns, 8);
            },
            else => {},
        }
    }

    fn drainPlayout(r: *Receiver) void {
        while (true) {
            // Re-evaluated per release: a gap appears the moment its predecessor is released.
            r.playout.recovering = r.playout.n > 0 and r.reasm.incompleteBefore(r.playout.next_seq, r.playout.buf[0].seq);
            const it = r.playout.due(hrtimer.nowMono()) orelse break;
            const si: usize = @intCast(it.tag);
            r.publish.mark(hrtimer.nowMono());
            if (r.on_frame) |cb| cb(r.user, r.slot_buf[si].ptr, r.slot_len[si], it.pts_ns, it.key);
            r.slot_free[si] = true;
            _ = r.delivered.fetchAdd(1, .monotonic);
        }
        if (r.playout.pli_pending) {
            r.playout.pli_pending = false;
            r.reclaimSlots(); // frames skipped by the gap policy left their slots behind
            r.sendPLI(hrtimer.nowMono());
        }
    }
    // reclaimSlots frees every used slot no playout item references (bounded scan).
    fn reclaimSlots(r: *Receiver) void {
        for (r.slot_free, 0..) |*fr, i| {
            if (fr.*) continue;
            var live = false;
            for (r.playout.buf[0..r.playout.n]) |it| {
                if (it.tag == i) {
                    live = true;
                    break;
                }
            }
            if (!live) fr.* = true;
        }
    }

    fn freeSlot(r: *Receiver) ?usize {
        for (r.slot_free, 0..) |fr, i| if (fr) return i;
        r.reclaimSlots();
        for (r.slot_free, 0..) |fr, i| if (fr) return i;
        return null;
    }

    fn scanAndNack(r: *Receiver, now: i64) void {
        if (!r.have_peer) return;
        var items: [nack_max]reasm_mod.NackItem = undefined;
        const k = r.reasm.scanMissing(&items, now, nack_interval_ns);
        if (k == 0) return;
        var pl: [2 + nack_max * 8]u8 = undefined;
        std.mem.writeInt(u16, pl[0..2], @intCast(k), .big);
        var off: usize = 2;
        for (items[0..k]) |it| {
            std.mem.writeInt(u16, pl[off..][0..2], it.stream, .big);
            std.mem.writeInt(u32, pl[off + 2 ..][0..4], it.frame_seq, .big);
            std.mem.writeInt(u16, pl[off + 6 ..][0..2], it.frag_idx, .big);
            off += 8;
        }
        r.sendCtrl(wire.dg_type_nack, 0, 0, pl[0..off]);
        _ = r.nacks_sent.fetchAdd(1, .monotonic);
    }

    // sendPLI sends a keyframe request as a stream-0 meta frame (medialink NACK, pli=true), rate-limited.
    pub fn sendPLI(r: *Receiver, now: i64) void {
        if (!r.have_peer or (r.last_pli != 0 and now - r.last_pli < pli_min_gap_ns)) return;
        r.last_pli = now;
        var js: [96]u8 = undefined;
        const json = std.fmt.bufPrint(&js, "{{\"t\":\"nack\",\"stream\":{d},\"from\":1,\"to\":0,\"pli\":true}}", .{r.cfg.stream}) catch return;
        var fr: [wire.frame_header_len + 96]u8 = undefined;
        const n = wire.marshalFrame(&fr, .{ .stream = 0, .kind = 0, .codec = 0, .flags = 0, .seq = r.meta_seq, .pts = now }, json);
        r.sendCtrl(wire.dg_type_media, 0, r.meta_seq, fr[0..n]);
        r.meta_seq +%= 1;
        _ = r.plis_sent.fetchAdd(1, .monotonic);
    }

    // noteSeq folds an authenticated datagram's seq into the ~1 s loss window (seq gaps / expected).
    fn noteSeq(r: *Receiver, seq: u64, now: i64) void {
        if (!r.dg_have) {
            r.dg_have = true;
            r.dg_hi = seq;
            r.win_hi0 = seq;
            r.win_start = now;
        }
        if (seq > r.dg_hi) r.dg_hi = seq;
        r.win_got += 1;
        if (now - r.win_start >= 1_000_000_000) {
            const expected = r.dg_hi - r.win_hi0;
            r.loss_pct = if (expected > 0 and r.win_got < expected) @as(f64, @floatFromInt(expected - r.win_got)) * 100 / @as(f64, @floatFromInt(expected)) else 0;
            r.win_hi0 = r.dg_hi;
            r.win_got = 0;
            r.win_start = now;
        }
    }

    // noteTransit maintains OWD as queuing delay above a 2 x 2 s windowed-min transit (offset-free,
    // the congestion controller's delay-gradient input) + |transit delta| jitter.
    fn noteTransit(r: *Receiver, transit: i64, now: i64) void {
        if (!r.owd_have) {
            r.owd_have = true;
            r.owd_min_cur = transit;
            r.owd_min_prev = transit;
            r.owd_bucket_at = now;
            r.last_transit = transit;
        }
        if (now - r.owd_bucket_at >= 2_000_000_000) {
            r.owd_min_prev = r.owd_min_cur;
            r.owd_min_cur = transit;
            r.owd_bucket_at = now;
        } else if (transit < r.owd_min_cur) r.owd_min_cur = transit;
        const q = transit - @min(r.owd_min_cur, r.owd_min_prev);
        r.owd_ns += @divTrunc(q - r.owd_ns, 8);
        const d = if (transit > r.last_transit) transit - r.last_transit else r.last_transit - transit;
        r.owd_jit += @divTrunc(d - r.owd_jit, 16);
        r.last_transit = transit;
    }

    /// sendMeta queues one stream-0 meta frame (Go-originated JSON: MetaRate, reports, sync) for the
    /// worker. false = mailbox full / too large (dropped, counted by the caller).
    pub fn sendMeta(r: *Receiver, json: []const u8) bool {
        if (json.len > 256) return false;
        spinLock(&r.mbox_mu);
        defer r.mbox_mu.unlock();
        if (r.mbox_n == r.mbox.len) return false;
        @memcpy(r.mbox[r.mbox_n][0..json.len], json);
        r.mbox_len[r.mbox_n] = json.len;
        r.mbox_n += 1;
        return true;
    }

    fn drainMailbox(r: *Receiver, now: i64) void {
        var tmp: [16][256]u8 = undefined;
        var lens: [16]usize = undefined;
        spinLock(&r.mbox_mu);
        const n = r.mbox_n;
        for (0..n) |i| {
            lens[i] = r.mbox_len[i];
            @memcpy(tmp[i][0..lens[i]], r.mbox[i][0..lens[i]]);
        }
        r.mbox_n = 0;
        r.mbox_mu.unlock();
        for (0..n) |i| {
            var fr: [wire.frame_header_len + 256]u8 = undefined;
            const fl = wire.marshalFrame(&fr, .{ .stream = 0, .kind = 0, .codec = 0, .flags = 0, .seq = r.meta_seq, .pts = now }, tmp[i][0..lens[i]]);
            r.sendCtrl(wire.dg_type_media, 0, r.meta_seq, fr[0..fl]);
            r.meta_seq +%= 1;
            _ = r.metas_sent.fetchAdd(1, .monotonic);
        }
    }

    fn keepalive(r: *Receiver, now: i64) void {
        if (!r.have_peer or now - r.last_ping < keepalive_ns) return;
        r.last_ping = now;
        var ts: [8]u8 = undefined;
        std.mem.writeInt(u64, &ts, @bitCast(now), .big);
        r.sendCtrl(wire.dg_type_ping, 0, 0, &ts);
    }

    fn sendCtrl(r: *Receiver, typ: u8, stream: u16, frame_seq: u32, payload: []const u8) void {
        if (!r.have_peer) return;
        const flags: u8 = if (r.cfg.encrypt) wire.flag_aead else 0;
        var hh = wire.Header{ .typ = typ, .flags = flags, .token = r.cfg.token, .seq = r.seq };
        if (typ == wire.dg_type_media) {
            hh.stream = stream;
            hh.frame_seq = frame_seq;
            hh.frag_idx = 0;
            hh.frag_count = 1;
        }
        hh.encode(r.dg_buf[0..wire.header_len]);
        var total: usize = wire.header_len;
        if (r.cfg.encrypt) {
            total += aead.seal(r.dg_buf[wire.header_len..], payload, r.dg_buf[0..wire.header_len], keys.nonce(r.cfg.send_prefix, r.seq), r.cfg.send_key);
        } else {
            @memcpy(r.dg_buf[wire.header_len..][0..payload.len], payload);
            total += payload.len;
        }
        r.seq +%= 1;
        _ = sock.sendTo(r.fd, r.dg_buf[0..total], &r.peer);
    }
};
