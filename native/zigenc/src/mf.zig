//! mf.zig - Media Foundation hardware H.264 encode pipeline in Zig (COM via extern
//! vtables, no cgo). Port of internal/mfenc/mf_shim_windows.cpp incl. every 4K60
//! crash-audit fix: deliberate adapter pick, SetMultithreadProtected before the device
//! manager, D3D11_CREATE_DEVICE_VIDEO_SUPPORT, MF_SA_D3D11_AWARE hard gate, cross-vendor
//! MFT ban, explicit H.264 level (4K60 = 5.2), async event-driven drive with sync
//! fallback, MF_LOW_LATENCY + CBR + GOP + no B-frames, live bitrate retarget.
//! Vtable slot counts are hand-derived from the SDK IDL order - every pad names the
//! methods it skips so the counting is reviewable.

const std = @import("std");
/// d3d = native/zigd3d/src/d3d11.zig. This file used to own the D3D11/DXGI declarations; the shell
/// child's native render surfaces need the same ones, so they live in one shared module and this is
/// the re-export face of it. Call sites (cap.zig, dec.zig, main.zig) keep saying `mf.X`.
const d3d = @import("d3d11");

pub const HRESULT = d3d.HRESULT;
const VOP = d3d.VOP;

pub const failed = d3d.failed;

pub const GUID = d3d.GUID;
const g = d3d.g;

// ── GUIDs (values identical to the C++ shim's TU-local DEFINE_GUIDs) ──
// MF_TRANSFORM_ASYNC is the ONLY legal discriminator for drive mode. QI(IMFMediaEventGenerator)
// is NOT: sync hardware MFTs expose that interface too and then never queue METransformNeedInput,
// so an async drive waits FEED_WAIT_MS per frame and the parent reports "encode timeout" - the AMD
// field failure. ffmpeg's mf_unlock_async reads this same attribute for the same reason.
const MF_TRANSFORM_ASYNC = g(0xf81a699a, 0x649a, 0x497d, .{ 0x8c, 0x73, 0x29, 0xf8, 0xfe, 0xd6, 0xad, 0x7a });
const MF_TRANSFORM_ASYNC_UNLOCK = g(0xe5666d6b, 0x3422, 0x4eb6, .{ 0xa4, 0x21, 0xda, 0x7d, 0xb1, 0xf8, 0xe2, 0x07 });
const MF_LOW_LATENCY = g(0x9c27891a, 0xed7a, 0x40e1, .{ 0x88, 0xe8, 0xb2, 0x27, 0x27, 0xa0, 0x24, 0xee });
const MFSampleExtension_CleanPoint = g(0x9cdf01d8, 0xa0f0, 0x43ba, .{ 0xb0, 0x77, 0xea, 0xa0, 0x6c, 0xbd, 0x72, 0x8a });
const MFVideoFormat_NV12 = g(0x3231564e, 0x0000, 0x0010, .{ 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71 });
const MFVideoFormat_H264 = g(0x34363248, 0x0000, 0x0010, .{ 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71 });
const MFMediaType_Video = g(0x73646976, 0x0000, 0x0010, .{ 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71 });
const MF_MT_MAJOR_TYPE = g(0x48eba18e, 0xf8c9, 0x4687, .{ 0xbf, 0x11, 0x0a, 0x74, 0xc9, 0xf9, 0x6a, 0x8f });
const MF_MT_SUBTYPE = g(0xf7e34c9a, 0x42e8, 0x4714, .{ 0xb7, 0x4b, 0xcb, 0x29, 0xd7, 0x2c, 0x35, 0xe5 });
const MF_MT_FRAME_SIZE = g(0x1652c33d, 0xd6b2, 0x4012, .{ 0xb8, 0x34, 0x72, 0x03, 0x08, 0x49, 0xa3, 0x7d });
// MF_MT_MINIMUM_DISPLAY_APERTURE {d7388766-18fe-48c6-a177-ee894867c8c4}: the DISPLAY rect a decoder
// reports when its coded surface is 16-row-aligned (1088 for a 1080 stream). FRAME_SIZE is the
// aligned size, so the decode src rect must come from HERE or the picture is squashed into the output.
const MF_MT_MINIMUM_DISPLAY_APERTURE = g(0xd7388766, 0x18fe, 0x48c6, .{ 0xa1, 0x77, 0xee, 0x89, 0x48, 0x67, 0xc8, 0xc4 });
const MF_MT_FRAME_RATE = g(0xc459a2e8, 0x3d2c, 0x4e44, .{ 0xb1, 0x32, 0xfe, 0xe5, 0x15, 0x6c, 0x7b, 0xb0 });
const MF_MT_PIXEL_ASPECT_RATIO = g(0xc6376a1e, 0x8d0a, 0x4027, .{ 0xbe, 0x45, 0x6d, 0x9a, 0x0a, 0xd3, 0x9b, 0xb6 });
const MF_MT_AVG_BITRATE = g(0x20332624, 0xfb0d, 0x4d9e, .{ 0xbd, 0x0d, 0xcb, 0xf6, 0x78, 0x6c, 0x10, 0x2e });
const MF_MT_INTERLACE_MODE = g(0xe2724bb8, 0xe676, 0x4806, .{ 0xb4, 0xb2, 0xa8, 0xd6, 0xef, 0xb4, 0x4c, 0xcd });
const MF_MT_MPEG2_PROFILE = g(0xad76a80b, 0x2d5c, 0x4e0b, .{ 0xb3, 0x75, 0x64, 0xe5, 0x20, 0x13, 0x70, 0x36 });
const MF_MT_MPEG2_LEVEL = g(0x96f66574, 0x11c5, 0x4015, .{ 0x86, 0x66, 0xbf, 0xf5, 0x16, 0x43, 0x6d, 0xa7 });
const MF_SA_D3D11_AWARE = g(0x206b4fc8, 0xfcf9, 0x4c51, .{ 0xaf, 0xe3, 0x97, 0x64, 0x36, 0x9e, 0x33, 0xa0 });
const MFT_FRIENDLY_NAME = g(0x314ffbae, 0x5b41, 0x4c95, .{ 0x9c, 0x19, 0x4e, 0x7d, 0x58, 0x6f, 0xac, 0xe3 });
const MFT_CATEGORY_VIDEO_ENCODER = g(0xf79eac7d, 0xe545, 0x4387, .{ 0xbd, 0xee, 0xd6, 0x47, 0xd7, 0xbd, 0xe4, 0x2a });
const MFT_CATEGORY_VIDEO_DECODER = g(0xd6c02d4b, 0x6833, 0x45b4, .{ 0x97, 0x1a, 0x05, 0xa4, 0xb0, 0x4b, 0xab, 0x91 });
pub const MFVideoFormat_HEVC = g(0x43564548, 0x0000, 0x0010, .{ 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71 });
// IMFDXGIBuffer: how a D3D11-aware MFT's output sample exposes its ID3D11Texture2D + array slice.
pub const IID_IMFDXGIBuffer = g(0xe7174cfa, 0x1c9e, 0x48b1, .{ 0x88, 0x66, 0x62, 0x62, 0x26, 0xbf, 0xc2, 0x58 });
const CODECAPI_AVEncCommonRateControlMode = g(0x1c0608e9, 0x370c, 0x4710, .{ 0x8a, 0x58, 0xcb, 0x61, 0x81, 0xc4, 0x24, 0x23 });
const CODECAPI_AVEncCommonMeanBitRate = g(0xf7222374, 0x2144, 0x4815, .{ 0xb5, 0x50, 0xa3, 0x7f, 0x8e, 0x12, 0xee, 0x52 });
const CODECAPI_AVEncMPVGOPSize = g(0x95f31b26, 0x95a4, 0x41aa, .{ 0x93, 0x03, 0x24, 0x6a, 0x7f, 0xc6, 0xee, 0xf1 });
const CODECAPI_AVEncCommonLowLatency = g(0x9d3ecd55, 0x89e8, 0x490a, .{ 0x97, 0x0a, 0x0c, 0x95, 0x48, 0xd5, 0xa5, 0x6e });
const CODECAPI_AVEncMPVDefaultBPictureCount = g(0x8d390aac, 0xdc5c, 0x4200, .{ 0xb5, 0x7f, 0x81, 0x4d, 0x04, 0xba, 0xba, 0xb2 });
const CODECAPI_AVEncVideoForceKeyFrame = g(0x398c1b98, 0x8353, 0x475a, .{ 0x9e, 0xf2, 0x8f, 0x26, 0x5d, 0x26, 0x03, 0x45 });
// Rate-control knobs for quality mode (values from the 10.0.26100 SDK codecapi.h).
const CODECAPI_AVEncCommonQuality = g(0xfcbf57a3, 0x7ea5, 0x4b0c, .{ 0x96, 0x44, 0x69, 0xb4, 0x0c, 0x39, 0xc3, 0x91 });
const CODECAPI_AVEncCommonMaxBitRate = g(0x9651eae4, 0x39b9, 0x4ebf, .{ 0x85, 0xef, 0xd7, 0xf4, 0x44, 0xec, 0x74, 0x65 });
const CODECAPI_AVEncCommonBufferSize = g(0x0db96574, 0xb6a4, 0x4c8b, .{ 0x81, 0x06, 0x37, 0x73, 0xde, 0x03, 0x10, 0xcd });
const CODECAPI_AVEncVideoEncodeQP = g(0x2cb5696b, 0x23fb, 0x4ce1, .{ 0xa0, 0xf9, 0xef, 0x5b, 0x90, 0xfd, 0x55, 0xca });
const CODECAPI_AVEncVideoMinQP = g(0x0ee22c6a, 0xa37c, 0x4568, .{ 0xb5, 0xf1, 0x9d, 0x4c, 0x2b, 0x3a, 0xb8, 0x86 });
const CODECAPI_AVEncVideoMaxQP = g(0x3daf6f66, 0xa6a7, 0x45e0, .{ 0xa8, 0xe5, 0xf2, 0x74, 0x3f, 0x46, 0xa3, 0xa2 });
const IID_ICodecAPI = g(0x901db4c7, 0x31ce, 0x41a2, .{ 0x85, 0xdc, 0x8f, 0xa0, 0xbf, 0x41, 0xb8, 0xda });
// NOTE last byte 0x7d: the hand-rolled 0x7b variant silently forces sync drive (E_NOINTERFACE).
const IID_IMFMediaEventGenerator = g(0x2cd0bd52, 0xbcd5, 0x4b89, .{ 0xb6, 0x2c, 0xea, 0xdc, 0x0c, 0x03, 0x1e, 0x7d });
const IID_ID3D10Multithread = d3d.IID_ID3D10Multithread;
pub const IID_ID3D11Texture2D = d3d.IID_ID3D11Texture2D;
pub const IID_IDXGIKeyedMutex = d3d.IID_IDXGIKeyedMutex;
const IID_IMF2DBuffer = g(0x7dc9d5f9, 0x9ed9, 0x44ec, .{ 0x9b, 0xbf, 0x06, 0x00, 0xbb, 0x58, 0x9f, 0xbb });
const IID_ID3D11VideoDevice = d3d.IID_ID3D11VideoDevice;
const IID_ID3D11VideoContext = d3d.IID_ID3D11VideoContext;
const IID_IMFTransform = g(0xbf94c121, 0x5b05, 0x4e6f, .{ 0x80, 0x00, 0xba, 0x59, 0x89, 0x61, 0x41, 0x4d });
const IID_IDXGIFactory1 = d3d.IID_IDXGIFactory1;

// ── HRESULT / enum constants ──
const MF_E_TRANSFORM_STREAM_CHANGE: HRESULT = @bitCast(@as(u32, 0xC00D6D61));
const MF_E_TRANSFORM_NEED_MORE_INPUT: HRESULT = @bitCast(@as(u32, 0xC00D6D72));
const MF_E_NO_EVENTS_AVAILABLE: HRESULT = @bitCast(@as(u32, 0xC00D3E80));
const MF_E_NOTACCEPTING: HRESULT = @bitCast(@as(u32, 0xC00D36B5));
const E_FAIL = d3d.E_FAIL;

const MF_VERSION: u32 = 0x00020070;
const MFSTARTUP_LITE: u32 = 1;
const MFT_ENUM_FLAG_SYNCMFT: u32 = 0x1;
const MFT_ENUM_FLAG_ASYNCMFT: u32 = 0x2;
const MFT_ENUM_FLAG_HARDWARE: u32 = 0x4;
const MFT_ENUM_FLAG_LOCALMFT: u32 = 0x10;
const MFT_ENUM_FLAG_SORTANDFILTER: u32 = 0x40;
const MFT_MESSAGE_COMMAND_FLUSH: u32 = 0;
const MFT_MESSAGE_SET_D3D_MANAGER: u32 = 2;
const MFT_MESSAGE_COMMAND_DRAIN: u32 = 1;
const MFT_MESSAGE_NOTIFY_BEGIN_STREAMING: u32 = 0x10000000;
const MFT_MESSAGE_NOTIFY_END_STREAMING: u32 = 0x10000001;
const MFT_MESSAGE_NOTIFY_END_OF_STREAM: u32 = 0x10000002;
const MFT_MESSAGE_NOTIFY_START_OF_STREAM: u32 = 0x10000003;
const MFT_OUTPUT_STREAM_PROVIDES_SAMPLES: u32 = 0x100;
const MFT_OUTPUT_STREAM_CAN_PROVIDE_SAMPLES: u32 = 0x200;
const METransformNeedInput: u32 = 601;
const METransformHaveOutput: u32 = 602;
const METransformDrainComplete: u32 = 611;
const MF_EVENT_FLAG_NO_WAIT: u32 = 1;

const D3D_DRIVER_TYPE_UNKNOWN = d3d.D3D_DRIVER_TYPE_UNKNOWN;
const D3D_DRIVER_TYPE_HARDWARE = d3d.D3D_DRIVER_TYPE_HARDWARE;
const D3D_DRIVER_TYPE_WARP = d3d.D3D_DRIVER_TYPE_WARP;
const D3D11_CREATE_DEVICE_BGRA_SUPPORT = d3d.D3D11_CREATE_DEVICE_BGRA_SUPPORT;
const D3D11_CREATE_DEVICE_VIDEO_SUPPORT = d3d.D3D11_CREATE_DEVICE_VIDEO_SUPPORT;
const D3D11_SDK_VERSION = d3d.D3D11_SDK_VERSION;
const DXGI_FORMAT_R8G8B8A8_UNORM = d3d.DXGI_FORMAT_R8G8B8A8_UNORM;
const DXGI_FORMAT_B8G8R8A8_UNORM = d3d.DXGI_FORMAT_B8G8R8A8_UNORM;
const DXGI_FORMAT_NV12 = d3d.DXGI_FORMAT_NV12;
pub const bind_shader_resource = d3d.bind_shader_resource;
const D3D11_BIND_SHADER_RESOURCE = d3d.D3D11_BIND_SHADER_RESOURCE;
pub const bind_render_target = d3d.bind_render_target;
const D3D11_BIND_RENDER_TARGET = d3d.D3D11_BIND_RENDER_TARGET;
const D3D11_VPIV_DIMENSION_TEXTURE2D = d3d.D3D11_VPIV_DIMENSION_TEXTURE2D;
const D3D11_VPOV_DIMENSION_TEXTURE2D = d3d.D3D11_VPOV_DIMENSION_TEXTURE2D;
const D3D11_VIDEO_PROCESSOR_FORMAT_SUPPORT_INPUT = d3d.D3D11_VIDEO_PROCESSOR_FORMAT_SUPPORT_INPUT;
pub const VP_FORMAT_SUPPORT_OUTPUT = d3d.VP_FORMAT_SUPPORT_OUTPUT;
pub const USAGE_STAGING = d3d.USAGE_STAGING;
pub const CPU_ACCESS_READ = d3d.CPU_ACCESS_READ;
pub const MAP_READ = d3d.MAP_READ;
const DXGI_ADAPTER_FLAG_SOFTWARE = d3d.DXGI_ADAPTER_FLAG_SOFTWARE;
const VT_UI4: u16 = 19;
const VT_BOOL: u16 = 11;
const VT_UI8: u16 = 21;

// ── structs (MS x64 layout; shared - see native/zigd3d/src/d3d11.zig) ──
pub const LUID = d3d.LUID;
const DXGI_ADAPTER_DESC1 = d3d.DXGI_ADAPTER_DESC1;
pub const D3D11_TEXTURE2D_DESC = d3d.D3D11_TEXTURE2D_DESC;
const DXGI_RATIONAL = d3d.DXGI_RATIONAL;
pub const RECT = d3d.RECT;
pub const MAPPED_SUBRESOURCE = d3d.MAPPED_SUBRESOURCE;
const D3D11_VIDEO_PROCESSOR_CONTENT_DESC = d3d.D3D11_VIDEO_PROCESSOR_CONTENT_DESC;
const D3D11_VIDEO_PROCESSOR_STREAM = d3d.D3D11_VIDEO_PROCESSOR_STREAM;
pub const VPIV_DESC = d3d.VPIV_DESC;
const VPOV_DESC = d3d.VPOV_DESC;
const COLOR_SPACE = d3d.COLOR_SPACE;

const MFT_REGISTER_TYPE_INFO = extern struct { major: GUID, sub: GUID };
const MFT_OUTPUT_STREAM_INFO = extern struct { dwFlags: u32, cbSize: u32, cbAlignment: u32 };
const MFT_OUTPUT_DATA_BUFFER = extern struct {
    dwStreamID: u32 = 0,
    pSample: ?*IMFSample = null,
    dwStatus: u32 = 0,
    pEvents: ?*anyopaque = null,
};

// VARIANT (24 bytes on x64): vt + 3 reserved words + 16-byte value area.
const VARIANT = extern struct { vt: u16, w1: u16 = 0, w2: u16 = 0, w3: u16 = 0, val: u64 = 0, val2: u64 = 0 };

// ── COM interfaces (self as *anyopaque; pads name the IDL methods they skip) ──

pub const IUnk = d3d.IUnk;
pub const release = d3d.release;
pub const qi = d3d.qi;

// IMFAttributes prefix (33 slots incl. IUnknown) shared by media types/samples/activates/events.
const AttrVtbl = extern struct {
    _iunk: [3]VOP,
    _get0: [4]VOP, // GetItem GetItemType CompareItem Compare
    GetUINT32: *const fn (*anyopaque, *const GUID, *u32) callconv(.winapi) HRESULT,
    GetUINT64: *const fn (*anyopaque, *const GUID, *u64) callconv(.winapi) HRESULT,
    _get1: [1]VOP, // GetDouble
    GetGUID: *const fn (*anyopaque, *const GUID, *GUID) callconv(.winapi) HRESULT,
    _get2: [1]VOP, // GetStringLength
    GetString: *const fn (*anyopaque, *const GUID, [*]u16, u32, ?*u32) callconv(.winapi) HRESULT,
    _get3a: [2]VOP, // GetAllocatedString GetBlobSize
    GetBlob: *const fn (*anyopaque, *const GUID, [*]u8, u32, ?*u32) callconv(.winapi) HRESULT,
    _get3b: [2]VOP, // GetAllocatedBlob GetUnknown
    _set0: [3]VOP, // SetItem DeleteItem DeleteAllItems
    SetUINT32: *const fn (*anyopaque, *const GUID, u32) callconv(.winapi) HRESULT,
    SetUINT64: *const fn (*anyopaque, *const GUID, u64) callconv(.winapi) HRESULT,
    _set1: [1]VOP, // SetDouble
    SetGUID: *const fn (*anyopaque, *const GUID, *const GUID) callconv(.winapi) HRESULT,
    _set2: [3]VOP, // SetString SetBlob SetUnknown
    _lock: [2]VOP, // LockStore UnlockStore
    _cnt: [3]VOP, // GetCount GetItemByIndex CopyAllItems
};

pub const IMFAttributes = extern struct {
    v: *const AttrVtbl,
};

pub const IMFMediaType = extern struct {
    v: *const AttrVtbl, // + GetMajorType.. (unused)
};

pub const IMFActivate = extern struct {
    v: *const extern struct {
        attr: AttrVtbl,
        ActivateObject: *const fn (*anyopaque, *const GUID, *?*anyopaque) callconv(.winapi) HRESULT,
        ShutdownObject: *const fn (*anyopaque) callconv(.winapi) HRESULT,
        DetachObject: VOP,
    },
};

pub const IMFMediaEvent = extern struct {
    v: *const extern struct {
        attr: AttrVtbl,
        GetType: *const fn (*anyopaque, *u32) callconv(.winapi) HRESULT,
    },
};

