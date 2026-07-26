// mf_shim.cpp - Media Foundation hardware H.264 encode pipeline (see mf_shim.h).
// Single translation unit; INITGUID instantiates every referenced GUID here so no
// mfuuid/dxguid import-lib gaps can break MinGW linking.
#include <initguid.h>
#include <windows.h>
#include <cguid.h>
#include <d3d11.h>
#include <dxgi1_2.h>
#include <mfapi.h>
#include <mfidl.h>
#include <mftransform.h>
#include <mferror.h>
#include <strmif.h>  // ICodecAPI (DirectShow interface reused by MF encoders)
#include <codecapi.h>
#include <stdio.h>
#include <string.h>
#include <ctype.h>
#include <stdlib.h>
#include <setjmp.h>

#include "mf_shim.h"

// MinGW header gaps: define locally when absent (values are ABI constants).
#ifndef MF_E_TRANSFORM_STREAM_CHANGE
#define MF_E_TRANSFORM_STREAM_CHANGE ((HRESULT)0xC00D6D61L)
#endif
#ifndef MF_E_NO_EVENTS_AVAILABLE
#define MF_E_NO_EVENTS_AVAILABLE ((HRESULT)0xC00D3E80L)
#endif

// Attribute/format GUIDs some MinGW libs lack as link symbols - INITGUID + DEFINE_GUID
// makes them TU-local. Names prefixed k to never collide with header externs.
DEFINE_GUID(kMF_TRANSFORM_ASYNC_UNLOCK, 0xe5666d6b, 0x3422, 0x4eb6, 0xa4, 0x21, 0xda, 0x7d, 0xb1, 0xf8, 0xe2, 0x07);
DEFINE_GUID(kMF_LOW_LATENCY, 0x9c27891a, 0xed7a, 0x40e1, 0x88, 0xe8, 0xb2, 0x27, 0x27, 0xa0, 0x24, 0xee);
DEFINE_GUID(kMF_SA_D3D11_BINDFLAGS, 0xeacf97ad, 0x065c, 0x4408, 0xbe, 0xe3, 0xfd, 0xcb, 0xfd, 0x12, 0x8b, 0xe2);
DEFINE_GUID(kMFSampleExtension_CleanPoint, 0x9cdf01d8, 0xa0f0, 0x43ba, 0xb0, 0x77, 0xea, 0xa0, 0x6c, 0xbd, 0x72, 0x8a);
DEFINE_GUID(kCLSID_VideoProcessorMFT, 0x88753b26, 0x5b24, 0x49bd, 0xb2, 0xe7, 0x0c, 0x44, 0x5c, 0x78, 0xc9, 0x82);
DEFINE_GUID(kMFVideoFormat_NV12, 0x3231564e, 0x0000, 0x0010, 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71);
DEFINE_GUID(kMFVideoFormat_H264, 0x34363248, 0x0000, 0x0010, 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71);
DEFINE_GUID(kMFVideoFormat_ARGB32, 0x00000015, 0x0000, 0x0010, 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71);
DEFINE_GUID(kMFVideoFormat_ABGR32, 0x00000020, 0x0000, 0x0010, 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71);
DEFINE_GUID(kMFMediaType_Video, 0x73646976, 0x0000, 0x0010, 0x80, 0x00, 0x00, 0xaa, 0x00, 0x38, 0x9b, 0x71);
DEFINE_GUID(kMF_MT_MAJOR_TYPE, 0x48eba18e, 0xf8c9, 0x4687, 0xbf, 0x11, 0x0a, 0x74, 0xc9, 0xf9, 0x6a, 0x8f);
DEFINE_GUID(kMF_MT_SUBTYPE, 0xf7e34c9a, 0x42e8, 0x4714, 0xb7, 0x4b, 0xcb, 0x29, 0xd7, 0x2c, 0x35, 0xe5);
DEFINE_GUID(kMF_MT_FRAME_SIZE, 0x1652c33d, 0xd6b2, 0x4012, 0xb8, 0x34, 0x72, 0x03, 0x08, 0x49, 0xa3, 0x7d);
DEFINE_GUID(kMF_MT_FRAME_RATE, 0xc459a2e8, 0x3d2c, 0x4e44, 0xb1, 0x32, 0xfe, 0xe5, 0x15, 0x6c, 0x7b, 0xb0);
DEFINE_GUID(kMF_MT_PIXEL_ASPECT_RATIO, 0xc6376a1e, 0x8d0a, 0x4027, 0xbe, 0x45, 0x6d, 0x9a, 0x0a, 0xd3, 0x9b, 0xb6);
DEFINE_GUID(kMF_MT_AVG_BITRATE, 0x20332624, 0xfb0d, 0x4d9e, 0xbd, 0x0d, 0xcb, 0xf6, 0x78, 0x6c, 0x10, 0x2e);
DEFINE_GUID(kMF_MT_INTERLACE_MODE, 0xe2724bb8, 0xe676, 0x4806, 0xb4, 0xb2, 0xa8, 0xd6, 0xef, 0xb4, 0x4c, 0xcd);
DEFINE_GUID(kMF_MT_MPEG2_PROFILE, 0xad76a80b, 0x2d5c, 0x4e0b, 0xb3, 0x75, 0x64, 0xe5, 0x20, 0x13, 0x70, 0x36);
DEFINE_GUID(kMF_MT_MPEG2_LEVEL, 0x96f66574, 0x11c5, 0x4015, 0x86, 0x66, 0xbf, 0xf5, 0x16, 0x43, 0x6d, 0xa7);
DEFINE_GUID(kMF_SA_D3D11_AWARE, 0x206b4fc8, 0xfcf9, 0x4c51, 0xaf, 0xe3, 0x97, 0x64, 0x36, 0x9e, 0x33, 0xa0);
DEFINE_GUID(kMF_MT_ALL_SAMPLES_INDEPENDENT, 0xc9173739, 0x5e56, 0x461c, 0xb7, 0x13, 0x46, 0xfb, 0x99, 0x5c, 0xb9, 0x5f);
DEFINE_GUID(kMFT_FRIENDLY_NAME_Attribute, 0x314ffbae, 0x5b41, 0x4c95, 0x9c, 0x19, 0x4e, 0x7d, 0x58, 0x6f, 0xac, 0xe3);
DEFINE_GUID(kCODECAPI_AVEncCommonRateControlMode, 0x1c0608e9, 0x370c, 0x4710, 0x8a, 0x58, 0xcb, 0x61, 0x81, 0xc4, 0x24, 0x23);
DEFINE_GUID(kCODECAPI_AVEncCommonMeanBitRate, 0xf7222374, 0x2144, 0x4815, 0xb5, 0x50, 0xa3, 0x7f, 0x8e, 0x12, 0xee, 0x52);
DEFINE_GUID(kCODECAPI_AVEncMPVGOPSize, 0x95f31b26, 0x95a4, 0x41aa, 0x93, 0x03, 0x24, 0x6a, 0x7f, 0xc6, 0xee, 0xf1);
DEFINE_GUID(kCODECAPI_AVEncCommonLowLatency, 0x9d3ecd55, 0x89e8, 0x490a, 0x97, 0x0a, 0x0c, 0x95, 0x48, 0xd5, 0xa5, 0x6e);
DEFINE_GUID(kCODECAPI_AVEncMPVDefaultBPictureCount, 0x8d390aac, 0xdc5c, 0x4200, 0xb5, 0x7f, 0x81, 0x4d, 0x04, 0xba, 0xba, 0xb2);
DEFINE_GUID(kCODECAPI_AVEncVideoForceKeyFrame, 0x398c1b98, 0x8353, 0x475a, 0x9e, 0xf2, 0x8f, 0x26, 0x5d, 0x26, 0x03, 0x45);
DEFINE_GUID(kIID_ICodecAPI, 0x901db4c7, 0x31ce, 0x41a2, 0x85, 0xdc, 0x8f, 0xa0, 0xbf, 0x41, 0xb8, 0xda);
DEFINE_GUID(kIID_IMFMediaEventGenerator, 0x2cd0bd52, 0xbcd5, 0x4b89, 0xb6, 0x2c, 0xea, 0xdc, 0x0c, 0x03, 0x1e, 0x7b);
DEFINE_GUID(kIID_ID3D10Multithread, 0x9b7e4e00, 0x342c, 0x4106, 0xa1, 0x9f, 0x4f, 0x27, 0x04, 0xf6, 0x89, 0xf0);
DEFINE_GUID(kIID_ID3D11Texture2D, 0x6f15aaf2, 0xd208, 0x4e89, 0x9a, 0xb4, 0x48, 0x95, 0x35, 0xd3, 0x4f, 0x9c);
DEFINE_GUID(kIID_IMF2DBuffer, 0x7dc9d5f9, 0x9ed9, 0x44ec, 0x9b, 0xbf, 0x06, 0x00, 0xbb, 0x58, 0x9f, 0xbb);
DEFINE_GUID(kIID_ID3D11VideoDevice, 0x10ec4d5b, 0x975a, 0x4689, 0xb9, 0xe4, 0xd0, 0xaa, 0xc3, 0x0f, 0xe3, 0x33);
DEFINE_GUID(kIID_ID3D11VideoContext, 0x61f21c45, 0x3c0e, 0x4a74, 0x9c, 0xea, 0x67, 0x10, 0x0d, 0x9a, 0xd5, 0xe4);

