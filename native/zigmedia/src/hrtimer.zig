//! High-resolution monotonic wait for PTS-locked playout. Linux: clock_nanosleep(ABSTIME) on
//! CLOCK_MONOTONIC - no coarse timer, sub-ms. Windows (increment 4 integration): will use
//! CreateWaitableTimerEx(CREATE_WAITABLE_TIMER_HIGH_RESOLUTION); until then a short spin-to-deadline
//! keeps the build cross-target. This is why playout moves native: the Go path's time.Timer is coarse
//! on Windows (~15.6 ms), the stutter's wake cause.
const std = @import("std");
const builtin = @import("builtin");
const linux = std.os.linux;

/// nowMono returns monotonic nanoseconds.
pub fn nowMono() i64 {
    if (builtin.os.tag == .linux) {
        var ts: linux.timespec = undefined;
        _ = linux.clock_gettime(.MONOTONIC, &ts);
        return @as(i64, @intCast(ts.sec)) * 1_000_000_000 + @as(i64, @intCast(ts.nsec));
    }
    if (builtin.os.tag == .windows) {
        // Coarse monotonic fallback for the host build; the real Windows receiver (increment 4) uses
        // CreateWaitableTimerEx + QueryPerformanceCounter.
        return @as(i64, @intCast(std.os.windows.kernel32.GetTickCount64())) * 1_000_000;
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
        // CLOCK_MONOTONIC(1), TIMER_ABSTIME(1)
        _ = linux.clock_nanosleep(.MONOTONIC, .{ .ABSTIME = true }, &ts, null);
        return;
    }
    // Portable fallback (Windows high-res timer lands in increment 4): spin the last stretch.
    while (nowMono() < deadline_ns) std.atomic.spinLoopHint();
}

test "sleepUntil returns promptly when past" {
    if (builtin.os.tag != .linux) return error.SkipZigTest; // exercises clock_nanosleep ABSTIME
    const t0 = nowMono();
    sleepUntil(t0 - 1_000_000); // already past
    try std.testing.expect(nowMono() - t0 < 50_000_000);
}

test "sleepUntil waits ~to the deadline with sub-ms precision (Linux)" {
    if (builtin.os.tag != .linux) return error.SkipZigTest;
    const t0 = nowMono();
    sleepUntil(t0 + 5_000_000); // 5ms
    const slept = nowMono() - t0;
    try std.testing.expect(slept >= 5_000_000);
    try std.testing.expect(slept < 7_000_000); // within ~2ms (high-res, not the ~15ms coarse timer)
}
