//! surfsrc.zig - the CONSUMER half of the producer→surface frame transport
//! (SDL_WEBVIEW_SURFACE_DESIGN §4.5, phase P3).
//!
//! A producer publishes frames as SHARED D3D11 TEXTURES named after the surface id; this file finds
//! them, synchronises on their keyed mutexes and hands the newest one to surfaces.zig to present.
//! The daemon is not in the path at all - not for the handle, not for the pixels, not for a rect.
//! That is the design's "preferred out-of-band handshake": the producer's objects EXISTING is the
//! bind, their disappearance is the unbind.
//!
//! Names (session namespace, not `Global\`: creating a Global\ kernel object needs
//! SeCreateGlobalPrivilege, which a user-session daemon does not have and must not ask for; both
//! processes are the same user + session, so `Local\` reaches exactly as far as it must):
//!   Local\rave-surface-<id>-ctl          file mapping, the control block below
//!   Local\rave-surface-<id>-g<gen>-s<i>  the ring's shared textures, gen-stamped
//!
//! The generation is in the texture NAME on purpose: a producer that re-sizes its ring publishes a
//! new generation rather than trying to recreate a name that is still open on our side (which
//! CreateSharedHandle refuses). The consumer notices gen changed and re-opens.
//!
//! RING BOUND (both halves state the same numbers): depth `max_slots` = 2 frames, i.e.
//! 2 * w * h * 4 bytes of shared VRAM (7.0 MiB at 1280x720, 16.6 MiB at 1920x1080) and nothing else
//! - there is no queue anywhere else in this path. Policy: the CONSUMER takes the NEWEST ready slot
//! and releases every older ready slot unread (drop-oldest), so a producer running ahead of the
//! compositor loses old frames, never accumulates; the PRODUCER, finding no slot released back to
//! it, drops its own newest frame rather than blocking its render loop.
//!
//! Frames are not "latest wins" blobs: every slot carries the producer's source PTS alongside its
//! sequence number, so a consumer can say WHICH frame it presented and how old it was - the
//! distinction the 4K-frozen-picture incident (#58) was diagnosed by.

const std = @import("std");
const d3d = @import("d3d11");
const wire = @import("wire.zig");

/// Ring depth. 2 = present/acquire, per §4.5. Bytes are w*h*4 per slot; the producer refuses a
/// geometry whose ring would exceed its own byte cap.
pub const max_slots: u32 = 2;

const ctl_magic: u32 = 0x52534631; // "RSF1"
const ctl_version: u32 = 1;
const ctl_bytes: usize = 4096;

// Control-block field offsets. MIRRORED BYTE-FOR-BYTE in internal/surfacepub/layout.go - change one,
// change both, and the version guard above is the tripwire if someone doesn't.
const off_magic = 0;
const off_version = 4;
const off_gen = 8;
const off_slots = 12;
const off_w = 16;
const off_h = 20;
const off_fmt = 24;
const off_pid = 28;
const off_write_seq = 32;
const off_prod_beat_ms = 40;
const off_want_w = 48; // consumer → producer: the surface's FULL rect, so it can render 1:1
const off_want_h = 52;
const off_cons_beat_ms = 56;
const off_present_seq = 64; // consumer → producer: last seq actually presented
const off_drop_count = 72; // consumer → producer: ready frames released unread
const off_slot0 = 128;
const slot_stride = 16; // seq u64, ptsNs i64

const key_producer: u64 = 0; // released with 0 = the producer may write
const key_consumer: u64 = 1; // released with 1 = a frame is ready for us

extern "kernel32" fn OpenFileMappingW(u32, i32, [*:0]const u16) callconv(.winapi) ?*anyopaque;
extern "kernel32" fn MapViewOfFile(*anyopaque, u32, u32, u32, usize) callconv(.winapi) ?[*]u8;
extern "kernel32" fn UnmapViewOfFile(*const anyopaque) callconv(.winapi) i32;
extern "kernel32" fn CloseHandle(*anyopaque) callconv(.winapi) i32;
extern "kernel32" fn GetTickCount64() callconv(.winapi) u64;

