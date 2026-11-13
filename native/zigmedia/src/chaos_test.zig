//! Chaos acceptance suite for the native playout (owner directive 2026-11-12). Deterministic
//! 10-minute traces at 4K60-realistic sizes (IDR 400-900 kB every 60 frames, P 80-150 kB) through a
//! link model -> the REAL Playout (balanced preset) with an event-driven release clock.
//! Link model: sender TX queue (bounded bytes, paced at 8x target capped by the link), per-datagram
//! loss recovered by NACK -> retransmit (one RTT per loss round), per-frame jitter, receiver arrival
//! stalls (everything due inside the stall lands at its end), optional link-rate cap window.
//! Profiles: "this rig", "wifi-ish" (1.5 % loss, 20 ms jitter), "congested" (link 50 Mbps for 5 s
//! under a ~60 Mbps stream: 6 MB backlog the 16 MiB TX queue must absorb; frames play late, none drop).
//! Without a rate cut a longer/deeper deficit overflows any bounded queue by design (drop-oldest
//! non-key); the congestion controller (Go policy) owns that case.
const std = @import("std");
const po = @import("playout.zig");

const Profile = struct {
    name: []const u8,
    dur_ns: i64 = 600_000_000_000,
    fps: f64 = 60,
    target_bps: f64 = 60e6, // = the realistic AU sizes' mean rate (~59.5 Mbps)
    link_bps: f64 = 1e9,
    cap_bps: f64 = 0, // link cap inside [cap_at, cap_at+cap_dur) (0 = none)
    cap_at: i64 = 0,
    cap_dur: i64 = 0,
    loss: f64 = 0, // per-datagram
    rtt_ns: i64 = 1_000_000,
    jitter_ns: i64 = 0, // uniform per frame
    stalls: bool = false, // receiver stalls 20-140 ms at 0.5-2/min
    queue_bytes: usize = 16 << 20, // sender TX queue bound
};

const Result = struct {
    frames: usize = 0,
    released: usize = 0,
    sender_drops: usize = 0,
    bad_gaps: usize = 0, // release intervals > 1.5 frames after convergence
    max_gap_ns: i64 = 0,
    plis: usize = 0,
    max_margin: i64 = 0,
    added_p50: i64 = 0,
    added_p99: i64 = 0,
    stats: po.Stats = undefined,
};

const Arr = struct { at: i64, seq: u32, pts: i64, key: bool };

fn frameBytes(i: usize) usize {
    if (i % 60 == 0) return 400_000 + (i * 5003) % 500_001;
    return 80_000 + (i * 4001) % 70_001;
}

fn lessAt(_: void, a: Arr, b: Arr) bool {
    return a.at < b.at;
}