pub const IMFSample = extern struct {
    v: *const extern struct {
        attr: AttrVtbl,
        _f: [2]VOP, // GetSampleFlags SetSampleFlags
        GetSampleTime: *const fn (*anyopaque, *i64) callconv(.winapi) HRESULT,
        SetSampleTime: *const fn (*anyopaque, i64) callconv(.winapi) HRESULT,
        _d0: [1]VOP, // GetSampleDuration
        SetSampleDuration: *const fn (*anyopaque, i64) callconv(.winapi) HRESULT,
        _b0: [2]VOP, // GetBufferCount GetBufferByIndex
        ConvertToContiguousBuffer: *const fn (*anyopaque, *?*IMFMediaBuffer) callconv(.winapi) HRESULT,
        AddBuffer: *const fn (*anyopaque, *IMFMediaBuffer) callconv(.winapi) HRESULT,
        // RemoveBufferByIndex RemoveAllBuffers GetTotalLength CopyToBuffer (unused)
    },
};

pub const IMFMediaBuffer = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        Lock: *const fn (*anyopaque, *?[*]u8, ?*u32, ?*u32) callconv(.winapi) HRESULT,
        Unlock: *const fn (*anyopaque) callconv(.winapi) HRESULT,
        GetCurrentLength: VOP,
        SetCurrentLength: *const fn (*anyopaque, u32) callconv(.winapi) HRESULT,
        // GetMaxLength (unused)
    },
};

pub const IMF2DBuffer = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _p: [4]VOP, // Lock2D Unlock2D GetScanline0AndPitch IsContiguousFormat
        GetContiguousLength: *const fn (*anyopaque, *u32) callconv(.winapi) HRESULT,
    },
};

// IMFDXGIBuffer: IUnknown(3) + GetResource(3) GetSubresourceIndex(4) GetUnknown SetUnknown.
pub const IMFDXGIBuffer = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        GetResource: *const fn (*anyopaque, *const GUID, *?*anyopaque) callconv(.winapi) HRESULT,
        GetSubresourceIndex: *const fn (*anyopaque, *u32) callconv(.winapi) HRESULT,
        // GetUnknown SetUnknown (unused)
    },
};

pub const IMFTransform = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _p3: [4]VOP, // GetStreamLimits GetStreamCount GetStreamIDs GetInputStreamInfo
        GetOutputStreamInfo: *const fn (*anyopaque, u32, *MFT_OUTPUT_STREAM_INFO) callconv(.winapi) HRESULT,
        GetAttributes: *const fn (*anyopaque, *?*IMFAttributes) callconv(.winapi) HRESULT,
        _p9: [4]VOP, // GetInputStreamAttributes GetOutputStreamAttributes DeleteInputStream AddInputStreams
        GetInputAvailableType: *const fn (*anyopaque, u32, u32, *?*IMFMediaType) callconv(.winapi) HRESULT,
        GetOutputAvailableType: *const fn (*anyopaque, u32, u32, *?*IMFMediaType) callconv(.winapi) HRESULT,
        SetInputType: *const fn (*anyopaque, u32, ?*IMFMediaType, u32) callconv(.winapi) HRESULT,
        SetOutputType: *const fn (*anyopaque, u32, ?*IMFMediaType, u32) callconv(.winapi) HRESULT,
        _p17: [6]VOP, // GetInputCurrentType GetOutputCurrentType GetInputStatus GetOutputStatus SetOutputBounds ProcessEvent
        ProcessMessage: *const fn (*anyopaque, u32, usize) callconv(.winapi) HRESULT,
        ProcessInput: *const fn (*anyopaque, u32, *IMFSample, u32) callconv(.winapi) HRESULT,
        ProcessOutput: *const fn (*anyopaque, u32, u32, *MFT_OUTPUT_DATA_BUFFER, *u32) callconv(.winapi) HRESULT,
    },
};

pub const IMFMediaEventGenerator = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        GetEvent: *const fn (*anyopaque, u32, *?*IMFMediaEvent) callconv(.winapi) HRESULT,
        // BeginGetEvent EndGetEvent QueueEvent (unused)
    },
};

pub const IMFDXGIDeviceManager = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        _p: [4]VOP, // CloseDeviceHandle GetVideoService LockDevice OpenDeviceHandle (alphabetical IDL)
        ResetDevice: *const fn (*anyopaque, *anyopaque, u32) callconv(.winapi) HRESULT,
        // TestDevice UnlockDevice (unused)
    },
};

/// swdec: the handful of private MF/D3D symbols the decode child's SOFTWARE mode (dec.zig, VRAM-light
/// CPU decode, .devnotes/VRAM_FULL_CARD_POLICY.md 1e) needs. Aliases only - no new definitions.
pub const VOP_ = VOP;

pub const swdec = struct {
    pub const IID_CodecAPI = IID_ICodecAPI;
    pub const IID_2DBuffer = IID_IMF2DBuffer;
    pub const Variant = VARIANT;
    pub const vt_ui4 = VT_UI4;
    pub const fmt_nv12 = DXGI_FORMAT_NV12;
    pub const bind_srv = D3D11_BIND_SHADER_RESOURCE;
    pub const bind_rt: u32 = 0x20; // D3D11_BIND_RENDER_TARGET
    pub const bind_decoder: u32 = 0x200; // D3D11_BIND_DECODER
    // CODECAPI_AVDecNumWorkerThreads {9561C3E8-EA9E-4435-9B1E-A93E691894D8}
    pub const AVDecNumWorkerThreads = g(0x9561c3e8, 0xea9e, 0x4435, .{ 0x9b, 0x1e, 0xa9, 0x3e, 0x69, 0x18, 0x94, 0xd8 });
};

pub const ICodecAPI = extern struct {
    v: *const extern struct {
        _iunk: [3]VOP,
        IsSupported: *const fn (*anyopaque, *const GUID) callconv(.winapi) HRESULT,
        IsModifiable: *const fn (*anyopaque, *const GUID) callconv(.winapi) HRESULT,
        GetParameterRange: *const fn (*anyopaque, *const GUID, *VARIANT, *VARIANT, *VARIANT) callconv(.winapi) HRESULT,
        GetParameterValues: *const fn (*anyopaque, *const GUID, *?[*]VARIANT, *u32) callconv(.winapi) HRESULT,
        GetDefaultValue: *const fn (*anyopaque, *const GUID, *VARIANT) callconv(.winapi) HRESULT,
        GetValue: *const fn (*anyopaque, *const GUID, *VARIANT) callconv(.winapi) HRESULT,
        SetValue: *const fn (*anyopaque, *const GUID, *const VARIANT) callconv(.winapi) HRESULT,
    },
};

// D3D11/DXGI interfaces: shared, one copy, native/zigd3d/src/d3d11.zig. Everything above this line
// is Media-Foundation-only and stays here.
pub const ID3D11Device = d3d.ID3D11Device;
pub const ID3D11Texture2D = d3d.ID3D11Texture2D;
pub const IDXGIKeyedMutex = d3d.IDXGIKeyedMutex;
pub const ID3D11DeviceContext = d3d.ID3D11DeviceContext;
pub const ID3D10Multithread = d3d.ID3D10Multithread;
pub const ID3D11VideoDevice = d3d.ID3D11VideoDevice;
pub const ID3D11VideoProcessorEnumerator = d3d.ID3D11VideoProcessorEnumerator;
pub const ID3D11VideoContext = d3d.ID3D11VideoContext;
pub const IDXGIFactory1 = d3d.IDXGIFactory1;
pub const IDXGIAdapter1 = d3d.IDXGIAdapter1;

// ── flat imports ──
extern "mfplat" fn MFStartup(u32, u32) callconv(.winapi) HRESULT;
extern "mfplat" fn MFTEnumEx(GUID, u32, ?*const MFT_REGISTER_TYPE_INFO, ?*const MFT_REGISTER_TYPE_INFO, *?[*]*IMFActivate, *u32) callconv(.winapi) HRESULT;
extern "mfplat" fn MFCreateMediaType(*?*IMFMediaType) callconv(.winapi) HRESULT;
extern "mfplat" fn MFCreateDXGIDeviceManager(*u32, *?*IMFDXGIDeviceManager) callconv(.winapi) HRESULT;
extern "mfplat" fn MFCreateSample(*?*IMFSample) callconv(.winapi) HRESULT;
extern "mfplat" fn MFCreateMemoryBuffer(u32, *?*IMFMediaBuffer) callconv(.winapi) HRESULT;
extern "mfplat" fn MFCreateDXGISurfaceBuffer(*const GUID, *anyopaque, u32, i32, *?*IMFMediaBuffer) callconv(.winapi) HRESULT;
extern "d3d11" fn D3D11CreateDevice(?*anyopaque, u32, ?*anyopaque, u32, ?[*]const u32, u32, u32, *?*ID3D11Device, ?*u32, *?*ID3D11DeviceContext) callconv(.winapi) HRESULT;
extern "dxgi" fn CreateDXGIFactory1(*const GUID, *?*anyopaque) callconv(.winapi) HRESULT;
extern "ole32" fn CoInitializeEx(?*anyopaque, u32) callconv(.winapi) HRESULT;
extern "ole32" fn CoTaskMemFree(?*anyopaque) callconv(.winapi) void;
extern "kernel32" fn Sleep(u32) callconv(.winapi) void;

pub fn coInitMTA() void {
    _ = CoInitializeEx(null, 0); // COINIT_MULTITHREADED
}

fn trace(comptime fmt: []const u8, args: anytype) void {
    std.debug.print("mfenc stage: " ++ fmt ++ "\n", args);
}

// ── crash attribution: the last driver-touching call, latched in shared memory ──
//
// Why not stderr: the AV happens INSIDE a vendor driver, often on its own worker thread, so
// (a) a per-frame stderr line at 60 fps is unusable and its last write may still be buffered
// when the process dies, and (b) the open-only stages that exist today stop at "open complete",
// which is exactly why the field AV is unattributed. A single relaxed store into a shm word the
// PARENT already maps survives the crash for free: the supervisor reads it after the exit code
// and names the faulting call. Cost is one instruction per stage, so it is ALWAYS on - a
// diagnostic that only works when someone remembered to set an env var is not a diagnostic.
pub const Stage = enum(u32) {
    idle = 0,
    // open
    mfstartup = 10,
    pick_adapter = 11,
    create_device = 12,
    device_manager = 13,
    enum_mft = 14,
    activate_mft = 15,
    set_d3d_manager = 16,
    set_output_type = 17,
    set_input_type = 18,
    codec_api = 19,
    stream_info = 20,
    evgen_qi = 21,
    create_vp = 22,
    create_textures = 23,
    begin_streaming = 24,
    open_done = 25,
    // feed / submit
    gate_input = 40,
    swizzle = 41,
    update_subresource = 42,
    vp_blt = 43,
    sample_time = 44,
    force_idr = 45,
    wait_need_input = 46,
    process_input = 47,
    get_event = 48,
    process_output = 49,
    lock_output = 50,
    sink_put = 51,
    sw_readback_copy = 52,
    sw_readback_map = 53,
    set_bitrate = 54,
    // drain / close
    drain_msg = 60,
    drain_pump = 61,
    close_flush = 70,
    close_end_streaming = 71,
    close_clear_d3d_manager = 72,
    close_release_mft = 73,
    close_shutdown_activate = 74,
    close_release_pool = 75,
    close_release_vp = 76,
    close_release_device = 77,
    close_done = 78,
};

/// StageSink is the shm word the parent reads after a child death (main.zig points it at the
/// session's header slot). null = no session shm (bench/tests): stages are then trace-only.
pub const StageSink = ?*volatile u32;

// ── helpers ──

fn attrSetU32(p: anytype, key: *const GUID, val: u32) void {
    const a: *const AttrVtbl = @ptrCast(@alignCast(p.v));
    _ = a.SetUINT32(@ptrCast(p), key, val);
}

fn attrSetU64(p: anytype, key: *const GUID, val: u64) void {
    const a: *const AttrVtbl = @ptrCast(@alignCast(p.v));
    _ = a.SetUINT64(@ptrCast(p), key, val);
}

fn attrSetGUID(p: anytype, key: *const GUID, val: *const GUID) void {
    const a: *const AttrVtbl = @ptrCast(@alignCast(p.v));
    _ = a.SetGUID(@ptrCast(p), key, val);
}

fn attrGetU32(p: anytype, key: *const GUID) ?u32 {
    const a: *const AttrVtbl = @ptrCast(@alignCast(p.v));
    var v: u32 = 0;
    if (failed(a.GetUINT32(@ptrCast(p), key, &v))) return null;
    return v;
}

fn attrGetGUID(p: anytype, key: *const GUID) ?GUID {
    const a: *const AttrVtbl = @ptrCast(@alignCast(p.v));
    var v: GUID = undefined;
    if (failed(a.GetGUID(@ptrCast(p), key, &v))) return null;
    return v;
}

fn attrGetU64(p: anytype, key: *const GUID) ?u64 {
    const a: *const AttrVtbl = @ptrCast(@alignCast(p.v));
    var v: u64 = 0;
    if (failed(a.GetUINT64(@ptrCast(p), key, &v))) return null;
    return v;
}

// VideoArea is the display size (cx, cy) read from an MFVideoArea blob.
pub const VideoArea = struct { cx: i32, cy: i32 };

// attrGetDisplayArea reads MF_MT_MINIMUM_DISPLAY_APERTURE (a 16-byte MFVideoArea: OffsetX(4),
// OffsetY(4), Area{cx:i32, cy:i32}). Returns the DISPLAY size, or null when the key is absent.
fn attrGetDisplayArea(p: anytype, key: *const GUID) ?VideoArea {
    const a: *const AttrVtbl = @ptrCast(@alignCast(p.v));
    var buf: [16]u8 = @splat(0);
    var got: u32 = 0;
    if (failed(a.GetBlob(@ptrCast(p), key, &buf, 16, &got)) or got < 16) return null;
    const cx = std.mem.readInt(i32, buf[8..12], .little);
    const cy = std.mem.readInt(i32, buf[12..16], .little);
    if (cx <= 0 or cy <= 0) return null;
    return .{ .cx = cx, .cy = cy };
}

fn guidEq(a: GUID, b: GUID) bool {
    return std.mem.eql(u8, std.mem.asBytes(&a), std.mem.asBytes(&b));
}

// mtVideo builds a video media type (rational fps; square PAR; progressive).
fn mtVideo(sub: *const GUID, w: i32, h: i32, fpsN: i32, fpsD: i32) ?*IMFMediaType {
    var mt: ?*IMFMediaType = null;
    if (failed(MFCreateMediaType(&mt)) or mt == null) return null;
    const m = mt.?;
    attrSetGUID(m, &MF_MT_MAJOR_TYPE, &MFMediaType_Video);
    attrSetGUID(m, &MF_MT_SUBTYPE, sub);
    attrSetU64(m, &MF_MT_FRAME_SIZE, (@as(u64, @intCast(w)) << 32) | @as(u64, @intCast(h)));
    attrSetU64(m, &MF_MT_FRAME_RATE, (@as(u64, @intCast(fpsN)) << 32) | @as(u64, @intCast(fpsD)));
    attrSetU64(m, &MF_MT_PIXEL_ASPECT_RATIO, (1 << 32) | 1);
    attrSetU32(m, &MF_MT_INTERLACE_MODE, 2); // progressive
    return m;
}

// vendorTag maps DXGI VendorID to encoder-MFT friendly-name substring.
fn vendorTag(vid: u32) ?[]const u8 {
    return switch (vid) {
        0x10DE => "NVIDIA",
        0x1002, 0x1022 => "AMD",
        0x8086 => "Intel",
        else => null,
    };
}

const known_tags = [_][]const u8{ "NVIDIA", "AMD", "Intel" };

fn containsNoCase(hay: []const u8, needle: []const u8) bool {
    return std.ascii.indexOfIgnoreCase(hay, needle) != null;
}

// vendorMismatch: fn names a DIFFERENT known vendor than the device's tag - never offer the
// device manager cross-vendor (broken vendor MFTs fault instead of failing there).
fn vendorMismatch(name: []const u8, device_tag: ?[]const u8) bool {
    const tag = device_tag orelse return false;
    if (name.len == 0) return false;
    for (known_tags) |t| {
        if (std.mem.eql(u8, t, tag)) continue;
        if (containsNoCase(name, t)) return true;
    }
    return false;
}

// h264LevelFor: minimal eAVEncH264VLevel per H.264 Table A-1 (4K60 = 5.2). Explicit level =
// the 4K crash-audit fix (drivers deriving it from an unset field mis-size buffers).
fn h264LevelFor(w: i32, h: i32, fpsN: i32, fpsD: i32) u32 {
    const mbs: i64 = @as(i64, @divTrunc(w + 15, 16)) * @as(i64, @divTrunc(h + 15, 16));
    const mbps: i64 = @divTrunc(mbs * fpsN, @max(fpsD, 1));
    const tab = [_]struct { lvl: u32, mb: i64, mbps: i64 }{
        .{ .lvl = 31, .mb = 3600, .mbps = 108000 },  .{ .lvl = 32, .mb = 5120, .mbps = 216000 },
        .{ .lvl = 40, .mb = 8192, .mbps = 245760 },  .{ .lvl = 41, .mb = 8192, .mbps = 245760 },
        .{ .lvl = 42, .mb = 8704, .mbps = 522240 },  .{ .lvl = 50, .mb = 22080, .mbps = 589824 },
        .{ .lvl = 51, .mb = 36864, .mbps = 983040 }, .{ .lvl = 52, .mb = 36864, .mbps = 2073600 },
    };
    for (tab) |t| {
        if (mbs <= t.mb and mbps <= t.mbps) return t.lvl;
    }
    return 52;
}

// NV12 input pool: starts at NVPOOL_INIT, grows on demand one slot at a time up to NVPOOL_MAX
// (the old fixed depth, ~95 MiB at 4K). Any depth >= 2 is CORRECT under gateInput's in-flight cap
// (a slot is rewritten only once its submission is out of the encoder); depth only bounds how many
// inputs the MFT may hold. That number is driver behaviour, not provable from this code - an async
// MFT that wants K inputs queued before its first output would starve at a smaller pool - so
// instead of guessing, the pool grows exactly when the cap would make the encoder wait.
// NVPOOL_INIT = 4 is what internal/gpubudget charges an encode session.
const NVPOOL_INIT = 4;
const NVPOOL_MAX = 8;
/// pool_init: initial depth; RAVE_MATE_MFENC_POOL_INIT (1..8) is a TEST hook that makes growth
/// reachable on silicon whose MFT never holds an input across a submit (1 = grows on frame one).
pub var pool_init: usize = NVPOOL_INIT;

// rotateIn moves a[n] to a[k], shifting a[k..n) up one (pool growth splice).
fn rotateIn(comptime T: type, a: *[NVPOOL_MAX]T, k: usize, n: usize) void {
    const v = a[n];
    var i = n;
    while (i > k) : (i -= 1) a[i] = a[i - 1];
    a[k] = v;
}

test "rotateIn splices the new slot at k and keeps rotation order" {
    var a = [_]u8{ 0, 1, 2, 3, 9, 0, 0, 0 };
    rotateIn(u8, &a, 1, 4);
    try std.testing.expectEqualSlices(u8, &.{ 0, 9, 1, 2, 3 }, a[0..5]);
    var b = [_]u8{ 0, 1, 2, 3, 9, 0, 0, 0 };
    rotateIn(u8, &b, 0, 4);
    try std.testing.expectEqualSlices(u8, &.{ 9, 0, 1, 2, 3 }, b[0..5]);
    var c = [_]u8{ 0, 1, 2, 3, 9, 0, 0, 0 };
    rotateIn(u8, &c, 4, 4); // nv_idx wrapped to the end: append
    try std.testing.expectEqualSlices(u8, &.{ 0, 1, 2, 3, 9 }, c[0..5]);
}
const FEED_WAIT_MS = 2000; // CLOSE-time budget only (drain): a tail may legitimately take a while

// SUBMIT_WAIT_MS bounds a SINGLE frame's wait for encoder credit, and it is deliberately far below
// the parent's encodeWait (4 s). Both used to be 2 s, so the parent's deadline expired at exactly
// the moment the child's did: a saturated encoder - e.g. a 4K60 route plus a second session on one
// iGPU - produced "mfenc: encode timeout" and the ROUTE ENDED, instead of dropping one frame and
// carrying on. The child now gives up on the frame quickly, counts it, and lets the parent proceed;
// saturation degrades to visible drops, which is a survivable failure, not a dead route.
const SUBMIT_WAIT_MS = 250;

/// RC_BUSY: the encoder had no credit for this frame inside SUBMIT_WAIT_MS. This is a DROP, not a
/// failure - the caller counts it and moves on. Every other negative return is a real error.
pub const RC_BUSY: i32 = -9;

/// SwPolicy gates the software MF encoder tier (RAVE_MATE_MFENC_SW=0|1 in the child).
/// auto = hardware tiers first, software as the last rung; off = hardware only (the pre-tier
/// behaviour, kept so a rig can prove a hardware regression); force = software only (the tier's
/// own test hook - it must be reachable on a box that HAS working silicon).
pub const SwPolicy = enum { auto, off, force };
pub var sw_policy: SwPolicy = .auto;

