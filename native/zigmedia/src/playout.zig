//! PTS-locked adaptive playout (native receiver core). A frame releases when the receiver's RAW
//! monotonic clock reaches `pts + base + margin`:
//!   base   = windowed-MIN transit (transit = arrival_mono - pts) over 4 x 2 s buckets. Cancels the
//!            sender<->receiver epoch offset, ignores cold-start stragglers, slow-tracks drift.
//!   margin = playout delay budget sized from MEASURED lateness (transit - base), owner directive
//!            2026-11-12 (receiver host stalls 20-140 ms at kernel/driver level - ride through them):
//!     - grow fast: a MISS (frame arrives after its deadline) raises margin at once to observed
//!       lateness x preset grow factor + headroom, bounded by preset max.
//!     - decay slow: <= interval/4 per DecayStep toward target = lateness quantile (balanced/smooth:
//!       max(quantile, worst recent stall x grow factor); the worst stall is forgotten at 1 ms/step)
//!       + headroom, floor 1 frame. A <= interval/4 step compresses one release interval instead of
//!       skipping a frame (an inter-coded stream cannot drop a reference frame).
//!     - balanced/smooth start AT max and hold it for one full lateness window; low-latency starts at
//!       1 frame.
//!   - anti-burst: never release closer than interval/2 after the previous release (a stall beyond
//!     the bound drains as compressed catch-up, not a burst).
//!   - a gap (missing predecessor) holds the head up to the max margin for the retransmit; only then
//!     is the gap skipped (inter-coded: wait for a keyframe + request one, rate-limited by the caller).
//!   - base-shift re-anchor: every frame > 2 intervals above base for 1 s (>= 30 arrivals) is a
//!     clock / PTS-domain step, not a stall (a stall ends with an on-time burst tail): re-anchor the
//!     base, un-count the run's misses, revert its margin growth, drop the lateness history.
//! Same policy as the legacy Go stopgap (internal/medialink/jitterbuf.go), which stays the reference.
//! Allocation-free (caller-provided item buffer; fixed-size histograms/rings inside the struct).
const std = @import("std");

pub const Item = struct {
    seq: u32,
    pts_ns: i64,
    key: bool = true, // decodable alone (keyframe / intra codec)
    tag: u64 = 0, // opaque payload handle (receiver: reassembled AU slot)
    used: bool = false,
};

/// Preset is a latency <-> smoothness trade-off (config mediaLink.playout).
pub const Preset = struct {
    name: []const u8,
    max_ns: i64,
    quantile: f64,
    peak_hold: bool,
    window_ns: i64,
    decay_step_ns: i64,
    grow_factor: f64,
    start_at_max: bool,
};

pub const low_latency = Preset{ .name = "low-latency", .max_ns = 60_000_000, .quantile = 0.99, .peak_hold = false, .window_ns = 120_000_000_000, .decay_step_ns = 5_000_000_000, .grow_factor = 1.0, .start_at_max = false };
pub const balanced = Preset{ .name = "balanced", .max_ns = 150_000_000, .quantile = 0.999, .peak_hold = true, .window_ns = 300_000_000_000, .decay_step_ns = 30_000_000_000, .grow_factor = 1.25, .start_at_max = true };
pub const smooth = Preset{ .name = "smooth", .max_ns = 250_000_000, .quantile = 0.9999, .peak_hold = true, .window_ns = 600_000_000_000, .decay_step_ns = 60_000_000_000, .grow_factor = 1.5, .start_at_max = true };

/// presetByName maps a config value to a preset (unknown / empty = balanced).
pub fn presetByName(s: []const u8) Preset {
    if (std.ascii.eqlIgnoreCase(s, "low-latency") or std.ascii.eqlIgnoreCase(s, "low")) return low_latency;
    if (std.ascii.eqlIgnoreCase(s, "smooth")) return smooth;
    return balanced;
}

