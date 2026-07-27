//! dec.zig - GPU-resident H.264/HEVC decode + publish (zigmedia inc 2, the receive side).
//!
//! Today's receive path is the sender path's mirror image and worse: wire AU → ffmpeg child →
//! 33 MB raw RGBA per 4K frame back up a stdout pipe → Go buffer → Spout SendImage (a second
//! upload). Here the compressed AUs ride an inbound SHM ring into this child, a D3D11-aware
//! decoder MFT turns them into NV12 surfaces on OUR device, and the video processor blits each one
//! straight into the destination Spout sender's shared texture. No raw frame ever crosses a pipe
//! or the Go heap.
//!
//! Who owns the destination sender: GO does. SPOUTLIBRARY has no CreateSender, so Go initialises
//! the sender (one zeroed frame, once per route) and passes its `GetHandle()` share handle here;
//! we open that texture on our device and use it as the VP OUTPUT view. Consequence, accepted in
//! the design: Spout bumps a sender's frame counter only inside SendTexture/SendImage, so a
//! receiver's IsFrameNew() hint goes stale - content updates are still seen, because receivers copy
//! the texture every tick. Same trade cap.zig already accepts on the capture side.
//!
//! Synchronisation is cap.zig's, in the same preference order (keyed mutex → Spout's named access
//! mutex → unsynchronised + a counted flag) with the same rule: the acquire timeout is ALWAYS
//! bounded and the lock covers ONE GPU-queued Blt and nothing else. Waiting on the decoder happens
//! outside it.
//!
//! Row order: identity, no flip - the same argument as cap.zig. The frame path publishes with
//! SendImage(bInvert=false) from a top-row-first buffer, so "shared-texture row 0" is the top row
//! a receiver expects, and writing the decoder's row 0 there must not introduce a flip.

const std = @import("std");
const mf = @import("mf.zig");
const api = mf.api;

extern "kernel32" fn OpenMutexW(u32, i32, [*:0]const u16) callconv(.winapi) ?*anyopaque;
extern "kernel32" fn WaitForSingleObject(*anyopaque, u32) callconv(.winapi) u32;
extern "kernel32" fn ReleaseMutex(*anyopaque) callconv(.winapi) i32;
extern "kernel32" fn CloseHandle(*anyopaque) callconv(.winapi) i32;

const SYNCHRONIZE: u32 = 0x00100000;
const MUTEX_MODIFY_STATE: u32 = 1;
const WAIT_OBJECT_0: u32 = 0;
const WAIT_ABANDONED: u32 = 0x80;

/// decFlags bits mirrored in the SHM header (offset 184) and in ProcDecStats.
pub const flag_live: u32 = 1 << 0;
pub const flag_keyed_mutex: u32 = 1 << 1;
pub const flag_named_mutex: u32 = 1 << 2;
pub const flag_unsynchronized: u32 = 1 << 3;
pub const flag_hw_decode: u32 = 1 << 4; // bound a TRUE hardware MFT (vs the D3D11-aware MS one)

/// acquire_ms: a publish that cannot get the destination texture in this long is SKIPPED
/// (decMtxTimeouts++), never retried in a spin - holding or hammering a Spout sender's mutex
/// serialises against every receiver's and DWM's GPU submissions.
pub const acquire_ms: u32 = 3;

/// AUPOOL bounds the input-sample ring. An MFT may hold an input sample after ProcessInput
/// returns, so a sample must not be reused immediately; 4 round-robin slots mean a slot is only
/// reused after 3 further accepted submissions. Cap in BYTES = AUPOOL * au_cap (au_cap is derived
/// from the ring size at open, 512 KiB..4 MiB) → at most 16 MiB of MF memory buffers per session.
/// Policy: an AU larger than au_cap gets a ONE-OFF sample released right after ProcessInput
/// (rare - a 4K IDR at 50 Mbps is ~1 MB) rather than being dropped.
const AUPOOL = 4;
const au_cap_min: u32 = 512 * 1024;
const au_cap_max: u32 = 4 * 1024 * 1024;

/// VIEWCACHE bounds the decoded-surface input views. A DXVA decoder recycles a small texture ARRAY,
/// so (texture, subresource) pairs repeat and creating a view per frame would be a COM allocation
/// on the hot path. Cap 16 entries; policy: round-robin evict + release the displaced view (a
/// decoder cycling more than 16 surfaces just pays the create again, it never grows).
const VIEWCACHE = 16;

/// Reason strings for the `dstgone` / `opened.err_dst` protocol fields.
pub const Reason = enum {
    open_shared,
    fmt_unsupported,
    dim_mismatch,
    view_failed,
    blt_failed,
    acquire_dead,
    decoder_failed,
    sw_decode_unsupported,

    pub fn text(r: Reason) []const u8 {
        return switch (r) {
            .open_shared => "open_shared",
            .fmt_unsupported => "fmt_unsupported",
            .dim_mismatch => "dim_mismatch",
            .view_failed => "view_failed",
            .blt_failed => "blt_failed",
            .acquire_dead => "acquire_dead",
            .decoder_failed => "decoder_failed",
            .sw_decode_unsupported => "sw_decode_unsupported",
        };
    }
};

pub const OpenErr = error{DecOpenFailed};