/// DevicePolicy decides whether the D3D11 device + MF device manager are shared by every session
/// in this child (`.child`, the default) or created per session (`.session`, the old behaviour).
///
/// WHY THIS EXISTS. Field evidence, 3/3: on AMD a SINGLE encode session is clean (real content,
/// zero drops, no fault), but opening a SECOND session in the same child wedges it - no
/// METransformNeedInput, the parent times out at 2.2 s, and the child later takes an access
/// violation. The child is a PER-ADAPTER process, yet every session was building its own
/// ID3D11Device + IMFDXGIDeviceManager, so two sessions meant two D3D11 devices and two device
/// managers on ONE adapter in ONE process - each MFT sitting on its own vendor context. That is
/// the pattern no reference MF pipeline uses (OBS, ffmpeg and the Media Engine all share one
/// device manager per adapter) and the one AMD's AMF-backed MFT falls over on. NVIDIA tolerated
/// it, which is why every dev-box gate passed: they are all single-session.
///
/// Sharing is legal because the device is created with D3D11_CREATE_DEVICE_VIDEO_SUPPORT and
/// ID3D10Multithread::SetMultithreadProtected(TRUE), which is exactly what makes the device and
/// its immediate/video context safe to drive from several session threads.
///
/// `RAVE_MATE_MFENC_DEVICE=session` restores the per-session device so one build can A/B the
/// theory on a live AMD rig without another deploy cycle.
pub const DevicePolicy = enum { child, session };
pub var device_policy: DevicePolicy = .child;

/// policyNames renders the active policies for the hello event. The AMD box has no Go toolchain
/// and no remote-exec, so which code path is running has to be legible in the LOG STREAM - a
/// passing run that cannot name its own configuration proves nothing.
pub fn devicePolicyName() []const u8 {
    return switch (device_policy) {
        .child => "child",
        .session => "session",
    };
}

pub fn swPolicyName() []const u8 {
    return switch (sw_policy) {
        .auto => "auto",
        .off => "off",
        .force => "force",
    };
}

// Lock is a raw SRWLOCK: Zig 0.16 moved std's blocking Mutex onto the Io runtime, and this lock is
// shared with COM/MFT worker threads (main.zig has the same shape for the same reason).
extern "kernel32" fn AcquireSRWLockExclusive(*usize) callconv(.winapi) void;
extern "kernel32" fn ReleaseSRWLockExclusive(*usize) callconv(.winapi) void;

const Lock = struct {
    srw: usize = 0,
    fn lock(l: *Lock) void {
        AcquireSRWLockExclusive(&l.srw);
    }
    fn unlock(l: *Lock) void {
        ReleaseSRWLockExclusive(&l.srw);
    }
};

// SharedDev is the child's ONE device + device manager (device_policy == .child), leased by encode
// AND decode sessions (dec.zig). Refcounted: the last lease out tears it down, so an idle child holds
// no GPU device. Heap-allocated so a LOST device can be detached from `shared_dev` (new sessions get a
// fresh one) while sessions still holding it release their own lease later.
pub const SharedDev = struct {
    dev: *ID3D11Device,
    ctx: *ID3D11DeviceContext,
    vdev: *ID3D11VideoDevice,
    vctx: *ID3D11VideoContext,
    devmgr: *IMFDXGIDeviceManager,
    vendor: u32,
    res_luid: i64,
    adapter_buf: [128]u8,
    adapter_len: usize,
    warp: bool, // software rasterizer (encode sw tier only): decode never leases it
    refs: u32,
};

var shared_lock: Lock = .{};
var shared_dev: ?*SharedDev = null;

// ID3D11Device::GetDeviceRemovedReason is vtable slot 39 (zigd3d's view stops at 28).
const DevRemovedView = extern struct {
    v: *const extern struct {
        _p: [39]VOP,
        GetDeviceRemovedReason: *const fn (*anyopaque) callconv(.winapi) HRESULT,
    },
};

// devLost: the device was removed/reset (TDR, driver update, hung). Open-time check only.
fn devLost(dev: *ID3D11Device) bool {
    const v: *DevRemovedView = @ptrCast(@alignCast(dev));
    return failed(v.v.GetDeviceRemovedReason(@ptrCast(v)));
}

// detachLostLocked (shared_lock held): a lost singleton is unlinked so the next lease builds a fresh
// device; current holders keep their lease and free it on close (their sessions fail and the parent
// re-establishes them - the encode recovery path, now reaching a WORKING device).
fn detachLostLocked() void {
    const sd = shared_dev orelse return;
    if (!devLost(sd.dev)) return;
    trace("device: shared D3D11 device lost (refs={d}) - detached, next session builds a fresh one", .{sd.refs});
    shared_dev = null;
}

// publishSharedLocked (shared_lock held) turns a freshly built Enc-owned device into the singleton.
fn publishSharedLocked(e: *const Enc) ?*SharedDev {
    const sd = std.heap.page_allocator.create(SharedDev) catch return null;
    sd.* = .{
        .dev = e.dev,
        .ctx = e.ctx,
        .vdev = e.vdev,
        .vctx = e.vctx,
        .devmgr = e.devmgr,
        .vendor = e.vendor,
        .res_luid = e.res_luid,
        .adapter_buf = e.adapter_buf,
        .adapter_len = e.adapter_len,
        .warp = e.warp,
        .refs = 1,
    };
    shared_dev = sd;
    return sd;
}

/// releaseShared drops one lease; the last one releases the COM objects and frees the slot.
pub fn releaseShared(sd: *SharedDev) void {
    shared_lock.lock();
    defer shared_lock.unlock();
    if (sd.refs > 1) {
        sd.refs -= 1;
        return;
    }
    release(sd.devmgr);
    release(sd.vctx);
    release(sd.vdev);
    release(sd.ctx);
    release(sd.dev);
    if (shared_dev == sd) shared_dev = null;
    std.heap.page_allocator.destroy(sd);
}

/// leaseChildDevice gives a DECODE session the child's shared hardware device (+ its one device
/// manager), creating it when absent. null = build a private device instead: policy `session`, a
/// different adapter, a WARP singleton, or no hardware video device. Open-time only (takes the lock).
pub fn leaseChildDevice(adapter_luid: i64) ?*SharedDev {
    if (device_policy != .child) return null;
    shared_lock.lock();
    defer shared_lock.unlock();
    detachLostLocked();
    if (shared_dev) |sd| {
        if (sd.warp) return null;
        if (adapter_luid != 0 and sd.res_luid != 0 and adapter_luid != sd.res_luid) return null;
        sd.refs += 1;
        trace("device: decode session reusing the child's shared D3D11 device (refs={d})", .{sd.refs});
        return sd;
    }
    var tmp = Enc{ .gpa = std.heap.page_allocator, .in_w = 0, .in_h = 0, .out_w = 0, .out_h = 0, .fps_n = 0, .fps_d = 1, .dur100 = 0 };
    tmp.createDevice(adapter_luid, false) catch return null;
    if (publishSharedLocked(&tmp)) |sd| return sd;
    release(tmp.devmgr);
    tmp.releaseVideoDevice();
    tmp.releaseDevice();
    return null;
}

pub const AuSink = struct {
    ctx: *anyopaque,
    put: *const fn (ctx: *anyopaque, data: []const u8, pts100: i64, key: bool) void,
};

pub const OpenErr = error{OpenFailed};

// Per-thread open failure detail ("stage hr=0x..."). A failed open deliberately LEAKS its
// partial COM pipeline (unwinding a half-built vendor pipeline is its own crash risk; the
// child process is disposable and open failures are rare + bounded).
threadlocal var open_err_buf: [256]u8 = @splat(0);
threadlocal var open_err_len: usize = 0;

pub fn lastOpenErr() []const u8 {
    return open_err_buf[0..open_err_len];
}

// ── rate control ──
//
// Two session modes, chosen by the parent (mediaLink.bitrateMode):
//   fixed   (qp == 0): the legacy program (post-type, see rc_legacy_fixed) - unchanged behaviour.
//   quality (qp > 0):  constant QUALITY, cap_kbps is only the CEILING. Same contract as the Linux VA
//                      path (internal/pwcapture vaenc.c): encode at the target QP; QP only RISES
//                      above it when the cap binds. Realised as a rate-controlled mode at the cap PLUS
//                      a QP floor (AVEncVideoMinQP = target QP): simple content cannot spend below
//                      the floor's QP so it undershoots; complex content is held at the cap.
// Rung order, first accepted wins. Ranked by (1) cap enforced, (2) cap movable LIVE by the "bitrate"
// op (receiver rate governor), (3) latency; every rung keeps 0 B-frames, no lookahead/multipass.
//   1 CBR + MinQP                - cap = CBR target; NVIDIA retargets it live (fixed mode's history
//                                  + ratectl gate). NVENC adds no filler, so the floor wins.
//   2 LowDelayVBR + MinQP        - peak-bounded via MaxBitRate + VBV, low-latency VBR. Ranked below
//                                  CBR: NVIDIA ignores LIVE Mean/MaxBitRate changes in VBR modes
//                                  (measured: 3000->1500 kbps cut, output unchanged); AMD/Intel unknown.
//   3 PeakConstrainedVBR + MinQP - same caveat, generic VBV; no added frame delay.
//   4 CBR fallback               - every quality rung refused: the legacy fixed program at the cap.
// Excluded: Quality (3) + MaxBitRate - NVIDIA ignores MaxBitRate in Quality mode (measured 9.3 Mbps
// on a 3 Mbps cap) and no API proves another vendor honours it, so it is an UNCAPPED mode;
// UnconstrainedVBR (no peak); GlobalVBR/GlobalLowDelayVBR (two-pass/lookahead = frame delay).
// The ICodecAPI 0..100 AVEncCommonQuality knob therefore goes unused: quality is expressed only as
// the QP floor, the same QP the Linux path holds (medialink.QualityToQP).
// Acceptance = SetValue S_OK on the mode AND every companion knob the rung needs. GetValue is no
// oracle: NVIDIA's MFT reads RateControlMode back as 0 whatever was set (rcprobe, RTX 3060).

/// RcMode is the engaged rate-control rung (shm header telemetry code, mirrored in Go procstats).
pub const RcMode = enum(u8) {
    none = 0, // no ICodecAPI / old child
    cbr = 1, // fixed mode
    cbr_qp = 2,
    lowdelay_vbr_qp = 3,
    peak_vbr_qp = 4,
    cbr_fallback = 5, // quality requested, encoder refused every quality rung

    pub fn label(m: RcMode) []const u8 {
        return switch (m) {
            .none => "none",
            .cbr => "cbr",
            .cbr_qp => "cbr+qp-floor",
            .lowdelay_vbr_qp => "lowdelay-vbr+qp-floor",
            .peak_vbr_qp => "peak-vbr+qp-floor",
            .cbr_fallback => "cbr(quality refused)",
        };
    }
};

// eAVEncCommonRateControlMode (codecapi.h). rc_legacy_fixed: fixed mode has always written 3
// (= _Quality in codecapi.h, commented "CBR" since the C++ shim) AFTER the media types. On NVIDIA
// that post-type write is ignored and the MFT runs CBR from MF_MT_AVG_BITRATE (measured: target
// hit on easy and hard content). Kept as-is so fixed mode does not change; AMD/Intel/software MFTs
// that DO honour a post-type 3 run Quality there today - see .devnotes/ADAPTIVE_BUDGET.md.
const rc_legacy_fixed: u32 = 3;
const rc_cbr: u32 = 0;
const rc_peak_vbr: u32 = 1;
const rc_lowdelay_vbr: u32 = 4;

/// RcReq is one session's rate-control request. qp == 0 = fixed mode.
pub const RcReq = struct {
    cap_kbps: u32 = 8000,
    qp: u8 = 0, // target QP = QP floor (medialink.QualityToQP for H.264)
    quality: u8 = 0, // model quality 1..100 it came from (telemetry only)
};

/// RcCaps is what selection reads from the encoder (IsSupported per knob; listed = the
/// GetParameterValues mode bitmask, null when the MFT does not enumerate - then SetValue decides).
/// enc_qp/quality are diagnostics only (open trace): no rung uses them.
pub const RcCaps = struct {
    mode: bool = false,
    mean: bool = false,
    max: bool = false,
    buffer: bool = false,
    min_qp: bool = false,
    enc_qp: bool = false,
    quality: bool = false,
    listed: ?u8 = null,

    fn lists(c: RcCaps, m: u32) bool {
        const l = c.listed orelse return true;
        return l & (@as(u8, 1) << @intCast(m)) != 0;
    }
};

/// rcLadder lists the rungs worth trying for req on an encoder with caps, best first.
pub fn rcLadder(caps: RcCaps, req: RcReq, out: *[4]RcMode) []RcMode {
    var n: usize = 0;
    if (req.qp == 0) {
        out[0] = .cbr;
        return out[0..1];
    }
    if (caps.mode and caps.min_qp) {
        if (caps.lists(rc_cbr) and caps.mean) {
            out[n] = .cbr_qp;
            n += 1;
        }
        if (caps.lists(rc_lowdelay_vbr) and caps.max) {
            out[n] = .lowdelay_vbr_qp;
            n += 1;
        }
        if (caps.lists(rc_peak_vbr) and caps.max) {
            out[n] = .peak_vbr_qp;
            n += 1;
        }
    }
    out[n] = .cbr_fallback;
    n += 1;
    return out[0..n];
}

fn vU4(x: u64) VARIANT {
    return .{ .vt = VT_UI4, .val = x };
}

/// applyRung programs one rung through ra (any type with `fn set(*const GUID, VARIANT) bool`).
/// false = the encoder refused a knob the rung NEEDS; the caller tries the next rung.
pub fn applyRung(ra: anytype, caps: RcCaps, m: RcMode, req: RcReq) bool {
    const bps: u64 = @as(u64, req.cap_kbps) * 1000;
    switch (m) {
        .none => return false,
        .cbr, .cbr_fallback => {
            // Programmed by the legacy post-type block (configureEncoder); here only undo a refused
            // rung's QP floor so the fallback really is today's CBR.
            if (caps.min_qp) _ = ra.set(&CODECAPI_AVEncVideoMinQP, vU4(0));
            return true;
        },
        .cbr_qp, .lowdelay_vbr_qp, .peak_vbr_qp => {
            const mode: u32 = switch (m) {
                .lowdelay_vbr_qp => rc_lowdelay_vbr,
                .peak_vbr_qp => rc_peak_vbr,
                else => rc_cbr,
            };
            if (!ra.set(&CODECAPI_AVEncCommonRateControlMode, vU4(mode))) return false;
            const mean_ok = caps.mean and ra.set(&CODECAPI_AVEncCommonMeanBitRate, vU4(bps));
            if (m == .cbr_qp and !mean_ok) return false; // CBR's target IS the cap
            if (m != .cbr_qp and !ra.set(&CODECAPI_AVEncCommonMaxBitRate, vU4(bps))) return false;
            // VBV = 1 s at the cap (BufferSize is in BYTES: bits here exceed the level CPB and NVENC
            // fails SetOutputType with MF_E_UNSUPPORTED_D3D_TYPE). Bounds a keyframe burst.
            if (caps.buffer) _ = ra.set(&CODECAPI_AVEncCommonBufferSize, vU4(bps / 8));
            return ra.set(&CODECAPI_AVEncVideoMinQP, vU4(req.qp));
        },
    }
}

/// retargetRung moves the cap of an ENGAGED rung live (the "bitrate" op): the CBR target (fixed
/// mode, CBR+floor), or mean + peak (VBR rungs). Mode and QP floor are untouched.
pub fn retargetRung(ra: anytype, caps: RcCaps, m: RcMode, kbps: u32) bool {
    const bps: u64 = @as(u64, kbps) * 1000;
    return switch (m) {
        .none => false,
        .cbr, .cbr_fallback => ra.set(&CODECAPI_AVEncCommonMeanBitRate, vU4(bps)),
        .cbr_qp, .lowdelay_vbr_qp, .peak_vbr_qp => blk: {
            var ok = ra.set(&CODECAPI_AVEncCommonMeanBitRate, vU4(bps));
            if (m != .cbr_qp) ok = ra.set(&CODECAPI_AVEncCommonMaxBitRate, vU4(bps)) and ok;
            if (caps.buffer) _ = ra.set(&CODECAPI_AVEncCommonBufferSize, vU4(bps / 8));
            break :blk ok;
        },
    };
}

/// pickRung walks the ladder (or [force, fallback]) and returns the first rung ra accepts. The CBR
/// fallback always "accepts": the legacy post-type program cannot be refused, only ignored.
pub fn pickRung(ra: anytype, caps: RcCaps, req: RcReq, force: ?RcMode) RcMode {
    var buf: [4]RcMode = undefined;
    var ladder = rcLadder(caps, req, &buf);
    if (force) |f| if (req.qp > 0) {
        const one = f == .cbr_fallback or f == .cbr or f == .none;
        buf[0] = if (one) .cbr_fallback else f;
        buf[1] = .cbr_fallback;
        ladder = buf[0..if (one) 1 else 2];
    };
    for (ladder) |m| {
        if (applyRung(ra, caps, m, req)) return m;
        trace("rate control: {s} refused", .{m.label()});
    }
    return .cbr_fallback; // unreachable: the ladder always ends in the fallback
}

// FakeRc is a scripted encoder for the selection tests: AMD/Intel MFTs are not on the dev rig, so
// their plausible capability tables are exercised here. refuse_mode = RateControlMode values whose
// SetValue fails; the log records every accepted (key, value) in order.
const FakeRc = struct {
    refuse_mode: u8 = 0,
    refuse_max: bool = false,
    refuse_mean: bool = false,
    refuse_minqp: bool = false,
    n: usize = 0,
    keys: [16]*const GUID = undefined,
    vals: [16]u64 = undefined,

    fn set(f: *FakeRc, key: *const GUID, v: VARIANT) bool {
        if (key == &CODECAPI_AVEncCommonRateControlMode and f.refuse_mode & (@as(u8, 1) << @intCast(v.val)) != 0) return false;
        if (key == &CODECAPI_AVEncCommonMaxBitRate and f.refuse_max) return false;
        if (key == &CODECAPI_AVEncCommonMeanBitRate and f.refuse_mean) return false;
        if (key == &CODECAPI_AVEncVideoMinQP and f.refuse_minqp) return false;
        f.keys[f.n] = key;
        f.vals[f.n] = v.val;
        f.n += 1;
        return true;
    }
    // last returns the value most recently accepted for key (null = never set).
    fn last(f: *const FakeRc, key: *const GUID) ?u64 {
        var i = f.n;
        while (i > 0) : (i -= 1) if (f.keys[i - 1] == key) return f.vals[i - 1];
        return null;
    }
};

const caps_all = RcCaps{ .mode = true, .mean = true, .max = true, .buffer = true, .min_qp = true, .enc_qp = true, .quality = true };
const req_q = RcReq{ .cap_kbps = 3000, .qp = 23, .quality = 70 };

test "rc: fixed mode never leaves the legacy program" {
    var buf: [4]RcMode = undefined;
    try std.testing.expectEqualSlices(RcMode, &.{.cbr}, rcLadder(caps_all, .{ .cap_kbps = 8000 }, &buf));
}

test "rc: NVIDIA-shaped caps (all knobs, no mode list) engage CBR + QP floor at the cap" {
    var f = FakeRc{};
    try std.testing.expectEqual(RcMode.cbr_qp, pickRung(&f, caps_all, req_q, null));
    try std.testing.expectEqual(@as(?u64, rc_cbr), f.last(&CODECAPI_AVEncCommonRateControlMode));
    try std.testing.expectEqual(@as(?u64, 3_000_000), f.last(&CODECAPI_AVEncCommonMeanBitRate));
    try std.testing.expectEqual(@as(?u64, 375_000), f.last(&CODECAPI_AVEncCommonBufferSize)); // bytes
    try std.testing.expectEqual(@as(?u64, 23), f.last(&CODECAPI_AVEncVideoMinQP));
    try std.testing.expectEqual(@as(?u64, null), f.last(&CODECAPI_AVEncCommonMaxBitRate));
}

test "rc: an MFT that lists only VBR modes gets low-delay VBR with a peak" {
    var c = caps_all;
    c.listed = (1 << rc_peak_vbr) | (1 << rc_lowdelay_vbr);
    var buf: [4]RcMode = undefined;
    try std.testing.expectEqualSlices(RcMode, &.{ .lowdelay_vbr_qp, .peak_vbr_qp, .cbr_fallback }, rcLadder(c, req_q, &buf));
    var f = FakeRc{};
    try std.testing.expectEqual(RcMode.lowdelay_vbr_qp, pickRung(&f, c, req_q, null));
    try std.testing.expectEqual(@as(?u64, 3_000_000), f.last(&CODECAPI_AVEncCommonMaxBitRate));
    try std.testing.expectEqual(@as(?u64, 23), f.last(&CODECAPI_AVEncVideoMinQP));
}