#define AURING_CAP 32
#define FEED_WAIT_MS 2000

struct auEntry {
    uint8_t* data;
    int      size;
    int64_t  pts100;
    int      key;
};

#define NVPOOL 8 // NV12 round-robin depth (encoder queue is 2-4 deep; 8 = safe slack)

struct mfenc {
    ID3D11Device*           dev;
    ID3D11DeviceContext*    ctx;
    IMFDXGIDeviceManager*   devmgr;
    UINT                    devmgrToken;
    // D3D11 Video API CSC (RGBA -> NV12 + scale): deterministic, no XVP MFT quirks
    ID3D11VideoDevice*      vdev;
    ID3D11VideoContext*     vctx;
    ID3D11VideoProcessorEnumerator* vpe;
    ID3D11VideoProcessor*   vproc;
    ID3D11VideoProcessorInputView*  inView;
    ID3D11Texture2D*        nvTex[NVPOOL];
    ID3D11VideoProcessorOutputView* nvView[NVPOOL];
    IMFSample*              nvSample[NVPOOL]; // prebuilt sample+DXGI buffer per pool texture
    int                     nvIdx;
    IMFTransform*           enc;  // async hardware encoder MFT
    IMFMediaEventGenerator* evgen;
    ICodecAPI*              capi;
    ID3D11Texture2D*        inTex;
    uint8_t*                swz;   // swizzle scratch (BGRA input negotiated) - inW*inH*4
    int                     inW, inH, outW, outH;
    int                     fpsN, fpsD;
    int64_t                 dur100;
    int                     bgraIn;       // 1 = VP wants ARGB32 (BGRA memory): swizzle on upload
    int                     vpProvides;   // VP allocates its own output samples
    int                     encProvides;  // encoder allocates its own output samples
    DWORD                   encOutSize;   // CPU output buffer size when !encProvides
    int                     needInput;    // pending METransformNeedInput credits
    int                     drainDone;
    int                     forceIDR;
    char                    name[128];
    HRESULT                 lastHR; // last pipeline failure (diagnostic via mf_enc_last_hr)
    auEntry                 ring[AURING_CAP]; // pending AUs, FIFO; full = stop harvesting (Go drains after every feed)
    int                     rHead, rCount;
    int                     comInit; // CoInitializeEx succeeded on this thread
    int                     mfInit;  // MFStartup succeeded
};

static void setErr(char* errbuf, int errcap, const char* stage, HRESULT hr) {
    if (errbuf && errcap > 0) snprintf(errbuf, (size_t)errcap, "%s hr=0x%08lx", stage, (unsigned long)hr);
}

template <class T> static void rel(T*& p) { if (p) { p->Release(); p = NULL; } }

// ── driver-fault guard ──
// The 4K60 field crash (build 157) was an access violation INSIDE a vendor encoder MFT during
// mf_enc_open - a c0000005 no HRESULT check can catch; it killed the media child before the Go
// ffmpeg fallback could run. A scoped vectored handler converts any such fault inside the open
// path into a clean failure: setjmp before the driver-touching body, VEH longjmps back on fault.
// On fault the partial pipeline is deliberately LEAKED (Release into a faulted driver can fault
// again) and the shim is POISONED - every later available/open call fails fast, so this process
// never re-enters the broken driver and the ffmpeg engine carries the routes from then on.
static volatile LONG g_shimPoisoned;    // 1 = a driver faulted once: native engine off for this process
static __thread int g_guardArmed;       // fault guard active on THIS thread
static __thread jmp_buf g_guardJmp;
static __thread DWORD g_guardCode;      // exception code captured by the VEH

static LONG CALLBACK mfFaultVEH(EXCEPTION_POINTERS* xp) {
    if (!g_guardArmed) return EXCEPTION_CONTINUE_SEARCH; // not ours (Go installs its own handlers)
    DWORD c = xp->ExceptionRecord->ExceptionCode;
    switch (c) {
    case EXCEPTION_ACCESS_VIOLATION:
    case EXCEPTION_ILLEGAL_INSTRUCTION:
    case EXCEPTION_PRIV_INSTRUCTION:
    case EXCEPTION_INT_DIVIDE_BY_ZERO:
    case EXCEPTION_ARRAY_BOUNDS_EXCEEDED:
    case 0xC0000374L: // STATUS_HEAP_CORRUPTION
        break;
    default: // breakpoints, C++ EH, Go's own traps: never intercept
        return EXCEPTION_CONTINUE_SEARCH;
    }
    g_guardArmed = 0;
    g_guardCode = c;
    longjmp(g_guardJmp, 1);
}

static void guardInstall(void) {
    static volatile LONG once;
    if (InterlockedCompareExchange((LONG*)&once, 1, 0) == 0)
        AddVectoredExceptionHandler(1 /*call first*/, mfFaultVEH);
}

// guardArm primes the per-thread jump target; Frame=0 forces msvcrt longjmp to plain-restore
// instead of RtlUnwindEx through driver frames. Pair every arm with g_guardArmed=0 before return.
#define guardArm() (((_JUMP_BUFFER*)&g_guardJmp)->Frame = 0, g_guardArmed = 1)

