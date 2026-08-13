//! d3d11.zig - the repo's ONE set of D3D11/DXGI COM declarations (extern vtables, no cgo).
//!
//! Lifted verbatim out of native/zigenc/src/mf.zig, which grew them for the Media Foundation
//! encode/decode path, and now shared with native/zigui's shell child (native render surfaces,
//! SDL_WEBVIEW_SURFACE_DESIGN P3). A second hand-written binding is exactly what §3.3 of that
//! design forbids: two copies of a vtable slot count drift, and a drifted slot is a silent call
//! into the wrong method.
//!
//! Scope: TYPES, CONSTANTS, IIDs and pure helpers only - deliberately NO `extern "d3d11"` flat
//! imports. zigenc links d3d11/dxgi at build time; the shell child resolves the same entry points
//! through LoadLibrary/GetProcAddress so a machine without a usable D3D11 stack still opens its
//! window. Putting the externs here would force the shell exe to import d3d11.dll at load.
//!
//! Vtable slot counts are hand-derived from the SDK IDL order - every pad names the methods it
//! skips so the counting is reviewable. Interfaces here have unique method names (no MSVC
//! overload-group reversal, unlike IDCompositionVisual - see surfaces.zig), so declaration order
//! holds.

const std = @import("std");

pub const HRESULT = i32;
/// VOP is one opaque vtable slot. *const anyopaque, not a fn pointer: it is never called.
pub const VOP = *const anyopaque;

pub fn failed(hr: HRESULT) bool {
    return hr < 0;
}

pub const GUID = extern struct { d1: u32, d2: u16, d3: u16, d4: [8]u8 };

pub fn g(d1: u32, d2: u16, d3: u16, d4: [8]u8) GUID {
    return .{ .d1 = d1, .d2 = d2, .d3 = d3, .d4 = d4 };
}

pub fn guidEq(a: GUID, b: GUID) bool {
    return a.d1 == b.d1 and a.d2 == b.d2 and a.d3 == b.d3 and std.mem.eql(u8, &a.d4, &b.d4);
}

// ── IIDs ──
pub const IID_ID3D11Texture2D = g(0x6f15aaf2, 0xd208, 0x4e89, .{ 0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c });
/// IDXGIKeyedMutex: preferred cross-process sync on a shared texture when the sender exposes it.
pub const IID_IDXGIKeyedMutex = g(0x9d8e1289, 0xd7b3, 0x465f, .{ 0x81, 0x26, 0x25, 0x0e, 0x34, 0x9a, 0xf8, 0x5d });
pub const IID_ID3D10Multithread = g(0x9b7e4e00, 0x342c, 0x4106, .{ 0xa1, 0x9f, 0x4f, 0x27, 0x04, 0xf6, 0x89, 0xf0 });
pub const IID_ID3D11VideoDevice = g(0x10ec4d5b, 0x975a, 0x4689, .{ 0xb9, 0xe4, 0xd0, 0xaa, 0xc3, 0x0f, 0xe3, 0x33 });
pub const IID_ID3D11VideoContext = g(0x61f21c45, 0x3c0e, 0x4a74, .{ 0x9c, 0xea, 0x67, 0x10, 0x0d, 0x9a, 0xd5, 0xe4 });
pub const IID_IDXGIFactory1 = g(0x770aae78, 0xf26f, 0x4dba, .{ 0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87 });
pub const IID_IDXGIFactory2 = g(0x50c83a1c, 0xe072, 0x4c48, .{ 0x87, 0xb0, 0x36, 0x30, 0xfa, 0x36, 0xa6, 0xd0 });
/// ID3D11Device1 carries OpenSharedResourceByName - the NAMED half of the P3 handshake, which is
/// what lets a producer publish a surface without the daemon couriering a raw HANDLE.
pub const IID_ID3D11Device1 = g(0xa04bfb29, 0x08ef, 0x43d6, .{ 0xa4, 0x9c, 0xa9, 0xbd, 0xbd, 0xcb, 0xe6, 0x86 });
/// IDXGIResource1::CreateSharedHandle is the producer half: an NT handle WITH a name.
pub const IID_IDXGIResource1 = g(0x30961379, 0x4609, 0x4a41, .{ 0x99, 0x8e, 0x54, 0xfe, 0x56, 0x7e, 0xe0, 0xc1 });