fn run(alloc: std.mem.Allocator, prof: Profile, seed: u64, converge_ns: i64) !Result {
    var prng = std.Random.DefaultPrng.init(seed);
    const rnd = prng.random();
    const ivl: i64 = @intFromFloat(1e9 / prof.fps);
    const n: usize = @intCast(@divTrunc(prof.dur_ns, ivl));
    var arr = try std.ArrayList(Arr).initCapacity(alloc, n);
    defer arr.deinit(alloc);

    // Receiver stalls.
    var stall_at = try std.ArrayList(i64).initCapacity(alloc, 64);
    defer stall_at.deinit(alloc);
    var stall_dur = try std.ArrayList(i64).initCapacity(alloc, 64);
    defer stall_dur.deinit(alloc);
    if (prof.stalls) {
        var t: i64 = 5_000_000_000 + rnd.intRangeLessThan(i64, 0, 25_000_000_000);
        while (t < prof.dur_ns) {
            try stall_at.append(alloc, t);
            try stall_dur.append(alloc, 20_000_000 + rnd.intRangeLessThan(i64, 0, 120_000_000));
            t += 30_000_000_000 + rnd.intRangeLessThan(i64, 0, 90_000_000_000);
        }
    }

    var res = Result{ .frames = n };
    var tx_end: i64 = 0; // sender link busy-until
    // Sender TX queue: (wire-done time, bytes) of AUs not fully sent yet (FIFO, bounded by the byte cap).
    var q_end = try std.ArrayList(i64).initCapacity(alloc, 256);
    defer q_end.deinit(alloc);
    var q_bytes = try std.ArrayList(usize).initCapacity(alloc, 256);
    defer q_bytes.deinit(alloc);
    var q_head: usize = 0;
    var queued: usize = 0;
    const pace_bps = prof.target_bps * 8;
    var i: usize = 0;
    while (i < n) : (i += 1) {
        const cap_t: i64 = @as(i64, @intCast(i)) * ivl;
        const bytes = frameBytes(i);
        var rate = @min(pace_bps, prof.link_bps);
        if (prof.cap_bps > 0 and cap_t >= prof.cap_at and cap_t < prof.cap_at + prof.cap_dur) rate = @min(rate, prof.cap_bps);
        // Sender TX queue occupancy (AUs not fully on the wire when this AU is submitted).
        while (q_head < q_end.items.len and q_end.items[q_head] <= cap_t) : (q_head += 1) queued -= q_bytes.items[q_head];
        if (queued + bytes > prof.queue_bytes) {
            res.sender_drops += 1;
            continue;
        }
        const start = @max(cap_t, tx_end);
        const ser: i64 = @intFromFloat(@as(f64, @floatFromInt(bytes)) * 8e9 / rate);
        tx_end = start + ser;
        try q_end.append(alloc, tx_end);
        try q_bytes.append(alloc, bytes);
        queued += bytes;
        var at = tx_end + 300_000;
        if (prof.jitter_ns > 0) at += rnd.intRangeLessThan(i64, 0, prof.jitter_ns);
        if (prof.loss > 0) { // per-datagram loss, NACK -> retransmit rounds
            const frags = (bytes + 1199) / 1200;
            var extra: i64 = 0;
            var f: usize = 0;
            while (f < frags) : (f += 1) {
                var rounds: i64 = 0;
                while (rnd.float(f64) < prof.loss and rounds < 5) rounds += 1;
                extra = @max(extra, rounds * (prof.rtt_ns + 1_000_000));
            }
            at += extra;
        }
        for (stall_at.items, stall_dur.items) |sa, sd| {
            if (at >= sa and at < sa + sd) at = sa + sd + @as(i64, @intCast(i % 64)) * 50_000;
        }
        try arr.append(alloc, .{ .at = at, .seq = @intCast(i), .pts = cap_t, .key = i % 60 == 0 });
    }
    std.mem.sort(Arr, arr.items, {}, lessAt);

    const items = try alloc.alloc(po.Item, 512);
    defer alloc.free(items);
    var p = po.Playout.initPreset(items, prof.fps, po.balanced);
    var added = try std.ArrayList(i64).initCapacity(alloc, n);
    defer added.deinit(alloc);

    var now: i64 = 0;
    var idx: usize = 0;
    var last_rel: i64 = -1;
    var last_pli: i64 = std.math.minInt(i64) / 2;
    while (true) {
        while (idx < arr.items.len and arr.items[idx].at <= now) : (idx += 1) {
            const a = arr.items[idx];
            _ = p.push(a.seq, a.pts, a.key, a.seq, a.at);
        }
        res.max_margin = @max(res.max_margin, p.margin_ns);
        if (p.pli_pending) { // the receiver's PLI rate limit (250 ms)
            p.pli_pending = false;
            if (now - last_pli >= 250_000_000) {
                res.plis += 1;
                last_pli = now;
            }
        }
        if (p.due(now)) |it| {
            if (last_rel >= 0 and now >= converge_ns) {
                const g = now - last_rel;
                res.max_gap_ns = @max(res.max_gap_ns, g);
                if (2 * g > 3 * ivl) res.bad_gaps += 1;
            }
            last_rel = now;
            res.released += 1;
            try added.append(alloc, now - it.pts_ns);
            continue;
        }
        var next: i64 = std.math.maxInt(i64);
        if (idx < arr.items.len) next = arr.items[idx].at;
        next = @min(next, p.nextWake());
        if (next == std.math.maxInt(i64)) break;
        now = @max(next, now + 1);
    }
    std.mem.sort(i64, added.items, {}, std.sort.asc(i64));
    if (added.items.len > 0) {
        res.added_p50 = added.items[added.items.len / 2];
        res.added_p99 = added.items[(added.items.len - 1) * 99 / 100];
    }
    res.stats = p.stats(now);
    return res;
}

