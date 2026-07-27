//! rave-mate-enc - per-adapter Media Foundation H.264 encoder child (Zig, no cgo).
//! One child per adapter; N encode sessions multiplex inside it. A vendor-driver fault
//! kills only THIS process - the media child supervises + restarts (crash containment by
//! process boundary, the Phase-1 hard requirement).
//!
//! Control plane (newline JSON): stdin ops
//!   {"op":"open","sid":1,"shm":"Local\\rvmfenc-P-S","in_w":..,"in_h":..,"out_w":..,
//!    "out_h":..,"fps_n":..,"fps_d":..,"kbps":..,"gop":..,
//!    "src":"shm"|"spout","sh":<u64 share handle>,"sfmt":<DXGI fmt>,"sname":"<sender>",
//!    "cap_n":..,"cap_d":..,"ring_kb":..,"pts0":<ns>}
//!    "dir":"enc"|"dec","codec":"h264"|"hevc","dsh":<u64 dest share handle>,
//!    "dfmt":<DXGI fmt>,"dname":"<dest sender>","in_ring_kb":..}
//!   {"op":"close","sid":1} | {"op":"bitrate","sid":1,"kbps":..} | {"op":"idr","sid":1}
//!   {"op":"quit"}
//! stdout events: {"ev":"hello","ver":2,"luid":..} {"ev":"opened","sid":..,"ok":..,
//!   "err":..,"name":..,"bgra":..,"src":..,"cap":"zerocopy"|"downgraded","err_src":..}
//!   {"ev":"srcgone","sid":..,"reason":..} {"ev":"dstgone","sid":..,"reason":..}
//!   {"ev":"closed","sid":..}
//!
//! Data plane per session (named shared memory, parent creates, we open):
//!   header 256 B: 0 magic 'RMF2' u32 | 4 ver u32 | 8 frameSeq u64 (parent) |
//!     16 framePTS i64 ns (parent) | 24 consSeq u64 (child) | 32 auWrite u64 (child,
//!     virtual offset) | 40 auRead u64 (parent) | 48 auDropped u64 | 56 encBusyNs u64 |
//!     64 capFrames | 72 capSkips | 80 mtxTimeouts | 88 srcErrors | 96 lastCapNs i64 |
//!     104 capFmt u32 | 108 capFlags u32   (64.. child-written zero-copy telemetry)
//!     dir "dec" adds a second ring-counter block + decode telemetry:
//!     128 inWrite u64 (PARENT) | 136 inRead u64 | 144 inDropped u64 (PARENT) |
//!     152 decBusyNs | 160 decFrames | 168 decErrors | 176 lastPubNs i64 |
//!     184 decFlags u32 | 192 decDropped u64 | 200 decMtxTimeouts u64
//!   src "shm": frame slot @256 (in_w*in_h*4 RGBA), AU ring after it.
//!   dir "dec": NO frame slot and no OUTBOUND ring - the INBOUND AU ring starts at 256 and the
//!     mapping is 256 + in_ring_kb*1024. Same record layout, opposite direction: the parent
//!     appends AUs + signals -f, the child consumes + signals -c. -a never fires.
//!   src "spout": NO frame slot - the AU ring starts at 256 and the mapping is
//!     256 + ring_kb*1024. Sizing MUST come from src + ring_kb, never from in_w*in_h*4,
//!     which is why a spout session requires header ver >= 2 before it maps anything.
//!   AU record: u32 len | u32 flags(bit0 key) | i64 pts ns | data | pad to 8.
//!   len 0xFFFFFFFF = wrap marker; tail < 16 = implicit wrap. Ring full → drop + count
//!   (bounded by design; parent drains on evAU).
//! Events (auto-reset, parent creates): <shm>-f frame ready, <shm>-c frame consumed,
//!   <shm>-a AU appended. Both src modes open all three (uniform open/restart/teardown);
//!   on a spout session -f is only the control-ping wake and -c never fires.

const std = @import("std");
const mf = @import("mf.zig");
const cap = @import("cap.zig");
const dec = @import("dec.zig");

extern "kernel32" fn OpenFileMappingW(u32, i32, [*:0]const u16) callconv(.winapi) ?*anyopaque;
extern "kernel32" fn MapViewOfFile(*anyopaque, u32, u32, u32, usize) callconv(.winapi) ?[*]u8;
extern "kernel32" fn OpenEventW(u32, i32, [*:0]const u16) callconv(.winapi) ?*anyopaque;
extern "kernel32" fn SetEvent(*anyopaque) callconv(.winapi) i32;
extern "kernel32" fn UnmapViewOfFile(*const anyopaque) callconv(.winapi) i32;
extern "kernel32" fn CloseHandle(*anyopaque) callconv(.winapi) i32;
extern "kernel32" fn WaitForSingleObject(*anyopaque, u32) callconv(.winapi) u32;
extern "kernel32" fn AcquireSRWLockExclusive(*usize) callconv(.winapi) void;
extern "kernel32" fn ReleaseSRWLockExclusive(*usize) callconv(.winapi) void;
extern "kernel32" fn QueryPerformanceCounter(*i64) callconv(.winapi) i32;
extern "kernel32" fn QueryPerformanceFrequency(*i64) callconv(.winapi) i32;
extern "winmm" fn timeBeginPeriod(u32) callconv(.winapi) u32;