test "rc: SetValue refusals walk the ladder in order" {
    var f = FakeRc{ .refuse_mode = (1 << rc_cbr) | (1 << rc_lowdelay_vbr) };
    try std.testing.expectEqual(RcMode.peak_vbr_qp, pickRung(&f, caps_all, req_q, null));
    try std.testing.expectEqual(@as(?u64, rc_peak_vbr), f.last(&CODECAPI_AVEncCommonRateControlMode));
    // CBR accepted but its target (MeanBitRate) refused: CBR+floor has no cap, so VBR wins.
    var f2 = FakeRc{ .refuse_mean = true };
    try std.testing.expectEqual(RcMode.lowdelay_vbr_qp, pickRung(&f2, caps_all, req_q, null));
}

test "rc: no QP floor or no peak = CBR fallback, never an uncapped mode" {
    var c = caps_all;
    c.min_qp = false; // Intel-shaped: no AVEncVideoMinQP
    var buf: [4]RcMode = undefined;
    try std.testing.expectEqualSlices(RcMode, &.{.cbr_fallback}, rcLadder(c, req_q, &buf));
    var f = FakeRc{};
    try std.testing.expectEqual(RcMode.cbr_fallback, pickRung(&f, c, req_q, null));
    try std.testing.expectEqual(@as(?u64, null), f.last(&CODECAPI_AVEncCommonRateControlMode)); // legacy block programs it
    // Every quality rung refused late (MinQP): the fallback undoes the floor a refused rung left.
    var f2 = FakeRc{ .refuse_mode = 1 << rc_cbr, .refuse_max = true };
    try std.testing.expectEqual(RcMode.cbr_fallback, pickRung(&f2, caps_all, req_q, null));
    try std.testing.expectEqual(@as(?u64, 0), f2.last(&CODECAPI_AVEncVideoMinQP));
    var f3 = FakeRc{};
    try std.testing.expectEqual(RcMode.cbr_fallback, pickRung(&f3, .{}, req_q, null)); // no ICodecAPI knobs at all
}

test "rc: live retarget moves the cap the engaged rung enforces" {
    var f = FakeRc{};
    try std.testing.expect(retargetRung(&f, caps_all, .cbr_qp, 1500));
    try std.testing.expectEqual(@as(?u64, 1_500_000), f.last(&CODECAPI_AVEncCommonMeanBitRate));
    try std.testing.expectEqual(@as(?u64, null), f.last(&CODECAPI_AVEncCommonMaxBitRate));
    var f2 = FakeRc{};
    try std.testing.expect(retargetRung(&f2, caps_all, .lowdelay_vbr_qp, 1500));
    try std.testing.expectEqual(@as(?u64, 1_500_000), f2.last(&CODECAPI_AVEncCommonMaxBitRate));
    var f3 = FakeRc{};
    try std.testing.expect(retargetRung(&f3, caps_all, .cbr, 1500)); // fixed: legacy MeanBitRate only
    try std.testing.expectEqual(@as(usize, 1), f3.n);
}

test "rc: forced rung (test hook) tries only that rung, then the fallback" {
    var f = FakeRc{ .refuse_mode = 1 << rc_peak_vbr };
    try std.testing.expectEqual(RcMode.cbr_fallback, pickRung(&f, caps_all, req_q, .peak_vbr_qp));
    var f2 = FakeRc{};
    try std.testing.expectEqual(RcMode.peak_vbr_qp, pickRung(&f2, caps_all, req_q, .peak_vbr_qp));
}

test "rc: telemetry codes are the wire contract (mirrored in internal/mfenc procstats.go rcLabels)" {
    try std.testing.expectEqual(@as(u8, 1), @intFromEnum(RcMode.cbr));
    try std.testing.expectEqual(@as(u8, 2), @intFromEnum(RcMode.cbr_qp));
    try std.testing.expectEqual(@as(u8, 5), @intFromEnum(RcMode.cbr_fallback));
    try std.testing.expectEqualStrings("cbr+qp-floor", RcMode.cbr_qp.label());
}

/// rc_force (RAVE_MATE_MFENC_RC test hook): try ONLY this rung for quality sessions (then the CBR
/// fallback), so a test can pin a rung or prove the fallback on silicon that accepts everything.
pub var rc_force: ?RcMode = null;

// CapiRc adapts a live ICodecAPI to applyRung/retargetRung.
const CapiRc = struct {
    c: *ICodecAPI,
    fn set(a: CapiRc, key: *const GUID, v: VARIANT) bool {
        return !failed(a.c.v.SetValue(@ptrCast(a.c), key, &v));
    }
};

fn probeRcCaps(c: *ICodecAPI) RcCaps {
    const sup = struct {
        fn f(cc: *ICodecAPI, key: *const GUID) bool {
            return cc.v.IsSupported(@ptrCast(cc), key) == 0; // S_OK only (S_FALSE = no)
        }
    }.f;
    var caps = RcCaps{
        .mode = sup(c, &CODECAPI_AVEncCommonRateControlMode),
        .mean = sup(c, &CODECAPI_AVEncCommonMeanBitRate),
        .max = sup(c, &CODECAPI_AVEncCommonMaxBitRate),
        .buffer = sup(c, &CODECAPI_AVEncCommonBufferSize),
        .min_qp = sup(c, &CODECAPI_AVEncVideoMinQP),
        .enc_qp = sup(c, &CODECAPI_AVEncVideoEncodeQP),
        .quality = sup(c, &CODECAPI_AVEncCommonQuality),
    };
    var vals: ?[*]VARIANT = null;
    var nv: u32 = 0;
    if (!failed(c.v.GetParameterValues(@ptrCast(c), &CODECAPI_AVEncCommonRateControlMode, &vals, &nv)) and vals != null) {
        var mask: u8 = 0;
        for (vals.?[0..nv]) |v| { // VT_UI4 entries: no VariantClear needed
            const m: u32 = @truncate(v.val);
            if (m < 8) mask |= @as(u8, 1) << @intCast(m);
        }
        CoTaskMemFree(@ptrCast(vals));
        caps.listed = mask;
    }
    return caps;
}