fn report(prof: Profile, seed: u64, r: Result) void {
    if (!@import("build_options").chaos_report) return;
    std.debug.print("chaos {s} seed {d}: frames={d} released={d} senderDrops={d} badGaps={d} maxGap={d:.1}ms PLIs={d} maxMargin={d:.1}ms playout={d:.1}ms misses={d} repeats={d} rebases={d} gapDrops={d} added p50/p99={d:.1}/{d:.1}ms\n", .{
        prof.name,                                          seed,                r.frames,           r.released,          r.sender_drops,
        r.bad_gaps,                                         ms(r.max_gap_ns),    r.plis,             ms(r.max_margin),    r.stats.playout_ms,
        r.stats.misses,                                     r.stats.repeats,     r.stats.rebases,    r.stats.gap_drops,   ms(r.added_p50),
        ms(r.added_p99),
    });
}

fn ms(ns: i64) f64 {
    return @as(f64, @floatFromInt(ns)) / 1e6;
}

const rig = Profile{ .name = "this-rig", .loss = 0.0003, .rtt_ns = 1_000_000, .jitter_ns = 2_000_000, .stalls = true };
const wifi = Profile{ .name = "wifi-ish", .loss = 0.015, .rtt_ns = 8_000_000, .jitter_ns = 20_000_000, .link_bps = 300e6 };
const congested = Profile{ .name = "congested", .cap_bps = 50e6, .cap_at = 60_000_000_000, .cap_dur = 5_000_000_000, .jitter_ns = 1_000_000 };

test "chaos: this rig - zero hitches after 10 s within the balanced bound, no drops, no PLI" {
    var seed: u64 = 1;
    while (seed <= 6) : (seed += 1) {
        const r = try run(std.testing.allocator, rig, seed, 10_000_000_000);
        report(rig, seed, r);
        try std.testing.expectEqual(@as(usize, 0), r.bad_gaps);
        try std.testing.expectEqual(@as(usize, 0), r.sender_drops);
        try std.testing.expectEqual(r.frames, r.released);
        try std.testing.expectEqual(@as(usize, 0), r.plis);
        try std.testing.expect(r.max_margin <= po.balanced.max_ns);
    }
}

test "chaos: wifi-ish - every frame recovered, no PLI storm, no sender drops" {
    const r = try run(std.testing.allocator, wifi, 7, 10_000_000_000);
    report(wifi, 7, r);
    try std.testing.expectEqual(@as(usize, 0), r.sender_drops);
    try std.testing.expect(r.plis <= 60); // <= 1 per 10 s on a 10-min run
    try std.testing.expect(r.released * 1000 >= r.frames * 995);
    try std.testing.expect(r.max_margin <= po.balanced.max_ns);
}

test "chaos: congested 5 s below target - sender queue absorbs it, no drops, bounded PLIs" {
    const r = try run(std.testing.allocator, congested, 8, 10_000_000_000);
    report(congested, 8, r);
    try std.testing.expectEqual(@as(usize, 0), r.sender_drops);
    try std.testing.expect(r.plis <= 5);
    try std.testing.expectEqual(r.frames, r.released);
}