// qpcNs: monotonic ns (std.time.Timer moved onto the Io runtime in 0.16).
var qpc_freq: i64 = 0;
fn qpcNs() u64 {
    if (qpc_freq == 0) _ = QueryPerformanceFrequency(&qpc_freq);
    var c: i64 = 0;
    _ = QueryPerformanceCounter(&c);
    if (qpc_freq <= 0) return 0;
    const wide: i128 = @as(i128, c) * std.time.ns_per_s;
    return @intCast(@divTrunc(wide, qpc_freq));
}

// Zig 0.16 moved std blocking Mutex onto the Io runtime; this exe is Win32-native
// (locks shared with COM threads) - SRWLOCK is the honest primitive (winshell precedent).
const Lock = struct {
    srw: usize = 0,
    fn lock(l: *Lock) void {
        AcquireSRWLockExclusive(&l.srw);
    }
    fn unlock(l: *Lock) void {
        ReleaseSRWLockExclusive(&l.srw);
    }
};

const FILE_MAP_ALL_ACCESS: u32 = 0xF001F;
const EVENT_ALL_ACCESS: u32 = 0x1F0003;

const hdr_size = 256;
const wrap_marker: u32 = 0xFFFFFFFF;
const hdr_magic: u32 = 0x32464D52; // 'RMF2' little-endian
const hdr_ver_zerocopy: u32 = 2; // minimum header version that may carry src:"spout"
// Ring bounds (design §3.1): bitrate-derived, geometry-INDEPENDENT, so a sender resize costs
// zero SHM realloc. Both ends clamp; a value outside this is a protocol error, not a resize.
const ring_kb_min: u32 = 4 * 1024;
const ring_kb_max: u32 = 16 * 1024;

// Header field accessors (volatile cross-process).
const Hdr = struct {
    base: [*]u8,

    fn u64At(h: Hdr, off: usize) *volatile u64 {
        return @ptrCast(@alignCast(h.base + off));
    }
    fn i64At(h: Hdr, off: usize) *volatile i64 {
        return @ptrCast(@alignCast(h.base + off));
    }
    fn frameSeq(h: Hdr) u64 {
        return @atomicLoad(u64, @volatileCast(h.u64At(8)), .acquire);
    }
    fn framePTS(h: Hdr) i64 {
        return h.i64At(16).*;
    }
    fn setConsSeq(h: Hdr, v: u64) void {
        @atomicStore(u64, @volatileCast(h.u64At(24)), v, .release);
    }
    fn auWrite(h: Hdr) u64 {
        return @atomicLoad(u64, @volatileCast(h.u64At(32)), .acquire);
    }
    fn setAuWrite(h: Hdr, v: u64) void {
        @atomicStore(u64, @volatileCast(h.u64At(32)), v, .release);
    }
    fn auRead(h: Hdr) u64 {
        return @atomicLoad(u64, @volatileCast(h.u64At(40)), .acquire);
    }
    fn bumpDropped(h: Hdr) void {
        _ = @atomicRmw(u64, @volatileCast(h.u64At(48)), .Add, 1, .monotonic);
    }
    fn addBusy(h: Hdr, ns: u64) void {
        _ = @atomicRmw(u64, @volatileCast(h.u64At(56)), .Add, ns, .monotonic);
    }
    fn u32At(h: Hdr, off: usize) *volatile u32 {
        return @ptrCast(@alignCast(h.base + off));
    }
    fn magic(h: Hdr) u32 {
        return @atomicLoad(u32, @volatileCast(h.u32At(0)), .acquire);
    }
    fn ver(h: Hdr) u32 {
        return @atomicLoad(u32, @volatileCast(h.u32At(4)), .acquire);
    }
    // Zero-copy capture counters (child-written, monotonic; the parent reads them for
    // telemetry so the frame path stays JSON-free).
    fn bumpAt(h: Hdr, off: usize) void {
        _ = @atomicRmw(u64, @volatileCast(h.u64At(off)), .Add, 1, .monotonic);
    }
    fn setLastCapNs(h: Hdr, v: i64) void {
        @atomicStore(i64, @volatileCast(h.i64At(96)), v, .release);
    }
    fn setCapFmt(h: Hdr, v: u32) void {
        @atomicStore(u32, @volatileCast(h.u32At(104)), v, .release);
    }
    fn setCapFlags(h: Hdr, v: u32) void {
        @atomicStore(u32, @volatileCast(h.u32At(108)), v, .release);
    }
    // dir:"dec" accessors. inWrite/inDropped are PARENT-written; everything else is ours.
    fn inWrite(h: Hdr) u64 {
        return @atomicLoad(u64, @volatileCast(h.u64At(off_in_write)), .acquire);
    }
    fn inRead(h: Hdr) u64 {
        return @atomicLoad(u64, @volatileCast(h.u64At(off_in_read)), .acquire);
    }
    fn setInRead(h: Hdr, v: u64) void {
        @atomicStore(u64, @volatileCast(h.u64At(off_in_read)), v, .release);
    }
    fn setU64At(h: Hdr, off: usize, v: u64) void {
        @atomicStore(u64, @volatileCast(h.u64At(off)), v, .release);
    }
    fn addAt(h: Hdr, off: usize, v: u64) void {
        _ = @atomicRmw(u64, @volatileCast(h.u64At(off)), .Add, v, .monotonic);
    }
    fn setI64At(h: Hdr, off: usize, v: i64) void {
        @atomicStore(i64, @volatileCast(h.i64At(off)), v, .release);
    }
    fn setU32At(h: Hdr, off: usize, v: u32) void {
        @atomicStore(u32, @volatileCast(h.u32At(off)), v, .release);
    }
};