/// vpOutputFormatOK is the destination-texture format allowlist. Go pins B8G8R8A8_UNORM when it
/// creates the sender (spoutSenderFmt), so this is normally 87; the rest are the typed formats a
/// pre-existing sender may legitimately carry. _TYPELESS is refused for the same reason as on the
/// capture side: CreateVideoProcessorOutputView rejects it and guessing a typed view is undefined
/// behaviour, not a workaround.
pub fn vpOutputFormatOK(fmt: u32) bool {
    return switch (fmt) {
        87, // B8G8R8A8_UNORM (what Go asks Spout for)
        88, // B8G8R8X8_UNORM
        28, // R8G8B8A8_UNORM
        24, // R10G10B10A2_UNORM
        10, // R16G16B16A16_FLOAT
        => true,
        else => false,
    };
}

/// Publish is one decoded frame's verdict.
pub const Publish = enum { ok, timeout, dead };

const ViewEntry = struct { tex: ?*anyopaque = null, sub: u32 = 0, view: ?*anyopaque = null };

/// Dec is one decode+publish pipeline (one session). All calls from ONE thread (COM MTA).
pub const Dec = struct {
    gpa: std.mem.Allocator,

    // GPU: own device (the decoder MFT gets it through the device manager), video processor and
    // the destination view. Deliberately NOT shared with Enc: an encode session's pipeline is the
    // parity reference and inc 1's proven path, so the decode side builds its own rather than
    // refactoring it.
    dev: *mf.ID3D11Device = undefined,
    ctx: *mf.ID3D11DeviceContext = undefined,
    devmgr: *mf.IMFDXGIDeviceManager = undefined,
    vdev: *mf.ID3D11VideoDevice = undefined,
    vctx: *mf.ID3D11VideoContext = undefined,
    vpe: *anyopaque = undefined,
    vproc: *anyopaque = undefined,

    // destination (the Spout sender's texture, opened on our device)
    dst_tex: *anyopaque = undefined,
    dst_view: *anyopaque = undefined,
    dst_fmt: u32 = 0,
    share: u64 = 0,
    kmutex: ?*mf.IDXGIKeyedMutex = null,
    amutex: ?*anyopaque = null,
    flags: u32 = 0,
    // Ownership bits. close() runs on every PARTIAL-open path (errdefer) and a long-lived
    // per-adapter child sees many open/close cycles, so "release exactly what exists" is the only
    // safe rule - a device or MFT leaked per route would dwarf everything this increment saves.
    has_dev: bool = false,
    has_devmgr: bool = false,
    has_vdev: bool = false,
    has_vctx: bool = false,
    has_vpe: bool = false,
    has_vproc: bool = false,
    has_dec: bool = false,
    has_dst: bool = false,
    has_view: bool = false,

    // decoder
    dec: *mf.IMFTransform = undefined,
    evgen: ?*mf.IMFMediaEventGenerator = null,
    dec_provides: bool = false,
    dec_out_size: u32 = 0,
    need_input: i32 = 0,
    hevc: bool = false,

    // input sample pool
    in_smp: [AUPOOL]?*mf.IMFSample = @splat(null),
    in_buf: [AUPOOL]?*mf.IMFMediaBuffer = @splat(null),
    in_idx: usize = 0,
    au_cap: u32 = 0,

    views: [VIEWCACHE]ViewEntry = @splat(.{}),
    view_idx: usize = 0,

    in_w: i32,
    in_h: i32,
    out_w: i32,
    out_h: i32,
    fps_n: i32,
    fps_d: i32,
    dur100: i64,
    src_rect: mf.RECT = .{ .left = 0, .top = 0, .right = 0, .bottom = 0 },

    fed_n: u64 = 0,
    pub_n: u64 = 0,
    mtx_timeouts: u64 = 0, // destination-texture acquire timeouts (receiver/DWM contention)
    drain_done: bool = false,
    reason: Reason = .decoder_failed,
    name_buf: [128]u8 = @splat(0),
    name_len: usize = 0,

    pub fn name(d: *const Dec) []const u8 {
        return d.name_buf[0..d.name_len];
    }

    /// auCapFor sizes the pooled input buffers from the inbound ring (a quarter of it), clamped.
    pub fn auCapFor(ring_bytes: u64) u32 {
        const q: u64 = ring_bytes / 4;
        if (q < au_cap_min) return au_cap_min;
        if (q > au_cap_max) return au_cap_max;
        return @intCast(q);
    }

    /// open builds the whole pipeline. Every failure is CLEAN: out.* names the rung, the caller
    /// emits `opened{cap:"downgraded"}` and Go reopens the route on the ffmpeg decode path.
    pub fn open(
        gpa: std.mem.Allocator,
        luid: i64,
        hevc: bool,
        in_w: i32,
        in_h: i32,
        out_w: i32,
        out_h: i32,
        fps_n: i32,
        fps_d: i32,
        share: u64,
        dname: []const u8,
        ring_bytes: u64,
        out: *Reason,
    ) OpenErr!*Dec {
        out.* = .decoder_failed;
        const d = gpa.create(Dec) catch return error.DecOpenFailed;
        d.* = .{
            .gpa = gpa,
            .in_w = in_w,
            .in_h = in_h,
            .out_w = out_w,
            .out_h = out_h,
            .fps_n = fps_n,
            .fps_d = @max(fps_d, 1),
            .dur100 = @divTrunc(10_000_000 * @as(i64, @max(fps_d, 1)), @max(fps_n, 1)),
            .hevc = hevc,
            .share = share,
            .au_cap = auCapFor(ring_bytes),
            .src_rect = .{ .left = 0, .top = 0, .right = in_w, .bottom = in_h },
        };
        if (in_w <= 0 or in_h <= 0 or out_w <= 0 or out_h <= 0 or fps_n <= 0 or share == 0) {
            gpa.destroy(d);
            return error.DecOpenFailed;
        }
        errdefer d.close();

        api.traceStage("dec MFStartup", .{});
        if (mf.failed(api.startup(api.MF_VER, api.STARTUP_LITE))) return error.DecOpenFailed;
        try d.openDevice(luid);
        try d.openDest(gpa, dname, out);
        try d.openVideoProcessor(out);
        try d.bindDecoder(out);
        try d.negotiateTypes(out);
        try d.buildInputPool();

        _ = d.dec.v.ProcessMessage(@ptrCast(d.dec), api.MSG_BEGIN_STREAMING, 0);
        _ = d.dec.v.ProcessMessage(@ptrCast(d.dec), api.MSG_START_OF_STREAM, 0);
        d.flags |= flag_live;
        api.traceStage("dec open complete ({s})", .{d.name()});
        return d;
    }

    // openDevice: device + multithread protection + video interfaces. Same audit fixes as the
    // encode path (deliberate adapter pick, VIDEO_SUPPORT, SetMultithreadProtected before the
    // device manager) - a decoder MFT's worker threads share this device too.
    fn openDevice(d: *Dec, luid: i64) OpenErr!void {
        var vendor: u32 = 0;
        var adapter = mf.Enc.findAdapter(luid, &vendor);
        if (adapter == null) adapter = mf.Enc.pickDefaultAdapter(&vendor);
        defer if (adapter) |a| mf.release(a);

        api.traceStage("dec D3D11CreateDevice({s})", .{if (adapter != null) "pinned adapter" else "default"});
        var dev: ?*mf.ID3D11Device = null;
        var dctx: ?*mf.ID3D11DeviceContext = null;
        var hr = api.createD3D11Device(@ptrCast(adapter), if (adapter != null) api.DRIVER_UNKNOWN else api.DRIVER_HARDWARE, null, api.DEV_BGRA | api.DEV_VIDEO, null, 0, api.SDK_VERSION, &dev, null, &dctx);
        if (mf.failed(hr) or dev == null or dctx == null) return error.DecOpenFailed;
        d.dev = dev.?;
        d.ctx = dctx.?;
        d.has_dev = true;
        var mtraw: ?*anyopaque = null;
        if (!mf.failed(mf.qi(d.dev, &api.IID_MULTITHREAD, &mtraw)) and mtraw != null) {
            const mt: *mf.ID3D10Multithread = @ptrCast(@alignCast(mtraw.?));
            _ = mt.v.SetMultithreadProtected(@ptrCast(mt), 1);
            mf.release(mt);
        }
        var vraw: ?*anyopaque = null;
        if (mf.failed(mf.qi(d.dev, &api.IID_VIDEO_DEVICE, &vraw)) or vraw == null) return error.DecOpenFailed;
        d.vdev = @ptrCast(@alignCast(vraw.?));
        d.has_vdev = true;
        vraw = null;
        if (mf.failed(mf.qi(d.ctx, &api.IID_VIDEO_CONTEXT, &vraw)) or vraw == null) return error.DecOpenFailed;
        d.vctx = @ptrCast(@alignCast(vraw.?));
        d.has_vctx = true;

        var token: u32 = 0;
        var dm: ?*mf.IMFDXGIDeviceManager = null;
        hr = api.createDeviceManager(&token, &dm);
        if (mf.failed(hr) or dm == null) return error.DecOpenFailed;
        d.devmgr = dm.?;
        d.has_devmgr = true;
        if (mf.failed(d.devmgr.v.ResetDevice(@ptrCast(d.devmgr), @ptrCast(d.dev), token))) return error.DecOpenFailed;
    }

    // openDest opens the destination sender's shared texture + its access sync. Geometry MUST
    // match: a mismatch means Go and Spout disagree about the sender, and silently scaling into
    // the wrong size would ship a wrong picture instead of a clean downgrade.
    fn openDest(d: *Dec, gpa: std.mem.Allocator, dname: []const u8, out: *Reason) OpenErr!void {
        out.* = .open_shared;
        var raw: ?*anyopaque = null;
        // Legacy SHARED handle (Spout hands out D3D11_RESOURCE_MISC_SHARED, not an NT handle).
        if (mf.failed(d.dev.v.OpenSharedResource(@ptrCast(d.dev), @ptrFromInt(@as(usize, @intCast(d.share))), &mf.IID_ID3D11Texture2D, &raw)) or raw == null) {
            return error.DecOpenFailed;
        }
        d.dst_tex = raw.?;
        d.has_dst = true;
        const tex: *mf.ID3D11Texture2D = @ptrCast(@alignCast(d.dst_tex));
        var desc: mf.D3D11_TEXTURE2D_DESC = undefined;
        tex.v.GetDesc(@ptrCast(tex), &desc);
        d.dst_fmt = desc.Format;
        if (desc.Width != @as(u32, @intCast(d.out_w)) or desc.Height != @as(u32, @intCast(d.out_h))) {
            out.* = .dim_mismatch;
            return error.DecOpenFailed;
        }
        if (!vpOutputFormatOK(desc.Format)) {
            out.* = .fmt_unsupported;
            return error.DecOpenFailed;
        }
        var kraw: ?*anyopaque = null;
        if (!mf.failed(mf.qi(tex, &mf.IID_IDXGIKeyedMutex, &kraw)) and kraw != null) {
            d.kmutex = @ptrCast(@alignCast(kraw.?));
            d.flags |= flag_keyed_mutex;
        } else if (openAccessMutex(gpa, dname)) |h| {
            d.amutex = h;
            d.flags |= flag_named_mutex;
        } else {
            d.flags |= flag_unsynchronized;
        }
    }

    // openVideoProcessor builds the VP for decoded-NV12 → destination-format, plus the output view
    // over the sender's texture. Content desc input = the DECODED size, output = the sender's.
    fn openVideoProcessor(d: *Dec, out: *Reason) OpenErr!void {
        out.* = .fmt_unsupported;
        const cd = api.ContentDesc{
            .InputFrameFormat = 0, // progressive
            .InputFrameRate = .{ .Numerator = @intCast(d.fps_n), .Denominator = @intCast(d.fps_d) },
            .InputWidth = @intCast(d.in_w),
            .InputHeight = @intCast(d.in_h),
            .OutputFrameRate = .{ .Numerator = @intCast(d.fps_n), .Denominator = @intCast(d.fps_d) },
            .OutputWidth = @intCast(d.out_w),
            .OutputHeight = @intCast(d.out_h),
            .Usage = 0,
        };
        api.traceStage("dec CreateVideoProcessorEnumerator", .{});
        var vpe: ?*anyopaque = null;
        if (mf.failed(d.vdev.v.CreateVideoProcessorEnumerator(@ptrCast(d.vdev), &cd, &vpe)) or vpe == null) {
            return error.DecOpenFailed;
        }
        d.vpe = vpe.?;
        d.has_vpe = true;
        const en: *mf.ID3D11VideoProcessorEnumerator = @ptrCast(@alignCast(d.vpe));
        var sup: u32 = 0;
        if (mf.failed(en.v.CheckVideoProcessorFormat(@ptrCast(en), api.FMT_DXGI_NV12, &sup)) or sup & api.VP_SUPPORT_INPUT == 0) {
            return error.DecOpenFailed;
        }
        sup = 0;
        if (mf.failed(en.v.CheckVideoProcessorFormat(@ptrCast(en), d.dst_fmt, &sup)) or sup & mf.VP_FORMAT_SUPPORT_OUTPUT == 0) {
            return error.DecOpenFailed;
        }
        var vproc: ?*anyopaque = null;
        if (mf.failed(d.vdev.v.CreateVideoProcessor(@ptrCast(d.vdev), d.vpe, 0, &vproc)) or vproc == null) {
            out.* = .view_failed;
            return error.DecOpenFailed;
        }
        d.vproc = vproc.?;
        d.has_vproc = true;
        // Same colour contract as the encode half (YCbCr_Matrix = BT.709 both sides), which is what
        // makes a round-trip through both halves colour-neutral.
        const cs = api.ColorSpace{ .bits = 1 << 2 };
        d.vctx.v.VideoProcessorSetOutputColorSpace(@ptrCast(d.vctx), d.vproc, &cs);
        d.vctx.v.VideoProcessorSetStreamColorSpace(@ptrCast(d.vctx), d.vproc, 0, &cs);
        // Explicit source rect: a hardware decoder's NV12 surface is 16-row aligned (1088 for
        // 1080), and the VP would otherwise sample the whole surface and squash those rows in.
        d.vctx.v.VideoProcessorSetStreamSourceRect(@ptrCast(d.vctx), d.vproc, 0, 1, &d.src_rect);

        out.* = .view_failed;
        const ovd = api.VPOVDesc{ .ViewDimension = api.VPOV_TEXTURE2D, .a = 0, .b = 0, .c = 0 };
        var ov: ?*anyopaque = null;
        if (mf.failed(d.vdev.v.CreateVideoProcessorOutputView(@ptrCast(d.vdev), d.dst_tex, d.vpe, &ovd, &ov)) or ov == null) {
            return error.DecOpenFailed;
        }
        d.dst_view = ov.?;
        d.has_view = true;
    }

    // bindDecoder binds a D3D11-AWARE decoder MFT and hands it our device manager.
    //
    // Two enumeration passes: MFT_ENUM_FLAG_HARDWARE first (true vendor decoders), then unflagged -
    // the "Microsoft H264 Video Decoder MFT" is registered as a software MFT but does DXVA when
    // given a device manager, and on most rigs it IS the decoder. MF_SA_D3D11_AWARE is a HARD gate
    // either way: without it the output samples are system memory, and uploading NV12 rows by hand
    // would be exactly the host frame plane this increment removes. That case downgrades cleanly to
    // the ffmpeg path (sw_decode_unsupported).
    fn bindDecoder(d: *Dec, out: *Reason) OpenErr!void {
        out.* = .decoder_failed;
        const sub = if (d.hevc) api.FMT_HEVC else api.FMT_H264;
        const ti = api.RegisterTypeInfo{ .major = api.MT_VIDEO, .sub = sub };
        const to = api.RegisterTypeInfo{ .major = api.MT_VIDEO, .sub = api.FMT_NV12 };
        var aware_seen = false;
        var pass: u32 = 0;
        while (pass < 2) : (pass += 1) {
            const flags: u32 = if (pass == 0) api.ENUM_HARDWARE | api.ENUM_SORTFILTER else api.ENUM_SORTFILTER;
            var acts: ?[*]*mf.IMFActivate = null;
            var n: u32 = 0;
            api.traceStage("dec MFTEnumEx(pass {d})", .{pass});
            if (mf.failed(api.enumMFT(api.CAT_VIDEO_DECODER, flags, &ti, &to, &acts, &n)) or n == 0 or acts == null) continue;
            const list = acts.?[0..n];
            defer {
                for (list) |a| mf.release(a);
                api.coTaskMemFree(@ptrCast(acts));
            }
            for (list) |a| {
                var wname: [128]u16 = @splat(0);
                var fname: [128]u8 = @splat(0);
                var flen: usize = 0;
                const av: *const api.AttrVtbl_ = @ptrCast(@alignCast(a.v));
                if (!mf.failed(av.GetString(@ptrCast(a), &api.A_FRIENDLY_NAME, &wname, 127, null))) {
                    for (wname) |c| {
                        if (c == 0 or flen >= fname.len) break;
                        fname[flen] = if (c < 128) @intCast(c) else '?';
                        flen += 1;
                    }
                }
                var raw: ?*anyopaque = null;
                if (mf.failed(a.v.ActivateObject(@ptrCast(a), &api.IID_TRANSFORM, &raw)) or raw == null) continue;
                const t: *mf.IMFTransform = @ptrCast(@alignCast(raw.?));
                var awarev: u32 = 0;
                var attrs: ?*mf.IMFAttributes = null;
                if (!mf.failed(t.v.GetAttributes(@ptrCast(t), &attrs)) and attrs != null) {
                    awarev = api.getU32(attrs.?, &api.A_D3D11_AWARE) orelse 0;
                    api.setU32(attrs.?, &api.A_ASYNC_UNLOCK, 1);
                    api.setU32(attrs.?, &api.A_LOW_LATENCY, 1);
                    mf.release(attrs.?);
                }
                if (awarev == 0) {
                    mf.release(t);
                    _ = a.v.ShutdownObject(@ptrCast(a));
                    continue;
                }
                aware_seen = true;
                if (mf.failed(t.v.ProcessMessage(@ptrCast(t), api.MSG_SET_D3D_MANAGER, @intFromPtr(d.devmgr)))) {
                    mf.release(t);
                    _ = a.v.ShutdownObject(@ptrCast(a));
                    continue;
                }
                d.dec = t;
                d.has_dec = true;
                @memcpy(d.name_buf[0..flen], fname[0..flen]);
                d.name_len = flen;
                if (pass == 0) d.flags |= flag_hw_decode;
                return;
            }
        }
        if (!aware_seen) out.* = .sw_decode_unsupported;
        return error.DecOpenFailed;
    }

    // negotiateTypes sets the compressed input type and the NV12 output type, then resolves the
    // async/sync drive mode and the output-sample ownership.
    fn negotiateTypes(d: *Dec, out: *Reason) OpenErr!void {
        out.* = .decoder_failed;
        const sub = if (d.hevc) api.FMT_HEVC else api.FMT_H264;
        const in_mt = api.mediaType(&sub, d.in_w, d.in_h, d.fps_n, d.fps_d) orelse return error.DecOpenFailed;
        api.traceStage("dec SetInputType", .{});
        const hr = d.dec.v.SetInputType(@ptrCast(d.dec), 0, in_mt, 0);
        mf.release(in_mt);
        if (mf.failed(hr)) return error.DecOpenFailed;

        if (!d.setNV12Output()) return error.DecOpenFailed;

        var osi = api.OutputStreamInfo{ .dwFlags = 0, .cbSize = 0, .cbAlignment = 0 };
        if (!mf.failed(d.dec.v.GetOutputStreamInfo(@ptrCast(d.dec), 0, &osi))) {
            d.dec_provides = osi.dwFlags & (api.OUT_PROVIDES_SAMPLES | api.OUT_CAN_PROVIDE_SAMPLES) != 0;
            d.dec_out_size = osi.cbSize;
        }
        if (!d.dec_provides) {
            // Without MFT-provided samples we would have to allocate the NV12 surface ourselves and
            // hand it in - a different (and untested here) contract. Downgrade instead of guessing.
            out.* = .sw_decode_unsupported;
            return error.DecOpenFailed;
        }
        var eraw: ?*anyopaque = null;
        if (!mf.failed(mf.qi(d.dec, &api.IID_EVGEN, &eraw)) and eraw != null) {
            d.evgen = @ptrCast(@alignCast(eraw.?));
        }
        api.traceStage("dec async={} provides={} outSize={d}", .{ d.evgen != null, d.dec_provides, d.dec_out_size });
    }

    // setNV12Output picks the decoder's own NV12 output candidate (also used on a stream change).
    fn setNV12Output(d: *Dec) bool {
        var i: u32 = 0;
        while (true) : (i += 1) {
            var c: ?*mf.IMFMediaType = null;
            if (mf.failed(d.dec.v.GetOutputAvailableType(@ptrCast(d.dec), 0, i, &c)) or c == null) break;
            const s = api.getGUID(c.?, &api.A_SUBTYPE);
            if (s != null and api.guidsEqual(s.?, api.FMT_NV12)) {
                const hr = d.dec.v.SetOutputType(@ptrCast(d.dec), 0, c.?, 0);
                mf.release(c.?);
                return !mf.failed(hr);
            }
            mf.release(c.?);
        }
        // No enumerated candidate (some decoders only publish after the first frame): build one.
        const mt = api.mediaType(&api.FMT_NV12, d.in_w, d.in_h, d.fps_n, d.fps_d) orelse return false;
        const hr = d.dec.v.SetOutputType(@ptrCast(d.dec), 0, mt, 0);
        mf.release(mt);
        return !mf.failed(hr);
    }

    // buildInputPool allocates AUPOOL fixed-capacity sample/buffer pairs (see AUPOOL).
    fn buildInputPool(d: *Dec) OpenErr!void {
        var i: usize = 0;
        while (i < AUPOOL) : (i += 1) {
            var mb: ?*mf.IMFMediaBuffer = null;
            if (mf.failed(api.createMemoryBuffer(d.au_cap, &mb)) or mb == null) return error.DecOpenFailed;
            var smp: ?*mf.IMFSample = null;
            if (mf.failed(api.createSample(&smp)) or smp == null) {
                mf.release(mb.?);
                return error.DecOpenFailed;
            }
            _ = smp.?.v.AddBuffer(@ptrCast(smp.?), mb.?);
            d.in_buf[i] = mb.?;
            d.in_smp[i] = smp.?;
        }
    }

    /// feed submits one access unit. Bounded waits only. <0 = hard error (the session ends);
    /// 0 = accepted; 1 = dropped (oversized allocation failed).
    pub fn feed(d: *Dec, au: []const u8, pts100: i64) i32 {
        if (au.len == 0) return 1;
        var smp: *mf.IMFSample = undefined;
        var one_off: ?*mf.IMFSample = null;
        if (au.len <= d.au_cap) {
            const slot = d.in_idx;
            d.in_idx = (d.in_idx + 1) % AUPOOL;
            smp = d.in_smp[slot].?;
            if (!fillBuffer(d.in_buf[slot].?, au)) return 1;
        } else {
            // Oversized AU (rare): a one-off sample, released right after ProcessInput.
            var mb: ?*mf.IMFMediaBuffer = null;
            if (mf.failed(api.createMemoryBuffer(@intCast(au.len), &mb)) or mb == null) return 1;
            if (!fillBuffer(mb.?, au)) {
                mf.release(mb.?);
                return 1;
            }
            var s2: ?*mf.IMFSample = null;
            if (mf.failed(api.createSample(&s2)) or s2 == null) {
                mf.release(mb.?);
                return 1;
            }
            _ = s2.?.v.AddBuffer(@ptrCast(s2.?), mb.?);
            mf.release(mb.?);
            smp = s2.?;
            one_off = s2.?;
        }
        defer if (one_off) |s| mf.release(s);
        _ = smp.v.SetSampleTime(@ptrCast(smp), pts100);
        _ = smp.v.SetSampleDuration(@ptrCast(smp), d.dur100);

        if (d.evgen != null) {
            var waited: u32 = 0;
            while (true) {
                if (d.pump() < 0) return -5;
                if (d.need_input > 0) break;
                if (waited >= api.feed_wait_ms) return -6;
                api.sleep(1);
                waited += 1;
            }
            d.need_input -= 1;
            if (mf.failed(d.dec.v.ProcessInput(@ptrCast(d.dec), 0, smp, 0))) return -7;
            d.fed_n += 1;
            if (d.pump() < 0) return -5;
            return 0;
        }
        var waited: u32 = 0;
        while (true) {
            const hr = d.dec.v.ProcessInput(@ptrCast(d.dec), 0, smp, 0);
            if (hr != api.E_NOTACCEPTING) {
                if (mf.failed(hr)) return -7;
                break;
            }
            if (d.harvest() < 0) return -5;
            if (waited >= api.feed_wait_ms) return -6;
            api.sleep(1);
            waited += 1;
        }
        d.fed_n += 1;
        if (d.harvest() < 0) return -5;
        return 0;
    }

    /// pump drains pending decoder events without blocking. <0 = hard error.
    pub fn pump(d: *Dec) i32 {
        const gen = d.evgen orelse return d.harvest(); // sync MFT
        while (true) {
            var ev: ?*mf.IMFMediaEvent = null;
            const hr = gen.v.GetEvent(@ptrCast(gen), api.EV_NO_WAIT, &ev);
            if (hr == api.E_NO_EVENTS) return 0;
            if (mf.failed(hr) or ev == null) return -1;
            var met: u32 = 0;
            _ = ev.?.v.GetType(@ptrCast(ev.?), &met);
            mf.release(ev.?);
            switch (met) {
                api.EV_NEED_INPUT => d.need_input += 1,
                api.EV_HAVE_OUTPUT => {
                    if (d.harvest() < 0) return -1;
                },
                api.EV_DRAIN_COMPLETE => d.drain_done = true,
                else => {},
            }
        }
    }

    // harvest pulls decoded surfaces out and publishes each one. Async MFTs: ONE ProcessOutput per
    // HaveOutput event; sync MFTs loop until NEED_MORE_INPUT.
    fn harvest(d: *Dec) i32 {
        while (true) {
            var ob = api.OutputDataBuffer{};
            var status: u32 = 0;
            const hr = d.dec.v.ProcessOutput(@ptrCast(d.dec), 0, 1, &ob, &status);
            if (hr == api.E_STREAM_CHANGE) {
                if (!d.setNV12Output()) return -1;
                continue;
            }
            if (hr == api.E_NEED_MORE_INPUT) return 0;
            if (mf.failed(hr)) {
                api.traceStage("dec ProcessOutput hr=0x{x:0>8}", .{@as(u32, @bitCast(hr))});
                return -1;
            }
            const s = ob.pSample orelse return 0;
            if (ob.pEvents) |ev| mf.release(ev);
            const v = d.publish(s);
            mf.release(s);
            switch (v) {
                .ok => d.pub_n += 1,
                .timeout => d.mtx_timeouts += 1, // frame simply not published; never a spin
                .dead => return -2,
            }
            if (d.evgen != null) return 0;
        }
    }

    /// publish blits one decoded NV12 surface into the destination texture. The ONLY work done
    /// while holding the sender's mutex is one GPU-queued Blt.
    pub fn publish(d: *Dec, s: *mf.IMFSample) Publish {
        var buf: ?*mf.IMFMediaBuffer = null;
        // One buffer per decoded sample, so this returns THAT buffer (AddRef'd), no copy.
        if (mf.failed(s.v.ConvertToContiguousBuffer(@ptrCast(s), &buf)) or buf == null) {
            d.reason = .view_failed;
            return .dead;
        }
        defer mf.release(buf.?);
        var draw: ?*anyopaque = null;
        if (mf.failed(mf.qi(buf.?, &mf.IID_IMFDXGIBuffer, &draw)) or draw == null) {
            d.reason = .sw_decode_unsupported; // system-memory output: not this path's contract
            return .dead;
        }
        const dbuf: *mf.IMFDXGIBuffer = @ptrCast(@alignCast(draw.?));
        defer mf.release(dbuf);
        var traw: ?*anyopaque = null;
        if (mf.failed(dbuf.v.GetResource(@ptrCast(dbuf), &mf.IID_ID3D11Texture2D, &traw)) or traw == null) {
            d.reason = .view_failed;
            return .dead;
        }
        const tex = traw.?;
        defer mf.release(@as(*mf.IUnk, @ptrCast(@alignCast(tex))));
        var sub: u32 = 0;
        _ = dbuf.v.GetSubresourceIndex(@ptrCast(dbuf), &sub);

        const in_view = d.inputView(tex, sub) orelse {
            d.reason = .view_failed;
            return .dead;
        };
        switch (d.acquire()) {
            .ok => {},
            .timeout => return .timeout,
            .dead => {
                d.reason = .acquire_dead;
                return .dead;
            },
        }
        const stream = api.VPStream{
            .Enable = 1,
            .OutputIndex = 0,
            .InputFrameOrField = 0,
            .PastFrames = 0,
            .FutureFrames = 0,
            .pInputSurface = in_view,
        };
        const ok = !mf.failed(d.vctx.v.VideoProcessorBlt(@ptrCast(d.vctx), d.vproc, d.dst_view, 0, 1, &stream));
        d.release();
        if (!ok) {
            d.reason = .blt_failed;
            return .dead;
        }
        return .ok;
    }

    // inputView returns a cached VP input view for (tex, sub), creating + caching on a miss.
    fn inputView(d: *Dec, tex: *anyopaque, sub: u32) ?*anyopaque {
        for (d.views) |e| {
            if (e.tex == tex and e.sub == sub and e.view != null) return e.view;
        }
        const ivd = mf.VPIV_DESC{ .FourCC = 0, .ViewDimension = api.VPIV_TEXTURE2D, .MipSlice = 0, .ArraySlice = sub };
        var iv: ?*anyopaque = null;
        if (mf.failed(d.vdev.v.CreateVideoProcessorInputView(@ptrCast(d.vdev), tex, d.vpe, &ivd, &iv)) or iv == null) {
            return null;
        }
        const slot = d.view_idx;
        d.view_idx = (d.view_idx + 1) % VIEWCACHE;
        if (d.views[slot].view) |old| mf.release(@as(*mf.IUnk, @ptrCast(@alignCast(old))));
        d.views[slot] = .{ .tex = tex, .sub = sub, .view = iv.? };
        return iv.?;
    }

    fn acquire(d: *Dec) Publish {
        if (d.kmutex) |k| {
            const hr = k.v.AcquireSync(@ptrCast(k), 0, acquire_ms);
            if (hr == 0) return .ok;
            if (hr == 0x102) return .timeout; // WAIT_TIMEOUT rides back as the HRESULT value
            return .dead;
        }
        if (d.amutex) |m| {
            return switch (WaitForSingleObject(m, acquire_ms)) {
                WAIT_OBJECT_0, WAIT_ABANDONED => .ok, // abandoned = the owner died holding it
                else => .timeout,
            };
        }
        return .ok; // unsynchronised (counted via flag_unsynchronized)
    }

    fn release(d: *Dec) void {
        if (d.kmutex) |k| {
            _ = k.v.ReleaseSync(@ptrCast(k), 0);
            return;
        }
        if (d.amutex) |m| _ = ReleaseMutex(m);
    }

    /// drain flushes the decoder tail (bounded). 0 done, 1 timeout, <0 error.
    pub fn drain(d: *Dec) i32 {
        _ = d.dec.v.ProcessMessage(@ptrCast(d.dec), api.MSG_END_OF_STREAM, 0);
        _ = d.dec.v.ProcessMessage(@ptrCast(d.dec), api.MSG_DRAIN, 0);
        if (d.evgen == null) return d.harvest();
        var waited: u32 = 0;
        while (!d.drain_done and waited < api.feed_wait_ms) {
            const rc = d.pump();
            if (rc < 0) return rc;
            if (d.drain_done) break;
            api.sleep(1);
            waited += 1;
        }
        return if (d.drain_done) 0 else 1;
    }

    /// close releases everything, safe on every PARTIAL-open path (errdefer runs it).
    pub fn close(d: *Dec) void {
        for (0..AUPOOL) |i| {
            if (d.in_smp[i]) |s| mf.release(s);
            if (d.in_buf[i]) |b| mf.release(b);
            d.in_smp[i] = null;
            d.in_buf[i] = null;
        }
        for (0..VIEWCACHE) |i| {
            if (d.views[i].view) |v| mf.release(@as(*mf.IUnk, @ptrCast(@alignCast(v))));
            d.views[i] = .{};
        }
        if (d.evgen) |e| mf.release(e);
        d.evgen = null;
        if (d.has_dec) mf.release(d.dec);
        d.has_dec = false;
        if (d.kmutex) |k| mf.release(k);
        d.kmutex = null;
        if (d.amutex) |m| _ = CloseHandle(m);
        d.amutex = null;
        if (d.has_view) mf.release(@as(*mf.IUnk, @ptrCast(@alignCast(d.dst_view))));
        d.has_view = false;
        if (d.has_dst) mf.release(@as(*mf.IUnk, @ptrCast(@alignCast(d.dst_tex))));
        d.has_dst = false;
        if (d.has_vproc) mf.release(@as(*mf.IUnk, @ptrCast(@alignCast(d.vproc))));
        d.has_vproc = false;
        if (d.has_vpe) mf.release(@as(*mf.IUnk, @ptrCast(@alignCast(d.vpe))));
        d.has_vpe = false;
        if (d.has_vctx) mf.release(d.vctx);
        d.has_vctx = false;
        if (d.has_vdev) mf.release(d.vdev);
        d.has_vdev = false;
        if (d.has_devmgr) mf.release(d.devmgr);
        d.has_devmgr = false;
        if (d.has_dev) {
            mf.release(d.ctx);
            mf.release(d.dev);
        }
        d.has_dev = false;
        const gpa = d.gpa;
        gpa.destroy(d);
    }
};

