//! Loopback interop: the native sender → a UDP receiver that decodes with the proven wire core
//! (equivalent to the Go receiver, which the goldens pin byte-for-byte). Linux-only (raw syscalls).
const std = @import("std");
const linux = std.os.linux;
const keys = @import("keys.zig");
const aead = @import("aead.zig");
const wire = @import("wire.zig");
const snd = @import("sender.zig");
const testing = std.testing;

const SockaddrIn = extern struct {
    family: u16 = linux.AF.INET,
    port: u16, // network order
    addr: u32, // network order
    zero: [8]u8 = [_]u8{0} ** 8,
};

fn loopbackAddr(port_be: u16) SockaddrIn {
    return .{ .port = port_be, .addr = @bitCast([4]u8{ 127, 0, 0, 1 }) };
}

fn bindEphemeral() !struct { fd: i32, addr: SockaddrIn } {
    const fd: i32 = @intCast(linux.socket(linux.AF.INET, linux.SOCK.DGRAM, 0));
    var a = loopbackAddr(0);
    if (linux.errno(linux.bind(fd, @ptrCast(&a), @sizeOf(SockaddrIn))) != .SUCCESS) return error.Bind;
    var got: SockaddrIn = undefined;
    var len: linux.socklen_t = @sizeOf(SockaddrIn);
    if (linux.errno(linux.getsockname(fd, @ptrCast(&got), &len)) != .SUCCESS) return error.GetSockName;
    // 200ms recv timeout so the test never hangs.
    const tv = linux.timeval{ .sec = 0, .usec = 200_000 };
    _ = linux.setsockopt(fd, linux.SOL.SOCKET, linux.SO.RCVTIMEO, std.mem.asBytes(&tv), @sizeOf(linux.timeval));
    return .{ .fd = fd, .addr = got };
}

fn fixtureKeys() struct { ks: keys.KeySchedule, token: u32 } {
    var master: [32]u8 = undefined;
    for (&master, 0..) |*b, i| b.* = @intCast(i + 1);
    var salt: [40]u8 = undefined;
    const nonces = ([_]u8{0xAA} ** 16) ++ ([_]u8{0xBB} ** 16);
    @memcpy(salt[0..32], &nonces);
    @memcpy(salt[32..40], "route-99");
    return .{ .ks = keys.deriveUdp(&master, &salt, true), .token = keys.token(&master, &salt) };
}

fn openNativeSender(recv_addr: SockaddrIn, encrypt: bool) !*snd.Sender {
    const fk = fixtureKeys();
    const fd = try snd.udpSocket();
    try snd.udpConnect(fd, @ptrCast(&recv_addr), @sizeOf(SockaddrIn));
    const cfg = snd.Config{
        .transport = .udp,
        .codec = 5,
        .stream = 1,
        .body_max = 1200,
        .encrypt = encrypt,
        .token = fk.token,
        .send_key = fk.ks.send.key,
        .send_prefix = fk.ks.send.prefix,
        .recv_key = fk.ks.recv.key,
        .recv_prefix = fk.ks.recv.prefix,
        .rate_bps = 0,
    };
    return snd.Sender.open(testing.allocator, cfg, fd, true);
}

// decodeMedia authenticates (AEAD) + parses a media datagram, returning (frame_seq, frag_idx,
// frag_count, inner frame wire bytes) into out. The receiver uses the sender's SEND key (c2s).
fn decodeMedia(dg: []const u8, encrypt: bool, send_key: [32]u8, send_prefix: u32, token: u32, out: []u8) !?struct { h: wire.Header, body: []const u8 } {
    const h = try wire.Header.decode(dg);
    if (h.token != token) return error.Token;
    if (h.typ != wire.dg_type_media) return null;
    if (encrypt) {
        const n = try aead.open(out, dg[wire.header_len..], dg[0..wire.header_len], keys.nonce(send_prefix, h.seq), send_key);
        return .{ .h = h, .body = out[0..n] };
    }
    @memcpy(out[0 .. dg.len - wire.header_len], dg[wire.header_len..]);
    return .{ .h = h, .body = out[0 .. dg.len - wire.header_len] };
}

