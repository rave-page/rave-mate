//! Per-route key schedule + token for the medialink UDP datagram profile. Byte-exact with the Go
//! implementation (internal/medialink/udpframe.go): HKDF-SHA256 over the per-route salt
//! (offerNonce‖answerNonce‖session), labels "medialink-udp c2s/s2c v1" and "medialink-udp-token".
const std = @import("std");
const Hkdf = std.crypto.kdf.hkdf.HkdfSha256;

/// hkdf writes RFC-5869 HKDF-SHA256(ikm, salt, info) into out (extract then expand).
pub fn hkdf(out: []u8, ikm: []const u8, salt: []const u8, info: []const u8) void {
    const prk = Hkdf.extract(salt, ikm);
    Hkdf.expand(out, info, prk);
}

/// token derives the 32-bit per-route demux + anti-injection token (big-endian of the first 4 HKDF
/// bytes), matching routeToken().
pub fn token(secret: []const u8, salt: []const u8) u32 {
    var k: [4]u8 = undefined;
    hkdf(&k, secret, salt, "medialink-udp-token");
    return std.mem.readInt(u32, &k, .big);
}

pub const Dir = struct {
    key: [32]u8,
    prefix: u32, // 32-bit nonce prefix (big-endian on the wire)
};

pub const KeySchedule = struct {
    send: Dir,
    recv: Dir,
};

/// deriveUdp builds the per-direction AES-256-GCM keys + nonce prefixes, matching newDgCrypto():
/// the initiator seals c2s / opens s2c; the responder mirrors.
pub fn deriveUdp(master: []const u8, salt: []const u8, initiator: bool) KeySchedule {
    var a: [36]u8 = undefined; // 32 key + 4 prefix
    var b: [36]u8 = undefined;
    hkdf(&a, master, salt, "medialink-udp c2s v1");
    hkdf(&b, master, salt, "medialink-udp s2c v1");
    const c2s = Dir{ .key = a[0..32].*, .prefix = std.mem.readInt(u32, a[32..36], .big) };
    const s2c = Dir{ .key = b[0..32].*, .prefix = std.mem.readInt(u32, b[32..36], .big) };
    if (initiator) return .{ .send = c2s, .recv = s2c };
    return .{ .send = s2c, .recv = c2s };
}

/// nonce builds the 12-byte explicit GCM nonce [prefix(4) ‖ seq(8)], big-endian (matches dgNonce()).
pub fn nonce(prefix: u32, seq: u64) [12]u8 {
    var n: [12]u8 = undefined;
    std.mem.writeInt(u32, n[0..4], prefix, .big);
    std.mem.writeInt(u64, n[4..12], seq, .big);
    return n;
}
