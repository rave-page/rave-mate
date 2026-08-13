//! Native render surfaces inside the composition-hosted webview (SDL_WEBVIEW_SURFACE_DESIGN §4.2/4.3,
//! phase P2). The CHILD owns them end to end: the page declares `[data-surface]` elements, the
//! rect reporter (surfaces.js) sends the CURRENT set every frame it changes, and this registry
//! reconciles it against a DComp visual tree sitting UNDER the webview visual. Appearance = open,
//! disappearance = close. The daemon never learns a rect and never commands open/close - there is
//! deliberately no internal/webui/surface.go.
//!
//! Content is either a solid colour picked HERE (id hash, or `data-surface-color`) - P2, enough to
//! prove the hole, the z-order and the geometry - or, once a producer publishes a shared D3D11
//! texture under the surface's name, that producer's FRAMES (P3, surfsrc.zig). A surface flips
//! between the two on its own: the producer's kernel objects existing IS the bind.
//!
//! Bounded: cap 8 surfaces, drop-newest past it, every drop logged. One swapchain per surface, 2
//! buffers, recreated only when the integer size changes. The frame ring is the producer's and is
//! bounded there (surfsrc.max_slots frames = 2 * w*h*4 bytes; drop-oldest on this side).
//!
//! DComp + DXGI-swapchain decls are LOCAL on purpose: this module talks to the device + parent
//! visual through *anyopaque, so it shares no type with winshell.zig and neither file can drift the
//! other's vtable. Slots come from dcomp.h / dxgi1_2.h (Windows Kits 10.0.26100). The D3D11 half is
//! the SHARED module (native/zigd3d) that zigenc also uses - one binding, not two.
//!
//! GOTCHA (P0, by execution): MSVC REVERSES same-name virtual overload groups in the vtable, so
//! IDCompositionVisual::SetOffsetX(float) is slot 4 (not 3), SetOffsetY(float) 6, SetTransform 8,
//! SetClip(rect) 14. Only IDCompositionVisual has overload groups here; the DXGI/D3D interfaces
//! below have unique method names, so their declaration order holds.

const std = @import("std");
const d3d = @import("d3d11");
const wire = @import("wire.zig");
const surfsrc = @import("surfsrc.zig");

/// cap bounds the registry: 8 live surfaces, drop-NEWEST past that (§4.4). The reporter caps the
/// wire array at the same number, so a drop here means the page declared more than the reporter did.
pub const cap = 8;

const HR = i32;
const GUID = extern struct { d1: u32, d2: u16, d3: u16, d4: [8]u8 };
const VOP = *const fn () callconv(.winapi) HR; // opaque slot filler

extern "kernel32" fn LoadLibraryW([*:0]const u16) callconv(.winapi) ?*anyopaque;
extern "kernel32" fn GetProcAddress(?*anyopaque, [*:0]const u8) callconv(.winapi) ?*anyopaque;

const IUnknownVtbl = extern struct {
    QueryInterface: *const fn (*anyopaque, *const GUID, *?*anyopaque) callconv(.winapi) HR,
    AddRef: *const fn (*anyopaque) callconv(.winapi) u32,
    Release: *const fn (*anyopaque) callconv(.winapi) u32,
};
const IUnknownObj = extern struct { v: *const IUnknownVtbl };

fn comRelease(obj: ?*anyopaque) void {
    const o = obj orelse return;
    const u: *IUnknownObj = @ptrCast(@alignCast(o));
    _ = u.v.Release(o);
}

// IDCompositionDevice : IUnknown — 3 Commit | 4 WaitForCommitCompletion | 5 GetFrameStatistics
//   6 CreateTargetForHwnd | 7 CreateVisual
const Dev = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        Commit: *const fn (*anyopaque) callconv(.winapi) HR,
        _p4: [3]VOP,
        CreateVisual: *const fn (*anyopaque, *?*anyopaque) callconv(.winapi) HR,
    },
};

