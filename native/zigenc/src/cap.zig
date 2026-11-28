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
const d3d = @import("d3d11");

extern "kernel32" fn QueryPerformanceCounter(*i64) callconv(.winapi) i32;
extern "kernel32" fn QueryPerformanceFrequency(*i64) callconv(.winapi) i32;
extern "kernel32" fn SwitchToThread() callconv(.winapi) i32;
extern "kernel32" fn Sleep(u32) callconv(.winapi) void;
extern "kernel32" fn OpenMutexW(u32, i32, [*:0]const u16) callconv(.winapi) ?*anyopaque;
extern "kernel32" fn WaitForSingleObject(*anyopaque, u32) callconv(.winapi) u32;
extern "kernel32" fn ReleaseMutex(*anyopaque) callconv(.winapi) i32;
extern "kernel32" fn CloseHandle(*anyopaque) callconv(.winapi) i32;
extern "kernel32" fn OpenSemaphoreW(u32, i32, [*:0]const u16) callconv(.winapi) ?*anyopaque;
// Read-only count query: never perturbs the semaphore (Spout's own receivers Wait+Release it, which
// transiently dips the count under the sender's own Wait(0)). Stable ntdll export since NT 3.1.
extern "ntdll" fn NtQuerySemaphore(*anyopaque, u32, *SemInfo, u32, ?*u32) callconv(.winapi) i32;

const SemInfo = extern struct { cur: i32 = 0, max: i32 = 0 };
const SEMAPHORE_QUERY_STATE: u32 = 0x0001;

const SYNCHRONIZE: u32 = 0x00100000;
const MUTEX_MODIFY_STATE: u32 = 1;
const WAIT_OBJECT_0: u32 = 0;
const WAIT_ABANDONED: u32 = 0x80;

// capFlags bits mirrored in the SHM header (offset 108) and in ProcStats.
pub const flag_zerocopy: u32 = 1 << 0;
pub const flag_keyed_mutex: u32 = 1 << 1;
pub const flag_named_mutex: u32 = 1 << 2;
pub const flag_unsynchronized: u32 = 1 << 3;
pub const flag_frame_counter: u32 = 1 << 4; // sender's Spout frame counter readable: new-frame gating live
pub const flag_gpu_compare: u32 = 1 << 5; // no counter: exact GPU compare vs last encoded frame gates encode
pub const flag_compare_off: u32 = 1 << 6; // no counter AND compare unavailable on this device: blind pacing

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

/// Grab is one pacing tick's verdict. `encfail` is deliberately NOT `dead`: an encoder wedge is
/// not a source problem, and reporting it as `srcgone` made the parent burn its three source
/// recycles and then PIN A HEALTHY SENDER to the readback path - i.e. an encoder bug was
/// "fixed" by disabling zero-copy for that sender. With zero-copy the default path, that
/// misattribution would silently demote every route on a rig with a wedged MFT.
/// `dup` = the GPU compare found the copy bit-identical to the last ENCODED frame: no CSC, no encode.
pub const Grab = enum { ok, timeout, dead, encfail, busy, dup };

/// Detect is how one tick decides "new frame". compare = run the GPU compare (sender has no frame
/// counter and the compare is live); force = encode regardless (first frame, keep-alive, IDR);
/// budget_ns bounds the wait for the compare verdict (an unready verdict encodes: never a miss).
pub const Detect = struct { compare: bool = false, force: bool = false, budget_ns: u64 = 4 * std.time.ns_per_ms };

pub const CmpState = enum { untried, live, failed };

/// framecmp_enabled: process-wide switch (env RAVE_MATE_MFENC_FRAMECMP=0 clears it at start).
pub var framecmp_enabled: bool = true;