// fillBuffer copies au into an MF memory buffer (bounded by its capacity).
fn fillBuffer(mb: *mf.IMFMediaBuffer, au: []const u8) bool {
    var p: ?[*]u8 = null;
    var maxlen: u32 = 0;
    if (mf.failed(mb.v.Lock(@ptrCast(mb), &p, &maxlen, null)) or p == null) return false;
    if (maxlen < au.len) {
        _ = mb.v.Unlock(@ptrCast(mb));
        return false;
    }
    @memcpy(p.?[0..au.len], au);
    _ = mb.v.Unlock(@ptrCast(mb));
    return !mf.failed(mb.v.SetCurrentLength(@ptrCast(mb), @intCast(au.len)));
}

/// openAccessMutex opens Spout's named access mutex for a sender - the SAME name cap.zig uses on
/// the capture side ("<sender>_SpoutAccessMutex", confirmed by execution in inc 1). A future SDK
/// rename lands on the unsynchronised path with flag_unsynchronized set and counted: visibly
/// wrong, never silently wrong.
fn openAccessMutex(gpa: std.mem.Allocator, sname: []const u8) ?*anyopaque {
    if (sname.len == 0 or sname.len > 256) return null;
    const name = std.fmt.allocPrint(gpa, "{s}_SpoutAccessMutex", .{sname}) catch return null;
    defer gpa.free(name);
    const w = gpa.allocSentinel(u16, name.len, 0) catch return null;
    defer gpa.free(w);
    for (name, 0..) |ch, i| w[i] = ch; // sender names are ASCII on this path
    return OpenMutexW(SYNCHRONIZE | MUTEX_MODIFY_STATE, 0, w.ptr);
}