// ── enum / flag constants ──
pub const E_FAIL: HRESULT = @bitCast(@as(u32, 0x80004005));
/// WAIT_TIMEOUT as an HRESULT-shaped success code: IDXGIKeyedMutex::AcquireSync returns it when the
/// other side still holds the key. NOT a failure - the caller drops this frame and moves on.
pub const WAIT_TIMEOUT: HRESULT = 0x00000102;
/// WAIT_ABANDONED: the holder died without releasing. AcquireSync SUCCEEDS and we own the mutex;
/// treating it as an error would wedge the transport permanently after one producer crash.
pub const WAIT_ABANDONED: HRESULT = 0x00000080;

pub const D3D_DRIVER_TYPE_UNKNOWN: u32 = 0;
pub const D3D_DRIVER_TYPE_HARDWARE: u32 = 1;
/// WARP: software rasterizer WITH a video processor - the sw tier's CSC+scale still runs when no
/// hardware adapter can host a video device at all.
pub const D3D_DRIVER_TYPE_WARP: u32 = 5;
pub const D3D11_CREATE_DEVICE_BGRA_SUPPORT: u32 = 0x20;
pub const D3D11_CREATE_DEVICE_VIDEO_SUPPORT: u32 = 0x800; // required for video MFTs (audit fix)
pub const D3D11_SDK_VERSION: u32 = 7;

pub const DXGI_FORMAT_R8G8B8A8_UNORM: u32 = 28;
pub const DXGI_FORMAT_B8G8R8A8_UNORM: u32 = 87;
pub const DXGI_FORMAT_NV12: u32 = 103;

pub const bind_shader_resource: u32 = 8;
pub const bind_render_target: u32 = 0x20;
pub const D3D11_BIND_SHADER_RESOURCE: u32 = bind_shader_resource;
pub const D3D11_BIND_RENDER_TARGET: u32 = bind_render_target;

pub const USAGE_DEFAULT: u32 = 0;
pub const USAGE_DYNAMIC: u32 = 2;
pub const USAGE_STAGING: u32 = 3;
pub const CPU_ACCESS_WRITE: u32 = 0x10000;
pub const CPU_ACCESS_READ: u32 = 0x20000;
pub const MAP_READ: u32 = 1;
pub const MAP_WRITE_DISCARD: u32 = 4;

/// D3D11_RESOURCE_MISC_*. SHARED_NTHANDLE is what upgrades a share to a NAMEABLE kernel object, and
/// it is only legal together with SHARED_KEYEDMUTEX - which is also the sync we want.
pub const MISC_SHARED: u32 = 0x2;
pub const MISC_SHARED_KEYEDMUTEX: u32 = 0x100;
pub const MISC_SHARED_NTHANDLE: u32 = 0x800;

/// DXGI_SHARED_RESOURCE_* access bits for OpenSharedResourceByName / CreateSharedHandle.
pub const DXGI_SHARED_RESOURCE_READ: u32 = 0x80000000;
pub const DXGI_SHARED_RESOURCE_WRITE: u32 = 0x00000001;

pub const D3D11_VPIV_DIMENSION_TEXTURE2D: u32 = 1;
pub const D3D11_VPOV_DIMENSION_TEXTURE2D: u32 = 1;
pub const D3D11_VIDEO_PROCESSOR_FORMAT_SUPPORT_INPUT: u32 = 1;
pub const VP_FORMAT_SUPPORT_OUTPUT: u32 = 2;
pub const DXGI_ADAPTER_FLAG_SOFTWARE: u32 = 2;