/// Cap owns the opened shared texture + its VP input view + whatever sync the sender offers.
/// No allocator use after open; no per-frame allocation at all.
pub const Cap = struct {
    tex: *anyopaque, // the SENDER's texture, opened on our device (we own this reference)
    /// our OWN textures, the CopyResource destinations. Restores the lock invariant this file's
    /// header states: the sender's mutex is held across ONE cheap GPU-queued copy and its submit,
    /// never across the RGBA->NV12 colour conversion. Commit 705a455 had the CSC Blt (a full
    /// per-pixel convert, and on some drivers a scale) inside the lock, where Spout's own reference
    /// receiver holds it across a plain CopyResource + Flush; every extra microsecond in there
    /// serialises against the SENDING app's and DWM's submissions, which is the documented cause of
    /// pointer lag on the sender PC. Costs 33 MB VRAM per 4K session - deliberately paid.
    ///
    /// copy[1] exists only while the GPU compare is live (counter-less sender; +33 MB at 4K, built
    /// lazily so counter/blind sessions never pay it): copy[cur] receives the tick's copy and
    /// copy[cur^1] holds the LAST ENCODED frame. Only a successful encode flips `cur`, so that
    /// invariant survives duplicates, busy drops and encoder failures.
    copy: [2]?*anyopaque = .{ null, null },
    view: [2]?*anyopaque = .{ null, null }, // VP input views over copy[i] (NOT over the sender's texture)
    cur: u1 = 0,
    have_prev: bool = false, // copy[cur^1] holds an encoded frame
    share: u64, // the handle we opened (parent compares it on rescan: stale-handle oracle R1)
    w: u32 = 0,
    h: u32 = 0,
    fmt: u32 = 0, // DXGI format actually consumed
    flags: u32 = 0,
    kmutex: ?*mf.IDXGIKeyedMutex = null,
    amutex: ?*anyopaque = null, // Spout's named access mutex (CPU mutex)
    reason: Reason = .open_shared, // last failure reason (valid when open/grab failed)
    fc: FrameCounter = .{}, // sender's frame counter (new-frame gating); absent = GPU compare / blind
    enc_rc: i32 = 0, // last encoder return code when feed returned .encfail (0 = none)
    // Borrowed from the encoder, which outlives the Cap: the lazy compare setup needs them.
    dev: *mf.ID3D11Device,
    vdev: *mf.ID3D11VideoDevice,
    vpe: *anyopaque,
    cmp: ?FrameCmp = null,
    cmp_state: CmpState = .untried,
    cmp_why: []const u8 = "", // why the compare is unavailable (one log line)
    cmp_changes: u64 = 0, // compare verdicts "changed" = producer frames seen (srcFrames)
    cmp_timeouts: u32 = 0, // verdicts not ready inside the budget (encoded anyway)
    cmp_lat_ns: u64 = 0, // EWMA (1/8) of issue -> verdict: copy + compare GPU time + queueing

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
        var c = Cap{ .tex = raw.?, .share = share, .flags = flag_zerocopy, .fc = FrameCounter.init(sname), .dev = dev, .vdev = vdev, .vpe = vpe };
        errdefer c.close();

        const tex: *mf.ID3D11Texture2D = @ptrCast(@alignCast(c.tex));
        var desc: mf.D3D11_TEXTURE2D_DESC = undefined;
        tex.v.GetDesc(@ptrCast(tex), &desc);
        c.fmt = desc.Format;
        c.w = desc.Width;
        c.h = desc.Height;
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

        // Our own copy target + its VP input view. The view is built over THIS, so the conversion
        // never reads the sender's texture and therefore never needs the sender's lock.
        if (c.makeCopy(0)) |r| {
            out.* = r;
            return error.CapOpenFailed;
        }
        return c;
    }

    /// makeCopy creates copy[i] (same geometry+format as the sender's, no sharing flags) and its VP
    /// input view. null = ok, else the failure reason (whatever was created stays for close()).
    fn makeCopy(c: *Cap, i: usize) ?Reason {
        var cd = mf.D3D11_TEXTURE2D_DESC{
            .Width = c.w,
            .Height = c.h,
            .MipLevels = 1,
            .ArraySize = 1,
            .Format = c.fmt,
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
        if (mf.failed(c.dev.v.CreateTexture2D(@ptrCast(c.dev), &cd, null, &ctex)) or ctex == null) return .copy_tex_failed;
        c.copy[i] = ctex.?;
        const ivd = mf.VPIV_DESC{ .FourCC = 0, .ViewDimension = 1, .MipSlice = 0, .ArraySlice = 0 };
        var iv: ?*anyopaque = null;
        if (mf.failed(c.vdev.v.CreateVideoProcessorInputView(@ptrCast(c.vdev), ctex.?, c.vpe, &ivd, &iv)) or iv == null) return .view_failed;
        c.view[i] = iv.?;
        return null;
    }

    fn dropCopy(c: *Cap, i: usize) void {
        if (c.view[i]) |v| relAny(v);
        c.view[i] = null;
        if (c.copy[i]) |t| relAny(t);
        c.copy[i] = null;
    }

    /// compareReady lazily builds the GPU compare (second copy + shaders + queries) and self-tests it
    /// ONCE per session. false = unavailable on this device (WARP, feature level < 10, a driver whose
    /// occlusion count fails the self-test): the session stays blind-paced and `cmp_why` says why.
    pub fn compareReady(c: *Cap, ctx: *mf.ID3D11DeviceContext, warp: bool) bool {
        switch (c.cmp_state) {
            .live => return true,
            .failed => return false,
            .untried => {},
        }
        c.cmp_state = .failed;
        if (!framecmp_enabled) {
            c.cmp_why = "disabled (RAVE_MATE_MFENC_FRAMECMP=0)";
            return false;
        }
        // WARP rasterises on the CPU: a 4K compare there is a CPU pixel pass, which this detector
        // exists to avoid. Blind pacing is the honest fallback.
        if (warp) {
            c.cmp_why = "warp device";
            return false;
        }
        if (c.makeCopy(1)) |_| {
            c.dropCopy(1);
            c.cmp_why = "second copy texture";
            return false;
        }
        c.cmp = FrameCmp.init(c.dev, .{ c.copy[0].?, c.copy[1].? }, c.w, c.h) catch |err| {
            c.dropCopy(1);
            c.cmp_why = switch (err) {
                error.Shader => "shader create (feature level < 10?)",
                error.Query => "occlusion query create",
                error.View => "shader resource view",
            };
            return false;
        };
        if (!c.cmp.?.selfTest(ctx, .{ c.copy[0].?, c.copy[1].? })) {
            c.cmp.?.deinit();
            c.cmp = null;
            c.dropCopy(1);
            c.cmp_why = "self-test (occlusion count wrong on this driver)";
            return false;
        }
        c.cmp_state = .live;
        return true;
    }

    /// feed runs one pacing tick: wait for a free encoder slot (OUTSIDE the sender's lock),
    /// acquire, one GPU-queued copy, release, then (compare mode) the duplicate check, convert and
    /// submit. `.timeout` = skip this tick, `.dead` = the source is unusable (the caller emits
    /// srcgone and stops capturing), `.dup` = identical to the last encoded frame, nothing encoded.
    pub fn feed(c: *Cap, e: *mf.Enc, pts100: i64, sink: mf.AuSink, det: Detect) Grab {
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
            .encfail, .busy, .dup => return .encfail,
        }
        // INSIDE the lock: one plain CopyResource, then submit it. Nothing else - the compare adds
        // NOTHING here: only the destination alternates between copy[0] and copy[1].
        //
        // The submit is load-bearing, not redundant: D3D11 only QUEUES, so releasing the mutex
        // before flushing left the read of the sender's texture unsubmitted - the sender was free
        // to overwrite it mid-read, and on the named-mutex path nothing ever made this device see
        // the sender's writes at all (the keyed-mutex path escapes that only because ReleaseSync
        // carries an implicit flush). What must NOT be in here is the RGBA->NV12 conversion: it is
        // a full per-pixel pass, and holding a sender's mutex across it serialises the sending app
        // and DWM behind us. Copy in, convert out - the same split Spout's reference receiver uses.
        const cur = c.cur;
        e.ctx.v.CopyResource(@ptrCast(e.ctx), c.copy[cur].?, c.tex);
        if (c.kmutex == null) e.flushCtx();
        c.release();

        // OUTSIDE the lock: exact duplicate check of OUR copy against the last encoded frame (GPU
        // only; the result is an occlusion count, never a pixel). Unready inside the budget = encode.
        if (det.compare and !det.force and c.have_prev and c.cmp != null) {
            const t0 = qpcNs();
            const v = c.cmp.?.verdict(e.ctx, cur, det.budget_ns);
            const lat = qpcNs() - t0;
            c.cmp_lat_ns = if (c.cmp_lat_ns == 0) lat else c.cmp_lat_ns - c.cmp_lat_ns / 8 + lat / 8;
            switch (v) {
                .same => return .dup,
                .changed => c.cmp_changes += 1,
                .unknown => c.cmp_timeouts += 1,
            }
        }

        // OUTSIDE the lock: the colour conversion, reading OUR copy.
        const slot = e.bltView(c.view[cur].?);
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
        if (c.cmp_state == .live) {
            c.cur ^= 1; // copy[cur] is now the last encoded frame
            c.have_prev = true;
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
        c.fc.close();
        if (c.cmp) |*f| f.deinit();
        c.cmp = null;
        if (c.kmutex) |k| mf.release(k);
        c.kmutex = null;
        if (c.amutex) |m| _ = CloseHandle(m);
        c.amutex = null;
        c.dropCopy(0);
        c.dropCopy(1);
        relAny(c.tex);
    }
};