/// Enc is one hardware encode pipeline (one session). All calls from ONE thread.
pub const Enc = struct {
    gpa: std.mem.Allocator,
    dev: *ID3D11Device = undefined,
    ctx: *ID3D11DeviceContext = undefined,
    devmgr: *IMFDXGIDeviceManager = undefined,
    vdev: *ID3D11VideoDevice = undefined,
    vctx: *ID3D11VideoContext = undefined,
    vpe: *anyopaque = undefined,
    vproc: *anyopaque = undefined,
    in_view: *anyopaque = undefined,
    in_tex: *anyopaque = undefined,
    nv_tex: [NVPOOL_MAX]?*anyopaque = @splat(null),
    nv_view: [NVPOOL_MAX]?*anyopaque = @splat(null),
    nv_sample: [NVPOOL_MAX]?*IMFSample = @splat(null),
    nv_idx: usize = 0,
    nv_n: usize = 0, // live pool depth (NVPOOL_INIT..NVPOOL_MAX)
    pool_grows: u32 = 0, // on-demand growth events this session (telemetry)
    enc: *IMFTransform = undefined,
    // act is the ACTIVATE the bound MFT came from, kept alive so close() can ShutdownObject it:
    // a hardware MFT created through IMFActivate is torn down by its activate, not by Release
    // alone (vendor MFTs keep driver-side state alive otherwise).
    act: ?*IMFActivate = null,
    evgen: ?*IMFMediaEventGenerator = null,
    capi: ?*ICodecAPI = null,
    // is_async comes from MF_TRANSFORM_ASYNC, never from a successful QI: a sync MFT that also
    // exposes IMFMediaEventGenerator must be driven SYNC or every submit burns FEED_WAIT_MS.
    is_async: bool = false,
    // sw = the software MF encoder tier: no device manager, system-memory NV12 input. The VP
    // still does CSC + scale on whatever device we have (hardware or WARP).
    sw: bool = false,
    sw_stage_tex: ?*anyopaque = null, // STAGING NV12 for the GPU→host readback (sw tier only)
    sw_sample: [NVPOOL_MAX]?*IMFSample = @splat(null), // system-memory NV12 samples (sw tier only)
    sw_buf: [NVPOOL_MAX]?*IMFMediaBuffer = @splat(null),
    stage_sink: StageSink = null,
    swz: ?[]u8 = null, // BGRA swizzle scratch when the VP rejects RGBA input
    in_w: i32,
    in_h: i32,
    out_w: i32,
    out_h: i32,
    fps_n: i32,
    fps_d: i32,
    dur100: i64,
    // Live-reconfigure (resolution cap) inputs, captured at open so reconfigureOutput can rebuild the
    // encoder MFT + NV12 pool at a new coded size on the SAME device + video processor. kbps/gop are
    // the configured rate; allow_sw/force_sw pin the tier the rebuild uses (software stays software).
    kbps_cfg: i32 = 0,
    gop_cfg: i32 = 0,
    // Rate control: the request (cap follows the live "bitrate" op, so a rebuild keeps the current
    // cap), the probed caps and the rung engaged. rc_sink = shm header 248 (mode|qp<<8|quality<<16)
    // + 252 (cap kbps), null without a session shm.
    rc_req: RcReq = .{},
    rc_caps: RcCaps = .{},
    rc_mode: RcMode = .none,
    rc_sink: ?*volatile [2]u32 = null,
    allow_sw_cfg: bool = false,
    force_sw_cfg: bool = false,
    has_enc: bool = false, // e.enc holds a bound MFT: guards releaseEncoder/close after a teardown
    bgra_in: bool = true,
    zero_copy: bool = false, // pixels come from a foreign shared texture: no in_tex/in_view/swz
    enc_provides: bool = false,
    enc_out_size: u32 = 0,
    need_input: i32 = 0,
    fed_n: u64 = 0,
    out_n: u64 = 0,
    drain_done: bool = false,
    force_idr: bool = false,
    name_buf: [128]u8 = @splat(0),
    name_len: usize = 0,
    // Resolved adapter, reported back to the parent. A CONFIGURED LUID that no longer exists
    // (LUIDs are not stable across reboots / driver resets / a Parsec virtual display appearing)
    // silently degraded to the default adapter before - so the log named an adapter the pipeline
    // was not on. The parent now compares requested vs resolved and warns once.
    res_luid: i64 = 0,
    adapter_buf: [128]u8 = @splat(0),
    adapter_len: usize = 0,
    vendor: u32 = 0,
    // owns_device: this Enc created the device/manager and must release them. false = the device
    // is the child's shared singleton (shared_ref) and close() only drops that lease.
    owns_device: bool = true,
    shared_ref: ?*SharedDev = null,
    warp: bool = false,

    pub fn name(e: *const Enc) []const u8 {
        return e.name_buf[0..e.name_len];
    }

    /// st latches the stage about to run. One relaxed store - safe to call on every frame.
    pub fn st(e: *Enc, s: Stage) void {
        if (e.stage_sink) |p| @atomicStore(u32, @volatileCast(p), @intFromEnum(s), .monotonic);
    }

    /// bindStage points the breadcrumb at the parent-mapped shm word (called right after open).
    pub fn bindStage(e: *Enc, p: StageSink) void {
        e.stage_sink = p;
        e.st(.open_done);
    }

    /// isAsync reports the resolved drive mode (MF_TRANSFORM_ASYNC), for the opened event.
    pub fn isAsync(e: *const Enc) bool {
        return e.is_async;
    }

    /// isSoftware reports whether the software MF encoder tier is serving this session.
    pub fn isSoftware(e: *const Enc) bool {
        return e.sw;
    }

    /// devShared reports whether this session adopted the CHILD's shared device rather than
    /// building its own. Reported per open so a passing run proves WHICH code path passed.
    pub fn devShared(e: *const Enc) bool {
        return !e.owns_device;
    }

    /// resolvedLUID is the adapter the pipeline ACTUALLY runs on (0 = WARP / no DXGI adapter).
    pub fn resolvedLUID(e: *const Enc) i64 {
        return e.res_luid;
    }

    /// adapterName is the resolved adapter's DXGI description ("" = WARP / unknown).
    pub fn adapterName(e: *const Enc) []const u8 {
        return e.adapter_buf[0..e.adapter_len];
    }

    // noteAdapter records the resolved adapter identity for the opened event.
    fn noteAdapter(e: *Enc, ad: ?*IDXGIAdapter1) void {
        e.res_luid = 0;
        e.adapter_len = 0;
        const a = ad orelse return;
        var d: DXGI_ADAPTER_DESC1 = undefined;
        if (failed(a.v.GetDesc1(@ptrCast(a), &d))) return;
        e.res_luid = (@as(i64, d.AdapterLuid.high) << 32) | @as(i64, @as(u32, @bitCast(d.AdapterLuid.low)));
        for (d.Description) |c| {
            if (c == 0 or e.adapter_len >= e.adapter_buf.len) break;
            e.adapter_buf[e.adapter_len] = if (c < 128) @intCast(c) else '?';
            e.adapter_len += 1;
        }
    }

    fn setErr(e: *Enc, stage: []const u8, hr: HRESULT) OpenErr {
        _ = e;
        const s = std.fmt.bufPrint(&open_err_buf, "{s} hr=0x{x:0>8}", .{ stage, @as(u32, @bitCast(hr)) }) catch open_err_buf[0..0];
        open_err_len = s.len;
        return error.OpenFailed;
    }

    // findAdapter: LUID (high<<32|low) → adapter + vendor id; null = not found.
    pub fn findAdapter(luid: i64, vendor: *u32) ?*IDXGIAdapter1 {
        vendor.* = 0;
        if (luid == 0) return null;
        var raw: ?*anyopaque = null;
        if (failed(CreateDXGIFactory1(&IID_IDXGIFactory1, &raw)) or raw == null) return null;
        const fac: *IDXGIFactory1 = @ptrCast(@alignCast(raw.?));
        defer release(fac);
        var i: u32 = 0;
        while (true) : (i += 1) {
            var ad: ?*IDXGIAdapter1 = null;
            if (fac.v.EnumAdapters1(@ptrCast(fac), i, &ad) != 0 or ad == null) break;
            var d: DXGI_ADAPTER_DESC1 = undefined;
            if (!failed(ad.?.v.GetDesc1(@ptrCast(ad.?), &d))) {
                const key = (@as(i64, d.AdapterLuid.high) << 32) | @as(i64, @as(u32, @bitCast(d.AdapterLuid.low)));
                if (key == luid) {
                    vendor.* = d.VendorId;
                    return ad;
                }
            }
            release(ad.?);
        }
        return null;
    }

    // pickDefaultAdapter: luid==0 binds a DELIBERATE default - known encode vendor first,
    // then any non-software adapter - never blind adapter 0 (Parsec-class virtual displays).
    pub fn pickDefaultAdapter(vendor: *u32) ?*IDXGIAdapter1 {
        vendor.* = 0;
        var raw: ?*anyopaque = null;
        if (failed(CreateDXGIFactory1(&IID_IDXGIFactory1, &raw)) or raw == null) return null;
        const fac: *IDXGIFactory1 = @ptrCast(@alignCast(raw.?));
        defer release(fac);
        var pass: u32 = 0;
        while (pass < 2) : (pass += 1) {
            var i: u32 = 0;
            while (true) : (i += 1) {
                var ad: ?*IDXGIAdapter1 = null;
                if (fac.v.EnumAdapters1(@ptrCast(fac), i, &ad) != 0 or ad == null) break;
                var d: DXGI_ADAPTER_DESC1 = undefined;
                if (!failed(ad.?.v.GetDesc1(@ptrCast(ad.?), &d)) and
                    d.Flags & DXGI_ADAPTER_FLAG_SOFTWARE == 0 and
                    (pass == 1 or vendorTag(d.VendorId) != null))
                {
                    vendor.* = d.VendorId;
                    return ad;
                }
                release(ad.?);
            }
        }
        return null;
    }

    // openDevice: device + multithread protection + video interfaces + VP enumerator for the
    // geometry. The VP enumerator doubles as the video-capability gate BEFORE any MFT sees
    // the device manager.
    fn openDevice(e: *Enc, adapter: ?*IDXGIAdapter1) OpenErr!void {
        return e.openDeviceOn(adapter, false);
    }

    // openDeviceOn: warp=true creates the software rasterizer device (last rung of the software
    // encode tier - a box with no usable hardware video device still gets CSC + scale).
    // Leaves the PER-GEOMETRY VP enumerator to createEnumerator: the device is shareable across
    // sessions, the enumerator is not (it is built from this session's content desc).
    fn openDeviceOn(e: *Enc, adapter: ?*IDXGIAdapter1, warp: bool) OpenErr!void {
        e.st(.create_device);
        trace("D3D11CreateDevice({s})", .{if (warp) "WARP" else if (adapter != null) "pinned adapter" else "default"});
        const driver: u32 = if (warp) D3D_DRIVER_TYPE_WARP else if (adapter != null) D3D_DRIVER_TYPE_UNKNOWN else D3D_DRIVER_TYPE_HARDWARE;
        const ad: ?*IDXGIAdapter1 = if (warp) null else adapter; // WARP forbids an adapter pointer
        var dev: ?*ID3D11Device = null;
        var dctx: ?*ID3D11DeviceContext = null;
        var hr = D3D11CreateDevice(@ptrCast(ad), driver, null, D3D11_CREATE_DEVICE_BGRA_SUPPORT | D3D11_CREATE_DEVICE_VIDEO_SUPPORT, null, 0, D3D11_SDK_VERSION, &dev, null, &dctx);
        if (failed(hr) or dev == null or dctx == null) return e.setErr("D3D11CreateDevice", hr);
        e.dev = dev.?;
        e.ctx = dctx.?;
        e.warp = warp;
        // Multithread protection BEFORE the device manager: MFT worker threads share this
        // device; without it they race the immediate context (audit fix, kept from shim).
        var mtraw: ?*anyopaque = null;
        if (!failed(qi(e.dev, &IID_ID3D10Multithread, &mtraw)) and mtraw != null) {
            const mt10: *ID3D10Multithread = @ptrCast(@alignCast(mtraw.?));
            _ = mt10.v.SetMultithreadProtected(@ptrCast(mt10), 1);
            release(mt10);
        }
        trace("QI ID3D11VideoDevice/Context", .{});
        var vraw: ?*anyopaque = null;
        hr = qi(e.dev, &IID_ID3D11VideoDevice, &vraw);
        if (failed(hr) or vraw == null) {
            e.releaseDevice();
            return e.setErr("QI ID3D11VideoDevice", hr);
        }
        e.vdev = @ptrCast(@alignCast(vraw.?));
        vraw = null;
        hr = qi(e.ctx, &IID_ID3D11VideoContext, &vraw);
        if (failed(hr) or vraw == null) {
            release(e.vdev);
            e.releaseDevice();
            return e.setErr("QI ID3D11VideoContext", hr);
        }
        e.vctx = @ptrCast(@alignCast(vraw.?));
        // Device manager here, not in open(): it belongs to the DEVICE, so a shared device carries
        // exactly one manager. Two managers on one adapter is the shape AMD's MFT rejects.
        e.st(.device_manager);
        var token: u32 = 0;
        var dm: ?*IMFDXGIDeviceManager = null;
        hr = MFCreateDXGIDeviceManager(&token, &dm);
        if (failed(hr) or dm == null) {
            e.releaseVideoDevice();
            e.releaseDevice();
            return e.setErr("MFCreateDXGIDeviceManager", hr);
        }
        e.devmgr = dm.?;
        hr = e.devmgr.v.ResetDevice(@ptrCast(e.devmgr), @ptrCast(e.dev), token);
        if (failed(hr)) {
            release(e.devmgr);
            e.releaseVideoDevice();
            e.releaseDevice();
            return e.setErr("ResetDevice", hr);
        }
    }

    // createEnumerator builds this session's PER-GEOMETRY video-processor enumerator. It doubles
    // as the video-capability gate: an adapter that cannot host the route's geometry is rejected
    // BEFORE any MFT is offered the device manager (4K60 crash-audit fix).
    fn createEnumerator(e: *Enc) OpenErr!void {
        const cd = D3D11_VIDEO_PROCESSOR_CONTENT_DESC{
            .InputFrameFormat = 0, // progressive
            .InputFrameRate = .{ .Numerator = @intCast(e.fps_n), .Denominator = @intCast(e.fps_d) },
            .InputWidth = @intCast(e.in_w),
            .InputHeight = @intCast(e.in_h),
            .OutputFrameRate = .{ .Numerator = @intCast(e.fps_n), .Denominator = @intCast(e.fps_d) },
            .OutputWidth = @intCast(e.out_w),
            .OutputHeight = @intCast(e.out_h),
            .Usage = 0, // playback normal
        };
        e.st(.create_vp);
        trace("CreateVideoProcessorEnumerator", .{});
        var vpe: ?*anyopaque = null;
        const hr = e.vdev.v.CreateVideoProcessorEnumerator(@ptrCast(e.vdev), &cd, &vpe);
        if (failed(hr) or vpe == null) return e.setErr("CreateVideoProcessorEnumerator", hr);
        e.vpe = vpe.?;
    }

    fn releaseVideoDevice(e: *Enc) void {
        release(e.vctx);
        release(e.vdev);
    }

    fn releaseDevice(e: *Enc) void {
        release(e.ctx);
        release(e.dev);
    }

    // acquireDevice fills in the device/context/video interfaces/device manager, either from the
    // child-wide singleton (device_policy == .child) or freshly per session. Adapter resolution +
    // the pinned→default→WARP degrade ladder live here so both policies get them identically.
    fn acquireDevice(e: *Enc, adapter_luid: i64, allow_sw: bool) OpenErr!void {
        if (device_policy == .child) {
            shared_lock.lock();
            defer shared_lock.unlock();
            detachLostLocked();
            if (shared_dev) |sd| {
                // The child is per-adapter, so every session in it wants the same GPU. If a
                // session ever asks for a DIFFERENT resolvable adapter, build it its own device
                // instead of silently binding the wrong one: feeding an MFT textures from a
                // foreign device is the access-violation class this whole child exists to contain.
                if (adapter_luid != 0 and sd.res_luid != 0 and adapter_luid != sd.res_luid) {
                    trace("device: session wants adapter 0x{x} but the shared device is on 0x{x} - own device for this session", .{ @as(u64, @bitCast(adapter_luid)), @as(u64, @bitCast(sd.res_luid)) });
                    return e.createDevice(adapter_luid, allow_sw);
                }
                sd.refs += 1;
                e.adoptShared(sd);
                trace("device: reusing the child's shared D3D11 device (refs={d})", .{sd.refs});
                return;
            }
            try e.createDevice(adapter_luid, allow_sw);
            const sd = publishSharedLocked(e) orelse return; // no slot: this Enc keeps owning it
            e.shared_ref = sd;
            e.owns_device = false; // the singleton owns it; close() drops a reference
            return;
        }
        try e.createDevice(adapter_luid, allow_sw);
    }

    fn adoptShared(e: *Enc, sd: *SharedDev) void {
        e.shared_ref = sd;
        e.dev = sd.dev;
        e.ctx = sd.ctx;
        e.vdev = sd.vdev;
        e.vctx = sd.vctx;
        e.devmgr = sd.devmgr;
        e.vendor = sd.vendor;
        e.res_luid = sd.res_luid;
        e.adapter_buf = sd.adapter_buf;
        e.adapter_len = sd.adapter_len;
        e.warp = sd.warp;
        e.owns_device = false;
    }

    // createDevice runs the adapter ladder and builds a device this Enc owns.
    fn createDevice(e: *Enc, adapter_luid: i64, allow_sw: bool) OpenErr!void {
        e.st(.pick_adapter);
        var vendor: u32 = 0;
        var adapter = findAdapter(adapter_luid, &vendor);
        const luid_resolved = adapter != null;
        if (adapter == null) adapter = pickDefaultAdapter(&vendor);
        if (adapter_luid != 0 and !luid_resolved) {
            // A configured LUID that no longer exists. LUIDs are NOT stable across reboots,
            // driver resets or a virtual display appearing, so a stale config silently ran the
            // pipeline on a different adapter than every log line claimed.
            trace("requested adapter luid 0x{x} not present - using the default adapter", .{@as(u64, @bitCast(adapter_luid))});
        }
        e.noteAdapter(adapter);
        e.openDevice(adapter) catch {
            if (adapter) |a| { // chosen adapter cannot host the pipeline: degrade to default
                release(a);
                adapter = null;
                vendor = 0;
                e.noteAdapter(null);
                e.openDevice(null) catch {
                    // No hardware adapter can host a video device (headless / virtual-display-only
                    // rig). WARP still gives us the video processor, so the software encode tier
                    // remains reachable - that is the rung that must never be missing.
                    if (!allow_sw) return error.OpenFailed;
                    trace("no hardware video device - falling back to WARP for CSC/scale", .{});
                    try e.openDeviceOn(null, true);
                };
            } else return error.OpenFailed;
        };
        if (adapter) |a| release(a);
        e.vendor = vendor;
        e.owns_device = true;
    }

    // activateName reads an activate's MFT_FRIENDLY_NAME into buf as ASCII; returns the slice.
    fn activateName(a: *IMFActivate, buf: []u8) []const u8 {
        var wname: [128]u16 = @splat(0);
        var len: usize = 0;
        const av: *const AttrVtbl = @ptrCast(@alignCast(a.v));
        if (failed(av.GetString(@ptrCast(a), &MFT_FRIENDLY_NAME, &wname, 127, null))) return buf[0..0];
        for (wname) |c| {
            if (c == 0 or len >= buf.len) break;
            buf[len] = if (c < 128) @intCast(c) else '?';
            len += 1;
        }
        return buf[0..len];
    }

    // resolveDrive fixes the drive mode from the MFT's OWN attributes and unlocks async mode.
    // Returns false when the MFT must be rejected (async but refuses to unlock - there is no
    // legal sync drive of an async MFT, and driving one sync is the documented E_UNEXPECTED
    // storm). Also reads MF_SA_D3D11_AWARE, which the caller uses as the device-manager gate.
    fn resolveDrive(e: *Enc, t: *IMFTransform, aware: *u32) bool {
        aware.* = 0;
        e.is_async = false;
        var attrs: ?*IMFAttributes = null;
        if (failed(t.v.GetAttributes(@ptrCast(t), &attrs)) or attrs == null) return true; // no store: sync
        defer release(attrs.?);
        aware.* = attrGetU32(attrs.?, &MF_SA_D3D11_AWARE) orelse 0;
        attrSetU32(attrs.?, &MF_LOW_LATENCY, 1);
        const async_flag = attrGetU32(attrs.?, &MF_TRANSFORM_ASYNC) orelse 0;
        if (async_flag == 0) return true; // SYNC MFT: never unlock, never take an event generator
        attrSetU32(attrs.?, &MF_TRANSFORM_ASYNC_UNLOCK, 1);
        // Verify the unlock TOOK: an MFT that keeps ASYNC_UNLOCK at 0 rejects every ProcessInput
        // with E_NOTACCEPTING forever, which used to present as a 2 s stall per frame.
        if ((attrGetU32(attrs.?, &MF_TRANSFORM_ASYNC_UNLOCK) orelse 0) == 0) return false;
        e.is_async = true;
        return true;
    }

    // bindTier is one MFTEnumEx sweep. enum_flags picks the tier (hardware vs software); when
    // want_d3d is false the MFT is bound WITHOUT a device manager (software tier: system-memory
    // NV12 input), which is why MF_SA_D3D11_AWARE is only a gate on the hardware tiers.
    fn bindTier(e: *Enc, enum_flags: u32, device_tag: ?[]const u8, want_d3d: bool) bool {
        e.st(.enum_mft);
        const ti = MFT_REGISTER_TYPE_INFO{ .major = MFMediaType_Video, .sub = MFVideoFormat_NV12 };
        const to = MFT_REGISTER_TYPE_INFO{ .major = MFMediaType_Video, .sub = MFVideoFormat_H264 };
        var acts: ?[*]*IMFActivate = null;
        var n: u32 = 0;
        if (failed(MFTEnumEx(MFT_CATEGORY_VIDEO_ENCODER, enum_flags | MFT_ENUM_FLAG_SORTANDFILTER, &ti, &to, &acts, &n)) or n == 0 or acts == null) return false;
        const list = acts.?[0..n];
        var bound: ?*IMFActivate = null;
        defer { // every activate we did NOT bind is released; the bound one is kept for close()
            for (list) |a| {
                if (bound != a) release(a);
            }
            CoTaskMemFree(@ptrCast(acts));
        }
        var pass: u32 = 0;
        while (pass < 2) : (pass += 1) {
            if (pass == 0 and device_tag == null) continue; // no vendor hint: one unfiltered pass
            for (list) |a| {
                var fname: [128]u8 = @splat(0);
                const fn_name = activateName(a, &fname);
                if (pass == 0 and !containsNoCase(fn_name, device_tag.?)) continue;
                if (want_d3d and vendorMismatch(fn_name, device_tag)) continue;
                e.st(.activate_mft);
                var raw: ?*anyopaque = null;
                if (failed(a.v.ActivateObject(@ptrCast(a), &IID_IMFTransform, &raw)) or raw == null) continue;
                const t: *IMFTransform = @ptrCast(@alignCast(raw.?));
                var aware: u32 = 0;
                const ok_drive = e.resolveDrive(t, &aware);
                if (!ok_drive or (want_d3d and aware == 0)) {
                    release(t);
                    _ = a.v.ShutdownObject(@ptrCast(a));
                    continue;
                }
                if (want_d3d) {
                    e.st(.set_d3d_manager);
                    if (failed(t.v.ProcessMessage(@ptrCast(t), MFT_MESSAGE_SET_D3D_MANAGER, @intFromPtr(e.devmgr)))) {
                        release(t);
                        _ = a.v.ShutdownObject(@ptrCast(a));
                        continue;
                    }
                }
                e.enc = t;
                e.act = a;
                e.has_enc = true;
                bound = a;
                @memcpy(e.name_buf[0..fn_name.len], fn_name);
                e.name_len = fn_name.len;
                e.sw = !want_d3d;
                trace("bound {s} drive={s} tier={s} aware={d}", .{ fn_name, if (e.is_async) "async" else "sync", if (want_d3d) "hw" else "sw", aware });
                return true;
            }
        }
        return false;
    }

    // bindEncoder walks the vendor-portability ladder, best first, and NEVER assumes a drive
    // mode (see resolveDrive). Rungs:
    //   1. hardware MFT whose friendly name carries this adapter's vendor tag
    //   2. any hardware MFT that accepts THIS device's manager (cross-vendor handoff still banned)
    //   3. the software MF H.264 encoder - no device manager, system-memory NV12 in
    // Rung 3 is a first-class tier, not a footnote: it is what a box with no usable hardware MFT
    // (or a poisoned adapter) encodes on, and after the zero-copy flip the child is load-bearing
    // for ALL capture, so "no hardware encoder" must not mean "no video".
    fn bindEncoder(e: *Enc, device_tag: ?[]const u8, allow_sw: bool, force_sw: bool) OpenErr!void {
        trace("MFTEnumEx(bind: hw tiers then sw)", .{});
        if (!force_sw and e.bindTier(MFT_ENUM_FLAG_HARDWARE, device_tag, true)) return;
        if (!allow_sw) return e.setErr("MFTEnumEx(no usable hw encoder for this device)", E_FAIL);
        // ASYNCMFT|SYNCMFT|LOCALMFT without HARDWARE = the software encoders (Microsoft's H.264
        // MFT and any locally registered one). No vendor filter: there is no adapter to match.
        if (e.bindTier(MFT_ENUM_FLAG_SYNCMFT | MFT_ENUM_FLAG_ASYNCMFT | MFT_ENUM_FLAG_LOCALMFT, null, false)) return;
        return e.setErr("MFTEnumEx(no hw and no sw H.264 encoder)", E_FAIL);
    }

    /// open builds one encode pipeline. zerocopy = the pixels arrive as a foreign shared
    /// texture (cap.zig owns the VP input view), so NO own input texture and no swizzle
    /// scratch are created - that is where the 33 MB VRAM + the host frame plane go.
    // configureEncoder binds a tier and configures the MFT end to end (types, rate control,
    // stream info, drive mode). Separated from open() so the SOFTWARE tier can be retried when a
    // hardware MFT binds but cannot be configured - see the call site.
    fn configureEncoder(e: *Enc, out_w: i32, out_h: i32, fps_n: i32, kbps_in: i32, gop: i32, allow_sw: bool, force_sw: bool) OpenErr!void {
        try e.bindEncoder(vendorTag(e.vendor), allow_sw, force_sw);

        // output type: H.264 CBR Main + EXPLICIT level (4K60 = 5.2)
        const out_mt = mtVideo(&MFVideoFormat_H264, out_w, out_h, fps_n, e.fps_d) orelse return e.setErr("mtVideo(out)", E_FAIL);
        const kbps: u32 = if (kbps_in > 0) @intCast(kbps_in) else 8000;
        e.rc_req.cap_kbps = kbps;
        e.rc_mode = .none;
        // Quality mode programs its rate control BEFORE the output type: NVIDIA's MFT fixes its rate
        // control at SetOutputType and ignores later mode/MinQP/BufferSize (measured, RTX 3060: the
        // same rung applied after the types gave output bit-identical to fixed mode). Fixed mode keeps
        // the legacy post-type block below byte for byte.
        if (e.rc_req.qp > 0) {
            e.st(.codec_api);
            var craw: ?*anyopaque = null;
            if (!failed(qi(e.enc, &IID_ICodecAPI, &craw)) and craw != null) {
                e.capi = @ptrCast(@alignCast(craw.?));
                e.applyRc(e.capi.?);
            }
        }
        attrSetU32(out_mt, &MF_MT_AVG_BITRATE, kbps * 1000);
        attrSetU32(out_mt, &MF_MT_MPEG2_PROFILE, 77); // Main
        attrSetU32(out_mt, &MF_MT_MPEG2_LEVEL, h264LevelFor(out_w, out_h, fps_n, e.fps_d));
        e.st(.set_output_type);
        trace("enc SetOutputType", .{});
        var hr = e.enc.v.SetOutputType(@ptrCast(e.enc), 0, out_mt, 0);
        release(out_mt);
        if (failed(hr)) return e.setErr("enc SetOutputType", hr);

        // input: the MFT's own NV12 candidate, geometry stamped
        e.st(.set_input_type);
        trace("enc input type negotiation", .{});
        var in_mt: ?*IMFMediaType = null;
        var ti: u32 = 0;
        while (true) : (ti += 1) {
            var c: ?*IMFMediaType = null;
            if (failed(e.enc.v.GetInputAvailableType(@ptrCast(e.enc), 0, ti, &c)) or c == null) break;
            const sub = attrGetGUID(c.?, &MF_MT_SUBTYPE);
            if (sub != null and guidEq(sub.?, MFVideoFormat_NV12)) {
                in_mt = c;
                break;
            }
            release(c.?);
        }
        if (in_mt == null) in_mt = mtVideo(&MFVideoFormat_NV12, out_w, out_h, fps_n, e.fps_d);
        if (in_mt == null) return e.setErr("mtVideo(in)", E_FAIL);
        attrSetU64(in_mt.?, &MF_MT_FRAME_SIZE, (@as(u64, @intCast(out_w)) << 32) | @as(u64, @intCast(out_h)));
        attrSetU64(in_mt.?, &MF_MT_FRAME_RATE, (@as(u64, @intCast(fps_n)) << 32) | @as(u64, @intCast(e.fps_d)));
        trace("enc SetInputType", .{});
        hr = e.enc.v.SetInputType(@ptrCast(e.enc), 0, in_mt.?, 0);
        release(in_mt.?);
        if (failed(hr)) return e.setErr("enc SetInputType", hr);

        // rate control / GOP / latency knobs (best-effort)
        e.st(.codec_api);
        trace("ICodecAPI knobs", .{});
        if (e.capi == null) {
            var craw: ?*anyopaque = null;
            if (!failed(qi(e.enc, &IID_ICodecAPI, &craw)) and craw != null) e.capi = @ptrCast(@alignCast(craw.?));
        }
        if (e.capi) |c| {
            var v = VARIANT{ .vt = VT_UI4, .val = 0 };
            if (e.rc_mode == .none or e.rc_mode == .cbr or e.rc_mode == .cbr_fallback) { // fixed / quality refused: legacy
                const fresh = e.rc_mode == .none; // not already traced by applyRc
                if (fresh) e.rc_mode = if (e.rc_req.qp > 0) .cbr_fallback else .cbr;
                v.val = rc_legacy_fixed;
                _ = c.v.SetValue(@ptrCast(c), &CODECAPI_AVEncCommonRateControlMode, &v);
                v.val = @as(u64, kbps) * 1000;
                _ = c.v.SetValue(@ptrCast(c), &CODECAPI_AVEncCommonMeanBitRate, &v);
                if (fresh) trace("rate control: engaged {s} cap={d} kbps", .{ e.rc_mode.label(), kbps });
                e.publishRc();
            } else {
                // Re-assert the engaged rung after the types for MFTs that only latch post-type
                // (AMD/Intel unverified). Failure here is ignored: the pre-type program stands.
                _ = applyRung(CapiRc{ .c = c }, e.rc_caps, e.rc_mode, e.rc_req);
            }
            v.val = if (gop > 0) @intCast(gop) else @intCast(@divTrunc(2 * fps_n, e.fps_d));
            _ = c.v.SetValue(@ptrCast(c), &CODECAPI_AVEncMPVGOPSize, &v);
            v.val = 0;
            _ = c.v.SetValue(@ptrCast(c), &CODECAPI_AVEncMPVDefaultBPictureCount, &v);
            const b = VARIANT{ .vt = VT_BOOL, .val = 0xFFFF }; // VARIANT_TRUE
            _ = c.v.SetValue(@ptrCast(c), &CODECAPI_AVEncCommonLowLatency, &b);
        }

        e.st(.stream_info);
        var osi = MFT_OUTPUT_STREAM_INFO{ .dwFlags = 0, .cbSize = 0, .cbAlignment = 0 };
        if (!failed(e.enc.v.GetOutputStreamInfo(@ptrCast(e.enc), 0, &osi))) {
            e.enc_provides = osi.dwFlags & (MFT_OUTPUT_STREAM_PROVIDES_SAMPLES | MFT_OUTPUT_STREAM_CAN_PROVIDE_SAMPLES) != 0;
            e.enc_out_size = osi.cbSize;
        }

        // Event generator ONLY for an MFT that declared itself async (resolveDrive read
        // MF_TRANSFORM_ASYNC). A sync MFT also answers this QI - taking its generator and then
        // waiting for METransformNeedInput is the AMD field failure: every submit burned
        // FEED_WAIT_MS and the parent reported "encode timeout" 2.2 s into the route.
        e.st(.evgen_qi);
        var qhr: HRESULT = 0;
        if (!e.is_async) {
            // DIAGNOSTIC, open-time only: does this SYNC MFT expose IMFMediaEventGenerator anyway?
            // If it does, the old QI-based drive-mode discriminator would have driven it async and
            // waited FEED_WAIT_MS per frame for METransformNeedInput events that never come - the
            // exact AMD field failure, reproducible on any MFT that answers yes here.
            var probe: ?*anyopaque = null;
            const phr = qi(e.enc, &IID_IMFMediaEventGenerator, &probe);
            if (!failed(phr) and probe != null) {
                release(@as(*IUnk, @ptrCast(@alignCast(probe.?))));
                trace("WARNING: sync MFT exposes IMFMediaEventGenerator - a QI-based drive-mode probe would hang here", .{});
            }
            qhr = phr;
        }
        if (e.is_async) {
            var eraw: ?*anyopaque = null;
            qhr = qi(e.enc, &IID_IMFMediaEventGenerator, &eraw);
            if (!failed(qhr) and eraw != null) {
                e.evgen = @ptrCast(@alignCast(eraw.?));
            } else {
                // Declared async but has no event generator: it cannot be driven either way.
                return e.setErr("QI IMFMediaEventGenerator (MFT declared async)", qhr);
            }
        }
        trace("drive={s} async_attr={} evgen={} (qi hr=0x{x:0>8}) provides={} outSize={d} sw={}", .{
            if (e.evgen != null) "async" else "sync",
            e.is_async,
            e.evgen != null,
            @as(u32, @bitCast(qhr)),
            e.enc_provides,
            e.enc_out_size,
            e.sw,
        });
    }

    // releaseEncoder undoes a partial encoder bind so another tier can be tried on the SAME
    // device + video processor. Mirrors close()'s MFT half, minus the pool and the device. No-op
    // when no MFT is bound (a failed reconfigure may call it twice).
    fn releaseEncoder(e: *Enc) void {
        if (!e.has_enc) return;
        e.has_enc = false;
        _ = e.enc.v.ProcessMessage(@ptrCast(e.enc), MFT_MESSAGE_COMMAND_FLUSH, 0);
        if (!e.sw) _ = e.enc.v.ProcessMessage(@ptrCast(e.enc), MFT_MESSAGE_SET_D3D_MANAGER, 0);
        if (e.capi) |c| {
            release(c);
            e.capi = null;
        }
        if (e.evgen) |ev| {
            release(ev);
            e.evgen = null;
        }
        release(e.enc);
        if (e.act) |a| {
            _ = a.v.ShutdownObject(@ptrCast(a));
            release(a);
            e.act = null;
        }
        e.is_async = false;
        e.enc_provides = false;
        e.enc_out_size = 0;
        e.name_len = 0;
    }

    /// sw_req = the PARENT asked for the software tier on this session (its per-(adapter,encoder)
    /// failure ledger poisoned the hardware MFT here). Per-session, unlike the env policy: one
    /// poisoned adapter must not force software onto every other route in the child.
    /// rc: rate-control request (qp 0 = fixed CBR at kbps_in; else quality mode with kbps_in as cap).
    pub fn open(gpa: std.mem.Allocator, adapter_luid: i64, in_w: i32, in_h: i32, out_w: i32, out_h: i32, fps_n: i32, fps_d: i32, kbps_in: i32, gop: i32, zerocopy: bool, sw_req: bool, rc: RcReq) OpenErr!*Enc {
        const e = gpa.create(Enc) catch return error.OpenFailed;
        e.* = .{
            .gpa = gpa,
            .in_w = in_w,
            .in_h = in_h,
            .out_w = out_w,
            .out_h = out_h,
            .fps_n = fps_n,
            .fps_d = @max(fps_d, 1),
            .dur100 = @intFromFloat(10_000_000.0 * @as(f64, @floatFromInt(@max(fps_d, 1))) / @as(f64, @floatFromInt(fps_n))),
            .zero_copy = zerocopy,
            .rc_req = rc,
        };
        errdefer gpa.destroy(e);
        if (in_w <= 0 or in_h <= 0 or out_w <= 0 or out_h <= 0 or fps_n <= 0) return e.setErr("args", E_FAIL);
        e.st(.mfstartup);
        trace("MFStartup", .{});
        var hr = MFStartup(MF_VERSION, MFSTARTUP_LITE);
        if (failed(hr)) return e.setErr("MFStartup", hr);

        const allow_sw = sw_policy != .off;
        const force_sw = sw_policy == .force or (sw_req and allow_sw);
        // Capture the rate + tier so a live resolution-cap reconfigure can rebuild the encoder MFT at
        // a new coded size (force_sw_cfg is finalised below once the actual tier is known).
        e.kbps_cfg = kbps_in;
        e.gop_cfg = gop;
        e.allow_sw_cfg = allow_sw;
        e.force_sw_cfg = force_sw;
        try e.acquireDevice(adapter_luid, allow_sw);
        try e.createEnumerator();
        // The whole encoder CONFIGURATION is retryable, not just the bind. A hardware MFT that
        // BINDS and then refuses to configure (NVENC/AMF return MF_E_UNSUPPORTED_D3D_TYPE from
        // SetOutputType once the silicon has no encode session left) used to fail the open
        // outright - the software rung was only reachable when ENUMERATION found nothing, so a
        // merely BUSY GPU took the whole route down with the last rung never tried.
        e.configureEncoder(out_w, out_h, fps_n, kbps_in, gop, allow_sw, force_sw) catch |err| {
            if (e.sw or force_sw or !allow_sw) return err; // already software: nothing left below
            trace("hardware encoder bound but could not be configured ({s}) - retrying on the software tier", .{lastOpenErr()});
            e.releaseEncoder();
            try e.configureEncoder(out_w, out_h, fps_n, kbps_in, gop, true, true);
        };

        // VP + textures
        e.st(.create_vp);
        trace("VP format check + CreateVideoProcessor", .{});
        var fmt_fl: u32 = 0;
        e.bgra_in = true;
        const vpe: *ID3D11VideoProcessorEnumerator = @ptrCast(@alignCast(e.vpe));
        if (!failed(vpe.v.CheckVideoProcessorFormat(@ptrCast(vpe), DXGI_FORMAT_R8G8B8A8_UNORM, &fmt_fl)) and
            fmt_fl & D3D11_VIDEO_PROCESSOR_FORMAT_SUPPORT_INPUT != 0) e.bgra_in = false;
        var vproc: ?*anyopaque = null;
        hr = e.vdev.v.CreateVideoProcessor(@ptrCast(e.vdev), e.vpe, 0, &vproc);
        if (failed(hr) or vproc == null) return e.setErr("CreateVideoProcessor", hr);
        e.vproc = vproc.?;
        const cs = COLOR_SPACE{ .bits = 1 << 2 }; // YCbCr_Matrix=1: BT.709 studio out
        e.vctx.v.VideoProcessorSetOutputColorSpace(@ptrCast(e.vctx), e.vproc, &cs);
        e.vctx.v.VideoProcessorSetStreamColorSpace(@ptrCast(e.vctx), e.vproc, 0, &cs);

        e.st(.create_textures);
        trace("input texture + views + NV12 pool (zerocopy={} sw={})", .{ e.zero_copy, e.sw });
        var td = D3D11_TEXTURE2D_DESC{
            .Width = @intCast(in_w),
            .Height = @intCast(in_h),
            .MipLevels = 1,
            .ArraySize = 1,
            .Format = if (e.bgra_in) DXGI_FORMAT_B8G8R8A8_UNORM else DXGI_FORMAT_R8G8B8A8_UNORM,
            .SampleCount = 1,
            .SampleQuality = 0,
            .Usage = 0, // default
            .BindFlags = D3D11_BIND_SHADER_RESOURCE | D3D11_BIND_RENDER_TARGET,
            .CPUAccessFlags = 0,
            .MiscFlags = 0,
        };
        if (!e.zero_copy) {
            var tex: ?*anyopaque = null;
            hr = e.dev.v.CreateTexture2D(@ptrCast(e.dev), &td, null, &tex);
            if (failed(hr) or tex == null) return e.setErr("CreateTexture2D(in)", hr);
            e.in_tex = tex.?;
            if (e.bgra_in) {
                e.swz = gpa.alloc(u8, @as(usize, @intCast(in_w)) * @as(usize, @intCast(in_h)) * 4) catch return e.setErr("swz alloc", E_FAIL);
            }
            const ivd = VPIV_DESC{ .FourCC = 0, .ViewDimension = D3D11_VPIV_DIMENSION_TEXTURE2D, .MipSlice = 0, .ArraySlice = 0 };
            var iv: ?*anyopaque = null;
            hr = e.vdev.v.CreateVideoProcessorInputView(@ptrCast(e.vdev), e.in_tex, e.vpe, &ivd, &iv);
            if (failed(hr) or iv == null) return e.setErr("CreateVideoProcessorInputView", hr);
            e.in_view = iv.?;
        }

        try e.buildNV12Pool();
        // Finalise the rebuild tier: if configuration fell back to the software MFT, a live
        // reconfigure must stay on software (not retry a hardware bind that just failed here).
        e.force_sw_cfg = e.force_sw_cfg or e.sw;

        e.st(.begin_streaming);
        trace("BEGIN_STREAMING/START_OF_STREAM", .{});
        _ = e.enc.v.ProcessMessage(@ptrCast(e.enc), MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0);
        _ = e.enc.v.ProcessMessage(@ptrCast(e.enc), MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0);
        e.st(.open_done);
        trace("open complete ({s})", .{e.name()});
        return e;
    }

    // buildNV12Pool creates the NV12 round-robin pool (textures + VP output views + - on the
    // hardware tier - DXGI surface samples) at the CURRENT e.out_w/e.out_h, plus the software-tier
    // host input when e.sw. Factored out of open() so reconfigureOutput rebuilds it at a new coded
    // size against the SAME video-processor enumerator (e.vpe), which scales its input to whatever
    // output-view size it is handed. Call with the OLD pool already released (never two pools at once).
    fn buildNV12Pool(e: *Enc) OpenErr!void {
        e.st(.create_textures);
        e.nv_idx = 0;
        e.nv_n = 0;
        if (e.sw) try e.openSoftwareInput();
        while (e.nv_n < pool_init) : (e.nv_n += 1) try e.buildSlot(e.nv_n);
    }

    // buildSlot creates pool slot i: NV12 texture + VP output view + (hardware) DXGI surface sample or
    // (software) system-memory sample. Partial state is left in the slot arrays for release().
    fn buildSlot(e: *Enc, i: usize) OpenErr!void {
        var td = D3D11_TEXTURE2D_DESC{
            .Width = @intCast(e.out_w),
            .Height = @intCast(e.out_h),
            .MipLevels = 1,
            .ArraySize = 1,
            .Format = DXGI_FORMAT_NV12,
            .SampleCount = 1,
            .SampleQuality = 0,
            .Usage = 0,
            // SHADER_RESOURCE alongside RENDER_TARGET: AMD's and Intel's encoder MFTs create
            // an SRV over the input surface (they run a shader pass before submitting to the
            // silicon), where NVENC consumes the texture directly. A render-target-only
            // surface therefore worked on NVIDIA and is a driver-side view creation failure -
            // in the worst case a fault - on the other two vendors.
            .BindFlags = D3D11_BIND_RENDER_TARGET | D3D11_BIND_SHADER_RESOURCE,
            .CPUAccessFlags = 0,
            .MiscFlags = 0,
        };
        var nt: ?*anyopaque = null;
        var hr = e.dev.v.CreateTexture2D(@ptrCast(e.dev), &td, null, &nt);
        if (failed(hr) or nt == null) {
            // Some drivers refuse NV12+SHADER_RESOURCE. Retry render-target-only rather than
            // fail the route: the bind flag is a portability fix, not a requirement.
            td.BindFlags = D3D11_BIND_RENDER_TARGET;
            hr = e.dev.v.CreateTexture2D(@ptrCast(e.dev), &td, null, &nt);
            if (failed(hr) or nt == null) return e.setErr("CreateTexture2D(nv12)", hr);
        }
        e.nv_tex[i] = nt.?;
        const ovd = VPOV_DESC{ .ViewDimension = D3D11_VPOV_DIMENSION_TEXTURE2D, .a = 0, .b = 0, .c = 0 };
        var ov: ?*anyopaque = null;
        hr = e.vdev.v.CreateVideoProcessorOutputView(@ptrCast(e.vdev), nt.?, e.vpe, &ovd, &ov);
        if (failed(hr) or ov == null) return e.setErr("CreateVideoProcessorOutputView", hr);
        e.nv_view[i] = ov.?;
        if (e.sw) return e.buildSwSlot(i); // software tier: host sample, no DXGI surface buffer
        var mb: ?*IMFMediaBuffer = null;
        hr = MFCreateDXGISurfaceBuffer(&IID_ID3D11Texture2D, nt.?, 0, 0, &mb);
        if (failed(hr) or mb == null) return e.setErr("MFCreateDXGISurfaceBuffer(nv12)", hr);
        var braw: ?*anyopaque = null;
        if (!failed(qi(mb.?, &IID_IMF2DBuffer, &braw)) and braw != null) {
            const b2: *IMF2DBuffer = @ptrCast(@alignCast(braw.?));
            var cl: u32 = 0;
            if (!failed(b2.v.GetContiguousLength(@ptrCast(b2), &cl))) {
                _ = mb.?.v.SetCurrentLength(@ptrCast(mb.?), cl);
            }
            release(b2);
        }
        var smp: ?*IMFSample = null;
        hr = MFCreateSample(&smp);
        if (failed(hr) or smp == null) {
            release(mb.?);
            return e.setErr("MFCreateSample(nv12)", hr);
        }
        _ = smp.?.v.AddBuffer(@ptrCast(smp.?), mb.?);
        release(mb.?);
        e.nv_sample[i] = smp.?;
    }

    // growPool adds one slot (bounded by NVPOOL_MAX) and splices it in at nv_idx, so it is the slot
    // written next and every existing slot keeps its rotation order: the oldest slot is then reused
    // after nv_n (old depth) submissions, which the in-flight cap (<= new depth - 2) already covers.
    // false = at max or creation failed (the caller waits for the encoder as before).
    fn growPool(e: *Enc) bool {
        const n = e.nv_n;
        if (n >= NVPOOL_MAX) return false;
        e.buildSlot(n) catch {
            e.releaseSlot(n);
            return false;
        };
        const k = e.nv_idx;
        rotateIn(?*anyopaque, &e.nv_tex, k, n);
        rotateIn(?*anyopaque, &e.nv_view, k, n);
        rotateIn(?*IMFSample, &e.nv_sample, k, n);
        rotateIn(?*IMFSample, &e.sw_sample, k, n);
        rotateIn(?*IMFMediaBuffer, &e.sw_buf, k, n);
        e.nv_n = n + 1;
        e.pool_grows += 1;
        trace("nv12 pool grew to {d} (grows={d})", .{ e.nv_n, e.pool_grows });
        return true;
    }

    // releaseNV12Pool releases every NV12 pool slot (textures, views, samples) + the software staging
    // texture, and NULLS each entry so it is safe to call repeatedly (reconfigure + close). Does NOT
    // touch the VP, the input texture/view or the device - only the per-output-geometry pool.
    fn releaseNV12Pool(e: *Enc) void {
        for (0..NVPOOL_MAX) |i| e.releaseSlot(i);
        e.nv_n = 0;
        if (e.sw_stage_tex) |t| release(@as(*IUnk, @ptrCast(@alignCast(t))));
        e.sw_stage_tex = null;
    }

    fn releaseSlot(e: *Enc, i: usize) void {
        if (e.nv_sample[i]) |s| release(s);
        if (e.sw_sample[i]) |s| release(s);
        if (e.sw_buf[i]) |b| release(b);
        if (e.nv_view[i]) |v_| release(@as(*IUnk, @ptrCast(@alignCast(v_))));
        if (e.nv_tex[i]) |t| release(@as(*IUnk, @ptrCast(@alignCast(t))));
        e.nv_sample[i] = null;
        e.sw_sample[i] = null;
        e.sw_buf[i] = null;
        e.nv_view[i] = null;
        e.nv_tex[i] = null;
    }

    // swNV12Bytes is the packed NV12 payload for the output geometry (Y plane + interleaved UV).
    fn swNV12Bytes(e: *const Enc) u32 {
        const w: u32 = @intCast(e.out_w);
        const h: u32 = @intCast(e.out_h);
        return w * h + w * ((h + 1) / 2);
    }

    // openSoftwareInput builds the software tier's host input: ONE staging NV12 texture for the
    // GPU→host readback plus a pool of packed system-memory NV12 samples. The VP still does CSC
    // and scale on the GPU (or WARP), so this adds exactly one host copy over the hardware tier.
    fn openSoftwareInput(e: *Enc) OpenErr!void {
        const td = D3D11_TEXTURE2D_DESC{
            .Width = @intCast(e.out_w),
            .Height = @intCast(e.out_h),
            .MipLevels = 1,
            .ArraySize = 1,
            .Format = DXGI_FORMAT_NV12,
            .SampleCount = 1,
            .SampleQuality = 0,
            .Usage = USAGE_STAGING,
            .BindFlags = 0,
            .CPUAccessFlags = CPU_ACCESS_READ,
            .MiscFlags = 0,
        };
        var tex: ?*anyopaque = null;
        const hr = e.dev.v.CreateTexture2D(@ptrCast(e.dev), &td, null, &tex);
        if (failed(hr) or tex == null) return e.setErr("CreateTexture2D(nv12 staging)", hr);
        e.sw_stage_tex = tex.?;
    }

    // buildSwSlot creates software-tier slot i's packed system-memory NV12 sample.
    fn buildSwSlot(e: *Enc, i: usize) OpenErr!void {
        const need = e.swNV12Bytes();
        var mb: ?*IMFMediaBuffer = null;
        if (failed(MFCreateMemoryBuffer(need, &mb)) or mb == null) return e.setErr("MFCreateMemoryBuffer(nv12)", E_FAIL);
        _ = mb.?.v.SetCurrentLength(@ptrCast(mb.?), need);
        var smp: ?*IMFSample = null;
        if (failed(MFCreateSample(&smp)) or smp == null) {
            release(mb.?);
            return e.setErr("MFCreateSample(sw nv12)", E_FAIL);
        }
        _ = smp.?.v.AddBuffer(@ptrCast(smp.?), mb.?);
        e.sw_buf[i] = mb.?; // kept: Lock/Unlock per frame without a per-frame QI
        e.sw_sample[i] = smp.?;
    }

    // swReadback copies NV12 pool slot -> staging -> the slot's system-memory sample, honouring
    // the mapped RowPitch (a staging NV12 surface is padded; assuming packed rows shears the
    // picture and can read past the mapping on the last row).
    fn swReadback(e: *Enc, slot: usize) i32 {
        const stage = e.sw_stage_tex orelse return -8;
        e.st(.sw_readback_copy);
        e.ctx.v.CopyResource(@ptrCast(e.ctx), stage, e.nv_tex[slot].?);
        e.st(.sw_readback_map);
        var ms = MAPPED_SUBRESOURCE{ .pData = null, .RowPitch = 0, .DepthPitch = 0 };
        if (failed(e.ctx.v.Map(@ptrCast(e.ctx), stage, 0, MAP_READ, 0, &ms)) or ms.pData == null) return -8;
        defer e.ctx.v.Unmap(@ptrCast(e.ctx), stage, 0);
        const mb = e.sw_buf[slot] orelse return -8;
        var dst: ?[*]u8 = null;
        var maxlen: u32 = 0;
        if (failed(mb.v.Lock(@ptrCast(mb), &dst, &maxlen, null)) or dst == null) return -8;
        defer _ = mb.v.Unlock(@ptrCast(mb));
        const w: usize = @intCast(e.out_w);
        const h: usize = @intCast(e.out_h);
        const pitch: usize = ms.RowPitch;
        const src = ms.pData.?;
        const need = e.swNV12Bytes();
        if (maxlen < need) return -8;
        var o: usize = 0;
        for (0..h) |row| { // Y plane
            @memcpy(dst.?[o .. o + w], src[row * pitch ..][0..w]);
            o += w;
        }
        const uv_rows = (h + 1) / 2;
        const uv_base = h * pitch; // NV12: the UV plane starts one full Y plane in, same pitch
        for (0..uv_rows) |row| {
            @memcpy(dst.?[o .. o + w], src[uv_base + row * pitch ..][0..w]);
            o += w;
        }
        _ = mb.v.SetCurrentLength(@ptrCast(mb), need);
        return 0;
    }

    // harvestOutput pulls encoded AUs into the sink. Async MFTs: ONE ProcessOutput per
    // HaveOutput event; sync MFTs loop until NEED_MORE_INPUT.
    fn harvestOutput(e: *Enc, sink: AuSink) i32 {
        while (true) {
            var ob = MFT_OUTPUT_DATA_BUFFER{};
            var cpu_sample: ?*IMFSample = null;
            if (!e.enc_provides) {
                var floor_sz: u32 = @intCast(@min(@as(i64, e.out_w) * @as(i64, e.out_h), 1 << 26));
                if (floor_sz < (1 << 20)) floor_sz = 1 << 20;
                const sz = @max(e.enc_out_size, floor_sz);
                var mb: ?*IMFMediaBuffer = null;
                if (failed(MFCreateMemoryBuffer(sz, &mb)) or mb == null) return -1;
                if (failed(MFCreateSample(&cpu_sample)) or cpu_sample == null) {
                    release(mb.?);
                    return -1;
                }
                _ = cpu_sample.?.v.AddBuffer(@ptrCast(cpu_sample.?), mb.?);
                release(mb.?);
                ob.pSample = cpu_sample;
            }
            var status: u32 = 0;
            e.st(.process_output);
            const hr = e.enc.v.ProcessOutput(@ptrCast(e.enc), 0, 1, &ob, &status);
            if (hr == MF_E_TRANSFORM_STREAM_CHANGE) {
                var mt: ?*IMFMediaType = null;
                if (!failed(e.enc.v.GetOutputAvailableType(@ptrCast(e.enc), 0, 0, &mt)) and mt != null) {
                    const shr = e.enc.v.SetOutputType(@ptrCast(e.enc), 0, mt.?, 0);
                    if (failed(shr)) trace("stream-change SetOutputType hr=0x{x:0>8}", .{@as(u32, @bitCast(shr))});
                    release(mt.?);
                }
                if (cpu_sample) |s| release(s);
                continue;
            }
            if (hr == MF_E_TRANSFORM_NEED_MORE_INPUT) {
                if (cpu_sample) |s| release(s);
                return 0;
            }
            if (failed(hr)) {
                trace("ProcessOutput hr=0x{x:0>8}", .{@as(u32, @bitCast(hr))});
                if (cpu_sample) |s| release(s);
                return -1;
            }
            const s = ob.pSample orelse return 0;
            if (ob.pEvents) |ev| release(ev);
            var pts: i64 = 0;
            _ = s.v.GetSampleTime(@ptrCast(s), &pts);
            const clean = attrGetU32(s, &MFSampleExtension_CleanPoint) orelse 0;
            e.st(.lock_output);
            var buf: ?*IMFMediaBuffer = null;
            if (!failed(s.v.ConvertToContiguousBuffer(@ptrCast(s), &buf)) and buf != null) {
                var p: ?[*]u8 = null;
                var len: u32 = 0;
                if (!failed(buf.?.v.Lock(@ptrCast(buf.?), &p, null, &len)) and p != null and len > 0) {
                    e.st(.sink_put);
                    sink.put(sink.ctx, p.?[0..len], pts, clean != 0);
                    _ = buf.?.v.Unlock(@ptrCast(buf.?));
                }
                release(buf.?);
            }
            release(s);
            e.out_n += 1;
            if (e.evgen != null) return 0; // async: one output per HaveOutput event
        }
    }

    // pump drains pending encoder events without blocking. <0 = hard error.
    pub fn pump(e: *Enc, sink: AuSink) i32 {
        const gen = e.evgen orelse return e.harvestOutput(sink); // sync MFT
        while (true) {
            var ev: ?*IMFMediaEvent = null;
            e.st(.get_event);
            const hr = gen.v.GetEvent(@ptrCast(gen), MF_EVENT_FLAG_NO_WAIT, &ev);
            if (hr == MF_E_NO_EVENTS_AVAILABLE) return 0;
            if (failed(hr) or ev == null) return -1;
            var met: u32 = 0;
            _ = ev.?.v.GetType(@ptrCast(ev.?), &met);
            release(ev.?);
            switch (met) {
                METransformNeedInput => e.need_input += 1,
                METransformHaveOutput => {
                    if (e.harvestOutput(sink) < 0) return -1;
                },
                METransformDrainComplete => e.drain_done = true,
                else => {},
            }
        }
    }

    fn swizzleTo(dst: []u8, src: [*]const u8) void {
        var i: usize = 0;
        while (i + 4 <= dst.len) : (i += 4) { // RGBA -> BGRA, auto-vectorizes at ReleaseFast
            dst[i] = src[i + 2];
            dst[i + 1] = src[i + 1];
            dst[i + 2] = src[i];
            dst[i + 3] = src[i + 3];
        }
    }

    /// gateInput waits until the next NV12 pool slot is safe to reuse. Bounded; <0 = error.
    /// In-flight cap: a pool sample must NOT be resubmitted while the encoder still queues it
    /// (async MFTs queue deeply; reuse corrupts the queue → E_UNEXPECTED storm + lost
    /// outputs). Also bounds latency: queue depth <= nv_n-1. Called BEFORE a zero-copy
    /// session touches the sender's mutex - never wait on the encoder while holding it.
    /// At the cap the pool first GROWS (bounded, NVPOOL_MAX) - only then does it wait.
    pub fn gateInput(e: *Enc, sink: AuSink) i32 {
        e.st(.gate_input);
        var gate_waited: u32 = 0;
        while (e.fed_n - e.out_n >= e.nv_n - 1) {
            if (e.pump(sink) < 0) return -5;
            if (e.fed_n - e.out_n < e.nv_n - 1) break;
            if (e.growPool()) break;
            if (gate_waited >= SUBMIT_WAIT_MS) return -9; // busy: drop this frame
            Sleep(1);
            gate_waited += 1;
        }
        return 0;
    }

    /// bltView converts one VP input view into the next NV12 pool slot (CSC + scale, GPU only)
    /// and returns that slot; <0 = Blt failed. The ONLY call a zero-copy session makes while
    /// holding the sender's shared-texture mutex.
    pub fn bltView(e: *Enc, in_view: *anyopaque) i32 {
        e.st(.vp_blt);
        const slot = e.nv_idx;
        e.nv_idx = (e.nv_idx + 1) % e.nv_n;
        const stream = D3D11_VIDEO_PROCESSOR_STREAM{
            .Enable = 1,
            .OutputIndex = 0,
            .InputFrameOrField = 0,
            .PastFrames = 0,
            .FutureFrames = 0,
            .pInputSurface = in_view,
        };
        if (failed(e.vctx.v.VideoProcessorBlt(@ptrCast(e.vctx), e.vproc, e.nv_view[slot].?, 0, 1, &stream))) return -4;
        return @intCast(slot);
    }

    /// flushCtx submits the queued GPU work. vctx is a QI of ctx (same device context), so this
    /// submits a VideoProcessorBlt issued through the video interface.
    ///
    /// Load-bearing for reads of a FOREIGN process's shared texture: VideoProcessorBlt only
    /// QUEUES, so without this the read of the sender's texture had not even been submitted when
    /// the sender's mutex was released - the sender was free to overwrite mid-read, and nothing
    /// forced this device to see the sender's writes at all. Spout's own DX11 receiver flushes
    /// inside the lock for exactly this reason.
    pub fn flushCtx(e: *Enc) void {
        e.ctx.v.Flush(@ptrCast(e.ctx));
    }

    /// feed uploads + converts + submits one RGBA frame (stride = in_w*4). Blocks until the
    /// encoder accepts input (bounded). <0 = error.
    pub fn feed(e: *Enc, rgba: [*]const u8, pts100: i64, sink: AuSink) i32 {
        const g0 = e.gateInput(sink);
        if (g0 < 0) return g0;
        var rows: [*]const u8 = rgba;
        if (e.bgra_in) {
            e.st(.swizzle);
            const dst = e.swz.?;
            swizzleTo(dst, rgba);
            rows = dst.ptr;
        }
        const stride: u32 = @intCast(e.in_w * 4);
        e.st(.update_subresource);
        e.ctx.v.UpdateSubresource(@ptrCast(e.ctx), e.in_tex, 0, null, rows, stride, 0);
        const slot = e.bltView(e.in_view);
        if (slot < 0) return slot;
        return e.submitSlot(@intCast(slot), pts100, sink);
    }

    /// submitSlot hands a converted NV12 pool slot to the encoder (bounded waits). <0 = error. A slot
    /// that never reached the encoder (busy / refused) is handed out again next: the rotation then
    /// counts only real submissions, so the in-flight cap's reuse guarantee holds across drops.
    pub fn submitSlot(e: *Enc, slot: usize, pts100: i64, sink: AuSink) i32 {
        const fed0 = e.fed_n;
        const rc = e.submitSlotInner(slot, pts100, sink);
        if (e.fed_n == fed0) e.nv_idx = slot;
        return rc;
    }

    fn submitSlotInner(e: *Enc, slot: usize, pts100: i64, sink: AuSink) i32 {
        if (e.sw) { // software tier: the MFT needs the pixels in system memory
            const rc = e.swReadback(slot);
            if (rc < 0) return rc;
        }
        e.st(.sample_time);
        const nv12 = (if (e.sw) e.sw_sample[slot] else e.nv_sample[slot]).?; // pool-owned
        _ = nv12.v.SetSampleTime(@ptrCast(nv12), pts100);
        _ = nv12.v.SetSampleDuration(@ptrCast(nv12), e.dur100);

        var hr: HRESULT = 0;
        if (e.force_idr) {
            e.st(.force_idr);
            if (e.capi) |c| {
                const v = VARIANT{ .vt = VT_UI4, .val = 1 };
                _ = c.v.SetValue(@ptrCast(c), &CODECAPI_AVEncVideoForceKeyFrame, &v);
            }
            e.force_idr = false;
        }

        if (e.evgen != null) {
            var waited: u32 = 0;
            e.st(.wait_need_input);
            while (true) {
                if (e.pump(sink) < 0) return -5;
                if (e.need_input > 0) break;
                if (waited >= SUBMIT_WAIT_MS) return -9; // busy: no encoder credit in time
                Sleep(1);
                waited += 1;
            }
            e.need_input -= 1;
            e.st(.process_input);
            hr = e.enc.v.ProcessInput(@ptrCast(e.enc), 0, nv12, 0);
            if (failed(hr)) return -7;
            e.fed_n += 1;
            if (e.pump(sink) < 0) return -5;
            return 0;
        }
        var waited: u32 = 0;
        while (true) {
            e.st(.process_input);
            hr = e.enc.v.ProcessInput(@ptrCast(e.enc), 0, nv12, 0);
            if (hr != MF_E_NOTACCEPTING) break;
            if (e.harvestOutput(sink) < 0) return -5;
            if (waited >= SUBMIT_WAIT_MS) return -9; // busy: MFT still NOTACCEPTING
            Sleep(1);
            waited += 1;
        }
        if (failed(hr)) return -7;
        e.fed_n += 1;
        if (e.harvestOutput(sink) < 0) return -5;
        return 0;
    }

    pub fn forceIDR(e: *Enc) void {
        e.force_idr = true;
    }

    // applyRc probes the MFT's rate-control knobs and engages the first rung it accepts (see the
    // rate-control block above). One trace line names the rung - the open-time verdict per session.
    fn applyRc(e: *Enc, c: *ICodecAPI) void {
        e.rc_caps = probeRcCaps(c);
        e.rc_mode = pickRung(CapiRc{ .c = c }, e.rc_caps, e.rc_req, rc_force);
        const cp = e.rc_caps;
        trace("rate control: engaged {s} cap={d} kbps qp={d} q={d} (caps mode={} mean={} max={} buf={} minqp={} encqp={} quality={} listed=0x{x})", .{
            e.rc_mode.label(), e.rc_req.cap_kbps, e.rc_req.qp, e.rc_req.quality, cp.mode, cp.mean, cp.max, cp.buffer, cp.min_qp, cp.enc_qp, cp.quality, cp.listed orelse 0xff,
        });
        e.publishRc();
    }

    // publishRc mirrors the engaged rung + cap into the session shm (parent ProcStats).
    fn publishRc(e: *Enc) void {
        const p = e.rc_sink orelse return;
        const w0: u32 = @as(u32, @intFromEnum(e.rc_mode)) | (@as(u32, e.rc_req.qp) << 8) | (@as(u32, e.rc_req.quality) << 16);
        @atomicStore(u32, @volatileCast(&p[1]), e.rc_req.cap_kbps, .release);
        @atomicStore(u32, @volatileCast(&p[0]), w0, .release);
    }

    /// bindRc points rate-control telemetry at the parent-mapped shm words and publishes once.
    pub fn bindRc(e: *Enc, p: *volatile [2]u32) void {
        e.rc_sink = p;
        e.publishRc();
    }

    /// rcMode is the engaged rate-control rung.
    pub fn rcMode(e: *const Enc) RcMode {
        return e.rc_mode;
    }

    /// setBitrate live-moves the cap (no reopen): CBR target in fixed mode, the peak in quality mode.
    /// The new cap is kept for a reconfigureOutput rebuild (which used to revert to the open value).
    pub fn setBitrate(e: *Enc, kbps: u32) bool {
        const c = e.capi orelse return false;
        e.st(.set_bitrate);
        e.rc_req.cap_kbps = kbps;
        e.kbps_cfg = @intCast(@min(kbps, std.math.maxInt(i32)));
        const ok = retargetRung(CapiRc{ .c = c }, e.rc_caps, e.rc_mode, kbps);
        e.publishRc();
        return ok;
    }

    // rebuildAt re-creates the encoder MFT + NV12 pool at (ow, oh) on the SAME device + video
    // processor, then starts streaming. Caller has already released the previous encoder + pool.
    // Resets the in-flight accounting (a fresh MFT has nothing queued). false = build failed.
    fn rebuildAt(e: *Enc, ow: i32, oh: i32) bool {
        e.out_w = ow;
        e.out_h = oh;
        e.configureEncoder(ow, oh, e.fps_n, e.kbps_cfg, e.gop_cfg, e.allow_sw_cfg, e.force_sw_cfg) catch return false;
        e.buildNV12Pool() catch return false;
        e.fed_n = 0;
        e.out_n = 0;
        e.need_input = 0;
        e.drain_done = false;
        e.st(.begin_streaming);
        _ = e.enc.v.ProcessMessage(@ptrCast(e.enc), MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0);
        _ = e.enc.v.ProcessMessage(@ptrCast(e.enc), MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0);
        return true;
    }

    /// reconfigureOutput changes the CODED output size LIVE (the receiver's VRAM resolution cap). The
    /// caller passes the already-computed even target dims (aspect-exact, never above source). It
    /// drains the current encoder tail, drops the encoder MFT + NV12 pool, and re-creates both at the
    /// new size against the SAME device + video processor - the "drain + re-create the MFT in place"
    /// path. The VP already scales its input to whatever output-view size it is given, so the input
    /// side (a zero-copy session's cap view, or the readback in_tex/in_view) is untouched and both
    /// capture paths keep working. Never holds the old and new NV12 pools at once. The first frame
    /// after the switch is an IDR with fresh SPS/PPS (a freshly-streaming MFT keyframes; forceIDR is
    /// belt-and-braces). Returns 0 on success; -1 if the new size failed but the OLD size was restored
    /// (route keeps encoding, cap not applied - never tear the route down for a failed cap); -2 if the
    /// device is lost and neither size could rebuild (the session ends; the parent re-establishes).
    pub fn reconfigureOutput(e: *Enc, new_out_w: i32, new_out_h: i32, sink: AuSink) i32 {
        if (new_out_w <= 0 or new_out_h <= 0) return -1;
        if (new_out_w == e.out_w and new_out_h == e.out_h) return 0;
        const ow = e.out_w;
        const oh = e.out_h;
        _ = e.drain(sink); // flush the old tail so no in-flight sample references the pool we drop
        e.releaseEncoder();
        e.releaseNV12Pool();
        if (e.rebuildAt(new_out_w, new_out_h)) {
            e.forceIDR();
            return 0;
        }
        trace("reconfigureOutput {d}x{d} failed - restoring {d}x{d}", .{ new_out_w, new_out_h, ow, oh });
        e.releaseEncoder(); // drop whatever the failed attempt half-built (no-op if nothing bound)
        e.releaseNV12Pool();
        if (e.rebuildAt(ow, oh)) {
            e.forceIDR();
            return -1;
        }
        return -2;
    }

    /// drain flushes the encoder tail into the sink (bounded). Returns 0 done, 1 timeout, <0 error.
    pub fn drain(e: *Enc, sink: AuSink) i32 {
        if (!e.has_enc) return 1; // no bound MFT (a failed reconfigure left none): nothing to drain
        e.st(.drain_msg);
        _ = e.enc.v.ProcessMessage(@ptrCast(e.enc), MFT_MESSAGE_NOTIFY_END_OF_STREAM, 0);
        _ = e.enc.v.ProcessMessage(@ptrCast(e.enc), MFT_MESSAGE_COMMAND_DRAIN, 0);
        e.st(.drain_pump);
        if (e.evgen == null) {
            return e.harvestOutput(sink);
        }
        var waited: u32 = 0;
        // Done when the MFT says so OR when every submitted frame came back (fed_n==out_n):
        // some vendor MFTs never deliver METransformDrainComplete - without the in-flight
        // check every close would burn the full FEED_WAIT_MS.
        while (!e.drain_done and e.fed_n != e.out_n and waited < FEED_WAIT_MS) {
            const rc = e.pump(sink);
            if (rc < 0) return rc;
            if (e.drain_done or e.fed_n == e.out_n) break;
            Sleep(1);
            waited += 1;
        }
        return if (e.drain_done or e.fed_n == e.out_n) 0 else 1;
    }

    // close follows the DOCUMENTED MFT teardown order. The old order (release the MFT, then the
    // device manager, then the device) left the vendor MFT holding OUR device manager while we
    // dropped our last references - the MFT's own worker threads then raced a dying device. AMD's
    // and Intel's MFTs keep driver-side state alive across Release and are torn down by their
    // ACTIVATE; NVIDIA's tolerated the short order, which is why this never showed up here.
    //   FLUSH (drop queued samples) -> END_STREAMING -> SET_D3D_MANAGER(0) (MFT drops the
    //   manager) -> Release MFT -> ShutdownObject the activate -> pool -> VP -> device.
    pub fn close(e: *Enc) void {
        // A failed reconfigure can leave the session with no bound MFT (has_enc == false): the
        // encoder teardown is then skipped, but the VP, input texture and device are still released.
        if (e.has_enc) {
            e.st(.close_flush);
            _ = e.enc.v.ProcessMessage(@ptrCast(e.enc), MFT_MESSAGE_COMMAND_FLUSH, 0);
            e.st(.close_end_streaming);
            _ = e.enc.v.ProcessMessage(@ptrCast(e.enc), MFT_MESSAGE_NOTIFY_END_STREAMING, 0);
            if (!e.sw) { // the software tier never took a device manager
                e.st(.close_clear_d3d_manager);
                _ = e.enc.v.ProcessMessage(@ptrCast(e.enc), MFT_MESSAGE_SET_D3D_MANAGER, 0);
            }
            if (e.capi) |c| release(c);
            if (e.evgen) |ev| release(ev);
            e.st(.close_release_mft);
            release(e.enc);
            if (e.act) |a| {
                e.st(.close_shutdown_activate);
                _ = a.v.ShutdownObject(@ptrCast(a));
                release(a);
                e.act = null;
            }
            e.has_enc = false;
        }
        e.st(.close_release_pool);
        e.releaseNV12Pool();
        if (!e.zero_copy) release(@as(*IUnk, @ptrCast(@alignCast(e.in_view))));
        e.st(.close_release_vp);
        release(@as(*IUnk, @ptrCast(@alignCast(e.vproc))));
        release(@as(*IUnk, @ptrCast(@alignCast(e.vpe))));
        if (!e.zero_copy) release(@as(*IUnk, @ptrCast(@alignCast(e.in_tex))));
        e.st(.close_release_device);
        // A shared device outlives this session: drop a reference, never Release it out from under
        // the sibling sessions still encoding on it.
        if (e.owns_device) {
            release(e.devmgr);
            e.releaseVideoDevice();
            e.releaseDevice();
        } else if (e.shared_ref) |sd| {
            releaseShared(sd);
        }
        if (e.swz) |s| e.gpa.free(s);
        e.st(.close_done);
        const gpa = e.gpa;
        gpa.destroy(e);
    }
};