// ── structs (MS x64 layout) ──
pub const LUID = extern struct { low: u32, high: i32 };

pub const DXGI_ADAPTER_DESC1 = extern struct {
    Description: [128]u16,
    VendorId: u32,
    DeviceId: u32,
    SubSysId: u32,
    Revision: u32,
    DedicatedVideoMemory: usize,
    DedicatedSystemMemory: usize,
    SharedSystemMemory: usize,
    AdapterLuid: LUID,
    Flags: u32,
};

pub const D3D11_TEXTURE2D_DESC = extern struct {
    Width: u32,
    Height: u32,
    MipLevels: u32,
    ArraySize: u32,
    Format: u32,
    SampleCount: u32,
    SampleQuality: u32,
    Usage: u32,
    BindFlags: u32,
    CPUAccessFlags: u32,
    MiscFlags: u32,
};

pub const DXGI_RATIONAL = extern struct { Numerator: u32, Denominator: u32 };

pub const RECT = extern struct { left: i32, top: i32, right: i32, bottom: i32 };

pub const MAPPED_SUBRESOURCE = extern struct { pData: ?[*]u8, RowPitch: u32, DepthPitch: u32 };

/// D3D11_BOX - the source region of a CopySubresourceRegion. back must be > front (1/0) or the copy
/// is silently a no-op.
pub const BOX = extern struct { left: u32, top: u32, front: u32, right: u32, bottom: u32, back: u32 };

pub const D3D11_VIDEO_PROCESSOR_CONTENT_DESC = extern struct {
    InputFrameFormat: u32,
    InputFrameRate: DXGI_RATIONAL,
    InputWidth: u32,
    InputHeight: u32,
    OutputFrameRate: DXGI_RATIONAL,
    OutputWidth: u32,
    OutputHeight: u32,
    Usage: u32,
};

pub const D3D11_VIDEO_PROCESSOR_STREAM = extern struct {
    Enable: i32,
    OutputIndex: u32,
    InputFrameOrField: u32,
    PastFrames: u32,
    FutureFrames: u32,
    ppPastSurfaces: ?*anyopaque = null,
    pInputSurface: ?*anyopaque = null,
    ppFutureSurfaces: ?*anyopaque = null,
    ppPastSurfacesRight: ?*anyopaque = null,
    pInputSurfaceRight: ?*anyopaque = null,
    ppFutureSurfacesRight: ?*anyopaque = null,
};

pub const VPIV_DESC = extern struct { FourCC: u32, ViewDimension: u32, MipSlice: u32, ArraySlice: u32 };
pub const VPOV_DESC = extern struct { ViewDimension: u32, a: u32, b: u32, c: u32 };
/// D3D11_VIDEO_PROCESSOR_COLOR_SPACE bitfield word: bit2 = YCbCr_Matrix (1 = BT.709).
pub const COLOR_SPACE = extern struct { bits: u32 };

// ── COM interfaces (self as *anyopaque; pads name the IDL methods they skip) ──

pub const IUnk = extern struct {
    v: *const extern struct {
        QueryInterface: *const fn (*anyopaque, *const GUID, *?*anyopaque) callconv(.winapi) HRESULT,
        AddRef: *const fn (*anyopaque) callconv(.winapi) u32,
        Release: *const fn (*anyopaque) callconv(.winapi) u32,
    },
};

pub fn release(p: anytype) void {
    const u: *IUnk = @ptrCast(@alignCast(p));
    _ = u.v.Release(u);
}

pub fn qi(p: anytype, iid: *const GUID, out: *?*anyopaque) HRESULT {
    const u: *IUnk = @ptrCast(@alignCast(p));
    return u.v.QueryInterface(u, iid, out);
}

