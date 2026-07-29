//! cap.zig - zero-copy capture of a video-share sender's DX11 shared texture (zigmedia inc 1).
//!
//! The parent resolves the sender's `dxShareHandle` + `dwFormat` from the Spout name registry
//! (two scalars in shared memory) and passes them in `open`. We open that texture on OUR OWN
//! D3D11 device and hand it to the video processor as its INPUT VIEW - so the sender's pixels
//! go straight from its texture into the NV12 pool and on into the encoder MFT. No GPU→CPU
//! readback, no host frame buffer, no OpenGL context anywhere, and no input texture of our own
//! (-33 MB VRAM per 4K session).
//!
//! Deliberate non-goals (see .devnotes/ZIGMEDIA_DESIGN.md §2.1):
//!   - No SpoutLibrary.dll here. SPOUTLIBRARY is a ~150-slot C++ abstract class with
//!     std::string/std::vector returns; hand-rolling that vtable buys nothing when the child
//!     needs exactly two scalars. Only DXGI/D3D11 is used.
//!   - No Spout shared-memory layout parsing either (undocumented; the handle round trip is
//!     cheap and the parent already caches the registry scan).
//!
//! Synchronisation, in preference order: the texture's own IDXGIKeyedMutex when the sender
//! exposes one, else Spout's named access mutex, else unsynchronised with a loud counter. The
//! acquire timeout is ALWAYS bounded (1..4 ms) and the lock is held across ONE GPU-queued Blt
//! and nothing else: hammering or holding a sender's texture mutex serialises against the
//! sending app's and DWM's submissions, which is the documented cause of systemic pointer lag
//! on the sender PC. Waiting on the encoder happens BEFORE the acquire, never inside it.
//!
//! Row order: identity, no flip. The readback path receives with `bInvert=false`, i.e. Spout's
//! WGL_NV_DX_interop2 view of the DX texture read out unflipped, and that buffer is uploaded
//! with UpdateSubresource - so "shared-texture row 0" is already the row the encoder treats as
//! the top. Reading the same texture directly must therefore NOT introduce a flip.

const std = @import("std");
const mf = @import("mf.zig");

extern "kernel32" fn OpenMutexW(u32, i32, [*:0]const u16) callconv(.winapi) ?*anyopaque;
extern "kernel32" fn WaitForSingleObject(*anyopaque, u32) callconv(.winapi) u32;
extern "kernel32" fn ReleaseMutex(*anyopaque) callconv(.winapi) i32;
extern "kernel32" fn CloseHandle(*anyopaque) callconv(.winapi) i32;

const SYNCHRONIZE: u32 = 0x00100000;
const MUTEX_MODIFY_STATE: u32 = 1;
const WAIT_OBJECT_0: u32 = 0;
const WAIT_ABANDONED: u32 = 0x80;

// capFlags bits mirrored in the SHM header (offset 108) and in ProcStats.
pub const flag_zerocopy: u32 = 1 << 0;
pub const flag_keyed_mutex: u32 = 1 << 1;
pub const flag_named_mutex: u32 = 1 << 2;
pub const flag_unsynchronized: u32 = 1 << 3;

// Acquire budget: a tick that cannot get the texture in this long is SKIPPED (mtxTimeouts++),
// never retried in a spin - the pacing rate is the honest bound (design §6).
pub const acquire_ms: u32 = 3;

/// Reason strings for the `srcgone` / `opened.err_src` protocol fields (design §3.3).
pub const Reason = enum {
    open_shared,
    fmt_unsupported,
    dim_mismatch,
    view_failed,
    copy_failed,
    copy_tex_failed, // OUR copy destination could not be created - ours, not the source's
    acquire_dead,

    pub fn text(r: Reason) []const u8 {
        return switch (r) {
            .open_shared => "open_shared",
            .fmt_unsupported => "fmt_unsupported",
            .dim_mismatch => "dim_mismatch",
            .view_failed => "view_failed",
            .copy_failed => "copy_failed",
            .copy_tex_failed => "copy_tex_failed",
            .acquire_dead => "acquire_dead",
        };
    }
};

pub const OpenErr = error{CapOpenFailed};

/// vpInputFormatOK is the EXPLICIT format allowlist (risk R4). Shared textures are sometimes
/// _TYPELESS - CreateVideoProcessorInputView refuses those and a guessed typed view is
/// undefined behaviour, not a workaround - and exotic formats may be refused by the VP anyway,
/// which is why CheckVideoProcessorFormat still gates every accepted value.
pub fn vpInputFormatOK(fmt: u32) bool {
    return switch (fmt) {
        87, // B8G8R8A8_UNORM (the common Spout format)
        88, // B8G8R8X8_UNORM
        28, // R8G8B8A8_UNORM
        24, // R10G10B10A2_UNORM
        10, // R16G16B16A16_FLOAT
        => true,
        else => false,
    };
}

