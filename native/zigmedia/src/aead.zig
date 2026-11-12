//! Per-datagram AES-256-GCM, byte-exact with Go's cipher.NewGCM/Seal/Open (12-byte explicit nonce,
//! the 24-byte datagram header as AAD, 16-byte tag appended to the ciphertext).
const std = @import("std");
const Gcm = std.crypto.aead.aes_gcm.Aes256Gcm;

pub const tag_len = Gcm.tag_length; // 16
pub const key_len = Gcm.key_length; // 32
pub const nonce_len = Gcm.nonce_length; // 12

/// seal writes ciphertext‖tag into out (out.len must be >= pt.len + tag_len) and returns the sealed
/// length (pt.len + tag_len). Matches dgCrypto.seal (Go appends tag after ciphertext).
pub fn seal(out: []u8, pt: []const u8, aad: []const u8, n: [nonce_len]u8, key: [key_len]u8) usize {
    std.debug.assert(out.len >= pt.len + tag_len);
    var tag: [tag_len]u8 = undefined;
    Gcm.encrypt(out[0..pt.len], &tag, pt, aad, n, key);
    @memcpy(out[pt.len..][0..tag_len], &tag);
    return pt.len + tag_len;
}

/// open authenticates + decrypts sealed (ciphertext‖tag) into out (out.len >= sealed.len - tag_len)
/// and returns the plaintext length, or an error on auth failure. Matches dgCrypto.open.
pub fn open(out: []u8, sealed: []const u8, aad: []const u8, n: [nonce_len]u8, key: [key_len]u8) !usize {
    if (sealed.len < tag_len) return error.Short;
    const ct = sealed[0 .. sealed.len - tag_len];
    const tag: [tag_len]u8 = sealed[sealed.len - tag_len ..][0..tag_len].*;
    std.debug.assert(out.len >= ct.len);
    try Gcm.decrypt(out[0..ct.len], ct, tag, aad, n, key);
    return ct.len;
}