const off_cap_frames = 64;
const off_cap_skips = 72;
const off_mtx_timeouts = 80;
const off_src_errors = 88;

// dir:"dec" block (design §10: the ring counters instantiated a second time, opposite direction).
const off_in_write = 128;
const off_in_read = 136;
const off_dec_busy = 152;
const off_dec_frames = 160;
const off_dec_errors = 168;
const off_last_pub_ns = 176;
const off_dec_flags = 184;
const off_dec_dropped = 192;
const off_dec_mtx_timeouts = 200;

// fault_after_frames > 0: crash the process after N encoded frames (test hook - proves
// route continuity across an encoder-child death; parent injects on first spawn only).
var fault_after_frames: u64 = 0;

// probe_bands (RAVE_MATE_MFDEC_PROBE_BANDS=1): after the first published frame, read the
// DESTINATION texture back on the GPU and print the top/bottom pixel. The orientation + channel
// oracle for the decode path - Spout's receive side cannot see a texture written by a foreign
// device (it never reports a new frame), so nothing outside this process can check the picture.
var probe_bands: bool = false;

const Session = struct {
    gpa: std.mem.Allocator,
    sid: u32,
    luid: i64,
    in_w: i32,
    in_h: i32,
    out_w: i32,
    out_h: i32,
    fps_n: i32,
    fps_d: i32,
    kbps: i32,
    gop: i32,
    shm_name: []u8,

    // Zero-copy capture source (src:"spout"); src_spout=false = v1 SHM frame ring.
    src_spout: bool = false,
    share: u64 = 0,
    sfmt: u32 = 0,
    sname: []u8 = &.{},
    cap_n: i32 = 0,
    cap_d: i32 = 1,
    ring_kb: u32 = 0,
    pts0: i64 = 0, // parent's wall-clock ns at open: AU pts = pts0 + qpc elapsed, so a
    // zero-copy route's timebase is identical to the readback path's (the receiver's
    // jitter buffer + transit telemetry compare pts against the sender's clock)
    cap_open: ?cap.Cap = null,

    // dir:"dec" - receive side (zigmedia inc 2). The child decodes the inbound AUs and renders
    // them into the DESTINATION video-share sender's shared texture (dsh), which Go created.
    dir_dec: bool = false,
    codec_hevc: bool = false,
    dsh: u64 = 0,
    dfmt: u32 = 0,
    dname: []u8 = &.{},
    in_ring_kb: u32 = 0,
    dec_open: ?*dec.Dec = null,

    // mailbox (stdin thread → session thread)
    mu: Lock = .{},
    want_idr: bool = false,
    want_kbps: u32 = 0,
    closing: bool = false,

    thread: ?std.Thread = null,

    hdr: Hdr = undefined,
    frame: [*]u8 = undefined,
    ring: [*]u8 = undefined,
    ring_size: u64 = 0,
    mapping: ?*anyopaque = null, // OS handles owned by this session - released in closeShm
    view: ?[*]u8 = null,
    ev_frame: *anyopaque = undefined,
    ev_cons: *anyopaque = undefined,
    ev_au: *anyopaque = undefined,
    evs_open: u32 = 0, // how many of the 3 events opened (partial-open cleanup)
    aus_put: u64 = 0,
    fed: u64 = 0,
};

var out_mu: Lock = .{};
var g_out: *std.Io.Writer = undefined;

fn emit(v: anytype) void {
    out_mu.lock();
    defer out_mu.unlock();
    var s: std.json.Stringify = .{ .writer = g_out };
    s.write(v) catch return;
    g_out.writeByte('\n') catch return;
    g_out.flush() catch return;
}

fn utf16Z(gpa: std.mem.Allocator, s: []const u8) ![:0]u16 {
    var buf = try gpa.allocSentinel(u16, s.len, 0);
    for (s, 0..) |c, i| buf[i] = c; // names are ASCII (Local\rvmfenc-...)
    return buf;
}

