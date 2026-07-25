//! WAV (RIFF) container parsing — byte-exact port of internal/audio/wav.go
//! (newWAVDecoder chunk loop + parseFmt, incl. WAVE_FORMAT_EXTENSIBLE and the
//! Go quirk that "fmt " is NOT pad-aligned: an odd fmt size shifts every later
//! chunk by one byte, exactly like the Go parser).

const std = @import("std");
const pcmdec = @import("pcmdec.zig");

const fmt_pcm = 0x0001;
const fmt_float = 0x0003;
const fmt_extensible = 0xFFFE;

pub fn onChunk(d: *pcmdec.Dec, id: *const [4]u8, sz: u64) pcmdec.Status {
    const body = d.chunk_off + 8;
    if (std.mem.eql(u8, id, "fmt ")) return d.reqBody(.wav_fmt, body, sz, sz);
    if (std.mem.eql(u8, id, "data")) {
        if (!d.have_fmt or d.block_align == 0) return d.fail(); // Go: "data before fmt"
        d.data_start = body;
        d.total = @divTrunc(@as(i64, @intCast(sz)), d.block_align);
        d.have_data = true;
        return d.finalize();
    }
    return d.reqChunk(body + sz + (sz & 1)); // unknown chunk: RIFF pads odd sizes
}

pub fn onBody(d: *pcmdec.Dec, buf: []const u8) pcmdec.Status {
    if (buf.len < d.chunk_sz) return d.fail(); // truncated fmt body (Go: ReadFull error)
    if (!parseFmt(d, buf[0..@intCast(d.chunk_sz)])) return d.fail();
    d.have_fmt = true;
    return d.reqChunk(d.chunk_off + 8 + d.chunk_sz); // NO odd pad — Go parser quirk
}

fn parseFmt(d: *pcmdec.Dec, b: []const u8) bool {
    if (b.len < 16) return false;
    var tag: u16 = std.mem.readInt(u16, b[0..2], .little);
    d.ch = std.mem.readInt(u16, b[2..4], .little);
    d.rate = std.mem.readInt(u32, b[4..8], .little);
    d.block_align = std.mem.readInt(u16, b[12..14], .little);
    d.bits = std.mem.readInt(u16, b[14..16], .little);
    if (tag == fmt_extensible) {
        if (b.len < 40) return false;
        tag = std.mem.readInt(u16, b[24..26], .little); // SubFormat GUID leads with the real tag
    }
    switch (tag) {
        fmt_pcm => {
            d.is_float = false;
            if (d.bits != 16 and d.bits != 24 and d.bits != 32 and d.bits != 8) return false;
        },
        fmt_float => {
            d.is_float = true;
            if (d.bits != 32 and d.bits != 64) return false;
        },
        else => return false, // compressed WAV not native — ffmpeg path
    }
    if (d.block_align == 0) d.block_align = @divTrunc(d.ch * d.bits, 8);
    // never smaller than the frame size — decode would index past the block
    // (crafted-file OOB guard; wav.go mirrors)
    if (d.block_align < @divTrunc(d.ch * d.bits, 8)) return false;
    return true;
}