// IDCompositionVisual : IUnknown — see the overload-group gotcha at the top of this file.
const Vis = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _offx_anim: VOP, // 3  SetOffsetX(IDCompositionAnimation*)
        SetOffsetX: *const fn (*anyopaque, f32) callconv(.winapi) HR, // 4
        _offy_anim: VOP, // 5  SetOffsetY(IDCompositionAnimation*)
        SetOffsetY: *const fn (*anyopaque, f32) callconv(.winapi) HR, // 6
        _p7: [8]VOP, // 7..14 SetTransform(2) SetTransformParent SetEffect
        //                    SetBitmapInterpolationMode SetBorderMode SetClip(2)
        SetContent: *const fn (*anyopaque, ?*anyopaque) callconv(.winapi) HR, // 15
        AddVisual: *const fn (*anyopaque, *anyopaque, i32, ?*anyopaque) callconv(.winapi) HR, // 16
        _remove_visual: VOP, // 17
        RemoveAllVisuals: *const fn (*anyopaque) callconv(.winapi) HR, // 18
    },
};

// IDXGIFactory2 : IDXGIFactory1 : IDXGIFactory : IDXGIObject : IUnknown (dxgi1_2.h)
//   3..6 IDXGIObject | 7..11 IDXGIFactory | 12,13 IDXGIFactory1 | 14..24 IDXGIFactory2,
//   CreateSwapChainForComposition LAST at 24.
const Factory = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _p3: [21]VOP, // 3..23
        CreateSwapChainForComposition: *const fn (*anyopaque, *anyopaque, *const SwapDesc, ?*anyopaque, *?*anyopaque) callconv(.winapi) HR,
    },
};

// IDXGISwapChain1 : IDXGISwapChain : IDXGIDeviceSubObject : IDXGIObject : IUnknown
//   3..6 IDXGIObject | 7 GetDevice | 8 Present | 9 GetBuffer | 10,11 (Get/Set)FullscreenState
//   12 GetDesc | 13 ResizeBuffers
const Swap = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _p3: [5]VOP, // 3..7
        Present: *const fn (*anyopaque, u32, u32) callconv(.winapi) HR, // 8
        GetBuffer: *const fn (*anyopaque, u32, *const GUID, *?*anyopaque) callconv(.winapi) HR, // 9
        _p10: [3]VOP, // 10..12
        ResizeBuffers: *const fn (*anyopaque, u32, u32, u32, u32, u32) callconv(.winapi) HR, // 13
    },
};

// ID3D11Device / ID3D11DeviceContext / ID3D11Texture2D come from the SHARED module (`d3d`).
/// DXGI_SWAP_CHAIN_DESC1 (48 bytes, all 32-bit fields incl. the inlined DXGI_SAMPLE_DESC).
const SwapDesc = extern struct {
    Width: u32,
    Height: u32,
    Format: u32,
    Stereo: i32,
    SampleCount: u32,
    SampleQuality: u32,
    BufferUsage: u32,
    BufferCount: u32,
    Scaling: u32,
    SwapEffect: u32,
    AlphaMode: u32,
    Flags: u32,
};

const fmt_b8g8r8a8: u32 = 87; // DXGI_FORMAT_B8G8R8A8_UNORM
const usage_rtv: u32 = 0x20; // DXGI_USAGE_RENDER_TARGET_OUTPUT
const scaling_stretch: u32 = 0; // composition swapchains require STRETCH
const swap_flip_sequential: u32 = 3; // DXGI_SWAP_EFFECT_FLIP_SEQUENTIAL
/// DXGI_ALPHA_MODE: UNSPECIFIED 0, PREMULTIPLIED 1, STRAIGHT 2, IGNORE 3. A composition swapchain
/// takes only the first, second or fourth - STRAIGHT is E_INVALIDARG, which is exactly what an
/// off-by-one here bought on the first run.
const alpha_premultiplied: u32 = 1;

/// errHR logs a failed COM call WITH its HRESULT. A bare "failed" line costs an extra build+deploy
/// cycle every time (it did, once).
fn errHR(what: []const u8, hr: HR) void {
    var buf: [160]u8 = undefined;
    const m = std.fmt.bufPrint(&buf, "rave-shell: {s} hr=0x{X:0>8}", .{ what, @as(u32, @bitCast(hr)) }) catch return;
    wire.errLine(m);
}