/// Grab is one pacing tick's verdict.
/// Grab is one pacing tick's verdict. `encfail` is deliberately NOT `dead`: an encoder wedge is
/// not a source problem, and reporting it as `srcgone` made the parent burn its three source
/// recycles and then PIN A HEALTHY SENDER to the readback path - i.e. an encoder bug was
/// "fixed" by disabling zero-copy for that sender. With zero-copy the default path, that
/// misattribution would silently demote every route on a rig with a wedged MFT.
pub const Grab = enum { ok, timeout, dead, encfail, busy };

/// Cap owns the opened shared texture + its VP input view + whatever sync the sender offers.
/// No allocator use after open; no per-frame allocation at all.
pub const Cap = struct {
    tex: *anyopaque, // the SENDER's texture, opened on our device (we own this reference)
    /// our OWN texture, the CopyResource destination. Restores the lock invariant this file's
    /// header states: the sender's mutex is held across ONE cheap GPU-queued copy and its submit,
    /// never across the RGBA->NV12 colour conversion. Commit 705a455 had the CSC Blt (a full
    /// per-pixel convert, and on some drivers a scale) inside the lock, where Spout's own reference
    /// receiver holds it across a plain CopyResource + Flush; every extra microsecond in there
    /// serialises against the SENDING app's and DWM's submissions, which is the documented cause of
    /// pointer lag on the sender PC. Costs 33 MB VRAM per 4K session - deliberately paid.
    copy_tex: *anyopaque,
    view: *anyopaque, // VP input view over copy_tex (NOT over the sender's texture)
    share: u64, // the handle we opened (parent compares it on rescan: stale-handle oracle R1)
    fmt: u32 = 0, // DXGI format actually consumed
    flags: u32 = 0,
    kmutex: ?*mf.IDXGIKeyedMutex = null,
    amutex: ?*anyopaque = null, // Spout's named access mutex (CPU mutex)
    has_view: bool = false, // c.view holds a real reference: close() must be safe on every
    // partial-open path, releasing exactly what was created
    has_copy: bool = false, // same, for copy_tex
    reason: Reason = .open_shared, // last failure reason (valid when open/grab failed)
    enc_rc: i32 = 0, // last encoder return code when feed returned .encfail (0 = none)

    /// open resolves the sender's texture and builds the VP input view over it. `want_w/h` are
    /// the session's negotiated input geometry: a sender that does not match is REFUSED (the
    /// parent reopens with the right dims) - never silently encoded at the wrong size.
    pub fn open(
        gpa: std.mem.Allocator,
        dev: *mf.ID3D11Device,
        vdev: *mf.ID3D11VideoDevice,
        vpe: *anyopaque,
        share: u64,
        sname: []const u8,
        want_w: i32,
        want_h: i32,
        out: *Reason,
    ) OpenErr!Cap {
        out.* = .open_shared;
        if (share == 0) return error.CapOpenFailed;
        var raw: ?*anyopaque = null;
        // Legacy SHARED handle (Spout hands out a D3D11_RESOURCE_MISC_SHARED handle, not an NT
        // handle), so OpenSharedResource is the right call, not OpenSharedResource1.
        if (mf.failed(dev.v.OpenSharedResource(@ptrCast(dev), @ptrFromInt(@as(usize, @intCast(share))), &mf.IID_ID3D11Texture2D, &raw)) or raw == null) {
            return error.CapOpenFailed;
        }
        var c = Cap{ .tex = raw.?, .copy_tex = undefined, .view = undefined, .share = share, .flags = flag_zerocopy };
        errdefer c.close();

        const tex: *mf.ID3D11Texture2D = @ptrCast(@alignCast(c.tex));
        var desc: mf.D3D11_TEXTURE2D_DESC = undefined;
        tex.v.GetDesc(@ptrCast(tex), &desc);
        c.fmt = desc.Format;
        if (want_w > 0 and want_h > 0 and (desc.Width != @as(u32, @intCast(want_w)) or desc.Height != @as(u32, @intCast(want_h)))) {
            out.* = .dim_mismatch;
            return error.CapOpenFailed;
        }
        if (!vpInputFormatOK(desc.Format)) {
            out.* = .fmt_unsupported;
            return error.CapOpenFailed;
        }
        const en: *mf.ID3D11VideoProcessorEnumerator = @ptrCast(@alignCast(vpe));
        var sup: u32 = 0;
        if (mf.failed(en.v.CheckVideoProcessorFormat(@ptrCast(en), desc.Format, &sup)) or sup & 1 == 0) {
            out.* = .fmt_unsupported;
            return error.CapOpenFailed;
        }

        // Sync: keyed mutex first (the texture's own, GPU-timeline correct), then Spout's named
        // access mutex, then unsynchronised + a counted flag. NEVER an unbounded wait (R3).
        var kraw: ?*anyopaque = null;
        if (!mf.failed(mf.qi(tex, &mf.IID_IDXGIKeyedMutex, &kraw)) and kraw != null) {
            c.kmutex = @ptrCast(@alignCast(kraw.?));
            c.flags |= flag_keyed_mutex;
        } else if (openAccessMutex(gpa, sname)) |h| {
            c.amutex = h;
            c.flags |= flag_named_mutex;
        } else {
            c.flags |= flag_unsynchronized;
        }

        // Our own copy target, same geometry+format as the sender's, no sharing flags. The VP input
        // view is built over THIS, so the conversion never reads the sender's texture and therefore
        // never needs the sender's lock (see the copy_tex doc comment).
        var cd = mf.D3D11_TEXTURE2D_DESC{
            .Width = desc.Width,
            .Height = desc.Height,
            .MipLevels = 1,
            .ArraySize = 1,
            .Format = desc.Format,
            .SampleCount = 1,
            .SampleQuality = 0,
            .Usage = 0, // DEFAULT
            // SHADER_RESOURCE|RENDER_TARGET is the combination this codebase already proves a
            // VideoProcessor accepts as an INPUT view (mf.zig's own in_tex). SHADER_RESOURCE alone
            // made CreateVideoProcessorInputView refuse the texture on this rig (view_failed).
            .BindFlags = mf.bind_shader_resource | mf.bind_render_target,
            .CPUAccessFlags = 0,
            .MiscFlags = 0,
        };
        var ctex: ?*anyopaque = null;
        if (mf.failed(dev.v.CreateTexture2D(@ptrCast(dev), &cd, null, &ctex)) or ctex == null) {
            out.* = .copy_tex_failed;
            return error.CapOpenFailed;
        }
        c.copy_tex = ctex.?;
        c.has_copy = true;

        const ivd = mf.VPIV_DESC{ .FourCC = 0, .ViewDimension = 1, .MipSlice = 0, .ArraySlice = 0 };
        var iv: ?*anyopaque = null;
        if (mf.failed(vdev.v.CreateVideoProcessorInputView(@ptrCast(vdev), c.copy_tex, vpe, &ivd, &iv)) or iv == null) {
            out.* = .view_failed;
            return error.CapOpenFailed;
        }
        c.view = iv.?;
        c.has_view = true;
        return c;
    }

    /// feed runs one pacing tick: wait for a free encoder slot (OUTSIDE the sender's lock),
    /// acquire, one GPU-queued Blt, release, then submit. `.timeout` = skip this tick,
    /// `.dead` = the source is unusable (the caller emits srcgone and stops capturing).
    pub fn feed(c: *Cap, e: *mf.Enc, pts100: i64, sink: mf.AuSink) Grab {
        c.enc_rc = 0;
        const g = e.gateInput(sink); // the ENCODER could not free a slot: not the sender's fault
        if (g == mf.RC_BUSY) return .busy; // saturated: skip this tick, never end the route
        if (g < 0) {
            c.enc_rc = g;
            return .encfail;
        }
        switch (c.acquire()) {
            .ok => {},
            .timeout => return .timeout,
            .dead => {
                c.reason = .acquire_dead;
                return .dead;
            },
            // unreachable from acquire(); keeps the switch exhaustive
            .encfail, .busy => return .encfail,
        }
        // INSIDE the lock: one plain CopyResource, then submit it. Nothing else.
        //
        // The submit is load-bearing, not redundant: D3D11 only QUEUES, so releasing the mutex
        // before flushing left the read of the sender's texture unsubmitted - the sender was free
        // to overwrite it mid-read, and on the named-mutex path nothing ever made this device see
        // the sender's writes at all (the keyed-mutex path escapes that only because ReleaseSync
        // carries an implicit flush). What must NOT be in here is the RGBA->NV12 conversion: it is
        // a full per-pixel pass, and holding a sender's mutex across it serialises the sending app
        // and DWM behind us. Copy in, convert out - the same split Spout's reference receiver uses.
        e.ctx.v.CopyResource(@ptrCast(e.ctx), c.copy_tex, c.tex);
        if (c.kmutex == null) e.flushCtx();
        c.release();

        // OUTSIDE the lock: the colour conversion, reading OUR copy.
        const slot = e.bltView(c.view);
        if (slot < 0) {
            // Over our own texture now, so a failure here is ours, not the source's - but the
            // parent's only recovery is a reopen either way, and copy_failed is what it maps to.
            c.reason = .copy_failed;
            return .dead;
        }
        const rc = e.submitSlot(@intCast(slot), pts100, sink);
        if (rc == mf.RC_BUSY) return .busy;
        if (rc < 0) {
            c.enc_rc = rc;
            return .encfail;
        }
        return .ok;
    }

    fn acquire(c: *Cap) Grab {
        if (c.kmutex) |k| {
            const hr = k.v.AcquireSync(@ptrCast(k), 0, acquire_ms);
            if (hr == 0) return .ok;
            // WAIT_TIMEOUT (0x102) / WAIT_ABANDONED (0x80) come back as the HRESULT value.
            if (hr == 0x102) return .timeout;
            return .dead;
        }
        if (c.amutex) |m| {
            return switch (WaitForSingleObject(m, acquire_ms)) {
                WAIT_OBJECT_0, WAIT_ABANDONED => .ok, // abandoned = sender died holding it: ours now
                else => .timeout,
            };
        }
        return .ok; // unsynchronised (counted via flag_unsynchronized)
    }

    fn release(c: *Cap) void {
        if (c.kmutex) |k| {
            _ = k.v.ReleaseSync(@ptrCast(k), 0);
            return;
        }
        if (c.amutex) |m| _ = ReleaseMutex(m);
    }

    pub fn close(c: *Cap) void {
        if (c.kmutex) |k| mf.release(k);
        c.kmutex = null;
        if (c.amutex) |m| _ = CloseHandle(m);
        c.amutex = null;
        if (c.has_view) mf.release(@as(*mf.IUnk, @ptrCast(@alignCast(c.view))));
        c.has_view = false;
        if (c.has_copy) mf.release(@as(*mf.IUnk, @ptrCast(@alignCast(c.copy_tex))));
        c.has_copy = false;
        mf.release(@as(*mf.IUnk, @ptrCast(@alignCast(c.tex))));
    }
};