const file_map_all_access: u32 = 0xF001F;

fn ld32(p: [*]u8, off: usize) u32 {
    const q: *const u32 = @ptrCast(@alignCast(p + off));
    return @atomicLoad(u32, q, .acquire);
}
fn ld64(p: [*]u8, off: usize) u64 {
    const q: *const u64 = @ptrCast(@alignCast(p + off));
    return @atomicLoad(u64, q, .acquire);
}
fn st32(p: [*]u8, off: usize, v: u32) void {
    const q: *u32 = @ptrCast(@alignCast(p + off));
    @atomicStore(u32, q, v, .release);
}
fn st64(p: [*]u8, off: usize, v: u64) void {
    const q: *u64 = @ptrCast(@alignCast(p + off));
    @atomicStore(u64, q, v, .release);
}

/// Stats is what the surface can honestly say about its producer. Counters only - the CONTENT
/// oracle lives in Go (internal/framedebug + internal/testcard), because proving a picture MOVES
/// needs pixels on a CPU and this side deliberately never reads them back.
pub const Stats = struct {
    attached: bool = false,
    gen: u32 = 0,
    w: u32 = 0,
    h: u32 = 0,
    presented: u64 = 0,
    dropped: u64 = 0,
    stale_ms: i64 = -1, // since the last frame we presented
    last_seq: u64 = 0,
    last_pts_ns: i64 = 0,
};

/// Source is one surface's attachment to a producer. Zero value = never attached, and every method
/// is safe on it.
pub const Source = struct {
    hmap: ?*anyopaque = null,
    view: ?[*]u8 = null,
    gen: u32 = 0,
    slots: u32 = 0,
    w: u32 = 0,
    h: u32 = 0,
    tex: [max_slots]?*anyopaque = .{null} ** max_slots,
    km: [max_slots]?*anyopaque = .{null} ** max_slots,
    last_seq: u64 = 0,
    presented: u64 = 0,
    dropped: u64 = 0,
    last_present_ms: i64 = 0,
    last_pts_ns: i64 = 0,
    /// next_try_ms rate-limits the "is a producer there yet" probe to one OpenFileMapping per
    /// surface per attach_retry_ms - a surface with no producer must cost nothing per frame.
    next_try_ms: i64 = 0,
    logged_attach: bool = false,

    pub fn attached(s: *const Source) bool {
        return s.view != null and s.gen != 0;
    }

    pub fn stats(s: *const Source) Stats {
        return .{
            .attached = s.attached(),
            .gen = s.gen,
            .w = s.w,
            .h = s.h,
            .presented = s.presented,
            .dropped = s.dropped,
            .stale_ms = if (s.presented == 0) -1 else @as(i64, @intCast(GetTickCount64())) - s.last_present_ms,
            .last_seq = s.last_seq,
            .last_pts_ns = s.last_pts_ns,
        };
    }
};

const attach_retry_ms: i64 = 500;
/// acquire_ms bounds a keyed-mutex wait on the WINDOW thread. It has to be small: this runs in the
/// message pump, and a producer stuck mid-write must cost a dropped frame, never a frozen UI.
const acquire_ms: u32 = 3;

/// probe opens the control block if a producer has published one. Cheap and rate-limited; it is the
/// whole "bind" - no PSH1 message, no daemon courier (§4.5's preferred handshake).
pub fn probe(s: *Source, gpa: std.mem.Allocator, id: []const u8) void {
    if (s.view != null) return;
    const now: i64 = @intCast(GetTickCount64());
    if (now < s.next_try_ms) return;
    s.next_try_ms = now + attach_retry_ms;
    const name = std.fmt.allocPrint(gpa, "Local\\rave-surface-{s}-ctl", .{id}) catch return;
    defer gpa.free(name);
    const name16 = std.unicode.utf8ToUtf16LeAllocZ(gpa, name) catch return;
    defer gpa.free(name16);
    const h = OpenFileMappingW(file_map_all_access, 0, name16) orelse return;
    const view = MapViewOfFile(h, file_map_all_access, 0, 0, ctl_bytes) orelse {
        _ = CloseHandle(h);
        return;
    };
    if (ld32(view, off_magic) != ctl_magic or ld32(view, off_version) != ctl_version) {
        _ = UnmapViewOfFile(view);
        _ = CloseHandle(h);
        wire.errLine("rave-shell: surface producer control block has a foreign magic/version - ignoring");
        return;
    }
    s.hmap = h;
    s.view = view;
    s.gen = 0; // textures open lazily, once the producer publishes a generation
}

