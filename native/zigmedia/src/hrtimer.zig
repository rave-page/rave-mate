//! High-resolution monotonic clock + wait for PTS-locked playout and pacing.
//! Linux: CLOCK_MONOTONIC + clock_nanosleep(ABSTIME), sub-ms.
//! Windows: QueryPerformanceCounter + a per-thread CreateWaitableTimerExW(HIGH_RESOLUTION) timer,
//! then a short spin to the exact deadline - the Go path's time.Timer wake (~1-15 ms) was a stutter
//! cause; this keeps release jitter well under 1 ms without timeBeginPeriod.
const std = @import("std");
const builtin = @import("builtin");
const linux = std.os.linux;
const is_windows = builtin.os.tag == .windows;

const win = struct {
    extern "kernel32" fn QueryPerformanceCounter(out: *i64) callconv(.winapi) i32;
    extern "kernel32" fn QueryPerformanceFrequency(out: *i64) callconv(.winapi) i32;
    extern "kernel32" fn CreateWaitableTimerExW(attr: ?*anyopaque, name: ?[*:0]const u16, flags: u32, access: u32) callconv(.winapi) ?*anyopaque;
    extern "kernel32" fn SetWaitableTimer(t: *anyopaque, due: *const i64, period: i32, cb: ?*anyopaque, arg: ?*anyopaque, resume_: i32) callconv(.winapi) i32;
    extern "kernel32" fn WaitForSingleObject(h: *anyopaque, ms: u32) callconv(.winapi) u32;
    extern "kernel32" fn Sleep(ms: u32) callconv(.winapi) void;
    const CREATE_WAITABLE_TIMER_HIGH_RESOLUTION: u32 = 0x2;
    const TIMER_ALL_ACCESS: u32 = 0x1F0003;
};

var qpc_freq: std.atomic.Value(i64) = .init(0);
threadlocal var thread_timer: ?*anyopaque = null;
threadlocal var thread_timer_tried: bool = false;

const spin_ns: i64 = 300_000; // final stretch spun (timer wake granularity ~0.5 ms)

/// nowMono returns monotonic nanoseconds.
pub fn nowMono() i64 {
    if (builtin.os.tag == .linux) {
        var ts: linux.timespec = undefined;
        _ = linux.clock_gettime(.MONOTONIC, &ts);
        return @as(i64, @intCast(ts.sec)) * 1_000_000_000 + @as(i64, @intCast(ts.nsec));
    }
    if (is_windows) {
        var f = qpc_freq.load(.monotonic);
        if (f == 0) {
            _ = win.QueryPerformanceFrequency(&f);
            qpc_freq.store(f, .monotonic);
        }
        var c: i64 = 0;
        _ = win.QueryPerformanceCounter(&c);
        // split to avoid i64 overflow of c * 1e9
        const s = @divTrunc(c, f);
        const r = @mod(c, f);
        return s * 1_000_000_000 + @divTrunc(r * 1_000_000_000, f);
    }
    return 0;
}

/// sleepUntil waits until the monotonic deadline (ns); returns promptly if already past.
pub fn sleepUntil(deadline_ns: i64) void {
    if (builtin.os.tag == .linux) {
        var ts = linux.timespec{
            .sec = @intCast(@divTrunc(deadline_ns, 1_000_000_000)),
            .nsec = @intCast(@mod(deadline_ns, 1_000_000_000)),
        };
        _ = linux.clock_nanosleep(.MONOTONIC, .{ .ABSTIME = true }, &ts, null);
        return;
    }
    if (is_windows) {
        const rem = deadline_ns - nowMono();
        if (rem > spin_ns) {
            if (!thread_timer_tried) {
                thread_timer_tried = true;
                thread_timer = win.CreateWaitableTimerExW(null, null, win.CREATE_WAITABLE_TIMER_HIGH_RESOLUTION, win.TIMER_ALL_ACCESS);
            }
            if (thread_timer) |t| {
                const due: i64 = -@divTrunc(rem - spin_ns, 100); // relative, 100 ns units
                if (win.SetWaitableTimer(t, &due, 0, null, null, 0) != 0) _ = win.WaitForSingleObject(t, 0xFFFFFFFF);
            } else {
                win.Sleep(@intCast(@divTrunc(rem - spin_ns, 1_000_000))); // old OS fallback (coarse)
            }
        }
    }
    while (nowMono() < deadline_ns) std.atomic.spinLoopHint();
}

test "sleepUntil returns promptly when past" {
    const t0 = nowMono();
    sleepUntil(t0 - 1_000_000); // already past
    try std.testing.expect(nowMono() - t0 < 50_000_000);
}

test "sleepUntil waits to the deadline with sub-ms precision" {
    // Median overshoot (a loaded shared PC can preempt a few samples; the coarse ~15.6 ms timer
    // this replaces would put the MEDIAN far above 2 ms).
    var over: [21]i64 = undefined;
    for (&over) |*o| {
        const t0 = nowMono();
        sleepUntil(t0 + 5_000_000); // 5 ms
        const slept = nowMono() - t0;
        try std.testing.expect(slept >= 5_000_000);
        o.* = slept - 5_000_000;
    }
    std.mem.sort(i64, &over, {}, std.sort.asc(i64));
    try std.testing.expect(over[over.len / 2] < 2_000_000);
}