const warmup_ns: i64 = 2_000_000_000;
const base_bucket_ns: i64 = 2_000_000_000;
const base_buckets = 4;
const headroom_ns: i64 = 2_000_000;
const bin_ns: i64 = 1_000_000;
const max_bins = 256; // 1 ms bins: covers every preset max (<= 250 ms) + overflow
const slices = 10;
const shift_win_ns: i64 = 1_000_000_000;
const shift_min_frames = 30;
const ring_cap = 1024;
const stat_minutes = 10;
const recover_grace_ns: i64 = 250_000_000;

const MinuteRing = struct {
    min: [stat_minutes]i64 = [_]i64{-1} ** stat_minutes,
    n: [stat_minutes]u64 = [_]u64{0} ** stat_minutes,

    fn add(r: *MinuteRing, now: i64, k: u64) void {
        const m = @divFloor(now, 60_000_000_000);
        const i: usize = @intCast(@mod(m, stat_minutes));
        if (r.min[i] != m) {
            r.min[i] = m;
            r.n[i] = 0;
        }
        r.n[i] += k;
    }
    fn sub(r: *MinuteRing, at: i64, k: u64) void {
        const m = @divFloor(at, 60_000_000_000);
        const i: usize = @intCast(@mod(m, stat_minutes));
        if (r.min[i] == m and r.n[i] >= k) r.n[i] -= k;
    }
    fn sum(r: *const MinuteRing, now: i64) u64 {
        const m = @divFloor(now, 60_000_000_000);
        var s: u64 = 0;
        for (r.min, r.n) |mm, n| {
            if (mm >= 0 and m - mm < stat_minutes) s += n;
        }
        return s;
    }
};

/// LatHist: sliding-window lateness histogram (slices x 1 ms bins + per-slice peak). Fixed size.
const LatHist = struct {
    slice_ns: i64,
    bins: usize,
    cur: i64 = 0,
    has: bool = false,
    counts: [slices][max_bins]u32 = [_][max_bins]u32{[_]u32{0} ** max_bins} ** slices,
    peak: [slices]i64 = [_]i64{0} ** slices,
    agg: [max_bins]u64 = [_]u64{0} ** max_bins,
    total: u64 = 0,

    fn init(p: Preset) LatHist {
        const b: usize = @min(@as(usize, @intCast(@divTrunc(p.max_ns, bin_ns))) + 2, max_bins);
        return .{ .slice_ns = @max(@divTrunc(p.window_ns, slices), 1), .bins = b };
    }

    fn rotate(h: *LatHist, now: i64) void {
        const id = @divFloor(now, h.slice_ns);
        if (!h.has) {
            h.cur = id;
            h.has = true;
            return;
        }
        var steps: usize = 0;
        while (h.cur < id and steps < slices) : (steps += 1) {
            h.cur += 1;
            const i: usize = @intCast(@mod(h.cur, slices));
            for (h.counts[i][0..h.bins], 0..) |c, b| {
                h.agg[b] -= c;
                h.total -= c;
            }
            @memset(&h.counts[i], 0);
            h.peak[i] = 0;
        }
        if (id > h.cur) h.cur = id;
    }

    fn add(h: *LatHist, now: i64, lat_in: i64) void {
        h.rotate(now);
        const lat = @max(lat_in, 0);
        const b: usize = @min(@as(usize, @intCast(@divTrunc(lat, bin_ns))), h.bins - 1);
        const i: usize = @intCast(@mod(h.cur, slices));
        h.counts[i][b] += 1;
        h.agg[b] += 1;
        h.total += 1;
        if (lat > h.peak[i]) h.peak[i] = lat;
    }

    fn quantile(h: *LatHist, now: i64, q: f64) struct { q: i64, peak: i64 } {
        h.rotate(now);
        var peak: i64 = 0;
        for (h.peak) |p| peak = @max(peak, p);
        if (h.total == 0) return .{ .q = 0, .peak = peak };
        var need: u64 = @intFromFloat(q * @as(f64, @floatFromInt(h.total)));
        if (need < 1) need = 1;
        var acc: u64 = 0;
        for (h.agg[0..h.bins], 0..) |c, b| {
            acc += c;
            if (acc >= need) return .{ .q = @as(i64, @intCast(b + 1)) * bin_ns, .peak = peak };
        }
        return .{ .q = @as(i64, @intCast(h.bins)) * bin_ns, .peak = peak };
    }
};

