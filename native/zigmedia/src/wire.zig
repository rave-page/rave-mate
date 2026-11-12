//! Datagram header + frame marshal + fragmentation, byte-exact with the Go wire
//! (internal/medialink/udpframe.go + frame.go). Big-endian throughout.
const std = @import("std");

pub const header_len = 24; // outer datagram header
pub const version = 1;

pub const dg_type_media = 0;
pub const dg_type_ping = 1;
pub const dg_type_pong = 2;
pub const dg_type_nack = 3;
pub const dg_type_bye = 4;

pub const flag_aead: u8 = 1 << 0;

pub const max_frags = 8192;

pub const HeaderError = error{ Short, BadVersion, BadFrag };

/// Header is the parsed 24-byte outer datagram header.
pub const Header = struct {
    typ: u8 = 0,
    flags: u8 = 0,
    token: u32 = 0,
    seq: u64 = 0,
    stream: u16 = 0,
    frame_seq: u32 = 0,
    frag_idx: u16 = 0,
    frag_count: u16 = 0,

    /// encode writes the 24 wire bytes (matches dgHeader.appendHeader).
    pub fn encode(h: Header, out: *[header_len]u8) void {
        out[0] = (@as(u8, version) << 4) | (h.typ & 0x0f);
        out[1] = h.flags;
        std.mem.writeInt(u32, out[2..6], h.token, .big);
        std.mem.writeInt(u64, out[6..14], h.seq, .big);
        std.mem.writeInt(u16, out[14..16], h.stream, .big);
        std.mem.writeInt(u32, out[16..20], h.frame_seq, .big);
        std.mem.writeInt(u16, out[20..22], h.frag_idx, .big);
        std.mem.writeInt(u16, out[22..24], h.frag_count, .big);
    }

    /// decode validates + parses (matches parseDatagramHeader): hostile fields are rejected.
    pub fn decode(buf: []const u8) HeaderError!Header {
        if (buf.len < header_len) return error.Short;
        if (buf[0] >> 4 != version) return error.BadVersion;
        const h = Header{
            .typ = buf[0] & 0x0f,
            .flags = buf[1],
            .token = std.mem.readInt(u32, buf[2..6], .big),
            .seq = std.mem.readInt(u64, buf[6..14], .big),
            .stream = std.mem.readInt(u16, buf[14..16], .big),
            .frame_seq = std.mem.readInt(u32, buf[16..20], .big),
            .frag_idx = std.mem.readInt(u16, buf[20..22], .big),
            .frag_count = std.mem.readInt(u16, buf[22..24], .big),
        };
        if (h.typ == dg_type_media) {
            if (h.frag_count == 0 or h.frag_count > max_frags or h.frag_idx >= h.frag_count) {
                return error.BadFrag;
            }
        }
        return h;
    }

    pub fn aead(h: Header) bool {
        return h.flags & flag_aead != 0;
    }
};

pub const frame_header_len = 26; // inner Frame header (frame.go)

/// FrameMeta is the inner Frame header fields (medialink.Frame minus payload).
pub const FrameMeta = struct {
    stream: u16,
    kind: u8,
    codec: u8,
    flags: u8,
    tc_rate: u8 = 0,
    seq: u32,
    tc_frames: u32 = 0,
    pts: i64,
};

/// marshalFrame writes the 26-byte inner header + payload into out (>= 26 + payload.len), returning
/// the total. Matches Frame.marshal.
pub fn marshalFrame(out: []u8, m: FrameMeta, payload: []const u8) usize {
    std.debug.assert(out.len >= frame_header_len + payload.len);
    std.mem.writeInt(u16, out[0..2], m.stream, .big);
    out[2] = m.kind;
    out[3] = m.codec;
    out[4] = m.flags;
    out[5] = m.tc_rate;
    std.mem.writeInt(u32, out[6..10], m.seq, .big);
    std.mem.writeInt(u32, out[10..14], m.tc_frames, .big);
    std.mem.writeInt(u64, out[14..22], @bitCast(m.pts), .big); // i64 PTS marshaled as uint64 BE
    std.mem.writeInt(u32, out[22..26], @intCast(payload.len), .big);
    @memcpy(out[frame_header_len..][0..payload.len], payload);
    return frame_header_len + payload.len;
}

/// numFrags reports how many datagrams a wire_len frame needs at body_max (matches numFrags).
pub fn numFrags(wire_len: usize, body_max: usize) usize {
    if (wire_len == 0) return 1;
    return (wire_len + body_max - 1) / body_max;
}

pub const ParsedFrame = struct {
    stream: u16,
    kind: u8,
    codec: u8,
    flags: u8,
    seq: u32,
    pts: i64,
    payload: []const u8,
};

/// parseFrameForTest decodes the 26-byte inner Frame header + payload (receiver-side verification).
pub fn parseFrameForTest(buf: []const u8) !ParsedFrame {
    if (buf.len < frame_header_len) return error.Short;
    const plen = std.mem.readInt(u32, buf[22..26], .big);
    if (frame_header_len + plen != buf.len) return error.BadLen;
    return .{
        .stream = std.mem.readInt(u16, buf[0..2], .big),
        .kind = buf[2],
        .codec = buf[3],
        .flags = buf[4],
        .seq = std.mem.readInt(u32, buf[6..10], .big),
        .pts = @bitCast(std.mem.readInt(u64, buf[14..22], .big)),
        .payload = buf[frame_header_len..],
    };
}