const iid_factory2: GUID = .{ .d1 = 0x50C83A1C, .d2 = 0xE072, .d3 = 0x4C48, .d4 = .{ 0x87, 0xB0, 0x36, 0x30, 0xFA, 0x36, 0xA6, 0xD0 } };
const iid_texture2d: GUID = .{ .d1 = 0x6F15AAF2, .d2 = 0xD208, .d3 = 0x4E89, .d4 = .{ 0x9A, 0xB4, 0x48, 0x95, 0x35, 0xD3, 0x4F, 0x9C } };

const CreateFactory1Fn = *const fn (*const GUID, *?*anyopaque) callconv(.winapi) HR;
const D3D11CreateDeviceFn = *const fn (?*anyopaque, u32, ?*anyopaque, u32, ?[*]const u32, u32, u32, *?*anyopaque, ?*u32, *?*anyopaque) callconv(.winapi) HR;

/// Report is one `[data-surface]` element as the page measured it: rect in DEVICE px (the reporter
/// already multiplied by devicePixelRatio and intersected with every scroll clip), vis from the
/// IntersectionObserver + a live layout check.
pub const Report = struct {
    id: []const u8,
    x: f32,
    y: f32,
    w: f32,
    h: f32,
    /// fx/fy/fw/fh = the element's FULL rect, unclipped. x/y/w/h above are what survived every
    /// scrolling ancestor. P2 only had the clipped one and sized content to it, which squashes a
    /// real picture; a producer frame is pinned to the FULL rect and CROPPED to the visible one
    /// (surfsrc.planBlit).
    fx: f32,
    fy: f32,
    fw: f32,
    fh: f32,
    vis: bool,
    dpr: f32,
    /// color is 0xRRGGBB from `data-surface-color`, or null = derive one from the id hash.
    color: ?u32,
};

const Surface = struct {
    id: []u8,
    visual: *anyopaque,
    swap: ?*anyopaque = null,
    w: u32 = 0,
    h: u32 = 0,
    x: f32 = std.math.nan(f32),
    y: f32 = std.math.nan(f32),
    /// full rect (client device px), unclipped - what a producer renders to and what a frame is
    /// positioned against.
    fx: i64 = 0,
    fy: i64 = 0,
    fw: i64 = 0,
    fh: i64 = 0,
    shown: bool = false,
    color: u32,
    seen: bool = false,
    /// src is the producer attachment. Zero value = no producer; the surface stays a solid colour.
    src: surfsrc.Source = .{},
};