/// wants tells the producer the surface's FULL rect in device px. It renders at exactly that size,
/// which is what keeps the present path a 1:1 CROP (never a squash) - P2's open item.
pub fn wants(s: *Source, w: u32, h: u32) void {
    const view = s.view orelse return;
    st32(view, off_want_w, w);
    st32(view, off_want_h, h);
    st64(view, off_cons_beat_ms, GetTickCount64());
}

/// sync opens (or re-opens) the ring's textures when the producer's generation changes. Returns
/// false when there is nothing to present from.
fn sync(s: *Source, gpa: std.mem.Allocator, id: []const u8, dev1: ?*anyopaque) bool {
    const view = s.view orelse return false;
    const gen = ld32(view, off_gen);
    if (gen == 0) return false;
    if (gen == s.gen) return true;
    closeTextures(s);
    const dv = dev1 orelse return false;
    const slots = @min(ld32(view, off_slots), max_slots);
    const w = ld32(view, off_w);
    const h = ld32(view, off_h);
    if (slots == 0 or w == 0 or h == 0) return false;
    var i: u32 = 0;
    while (i < slots) : (i += 1) {
        const name = std.fmt.allocPrint(gpa, "Local\\rave-surface-{s}-g{d}-s{d}", .{ id, gen, i }) catch return false;
        defer gpa.free(name);
        const name16 = std.unicode.utf8ToUtf16LeAllocZ(gpa, name) catch return false;
        defer gpa.free(name16);
        const dd: *d3d.ID3D11Device1 = @ptrCast(@alignCast(dv));
        var tex: ?*anyopaque = null;
        const hr = dd.v.OpenSharedResourceByName(dv, name16, d3d.DXGI_SHARED_RESOURCE_READ | d3d.DXGI_SHARED_RESOURCE_WRITE, &d3d.IID_ID3D11Texture2D, &tex);
        if (d3d.failed(hr) or tex == null) {
            // A named open failing after the control block appeared is the ONE interesting failure
            // here (wrong adapter, producer died between the two, NTHANDLE not set) - say the HRESULT.
            var buf: [200]u8 = undefined;
            const m = std.fmt.bufPrint(&buf, "rave-shell: surface \"{s}\" OpenSharedResourceByName(gen {d} slot {d}) hr=0x{X:0>8}", .{ id, gen, i, @as(u32, @bitCast(hr)) }) catch return false;
            wire.errLine(m);
            closeTextures(s);
            return false;
        }
        s.tex[i] = tex;
        var kraw: ?*anyopaque = null;
        if (d3d.failed(d3d.qi(tex.?, &d3d.IID_IDXGIKeyedMutex, &kraw)) or kraw == null) {
            wire.errLine("rave-shell: surface producer texture has no IDXGIKeyedMutex - refusing an unsynchronised read");
            closeTextures(s);
            return false;
        }
        s.km[i] = kraw;
    }
    s.gen = gen;
    s.slots = slots;
    s.w = w;
    s.h = h;
    s.last_seq = 0;
    var buf: [180]u8 = undefined;
    const m = std.fmt.bufPrint(&buf, "rave-shell: surface \"{s}\" producer attached gen {d} {d}x{d} ring {d} ({d} KiB shared)", .{ id, gen, w, h, slots, (w * h * 4 * slots) / 1024 }) catch return true;
    wire.errLine(m);
    s.logged_attach = true;
    return true;
}

/// Frame is a slot the caller may read for the duration of `endFrame`.
pub const Frame = struct {
    tex: *anyopaque,
    slot: u32,
    seq: u64,
    pts_ns: i64,
    w: u32,
    h: u32,
};