/// TimeRing: bounded ring of timestamps (shift detector: arrivals + misses inside the window).
const TimeRing = struct {
    t: [ring_cap]i64 = undefined,
    head: usize = 0,
    len: usize = 0,

    fn push(r: *TimeRing, v: i64) void {
        if (r.len == ring_cap) {
            r.head = (r.head + 1) % ring_cap;
            r.len -= 1;
        }
        r.t[(r.head + r.len) % ring_cap] = v;
        r.len += 1;
    }
    fn prune(r: *TimeRing, before: i64) void {
        while (r.len > 0 and r.t[r.head] < before) {
            r.head = (r.head + 1) % ring_cap;
            r.len -= 1;
        }
    }
    fn at(r: *const TimeRing, i: usize) i64 {
        return r.t[(r.head + i) % ring_cap];
    }
};

/// MinDeque: rolling min of transit over the shift window (monotonic deque, bounded).
const MinDeque = struct {
    at: [ring_cap]i64 = undefined,
    tr: [ring_cap]i64 = undefined,
    head: usize = 0,
    len: usize = 0,

    fn push(d: *MinDeque, now: i64, transit: i64) void {
        while (d.len > 0 and d.tr[(d.head + d.len - 1) % ring_cap] >= transit) d.len -= 1;
        if (d.len == ring_cap) {
            d.head = (d.head + 1) % ring_cap;
            d.len -= 1;
        }
        const i = (d.head + d.len) % ring_cap;
        d.at[i] = now;
        d.tr[i] = transit;
        d.len += 1;
        while (d.len > 0 and now - d.at[d.head] > shift_win_ns) {
            d.head = (d.head + 1) % ring_cap;
            d.len -= 1;
        }
    }
    fn min(d: *const MinDeque) ?i64 {
        if (d.len == 0) return null;
        return d.tr[d.head];
    }
};

/// MarginEvent is the last margin grow (for the hitch log).
pub const MarginEvent = struct { at: i64 = 0, from_ns: i64 = 0, to_ns: i64 = 0, observed_ns: i64 = 0 };

pub const Stats = struct {
    preset: []const u8,
    playout_ms: f64,
    target_ms: f64,
    max_ms: f64,
    buffered: usize,
    released: u64,
    misses: u64,
    misses10m: u64,
    repeats: u64,
    repeats10m: u64,
    grows: u64,
    decays: u64,
    rebases: u64,
    gap_drops: u64, // frames skipped after an unrecovered gap (+ keyframe wait)
    dups: u64,
    overflow: u64,
};