pub const Registry = struct {
    gpa: std.mem.Allocator,
    dev: *anyopaque, // IDCompositionDevice (owned by winshell)
    layer: *anyopaque, // the surface-layer IDCompositionVisual, z BELOW the webview visual
    items: [cap]?Surface = [_]?Surface{null} ** cap,
    /// D3D is created LAZILY, on the first surface: a shell hosting zero surfaces has no reason to
    /// hold a device (and R7's GPU-fault blast radius shrinks with it).
    d3d: ?*anyopaque = null,
    ctx: ?*anyopaque = null,
    factory: ?*anyopaque = null,
    /// dev1 = the same device QI'd to ID3D11Device1, which is where OpenSharedResourceByName lives.
    dev1: ?*anyopaque = null,
    gpu_dead: bool = false, // one bring-up failure is enough; don't retry every frame
    dropped_logged: bool = false,

    pub fn init(gpa: std.mem.Allocator, dev: *anyopaque, layer: *anyopaque) Registry {
        return .{ .gpa = gpa, .dev = dev, .layer = layer };
    }

    /// apply reconciles the reported set with the live one: new ids open, missing ids close,
    /// everything else is repositioned. One Commit for the whole batch - the page's frame and the
    /// compositor's stay in step (R4).
    pub fn apply(r: *Registry, reports: []const Report, dropped_by_page: u32) void {
        for (&r.items) |*slot| {
            if (slot.*) |*s| s.seen = false;
        }
        var order_changed = false;
        for (reports) |rep| {
            if (rep.id.len == 0) continue;
            if (r.find(rep.id)) |s| {
                s.seen = true;
                r.place(s, rep);
                continue;
            }
            if (!rep.vis) continue; // never open a surface for an already-hidden element
            if (r.open(rep)) |s| {
                s.seen = true;
                order_changed = true;
                r.place(s, rep);
            }
        }
        for (&r.items) |*slot| {
            if (slot.*) |*s| {
                if (s.seen) continue;
                r.close(s);
                slot.* = null;
                order_changed = true;
            }
        }
        // Z-order = DOM order. Rebuilding the child list only when the SET changed keeps the
        // per-frame path to offsets + one Commit; AddVisual(insertAbove=FALSE, ref=NULL) puts each
        // new child on top, so replaying the reports in document order lands DOM order.
        if (order_changed) r.restack(reports);
        r.commit();
        if (dropped_by_page > 0 and !r.dropped_logged) {
            r.dropped_logged = true;
            var buf: [128]u8 = undefined;
            const m = std.fmt.bufPrint(&buf, "rave-shell: surface cap {d} reached - page dropped {d} newer [data-surface] element(s)", .{ cap, dropped_by_page }) catch return;
            wire.errLine(m);
        }
    }

    fn find(r: *Registry, id: []const u8) ?*Surface {
        for (&r.items) |*slot| {
            if (slot.*) |*s| {
                if (std.mem.eql(u8, s.id, id)) return s;
            }
        }
        return null;
    }

    fn open(r: *Registry, rep: Report) ?*Surface {
        var free: ?*?Surface = null;
        for (&r.items) |*slot| {
            if (slot.* == null) {
                free = slot;
                break;
            }
        }
        const slot = free orelse {
            // Drop-NEWEST: the live 8 keep their visuals, the 9th never opens. Loud, once.
            if (!r.dropped_logged) {
                r.dropped_logged = true;
                wire.errLine("rave-shell: surface registry full (cap 8) - dropping newest [data-surface]");
            }
            return null;
        };
        const dev: *Dev = @ptrCast(@alignCast(r.dev));
        var v: ?*anyopaque = null;
        const hrv = dev.v.CreateVisual(r.dev, &v);
        if (hrv < 0 or v == null) {
            errHR("surface CreateVisual failed", hrv);
            return null;
        }
        const id = r.gpa.dupe(u8, rep.id) catch {
            comRelease(v);
            return null;
        };
        const parent: *Vis = @ptrCast(@alignCast(r.layer));
        if (parent.v.AddVisual(r.layer, v.?, 0, null) < 0) {
            wire.errLine("rave-shell: surface AddVisual failed");
            r.gpa.free(id);
            comRelease(v);
            return null;
        }
        slot.* = .{ .id = id, .visual = v.?, .color = rep.color orelse hashColor(rep.id) };
        return &slot.*.?;
    }

    /// close drops OUR references. The visual is still a child of the layer here and DComp holds
    /// its own reference, so nothing dangles: the follow-up restack's RemoveAllVisuals is what
    /// actually destroys it. Releasing first and detaching after is the only order that cannot
    /// leave a ghost visual painting after the element is gone.
    fn close(r: *Registry, s: *Surface) void {
        surfsrc.close(&s.src); // producer objects first: they outlive the visual otherwise
        const v: *Vis = @ptrCast(@alignCast(s.visual));
        _ = v.v.SetContent(s.visual, null);
        comRelease(s.swap);
        comRelease(s.visual);
        r.gpa.free(s.id);
    }

    /// restack replays the whole child list so z follows DOM order. Cheap and only on set changes.
    fn restack(r: *Registry, reports: []const Report) void {
        const parent: *Vis = @ptrCast(@alignCast(r.layer));
        _ = parent.v.RemoveAllVisuals(r.layer);
        for (reports) |rep| {
            const s = r.find(rep.id) orelse continue;
            _ = parent.v.AddVisual(r.layer, s.visual, 0, null); // FALSE + NULL ref = on top of siblings
        }
    }

    fn place(r: *Registry, s: *Surface, rep: Report) void {
        const v: *Vis = @ptrCast(@alignCast(s.visual));
        s.fx = @intFromFloat(@round(rep.fx));
        s.fy = @intFromFloat(@round(rep.fy));
        s.fw = @intFromFloat(@max(0, @round(rep.fw)));
        s.fh = @intFromFloat(@max(0, @round(rep.fh)));
        const want_w: u32 = @intFromFloat(@max(0, @round(rep.w)));
        const want_h: u32 = @intFromFloat(@max(0, @round(rep.h)));
        const show = rep.vis and want_w > 0 and want_h > 0;
        if (!show) {
            if (s.shown) {
                _ = v.v.SetContent(s.visual, null); // §4.6: hidden = no content, visual survives
                s.shown = false;
            }
            return;
        }
        if (want_w != s.w or want_h != s.h) {
            r.resize(s, want_w, want_h);
        }
        if (s.swap == null) return;
        // Offsets are the DComp target's space = the window's CLIENT space, which is also where
        // put_Bounds put the page - so a device-px page rect maps straight through.
        const nx = @round(rep.x);
        const ny = @round(rep.y);
        if (nx != s.x or ny != s.y) {
            _ = v.v.SetOffsetX(s.visual, nx);
            _ = v.v.SetOffsetY(s.visual, ny);
            s.x = nx;
            s.y = ny;
        }
        if (!s.shown) {
            _ = v.v.SetContent(s.visual, s.swap);
            s.shown = true;
        }
    }

    /// resize (re)builds the swapchain at the surface's exact size and repaints it. The reported
    /// rect is already intersected with every scroll clip, so sizing the CONTENT to it avoids
    /// IDCompositionVisual::SetClip entirely - whose rect coordinate space is the one thing dcomp.h
    /// does not state unambiguously. P3 (real frames) will need the clip instead: a frame producer
    /// cannot have its picture squashed by a partially scrolled-out element.
    fn resize(r: *Registry, s: *Surface, w: u32, h: u32) void {
        if (s.swap == null) {
            s.swap = r.newSwapchain(w, h) orelse return;
        } else {
            const sc: *Swap = @ptrCast(@alignCast(s.swap.?));
            const hr = sc.v.ResizeBuffers(s.swap.?, 0, w, h, 0, 0);
            if (hr < 0) {
                errHR("surface ResizeBuffers failed", hr);
                return;
            }
        }
        s.w = w;
        s.h = h;
        r.paint(s);
        // A resized swapchain is a NEW back-buffer chain; rebind so DComp picks it up.
        const v: *Vis = @ptrCast(@alignCast(s.visual));
        _ = v.v.SetContent(s.visual, s.swap);
        s.shown = true;
    }

    /// paint fills the whole back buffer with the surface's solid colour and presents it. P2's
    /// entire "producer": no frames, no shared textures (P3).
    fn paint(r: *Registry, s: *Surface) void {
        const sc: *Swap = @ptrCast(@alignCast(s.swap.?));
        var tex: ?*anyopaque = null;
        const hrb = sc.v.GetBuffer(s.swap.?, 0, &iid_texture2d, &tex);
        if (hrb < 0 or tex == null) {
            errHR("surface GetBuffer failed", hrb);
            return;
        }
        defer comRelease(tex);
        r.clearTo(tex.?, s.color);
        _ = sc.v.Present(s.swap.?, 0, 0); // flushes the immediate context
    }

    /// clearTo fills one texture with a solid colour: a producerless surface's whole content, and
    /// the letterbox behind a picture that does not fill its element.
    fn clearTo(r: *Registry, tex: *anyopaque, color: u32) void {
        const dev: *d3d.ID3D11Device = @ptrCast(@alignCast(r.d3d.?));
        var rtv: ?*anyopaque = null;
        const hrv = dev.v.CreateRenderTargetView(r.d3d.?, tex, null, &rtv);
        if (hrv < 0 or rtv == null) {
            errHR("surface CreateRenderTargetView failed", hrv);
            return;
        }
        defer comRelease(rtv);
        const c: [4]f32 = .{
            @as(f32, @floatFromInt((color >> 16) & 0xff)) / 255.0,
            @as(f32, @floatFromInt((color >> 8) & 0xff)) / 255.0,
            @as(f32, @floatFromInt(color & 0xff)) / 255.0,
            1.0,
        };
        const ctx: *d3d.ID3D11DeviceContext = @ptrCast(@alignCast(r.ctx.?));
        ctx.v.ClearRenderTargetView(r.ctx.?, rtv.?, &c);
    }

    // -- P3: producer frames ---------------------------------------------------------------------
    // The pump runs off a window timer, NOT off the page's rect reports: surfaces.js drops identical
    // consecutive reports, so a still page emits nothing and a report-driven present would show
    // exactly one frame for ever. That is the failure this repo already paid for once (#58: "fps
    // 58.5" over one bit-identical picture) - the present cadence has to be the producer's, not the
    // DOM's.

    /// pump probes for producers, tells them the size to render, and presents at most one new frame
    /// per surface per tick. Returns the number of live surfaces (0 = the caller may stop ticking).
    pub fn pump(r: *Registry) u32 {
        var live: u32 = 0;
        for (&r.items) |*slot| {
            if (slot.* == null) continue;
            const s = &slot.*.?;
            live += 1;
            surfsrc.probe(&s.src, r.gpa, s.id);
            if (s.src.view == null) continue;
            surfsrc.wants(&s.src, @intCast(@max(0, s.fw)), @intCast(@max(0, s.fh)));
            // x/y start as NaN and only become real in place(); @intFromFloat(NaN) is undefined, so
            // a surface that has a swapchain but has never been positioned is skipped, not guessed.
            if (!s.shown or s.swap == null or r.dev1 == null) continue;
            if (!std.math.isFinite(s.x) or !std.math.isFinite(s.y)) continue;
            const f = surfsrc.begin(&s.src, r.gpa, s.id, r.dev1) orelse continue;
            surfsrc.end(&s.src, f, r.blit(s, f));
        }
        return live;
    }

    /// blit copies the producer's frame into the surface's back buffer and presents it. 1:1 pixels -
    /// there is no scaler in this path on purpose: the producer renders at the full rect it was
    /// asked for, and anything scrolled out is simply not copied.
    fn blit(r: *Registry, s: *Surface, f: surfsrc.Frame) bool {
        const plan = surfsrc.planBlit(f.w, f.h, s.fx, s.fy, s.fw, s.fh, @intFromFloat(s.x), @intFromFloat(s.y), s.w, s.h) orelse return false;
        const sc: *Swap = @ptrCast(@alignCast(s.swap.?));
        var back: ?*anyopaque = null;
        const hrb = sc.v.GetBuffer(s.swap.?, 0, &iid_texture2d, &back);
        if (hrb < 0 or back == null) {
            errHR("surface GetBuffer failed", hrb);
            return false;
        }
        defer comRelease(back);
        // FLIP_SEQUENTIAL hands back an UNDEFINED buffer, so anything the picture does not cover has
        // to be repainted every frame, not once.
        if (!plan.covers) r.clearTo(back.?, s.color);
        const box: d3d.BOX = .{ .left = plan.sx, .top = plan.sy, .front = 0, .right = plan.sx + plan.sw, .bottom = plan.sy + plan.sh, .back = 1 };
        const ctx: *d3d.ID3D11DeviceContext = @ptrCast(@alignCast(r.ctx.?));
        ctx.v.CopySubresourceRegion(r.ctx.?, back.?, 0, plan.dx, plan.dy, 0, f.tex, 0, &box);
        _ = sc.v.Present(s.swap.?, 0, 0);
        return true;
    }

    /// producerRect reports the visible rect of the first surface actually presenting a producer's
    /// frames, in CLIENT device px. It exists so `ctl surface-test stats` can screenshot exactly the
    /// picture and decode the testcard out of it - the rect never crosses to the daemon, only the
    /// cropped PNG does (directive #2 holds: Go still learns no geometry).
    pub fn producerRect(r: *Registry) ?struct { x: i32, y: i32, w: i32, h: i32 } {
        for (&r.items) |*slot| {
            if (slot.* == null) continue;
            const s = &slot.*.?;
            if (!s.src.attached() or !s.shown or s.w == 0 or s.h == 0) continue;
            if (!std.math.isFinite(s.x) or !std.math.isFinite(s.y)) continue;
            return .{ .x = @intFromFloat(s.x), .y = @intFromFloat(s.y), .w = @intCast(s.w), .h = @intCast(s.h) };
        }
        return null;
    }

    /// logSources prints one line per attached producer. The child has no request/response lane of
    /// its own and does not need one: these land in the daemon log like every other child line.
    pub fn logSources(r: *Registry) void {
        for (&r.items) |*slot| {
            if (slot.* == null) continue;
            const s = &slot.*.?;
            if (s.src.view == null) continue;
            const st = s.src.stats();
            var buf: [256]u8 = undefined;
            const m = std.fmt.bufPrint(&buf, "rave-shell: surface {s} src gen {d} {d}x{d} presented {d} dropped {d} lastSeq {d} lastPtsMs {d} staleMs {d} visible {d}x{d}", .{
                s.id, st.gen, st.w, st.h, st.presented, st.dropped, st.last_seq, @divTrunc(st.last_pts_ns, 1_000_000), st.stale_ms, s.w, s.h,
            }) catch continue;
            wire.errLine(m);
        }
    }

    fn newSwapchain(r: *Registry, w: u32, h: u32) ?*anyopaque {
        if (!r.ensureGPU()) return null;
        const desc: SwapDesc = .{
            .Width = w,
            .Height = h,
            .Format = fmt_b8g8r8a8,
            .Stereo = 0,
            .SampleCount = 1,
            .SampleQuality = 0,
            .BufferUsage = usage_rtv,
            .BufferCount = 2,
            .Scaling = scaling_stretch,
            .SwapEffect = swap_flip_sequential,
            .AlphaMode = alpha_premultiplied,
            .Flags = 0,
        };
        const f: *Factory = @ptrCast(@alignCast(r.factory.?));
        var sc: ?*anyopaque = null;
        const hr = f.v.CreateSwapChainForComposition(r.factory.?, r.d3d.?, &desc, null, &sc);
        if (hr < 0 or sc == null) {
            errHR("CreateSwapChainForComposition failed - surface stays empty", hr);
            return null;
        }
        return sc;
    }

    /// ensureGPU brings up the D3D11 device + DXGI factory the swapchains need. A failure is LOUD
    /// and permanent for this session, but NOT fatal: a surface is page-declared content, and a
    /// page must never be able to kill the window process (visualFatal covers the shell's own
    /// composition contract, which is a different promise).
    fn ensureGPU(r: *Registry) bool {
        if (r.d3d != null) return true;
        if (r.gpu_dead) return false;
        r.gpu_dead = true; // cleared on success
        const d3dlib = LoadLibraryW(std.unicode.utf8ToUtf16LeStringLiteral("d3d11.dll")) orelse {
            wire.errLine("rave-shell: surfaces need d3d11.dll - not loadable");
            return false;
        };
        const d3dproc = GetProcAddress(d3dlib, "D3D11CreateDevice") orelse return false;
        const create: D3D11CreateDeviceFn = @ptrCast(@alignCast(d3dproc));
        var dev: ?*anyopaque = null;
        var ctx: ?*anyopaque = null;
        // 1 = HARDWARE, 5 = WARP; 0x20 = BGRA_SUPPORT (required for composition surfaces); 7 = SDK ver
        if (create(null, 1, null, 0x20, null, 0, 7, &dev, null, &ctx) < 0 or dev == null) {
            if (create(null, 5, null, 0x20, null, 0, 7, &dev, null, &ctx) < 0 or dev == null) {
                wire.errLine("rave-shell: D3D11CreateDevice failed (hardware and WARP) - no surfaces");
                return false;
            }
        }
        const dxgilib = LoadLibraryW(std.unicode.utf8ToUtf16LeStringLiteral("dxgi.dll")) orelse return false;
        const fproc = GetProcAddress(dxgilib, "CreateDXGIFactory1") orelse return false;
        const cf: CreateFactory1Fn = @ptrCast(@alignCast(fproc));
        var fac: ?*anyopaque = null;
        if (cf(&iid_factory2, &fac) < 0 or fac == null) {
            wire.errLine("rave-shell: CreateDXGIFactory1(IDXGIFactory2) failed - no surfaces");
            comRelease(ctx);
            comRelease(dev);
            return false;
        }
        // ID3D11Device1 carries OpenSharedResourceByName, the named-shared-resource half. Absent =
        // no producer ingest; solid-colour surfaces still work, so this is a WARN, not a failure.
        var dev1: ?*anyopaque = null;
        if (d3d.failed(d3d.qi(dev.?, &d3d.IID_ID3D11Device1, &dev1))) dev1 = null;
        if (dev1 == null) wire.errLine("rave-shell: no ID3D11Device1 - surfaces cannot ingest producer frames");
        r.d3d = dev;
        r.ctx = ctx;
        r.factory = fac;
        r.dev1 = dev1;
        r.gpu_dead = false;
        wire.errLine("rave-shell: surface presenter ready (D3D11 + composition swapchains)");
        return true;
    }

    fn commit(r: *Registry) void {
        const dev: *Dev = @ptrCast(@alignCast(r.dev));
        _ = dev.v.Commit(r.dev);
    }

    /// deinit tears every surface down (§4.6 quit row). Safe on a registry that never opened one.
    pub fn deinit(r: *Registry) void {
        for (&r.items) |*slot| {
            if (slot.*) |*s| {
                surfsrc.close(&s.src);
                const v: *Vis = @ptrCast(@alignCast(s.visual));
                _ = v.v.SetContent(s.visual, null);
                comRelease(s.swap);
                comRelease(s.visual);
                r.gpa.free(s.id);
                slot.* = null;
            }
        }
        const parent: *Vis = @ptrCast(@alignCast(r.layer));
        _ = parent.v.RemoveAllVisuals(r.layer);
        comRelease(r.dev1);
        comRelease(r.factory);
        comRelease(r.ctx);
        comRelease(r.d3d);
        r.dev1 = null;
        r.factory = null;
        r.ctx = null;
        r.d3d = null;
    }
};