// stageTrace (RAVE_MATE_MFENC_TRACE=1) breadcrumbs every driver-touching open stage to stderr.
// The encoder child runs with it always on: when a vendor driver kills the child, the parent's
// captured stderr tail names the exact faulting call - field root-cause without a debugger.
static int g_trace = -1;
static void stageTrace(const char* s) {
    if (g_trace < 0) g_trace = getenv("RAVE_MATE_MFENC_TRACE") ? 1 : 0;
    if (!g_trace) return;
    fprintf(stderr, "mfenc stage: %s\n", s);
    fflush(stderr);
}

// h264LevelFor returns the minimal eAVEncH264VLevel for the geometry (H.264 Table A-1
// MB/frame + MB/s limits). 4K60 needs level 5.2; a driver deriving the level itself from an
// UNSET media-type field can size internal buffers for a lower level and fault at 4K - set
// it explicitly.
static UINT32 h264LevelFor(int w, int h, int fpsN, int fpsD) {
    int64_t mbs = (int64_t)((w + 15) / 16) * (int64_t)((h + 15) / 16);
    int64_t mbps = mbs * fpsN / (fpsD > 0 ? fpsD : 1);
    static const struct { UINT32 lvl; int64_t maxMBs, maxMBps; } t[] = {
        {31, 3600, 108000}, {32, 5120, 216000}, {40, 8192, 245760}, {41, 8192, 245760},
        {42, 8704, 522240}, {50, 22080, 589824}, {51, 36864, 983040}, {52, 36864, 2073600},
    };
    for (size_t i = 0; i < sizeof(t) / sizeof(t[0]); i++)
        if (mbs <= t[i].maxMBs && mbps <= t[i].maxMBps) return t[i].lvl;
    return 52;
}

void mf_swizzle_rgba_bgra(uint8_t* dst, const uint8_t* src, int npx) {
    for (int i = 0; i < npx; i++) { // auto-vectorizes; ~memcpy speed at -O2
        dst[0] = src[2]; dst[1] = src[1]; dst[2] = src[0]; dst[3] = src[3];
        dst += 4; src += 4;
    }
}

// mtVideo builds a video media type (rational fps; square PAR; progressive).
static HRESULT mtVideo(const GUID& sub, int w, int h, int fpsN, int fpsD, IMFMediaType** out) {
    IMFMediaType* mt = NULL;
    HRESULT hr = MFCreateMediaType(&mt);
    if (FAILED(hr)) return hr;
    mt->SetGUID(kMF_MT_MAJOR_TYPE, kMFMediaType_Video);
    mt->SetGUID(kMF_MT_SUBTYPE, sub);
    mt->SetUINT64(kMF_MT_FRAME_SIZE, ((UINT64)(UINT32)w << 32) | (UINT32)h);
    mt->SetUINT64(kMF_MT_FRAME_RATE, ((UINT64)(UINT32)fpsN << 32) | (UINT32)fpsD);
    mt->SetUINT64(kMF_MT_PIXEL_ASPECT_RATIO, ((UINT64)1 << 32) | 1);
    mt->SetUINT32(kMF_MT_INTERLACE_MODE, 2 /*MFVideoInterlace_Progressive*/);
    *out = mt;
    return S_OK;
}

// vendorTag maps a DXGI VendorID to the substring that vendor's encoder MFT friendly name carries
// ("NVIDIA H.264 Encoder MFT", "AMD H.264 Hardware MFT Encoder", "Intel(R) Quick Sync ..."). NULL =
// unknown vendor -> no name filtering (any MFT that accepts our device manager is accepted).
static const char* vendorTag(UINT vid) {
    switch (vid) {
    case 0x10DE: return "NVIDIA";
    case 0x1002: return "AMD";
    case 0x1022: return "AMD";
    case 0x8086: return "Intel";
    }
    return NULL;
}

// containsNoCase: case-insensitive substring test (empty needle matches).
static int containsNoCase(const char* hay, const char* needle) {
    if (!hay || !needle || !*needle) return 1;
    size_t nl = strlen(needle);
    for (const char* p = hay; *p; p++) {
        size_t i = 0;
        while (i < nl && p[i] && tolower((unsigned char)p[i]) == tolower((unsigned char)needle[i])) i++;
        if (i == nl) return 1;
    }
    return 0;
}

// findAdapter locates the DXGI adapter whose LUID equals luid (HighPart<<32 | LowPart - the same
// int64 encoderscan.LUIDInt64 produces). luid 0 or no match -> *out NULL (default adapter). Caller
// releases *out. *vendorId gets the adapter's PCI vendor id (0 when unresolved).
static void findAdapter(int64_t luid, IDXGIAdapter1** out, UINT* vendorId) {
    *out = NULL;
    if (vendorId) *vendorId = 0;
    if (luid == 0) return;
    IDXGIFactory1* fac = NULL;
    if (FAILED(CreateDXGIFactory1(__uuidof(IDXGIFactory1), (void**)&fac)) || !fac) return;
    for (UINT i = 0;; i++) {
        IDXGIAdapter1* ad = NULL;
        if (fac->EnumAdapters1(i, &ad) != S_OK || !ad) break;
        DXGI_ADAPTER_DESC1 d;
        if (SUCCEEDED(ad->GetDesc1(&d))) {
            int64_t key = ((int64_t)d.AdapterLuid.HighPart << 32) | (int64_t)(uint32_t)d.AdapterLuid.LowPart;
            if (key == luid) {
                if (vendorId) *vendorId = d.VendorId;
                *out = ad;
                fac->Release();
                return;
            }
        }
        ad->Release();
    }
    fac->Release();
}

// pickDefaultAdapter chooses the adapter a luid==0 open binds. Blind adapter-0 creation
// (NULL + DRIVER_TYPE_HARDWARE) follows the primary display - on rigs with a virtual display
// adapter (Parsec/spacedesk) that can be a device no encoder silicon lives on, and a vendor
// MFT handed a foreign device's manager can FAULT instead of refusing it (the 4K60 field
// crash class). Pass 0: first non-software adapter of a known encode vendor. Pass 1: first
// non-software adapter. *out NULL = keep the system HARDWARE default.
static void pickDefaultAdapter(IDXGIAdapter1** out, UINT* vendorId) {
    *out = NULL;
    if (vendorId) *vendorId = 0;
    IDXGIFactory1* fac = NULL;
    if (FAILED(CreateDXGIFactory1(__uuidof(IDXGIFactory1), (void**)&fac)) || !fac) return;
    for (int pass = 0; pass < 2 && !*out; pass++) {
        for (UINT i = 0;; i++) {
            IDXGIAdapter1* ad = NULL;
            if (fac->EnumAdapters1(i, &ad) != S_OK || !ad) break;
            DXGI_ADAPTER_DESC1 d;
            if (SUCCEEDED(ad->GetDesc1(&d)) && !(d.Flags & DXGI_ADAPTER_FLAG_SOFTWARE) &&
                (pass == 1 || vendorTag(d.VendorId) != NULL)) {
                if (vendorId) *vendorId = d.VendorId;
                *out = ad;
                break;
            }
            ad->Release();
        }
    }
    fac->Release();
}

// kKnownVendorTags: vendor substrings hardware encoder MFT friendly names carry.
static const char* kKnownVendorTags[] = { "NVIDIA", "AMD", "Intel" };