test "GetDeviceRemovedReason vtable slot" {
    const vt = @typeInfo(@FieldType(DevRemovedView, "v")).pointer.child;
    try std.testing.expectEqual(@as(usize, 39), @offsetOf(vt, "GetDeviceRemovedReason") / @sizeOf(VOP));
}

test "h264 level table" {
    try std.testing.expectEqual(@as(u32, 52), h264LevelFor(3840, 2160, 60, 1));
    try std.testing.expectEqual(@as(u32, 51), h264LevelFor(3840, 2160, 30, 1));
    try std.testing.expectEqual(@as(u32, 42), h264LevelFor(1920, 1080, 60, 1));
    try std.testing.expectEqual(@as(u32, 31), h264LevelFor(1280, 720, 30, 1));
}

// The stage codes are a CROSS-PROCESS contract: the Go supervisor decodes them out of shared
// memory after the child is already dead (internal/mfenc stageName). Renumbering one silently
// mislabels a crash, so the load-bearing values are pinned here.
test "stage codes are a stable cross-process contract" {
    try std.testing.expectEqual(@as(u32, 0), @intFromEnum(Stage.idle));
    try std.testing.expectEqual(@as(u32, 16), @intFromEnum(Stage.set_d3d_manager));
    try std.testing.expectEqual(@as(u32, 25), @intFromEnum(Stage.open_done));
    try std.testing.expectEqual(@as(u32, 46), @intFromEnum(Stage.wait_need_input));
    try std.testing.expectEqual(@as(u32, 47), @intFromEnum(Stage.process_input));
    try std.testing.expectEqual(@as(u32, 49), @intFromEnum(Stage.process_output));
    try std.testing.expectEqual(@as(u32, 73), @intFromEnum(Stage.close_release_mft));
    try std.testing.expectEqual(@as(u32, 78), @intFromEnum(Stage.close_done));
}