pub const Playout = struct {
    buf: []Item,
    n: usize = 0,
    interval_ns: i64,
    preset: Preset,
    margin_ns: i64,
    min_spacing_ns: i64,
    warmup: i64 = warmup_ns,

    anchored: bool = false,
    start_at: i64 = 0,
    next_seq: u32 = 0,
    wait_key: bool = false,
    pli_pending: bool = false, // set on an unrecovered gap; the caller sends a rate-limited PLI and clears
    // recovering: the caller's reassembler holds a PARTIAL frame inside the current gap (a retransmit
    // is in flight) - the gap is held recover_grace_ns longer before giving up (a stall-delayed IDR
    // plus its NACK round trip can exceed the max margin; skipping it costs a whole GOP + a PLI).
    recovering: bool = false,

    // base: current bucket + previous bucket mins.
    bucket_start: i64 = 0,
    bucket_min: i64 = 0,
    prev_mins: [base_buckets - 1]i64 = undefined,
    n_prev: usize = 0,

    hist: LatHist,
    long_peak: i64 = 0,
    long_peak_t: i64 = 0,
    last_change: i64 = 0,
    last_miss: i64 = 0,
    have_miss: bool = false,
    margin_before_run: i64 = 0,
    last_grow: MarginEvent = .{},
    last_rel: i64 = 0,
    have_rel: bool = false,

    shift_q: MinDeque = .{},
    arrivals: TimeRing = .{},
    miss_at: TimeRing = .{},

    misses10m: MinuteRing = .{},
    repeats10m: MinuteRing = .{},
    released: u64 = 0,
    misses: u64 = 0,
    repeats: u64 = 0,
    grows: u64 = 0,
    decays: u64 = 0,
    rebases: u64 = 0,
    gap_drops: u64 = 0,
    dups: u64 = 0,
    overflow: u64 = 0,

    /// init builds a balanced-preset playout.
    pub fn init(buf: []Item, fps: f64) Playout {
        return initPreset(buf, fps, balanced);
    }

    pub fn initPreset(buf: []Item, fps: f64, preset: Preset) Playout {
        var ivl: i64 = 16_666_667;
        if (fps > 0) ivl = @intFromFloat(1e9 / fps);
        var pr = preset;
        if (pr.max_ns < ivl) pr.max_ns = ivl;
        for (buf) |*it| it.* = .{ .seq = 0, .pts_ns = 0 };
        return .{
            .buf = buf,
            .interval_ns = ivl,
            .preset = pr,
            .margin_ns = if (pr.start_at_max) pr.max_ns else ivl,
            .min_spacing_ns = @divTrunc(ivl, 2),
            .hist = LatHist.init(pr),
        };
    }

    pub fn base(p: *const Playout) i64 {
        var m = p.bucket_min;
        for (p.prev_mins[0..p.n_prev]) |v| m = @min(m, v);
        return m;
    }

    /// deadline is frame pts_ns's monotonic release time.
    pub fn deadline(p: *const Playout, pts_ns: i64) i64 {
        return pts_ns + p.base() + p.margin_ns;
    }

    /// push accepts a received frame at RAW monotonic time now. false = dropped (dup / too old /
    /// overflow-evicted itself).
    pub fn push(p: *Playout, seq: u32, pts_ns: i64, key: bool, tag: u64, now: i64) bool {
        const transit = now - pts_ns;
        if (!p.anchored) {
            p.anchored = true;
            p.start_at = now;
            p.next_seq = seq;
            p.bucket_start = now;
            p.bucket_min = transit;
            p.last_change = now;
        }
        if (seqLess(seq, p.next_seq)) {
            p.dups += 1;
            return false;
        }
        var i = p.n;
        while (i > 0 and seqLess(seq, p.buf[i - 1].seq)) : (i -= 1) {}
        if (i > 0 and p.buf[i - 1].seq == seq) {
            p.dups += 1;
            return false;
        }
        if (p.n == p.buf.len) { // bounded: evict the head (oldest)
            if (i == 0) {
                p.overflow += 1;
                return false;
            }
            p.popHead();
            p.overflow += 1;
            i -= 1;
        }
        var j = p.n;
        while (j > i) : (j -= 1) p.buf[j] = p.buf[j - 1];
        p.buf[i] = .{ .seq = seq, .pts_ns = pts_ns, .key = key, .tag = tag, .used = true };
        p.n += 1;

        p.updateBase(transit, now);
        p.account(pts_ns, transit, now);
        return true;
    }

    fn updateBase(p: *Playout, transit: i64, now: i64) void {
        if (now - p.bucket_start >= base_bucket_ns) {
            if (p.n_prev == p.prev_mins.len) {
                var k: usize = 1;
                while (k < p.n_prev) : (k += 1) p.prev_mins[k - 1] = p.prev_mins[k];
                p.n_prev -= 1;
            }
            p.prev_mins[p.n_prev] = p.bucket_min;
            p.n_prev += 1;
            p.bucket_start = now;
            p.bucket_min = transit;
            return;
        }
        if (transit < p.bucket_min) p.bucket_min = transit;
    }

    fn account(p: *Playout, pts_ns: i64, transit: i64, now: i64) void {
        const lateness = transit - p.base();
        const late_by = now - p.deadline(pts_ns);
        if (now - p.start_at < p.warmup) return; // startup transients: no history, no grow, no count
        p.arrivals.push(now);
        p.arrivals.prune(now - shift_win_ns);
        if (p.detectShift(now, transit)) return;
        p.hist.add(now, lateness);
        if (lateness > p.longPeakAt(now)) {
            p.long_peak = lateness;
            p.long_peak_t = now;
        }
        if (late_by > 0) {
            if (!p.have_miss or now - p.last_miss > shift_win_ns) p.margin_before_run = p.margin_ns;
            p.misses += 1;
            p.misses10m.add(now, 1);
            p.miss_at.push(now);
            p.last_miss = now;
            p.have_miss = true;
            var want: i64 = @as(i64, @intFromFloat(@as(f64, @floatFromInt(lateness)) * p.preset.grow_factor)) + headroom_ns;
            if (want > p.preset.max_ns) want = p.preset.max_ns;
            if (want > p.margin_ns) {
                p.last_grow = .{ .at = now, .from_ns = p.margin_ns, .to_ns = want, .observed_ns = lateness };
                p.margin_ns = want;
                p.grows += 1;
                p.last_change = now;
            }
            return;
        }
        if (now - p.last_change < p.preset.decay_step_ns) return;
        if (p.have_miss and now - p.last_miss < p.preset.decay_step_ns) return;
        if (p.preset.start_at_max and now - p.start_at < p.preset.window_ns) return;
        p.last_change = now;
        const t = p.target(now);
        if (p.margin_ns > t) {
            p.margin_ns -= @min(p.margin_ns - t, @divTrunc(p.interval_ns, 4));
            p.decays += 1;
        }
    }

    fn detectShift(p: *Playout, now: i64, transit: i64) bool {
        p.shift_q.push(now, transit);
        p.miss_at.prune(now - shift_win_ns);
        if (now - p.start_at < p.warmup + shift_win_ns) return false;
        const rmin = p.shift_q.min() orelse return false;
        if (rmin - p.base() <= 2 * p.interval_ns) return false;
        if (p.arrivals.len < shift_min_frames) return false;
        var k: usize = 0;
        while (k < p.miss_at.len) : (k += 1) {
            p.misses -|= 1;
            p.misses10m.sub(p.miss_at.at(k), 1);
        }
        p.miss_at.len = 0;
        if (p.margin_before_run > 0) p.margin_ns = p.margin_before_run;
        p.bucket_start = now;
        p.bucket_min = rmin;
        p.n_prev = 0;
        p.hist = LatHist.init(p.preset);
        p.long_peak = 0;
        p.long_peak_t = 0;
        p.have_miss = false;
        p.rebases += 1;
        return true;
    }

    fn gapLimit(p: *const Playout) i64 {
        return p.preset.max_ns + (if (p.recovering) recover_grace_ns else 0);
    }

    fn longPeakAt(p: *const Playout, now: i64) i64 {
        if (p.preset.decay_step_ns <= 0) return 0;
        return @max(p.long_peak - @divTrunc(now - p.long_peak_t, p.preset.decay_step_ns) * bin_ns, 0);
    }

    /// target is the decay target in [1 frame, max].
    pub fn target(p: *Playout, now: i64) i64 {
        const r = p.hist.quantile(now, p.preset.quantile);
        var q = r.q;
        if (p.preset.peak_hold) {
            const pk = @max(r.peak, p.longPeakAt(now));
            q = @max(q, @as(i64, @intFromFloat(@as(f64, @floatFromInt(pk)) * p.preset.grow_factor)));
        }
        return std.math.clamp(q + headroom_ns, p.interval_ns, p.preset.max_ns);
    }

    /// due returns the next releasable frame (seq order) or null.
    pub fn due(p: *Playout, now: i64) ?Item {
        while (p.n > 0) {
            const h = p.buf[0];
            if (p.wait_key) {
                if (!h.key) {
                    p.popHead();
                    p.next_seq = h.seq +% 1;
                    p.gap_drops += 1;
                    continue;
                }
                p.wait_key = false;
            }
            if (h.seq != p.next_seq) {
                // Missing predecessor: wait for its retransmit up to the max margin, then skip.
                const gap: i64 = @intCast(h.seq -% p.next_seq);
                const miss_pts = h.pts_ns - gap * p.interval_ns;
                if (now < miss_pts + p.base() + p.gapLimit()) return null;
                p.next_seq = h.seq;
                p.pli_pending = true;
                if (!h.key) {
                    p.wait_key = true; // inter-coded: an undecodable run never reaches the decoder
                    continue;
                }
            }
            if (now < p.deadline(h.pts_ns)) return null;
            if (p.have_rel and now - p.last_rel < p.min_spacing_ns) return null; // anti-burst
            p.popHead();
            p.next_seq = h.seq +% 1;
            if (p.have_rel) {
                const g = now - p.last_rel;
                if (2 * g > 3 * p.interval_ns) {
                    const k: u64 = @intCast(@divTrunc(g + @divTrunc(p.interval_ns, 2), p.interval_ns) - 1);
                    p.repeats += k;
                    p.repeats10m.add(now, k);
                }
            }
            p.last_rel = now;
            p.have_rel = true;
            p.released += 1;
            return h;
        }
        return null;
    }

    /// nextWake is the monotonic time to wake for the next release decision (max i64 when empty).
    pub fn nextWake(p: *const Playout) i64 {
        if (p.n == 0) return std.math.maxInt(i64);
        const h = p.buf[0];
        var w: i64 = undefined;
        if (h.seq != p.next_seq and !p.wait_key) {
            const gap: i64 = @intCast(h.seq -% p.next_seq);
            w = (h.pts_ns - gap * p.interval_ns) + p.base() + p.gapLimit();
        } else {
            w = p.deadline(h.pts_ns);
        }
        if (p.have_rel) w = @max(w, p.last_rel + p.min_spacing_ns);
        return w;
    }

    /// nextDeadlineGap: legacy name for nextWake.
    pub fn nextDeadlineGap(p: *const Playout) i64 {
        return p.nextWake();
    }

    pub fn stats(p: *Playout, now: i64) Stats {
        return .{
            .preset = p.preset.name,
            .playout_ms = ms(p.margin_ns),
            .target_ms = ms(p.target(now)),
            .max_ms = ms(p.preset.max_ns),
            .buffered = p.n,
            .released = p.released,
            .misses = p.misses,
            .misses10m = p.misses10m.sum(now),
            .repeats = p.repeats,
            .repeats10m = p.repeats10m.sum(now),
            .grows = p.grows,
            .decays = p.decays,
            .rebases = p.rebases,
            .gap_drops = p.gap_drops,
            .dups = p.dups,
            .overflow = p.overflow,
        };
    }

    pub fn marginMs(p: *const Playout) f64 {
        return ms(p.margin_ns);
    }

    fn popHead(p: *Playout) void {
        var i: usize = 1;
        while (i < p.n) : (i += 1) p.buf[i - 1] = p.buf[i];
        p.n -= 1;
    }
};