fn relAny(p: *anyopaque) void {
    mf.release(@as(*mf.IUnk, @ptrCast(@alignCast(p))));
}

var qpc_freq: i64 = 0;
fn qpcNs() u64 {
    if (qpc_freq == 0) _ = QueryPerformanceFrequency(&qpc_freq);
    var t: i64 = 0;
    _ = QueryPerformanceCounter(&t);
    if (qpc_freq <= 0) return 0;
    return @intCast(@divTrunc(@as(i128, t) * std.time.ns_per_s, qpc_freq));
}

const vs_dxbc = @embedFile("framecmp_vs.dxbc");
const ps_dxbc = @embedFile("framecmp_ps.dxbc");

/// FrameCmp is the EXACT new-frame detector for senders without a frame counter (framecmp.hlsl): a
/// full-screen pass samples copy[cur] and copy[cur^1] texel-exact and discards equal pixels, inside an
/// occlusion query - the query's sample count is the number of CHANGED pixels. One changed pixel
/// anywhere = changed; nothing is subsampled. No render target, no GL, no pixel ever reaches the CPU:
/// the only thing read back is the query's u64.
///
/// The pass runs on the encoder's (possibly shared) immediate context: the state + draw sequence is
/// held under ID3D10Multithread Enter/Leave so another session cannot interleave its bindings, and
/// every binding is cleared afterwards (copy[cur] is the next CopyResource destination).
pub const FrameCmp = struct {
    vs: *anyopaque,
    ps: *anyopaque,
    q: [2]*anyopaque, // alternated, so a verdict abandoned on timeout is never re-Begun while pending
    qi: u1 = 0,
    srv: [2]*anyopaque, // over copy[0], copy[1]
    mt: ?*mf.ID3D10Multithread,
    vp: d3d.VIEWPORT,

    pub const Verdict = enum { same, changed, unknown };
    pub const Err = error{ Shader, Query, View };

    pub fn init(dev: *mf.ID3D11Device, tex: [2]*anyopaque, w: u32, h: u32) Err!FrameCmp {
        const dv: *anyopaque = @ptrCast(dev);
        var vs: ?*anyopaque = null;
        if (mf.failed(dev.v.CreateVertexShader(dv, vs_dxbc.ptr, vs_dxbc.len, null, &vs)) or vs == null) return error.Shader;
        errdefer relAny(vs.?);
        var ps: ?*anyopaque = null;
        if (mf.failed(dev.v.CreatePixelShader(dv, ps_dxbc.ptr, ps_dxbc.len, null, &ps)) or ps == null) return error.Shader;
        errdefer relAny(ps.?);
        const qd = d3d.QUERY_DESC{ .Query = d3d.QUERY_OCCLUSION };
        var q0: ?*anyopaque = null;
        if (mf.failed(dev.v.CreateQuery(dv, &qd, &q0)) or q0 == null) return error.Query;
        errdefer relAny(q0.?);
        var q1: ?*anyopaque = null;
        if (mf.failed(dev.v.CreateQuery(dv, &qd, &q1)) or q1 == null) return error.Query;
        errdefer relAny(q1.?);
        var s0: ?*anyopaque = null;
        if (mf.failed(dev.v.CreateShaderResourceView(dv, tex[0], null, &s0)) or s0 == null) return error.View;
        errdefer relAny(s0.?);
        var s1: ?*anyopaque = null;
        if (mf.failed(dev.v.CreateShaderResourceView(dv, tex[1], null, &s1)) or s1 == null) return error.View;
        var mraw: ?*anyopaque = null;
        const mt: ?*mf.ID3D10Multithread = if (!mf.failed(mf.qi(dev, &d3d.IID_ID3D10Multithread, &mraw)) and mraw != null) @ptrCast(@alignCast(mraw.?)) else null;
        return .{
            .vs = vs.?,
            .ps = ps.?,
            .q = .{ q0.?, q1.? },
            .srv = .{ s0.?, s1.? },
            .mt = mt,
            .vp = .{ .TopLeftX = 0, .TopLeftY = 0, .Width = @floatFromInt(w), .Height = @floatFromInt(h), .MinDepth = 0, .MaxDepth = 1 },
        };
    }

    pub fn deinit(f: *FrameCmp) void {
        if (f.mt) |m| mf.release(m);
        for (f.srv) |s| relAny(s);
        for (f.q) |q| relAny(q);
        relAny(f.ps);
        relAny(f.vs);
    }

    /// issue queues "count pixels where copy[cur] != copy[cur^1]" and submits it; returns the query.
    pub fn issue(f: *FrameCmp, ctx: *mf.ID3D11DeviceContext, cur: u1) *anyopaque {
        const q = f.q[f.qi];
        f.qi ^= 1;
        const cx: *anyopaque = @ptrCast(ctx);
        if (f.mt) |m| m.v.Enter(@ptrCast(m));
        defer if (f.mt) |m| m.v.Leave(@ptrCast(m));
        const srvs = [2]?*anyopaque{ f.srv[cur], f.srv[cur ^ 1] };
        const none = [2]?*anyopaque{ null, null };
        ctx.v.OMSetRenderTargets(cx, 0, null, null); // no RT: the query is the only output
        ctx.v.RSSetState(cx, null); // default: solid, back-face cull (the triangle is clockwise), no scissor
        ctx.v.RSSetViewports(cx, 1, @ptrCast(&f.vp));
        ctx.v.IASetInputLayout(cx, null);
        ctx.v.IASetPrimitiveTopology(cx, d3d.PRIMITIVE_TOPOLOGY_TRIANGLELIST);
        ctx.v.VSSetShader(cx, f.vs, null, 0);
        ctx.v.PSSetShader(cx, f.ps, null, 0);
        ctx.v.PSSetShaderResources(cx, 0, 2, &srvs);
        ctx.v.Begin(cx, q);
        ctx.v.Draw(cx, 3, 0);
        ctx.v.End(cx, q);
        ctx.v.PSSetShaderResources(cx, 0, 2, &none);
        ctx.v.PSSetShader(cx, null, null, 0);
        ctx.v.VSSetShader(cx, null, null, 0);
        ctx.v.Flush(cx);
        return q;
    }

    /// wait polls q (DONOTFLUSH: issue already submitted) for at most budget_ns. null = not ready or
    /// device error. Yields for the first 0.5 ms (the usual 4K verdict), then sleeps 1 ms steps.
    pub fn wait(ctx: *mf.ID3D11DeviceContext, q: *anyopaque, budget_ns: u64) ?u64 {
        const t0 = qpcNs();
        var n: u64 = 0;
        while (true) {
            const hr = ctx.v.GetData(@ptrCast(ctx), q, &n, @sizeOf(u64), d3d.GETDATA_DONOTFLUSH);
            if (hr == 0) return n;
            if (hr < 0) return null;
            const el = qpcNs() - t0;
            if (el >= budget_ns) return null;
            if (el < 500 * std.time.ns_per_us) _ = SwitchToThread() else Sleep(1);
        }
    }

    pub fn verdict(f: *FrameCmp, ctx: *mf.ID3D11DeviceContext, cur: u1, budget_ns: u64) Verdict {
        const n = wait(ctx, f.issue(ctx, cur), budget_ns) orelse return .unknown;
        return if (n == 0) .same else .changed;
    }

    /// selfTest proves the mechanism on THIS driver before it is trusted to skip anything: two
    /// identical textures must count 0, and one differing texel must count >= 1. A driver that
    /// skips the pixel shader with no render target bound would read 0 for both - and silently
    /// freeze every route - so it lands on blind pacing instead.
    pub fn selfTest(f: *FrameCmp, ctx: *mf.ID3D11DeviceContext, tex: [2]*anyopaque) bool {
        const cx: *anyopaque = @ptrCast(ctx);
        const budget = 250 * std.time.ns_per_ms; // one-off, at the first counter-less tick
        ctx.v.CopyResource(cx, tex[1], tex[0]);
        const same = wait(ctx, f.issue(ctx, 0), budget) orelse return false;
        if (same != 0) return false;
        // One texel, 8 bytes covers every allowed format (4 B/texel UNORM, 8 B/texel FP16); the mark
        // is non-zero under every interpretation (0x3C00 = fp16 1.0).
        const zero = [_]u8{0} ** 8;
        const mark = [_]u8{ 0x00, 0x3C } ** 4;
        const box = d3d.BOX{ .left = 0, .top = 0, .front = 0, .right = 1, .bottom = 1, .back = 1 };
        ctx.v.UpdateSubresource(cx, tex[0], 0, &box, &zero, 8, 0);
        ctx.v.UpdateSubresource(cx, tex[1], 0, &box, &mark, 8, 0);
        const diff = wait(ctx, f.issue(ctx, 0), budget) orelse return false;
        return diff >= 1;
    }
};

