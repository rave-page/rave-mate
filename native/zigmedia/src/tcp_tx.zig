//! Native TCP sender transport (the legacy medialink TCP wire, byte-exact with transport.go Conn):
//!   [4-byte BE length][AES-256-GCM(frame) | frame]   - no AAD, implicit counter nonce
//!   nonce = 4 zero bytes || u64 BE counter, one counter per direction (wirecrypto.Sealer).
//! Keys = HKDF(master, routeSalt, "medialink c2s v1"/"medialink s2c v1"); the ACCEPTING end (the
//! sender/answerer, initiator=false) seals s2c and opens c2s. Go accepts the route connection, reads
//! the session preamble, then hands the connected socket to the native sender (rm_open_params
//! .socket_fd) - from then on only the native TX thread reads/writes it (one counter per direction).
//! Allocation-free after init: one output buffer (largest frame + tag), a bounded control-RX buffer.
const std = @import("std");
const builtin = @import("builtin");
const keys = @import("keys.zig");
const aead = @import("aead.zig");
const sock = @import("sock.zig");
const linux = std.os.linux;
const is_windows = builtin.os.tag == .windows;

const win = struct {
    extern "ws2_32" fn send(s: usize, buf: [*]const u8, len: i32, flags: i32) callconv(.winapi) i32;
    extern "ws2_32" fn recv(s: usize, buf: [*]u8, len: i32, flags: i32) callconv(.winapi) i32;
    extern "ws2_32" fn ioctlsocket(s: usize, cmd: i32, arg: *u32) callconv(.winapi) i32;
    extern "ws2_32" fn WSAGetLastError() callconv(.winapi) i32;
    extern "ws2_32" fn shutdown(s: usize, how: i32) callconv(.winapi) i32;
    const FIONBIO: i32 = @bitCast(@as(u32, 0x8004667e));
    const WSAEWOULDBLOCK = 10035;
};

/// deriveTcp returns (send key, open key) for one end, = wirecrypto.NewDuplexSealer.
pub fn deriveTcp(master: []const u8, salt: []const u8, initiator: bool) struct { send: [32]u8, recv: [32]u8 } {
    var a: [32]u8 = undefined;
    var b: [32]u8 = undefined;
    keys.hkdf(&a, master, salt, "medialink c2s v1");
    keys.hkdf(&b, master, salt, "medialink s2c v1");
    return if (initiator) .{ .send = a, .recv = b } else .{ .send = b, .recv = a };
}

const rx_cap = 256 << 10; // control frames are small JSON metas; a bigger one is a protocol error
const send_wait_ns = 200_000;

pub const TcpTx = struct {
    alloc: std.mem.Allocator,
    fd: sock.Fd,
    out: []u8,
    rx: []u8,
    rx_len: usize = 0,
    pt: []u8,
    ctr_tx: u64 = 0,
    ctr_rx: u64 = 0,
    dead: bool = false,

    pub fn init(alloc: std.mem.Allocator, fd: sock.Fd, max_frame: usize) !TcpTx {
        const out = try alloc.alloc(u8, 4 + max_frame + aead.tag_len);
        errdefer alloc.free(out);
        const rx = try alloc.alloc(u8, rx_cap);
        errdefer alloc.free(rx);
        const pt = try alloc.alloc(u8, rx_cap);
        if (is_windows) {
            var one: u32 = 1;
            _ = win.ioctlsocket(fd, win.FIONBIO, &one); // recv must not block the TX thread
        }
        return .{ .alloc = alloc, .fd = fd, .out = out, .rx = rx, .pt = pt };
    }

    pub fn deinit(t: *TcpTx) void {
        t.alloc.free(t.out);
        t.alloc.free(t.rx);
        t.alloc.free(t.pt);
    }

    /// sendFrame seals (or not) one marshaled frame and writes it whole. false = connection dead.
    pub fn sendFrame(t: *TcpTx, frame: []const u8, encrypt: bool, key: [32]u8, stop: *const std.atomic.Value(bool)) bool {
        if (t.dead) return false;
        var n: usize = 0;
        if (encrypt) {
            n = aead.seal(t.out[4..], frame, &.{}, keys.nonce(0, t.ctr_tx), key);
            t.ctr_tx += 1;
        } else {
            @memcpy(t.out[4..][0..frame.len], frame);
            n = frame.len;
        }
        std.mem.writeInt(u32, t.out[0..4], @intCast(n), .big);
        if (!sendAll(t.fd, t.out[0 .. 4 + n], stop)) t.dead = true;
        return !t.dead;
    }

    /// poll reads whatever control bytes are pending (non-blocking) and returns the next complete,
    /// opened frame (plaintext, valid until the next poll), or null.
    pub fn poll(t: *TcpTx, encrypt: bool, key: [32]u8) ?[]const u8 {
        if (t.dead) return null;
        while (t.rx_len < t.rx.len) {
            const got = recvSome(t.fd, t.rx[t.rx_len..]) catch |e| {
                if (e == error.WouldBlock) break;
                t.dead = true; // EOF / reset: the receiver ended the route
                return null;
            };
            t.rx_len += got;
        }
        if (t.rx_len < 4) return null;
        const n = std.mem.readInt(u32, t.rx[0..4], .big);
        if (n < 16 or n > t.rx.len - 4) { // = transport.go bounds; control frames are small
            t.dead = true;
            return null;
        }
        if (t.rx_len < 4 + n) return null;
        var plen: usize = n;
        if (encrypt) {
            plen = aead.open(t.pt, t.rx[4 .. 4 + n], &.{}, keys.nonce(0, t.ctr_rx), key) catch {
                t.dead = true; // forged / desynced: the direction is dead (as Sealer.Open)
                return null;
            };
            t.ctr_rx += 1;
        } else {
            @memcpy(t.pt[0..n], t.rx[4 .. 4 + n]);
        }
        std.mem.copyForwards(u8, t.rx[0 .. t.rx_len - 4 - n], t.rx[4 + n .. t.rx_len]);
        t.rx_len -= 4 + n;
        return t.pt[0..plen];
    }
};