// openShm maps the session's shared memory + events created by the parent. A spout session's
// mapping is sized from src + ring_kb ONLY - deriving it from in_w*in_h*4 is the one place a v1
// child would map past the end of the parent's smaller mapping, hence the ver gate below.
fn openShm(s: *Session) !void {
    const gpa = s.gpa;
    const frame_bytes: u64 = if (s.src_spout or s.dir_dec) 0 else @as(u64, @intCast(s.in_w)) * @as(u64, @intCast(s.in_h)) * 4;
    var ring: u64 = 8 << 20;
    if (s.dir_dec) {
        // INBOUND ring only: sized from in_ring_kb, never from geometry (a stream resize costs no
        // SHM realloc, exactly like the outbound side).
        if (s.in_ring_kb < ring_kb_min or s.in_ring_kb > ring_kb_max) return error.RingSize;
        ring = @as(u64, s.in_ring_kb) * 1024;
    } else if (s.src_spout) {
        if (s.ring_kb < ring_kb_min or s.ring_kb > ring_kb_max) return error.RingSize;
        ring = @as(u64, s.ring_kb) * 1024;
    } else if (frame_bytes > ring) {
        ring = frame_bytes;
    }
    const total: usize = @intCast(hdr_size + frame_bytes + ring);

    const wname = try utf16Z(gpa, s.shm_name);
    defer gpa.free(wname);
    const mapping = OpenFileMappingW(FILE_MAP_ALL_ACCESS, 0, wname) orelse return error.ShmOpen;
    s.mapping = mapping;
    const view = MapViewOfFile(mapping, FILE_MAP_ALL_ACCESS, 0, 0, total) orelse return error.ShmMap;
    s.view = view;
    s.hdr = .{ .base = view };
    if ((s.src_spout or s.dir_dec) and (s.hdr.magic() != hdr_magic or s.hdr.ver() < hdr_ver_zerocopy)) {
        return error.HdrVersion; // parent did not stamp a v2 header: refuse, never guess a layout
    }
    s.frame = view + hdr_size;
    s.ring = view + hdr_size + @as(usize, @intCast(frame_bytes));
    s.ring_size = ring;

    inline for (.{ "-f", "-c", "-a" }, 0..) |suffix, i| {
        const en = try std.fmt.allocPrint(gpa, "{s}{s}", .{ s.shm_name, suffix });
        defer gpa.free(en);
        const wen = try utf16Z(gpa, en);
        defer gpa.free(wen);
        const ev = OpenEventW(EVENT_ALL_ACCESS, 0, wen) orelse return error.EventOpen;
        switch (i) {
            0 => s.ev_frame = ev,
            1 => s.ev_cons = ev,
            else => s.ev_au = ev,
        }
        s.evs_open = i + 1;
    }
}

// closeShm releases the view + mapping + event handles (ALL exit paths incl. partial
// opens): a long-lived per-adapter child over many open/close cycles must not leak
// handles or VA (>=8 MB view per session).
fn closeShm(s: *Session) void {
    if (s.view) |v| _ = UnmapViewOfFile(v);
    s.view = null;
    if (s.mapping) |m| _ = CloseHandle(m);
    s.mapping = null;
    if (s.evs_open >= 1) _ = CloseHandle(s.ev_frame);
    if (s.evs_open >= 2) _ = CloseHandle(s.ev_cons);
    if (s.evs_open >= 3) _ = CloseHandle(s.ev_au);
    s.evs_open = 0;
}

// ringPut appends one AU record; full ring drops (parent drains on evAU; bounded by design).
fn ringPut(s: *Session, data: []const u8, pts_ns: i64, key: bool) void {
    const rec: u64 = 16 + std.mem.alignForward(u64, data.len, 8);
    var w = s.hdr.auWrite();
    const r = s.hdr.auRead();
    var need = rec;
    const tail = s.ring_size - (w % s.ring_size);
    const wraps = tail < rec;
    if (wraps) need = rec + tail;
    if (need > s.ring_size - (w - r)) {
        s.hdr.bumpDropped();
        return;
    }
    if (wraps) {
        if (tail >= 4) {
            const mp: *align(1) u32 = @ptrCast(s.ring + @as(usize, @intCast(w % s.ring_size)));
            mp.* = wrap_marker;
        }
        w += tail;
    }
    const pos: usize = @intCast(w % s.ring_size);
    const lp: *align(1) u32 = @ptrCast(s.ring + pos);
    lp.* = @intCast(data.len);
    const fp: *align(1) u32 = @ptrCast(s.ring + pos + 4);
    fp.* = if (key) 1 else 0;
    const pp: *align(1) i64 = @ptrCast(s.ring + pos + 8);
    pp.* = pts_ns;
    @memcpy((s.ring + pos + 16)[0..data.len], data);
    s.hdr.setAuWrite(w + rec);
    s.aus_put += 1;
    _ = SetEvent(s.ev_au);
}

fn sinkPut(ctx: *anyopaque, data: []const u8, pts100: i64, key: bool) void {
    const s: *Session = @ptrCast(@alignCast(ctx));
    ringPut(s, data, pts100 * 100, key); // MF 100ns → ns on the wire
}

const OpenedEv = struct {
    ev: []const u8 = "opened",
    sid: u32,
    ok: bool,
    err: []const u8 = "",
    name: []const u8 = "",
    bgra: bool = false,
    // Zero-copy verdict rides the SAME event: the parent learns which path it got without a
    // second round trip. cap = "zerocopy" (live) | "downgraded" (open refused, err_src says why).
    src: []const u8 = "shm",
    cap: []const u8 = "",
    err_src: []const u8 = "",
    // dir:"dec" verdict rides the same event: dir + err_dst say which path the session got.
    dir: []const u8 = "enc",
    err_dst: []const u8 = "",
};

const SrcGoneEv = struct {
    ev: []const u8 = "srcgone",
    sid: u32,
    reason: []const u8,
};

// DstGoneEv: the DESTINATION texture became unusable (sender restarted/resized/gone) or the
// decoder failed. Session + decoder stay alive and stop publishing; the PARENT decides.
const DstGoneEv = struct {
    ev: []const u8 = "dstgone",
    sid: u32,
    reason: []const u8,
};