pub const ID3D11Device = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _p3: [2]VOP, // CreateBuffer CreateTexture1D
        CreateTexture2D: *const fn (*anyopaque, *const D3D11_TEXTURE2D_DESC, ?*const anyopaque, *?*anyopaque) callconv(.winapi) HRESULT,
        _p6: [3]VOP, // CreateTexture3D CreateShaderResourceView CreateUnorderedAccessView
        CreateRenderTargetView: *const fn (*anyopaque, *anyopaque, ?*const anyopaque, *?*anyopaque) callconv(.winapi) HRESULT, // 9
        _p10: [18]VOP, // CreateDepthStencilView(10) .. CreateDeferredContext(27)
        OpenSharedResource: *const fn (*anyopaque, *anyopaque, *const GUID, *?*anyopaque) callconv(.winapi) HRESULT,
    },
};

/// ID3D11Device1 : ID3D11Device - ID3D11Device ends at GetExceptionMode(42), so
/// GetImmediateContext1 is 43 and OpenSharedResourceByName lands at 49.
pub const ID3D11Device1 = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _dev: [40]VOP, // CreateBuffer(3) .. GetExceptionMode(42)
        _p43: [6]VOP, // GetImmediateContext1 CreateDeferredContext1 CreateBlendState1
        //              CreateRasterizerState1 CreateDeviceContextState OpenSharedResource1
        OpenSharedResourceByName: *const fn (*anyopaque, [*:0]const u16, u32, *const GUID, *?*anyopaque) callconv(.winapi) HRESULT,
    },
};

// ID3D11Texture2D: IUnknown(3) + ID3D11DeviceChild(4) + ID3D11Resource(3) → GetDesc at 10.
pub const ID3D11Texture2D = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _child: [4]VOP, // GetDevice GetPrivateData SetPrivateData SetPrivateDataInterface
        _res: [3]VOP, // GetType SetEvictionPriority GetEvictionPriority
        GetDesc: *const fn (*anyopaque, *D3D11_TEXTURE2D_DESC) callconv(.winapi) void,
    },
};

// IDXGIResource1 : IDXGIResource : IDXGIDeviceSubObject : IDXGIObject : IUnknown
//   3..6 IDXGIObject | 7 GetDevice | 8..11 IDXGIResource (GetSharedHandle GetUsage
//   SetEvictionPriority GetEvictionPriority) | 12 CreateSubresourceSurface | 13 CreateSharedHandle
pub const IDXGIResource1 = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _p3: [10]VOP, // IDXGIObject(4) + GetDevice + IDXGIResource(4) + CreateSubresourceSurface
        CreateSharedHandle: *const fn (*anyopaque, ?*const anyopaque, u32, ?[*:0]const u16, *?*anyopaque) callconv(.winapi) HRESULT,
    },
};

// IDXGIKeyedMutex: IUnknown(3) + IDXGIObject(4) + IDXGIDeviceSubObject(1) → AcquireSync at 8.
pub const IDXGIKeyedMutex = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _obj: [4]VOP, // SetPrivateData SetPrivateDataInterface GetPrivateData GetParent
        _sub: [1]VOP, // GetDevice
        AcquireSync: *const fn (*anyopaque, u64, u32) callconv(.winapi) HRESULT,
        ReleaseSync: *const fn (*anyopaque, u64) callconv(.winapi) HRESULT,
    },
};