// vendorMismatch: fn names a DIFFERENT known vendor than the device's tag. Cross-vendor
// SET_D3D_MANAGER is exactly where broken vendor MFTs fault instead of failing - never offer
// the manager across vendors. Unknown names stay eligible (SET_D3D_MANAGER stays the gate).
static int vendorMismatch(const char* fn, const char* deviceTag) {
    if (!deviceTag || !*deviceTag || !fn || !*fn) return 0;
    for (size_t i = 0; i < sizeof(kKnownVendorTags) / sizeof(kKnownVendorTags[0]); i++) {
        if (strcmp(kKnownVendorTags[i], deviceTag) == 0) continue;
        if (containsNoCase(fn, kKnownVendorTags[i])) return 1;
    }
    return 0;
}

// enumHWEncoder activates a hardware encoder MFT for outSub. out==NULL only reports existence.
// vendorHint (may be NULL) is the pinned adapter's vendor tag: candidates whose friendly name
// carries it are tried FIRST, so a two-vendor machine binds the MFT that belongs to the chosen
// adapter instead of blind acts[0]. devmgr (may be NULL) is the device manager built on that
// adapter - an MFT that refuses it (foreign adapter) is skipped, which is the hard gate.
static int enumHWEncoder(const GUID& outSub, IMFTransform** out, char* name, int namecap,
                         const char* vendorHint, IMFDXGIDeviceManager* devmgr) {
    MFT_REGISTER_TYPE_INFO ti = { kMFMediaType_Video, kMFVideoFormat_NV12 };
    MFT_REGISTER_TYPE_INFO to = { kMFMediaType_Video, outSub };
    IMFActivate** acts = NULL;
    UINT32 n = 0;
    HRESULT hr = MFTEnumEx(MFT_CATEGORY_VIDEO_ENCODER,
        MFT_ENUM_FLAG_HARDWARE | MFT_ENUM_FLAG_SORTANDFILTER, &ti, &to, &acts, &n);
    if (FAILED(hr) || n == 0) return 0;
    if (!out) { // availability probe only
        for (UINT32 i = 0; i < n; i++) acts[i]->Release();
        CoTaskMemFree(acts);
        return 1;
    }
    int ok = 0;
    for (int pass = 0; pass < 2 && !ok; pass++) {
        if (pass == 0 && (!vendorHint || !*vendorHint)) continue; // no hint: one unfiltered pass
        for (UINT32 i = 0; i < n && !ok; i++) {
            char fn[128] = {0};
            WCHAR wn[128] = {0};
            UINT32 wl = 0;
            if (SUCCEEDED(acts[i]->GetString(kMFT_FRIENDLY_NAME_Attribute, wn, 127, &wl)))
                WideCharToMultiByte(CP_UTF8, 0, wn, -1, fn, sizeof(fn) - 1, NULL, NULL);
            if (pass == 0 && !containsNoCase(fn, vendorHint)) continue;
            if (vendorMismatch(fn, vendorHint)) continue; // both passes: no cross-vendor manager handoff
            IMFTransform* t = NULL;
            if (FAILED(acts[i]->ActivateObject(__uuidof(IMFTransform), (void**)&t)) || !t) continue;
            UINT32 aware = 0;
            IMFAttributes* ea = NULL;
            if (SUCCEEDED(t->GetAttributes(&ea)) && ea) {
                ea->GetUINT32(kMF_SA_D3D11_AWARE, &aware);
                ea->SetUINT32(kMF_TRANSFORM_ASYNC_UNLOCK, 1);
                ea->SetUINT32(kMF_LOW_LATENCY, 1);
                ea->Release();
            }
            if (devmgr && !aware) { // HW MFTs MUST publish MF_SA_D3D11_AWARE (Chromium enforces
                t->Release();       // the same) - one that doesn't cannot take our device manager
                acts[i]->ShutdownObject();
                continue;
            }
            if (devmgr && FAILED(t->ProcessMessage(MFT_MESSAGE_SET_D3D_MANAGER, (ULONG_PTR)devmgr))) {
                t->Release(); // will not run on the chosen adapter's device
                acts[i]->ShutdownObject(); // activated-but-rejected: full MF activate teardown
                continue;
            }
            *out = t;
            ok = 1;
            if (name && namecap > 0) {
                strncpy(name, fn, (size_t)namecap - 1);
                name[namecap - 1] = 0;
            }
        }
    }
    for (UINT32 i = 0; i < n; i++) acts[i]->Release();
    CoTaskMemFree(acts);
    return ok;
}

static int availImpl(void) {
    HRESULT ci = CoInitializeEx(NULL, COINIT_MULTITHREADED);
    int comHere = (ci == S_OK || ci == S_FALSE);
    HRESULT hr = MFStartup(MF_VERSION, MFSTARTUP_LITE);
    if (FAILED(hr)) { if (comHere && ci == S_OK) CoUninitialize(); return 0; }
    int ok = 0;
    ID3D11Device* dev = NULL;
    D3D_FEATURE_LEVEL fl;
    hr = D3D11CreateDevice(NULL, D3D_DRIVER_TYPE_HARDWARE, NULL,
        D3D11_CREATE_DEVICE_BGRA_SUPPORT | D3D11_CREATE_DEVICE_VIDEO_SUPPORT,
        NULL, 0, D3D11_SDK_VERSION, &dev, &fl, NULL);
    if (SUCCEEDED(hr)) {
        ok = enumHWEncoder(kMFVideoFormat_H264, NULL, NULL, 0, NULL, NULL);
        dev->Release();
    }
    MFShutdown();
    if (comHere && ci == S_OK) CoUninitialize();
    return ok;
}

int mf_shim_available(void) {
    if (g_shimPoisoned) return 0;
    guardInstall();
    if (setjmp(g_guardJmp) != 0) { // driver faulted during the probe: report unavailable, stay off
        g_guardArmed = 0;
        InterlockedExchange((LONG*)&g_shimPoisoned, 1);
        return 0;
    }
    guardArm();
    int ok = availImpl();
    g_guardArmed = 0;
    return ok;
}

// pumpEvents drains pending encoder events without blocking. Returns <0 on hard error.
static int pumpEvents(mfenc* e);