/// begin picks the newest ready frame, drops every older ready one (drop-oldest), and leaves that
/// slot's keyed mutex HELD. The caller must call end() with the same Frame.
pub fn begin(s: *Source, gpa: std.mem.Allocator, id: []const u8, dev1: ?*anyopaque) ?Frame {
    if (!sync(s, gpa, id, dev1)) return null;
    const view = s.view orelse return null;
    if (ld64(view, off_write_seq) == s.last_seq) return null; // nothing new; keep the last picture

    var best: ?u32 = null;
    var best_seq: u64 = s.last_seq;
    var i: u32 = 0;
    while (i < s.slots) : (i += 1) {
        const sq = ld64(view, off_slot0 + i * slot_stride);
        if (sq > best_seq) {
            best_seq = sq;
            best = i;
        }
    }
    const pick = best orelse return null;

    // Drop-oldest: any OTHER slot still holding a frame we are about to skip is released unread, or
    // it starves the producer (its AcquireSync(0) would time out on that slot forever).
    i = 0;
    while (i < s.slots) : (i += 1) {
        if (i == pick) continue;
        const sq = ld64(view, off_slot0 + i * slot_stride);
        if (sq <= s.last_seq or sq >= best_seq) continue;
        const km: *d3d.IDXGIKeyedMutex = @ptrCast(@alignCast(s.km[i] orelse continue));
        const hr = km.v.AcquireSync(s.km[i].?, key_consumer, 0);
        if (hr == 0 or hr == d3d.WAIT_ABANDONED) {
            _ = km.v.ReleaseSync(s.km[i].?, key_producer);
            s.dropped += 1;
        }
    }

    const km: *d3d.IDXGIKeyedMutex = @ptrCast(@alignCast(s.km[pick] orelse return null));
    const hr = km.v.AcquireSync(s.km[pick].?, key_consumer, acquire_ms);
    if (d3d.failed(hr) or hr == d3d.WAIT_TIMEOUT) return null; // producer mid-write: try next tick
    return .{
        .tex = s.tex[pick].?,
        .slot = pick,
        .seq = best_seq,
        .pts_ns = @bitCast(ld64(view, off_slot0 + pick * slot_stride + 8)),
        .w = s.w,
        .h = s.h,
    };
}

/// end releases the slot back to the producer and books the frame. presented=false still releases -
/// a held mutex is the one failure that wedges the whole transport.
pub fn end(s: *Source, f: Frame, presented: bool) void {
    const km: *d3d.IDXGIKeyedMutex = @ptrCast(@alignCast(s.km[f.slot].?));
    _ = km.v.ReleaseSync(s.km[f.slot].?, key_producer);
    s.last_seq = f.seq;
    s.last_pts_ns = f.pts_ns;
    if (!presented) return;
    s.presented += 1;
    s.last_present_ms = @intCast(GetTickCount64());
    if (s.view) |view| {
        st64(view, off_present_seq, f.seq);
        st64(view, off_drop_count, s.dropped);
        st64(view, off_cons_beat_ms, GetTickCount64());
    }
}

fn closeTextures(s: *Source) void {
    var i: usize = 0;
    while (i < max_slots) : (i += 1) {
        if (s.km[i]) |k| d3d.release(k);
        if (s.tex[i]) |t| d3d.release(t);
        s.km[i] = null;
        s.tex[i] = null;
    }
    s.gen = 0;
    s.slots = 0;
}

/// close detaches from the producer entirely (surface gone, or shell shutting down).
pub fn close(s: *Source) void {
    closeTextures(s);
    if (s.view) |v| _ = UnmapViewOfFile(v);
    if (s.hmap) |h| _ = CloseHandle(h);
    s.view = null;
    s.hmap = null;
    s.presented = 0;
    s.dropped = 0;
    s.last_seq = 0;
}

// ── geometry: where the producer's picture lands, and what of it survives the scroll clip ──