/// Fresh is one frame-counter poll's verdict.
pub const Fresh = enum { new, same, unknown };

/// FrameCounter reads a Spout 2.007 sender's frame counter WITHOUT a GL context or any pixel access:
/// the sender keeps a named semaphore "<sender>_Count_Semaphore" whose COUNT is its frame number
/// (SpoutFrameCount.cpp: created with count 1, SetNewFrame = Wait(0) + Release(2) per sent frame).
/// Absent (registry "Framecount" off, pre-2.007 or non-SDK sender, app called DisableFrameCount) =
/// `.unknown`, and the caller falls back to blind pacing. Allocation-free after init.
///
/// A frozen count while the semaphore exists is indistinguishable from a static picture; Spout's own
/// receivers gate on the same signal (IsFrameNew), so a sender that pauses counting while still
/// sending already freezes every Spout receiver. One case we CAN catch: a sender that closed its
/// handle (DisableFrameCount) leaves the object alive only through OUR handle - so a counter that has
/// not moved for `stuck_reopen_ns` is closed and reopened, which drops a dead object and falls back.
pub const FrameCounter = struct {
    name: [name_cap:0]u16 = undefined,
    name_ok: bool = false,
    sem: ?*anyopaque = null,
    last: i64 = -1, // highest count seen (-1 = none: first poll is .new)
    moved_ns: u64 = 0, // when `last` last advanced
    next_try_ns: u64 = 0, // reopen backoff while absent
    advances: u64 = 0, // new frames observed (producer-rate telemetry)

    const name_cap = 256 + 16;
    const suffix = "_Count_Semaphore";
    pub const retry_ns: u64 = 2 * std.time.ns_per_s;
    pub const stuck_reopen_ns: u64 = 2 * std.time.ns_per_s;
    // A real reset (new object, count restarts at 1) vs the transient -1 dip of the sender's own
    // Wait(0)+Release(2) and of other receivers' Wait+Release.
    const reset_dip: i64 = 64;

    pub fn init(sname: []const u8) FrameCounter {
        var fc = FrameCounter{};
        if (sname.len == 0 or sname.len + suffix.len >= name_cap) return fc;
        var i: usize = 0;
        for (sname) |ch| {
            fc.name[i] = ch; // sender names are ASCII on this path (openAccessMutex does the same)
            i += 1;
        }
        for (suffix) |ch| {
            fc.name[i] = ch;
            i += 1;
        }
        fc.name[i] = 0;
        fc.name_ok = true;
        return fc;
    }

    pub fn present(fc: *const FrameCounter) bool {
        return fc.sem != null;
    }

    /// poll reads the counter once (one OpenSemaphore at most every retry_ns while absent).
    pub fn poll(fc: *FrameCounter, now_ns: u64) Fresh {
        if (fc.sem == null) {
            if (!fc.name_ok or now_ns < fc.next_try_ns) return .unknown;
            fc.next_try_ns = now_ns + retry_ns;
            fc.sem = OpenSemaphoreW(SYNCHRONIZE | SEMAPHORE_QUERY_STATE, 0, @ptrCast(&fc.name));
            if (fc.sem == null) return .unknown;
            if (fc.moved_ns == 0) fc.moved_ns = now_ns;
        }
        var info = SemInfo{};
        if (NtQuerySemaphore(fc.sem.?, 0, &info, @sizeOf(SemInfo), null) != 0) {
            fc.drop(now_ns);
            return .unknown;
        }
        const v = fc.classify(info.cur, now_ns);
        if (v == .same and now_ns - fc.moved_ns >= stuck_reopen_ns) {
            fc.drop(0); // stuck: release our hold so a dead object can vanish; reopen next poll
            fc.moved_ns = now_ns;
        }
        return v;
    }

    /// classify maps one count reading to a verdict (pure; `last` survives a reopen of the same object).
    pub fn classify(fc: *FrameCounter, cur: i64, now_ns: u64) Fresh {
        if (cur > fc.last or cur < fc.last - reset_dip) {
            fc.last = cur;
            fc.moved_ns = now_ns;
            fc.advances += 1;
            return .new;
        }
        return .same;
    }

    fn drop(fc: *FrameCounter, retry_at: u64) void {
        if (fc.sem) |h| _ = CloseHandle(h);
        fc.sem = null;
        fc.next_try_ns = retry_at;
    }

    pub fn close(fc: *FrameCounter) void {
        fc.drop(0);
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

test "frame counter: first read new, repeat same, sender dip ignored, reset accepted" {
    var fc = FrameCounter.init("s");
    try std.testing.expectEqual(Fresh.new, fc.classify(5, 1));
    try std.testing.expectEqual(Fresh.same, fc.classify(5, 2));
    try std.testing.expectEqual(Fresh.same, fc.classify(4, 3)); // sender's Wait(0) before Release(2)
    try std.testing.expectEqual(Fresh.new, fc.classify(6, 4));
    try std.testing.expectEqual(Fresh.new, fc.classify(500, 5));
    try std.testing.expectEqual(Fresh.new, fc.classify(1, 6)); // new object: count restarted
    try std.testing.expectEqual(Fresh.same, fc.classify(1, 7));
    try std.testing.expectEqual(@as(u64, 4), fc.advances);
}

test "frame counter name = <sender>_Count_Semaphore, refuses empty + oversize" {
    const fc = FrameCounter.init("ab");
    try std.testing.expect(fc.name_ok);
    const want = std.unicode.utf8ToUtf16LeStringLiteral("ab_Count_Semaphore");
    try std.testing.expectEqualSlices(u16, want, std.mem.sliceTo(@as([*:0]const u16, @ptrCast(&fc.name)), 0));
    try std.testing.expect(!FrameCounter.init("").name_ok);
    try std.testing.expect(!FrameCounter.init("x" ** 300).name_ok);
}

test "frame counter absent = unknown, no busy reopen" {
    var fc = FrameCounter.init("rave-mate zigenc test no such sender 7f3a");
    try std.testing.expectEqual(Fresh.unknown, fc.poll(10));
    try std.testing.expectEqual(Fresh.unknown, fc.poll(11)); // inside retry_ns: no OpenSemaphore
    try std.testing.expect(fc.next_try_ns == 10 + FrameCounter.retry_ns);
}

extern "kernel32" fn CreateSemaphoreW(?*anyopaque, i32, i32, [*:0]const u16) callconv(.winapi) ?*anyopaque;
extern "kernel32" fn ReleaseSemaphore(*anyopaque, i32, ?*i32) callconv(.winapi) i32;

test "frame counter reads a live Spout-shaped semaphore without perturbing it" {
    // Sender side exactly as SpoutFrameCount.cpp: CreateSemaphore(initial 1, max LONG_MAX), then per
    // frame Wait(0) + Release(2).
    const sem = CreateSemaphoreW(null, 1, std.math.maxInt(i32), std.unicode.utf8ToUtf16LeStringLiteral("rave-mate zigenc fc gate_Count_Semaphore")) orelse return error.SkipZigTest;
    defer _ = CloseHandle(sem);
    var fc = FrameCounter.init("rave-mate zigenc fc gate");
    defer fc.close();
    try std.testing.expectEqual(Fresh.new, fc.poll(1));
    try std.testing.expect(fc.present());
    try std.testing.expectEqual(Fresh.same, fc.poll(2));
    try std.testing.expectEqual(Fresh.same, fc.poll(3)); // query is read-only: count unchanged
    _ = WaitForSingleObject(sem, 0);
    _ = ReleaseSemaphore(sem, 2, null);
    try std.testing.expectEqual(Fresh.new, fc.poll(4));
    try std.testing.expectEqual(Fresh.same, fc.poll(5));
    var prev: i32 = 0;
    _ = ReleaseSemaphore(sem, 1, &prev);
    try std.testing.expectEqual(@as(i32, 2), prev); // 1 + one frame, the poller never moved it
}

// GPU gate for FrameCmp on the real device (this rig: RTX 3060). Exactness at 4K: one texel changed
// anywhere (here the last one) must flip the verdict, and the identical pair must read 0. Then the
// GPU cost of copy vs compare at 3840x2160 from timestamp queries (no CPU clock in the number).
test "frame compare: exact on a real 4K device, GPU cost of copy vs compare" {
    var dev_o: ?*mf.ID3D11Device = null;
    var ctx_o: ?*mf.ID3D11DeviceContext = null;
    if (mf.failed(mf.api.createD3D11Device(null, 1, null, 0, null, 0, 7, &dev_o, null, &ctx_o)) or dev_o == null) return error.SkipZigTest;
    const dev = dev_o.?;
    const ctx = ctx_o.?;
    defer relAny(dev);
    defer relAny(ctx);
    const dv: *anyopaque = @ptrCast(dev);
    const cx: *anyopaque = @ptrCast(ctx);
    const W: u32 = 3840;
    const H: u32 = 2160;
    var td = mf.D3D11_TEXTURE2D_DESC{ .Width = W, .Height = H, .MipLevels = 1, .ArraySize = 1, .Format = 87, .SampleCount = 1, .SampleQuality = 0, .Usage = 0, .BindFlags = mf.bind_shader_resource | mf.bind_render_target, .CPUAccessFlags = 0, .MiscFlags = 0 };
    var tex: [3]*anyopaque = undefined; // [0],[1] = copy pair, [2] = a stand-in "sender" texture
    for (&tex) |*t| {
        var o: ?*anyopaque = null;
        try std.testing.expect(!mf.failed(dev.v.CreateTexture2D(dv, &td, null, &o)) and o != null);
        t.* = o.?;
    }
    defer for (tex) |t| relAny(t);
    var f = try FrameCmp.init(dev, .{ tex[0], tex[1] }, W, H);
    defer f.deinit();
    try std.testing.expect(f.selfTest(ctx, .{ tex[0], tex[1] }));

    const budget = 500 * std.time.ns_per_ms;
    ctx.v.CopyResource(cx, tex[1], tex[0]);
    try std.testing.expectEqual(FrameCmp.Verdict.same, f.verdict(ctx, 0, budget));
    const px = [_]u8{ 0x01, 0x02, 0x03, 0xFF };
    const last = d3d.BOX{ .left = W - 1, .top = H - 1, .front = 0, .right = W, .bottom = H, .back = 1 };
    ctx.v.UpdateSubresource(cx, tex[0], 0, &last, &px, 4, 0);
    try std.testing.expectEqual(@as(?u64, 1), FrameCmp.wait(ctx, f.issue(ctx, 0), budget));
    try std.testing.expectEqual(FrameCmp.Verdict.changed, f.verdict(ctx, 1, budget)); // order-independent
    // Alpha-only change counts: all four channels are compared.
    ctx.v.CopyResource(cx, tex[1], tex[0]);
    const px_a = [_]u8{ 0x01, 0x02, 0x03, 0xFE };
    ctx.v.UpdateSubresource(cx, tex[0], 0, &last, &px_a, 4, 0);
    try std.testing.expectEqual(FrameCmp.Verdict.changed, f.verdict(ctx, 0, budget));

    // Cost on INCOMPRESSIBLE content (an uninitialised or flat texture rides the GPU's colour
    // compression and under-reports both numbers ~5x): noise in the "sender" and in the retained copy,
    // differing everywhere, so the compare reads every texel of both. Worst case for the compare.
    const noise = try std.testing.allocator.alloc(u8, W * H * 4);
    defer std.testing.allocator.free(noise);
    var x: u64 = 0x9E3779B97F4A7C15;
    for (std.mem.bytesAsSlice(u64, noise)) |*v| {
        x ^= x << 13;
        x ^= x >> 7;
        x ^= x << 17;
        v.* = x;
    }
    ctx.v.UpdateSubresource(cx, tex[2], 0, null, noise.ptr, W * 4, 0);
    for (noise) |*b| b.* +%= 1;
    ctx.v.UpdateSubresource(cx, tex[1], 0, null, noise.ptr, W * 4, 0);
    // Cost: N rounds of [copy sender->copy[0]] then [compare copy[0] vs copy[1]], timestamps between.
    var qd = d3d.QUERY_DESC{ .Query = d3d.QUERY_TIMESTAMP_DISJOINT };
    var dj: ?*anyopaque = null;
    try std.testing.expect(!mf.failed(dev.v.CreateQuery(dv, &qd, &dj)));
    defer relAny(dj.?);
    qd.Query = d3d.QUERY_TIMESTAMP;
    var ts: [3]*anyopaque = undefined;
    for (&ts) |*t| {
        var o: ?*anyopaque = null;
        try std.testing.expect(!mf.failed(dev.v.CreateQuery(dv, &qd, &o)));
        t.* = o.?;
    }
    defer for (ts) |t| relAny(t);
    const rounds = 30;
    var copy_ticks: u64 = 0;
    var cmp_ticks: u64 = 0;
    var freq: u64 = 0;
    var r: usize = 0;
    var counted: usize = 0;
    while (r < rounds) : (r += 1) {
        ctx.v.Begin(cx, dj.?);
        ctx.v.End(cx, ts[0]);
        ctx.v.CopyResource(cx, tex[0], tex[2]);
        ctx.v.End(cx, ts[1]);
        const q = f.issue(ctx, 0);
        ctx.v.End(cx, ts[2]);
        ctx.v.End(cx, dj.?);
        ctx.v.Flush(cx);
        _ = FrameCmp.wait(ctx, q, budget);
        var dd: d3d.QUERY_TIMESTAMP_DISJOINT_DATA = undefined;
        while (ctx.v.GetData(cx, dj.?, &dd, @sizeOf(d3d.QUERY_TIMESTAMP_DISJOINT_DATA), 0) != 0) {}
        var t: [3]u64 = undefined;
        for (ts, 0..) |tq, i| {
            while (ctx.v.GetData(cx, tq, &t[i], 8, 0) != 0) {}
        }
        if (dd.Disjoint != 0 or r < 5) continue; // warm-up rounds + clock changes excluded
        freq = dd.Frequency;
        copy_ticks += t[1] - t[0];
        cmp_ticks += t[2] - t[1];
        counted += 1;
    }
    try std.testing.expect(counted > 0 and freq > 0);
    const ms = struct {
        fn of(ticks: u64, n: usize, hz: u64) f64 {
            return @as(f64, @floatFromInt(ticks)) * 1000.0 / @as(f64, @floatFromInt(hz)) / @as(f64, @floatFromInt(n));
        }
    }.of;
    std.debug.print("framecmp 3840x2160 GPU: copy {d:.3} ms, compare {d:.3} ms (mean of {d} rounds)\n", .{ ms(copy_ticks, counted, freq), ms(cmp_ticks, counted, freq), counted });
}