// harvestOutput pulls encoded AUs. Async MFTs allow exactly ONE ProcessOutput per
// METransformHaveOutput event (more = E_UNEXPECTED) - one-shot there; sync MFTs loop
// until NEED_MORE_INPUT.
static int harvestOutput(mfenc* e) {
    for (;;) {
        if (e->rCount >= AURING_CAP) return 0; // Go drains after every feed; never wedge
        MFT_OUTPUT_DATA_BUFFER ob;
        memset(&ob, 0, sizeof(ob));
        IMFSample* cpuSample = NULL;
        if (!e->encProvides) {
            IMFMediaBuffer* mb = NULL;
            // CPU output buffer scales with the frame: a 4K IDR AU exceeds the old 1 MB floor
            // (MF_E_BUFFERTOOSMALL would kill the route needlessly).
            DWORD floorSz = (DWORD)e->outW * (DWORD)e->outH;
            if (floorSz < (1u << 20)) floorSz = 1u << 20;
            DWORD sz = e->encOutSize > floorSz ? e->encOutSize : floorSz;
            if (FAILED(MFCreateMemoryBuffer(sz, &mb))) return -1;
            if (FAILED(MFCreateSample(&cpuSample))) { mb->Release(); return -1; }
            cpuSample->AddBuffer(mb);
            mb->Release();
            ob.pSample = cpuSample;
        }
        DWORD status = 0;
        HRESULT hr = e->enc->ProcessOutput(0, 1, &ob, &status);
        if (hr == MF_E_TRANSFORM_STREAM_CHANGE) {
            IMFMediaType* mt = NULL;
            if (SUCCEEDED(e->enc->GetOutputAvailableType(0, 0, &mt))) {
                e->enc->SetOutputType(0, mt, 0);
                mt->Release();
            }
            rel(cpuSample);
            continue;
        }
        if (hr == MF_E_TRANSFORM_NEED_MORE_INPUT) { rel(cpuSample); return 0; }
        if (FAILED(hr)) { e->lastHR = hr; rel(cpuSample); return -1; }
        IMFSample* s = ob.pSample;
        if (ob.pEvents) ob.pEvents->Release();
        if (!s) return 0;
        LONGLONG pts = 0;
        s->GetSampleTime(&pts);
        UINT32 clean = 0;
        s->GetUINT32(kMFSampleExtension_CleanPoint, &clean);
        IMFMediaBuffer* buf = NULL;
        if (SUCCEEDED(s->ConvertToContiguousBuffer(&buf))) {
            BYTE* p = NULL;
            DWORD len = 0;
            if (SUCCEEDED(buf->Lock(&p, NULL, &len)) && len > 0) {
                auEntry* ae = &e->ring[(e->rHead + e->rCount) % AURING_CAP];
                ae->data = (uint8_t*)malloc(len);
                if (ae->data) {
                    memcpy(ae->data, p, len);
                    ae->size = (int)len;
                    ae->pts100 = (int64_t)pts;
                    ae->key = clean ? 1 : 0;
                    e->rCount++;
                }
                buf->Unlock();
            }
            buf->Release();
        }
        s->Release(); // provided samples: MFT gave us a ref; CPU sample: ours
        if (e->evgen) return 0; // async: one output per HaveOutput event
    }
}

static int pumpEvents(mfenc* e) {
    if (!e->evgen) return harvestOutput(e); // sync MFT: no events, just drain output
    for (;;) {
        IMFMediaEvent* ev = NULL;
        HRESULT hr = e->evgen->GetEvent(MF_EVENT_FLAG_NO_WAIT, &ev);
        if (hr == MF_E_NO_EVENTS_AVAILABLE) return 0;
        if (FAILED(hr)) return -1;
        MediaEventType met = MEUnknown;
        ev->GetType(&met);
        ev->Release();
        switch (met) {
        case METransformNeedInput:
            e->needInput++;
            break;
        case METransformHaveOutput:
            if (harvestOutput(e) < 0) return -1;
            break;
        case METransformDrainComplete:
            e->drainDone = 1;
            break;
        default:
            break;
        }
    }
}

// vpBlt converts the uploaded RGBA texture to the next pooled NV12 texture (GPU CSC +
// scale) and returns that slot's prebuilt sample (POOL-OWNED: caller must NOT release;
// take pool index for time-stamping).
static HRESULT vpBlt(mfenc* e, int* slot) {
    int i = e->nvIdx;
    e->nvIdx = (e->nvIdx + 1) % NVPOOL;
    D3D11_VIDEO_PROCESSOR_STREAM s;
    memset(&s, 0, sizeof(s));
    s.Enable = TRUE;
    s.pInputSurface = e->inView;
    HRESULT hr = e->vctx->VideoProcessorBlt(e->vproc, e->nvView[i], 0, 1, &s);
    if (FAILED(hr)) return hr;
    *slot = i;
    return S_OK;
}

// openDevice creates dev/ctx + the video interfaces + a VideoProcessor enumerator for the
// route geometry on ONE adapter (NULL = system HARDWARE default; non-NULL mandates
// DRIVER_TYPE_UNKNOWN per the D3D11CreateDevice contract). The VP enumerator doubles as the
// video-capability gate: a device that cannot run the VP at this geometry (virtual display
// adapters) is rejected HERE, before any vendor MFT is offered its device manager. Failure
// releases everything it created; *stage names the failing call.
static HRESULT openDevice(mfenc* e, IDXGIAdapter1* adapter, int inW, int inH, int outW, int outH,
                          int fpsN, int fpsD, const char** stage) {
    D3D_FEATURE_LEVEL fl;
    *stage = "D3D11CreateDevice";
    stageTrace(adapter ? "D3D11CreateDevice(pinned adapter)" : "D3D11CreateDevice(default)");
    HRESULT hr = D3D11CreateDevice(adapter, adapter ? D3D_DRIVER_TYPE_UNKNOWN : D3D_DRIVER_TYPE_HARDWARE, NULL,
        D3D11_CREATE_DEVICE_BGRA_SUPPORT | D3D11_CREATE_DEVICE_VIDEO_SUPPORT,
        NULL, 0, D3D11_SDK_VERSION, &e->dev, &fl, &e->ctx);
    if (FAILED(hr)) return hr;
    ID3D10Multithread* mt10 = NULL;
    if (SUCCEEDED(e->dev->QueryInterface(kIID_ID3D10Multithread, (void**)&mt10))) {
        mt10->SetMultithreadProtected(TRUE);
        mt10->Release();
    }
    *stage = "QI ID3D11VideoDevice";
    stageTrace(*stage);
    hr = e->dev->QueryInterface(kIID_ID3D11VideoDevice, (void**)&e->vdev);
    if (SUCCEEDED(hr)) {
        *stage = "QI ID3D11VideoContext";
        hr = e->ctx->QueryInterface(kIID_ID3D11VideoContext, (void**)&e->vctx);
    }
    if (SUCCEEDED(hr)) {
        D3D11_VIDEO_PROCESSOR_CONTENT_DESC cd;
        memset(&cd, 0, sizeof(cd));
        cd.InputFrameFormat = D3D11_VIDEO_FRAME_FORMAT_PROGRESSIVE;
        cd.InputFrameRate.Numerator = (UINT)fpsN;
        cd.InputFrameRate.Denominator = (UINT)fpsD;
        cd.InputWidth = (UINT)inW;
        cd.InputHeight = (UINT)inH;
        cd.OutputFrameRate = cd.InputFrameRate;
        cd.OutputWidth = (UINT)outW;
        cd.OutputHeight = (UINT)outH;
        cd.Usage = D3D11_VIDEO_USAGE_PLAYBACK_NORMAL;
        *stage = "CreateVideoProcessorEnumerator";
        stageTrace(*stage);
        hr = e->vdev->CreateVideoProcessorEnumerator(&cd, &e->vpe);
    }
    if (FAILED(hr)) {
        rel(e->vpe);
        rel(e->vctx);
        rel(e->vdev);
        rel(e->ctx);
        rel(e->dev);
    }
    return hr;
}

// faultThreadProc AVs on a thread the fault guard cannot reach (test hook, see below).
static DWORD WINAPI faultThreadProc(LPVOID) {
    volatile int* p = NULL;
    *p = 1;
    return 0;
}