test "vp output format allowlist refuses TYPELESS + planar" {
    try std.testing.expect(vpOutputFormatOK(87)); // B8G8R8A8_UNORM (what Go pins)
    try std.testing.expect(vpOutputFormatOK(28)); // R8G8B8A8_UNORM
    try std.testing.expect(!vpOutputFormatOK(86)); // B8G8R8A8_TYPELESS
    try std.testing.expect(!vpOutputFormatOK(103)); // NV12 is the VP INPUT here, never its output
    try std.testing.expect(!vpOutputFormatOK(0)); // UNKNOWN
}

test "reason strings match the wire contract" {
    try std.testing.expectEqualStrings("open_shared", Reason.open_shared.text());
    try std.testing.expectEqualStrings("dim_mismatch", Reason.dim_mismatch.text());
    try std.testing.expectEqualStrings("blt_failed", Reason.blt_failed.text());
    try std.testing.expectEqualStrings("sw_decode_unsupported", Reason.sw_decode_unsupported.text());
}

test "au pool capacity is ring-derived and clamped" {
    try std.testing.expectEqual(au_cap_min, Dec.auCapFor(4 * 1024)); // tiny ring → the floor
    try std.testing.expectEqual(@as(u32, 1024 * 1024), Dec.auCapFor(4 * 1024 * 1024)); // 4 MiB ring
    try std.testing.expectEqual(au_cap_max, Dec.auCapFor(64 * 1024 * 1024)); // ceiling holds
}
