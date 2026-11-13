//! Receiver lifecycle surface the zigenc net session relies on: rebind to a native sender's own socket
//! (strictly-newer authenticated datagram), sender meta (sync pong) reaching the inbound mailbox, bye
//! ending the route, idle hook on the receiver thread, NACK accounting fields.
const std = @import("std");
const keys = @import("keys.zig");
const sock = @import("sock.zig");
const snd = @import("sender.zig");
const hrtimer = @import("hrtimer.zig");
const rx_mod = @import("receiver.zig");
const po = @import("playout.zig");
const testing = std.testing;

var g_frames = std.atomic.Value(u32).init(0);
var g_idle = std.atomic.Value(u32).init(0);
fn onFrame(_: ?*anyopaque, _: [*]const u8, _: usize, _: i64, _: bool) callconv(.c) void {
    _ = g_frames.fetchAdd(1, .monotonic);
}
fn onIdle(_: ?*anyopaque) callconv(.c) void {
    _ = g_idle.fetchAdd(1, .monotonic);
}

test "rx lifecycle: rebind to the sender socket, sync pong in the meta mailbox, bye observed" {
    var master: [32]u8 = undefined;
    for (&master, 0..) |*b, i| b.* = @intCast(i + 3);
    const salt = "rx-lifecycle-salt-0123456789abcdef!";
    const txk = keys.deriveUdp(&master, salt, false);
    const rxk = keys.deriveUdp(&master, salt, true);
    const tok = keys.token(&master, salt);

    const rfd = try sock.udpSocket();
    defer sock.close(rfd);
    var la = sock.Addr4.loopback(0);
    try sock.bind(rfd, &la);
    const raddr = try sock.localAddr(rfd);
    // Decoy peer (the Go answerer mux address in production): control goes there until the native
    // sender's first strictly-newer datagram moves it.
    const dfd = try sock.udpSocket();
    defer sock.close(dfd);
    try sock.bind(dfd, &la);
    const daddr = try sock.localAddr(dfd);

    g_frames.store(0, .monotonic);
    g_idle.store(0, .monotonic);
    const r = try rx_mod.Receiver.openPeerIdle(testing.allocator, rfd, .{ .encrypt = true, .token = tok, .open_key = rxk.recv.key, .open_prefix = rxk.recv.prefix, .send_key = rxk.send.key, .send_prefix = rxk.send.prefix, .preset = po.low_latency, .initial_seq = 900 }, daddr, onFrame, onIdle, null);
    defer r.close();

    const tfd = try snd.udpSocket();
    try snd.udpConnect(tfd, &raddr, @sizeOf(sock.Addr4));
    const s = try snd.Sender.open(testing.allocator, .{ .token = tok, .send_key = txk.send.key, .send_prefix = txk.send.prefix, .recv_key = txk.recv.key, .recv_prefix = txk.recv.prefix, .initial_seq = 50 }, tfd, true);
    var au = [_]u8{0x11} ** 4000;
    const t0 = hrtimer.nowMono();
    var i: u32 = 0;
    while (i < 20) : (i += 1) {
        s.submit(&au, @as(i64, i) * 16_666_667, if (i == 0) snd.flag_keyframe else 0);
        hrtimer.sleepUntil(t0 + @as(i64, i + 1) * 16_666_667);
    }
    try testing.expect(r.sendMeta("{\"t\":\"sync\",\"id\":9,\"t1\":12345}"));
    var got: [rx_mod.meta_in_max]u8 = undefined;
    var at: i64 = 0;
    var n: ?usize = null;
    const dl = hrtimer.nowMono() + 2_000_000_000;
    while (n == null and hrtimer.nowMono() < dl) {
        n = r.takeMetaIn(&got, &at);
        if (n == null) hrtimer.sleepUntil(hrtimer.nowMono() + 2_000_000);
    }
    try testing.expect(n != null);
    try testing.expect(std.mem.indexOf(u8, got[0..n.?], "\"syncr\"") != null and at > 0);
    s.close(); // bye
    const dl2 = hrtimer.nowMono() + 2_000_000_000;
    while (!r.stats().bye and hrtimer.nowMono() < dl2) hrtimer.sleepUntil(hrtimer.nowMono() + 5_000_000);
    const st = r.stats();
    try testing.expect(st.bye);
    try testing.expectEqual(@as(u64, 1), st.rebinds);
    try testing.expectEqual(@as(u32, 20), g_frames.load(.monotonic));
    try testing.expect(st.keyframes == 1 and st.media_bytes == 20 * au.len);
    try testing.expect(st.rtt_ns > 0); // pongs answered by the sender = control reached its socket
    try testing.expect(st.ctl_seq > 900 and st.last_rx_ns > 0);
    try testing.expect(g_idle.load(.monotonic) > 10);
}