/// openAccessMutex opens Spout's named access mutex for a sender.
///
/// Name: "<sender>_SpoutAccessMutex" - Spout's SpoutSharedMemory/spoutDirectX create it with
/// exactly that suffix. If a future SDK renames it we get null here, which lands on the
/// unsynchronised path with `flag_unsynchronized` set and counted - visibly wrong, never
/// silently wrong.
fn openAccessMutex(gpa: std.mem.Allocator, sname: []const u8) ?*anyopaque {
    if (sname.len == 0 or sname.len > 256) return null;
    const name = std.fmt.allocPrint(gpa, "{s}_SpoutAccessMutex", .{sname}) catch return null;
    defer gpa.free(name);
    const w = gpa.allocSentinel(u16, name.len, 0) catch return null;
    defer gpa.free(w);
    for (name, 0..) |ch, i| w[i] = ch; // sender names are ASCII on this path
    return OpenMutexW(SYNCHRONIZE | MUTEX_MODIFY_STATE, 0, w.ptr);
}

test "vp input format allowlist refuses TYPELESS + exotic" {
    try std.testing.expect(vpInputFormatOK(87)); // B8G8R8A8_UNORM
    try std.testing.expect(vpInputFormatOK(28)); // R8G8B8A8_UNORM
    try std.testing.expect(!vpInputFormatOK(86)); // B8G8R8A8_TYPELESS
    try std.testing.expect(!vpInputFormatOK(27)); // R8G8B8A8_TYPELESS
    try std.testing.expect(!vpInputFormatOK(0)); // UNKNOWN
    try std.testing.expect(!vpInputFormatOK(103)); // NV12 (an encoder output, never an input here)
}

test "reason strings match the wire contract" {
    try std.testing.expectEqualStrings("open_shared", Reason.open_shared.text());
    try std.testing.expectEqualStrings("fmt_unsupported", Reason.fmt_unsupported.text());
    try std.testing.expectEqualStrings("acquire_dead", Reason.acquire_dead.text());
    try std.testing.expectEqualStrings("copy_failed", Reason.copy_failed.text());
}
