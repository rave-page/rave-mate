//! Thin Win32 sync primitives (SRWLOCK + CONDITION_VARIABLE + Sleep). Zig 0.16 moved std's
//! blocking Mutex/Condition/sleep onto the std.Io runtime; this exe is Win32-native and its
//! locks are held across wndproc/COM callbacks, so the OS primitives are the honest fit.

extern "kernel32" fn AcquireSRWLockExclusive(*usize) callconv(.winapi) void;
extern "kernel32" fn ReleaseSRWLockExclusive(*usize) callconv(.winapi) void;
extern "kernel32" fn WakeConditionVariable(*usize) callconv(.winapi) void;
extern "kernel32" fn WakeAllConditionVariable(*usize) callconv(.winapi) void;
extern "kernel32" fn SleepConditionVariableSRW(*usize, *usize, u32, u32) callconv(.winapi) i32;
extern "kernel32" fn Sleep(u32) callconv(.winapi) void;

pub const Lock = struct {
    srw: usize = 0,

    pub fn lock(l: *Lock) void {
        AcquireSRWLockExclusive(&l.srw);
    }

    pub fn unlock(l: *Lock) void {
        ReleaseSRWLockExclusive(&l.srw);
    }
};

pub const Cond = struct {
    cv: usize = 0,

    /// wait releases l, blocks until a wake, and re-acquires l.
    pub fn wait(c: *Cond, l: *Lock) void {
        _ = SleepConditionVariableSRW(&c.cv, &l.srw, 0xffffffff, 0);
    }

    pub fn signal(c: *Cond) void {
        WakeConditionVariable(&c.cv);
    }

    pub fn broadcast(c: *Cond) void {
        WakeAllConditionVariable(&c.cv);
    }
};

pub fn sleepMs(ms: u32) void {
    Sleep(ms);
}