test "native sender -> receiver: AEAD media decodes in order" {
    const r = try bindEphemeral();
    defer snd.closeFd(r.fd);
    const fk = fixtureKeys();

    const sender = try openNativeSender(r.addr, true);
    defer sender.close();

    // Submit slower than the worker drains (5ms gap) so none are dropped → frame.seq == submit index.
    const n_aus = 5;
    var i: u32 = 0;
    while (i < n_aus) : (i += 1) {
        var au: [16]u8 = undefined;
        const au_str = std.fmt.bufPrint(&au, "au-payload-{d}", .{i}) catch unreachable;
        sender.submit(au_str, @as(i64, i) + 1, if (i == 0) snd.flag_keyframe else 0);
        sleepMs(5);
    }

    // Receive + verify (each small AU is a single datagram here).
    var got: u32 = 0;
    var buf: [2048]u8 = undefined;
    var pt: [2048]u8 = undefined;
    const deadline = nowMs() + 3000;
    while (got < n_aus and nowMs() < deadline) {
        const rn = linux.recvfrom(r.fd, &buf, buf.len, 0, null, null);
        if (linux.errno(rn) != .SUCCESS) continue;
        const dg = buf[0..rn];
        const md = decodeMedia(dg, true, fk.ks.send.key, fk.ks.send.prefix, fk.token, &pt) catch continue;
        const m = md orelse continue; // skip control (ping/keepalive)
        const frame = wire.parseFrameForTest(m.body) catch continue;
        var exp: [16]u8 = undefined;
        const expbuf = std.fmt.bufPrint(&exp, "au-payload-{d}", .{frame.seq}) catch unreachable;
        try testing.expectEqualSlices(u8, expbuf, frame.payload);
        got += 1;
    }
    try testing.expectEqual(@as(u32, n_aus), got);
    try testing.expect(sender.snapshot().aus_sent >= n_aus);
}

test "native sender -> receiver: large AU fragments + reassembles" {
    const r = try bindEphemeral();
    defer snd.closeFd(r.fd);
    const fk = fixtureKeys();
    const sender = try openNativeSender(r.addr, true);
    defer sender.close();

    // 3000-byte AU at body_max 1200 → 3 datagrams (frame wire = 26 + 3000 = 3026 → 3 frags).
    var big: [3000]u8 = undefined;
    for (&big, 0..) |*b, j| b.* = @intCast(j & 0xff);
    sender.submit(&big, 1, snd.flag_keyframe);

    var frag_store: [8][1500]u8 = undefined;
    var frag_lens = [_]usize{0} ** 8;
    var count: u16 = 0;
    var have: u16 = 0;
    var buf: [2048]u8 = undefined;
    var pt: [2048]u8 = undefined;
    const deadline = nowMs() + 3000;
    while ((count == 0 or have < count) and nowMs() < deadline) {
        const rn = linux.recvfrom(r.fd, &buf, buf.len, 0, null, null);
        if (linux.errno(rn) != .SUCCESS) continue;
        const md = decodeMedia(buf[0..rn], true, fk.ks.send.key, fk.ks.send.prefix, fk.token, &pt) catch continue;
        const m = md orelse continue;
        count = m.h.frag_count;
        if (m.h.frag_idx < 8 and frag_lens[m.h.frag_idx] == 0) {
            @memcpy(frag_store[m.h.frag_idx][0..m.body.len], m.body);
            frag_lens[m.h.frag_idx] = m.body.len;
            have += 1;
        }
    }
    try testing.expectEqual(@as(u16, 3), count);
    // Reassemble in fragment order → inner frame → payload == big.
    var whole: [4096]u8 = undefined;
    var off: usize = 0;
    var k: u16 = 0;
    while (k < count) : (k += 1) {
        @memcpy(whole[off..][0..frag_lens[k]], frag_store[k][0..frag_lens[k]]);
        off += frag_lens[k];
    }
    const frame = try wire.parseFrameForTest(whole[0..off]);
    try testing.expectEqualSlices(u8, &big, frame.payload);
}