// sessionMain owns one pipeline on one thread (COM MTA).
fn sessionMain(s: *Session) void {
    mf.coInitMTA();
    defer teardownSession(s);

    openShm(s) catch |e| {
        emit(OpenedEv{ .sid = s.sid, .ok = false, .err = @errorName(e) });
        return;
    };
    if (s.dir_dec) {
        decSessionMain(s);
        return;
    }
    const enc = mf.Enc.open(s.gpa, s.luid, s.in_w, s.in_h, s.out_w, s.out_h, s.fps_n, s.fps_d, s.kbps, s.gop, s.src_spout) catch {
        emit(OpenedEv{ .sid = s.sid, .ok = false, .err = mf.lastOpenErr() });
        return;
    };
    const sink = mf.AuSink{ .ctx = @ptrCast(s), .put = &sinkPut };

    if (s.src_spout) {
        // The capture source is opened AFTER the encoder: a refusal (foreign adapter, exotic
        // format, sender resized between the parent's scan and now) must downgrade the SESSION,
        // never fail the encoder that is otherwise healthy. The parent reopens with src:"shm".
        var reason: cap.Reason = .open_shared;
        if (cap.Cap.open(s.gpa, enc.dev, enc.vdev, enc.vpe, s.share, s.sname, s.in_w, s.in_h, &reason)) |c| {
            s.cap_open = c;
            s.hdr.setCapFmt(c.fmt);
            s.hdr.setCapFlags(c.flags);
            emit(OpenedEv{ .sid = s.sid, .ok = true, .name = enc.name(), .bgra = enc.bgra_in, .src = "spout", .cap = "zerocopy" });
            spoutLoop(s, enc, sink);
            const rc = enc.drain(sink);
            std.debug.print("mfenc session {d} (zerocopy): fed={d} put={d} drain_rc={d}\n", .{ s.sid, s.fed, s.aus_put, rc });
            if (s.cap_open) |*c2| c2.close();
            s.cap_open = null;
            enc.close();
            return;
        } else |_| {
            emit(OpenedEv{ .sid = s.sid, .ok = false, .err = "zero-copy source refused", .src = "spout", .cap = "downgraded", .err_src = reason.text() });
            enc.close();
            return;
        }
    }
    emit(OpenedEv{ .sid = s.sid, .ok = true, .name = enc.name(), .bgra = enc.bgra_in });

    var last_seq: u64 = s.hdr.frameSeq(); // frames before (re)open are stale - skip
    var fed_frames: u64 = 0;
    while (true) {
        _ = WaitForSingleObject(s.ev_frame, 20);
        var want_idr = false;
        var want_kbps: u32 = 0;
        var closing = false;
        {
            s.mu.lock();
            defer s.mu.unlock();
            want_idr = s.want_idr;
            want_kbps = s.want_kbps;
            closing = s.closing;
            s.want_idr = false;
            s.want_kbps = 0;
        }
        if (closing) break;
        if (want_idr) enc.forceIDR();
        if (want_kbps > 0) _ = enc.setBitrate(want_kbps);
        const seq = s.hdr.frameSeq();
        if (seq != last_seq) {
            last_seq = seq;
            fed_frames += 1;
            s.fed += 1;
            if (fault_after_frames > 0 and fed_frames == fault_after_frames) {
                const p: *volatile u32 = @ptrFromInt(8);
                p.* = 1; // deliberate AV mid-route (test hook)
            }
            const pts = s.hdr.framePTS();
            const t0 = qpcNs();
            _ = enc.feed(s.frame, @divTrunc(pts, 100), sink);
            s.hdr.addBusy(qpcNs() - t0);
            s.hdr.setConsSeq(seq);
            _ = SetEvent(s.ev_cons);
        } else {
            _ = enc.pump(sink); // async events arrive without new frames too
        }
    }
    const rc = enc.drain(sink);
    std.debug.print("mfenc session {d}: fed={d} put={d} drain_rc={d}\n", .{ s.sid, s.fed, s.aus_put, rc });
    enc.close();
}

// Mailbox is one drained control snapshot (stdin thread → session thread).
const Mailbox = struct { idr: bool, kbps: u32, closing: bool };

fn drainMailbox(s: *Session) Mailbox {
    s.mu.lock();
    defer s.mu.unlock();
    const m = Mailbox{ .idr = s.want_idr, .kbps = s.want_kbps, .closing = s.closing };
    s.want_idr = false;
    s.want_kbps = 0;
    return m;
}

