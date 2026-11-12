//! Byte-exact goldens vs the Go wire implementation. Regenerate with:
//!   GOWORK=off go test ./internal/medialink -run TestZigGoldenVectors -v
//! and re-pin the hex below if the wire format changes.
const std = @import("std");
const keys = @import("keys.zig");
const aead = @import("aead.zig");
const wire = @import("wire.zig");
const testing = std.testing;

// Fixed inputs mirroring the Go generator (TestZigGoldenVectors).
fn goldenMaster() [32]u8 {
    var m: [32]u8 = undefined;
    for (&m, 0..) |*b, i| b.* = @intCast(i + 1);
    return m;
}
fn goldenSalt(buf: []u8) []const u8 {
    var n: usize = 0;
    for (0..16) |i| {
        buf[n] = @intCast(0xA0 + i);
        n += 1;
    } // offerNonce
    for (0..16) |i| {
        buf[n] = @intCast(0xB0 + i);
        n += 1;
    } // answerNonce
    const sess = "sess-1234";
    @memcpy(buf[n..][0..sess.len], sess);
    return buf[0 .. n + sess.len];
}

fn expectHex(expected_hex: []const u8, got: []const u8) !void {
    var buf: [1024]u8 = undefined;
    const want = try std.fmt.hexToBytes(buf[0..], expected_hex);
    try testing.expectEqualSlices(u8, want, got);
}

test "token + key schedule match Go" {
    const master = goldenMaster();
    var sb: [64]u8 = undefined;
    const salt = goldenSalt(&sb);

    try testing.expectEqual(@as(u32, 0xf61ab158), keys.token(&master, salt));

    const ks = keys.deriveUdp(&master, salt, true);
    try expectHex("b380fcc8c313f4e0349083db5751f9c82d46eb96d78dc69c21b904c71be72bd5", &ks.send.key);
    try testing.expectEqual(@as(u32, 0x893e3a8c), ks.send.prefix);
    try expectHex("8463591a9f40d1074d5281d91f655fd1a4e4a0fcfbcf8a5fc376dcbcffc2dc1e", &ks.recv.key);
    try testing.expectEqual(@as(u32, 0xb398b7ce), ks.recv.prefix);
}

test "datagram header encodes byte-exact" {
    var out: [wire.header_len]u8 = undefined;
    const h = wire.Header{ .typ = wire.dg_type_media, .flags = wire.flag_aead, .token = 0xf61ab158, .seq = 0x0102030405060708, .stream = 0x1122, .frame_seq = 0x33445566, .frag_idx = 2, .frag_count = 5 };
    h.encode(&out);
    try expectHex("1001f61ab158010203040506070811223344556600020005", &out);
    // Roundtrip.
    const d = try wire.Header.decode(&out);
    try testing.expectEqual(h, d);
}

test "frame marshal byte-exact" {
    var out: [128]u8 = undefined;
    const m = wire.FrameMeta{ .stream = 0x1122, .kind = 1, .codec = 5, .flags = 1, .seq = 0x33445566, .pts = 0x0011223344556677 };
    const n = wire.marshalFrame(&out, m, "hello-au-payload");
    try expectHex("112201050100334455660000000000112233445566770000001068656c6c6f2d61752d7061796c6f6164", out[0..n]);
}

test "sealed media datagram byte-exact" {
    const master = goldenMaster();
    var sb: [64]u8 = undefined;
    const salt = goldenSalt(&sb);
    const ks = keys.deriveUdp(&master, salt, true);
    const tok = keys.token(&master, salt);

    // Inner frame (same as the Go golden).
    var fbuf: [128]u8 = undefined;
    const m = wire.FrameMeta{ .stream = 0x1122, .kind = 1, .codec = 5, .flags = 1, .seq = 0x33445566, .pts = 0x0011223344556677 };
    const pt = fbuf[0..wire.marshalFrame(&fbuf, m, "hello-au-payload")];

    // One datagram: header (seq 0, frag 0/1) ‖ seal(pt, aad=header).
    var dg: [256]u8 = undefined;
    const hdr: *[wire.header_len]u8 = dg[0..wire.header_len];
    const h = wire.Header{ .typ = wire.dg_type_media, .flags = wire.flag_aead, .token = tok, .seq = 0, .stream = 0x1122, .frame_seq = 0x33445566, .frag_idx = 0, .frag_count = 1 };
    h.encode(hdr);
    const sealed = aead.seal(dg[wire.header_len..], pt, hdr, keys.nonce(ks.send.prefix, 0), ks.send.key);
    const total = wire.header_len + sealed;
    try expectHex("1001f61ab15800000000000000001122334455660000000145e700bb7c992d1c99d9086e08df15d037bdb736e12af2dbd65bf853c6a5eac4733f462d76449af3fda313a601f5be23e387c0d22a6ff065f252", dg[0..total]);

    // Open roundtrip (recv side uses the same key for c2s).
    var ptout: [128]u8 = undefined;
    const got = try aead.open(&ptout, dg[wire.header_len..total], hdr, keys.nonce(ks.send.prefix, 0), ks.send.key);
    try testing.expectEqualSlices(u8, pt, ptout[0..got]);
}

test "sealed ping control datagram byte-exact" {
    const master = goldenMaster();
    var sb: [64]u8 = undefined;
    const salt = goldenSalt(&sb);
    const ks = keys.deriveUdp(&master, salt, true);
    const tok = keys.token(&master, salt);

    var dg: [64]u8 = undefined;
    const hdr: *[wire.header_len]u8 = dg[0..wire.header_len];
    const h = wire.Header{ .typ = wire.dg_type_ping, .flags = wire.flag_aead, .token = tok, .seq = 1 };
    h.encode(hdr);
    const payload = [_]u8{ 0, 0, 0, 0, 0, 0, 0, 1 };
    const sealed = aead.seal(dg[wire.header_len..], &payload, hdr, keys.nonce(ks.send.prefix, 1), ks.send.key);
    const total = wire.header_len + sealed;
    try expectHex("1101f61ab158000000000000000100000000000000000000967494773c00e9fdcf95918868dc86bcc159f224143f5bb1", dg[0..total]);
}

test "header decode rejects hostile input" {
    try testing.expectError(error.Short, wire.Header.decode(&[_]u8{0} ** 10));
    var b: [wire.header_len]u8 = undefined;
    const h = wire.Header{ .typ = wire.dg_type_media, .frag_count = 0 }; // media frag_count 0 invalid
    h.encode(&b);
    try testing.expectError(error.BadFrag, wire.Header.decode(&b));
    b[0] = 0x20; // version 2
    try testing.expectError(error.BadVersion, wire.Header.decode(&b));
}