fn ms(ns: i64) f64 {
    return @as(f64, @floatFromInt(ns)) / 1e6;
}

/// seqLess is wrap-aware (u32 sequence space).
fn seqLess(a: u32, b: u32) bool {
    return @as(i32, @bitCast(a -% b)) < 0;
}

test "PTS-locked release is evenly spaced under bunched arrivals" {
    var mem: [16]Item = undefined;
    var p = Playout.initPreset(&mem, 60, low_latency);
    const ivl = p.interval_ns;
    _ = p.push(0, 0, true, 100, 0);
    _ = p.push(1, ivl, false, 101, 3 * ivl);
    _ = p.push(2, 2 * ivl, false, 102, 3 * ivl);
    _ = p.push(3, 3 * ivl, false, 103, 3 * ivl);
    var rel: [4]u64 = undefined;
    var prev_t: i64 = -1;
    var k: usize = 0;
    var t: i64 = 0;
    while (k < 4) {
        if (p.due(t)) |it| {
            rel[k] = it.tag;
            if (prev_t >= 0) try std.testing.expect(t - prev_t >= @divTrunc(ivl, 2)); // never a burst
            prev_t = t;
            k += 1;
        } else {
            t = p.nextWake();
        }
    }
    try std.testing.expectEqual([4]u64{ 100, 101, 102, 103 }, rel);
}