pub const ID3D11DeviceContext = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _child: [4]VOP, // GetDevice GetPrivateData SetPrivateData SetPrivateDataInterface
        _p7: [7]VOP, // VSSetConstantBuffers(7) .. Draw(13)
        Map: *const fn (*anyopaque, *anyopaque, u32, u32, u32, *MAPPED_SUBRESOURCE) callconv(.winapi) HRESULT,
        Unmap: *const fn (*anyopaque, *anyopaque, u32) callconv(.winapi) void,
        _p16: [30]VOP, // PSSetConstantBuffers(16) .. CopyStructureCount-1, i.e. up to 45
        CopySubresourceRegion: *const fn (*anyopaque, *anyopaque, u32, u32, u32, u32, *anyopaque, u32, ?*const BOX) callconv(.winapi) void, // 46
        CopyResource: *const fn (*anyopaque, *anyopaque, *anyopaque) callconv(.winapi) void, // 47
        UpdateSubresource: *const fn (*anyopaque, *anyopaque, u32, ?*const BOX, *const anyopaque, u32, u32) callconv(.winapi) void, // 48
        _p49: [1]VOP, // CopyStructureCount(49)
        ClearRenderTargetView: *const fn (*anyopaque, *anyopaque, *const [4]f32) callconv(.winapi) void, // 50
        _p51: [60]VOP, // ClearUnorderedAccessViewUint(51) .. ClearState(110)
        // Flush(111) is REQUIRED on any path where ANOTHER PROCESS reads what we wrote: the write is
        // only visible once the command list is submitted, and a named (CPU) access mutex carries no
        // implicit flush the way IDXGIKeyedMutex.ReleaseSync does. Without it the receiver reads the
        // pre-blit content - a blank picture with zero errors in every counter.
        Flush: *const fn (*anyopaque) callconv(.winapi) void,
    },
};

pub const ID3D10Multithread = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _p: [2]VOP, // Enter Leave
        SetMultithreadProtected: *const fn (*anyopaque, i32) callconv(.winapi) i32,
    },
};

pub const ID3D11VideoDevice = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _p3: [1]VOP, // CreateVideoDecoder
        CreateVideoProcessor: *const fn (*anyopaque, *anyopaque, u32, *?*anyopaque) callconv(.winapi) HRESULT,
        _p5: [3]VOP, // CreateAuthenticatedChannel CreateCryptoSession CreateVideoDecoderOutputView
        CreateVideoProcessorInputView: *const fn (*anyopaque, *anyopaque, *anyopaque, *const VPIV_DESC, *?*anyopaque) callconv(.winapi) HRESULT,
        CreateVideoProcessorOutputView: *const fn (*anyopaque, *anyopaque, *anyopaque, *const VPOV_DESC, *?*anyopaque) callconv(.winapi) HRESULT,
        CreateVideoProcessorEnumerator: *const fn (*anyopaque, *const D3D11_VIDEO_PROCESSOR_CONTENT_DESC, *?*anyopaque) callconv(.winapi) HRESULT,
    },
};

pub const ID3D11VideoProcessorEnumerator = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _child: [4]VOP,
        _p7: [1]VOP, // GetVideoProcessorContentDesc
        CheckVideoProcessorFormat: *const fn (*anyopaque, u32, *u32) callconv(.winapi) HRESULT,
    },
};

pub const ID3D11VideoContext = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _child: [4]VOP,
        _p7: [8]VOP, // GetDecoderBuffer ReleaseDecoderBuffer DecoderBeginFrame DecoderEndFrame SubmitDecoderBuffers DecoderExtension VideoProcessorSetOutputTargetRect VideoProcessorSetOutputBackgroundColor
        VideoProcessorSetOutputColorSpace: *const fn (*anyopaque, *anyopaque, *const COLOR_SPACE) callconv(.winapi) void,
        _p16: [12]VOP, // SetOutputAlphaFillMode..GetOutputExtension(26) + SetStreamFrameFormat(27)
        VideoProcessorSetStreamColorSpace: *const fn (*anyopaque, *anyopaque, u32, *const COLOR_SPACE) callconv(.winapi) void,
        _p29: [1]VOP, // SetStreamOutputRate(29)
        // A hardware decoder's NV12 surface is often taller than the frame (16-row alignment:
        // 1088 for 1080). Without an explicit source rect the VP samples the WHOLE surface and
        // squashes those alignment rows into the output - so this slot is load-bearing on the
        // decode path, and it splits the old _p29 pad 24 → 1 + 1 + 22.
        VideoProcessorSetStreamSourceRect: *const fn (*anyopaque, *anyopaque, u32, i32, ?*const RECT) callconv(.winapi) void,
        _p31: [22]VOP, // SetStreamDestRect(31)..GetStreamExtension(52)
        VideoProcessorBlt: *const fn (*anyopaque, *anyopaque, *anyopaque, u32, u32, *const D3D11_VIDEO_PROCESSOR_STREAM) callconv(.winapi) HRESULT,
    },
};