// The whole AMD field failure was a wrong drive-mode discriminator, and this codebase has already
// shipped one hand-rolled-GUID typo in exactly this area (IMFMediaEventGenerator …1e7b vs …1e7d).
// Pin both bytes against the SDK values so the next transcription error fails a test, not a rig.
test "async drive GUIDs match the SDK" {
    // MF_TRANSFORM_ASYNC {f81a699a-649a-497d-8c73-29f8fed6ad7a} (mftransform.h)
    try std.testing.expectEqual(@as(u32, 0xf81a699a), MF_TRANSFORM_ASYNC.d1);
    try std.testing.expectEqual(@as(u8, 0x7a), MF_TRANSFORM_ASYNC.d4[7]);
    // MF_TRANSFORM_ASYNC_UNLOCK {e5666d6b-3422-4eb6-a421-da7db1f8e207}
    try std.testing.expectEqual(@as(u32, 0xe5666d6b), MF_TRANSFORM_ASYNC_UNLOCK.d1);
    // IMFMediaEventGenerator {2CD0BD52-BCD5-4B89-B62C-EADC0C031E7D} - last byte 0x7d, NOT 0x7b.
    try std.testing.expectEqual(@as(u8, 0x7d), IID_IMFMediaEventGenerator.d4[7]);
}

test "software tier NV12 payload size covers odd heights" {
    var e = Enc{ .gpa = std.testing.allocator, .in_w = 0, .in_h = 0, .out_w = 1280, .out_h = 720, .fps_n = 30, .fps_d = 1, .dur100 = 0 };
    try std.testing.expectEqual(@as(u32, 1280 * 720 * 3 / 2), e.swNV12Bytes());
    e.out_w = 640;
    e.out_h = 361; // odd height: the UV plane rounds UP, else the last chroma row is truncated
    try std.testing.expectEqual(@as(u32, 640 * 361 + 640 * 181), e.swNV12Bytes());
}