/// hashColor derives a vivid, stable colour from the surface id (FNV-1a → hue, full saturation).
/// A test surface is therefore identifiable on sight without the daemon naming a colour.
fn hashColor(id: []const u8) u32 {
    var h: u32 = 2166136261;
    for (id) |b| {
        h ^= b;
        h *%= 16777619;
    }
    return hsvToRGB(@as(f32, @floatFromInt(h % 360)), 0.85, 1.0);
}

fn hsvToRGB(hdeg: f32, s: f32, v: f32) u32 {
    const c = v * s;
    const hp = hdeg / 60.0;
    const x = c * (1.0 - @abs(@mod(hp, 2.0) - 1.0));
    var rf: f32 = 0;
    var gf: f32 = 0;
    var bf: f32 = 0;
    const seg: u32 = @intFromFloat(hp);
    switch (seg) {
        0 => {
            rf = c;
            gf = x;
        },
        1 => {
            rf = x;
            gf = c;
        },
        2 => {
            gf = c;
            bf = x;
        },
        3 => {
            gf = x;
            bf = c;
        },
        4 => {
            rf = x;
            bf = c;
        },
        else => {
            rf = c;
            bf = x;
        },
    }
    const m = v - c;
    const r8: u32 = @intFromFloat(@min(255.0, (rf + m) * 255.0));
    const g8: u32 = @intFromFloat(@min(255.0, (gf + m) * 255.0));
    const b8: u32 = @intFromFloat(@min(255.0, (bf + m) * 255.0));
    return (r8 << 16) | (g8 << 8) | b8;
}

test "hashColor is stable and non-black" {
    const a = hashColor("editor-preview");
    try std.testing.expectEqual(a, hashColor("editor-preview"));
    try std.testing.expect(a != 0);
    try std.testing.expect(hashColor("a") != hashColor("b"));
}
