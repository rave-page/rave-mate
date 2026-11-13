//! Minimal UDP socket layer for the native sender/receiver: Linux raw syscalls, Windows Winsock
//! (ws2_32, declared here - the 0.16 std has no Winsock surface). Non-blocking receive; send never
//! blocks the realtime thread on a full socket buffer (returns 0 = datagram not sent).
const std = @import("std");
const builtin = @import("builtin");
const is_windows = builtin.os.tag == .windows;
const linux = std.os.linux;

/// Fd is the OS socket handle (Windows SOCKET / POSIX fd).
pub const Fd = if (is_windows) usize else i32;
pub const invalid: Fd = if (is_windows) ~@as(usize, 0) else -1;

/// Addr4 is a sockaddr_in (identical layout on Linux and Windows): port + addr in network order.
pub const Addr4 = extern struct {
    family: u16 = 2, // AF_INET
    port_be: u16 = 0,
    addr: [4]u8 = .{ 0, 0, 0, 0 },
    zero: [8]u8 = [_]u8{0} ** 8,

    pub fn loopback(p: u16) Addr4 {
        return .{ .port_be = std.mem.nativeToBig(u16, p), .addr = .{ 127, 0, 0, 1 } };
    }
    pub fn port(a: Addr4) u16 {
        return std.mem.bigToNative(u16, a.port_be);
    }
};

const win = struct {
    const WSADATA = extern struct { data: [408]u8 };
    extern "ws2_32" fn WSAStartup(ver: u16, data: *WSADATA) callconv(.winapi) i32;
    extern "ws2_32" fn WSAGetLastError() callconv(.winapi) i32;
    extern "ws2_32" fn socket(af: i32, typ: i32, proto: i32) callconv(.winapi) usize;
    extern "ws2_32" fn bind(s: usize, addr: *const anyopaque, len: i32) callconv(.winapi) i32;
    extern "ws2_32" fn connect(s: usize, addr: *const anyopaque, len: i32) callconv(.winapi) i32;
    extern "ws2_32" fn getsockname(s: usize, addr: *anyopaque, len: *i32) callconv(.winapi) i32;
    extern "ws2_32" fn setsockopt(s: usize, level: i32, opt: i32, val: [*]const u8, len: i32) callconv(.winapi) i32;
    extern "ws2_32" fn ioctlsocket(s: usize, cmd: i32, arg: *u32) callconv(.winapi) i32;
    extern "ws2_32" fn WSAIoctl(s: usize, code: u32, in: ?*const anyopaque, in_len: u32, out: ?*anyopaque, out_len: u32, ret: *u32, ov: ?*anyopaque, cr: ?*anyopaque) callconv(.winapi) i32;
    extern "ws2_32" fn send(s: usize, buf: [*]const u8, len: i32, flags: i32) callconv(.winapi) i32;
    extern "ws2_32" fn sendto(s: usize, buf: [*]const u8, len: i32, flags: i32, to: *const anyopaque, tolen: i32) callconv(.winapi) i32;
    extern "ws2_32" fn recvfrom(s: usize, buf: [*]u8, len: i32, flags: i32, from: ?*anyopaque, fromlen: ?*i32) callconv(.winapi) i32;
    extern "ws2_32" fn closesocket(s: usize) callconv(.winapi) i32;
    const FIONBIO: i32 = @bitCast(@as(u32, 0x8004667e));
    const SIO_UDP_CONNRESET: u32 = 0x9800000C;
    const SOL_SOCKET: i32 = 0xffff;
    const SO_RCVBUF: i32 = 0x1002;
    const SO_SNDBUF: i32 = 0x1001;
    const WSAEWOULDBLOCK = 10035;
    const WSAECONNRESET = 10054;
    const WSAEMSGSIZE = 10040;
};

var wsa_done: std.atomic.Value(bool) = .init(false);
fn wsaInit() void {
    if (wsa_done.swap(true, .acq_rel)) return; // WSAStartup is refcounted + thread-safe; once is enough
    var d: win.WSADATA = undefined;
    _ = win.WSAStartup(0x0202, &d);
}

/// udpSocket opens an IPv4 UDP socket with large kernel buffers (8 MiB rcv / 4 MiB snd: WE never add
/// drops under a burst). Receive is always non-blocking; send waits briefly for buffer space.
pub fn udpSocket() !Fd {
    if (is_windows) {
        wsaInit();
        const s = win.socket(2, 2, 17); // AF_INET, SOCK_DGRAM, IPPROTO_UDP
        if (s == invalid) return error.Socket;
        var one: u32 = 1;
        _ = win.ioctlsocket(s, win.FIONBIO, &one);
        // An ICMP port-unreachable would otherwise surface as WSAECONNRESET on the next recvfrom.
        var off: u32 = 0;
        var ret: u32 = 0;
        _ = win.WSAIoctl(s, win.SIO_UDP_CONNRESET, &off, 4, null, 0, &ret, null, null);
        setBufs(s, 8 << 20, 4 << 20);
        return s;
    }
    const r = linux.socket(linux.AF.INET, linux.SOCK.DGRAM, 0); // blocking send; recv uses MSG_DONTWAIT
    if (linux.errno(r) != .SUCCESS) return error.Socket;
    const fd: i32 = @intCast(r);
    setBufs(fd, 8 << 20, 4 << 20);
    return fd;
}

pub fn bind(fd: Fd, a: *const Addr4) !void {
    if (is_windows) {
        if (win.bind(fd, a, @sizeOf(Addr4)) != 0) return error.Bind;
        return;
    }
    if (linux.errno(linux.bind(fd, @ptrCast(a), @sizeOf(Addr4))) != .SUCCESS) return error.Bind;
}