// spoutLoop is the zero-copy session body: pace, sample whatever the sender currently has,
// encode. ALLOCATION-FREE - no queue exists, so backpressure is newest-wins by construction: a
// late encode delays the next tick and the tick is then RESYNCED, never caught up (no burst).
//
// Pacing is BLIND (design §6): we do not consult Spout's IsFrameNew, which would need a bound
// receiver - the very GL/readback object this path removes. A static sender therefore encodes
// duplicate frames as near-free skipped-macroblock P-frames instead of going quiet, which keeps
// the peer's jitter buffer fed. capFrames vs AU sizes makes the cost visible.
fn spoutLoop(s: *Session, enc: *mf.Enc, sink: mf.AuSink) void {
    const cap_n: i64 = if (s.cap_n > 0) @intCast(s.cap_n) else @intCast(s.fps_n);
    const cap_d: i64 = if (s.cap_d > 0) @intCast(s.cap_d) else 1;
    const period: u64 = @intCast(@max(@divTrunc(std.time.ns_per_s * cap_d, @max(cap_n, 1)), 1_000_000));
    const qpc0 = qpcNs();
    var next = qpc0;
    var live = true; // false after srcgone: session + encoder stay alive, capture stops
    while (true) {
        // Sleep only up to the next tick (1..20 ms) - prompt for close/idr/bitrate, and never
        // a hot spin. timeBeginPeriod(1) is already set, so Sleep granularity is ~1 ms.
        const now0 = qpcNs();
        var wait_ms: u32 = 20;
        if (live and next > now0) {
            const rem = (next - now0) / std.time.ns_per_ms;
            wait_ms = @intCast(@min(@max(rem, 1), 20));
        } else if (live) {
            wait_ms = 1;
        }
        _ = WaitForSingleObject(s.ev_frame, wait_ms);
        const m = drainMailbox(s);
        if (m.closing) break;
        if (m.idr) enc.forceIDR();
        if (m.kbps > 0) _ = enc.setBitrate(m.kbps);
        if (!live) {
            _ = enc.pump(sink); // async MFT events still arrive; keep draining the tail
            continue;
        }
        const now = qpcNs();
        if (now < next) {
            _ = enc.pump(sink);
            continue;
        }
        const pts_ns: i64 = s.pts0 + @as(i64, @intCast(now - qpc0));
        const t0 = now;
        const c = &s.cap_open.?;
        switch (c.feed(enc, @divTrunc(pts_ns, 100), sink)) {
            .ok => {
                s.hdr.bumpAt(off_cap_frames);
                s.hdr.setLastCapNs(@intCast(qpcNs()));
                s.fed += 1;
                s.hdr.addBusy(qpcNs() - t0);
            },
            .timeout => s.hdr.bumpAt(off_mtx_timeouts),
            .dead => {
                s.hdr.bumpAt(off_src_errors);
                live = false;
                emit(SrcGoneEv{ .sid = s.sid, .reason = c.reason.text() });
            },
        }
        next += period;
        const after = qpcNs();
        if (after > next and after - next > 2 * period) {
            next = after; // resync, never catch up
            s.hdr.bumpAt(off_cap_skips);
        }
    }
}

// decSessionMain owns one DECODE pipeline: inbound AU ring -> decoder MFT -> VideoProcessorBlt into
// the destination sender's shared texture. Nothing but scalars and compressed AUs crosses the
// process boundary; a decoded frame never leaves the GPU.
fn decSessionMain(s: *Session) void {
    var reason: dec.Reason = .decoder_failed;
    const d = dec.Dec.open(s.gpa, s.luid, s.codec_hevc, s.in_w, s.in_h, s.out_w, s.out_h, s.fps_n, s.fps_d, s.dsh, s.dname, s.ring_size, &reason) catch {
        emit(OpenedEv{ .sid = s.sid, .ok = false, .err = "native decode refused", .dir = "dec", .cap = "downgraded", .err_dst = reason.text() });
        return;
    };
    s.dec_open = d;
    s.hdr.setU32At(off_dec_flags, d.flags);
    emit(OpenedEv{ .sid = s.sid, .ok = true, .name = d.name(), .dir = "dec", .cap = "zerocopy" });
    decLoop(s, d);
    const rc = d.drain();
    std.debug.print("mfenc session {d} (decode): fed={d} published={d} drain_rc={d}\n", .{ s.sid, d.fed_n, d.pub_n, rc });
    s.dec_open = null;
    d.close();
}

// AuRec is one record read out of the inbound ring; data points INTO the ring and stays valid until
// inRead is advanced past it (the parent never writes past write-read <= ring_size).
const AuRec = struct { data: []const u8, pts_ns: i64, key: bool, rec: u64 };

// ringTake reads the next inbound AU without consuming it. null = ring empty / nothing readable.
fn ringTake(s: *Session) ?AuRec {
    var hops: u32 = 0;
    while (hops < 2) : (hops += 1) { // at most one wrap hop per call: the ring base always follows
        const r = s.hdr.inRead();
        const w = s.hdr.inWrite();
        if (r >= w) return null;
        const tail = s.ring_size - (r % s.ring_size);
        if (tail < 16) { // implicit wrap: no record header fits in the tail
            s.hdr.setInRead(r + tail);
            continue;
        }
        const pos: usize = @intCast(r % s.ring_size);
        const lp: *align(1) const u32 = @ptrCast(s.ring + pos);
        const ln = lp.*;
        if (ln == wrap_marker) {
            s.hdr.setInRead(r + tail);
            continue;
        }
        if (ln == 0 or @as(u64, ln) + 16 > tail) return null; // torn/corrupt: read nothing
        const fp: *align(1) const u32 = @ptrCast(s.ring + pos + 4);
        const pp: *align(1) const i64 = @ptrCast(s.ring + pos + 8);
        return .{
            .data = (s.ring + pos + 16)[0..ln],
            .pts_ns = pp.*,
            .key = fp.* & 1 != 0,
            .rec = 16 + std.mem.alignForward(u64, ln, 8),
        };
    }
    return null;
}

// dec_batch bounds the AUs consumed per wake so close/quit stay prompt. A decoder that cannot keep
// up backpressures through the ring, which the PARENT bounds by dropping (inDropped) - never by
// accumulating.
const dec_batch = 8;