test "vendor mismatch" {
    try std.testing.expect(vendorMismatch("Intel Quick Sync H.264", "NVIDIA"));
    try std.testing.expect(!vendorMismatch("NVIDIA H.264 Encoder MFT", "NVIDIA"));
    try std.testing.expect(!vendorMismatch("Contoso HW Encoder", "NVIDIA"));
    try std.testing.expect(!vendorMismatch("AMD AMF Encoder", null));
}

/// api is the INTERNAL surface dec.zig needs. Zig has no friend visibility and a separate file
/// cannot see this one's privates, so these are pure ALIASES - nothing here changes the encode
/// path, and the block documents exactly how much of mf.zig the decode side couples to.
pub const api = struct {
    // flat imports
    pub const startup = MFStartup;
    pub const enumMFT = MFTEnumEx;
    pub const createMediaType = MFCreateMediaType;
    pub const createDeviceManager = MFCreateDXGIDeviceManager;
    pub const createSample = MFCreateSample;
    pub const createMemoryBuffer = MFCreateMemoryBuffer;
    pub const createD3D11Device = D3D11CreateDevice;
    pub const sleep = Sleep;
    pub const coTaskMemFree = CoTaskMemFree;

    // interfaces + structs not already public
    pub const AttrVtbl_ = AttrVtbl;
    pub const ContentDesc = D3D11_VIDEO_PROCESSOR_CONTENT_DESC;
    pub const Rational = DXGI_RATIONAL;
    pub const VPStream = D3D11_VIDEO_PROCESSOR_STREAM;
    pub const VPOVDesc = VPOV_DESC;
    pub const ColorSpace = COLOR_SPACE;
    pub const RegisterTypeInfo = MFT_REGISTER_TYPE_INFO;
    pub const OutputStreamInfo = MFT_OUTPUT_STREAM_INFO;
    pub const OutputDataBuffer = MFT_OUTPUT_DATA_BUFFER;

    // helpers
    pub const setU32 = attrSetU32;
    pub const setU64 = attrSetU64;
    pub const setGUID = attrSetGUID;
    pub const getU32 = attrGetU32;
    pub const getU64 = attrGetU64;
    pub const getDisplayArea = attrGetDisplayArea;
    pub const DisplayArea = VideoArea;
    pub const getGUID = attrGetGUID;
    pub const guidsEqual = guidEq;
    pub const mediaType = mtVideo;
    pub const traceStage = trace;

    // GUIDs
    pub const CAT_VIDEO_DECODER = MFT_CATEGORY_VIDEO_DECODER;
    pub const MT_VIDEO = MFMediaType_Video;
    pub const FMT_NV12 = MFVideoFormat_NV12;
    pub const FMT_H264 = MFVideoFormat_H264;
    pub const FMT_HEVC = MFVideoFormat_HEVC;
    pub const A_MAJOR_TYPE = MF_MT_MAJOR_TYPE;
    pub const A_SUBTYPE = MF_MT_SUBTYPE;
    pub const A_FRAME_SIZE = MF_MT_FRAME_SIZE;
    pub const A_MIN_DISPLAY_APERTURE = MF_MT_MINIMUM_DISPLAY_APERTURE;
    pub const A_FRAME_RATE = MF_MT_FRAME_RATE;
    pub const A_D3D11_AWARE = MF_SA_D3D11_AWARE;
    pub const A_ASYNC_UNLOCK = MF_TRANSFORM_ASYNC_UNLOCK;
    pub const A_LOW_LATENCY = MF_LOW_LATENCY;
    pub const A_FRIENDLY_NAME = MFT_FRIENDLY_NAME;
    pub const IID_EVGEN = IID_IMFMediaEventGenerator;
    pub const IID_MULTITHREAD = IID_ID3D10Multithread;
    pub const IID_VIDEO_DEVICE = IID_ID3D11VideoDevice;
    pub const IID_VIDEO_CONTEXT = IID_ID3D11VideoContext;
    pub const IID_TRANSFORM = IID_IMFTransform;

    // constants
    pub const MF_VER = MF_VERSION;
    pub const STARTUP_LITE = MFSTARTUP_LITE;
    pub const ENUM_HARDWARE = MFT_ENUM_FLAG_HARDWARE;
    pub const ENUM_SORTFILTER = MFT_ENUM_FLAG_SORTANDFILTER;
    pub const MSG_SET_D3D_MANAGER = MFT_MESSAGE_SET_D3D_MANAGER;
    pub const MSG_DRAIN = MFT_MESSAGE_COMMAND_DRAIN;
    pub const MSG_BEGIN_STREAMING = MFT_MESSAGE_NOTIFY_BEGIN_STREAMING;
    pub const MSG_END_OF_STREAM = MFT_MESSAGE_NOTIFY_END_OF_STREAM;
    pub const MSG_START_OF_STREAM = MFT_MESSAGE_NOTIFY_START_OF_STREAM;
    pub const OUT_PROVIDES_SAMPLES = MFT_OUTPUT_STREAM_PROVIDES_SAMPLES;
    pub const OUT_CAN_PROVIDE_SAMPLES = MFT_OUTPUT_STREAM_CAN_PROVIDE_SAMPLES;
    pub const EV_NEED_INPUT = METransformNeedInput;
    pub const EV_HAVE_OUTPUT = METransformHaveOutput;
    pub const EV_DRAIN_COMPLETE = METransformDrainComplete;
    pub const EV_NO_WAIT = MF_EVENT_FLAG_NO_WAIT;
    pub const E_STREAM_CHANGE = MF_E_TRANSFORM_STREAM_CHANGE;
    pub const E_NEED_MORE_INPUT = MF_E_TRANSFORM_NEED_MORE_INPUT;
    pub const E_NO_EVENTS = MF_E_NO_EVENTS_AVAILABLE;
    pub const E_NOTACCEPTING = MF_E_NOTACCEPTING;
    pub const E_HFAIL = E_FAIL;
    pub const DRIVER_UNKNOWN = D3D_DRIVER_TYPE_UNKNOWN;
    pub const DRIVER_HARDWARE = D3D_DRIVER_TYPE_HARDWARE;
    pub const DEV_BGRA = D3D11_CREATE_DEVICE_BGRA_SUPPORT;
    pub const DEV_VIDEO = D3D11_CREATE_DEVICE_VIDEO_SUPPORT;
    pub const SDK_VERSION = D3D11_SDK_VERSION;
    pub const FMT_DXGI_NV12 = DXGI_FORMAT_NV12;
    pub const VPIV_TEXTURE2D = D3D11_VPIV_DIMENSION_TEXTURE2D;
    pub const VPOV_TEXTURE2D = D3D11_VPOV_DIMENSION_TEXTURE2D;
    pub const VP_SUPPORT_INPUT = D3D11_VIDEO_PROCESSOR_FORMAT_SUPPORT_INPUT;
    pub const feed_wait_ms = FEED_WAIT_MS;
};

// ── rate-control capability probe (test-only, opt-in: RAVE_MATE_MFENC_RCPROBE=1) ──
// Prints what each hardware H.264/HEVC encoder MFT on the default adapter reports for the
// quality-mode knobs, after the media types are set (vendor MFTs answer differently before).
extern "kernel32" fn GetEnvironmentVariableA([*:0]const u8, ?[*]u8, u32) callconv(.winapi) u32;

fn variantText(v: VARIANT, buf: []u8) []const u8 {
    return switch (v.vt) {
        VT_UI4 => std.fmt.bufPrint(buf, "{d}", .{@as(u32, @truncate(v.val))}) catch "?",
        VT_UI8 => std.fmt.bufPrint(buf, "{d}", .{v.val}) catch "?",
        VT_BOOL => if (@as(u16, @truncate(v.val)) != 0) "true" else "false",
        0 => "-",
        else => std.fmt.bufPrint(buf, "vt{d}:{d}", .{ v.vt, v.val }) catch "?",
    };
}

fn probeOne(c: *ICodecAPI, label: []const u8, key: *const GUID) void {
    const sup = c.v.IsSupported(@ptrCast(c), key);
    const mdf = c.v.IsModifiable(@ptrCast(c), key);
    var dv = VARIANT{ .vt = 0 };
    var cv = VARIANT{ .vt = 0 };
    var lo = VARIANT{ .vt = 0 };
    var hi = VARIANT{ .vt = 0 };
    var step = VARIANT{ .vt = 0 };
    _ = c.v.GetDefaultValue(@ptrCast(c), key, &dv);
    _ = c.v.GetValue(@ptrCast(c), key, &cv);
    const rhr = c.v.GetParameterRange(@ptrCast(c), key, &lo, &hi, &step);
    var b1: [32]u8 = undefined;
    var b2: [32]u8 = undefined;
    var b3: [32]u8 = undefined;
    var b4: [32]u8 = undefined;
    var b5: [32]u8 = undefined;
    std.debug.print("  {s:<18} supported={s:<3} modifiable={s:<3} default={s:<10} value={s:<10} range={s}..{s} step {s} (hr=0x{x:0>8})\n", .{
        label,                if (sup == 0) "yes" else "no", if (mdf == 0) "yes" else "no",
        variantText(dv, &b1), variantText(cv, &b2),          variantText(lo, &b3),
        variantText(hi, &b4), variantText(step, &b5),        @as(u32, @bitCast(rhr)),
    });
}

fn probeCodec(dm: *IMFDXGIDeviceManager, sub: *const GUID, cname: []const u8) void {
    const ti = MFT_REGISTER_TYPE_INFO{ .major = MFMediaType_Video, .sub = MFVideoFormat_NV12 };
    const to = MFT_REGISTER_TYPE_INFO{ .major = MFMediaType_Video, .sub = sub.* };
    var acts: ?[*]*IMFActivate = null;
    var n: u32 = 0;
    if (failed(MFTEnumEx(MFT_CATEGORY_VIDEO_ENCODER, MFT_ENUM_FLAG_HARDWARE | MFT_ENUM_FLAG_SORTANDFILTER, &ti, &to, &acts, &n)) or n == 0) {
        std.debug.print("{s}: no hardware encoder MFT\n", .{cname});
        return;
    }
    defer CoTaskMemFree(@ptrCast(acts));
    for (acts.?[0..n]) |a| {
        defer release(a);
        var nb: [128]u8 = @splat(0);
        const nm = Enc.activateName(a, &nb);
        var raw: ?*anyopaque = null;
        if (failed(a.v.ActivateObject(@ptrCast(a), &IID_IMFTransform, &raw)) or raw == null) continue;
        const t: *IMFTransform = @ptrCast(@alignCast(raw.?));
        defer {
            release(t);
            _ = a.v.ShutdownObject(@ptrCast(a));
        }
        var attrs: ?*IMFAttributes = null;
        if (!failed(t.v.GetAttributes(@ptrCast(t), &attrs)) and attrs != null) {
            attrSetU32(attrs.?, &MF_TRANSFORM_ASYNC_UNLOCK, 1);
            attrSetU32(attrs.?, &MF_LOW_LATENCY, 1);
            release(attrs.?);
        }
        const dmr = t.v.ProcessMessage(@ptrCast(t), MFT_MESSAGE_SET_D3D_MANAGER, @intFromPtr(dm));
        const om = mtVideo(sub, 1920, 1080, 60, 1) orelse continue;
        attrSetU32(om, &MF_MT_AVG_BITRATE, 20_000_000);
        const hevc = guidEq(sub.*, MFVideoFormat_HEVC);
        attrSetU32(om, &MF_MT_MPEG2_PROFILE, if (hevc) 1 else 77);
        attrSetU32(om, &MF_MT_MPEG2_LEVEL, if (hevc) 123 else 42);
        const ohr = t.v.SetOutputType(@ptrCast(t), 0, om, 0);
        release(om);
        const im = mtVideo(&MFVideoFormat_NV12, 1920, 1080, 60, 1) orelse continue;
        const ihr = t.v.SetInputType(@ptrCast(t), 0, im, 0);
        release(im);
        std.debug.print("{s}: {s} (d3d=0x{x:0>8} out=0x{x:0>8} in=0x{x:0>8})\n", .{ cname, nm, @as(u32, @bitCast(dmr)), @as(u32, @bitCast(ohr)), @as(u32, @bitCast(ihr)) });
        var craw: ?*anyopaque = null;
        if (failed(qi(t, &IID_ICodecAPI, &craw)) or craw == null) {
            std.debug.print("  no ICodecAPI\n", .{});
            continue;
        }
        const c: *ICodecAPI = @ptrCast(@alignCast(craw.?));
        defer release(c);
        probeOne(c, "RateControlMode", &CODECAPI_AVEncCommonRateControlMode);
        var vals: ?[*]VARIANT = null;
        var nv: u32 = 0;
        if (!failed(c.v.GetParameterValues(@ptrCast(c), &CODECAPI_AVEncCommonRateControlMode, &vals, &nv)) and vals != null) {
            std.debug.print("    RateControlMode values:", .{});
            for (vals.?[0..nv]) |v| std.debug.print(" {d}", .{@as(u32, @truncate(v.val))});
            std.debug.print("\n", .{});
            CoTaskMemFree(@ptrCast(vals));
        }
        var m: u32 = 0;
        while (m <= 6) : (m += 1) {
            const sv = VARIANT{ .vt = VT_UI4, .val = m };
            const shr = c.v.SetValue(@ptrCast(c), &CODECAPI_AVEncCommonRateControlMode, &sv);
            var rb = VARIANT{ .vt = 0 };
            _ = c.v.GetValue(@ptrCast(c), &CODECAPI_AVEncCommonRateControlMode, &rb);
            std.debug.print("    set RateControlMode={d}: hr=0x{x:0>8} readback={d}\n", .{ m, @as(u32, @bitCast(shr)), @as(u32, @truncate(rb.val)) });
        }
        probeOne(c, "MeanBitRate", &CODECAPI_AVEncCommonMeanBitRate);
        probeOne(c, "MaxBitRate", &CODECAPI_AVEncCommonMaxBitRate);
        probeOne(c, "BufferSize", &CODECAPI_AVEncCommonBufferSize);
        probeOne(c, "Quality", &CODECAPI_AVEncCommonQuality);
        probeOne(c, "VideoEncodeQP", &CODECAPI_AVEncVideoEncodeQP);
        probeOne(c, "VideoMinQP", &CODECAPI_AVEncVideoMinQP);
        probeOne(c, "VideoMaxQP", &CODECAPI_AVEncVideoMaxQP);
        probeOne(c, "LowLatency", &CODECAPI_AVEncCommonLowLatency);
        probeOne(c, "BPictureCount", &CODECAPI_AVEncMPVDefaultBPictureCount);
        probeOne(c, "GOPSize", &CODECAPI_AVEncMPVGOPSize);
    }
}

// probePreType: which knob, SET BEFORE SetOutputType, does the H.264 MFT reject at type time?
fn probePreType(dm: *IMFDXGIDeviceManager) void {
    const K = struct { l: []const u8, k: *const GUID, v: VARIANT };
    const ks = [_]K{
        .{ .l = "none", .k = &CODECAPI_AVEncMPVGOPSize, .v = vU4(120) },
        .{ .l = "mode=0", .k = &CODECAPI_AVEncCommonRateControlMode, .v = vU4(0) },
        .{ .l = "mode=1", .k = &CODECAPI_AVEncCommonRateControlMode, .v = vU4(1) },
        .{ .l = "mode=3", .k = &CODECAPI_AVEncCommonRateControlMode, .v = vU4(3) },
        .{ .l = "mode=4", .k = &CODECAPI_AVEncCommonRateControlMode, .v = vU4(4) },
        .{ .l = "mean=6M", .k = &CODECAPI_AVEncCommonMeanBitRate, .v = vU4(6_000_000) },
        .{ .l = "max=6M", .k = &CODECAPI_AVEncCommonMaxBitRate, .v = vU4(6_000_000) },
        .{ .l = "max=12M", .k = &CODECAPI_AVEncCommonMaxBitRate, .v = vU4(12_000_000) },
        .{ .l = "buffer=6M", .k = &CODECAPI_AVEncCommonBufferSize, .v = vU4(6_000_000) },
        .{ .l = "minqp=23", .k = &CODECAPI_AVEncVideoMinQP, .v = vU4(23) },
        .{ .l = "encqp=23", .k = &CODECAPI_AVEncVideoEncodeQP, .v = .{ .vt = VT_UI8, .val = 23 } },
        .{ .l = "quality=55", .k = &CODECAPI_AVEncCommonQuality, .v = vU4(55) },
    };
    for (ks) |kk| {
        const ti = MFT_REGISTER_TYPE_INFO{ .major = MFMediaType_Video, .sub = MFVideoFormat_NV12 };
        const to = MFT_REGISTER_TYPE_INFO{ .major = MFMediaType_Video, .sub = MFVideoFormat_H264 };
        var acts: ?[*]*IMFActivate = null;
        var n: u32 = 0;
        if (failed(MFTEnumEx(MFT_CATEGORY_VIDEO_ENCODER, MFT_ENUM_FLAG_HARDWARE | MFT_ENUM_FLAG_SORTANDFILTER, &ti, &to, &acts, &n)) or n == 0) return;
        const a = acts.?[0];
        var raw: ?*anyopaque = null;
        if (!failed(a.v.ActivateObject(@ptrCast(a), &IID_IMFTransform, &raw)) and raw != null) {
            const t: *IMFTransform = @ptrCast(@alignCast(raw.?));
            _ = t.v.ProcessMessage(@ptrCast(t), MFT_MESSAGE_SET_D3D_MANAGER, @intFromPtr(dm));
            var craw: ?*anyopaque = null;
            var shr: HRESULT = -1;
            if (!failed(qi(t, &IID_ICodecAPI, &craw)) and craw != null) {
                const c: *ICodecAPI = @ptrCast(@alignCast(craw.?));
                if (!std.mem.eql(u8, kk.l, "none")) shr = c.v.SetValue(@ptrCast(c), kk.k, &kk.v);
                release(c);
            }
            const om = mtVideo(&MFVideoFormat_H264, 1280, 720, 60, 1).?;
            attrSetU32(om, &MF_MT_AVG_BITRATE, 6_000_000);
            attrSetU32(om, &MF_MT_MPEG2_PROFILE, 77);
            attrSetU32(om, &MF_MT_MPEG2_LEVEL, 32);
            const ohr = t.v.SetOutputType(@ptrCast(t), 0, om, 0);
            release(om);
            std.debug.print("pre-type {s:<12} set=0x{x:0>8} SetOutputType=0x{x:0>8}\n", .{ kk.l, @as(u32, @bitCast(shr)), @as(u32, @bitCast(ohr)) });
            release(t);
            _ = a.v.ShutdownObject(@ptrCast(a));
        }
        for (acts.?[0..n]) |x| release(x);
        CoTaskMemFree(@ptrCast(acts));
    }
}

test "rate-control capability probe (opt-in, needs a GPU)" {
    var envb: [8]u8 = undefined;
    if (GetEnvironmentVariableA("RAVE_MATE_MFENC_RCPROBE", &envb, envb.len) == 0) return error.SkipZigTest;
    coInitMTA();
    if (failed(MFStartup(MF_VERSION, MFSTARTUP_LITE))) return error.SkipZigTest;
    var vendor: u32 = 0;
    const ad = Enc.pickDefaultAdapter(&vendor) orelse return error.SkipZigTest;
    defer release(ad);
    var dev: ?*ID3D11Device = null;
    var dctx: ?*ID3D11DeviceContext = null;
    if (failed(D3D11CreateDevice(@ptrCast(ad), D3D_DRIVER_TYPE_UNKNOWN, null, D3D11_CREATE_DEVICE_BGRA_SUPPORT | D3D11_CREATE_DEVICE_VIDEO_SUPPORT, null, 0, D3D11_SDK_VERSION, &dev, null, &dctx))) return error.SkipZigTest;
    defer {
        release(dctx.?);
        release(dev.?);
    }
    var mtraw: ?*anyopaque = null;
    if (!failed(qi(dev.?, &IID_ID3D10Multithread, &mtraw)) and mtraw != null) {
        const mt10: *ID3D10Multithread = @ptrCast(@alignCast(mtraw.?));
        _ = mt10.v.SetMultithreadProtected(@ptrCast(mt10), 1);
        release(mt10);
    }
    var token: u32 = 0;
    var dm: ?*IMFDXGIDeviceManager = null;
    if (failed(MFCreateDXGIDeviceManager(&token, &dm)) or dm == null) return error.SkipZigTest;
    defer release(dm.?);
    _ = dm.?.v.ResetDevice(@ptrCast(dm.?), @ptrCast(dev.?), token);
    std.debug.print("\nadapter vendor 0x{x}\n", .{vendor});
    probePreType(dm.?);
    probeCodec(dm.?, &MFVideoFormat_H264, "H.264");
    probeCodec(dm.?, &MFVideoFormat_HEVC, "HEVC");
}