// openImpl is mf_enc_open's body; it runs under the driver-fault guard armed by the wrapper.
static mfenc* openImpl(int64_t adapterLuid, int inW, int inH, int outW, int outH,
                       int fpsN, int fpsD, int bitrateKbps, int gopFrames,
                       char* errbuf, int errcap) {
    if (getenv("RAVE_MATE_MFENC_FAULT_INJECT_THREAD")) {
        // FIELD failure mode test hook: vendor MFTs fault on THEIR OWN worker threads,
        // where the thread-local guard context is unarmed - the process dies. That is why
        // first-time opens run in a sacrificial probe child, never in the media child.
        HANDLE h = CreateThread(NULL, 0, faultThreadProc, NULL, 0, NULL);
        if (h) { WaitForSingleObject(h, 2000); CloseHandle(h); }
    }
    if (getenv("RAVE_MATE_MFENC_FAULT_INJECT")) { // guard-path test hook: calling-thread AV
        volatile int* p = NULL;
        *p = 1;
    }
    if (inW <= 0 || inH <= 0 || outW <= 0 || outH <= 0 || fpsN <= 0) {
        setErr(errbuf, errcap, "args", E_INVALIDARG);
        return NULL;
    }
    if (fpsD <= 0) fpsD = 1;
    mfenc* e = (mfenc*)calloc(1, sizeof(mfenc));
    if (!e) return NULL;
    e->inW = inW; e->inH = inH; e->outW = outW; e->outH = outH;
    e->fpsN = fpsN; e->fpsD = fpsD;
    e->dur100 = (int64_t)(10000000.0 * fpsD / fpsN);

    HRESULT ci = CoInitializeEx(NULL, COINIT_MULTITHREADED);
    e->comInit = (ci == S_OK) ? 1 : 0;
    stageTrace("MFStartup");
    HRESULT hr = MFStartup(MF_VERSION, MFSTARTUP_LITE);
    if (FAILED(hr)) { setErr(errbuf, errcap, "MFStartup", hr); mf_enc_close(e); return NULL; }
    e->mfInit = 1;

    // Device selection (WP-3 + 4K60 crash fix): a pinned adapter LUID creates the device ON
    // THAT ADAPTER; luid 0 picks a DELIBERATE default (known encode vendor first) instead of
    // blind adapter 0 - on rigs with a virtual display adapter (Parsec) the blind default can
    // be a device no encoder MFT can run on, and a vendor MFT handed that foreign device's
    // manager faults instead of refusing it. Unusable adapter degrades to the system default;
    // the adapter's vendor also steers which encoder MFT gets bound below.
    IDXGIAdapter1* adapter = NULL;
    UINT vendorId = 0;
    findAdapter(adapterLuid, &adapter, &vendorId);
    if (!adapter) pickDefaultAdapter(&adapter, &vendorId);
    const char* stage = "D3D11CreateDevice";
    hr = openDevice(e, adapter, inW, inH, outW, outH, fpsN, fpsD, &stage);
    if (FAILED(hr) && adapter) { // chosen adapter cannot host the pipeline: degrade, never kill the route
        adapter->Release();
        adapter = NULL;
        vendorId = 0;
        hr = openDevice(e, NULL, inW, inH, outW, outH, fpsN, fpsD, &stage);
    }
    if (adapter) adapter->Release();
    if (FAILED(hr)) { setErr(errbuf, errcap, stage, hr); mf_enc_close(e); return NULL; }
    stageTrace("MFCreateDXGIDeviceManager+ResetDevice");
    hr = MFCreateDXGIDeviceManager(&e->devmgrToken, &e->devmgr);
    if (FAILED(hr)) { setErr(errbuf, errcap, "MFCreateDXGIDeviceManager", hr); mf_enc_close(e); return NULL; }
    hr = e->devmgr->ResetDevice(e->dev, e->devmgrToken);
    if (FAILED(hr)) { setErr(errbuf, errcap, "ResetDevice", hr); mf_enc_close(e); return NULL; }

    // ── async hardware encoder ──
    // Adapter-bound: vendor-first candidate order + SET_D3D_MANAGER as the hard gate (an MFT that
    // refuses this adapter's device manager is skipped instead of failing the whole pipeline).
    stageTrace("enumHWEncoder(bind + SET_D3D_MANAGER)");
    if (!enumHWEncoder(kMFVideoFormat_H264, &e->enc, e->name, sizeof(e->name),
                       vendorTag(vendorId), e->devmgr)) {
        setErr(errbuf, errcap, "MFTEnumEx(no hw encoder for this device)", E_FAIL);
        mf_enc_close(e);
        return NULL;
    }

    IMFMediaType* outMT = NULL;
    hr = mtVideo(kMFVideoFormat_H264, outW, outH, fpsN, fpsD, &outMT);
    if (FAILED(hr)) { setErr(errbuf, errcap, "mtVideo(out)", hr); mf_enc_close(e); return NULL; }
    int kbps = bitrateKbps > 0 ? bitrateKbps : 8000;
    outMT->SetUINT32(kMF_MT_AVG_BITRATE, (UINT32)kbps * 1000u);
    outMT->SetUINT32(kMF_MT_MPEG2_PROFILE, 77 /*eAVEncH264VProfile_Main*/);
    // Explicit level: 4K60 needs 5.2; leaving it unset makes the driver derive it, and a
    // mis-derived level sizes internal buffers for less than 4K (crash-audit fix).
    outMT->SetUINT32(kMF_MT_MPEG2_LEVEL, h264LevelFor(outW, outH, fpsN, fpsD));
    stageTrace("enc SetOutputType");
    hr = e->enc->SetOutputType(0, outMT, 0);
    outMT->Release();
    if (FAILED(hr)) { setErr(errbuf, errcap, "enc SetOutputType", hr); mf_enc_close(e); return NULL; }

    // input: pick the MFT's own NV12 candidate (HW MFTs attach required attrs), stamp geometry
    stageTrace("enc input type negotiation");
    IMFMediaType* inMT = NULL;
    for (DWORD i = 0; ; i++) {
        IMFMediaType* c = NULL;
        if (FAILED(e->enc->GetInputAvailableType(0, i, &c))) break;
        GUID sub = {0};
        c->GetGUID(kMF_MT_SUBTYPE, &sub);
        if (IsEqualGUID(sub, kMFVideoFormat_NV12)) { inMT = c; break; }
        c->Release();
    }
    if (!inMT) {
        hr = mtVideo(kMFVideoFormat_NV12, outW, outH, fpsN, fpsD, &inMT);
        if (FAILED(hr)) { setErr(errbuf, errcap, "mtVideo(in)", hr); mf_enc_close(e); return NULL; }
    }
    inMT->SetUINT64(kMF_MT_FRAME_SIZE, ((UINT64)(UINT32)outW << 32) | (UINT32)outH);
    inMT->SetUINT64(kMF_MT_FRAME_RATE, ((UINT64)(UINT32)fpsN << 32) | (UINT32)fpsD);
    stageTrace("enc SetInputType");
    hr = e->enc->SetInputType(0, inMT, 0);
    if (FAILED(hr)) { inMT->Release(); setErr(errbuf, errcap, "enc SetInputType", hr); mf_enc_close(e); return NULL; }

    // rate control / GOP / latency knobs (best-effort: encoders vary)
    stageTrace("ICodecAPI knobs");
    if (SUCCEEDED(e->enc->QueryInterface(kIID_ICodecAPI, (void**)&e->capi)) && e->capi) {
        VARIANT v;
        VariantInit(&v);
        v.vt = VT_UI4; v.ulVal = 3; // eAVEncCommonRateControlMode_CBR
        e->capi->SetValue(&kCODECAPI_AVEncCommonRateControlMode, &v);
        v.ulVal = (ULONG)kbps * 1000u;
        e->capi->SetValue(&kCODECAPI_AVEncCommonMeanBitRate, &v);
        v.ulVal = gopFrames > 0 ? (ULONG)gopFrames : (ULONG)(2.0 * fpsN / fpsD);
        e->capi->SetValue(&kCODECAPI_AVEncMPVGOPSize, &v);
        v.ulVal = 0;
        e->capi->SetValue(&kCODECAPI_AVEncMPVDefaultBPictureCount, &v);
        VARIANT b;
        VariantInit(&b);
        b.vt = VT_BOOL; b.boolVal = VARIANT_TRUE;
        e->capi->SetValue(&kCODECAPI_AVEncCommonLowLatency, &b);
    }

    MFT_OUTPUT_STREAM_INFO osi;
    memset(&osi, 0, sizeof(osi));
    if (SUCCEEDED(e->enc->GetOutputStreamInfo(0, &osi))) {
        e->encProvides = (osi.dwFlags & (MFT_OUTPUT_STREAM_PROVIDES_SAMPLES | MFT_OUTPUT_STREAM_CAN_PROVIDE_SAMPLES)) ? 1 : 0;
        e->encOutSize = osi.cbSize;
    }

    // async (event-driven) when the MFT exposes it; else sync ProcessInput/Output.
    // MS docs say HW MFTs are async, but some vendor MFTs answer E_NOINTERFACE here -
    // supporting both kills that variance.
    if (FAILED(e->enc->QueryInterface(__uuidof(IMFMediaEventGenerator), (void**)&e->evgen)))
        e->evgen = NULL;

    inMT->Release();

    // ── D3D11 Video API CSC + scale (VideoProcessorBlt: deterministic, no XVP quirks) ──
    // vdev/vctx/vpe already exist: openDevice created them as the video-capability gate.
    stageTrace("VP format check + CreateVideoProcessor");
    UINT fmtFl = 0;
    e->bgraIn = 1; // default BGRA+swizzle; prefer RGBA input when the VP takes it directly
    if (SUCCEEDED(e->vpe->CheckVideoProcessorFormat(DXGI_FORMAT_R8G8B8A8_UNORM, &fmtFl)) &&
        (fmtFl & D3D11_VIDEO_PROCESSOR_FORMAT_SUPPORT_INPUT))
        e->bgraIn = 0;
    hr = e->vdev->CreateVideoProcessor(e->vpe, 0, &e->vproc);
    if (FAILED(hr)) { setErr(errbuf, errcap, "CreateVideoProcessor", hr); mf_enc_close(e); return NULL; }
    // BT.709 studio-range output (what H.264 decoders assume), full-range RGB input
    D3D11_VIDEO_PROCESSOR_COLOR_SPACE cs;
    memset(&cs, 0, sizeof(cs));
    cs.YCbCr_Matrix = 1; // 709
    e->vctx->VideoProcessorSetOutputColorSpace(e->vproc, &cs);
    e->vctx->VideoProcessorSetStreamColorSpace(e->vproc, 0, &cs);

    // input upload texture (byte order per the VP's accepted RGB format)
    stageTrace("input texture + views + NV12 pool");
    D3D11_TEXTURE2D_DESC td;
    memset(&td, 0, sizeof(td));
    td.Width = (UINT)inW;
    td.Height = (UINT)inH;
    td.MipLevels = 1;
    td.ArraySize = 1;
    td.Format = e->bgraIn ? DXGI_FORMAT_B8G8R8A8_UNORM : DXGI_FORMAT_R8G8B8A8_UNORM;
    td.SampleDesc.Count = 1;
    td.Usage = D3D11_USAGE_DEFAULT;
    td.BindFlags = D3D11_BIND_SHADER_RESOURCE | D3D11_BIND_RENDER_TARGET;
    hr = e->dev->CreateTexture2D(&td, NULL, &e->inTex);
    if (FAILED(hr)) { setErr(errbuf, errcap, "CreateTexture2D(in)", hr); mf_enc_close(e); return NULL; }
    if (e->bgraIn) {
        e->swz = (uint8_t*)malloc((size_t)inW * inH * 4);
        if (!e->swz) { setErr(errbuf, errcap, "swz alloc", E_OUTOFMEMORY); mf_enc_close(e); return NULL; }
    }
    D3D11_VIDEO_PROCESSOR_INPUT_VIEW_DESC ivd;
    memset(&ivd, 0, sizeof(ivd));
    ivd.ViewDimension = D3D11_VPIV_DIMENSION_TEXTURE2D;
    hr = e->vdev->CreateVideoProcessorInputView(e->inTex, e->vpe, &ivd, &e->inView);
    if (FAILED(hr)) { setErr(errbuf, errcap, "CreateVideoProcessorInputView", hr); mf_enc_close(e); return NULL; }

    // NV12 pool: Blt target + prebuilt DXGI sample per slot (encoder queue ≤4; 8 = slack)
    for (int i = 0; i < NVPOOL; i++) {
        D3D11_TEXTURE2D_DESC nd;
        memset(&nd, 0, sizeof(nd));
        nd.Width = (UINT)outW;
        nd.Height = (UINT)outH;
        nd.MipLevels = 1;
        nd.ArraySize = 1;
        nd.Format = DXGI_FORMAT_NV12;
        nd.SampleDesc.Count = 1;
        nd.Usage = D3D11_USAGE_DEFAULT;
        nd.BindFlags = D3D11_BIND_RENDER_TARGET;
        hr = e->dev->CreateTexture2D(&nd, NULL, &e->nvTex[i]);
        if (FAILED(hr)) { setErr(errbuf, errcap, "CreateTexture2D(nv12)", hr); mf_enc_close(e); return NULL; }
        D3D11_VIDEO_PROCESSOR_OUTPUT_VIEW_DESC ovd;
        memset(&ovd, 0, sizeof(ovd));
        ovd.ViewDimension = D3D11_VPOV_DIMENSION_TEXTURE2D;
        hr = e->vdev->CreateVideoProcessorOutputView(e->nvTex[i], e->vpe, &ovd, &e->nvView[i]);
        if (FAILED(hr)) { setErr(errbuf, errcap, "CreateVideoProcessorOutputView", hr); mf_enc_close(e); return NULL; }
        IMFMediaBuffer* mb = NULL;
        hr = MFCreateDXGISurfaceBuffer(kIID_ID3D11Texture2D, e->nvTex[i], 0, FALSE, &mb);
        if (FAILED(hr)) { setErr(errbuf, errcap, "MFCreateDXGISurfaceBuffer(nv12)", hr); mf_enc_close(e); return NULL; }
        IMF2DBuffer* b2 = NULL;
        if (SUCCEEDED(mb->QueryInterface(kIID_IMF2DBuffer, (void**)&b2))) {
            DWORD cl = 0;
            if (SUCCEEDED(b2->GetContiguousLength(&cl))) mb->SetCurrentLength(cl);
            b2->Release();
        }
        hr = MFCreateSample(&e->nvSample[i]);
        if (FAILED(hr)) { mb->Release(); setErr(errbuf, errcap, "MFCreateSample(nv12)", hr); mf_enc_close(e); return NULL; }
        e->nvSample[i]->AddBuffer(mb);
        mb->Release();
    }

    stageTrace("BEGIN_STREAMING/START_OF_STREAM");
    e->enc->ProcessMessage(MFT_MESSAGE_NOTIFY_BEGIN_STREAMING, 0);
    e->enc->ProcessMessage(MFT_MESSAGE_NOTIFY_START_OF_STREAM, 0);
    stageTrace("open complete");
    return e;
}