fn decLoop(s: *Session, d: *dec.Dec) void {
    var live = true; // false after dstgone: session + decoder stay up, publishing stops
    while (true) {
        _ = WaitForSingleObject(s.ev_frame, 20);
        const m = drainMailbox(s);
        if (m.closing) break;
        if (!live) {
            _ = d.pump(); // async MFT events still arrive; keep the tail draining
            continue;
        }
        var n: u32 = 0;
        while (n < dec_batch) : (n += 1) {
            const rec = ringTake(s) orelse break;
            const t0 = qpcNs();
            const rc = d.feed(rec.data, @divTrunc(rec.pts_ns, 100));
            // Advance ONLY after feed copied the AU out of the ring.
            s.hdr.setInRead(s.hdr.inRead() + rec.rec);
            _ = SetEvent(s.ev_cons);
            s.hdr.addAt(off_dec_busy, qpcNs() - t0);
            s.fed += 1;
            if (rc < 0) {
                s.hdr.addAt(off_dec_errors, 1);
                live = false;
                emit(DstGoneEv{ .sid = s.sid, .reason = d.reason.text() });
                break;
            }
            if (rc > 0) s.hdr.addAt(off_dec_dropped, 1);
            if (d.pub_n > s.aus_put) {
                if (probe_bands and s.aus_put == 0) d.probeBands(); // one shot, first published frame
                s.aus_put = d.pub_n;
                s.hdr.setU64At(off_dec_frames, d.pub_n);
                s.hdr.setI64At(off_last_pub_ns, @intCast(qpcNs()));
            }
            s.hdr.setU64At(off_dec_mtx_timeouts, d.mtx_timeouts);
        }
        if (n == 0) _ = d.pump();
    }
}

fn teardownSession(s: *Session) void {
    closeShm(s); // pipeline is closed by now; the parent holds its own view of the mapping
    emit(struct { ev: []const u8 = "closed", sid: u32 }{ .sid = s.sid });
}

// closeSession signals + joins one session off the dispatch thread, then frees it.
fn closeSession(s: *Session) void {
    s.mu.lock();
    s.closing = true;
    s.mu.unlock();
    if (s.thread) |t| t.join();
    s.gpa.free(s.shm_name);
    s.gpa.free(s.sname);
    s.gpa.free(s.dname);
    s.gpa.destroy(s);
}

const Cmd = struct {
    op: []const u8 = "",
    sid: u32 = 0,
    shm: []const u8 = "",
    in_w: i32 = 0,
    in_h: i32 = 0,
    out_w: i32 = 0,
    out_h: i32 = 0,
    fps_n: i32 = 30,
    fps_d: i32 = 1,
    kbps: i32 = 0,
    gop: i32 = 0,
    // src:"spout" zero-copy fields (all optional; absent = today's SHM frame ring).
    src: []const u8 = "shm",
    sh: u64 = 0,
    sfmt: u32 = 0,
    sname: []const u8 = "",
    cap_n: i32 = 0,
    cap_d: i32 = 0,
    ring_kb: u32 = 0,
    pts0: i64 = 0,
    // dir:"dec" receive-side fields (absent = an encode session, i.e. everything before inc 2).
    dir: []const u8 = "enc",
    codec: []const u8 = "h264",
    dsh: u64 = 0,
    dfmt: u32 = 0,
    dname: []const u8 = "",
    in_ring_kb: u32 = 0,
};