fn buildCtrl(typ: u8, payload: []const u8, key: [32]u8, prefix: u32, seq: u64, token: u32, out: []u8) usize {
    const h = wire.Header{ .typ = typ, .flags = wire.flag_aead, .token = token, .seq = seq };
    h.encode(out[0..wire.header_len]);
    return wire.header_len + aead.seal(out[wire.header_len..], payload, out[0..wire.header_len], keys.nonce(prefix, seq), key);
}

test "native sender -> receiver: CLEAR (plaintext) media decodes" {
    const r = try bindEphemeral();
    defer snd.closeFd(r.fd);
    const fk = fixtureKeys();
    const sender = try openNativeSender(r.addr, false); // encrypt = false
    defer sender.close();
    sender.submit("clear-au", 1, snd.flag_keyframe);

    var buf: [2048]u8 = undefined;
    var pt: [2048]u8 = undefined;
    const deadline = nowMs() + 3000;
    while (nowMs() < deadline) {
        const rn = linux.recvfrom(r.fd, &buf, buf.len, 0, null, null);
        if (linux.errno(rn) != .SUCCESS) continue;
        const md = decodeMedia(buf[0..rn], false, fk.ks.send.key, fk.ks.send.prefix, fk.token, &pt) catch continue;
        const m = md orelse continue;
        const frame = try wire.parseFrameForTest(m.body);
        try testing.expectEqualSlices(u8, "clear-au", frame.payload);
        return;
    }
    return error.NoMedia;
}

test "receiver NACK -> sender retransmits from the ring" {
    const r = try bindEphemeral();
    defer snd.closeFd(r.fd);
    const fk = fixtureKeys();
    const sender = try openNativeSender(r.addr, true);
    defer sender.close();
    sender.submit("nack-me", 1, snd.flag_keyframe); // frame_seq 0, single fragment

    var buf: [2048]u8 = undefined;
    var pt: [2048]u8 = undefined;
    var src: SockaddrIn = undefined;
    var srclen: linux.socklen_t = @sizeOf(SockaddrIn);
    var recv_seq: u64 = 0;
    var media_seen: u32 = 0;
    var retx_seen = false;
    const deadline = nowMs() + 3000;
    while (nowMs() < deadline and !retx_seen) {
        const rn = linux.recvfrom(r.fd, &buf, buf.len, 0, @ptrCast(&src), &srclen);
        if (linux.errno(rn) != .SUCCESS) continue;
        const md = decodeMedia(buf[0..rn], true, fk.ks.send.key, fk.ks.send.prefix, fk.token, &pt) catch continue;
        const m = md orelse continue;
        media_seen += 1;
        if (media_seen == 1) {
            // NACK frame 0, frag 0: [count=1][stream(2) frameSeq(4) fragIdx(2)].
            var nb: [10]u8 = undefined;
            std.mem.writeInt(u16, nb[0..2], 1, .big);
            std.mem.writeInt(u16, nb[2..4], m.h.stream, .big);
            std.mem.writeInt(u32, nb[4..8], m.h.frame_seq, .big);
            std.mem.writeInt(u16, nb[8..10], m.h.frag_idx, .big);
            var out: [128]u8 = undefined;
            const nlen = buildCtrl(wire.dg_type_nack, &nb, fk.ks.recv.key, fk.ks.recv.prefix, recv_seq, fk.token, &out);
            recv_seq += 1;
            _ = linux.sendto(r.fd, &out, nlen, 0, @ptrCast(&src), srclen);
        } else if (media_seen >= 2) {
            retx_seen = true; // a second media datagram for the same frame = the retransmit
        }
    }
    try testing.expect(retx_seen);
    try testing.expect(sender.snapshot().retransmits >= 1);
}

fn nowMs() i64 {
    var ts: linux.timespec = undefined;
    _ = linux.clock_gettime(.MONOTONIC, &ts);
    return @as(i64, @intCast(ts.sec)) * 1000 + @divTrunc(@as(i64, @intCast(ts.nsec)), 1_000_000);
}

fn sleepMs(ms: i64) void {
    var req = linux.timespec{ .sec = @intCast(@divTrunc(ms, 1000)), .nsec = @intCast(@mod(ms, 1000) * 1_000_000) };
    _ = linux.nanosleep(&req, null);
}