mfenc* mf_enc_open(int64_t adapterLuid, int inW, int inH, int outW, int outH,
                   int fpsN, int fpsD, int bitrateKbps, int gopFrames,
                   char* errbuf, int errcap) {
    if (g_shimPoisoned) {
        setErr(errbuf, errcap, "native engine disabled by earlier driver fault", E_FAIL);
        return NULL;
    }
    guardInstall();
    if (setjmp(g_guardJmp) != 0) {
        // Driver fault (c0000005 class) anywhere in the open path: clean failure instead of
        // process death. Partial pipeline deliberately leaked; native engine off for this
        // process - the Go side substitutes the probed ffmpeg H.264 encoder.
        g_guardArmed = 0;
        InterlockedExchange((LONG*)&g_shimPoisoned, 1);
        setErr(errbuf, errcap, "driver fault during open (native engine disabled, pipeline leaked)",
               (HRESULT)g_guardCode);
        return NULL;
    }
    guardArm();
    mfenc* e = openImpl(adapterLuid, inW, inH, outW, outH, fpsN, fpsD, bitrateKbps, gopFrames,
                        errbuf, errcap);
    g_guardArmed = 0;
    return e;
}

int mf_enc_feed(mfenc* e, const uint8_t* rgba, int stride, int64_t pts100) {
    if (!e || !rgba || stride < e->inW * 4) return -1;
    const uint8_t* rows = rgba;
    if (e->bgraIn) { // negotiated BGRA memory order - swizzle our RGBA rows
        if (stride == e->inW * 4) {
            mf_swizzle_rgba_bgra(e->swz, rgba, e->inW * e->inH);
        } else {
            for (int y = 0; y < e->inH; y++)
                mf_swizzle_rgba_bgra(e->swz + (size_t)y * e->inW * 4, rgba + (size_t)y * stride, e->inW);
        }
        rows = e->swz;
        stride = e->inW * 4;
    }
    e->ctx->UpdateSubresource(e->inTex, 0, NULL, rows, (UINT)stride, 0);

    int slot = -1;
    HRESULT hr = vpBlt(e, &slot);
    if (FAILED(hr) || slot < 0) {
        e->lastHR = hr;
        return -4;
    }
    IMFSample* nv12 = e->nvSample[slot]; // pool-owned: never released here
    nv12->SetSampleTime((LONGLONG)pts100);
    nv12->SetSampleDuration((LONGLONG)e->dur100);

    if (e->forceIDR && e->capi) {
        VARIANT v;
        VariantInit(&v);
        v.vt = VT_UI4; v.ulVal = 1;
        e->capi->SetValue(&kCODECAPI_AVEncVideoForceKeyFrame, &v);
        e->forceIDR = 0;
    }

    if (e->evgen) {
        // async MFT: wait for an input credit, bounded
        DWORD waited = 0;
        for (;;) {
            if (pumpEvents(e) < 0) return -5;
            if (e->needInput > 0) break;
            if (waited >= FEED_WAIT_MS) return -6;
            Sleep(1);
            waited++;
        }
        e->needInput--;
        hr = e->enc->ProcessInput(0, nv12, 0);
        if (FAILED(hr)) { e->lastHR = hr; return -7; }
        if (pumpEvents(e) < 0) return -5;
        return 0;
    }
    // sync MFT: drain output on NOTACCEPTING, bounded
    DWORD waited = 0;
    for (;;) {
        hr = e->enc->ProcessInput(0, nv12, 0);
        if (hr != MF_E_NOTACCEPTING) break;
        if (harvestOutput(e) < 0) return -5;
        if (waited >= FEED_WAIT_MS) return -6;
        Sleep(1);
        waited++;
    }
    if (FAILED(hr)) { e->lastHR = hr; return -7; }
    if (harvestOutput(e) < 0) return -5;
    return 0;
}