/// Blit is one CopySubresourceRegion, already clipped: the SOURCE box in producer pixels and the
/// DESTINATION origin in back-buffer pixels.
pub const Blit = struct {
    sx: u32,
    sy: u32,
    sw: u32,
    sh: u32,
    dx: u32,
    dy: u32,
    /// covers = the copy fills the whole back buffer, so the letterbox clear can be skipped.
    covers: bool,
};

/// planBlit maps a source picture of (sw,sh) onto the element's FULL rect (fx,fy,fw,fh) and clips it
/// to the VISIBLE rect (vx,vy,vw,vh) - both in client device px, both reported by surfaces.js.
///
/// This is P2's open item closed. P2 sized the swapchain to the VISIBLE rect and painted it, which
/// is invisible for a solid colour and WRONG for a picture: scrolling an element half out of view
/// squashed the frame into the remaining strip. The picture is instead pinned to the FULL rect
/// (centred, never scaled - the producer renders at the size the consumer asks for) and the part of
/// it outside the visible rect is simply not copied. Scrolled out = CROPPED.
pub fn planBlit(sw: i64, sh: i64, fx: i64, fy: i64, fw: i64, fh: i64, vx: i64, vy: i64, vw: i64, vh: i64) ?Blit {
    if (sw <= 0 or sh <= 0 or vw <= 0 or vh <= 0) return null;
    // Picture origin in client space: centred in the full rect. With the producer honouring `wants`
    // this is exactly (fx,fy); the centring only matters for the frames between a resize and the
    // producer's new generation, where a squash would otherwise appear.
    const px = fx + @divFloor(fw - sw, 2);
    const py = fy + @divFloor(fh - sh, 2);
    // Overlap of the picture rect with the visible rect, in client space.
    const l = @max(px, vx);
    const t = @max(py, vy);
    const r = @min(px + sw, vx + vw);
    const b = @min(py + sh, vy + vh);
    if (r <= l or b <= t) return null;
    return .{
        .sx = @intCast(l - px),
        .sy = @intCast(t - py),
        .sw = @intCast(r - l),
        .sh = @intCast(b - t),
        .dx = @intCast(l - vx),
        .dy = @intCast(t - vy),
        .covers = (r - l) == vw and (b - t) == vh,
    };
}

test "planBlit: exact fit covers the whole back buffer" {
    const b = planBlit(800, 600, 10, 20, 800, 600, 10, 20, 800, 600).?;
    try std.testing.expectEqual(@as(u32, 0), b.sx);
    try std.testing.expectEqual(@as(u32, 0), b.sy);
    try std.testing.expectEqual(@as(u32, 800), b.sw);
    try std.testing.expectEqual(@as(u32, 600), b.sh);
    try std.testing.expect(b.covers);
}

test "planBlit: scrolled half out CROPS, it does not squash" {
    // Element is 800x600 at y=0 but only its bottom 300px are inside the scroll port.
    const b = planBlit(800, 600, 0, -300, 800, 600, 0, 0, 800, 300).?;
    try std.testing.expectEqual(@as(u32, 300), b.sy); // source starts 300 rows down
    try std.testing.expectEqual(@as(u32, 300), b.sh); // and only 300 rows are copied
    try std.testing.expectEqual(@as(u32, 800), b.sw); // width UNCHANGED - no scaling anywhere
    try std.testing.expectEqual(@as(u32, 0), b.dy);
    try std.testing.expect(b.covers);
}

test "planBlit: smaller picture letterboxes inside the full rect" {
    const b = planBlit(400, 200, 0, 0, 800, 600, 0, 0, 800, 600).?;
    try std.testing.expectEqual(@as(u32, 200), b.dx); // (800-400)/2
    try std.testing.expectEqual(@as(u32, 200), b.dy); // (600-200)/2
    try std.testing.expect(!b.covers); // so the letterbox clear must run
}

test "planBlit: fully scrolled out yields nothing" {
    try std.testing.expect(planBlit(800, 600, 0, -900, 800, 600, 0, 0, 800, 300) == null);
    try std.testing.expect(planBlit(800, 600, 0, 0, 800, 600, 0, 0, 0, 0) == null);
}