/// connect fixes the default destination (send) for a raw sockaddr (in/in6).
pub fn connect(fd: Fd, addr: *const anyopaque, len: u32) !void {
    if (is_windows) {
        if (win.connect(fd, addr, @intCast(len)) != 0) return error.Connect;
        return;
    }
    if (linux.errno(linux.connect(fd, addr, len)) != .SUCCESS) return error.Connect;
}

pub fn localAddr(fd: Fd) !Addr4 {
    var a: Addr4 = .{};
    if (is_windows) {
        var l: i32 = @sizeOf(Addr4);
        if (win.getsockname(fd, &a, &l) != 0) return error.GetSockName;
        return a;
    }
    var l: linux.socklen_t = @sizeOf(Addr4);
    if (linux.errno(linux.getsockname(fd, @ptrCast(&a), &l)) != .SUCCESS) return error.GetSockName;
    return a;
}

/// setBufs sets SO_RCVBUF / SO_SNDBUF (best effort; 0 = leave).
pub fn setBufs(fd: Fd, rcv: i32, snd: i32) void {
    const pairs = [_][2]i32{ .{ if (is_windows) win.SO_RCVBUF else linux.SO.RCVBUF, rcv }, .{ if (is_windows) win.SO_SNDBUF else linux.SO.SNDBUF, snd } };
    for (pairs) |p| {
        if (p[1] <= 0) continue;
        const b = std.mem.asBytes(&p[1]);
        if (is_windows) {
            _ = win.setsockopt(fd, win.SOL_SOCKET, p[0], b.ptr, @intCast(b.len));
        } else {
            _ = linux.setsockopt(fd, linux.SOL.SOCKET, @intCast(p[0]), b.ptr, @intCast(b.len));
        }
    }
}

/// send writes one datagram to the connected peer; 0 = not sent.
pub fn send(fd: Fd, buf: []const u8) usize {
    if (is_windows) {
        var spins: usize = 0;
        while (true) : (spins += 1) { // non-blocking socket: wait out a momentarily full send buffer
            const r = win.send(fd, buf.ptr, @intCast(buf.len), 0);
            if (r > 0) return @intCast(r);
            if (win.WSAGetLastError() != win.WSAEWOULDBLOCK or spins > 20_000) return 0;
            std.atomic.spinLoopHint();
        }
    }
    const r = linux.sendto(fd, buf.ptr, buf.len, 0, null, 0);
    if (linux.errno(r) != .SUCCESS) return 0;
    return r;
}

/// sendTo writes one datagram to a; 0 = not sent.
pub fn sendTo(fd: Fd, buf: []const u8, a: *const Addr4) usize {
    if (is_windows) {
        var spins: usize = 0;
        while (true) : (spins += 1) {
            const r = win.sendto(fd, buf.ptr, @intCast(buf.len), 0, a, @sizeOf(Addr4));
            if (r > 0) return @intCast(r);
            if (win.WSAGetLastError() != win.WSAEWOULDBLOCK or spins > 20_000) return 0;
            std.atomic.spinLoopHint();
        }
    }
    const r = linux.sendto(fd, buf.ptr, buf.len, 0, @ptrCast(a), @sizeOf(Addr4));
    if (linux.errno(r) != .SUCCESS) return 0;
    return r;
}

/// recvFrom reads one datagram without blocking (error.WouldBlock when none). from may be null.
pub fn recvFrom(fd: Fd, buf: []u8, from: ?*Addr4) !usize {
    if (is_windows) {
        while (true) {
            var l: i32 = @sizeOf(Addr4);
            const r = win.recvfrom(fd, buf.ptr, @intCast(buf.len), 0, if (from) |f| @ptrCast(f) else null, if (from != null) &l else null);
            if (r >= 0) return @intCast(r);
            switch (win.WSAGetLastError()) {
                win.WSAEWOULDBLOCK => return error.WouldBlock,
                win.WSAECONNRESET, win.WSAEMSGSIZE => continue, // stray ICMP / oversize: skip it
                else => return error.Recv,
            }
        }
    }
    var l: linux.socklen_t = @sizeOf(Addr4);
    const r = linux.recvfrom(fd, buf.ptr, buf.len, linux.MSG.DONTWAIT, if (from) |f| @ptrCast(f) else null, if (from != null) &l else null);
    switch (linux.errno(r)) {
        .SUCCESS => return r,
        .AGAIN => return error.WouldBlock,
        else => return error.Recv,
    }
}

pub fn close(fd: Fd) void {
    if (is_windows) {
        _ = win.closesocket(fd);
    } else {
        _ = linux.close(fd);
    }
}

test "loopback datagram round trip (non-blocking)" {
    const a = try udpSocket();
    defer close(a);
    const b = try udpSocket();
    defer close(b);
    var la = Addr4.loopback(0);
    try bind(a, &la);
    const got = try localAddr(a);
    try std.testing.expect(got.port() != 0);
    var probe: [16]u8 = undefined;
    try std.testing.expectError(error.WouldBlock, recvFrom(a, &probe, null));
    try std.testing.expectEqual(@as(usize, 5), sendTo(b, "hello", &got));
    var buf: [64]u8 = undefined;
    var from: Addr4 = .{};
    var n: usize = 0;
    var tries: usize = 0;
    while (tries < 1000) : (tries += 1) {
        n = recvFrom(a, &buf, &from) catch |e| switch (e) {
            error.WouldBlock => {
                @import("hrtimer.zig").sleepUntil(@import("hrtimer.zig").nowMono() + 1_000_000);
                continue;
            },
            else => return e,
        };
        break;
    }
    try std.testing.expectEqualSlices(u8, "hello", buf[0..n]);
}