pub const IDXGIFactory1 = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _obj: [4]VOP, // SetPrivateData SetPrivateDataInterface GetPrivateData GetParent
        _fac: [5]VOP, // EnumAdapters MakeWindowAssociation GetWindowAssociation CreateSwapChain CreateSoftwareAdapter
        EnumAdapters1: *const fn (*anyopaque, u32, *?*IDXGIAdapter1) callconv(.winapi) HRESULT,
    },
};

pub const IDXGIAdapter1 = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _obj: [4]VOP,
        _p7: [3]VOP, // EnumOutputs GetDesc CheckInterfaceSupport
        GetDesc1: *const fn (*anyopaque, *DXGI_ADAPTER_DESC1) callconv(.winapi) HRESULT,
    },
};

test "vtable pads keep the audited slot numbers" {
    // A pad edit that shifts a slot is the one bug this file exists to prevent, and it is invisible
    // at compile time: every slot is the same pointer width. Pin the counts by construction.
    const ctxv = @typeInfo(@TypeOf(@as(ID3D11DeviceContext, undefined).v)).pointer.child;
    try std.testing.expectEqual(@as(usize, 14), @offsetOf(ctxv, "Map") / @sizeOf(VOP));
    try std.testing.expectEqual(@as(usize, 46), @offsetOf(ctxv, "CopySubresourceRegion") / @sizeOf(VOP));
    try std.testing.expectEqual(@as(usize, 47), @offsetOf(ctxv, "CopyResource") / @sizeOf(VOP));
    try std.testing.expectEqual(@as(usize, 48), @offsetOf(ctxv, "UpdateSubresource") / @sizeOf(VOP));
    try std.testing.expectEqual(@as(usize, 50), @offsetOf(ctxv, "ClearRenderTargetView") / @sizeOf(VOP));
    try std.testing.expectEqual(@as(usize, 111), @offsetOf(ctxv, "Flush") / @sizeOf(VOP));
    const devv = @typeInfo(@TypeOf(@as(ID3D11Device, undefined).v)).pointer.child;
    try std.testing.expectEqual(@as(usize, 5), @offsetOf(devv, "CreateTexture2D") / @sizeOf(VOP));
    try std.testing.expectEqual(@as(usize, 9), @offsetOf(devv, "CreateRenderTargetView") / @sizeOf(VOP));
    try std.testing.expectEqual(@as(usize, 28), @offsetOf(devv, "OpenSharedResource") / @sizeOf(VOP));
    const dev1v = @typeInfo(@TypeOf(@as(ID3D11Device1, undefined).v)).pointer.child;
    try std.testing.expectEqual(@as(usize, 49), @offsetOf(dev1v, "OpenSharedResourceByName") / @sizeOf(VOP));
    const resv = @typeInfo(@TypeOf(@as(IDXGIResource1, undefined).v)).pointer.child;
    try std.testing.expectEqual(@as(usize, 13), @offsetOf(resv, "CreateSharedHandle") / @sizeOf(VOP));
    const kmv = @typeInfo(@TypeOf(@as(IDXGIKeyedMutex, undefined).v)).pointer.child;
    try std.testing.expectEqual(@as(usize, 8), @offsetOf(kmv, "AcquireSync") / @sizeOf(VOP));
    const vcv = @typeInfo(@TypeOf(@as(ID3D11VideoContext, undefined).v)).pointer.child;
    try std.testing.expectEqual(@as(usize, 53), @offsetOf(vcv, "VideoProcessorBlt") / @sizeOf(VOP));
}