test "miss grows the margin to cover the observed lateness" {
    var mem: [64]Item = undefined;
    var p = Playout.initPreset(&mem, 100, low_latency);
    p.warmup = 0;
    const ivl = p.interval_ns; // 10 ms
    var i: u32 = 0;
    while (i < 10) : (i += 1) _ = p.push(i, @as(i64, i) * ivl, true, i, @as(i64, i) * ivl + 1_000_000);
    _ = p.push(10, 10 * ivl, true, 10, 10 * ivl + 1_000_000 + 25_000_000); // 25 ms late vs base
    try std.testing.expectEqual(@as(u64, 1), p.misses);
    try std.testing.expectEqual(@as(i64, 27_000_000), p.margin_ns);
}

test "gap: held for the retransmit within the max margin, then skipped to a keyframe" {
    var mem: [16]Item = undefined;
    var p = Playout.initPreset(&mem, 100, low_latency);
    const ivl = p.interval_ns;
    _ = p.push(0, 0, true, 0, 0);
    _ = p.push(2, 2 * ivl, false, 2, 2 * ivl); // seq 1 missing, delta frame
    _ = p.push(3, 3 * ivl, true, 3, 3 * ivl); // keyframe
    try std.testing.expectEqual(@as(u32, 0), p.due(p.deadline(0)).?.seq);
    // Before the gap's max-margin window: held.
    try std.testing.expectEqual(@as(?Item, null), p.due(2 * ivl + 10_000_000));
    // Retransmit of seq 1 arrives in time: released in order.
    _ = p.push(1, ivl, false, 1, 2 * ivl + 20_000_000);
    try std.testing.expectEqual(@as(u32, 1), p.due(2 * ivl + 40_000_000).?.seq);
    // Now a real loss: seq 4 missing, 5 delta, 6 key. Past the window: 5 dropped, PLI, 6 released.
    _ = p.push(5, 5 * ivl, false, 5, 5 * ivl);
    _ = p.push(6, 6 * ivl, true, 6, 6 * ivl);
    var t: i64 = 3 * ivl + 50_000_000;
    var got: [3]u32 = undefined;
    var k: usize = 0;
    while (k < 3 and t < 2_000_000_000) : (t += 1_000_000) {
        if (p.due(t)) |it| {
            got[k] = it.seq;
            k += 1;
        }
    }
    try std.testing.expectEqual(@as(usize, 3), k);
    try std.testing.expectEqual([3]u32{ 2, 3, 6 }, got);
    try std.testing.expect(p.pli_pending);
    try std.testing.expectEqual(@as(u64, 1), p.gap_drops);
}

test "presetByName" {
    try std.testing.expectEqualStrings("balanced", presetByName("").name);
    try std.testing.expectEqualStrings("smooth", presetByName("Smooth").name);
    try std.testing.expectEqualStrings("low-latency", presetByName("low-latency").name);
}