int mf_enc_next(mfenc* e, uint8_t* out, int cap, int64_t* pts100, int* keyframe) {
    if (!e) return -1;
    if (pumpEvents(e) < 0) return -1;
    if (e->rCount == 0) return 0;
    auEntry* ae = &e->ring[e->rHead];
    if (ae->size > cap) return -(ae->size);
    memcpy(out, ae->data, (size_t)ae->size);
    if (pts100) *pts100 = ae->pts100;
    if (keyframe) *keyframe = ae->key;
    int n = ae->size;
    free(ae->data);
    ae->data = NULL;
    e->rHead = (e->rHead + 1) % AURING_CAP;
    e->rCount--;
    return n;
}

// mf_enc_set_bitrate live-retargets CBR mean bitrate (no reopen; Phase-2 degrade ladder).
int mf_enc_set_bitrate(mfenc* e, int kbps) {
    if (!e || kbps <= 0) return -1;
    if (!e->capi) return -2;
    VARIANT v;
    VariantInit(&v);
    v.vt = VT_UI4; v.ulVal = (ULONG)kbps * 1000u;
    return SUCCEEDED(e->capi->SetValue(&kCODECAPI_AVEncCommonMeanBitRate, &v)) ? 0 : -3;
}

int mf_enc_force_idr(mfenc* e) {
    if (!e) return -1;
    e->forceIDR = 1;
    return 0;
}

int mf_enc_drain(mfenc* e) {
    if (!e) return -1;
    e->enc->ProcessMessage(MFT_MESSAGE_NOTIFY_END_OF_STREAM, 0);
    e->enc->ProcessMessage(MFT_MESSAGE_COMMAND_DRAIN, 0);
    if (!e->evgen) return harvestOutput(e) < 0 ? -1 : 0; // sync: output runs dry inline
    DWORD waited = 0;
    while (!e->drainDone && waited < FEED_WAIT_MS) {
        if (pumpEvents(e) < 0) return -1;
        if (e->drainDone) break;
        Sleep(1);
        waited++;
    }
    return 0;
}

int mf_enc_input_is_bgra(mfenc* e) { return e ? e->bgraIn : 0; }

int64_t mf_enc_last_hr(mfenc* e) { return e ? (int64_t)(uint32_t)e->lastHR : 0; }

void mf_enc_name(mfenc* e, char* out, int cap) {
    if (!e || !out || cap <= 0) return;
    snprintf(out, (size_t)cap, "%s", e->name[0] ? e->name : "hardware H.264 MFT");
}

void mf_enc_close(mfenc* e) {
    if (!e) return;
    for (int i = 0; i < e->rCount; i++) {
        auEntry* ae = &e->ring[(e->rHead + i) % AURING_CAP];
        free(ae->data);
        ae->data = NULL;
    }
    e->rCount = 0;
    rel(e->capi);
    rel(e->evgen);
    rel(e->enc);
    for (int i = 0; i < NVPOOL; i++) {
        rel(e->nvSample[i]);
        rel(e->nvView[i]);
        rel(e->nvTex[i]);
    }
    rel(e->inView);
    rel(e->vproc);
    rel(e->vpe);
    rel(e->vctx);
    rel(e->vdev);
    rel(e->inTex);
    rel(e->devmgr);
    rel(e->ctx);
    rel(e->dev);
    free(e->swz);
    if (e->mfInit) MFShutdown();
    if (e->comInit) CoUninitialize();
    free(e);
}