pub fn main(init: std.process.Init) !void {
    const gpa = init.gpa;

    var out_buf: [16 * 1024]u8 = undefined;
    var stdout_writer = std.Io.File.stdout().writerStreaming(init.io, &out_buf);
    g_out = &stdout_writer.interface;

    var luid: i64 = 0;
    var args = try std.process.Args.Iterator.initAllocator(init.minimal.args, gpa);
    defer args.deinit();
    _ = args.next(); // exe
    if (args.next()) |a| luid = std.fmt.parseInt(i64, a, 10) catch 0;

    // Test hook: field failure mode - AV on a thread no in-process guard can reach. The
    // process boundary IS the containment; the parent must survive + restart us.
    if (init.environ_map.get("RAVE_MATE_MFENC_FAULT_INJECT_THREAD") != null) {
        const t = try std.Thread.spawn(.{}, faultThread, .{});
        t.join(); // AV fires in faultThread → process dies before join returns
    }
    if (init.environ_map.get("RAVE_MATE_MFENC_FAULT_AFTER_FRAMES")) |v| {
        fault_after_frames = std.fmt.parseInt(u64, v, 10) catch 0;
    }
    if (init.environ_map.get("RAVE_MATE_MFDEC_PROBE_BANDS")) |v| {
        probe_bands = std.mem.eql(u8, v, "1");
    }

    _ = timeBeginPeriod(1); // media child: 1 ms scheduler quantum (Sleep(1) is ~15.6 ms without it)
    // ver 2 = src:"spout" + header v2; ver 3 = dir:"dec" (the inbound-ring layout). The version gate
    // is the ONLY thing standing between an older child and a mapping it would size wrong: a v2
    // child ignores an unknown "dir" and would open an ENCODE session sized from in_w*in_h*4, i.e.
    // past the end of the parent's much smaller dec mapping.
    emit(struct { ev: []const u8 = "hello", ver: u32 = 3, luid: i64 }{ .luid = luid });

    var sessions = std.AutoHashMap(u32, *Session).init(gpa);
    defer sessions.deinit();

    var in_buf: [16 * 1024]u8 = undefined;
    var stdin_reader = std.Io.File.stdin().readerStreaming(init.io, &in_buf);
    const in = &stdin_reader.interface;

    var arena_state: std.heap.ArenaAllocator = .init(gpa);
    defer arena_state.deinit();

    while (true) {
        const line = in.takeDelimiter('\n') catch |err| switch (err) {
            error.StreamTooLong => {
                _ = in.discardDelimiterInclusive('\n') catch break;
                continue;
            },
            error.ReadFailed => break,
        } orelse break; // EOF: parent died/closed → shut down
        const trimmed = std.mem.trim(u8, line, " \t\r");
        if (trimmed.len == 0) continue;
        _ = arena_state.reset(.retain_capacity);
        const cmd = std.json.parseFromSliceLeaky(Cmd, arena_state.allocator(), trimmed, .{
            .ignore_unknown_fields = true,
        }) catch continue;

        if (std.mem.eql(u8, cmd.op, "quit")) break;
        if (std.mem.eql(u8, cmd.op, "open")) {
            if (sessions.contains(cmd.sid)) continue;
            if (cmd.in_w <= 0 or cmd.in_h <= 0 or cmd.in_w > 16384 or cmd.in_h > 16384 or cmd.shm.len == 0) {
                emit(OpenedEv{ .sid = cmd.sid, .ok = false, .err = "bad open args" });
                continue;
            }
            const spout = std.mem.eql(u8, cmd.src, "spout");
            if (spout and (cmd.sh == 0 or cmd.sname.len == 0 or cmd.sname.len > 256 or
                cmd.ring_kb < ring_kb_min or cmd.ring_kb > ring_kb_max))
            {
                emit(OpenedEv{ .sid = cmd.sid, .ok = false, .err = "bad spout open args", .src = "spout", .cap = "downgraded", .err_src = "open_shared" });
                continue;
            }
            const dir_dec = std.mem.eql(u8, cmd.dir, "dec");
            if (dir_dec and (cmd.dsh == 0 or cmd.dname.len == 0 or cmd.dname.len > 256 or
                cmd.out_w <= 0 or cmd.out_h <= 0 or
                cmd.in_ring_kb < ring_kb_min or cmd.in_ring_kb > ring_kb_max))
            {
                emit(OpenedEv{ .sid = cmd.sid, .ok = false, .err = "bad dec open args", .dir = "dec", .cap = "downgraded", .err_dst = "open_shared" });
                continue;
            }
            const s = try gpa.create(Session);
            s.* = .{
                .gpa = gpa,
                .sid = cmd.sid,
                .luid = luid,
                .in_w = cmd.in_w,
                .in_h = cmd.in_h,
                .out_w = cmd.out_w,
                .out_h = cmd.out_h,
                .fps_n = cmd.fps_n,
                .fps_d = cmd.fps_d,
                .kbps = cmd.kbps,
                .gop = cmd.gop,
                .shm_name = try gpa.dupe(u8, cmd.shm),
                .src_spout = spout,
                .share = cmd.sh,
                .sfmt = cmd.sfmt,
                .sname = try gpa.dupe(u8, cmd.sname),
                .cap_n = cmd.cap_n,
                .cap_d = cmd.cap_d,
                .ring_kb = cmd.ring_kb,
                .pts0 = cmd.pts0,
                .dir_dec = dir_dec,
                .codec_hevc = std.mem.eql(u8, cmd.codec, "hevc"),
                .dsh = cmd.dsh,
                .dfmt = cmd.dfmt,
                .dname = try gpa.dupe(u8, cmd.dname),
                .in_ring_kb = cmd.in_ring_kb,
            };
            s.thread = try std.Thread.spawn(.{}, sessionMain, .{s});
            try sessions.put(cmd.sid, s);
            continue;
        }
        const s = sessions.get(cmd.sid) orelse continue;
        if (std.mem.eql(u8, cmd.op, "close")) {
            // Session independence: drain+join can take ~2 s (FEED_WAIT_MS) - never on the
            // dispatch thread, or every other session's ops on this adapter stall. Remove
            // from the map FIRST (no double-join from the shutdown sweep), then a detached
            // closer joins + frees; teardownSession (session thread) emits "closed" +
            // releases the shm handles before the closer frees the struct.
            _ = sessions.remove(cmd.sid);
            const closer = std.Thread.spawn(.{}, closeSession, .{s}) catch {
                closeSession(s); // spawn failed (rare): close inline rather than leak
                continue;
            };
            closer.detach();
        } else if (std.mem.eql(u8, cmd.op, "bitrate")) {
            s.mu.lock();
            s.want_kbps = if (cmd.kbps > 0) @intCast(cmd.kbps) else 0;
            s.mu.unlock();
        } else if (std.mem.eql(u8, cmd.op, "idr")) {
            s.mu.lock();
            s.want_idr = true;
            s.mu.unlock();
        }
    }

    // shut every session down cleanly (drain tails) before exit. Sessions being closed
    // by a detached closer are already OUT of the map - no double-join.
    var it = sessions.valueIterator();
    while (it.next()) |sp| closeSession(sp.*);
}

fn faultThread() void {
    const p: *volatile u32 = @ptrFromInt(8);
    p.* = 1;
}

test {
    _ = @import("mf.zig");
    _ = @import("cap.zig");
    _ = @import("dec.zig");
}

test "spout ring bounds are the bitrate-derived window, geometry-independent" {
    // 4K60 @ 50 Mbps → half a second of bitstream → the 4 MiB floor; 8K/200 Mbps stays under
    // the 16 MiB ceiling. Both ends clamp to the same constants (parent: ringKB in mfenc).
    try std.testing.expectEqual(@as(u32, 4 * 1024), ring_kb_min);
    try std.testing.expectEqual(@as(u32, 16 * 1024), ring_kb_max);
    try std.testing.expectEqual(@as(u32, 0x32464D52), hdr_magic); // 'RMF2'
}
