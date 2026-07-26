//! rave-mate-enc - per-adapter Media Foundation H.264 encoder child (Zig, no cgo).
//! One child per adapter; N encode sessions multiplex inside it. A vendor-driver fault
//! kills only THIS process - the media child supervises + restarts (crash containment by
//! process boundary, the Phase-1 hard requirement).
//!
//! Control plane (newline JSON): stdin ops
//!   {"op":"open","sid":1,"shm":"Local\\rvmfenc-P-S","in_w":..,"in_h":..,"out_w":..,
//!    "out_h":..,"fps_n":..,"fps_d":..,"kbps":..,"gop":..}
//!   {"op":"close","sid":1} | {"op":"bitrate","sid":1,"kbps":..} | {"op":"idr","sid":1}
//!   {"op":"quit"}
//! stdout events: {"ev":"hello","ver":1,"luid":..} {"ev":"opened","sid":..,"ok":..,
//!   "err":..,"name":..,"bgra":..} {"ev":"closed","sid":..}
//!
//! Data plane per session (named shared memory, parent creates, we open):
//!   header 256 B: 0 magic 'RMF2' u32 | 4 ver u32 | 8 frameSeq u64 (parent) |
//!     16 framePTS i64 ns (parent) | 24 consSeq u64 (child) | 32 auWrite u64 (child,
//!     virtual offset) | 40 auRead u64 (parent) | 48 auDropped u64 | 56 encBusyNs u64
//!   frame slot @256 (in_w*in_h*4 RGBA), AU ring after it.
//!   AU record: u32 len | u32 flags(bit0 key) | i64 pts ns | data | pad to 8.
//!   len 0xFFFFFFFF = wrap marker; tail < 16 = implicit wrap. Ring full → drop + count
//!   (bounded by design; parent drains on evAU).
//! Events (auto-reset, parent creates): <shm>-f frame ready, <shm>-c frame consumed,
//!   <shm>-a AU appended.

const std = @import("std");
const mf = @import("mf.zig");

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
};

// fault_after_frames > 0: crash the process after N encoded frames (test hook - proves
// route continuity across an encoder-child death; parent injects on first spawn only).
var fault_after_frames: u64 = 0;

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

// openShm maps the session's shared memory + events created by the parent.
fn openShm(s: *Session) !void {
    const gpa = s.gpa;
    const frame_bytes: u64 = @as(u64, @intCast(s.in_w)) * @as(u64, @intCast(s.in_h)) * 4;
    var ring: u64 = 8 << 20;
    if (frame_bytes > ring) ring = frame_bytes;
    const total: usize = @intCast(hdr_size + frame_bytes + ring);

    const wname = try utf16Z(gpa, s.shm_name);
    defer gpa.free(wname);
    const mapping = OpenFileMappingW(FILE_MAP_ALL_ACCESS, 0, wname) orelse return error.ShmOpen;
    s.mapping = mapping;
    const view = MapViewOfFile(mapping, FILE_MAP_ALL_ACCESS, 0, 0, total) orelse return error.ShmMap;
    s.view = view;
    s.hdr = .{ .base = view };
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
};

// sessionMain owns one pipeline on one thread (COM MTA).
fn sessionMain(s: *Session) void {
    mf.coInitMTA();
    defer teardownSession(s);

    openShm(s) catch |e| {
        emit(OpenedEv{ .sid = s.sid, .ok = false, .err = @errorName(e) });
        return;
    };
    const enc = mf.Enc.open(s.gpa, s.luid, s.in_w, s.in_h, s.out_w, s.out_h, s.fps_n, s.fps_d, s.kbps, s.gop) catch {
        emit(OpenedEv{ .sid = s.sid, .ok = false, .err = mf.lastOpenErr() });
        return;
    };
    emit(OpenedEv{ .sid = s.sid, .ok = true, .name = enc.name(), .bgra = enc.bgra_in });

    const sink = mf.AuSink{ .ctx = @ptrCast(s), .put = &sinkPut };
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

    _ = timeBeginPeriod(1); // media child: 1 ms scheduler quantum (Sleep(1) is ~15.6 ms without it)
    emit(struct { ev: []const u8 = "hello", ver: u32 = 1, luid: i64 }{ .luid = luid });

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
}