// sendAll writes buf NON-BLOCKING (MSG_DONTWAIT / FIONBIO) so the TX thread can always stop: a
// handed fd is blocking (Go File() dup) and a receiver that stops reading (half-open after a link
// reset) would otherwise park the thread in the kernel forever and hang rm_tx_close. Gives up (dead)
// on stop or after stall_dead_ns without progress.
const stall_dead_ns: i64 = 5_000_000_000;

fn sendAll(fd: sock.Fd, buf: []const u8, stop: *const std.atomic.Value(bool)) bool {
    const hrt = @import("hrtimer.zig");
    var off: usize = 0;
    var last_progress = hrt.nowMono();
    while (off < buf.len) {
        if (stop.load(.acquire)) return false;
        if (hrt.nowMono() - last_progress > stall_dead_ns) return false;
        if (is_windows) {
            const r = win.send(fd, buf[off..].ptr, @intCast(@min(buf.len - off, 1 << 30)), 0);
            if (r > 0) {
                off += @intCast(r);
                last_progress = hrt.nowMono();
                continue;
            }
            if (win.WSAGetLastError() != win.WSAEWOULDBLOCK) return false;
        } else {
            const r = linux.sendto(fd, buf[off..].ptr, buf.len - off, linux.MSG.NOSIGNAL | linux.MSG.DONTWAIT, null, 0);
            switch (linux.errno(r)) {
                .SUCCESS => {
                    off += r;
                    last_progress = hrt.nowMono();
                    continue;
                },
                .AGAIN, .INTR => {},
                else => return false,
            }
        }
        // Receiver not draining (TCP backpressure): wait briefly; the AU queue absorbs the stall.
        hrt.sleepUntil(hrt.nowMono() + send_wait_ns);
    }
    return true;
}

/// shutdownBoth half-closes both directions: wakes any thread blocked on the socket (on Linux a
/// close() from another thread does NOT wake a blocked recv) and sends FIN.
pub fn shutdownBoth(fd: sock.Fd) void {
    if (is_windows) {
        _ = win.shutdown(fd, 2); // SD_BOTH
    } else {
        _ = linux.shutdown(fd, linux.SHUT.RDWR);
    }
}

fn recvSome(fd: sock.Fd, buf: []u8) !usize {
    if (is_windows) {
        const r = win.recv(fd, buf.ptr, @intCast(@min(buf.len, 1 << 30)), 0);
        if (r > 0) return @intCast(r);
        if (r == 0) return error.Closed;
        if (win.WSAGetLastError() == win.WSAEWOULDBLOCK) return error.WouldBlock;
        return error.Recv;
    }
    const r = linux.recvfrom(fd, buf.ptr, buf.len, linux.MSG.DONTWAIT, null, null);
    switch (linux.errno(r)) {
        .SUCCESS => return if (r == 0) error.Closed else r,
        .AGAIN, .INTR => return error.WouldBlock,
        else => return error.Recv,
    }
}
