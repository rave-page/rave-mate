//go:build spout

// Flat C wrappers over the Spout SDK (SpoutLibrary.h pulls in <windows.h>, <d3d11.h>,
// <string>, <vector> - compiled by g++ via cgo). One SPOUTLIBRARY handle per deck/thread.
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <mutex>
#include <windows.h>
#include "SpoutLibrary.h"
#include "spout_shim.h"

// SpoutLibrary.dll is loaded at RUNTIME (LoadLibrary), NOT import-linked - so a missing DLL
// disables the Spout feature instead of crashing the whole process at load time (the exe carries
// no SpoutLibrary import; sender_spout.go drops -lSpoutLibrary). The interface methods are C++
// vtable calls on the handle the DLL returns, so only the GetSpout factory needs resolving.
typedef SPOUTHANDLE(WINAPI* GetSpoutFn)(void);

// RAVE_SPOUT_DIM_OK bounds a frame edge (mirrors videoshare.MaxFrameDim / mediapipe's encode
// guard). GetSenderWidth/Height read the sender's SHARED MEMORY info, which can be TORN while a
// large shared texture is still being created - a 3840x2160 sender was observed reporting
// w=139846784 h=3840 on a receiver's first poll, and the Go side then sized a 2 TB buffer and
// killed the media child ("runtime: cannot allocate memory"). Never REPORT garbage dims either.
#define RAVE_SPOUT_DIM_OK(d) ((d) > 0 && (d) <= 16384)

// spout_factory resolves DLL!GetSpout once (C++11 magic statics → thread-safe, run-once).
// Returns NULL if the DLL is absent or the export is missing.
static GetSpoutFn spout_factory(void) {
    static GetSpoutFn fn = []() -> GetSpoutFn {
        HMODULE m = LoadLibraryA("SpoutLibrary.dll");
        if (!m) return nullptr;
        return (GetSpoutFn)GetProcAddress(m, "GetSpout");
    }();
    return fn;
}

// make_spout calls the resolved factory (NULL if the DLL never loaded).
static SPOUTHANDLE make_spout(void) {
    GetSpoutFn fn = spout_factory();
    return fn ? fn() : nullptr;
}

// registry() is ONE process-wide handle used only for name-registry queries (shared-memory reads:
// GetSenderCount / GetSender / GetSenderInfo / FindSenderName). No OpenGL context, no sender or
// receiver binding, never released. The registry lives in shared memory, so a long-lived object
// answers every query - the old create-and-Release per call churned 1+2N COM objects on every 2 s
// mediaroute scan, on the machine that is already busy encoding. Guarded by registry_mu because
// the scan goroutine and route opens can query concurrently.
static std::mutex& registry_mu(void) {
    static std::mutex m;
    return m;
}

static SPOUTHANDLE registry(void) {
    static SPOUTHANDLE s = make_spout(); // magic static: run-once, thread-safe
    return s;
}

extern "C" {

// rave_spout_available reports whether SpoutLibrary.dll loaded + exports GetSpout (1) or not (0).
// Lets the sink degrade gracefully (idle, logged) instead of warning per deck.
int rave_spout_available(void) { return spout_factory() ? 1 : 0; }

void* rave_spout_create(void) {
    SPOUTHANDLE s = make_spout();
    if (!s) return 0;
    // CreateOpenGL makes a context current on THIS thread; SendImage needs it.
    if (!s->CreateOpenGL()) {
        s->Release();
        return 0;
    }
    return (void*)s;
}

// flip_buf is the flip staging buffer, POOLED per sender thread (one SPOUTHANDLE per
// LockOSThread'd worker, so thread_local is per handle and needs no lock). It used to be a
// malloc+free per frame: 33 MB of C-heap churn per frame at 4K60 = 2 GB/s the Go GOMEMLIMIT
// cannot see. Grown only on a geometry change; freed in rave_spout_release (same thread).
static thread_local unsigned char* flip_buf = nullptr;
static thread_local size_t flip_cap = 0;

// grow_flip_buf sizes the pooled per-thread staging buffer; false = out of memory.
static bool grow_flip_buf(size_t need) {
    if (flip_cap >= need) return true;
    unsigned char* nb = (unsigned char*)realloc(flip_buf, need);
    if (!nb) return false;
    flip_buf = nb;
    flip_cap = need;
    return true;
}

// flip_rows copies src into dst applying the requested geometric flip.
//
// The vertical component is a WHOLE-ROW memcpy in reverse row order; only a horizontal mirror needs
// to touch individual pixels, and that is a 32-bit-per-pixel loop the compiler keeps in registers.
// The old code was one scalar memcpy(...,4) per PIXEL inside a doubly-nested loop - 8.3 M libc calls
// per 4K frame, where a vertical-only flip now costs 2160 memcpys.
void flip_rows(unsigned char* dst, const unsigned char* src,
               unsigned int w, unsigned int height, bool flip_h, bool flip_v) {
    const size_t stride = (size_t)w * 4;
    for (unsigned int y = 0; y < height; y++) {
        const unsigned int sy = flip_v ? (height - 1 - y) : y;
        const unsigned char* in = src + (size_t)sy * stride;
        unsigned char* out = dst + (size_t)y * stride;
        if (!flip_h) {
            memcpy(out, in, stride);
            continue;
        }
        const uint32_t* ip = (const uint32_t*)in;
        uint32_t* op = (uint32_t*)out;
        for (unsigned int x = 0; x < w; x++) op[x] = ip[w - 1 - x];
    }
}

// rave_spout_flip_rows is flip_rows behind the flat C API (test parity gate; see the header).
void rave_spout_flip_rows(unsigned char* dst, const unsigned char* src,
                          unsigned int w, unsigned int height, int flip) {
    if (!dst || !src || !RAVE_SPOUT_DIM_OK(w) || !RAVE_SPOUT_DIM_OK(height)) return;
    flip_rows(dst, src, w, height, (flip & 1) != 0, (flip & 2) != 0);
}

// rave_spout_send publishes one RGBA frame with the configured geometric flip (bit0=horizontal,
// bit1=vertical, RAVE_SPOUT_FLIP). flip == 0 (the default, measured upright on the dev rig) sends
// the caller's buffer straight through with no host pass at all.
//
// bInvert is ALWAYS false, deliberately. Spout's own bInvert would do the vertical flip inside the
// GL/DX interop copy (free, no host pass), and increment 4 MEASURED it working: with bInvert=true the
// v mode decoded to exactly the same quadrants as this CPU path. It is still not used, because the
// CPU transform can be proven byte-identical to its predecessor by a deterministic unit test
// (TestFlipRowsMatchesTheOriginalPerPixelAlgorithm) whereas bInvert can only be checked on the live
// rig - and this SpoutLibrary pairing has already produced skewed late-vtable behaviour in three
// separate places. An upside-down output is user-visible breakage; 2.16 ms/frame at 4K is not.
// If you do switch, TestFlipLiveOrientation is the gate that must stay green for all four modes.
// deckcard.Render produces top-row-first RGBA.
int rave_spout_send(void* h, const char* name, const unsigned char* rgba,
                    unsigned int w, unsigned int height, int flip) {
    if (!h || !rgba || !RAVE_SPOUT_DIM_OK(w) || !RAVE_SPOUT_DIM_OK(height)) return 0;
    SPOUTHANDLE s = (SPOUTHANDLE)h;
    s->SetSenderName(name);
    if (flip == 0) {
        return s->SendImage(rgba, w, height, 0x1908 /*GL_RGBA*/, false) ? 1 : 0;
    }
    const size_t need = (size_t)w * height * 4;
    if (!grow_flip_buf(need)) return 0;
    flip_rows(flip_buf, rgba, w, height, (flip & 1) != 0, (flip & 2) != 0);
    return s->SendImage(flip_buf, w, height, 0x1908 /*GL_RGBA*/, false) ? 1 : 0;
}

// rave_spout_open_sender: force the sender's shared texture to exist, then hand its handle out.
// GetHandle() returns the DX11 shared handle of the CURRENT sender - which is only allocated once
// something has been sent, hence the single zeroed frame. See the header for why there is no
// CreateSender to call.
int rave_spout_open_sender(void* h, const char* name, unsigned int w, unsigned int hgt,
                          unsigned int fmt, unsigned long long* share, unsigned int* out_fmt) {
    if (!h || !name || !share || !RAVE_SPOUT_DIM_OK(w) || !RAVE_SPOUT_DIM_OK(hgt)) return 0;
    *share = 0;
    SPOUTHANDLE s = (SPOUTHANDLE)h;
    s->SetSenderName(name);
    if (fmt != 0) s->SetSenderFormat((DWORD)fmt);
    const size_t need = (size_t)w * hgt * 4;
    if (!grow_flip_buf(need)) return 0;
    memset(flip_buf, 0, need);
    if (!s->SendImage(flip_buf, w, hgt, 0x1908 /*GL_RGBA*/, false)) return -1; // send refused
    // Read the handle back out of the REGISTRY rather than from GetHandle(): on this SDK pairing
    // GetHandle() returns NULL for a sender created through SendImage, while GetSenderInfo (the
    // shared-memory read every other query here uses, and the one the zero-copy CAPTURE path is
    // already proven against) reports the real dxShareHandle + format.
    unsigned int rw = 0, rh = 0;
    HANDLE sh = 0;
    DWORD rf = 0;
    {
        std::lock_guard<std::mutex> lk(registry_mu());
        SPOUTHANDLE reg = registry();
        if (!reg || !reg->GetSenderInfo(name, rw, rh, sh, rf)) return -2;
    }
    if (!sh || rw != w || rh != hgt) return -2; // CPU/memoryshare sender, or torn/mismatched info
    *share = (unsigned long long)(uintptr_t)sh;
    if (out_fmt) *out_fmt = (unsigned int)rf;
    return 1;
}

void rave_spout_release(void* h) {
    if (flip_buf) {
        free(flip_buf);
        flip_buf = nullptr;
        flip_cap = 0;
    }
    if (!h) return;
    SPOUTHANDLE s = (SPOUTHANDLE)h;
    s->ReleaseSender();
    s->CloseOpenGL();
    s->Release();
}

int rave_spout_sender_count(void) {
    std::lock_guard<std::mutex> lk(registry_mu());
    SPOUTHANDLE s = registry();
    if (!s) return -1;
    return s->GetSenderCount();
}

int rave_spout_find(const char* name) {
    if (!name) return 0;
    std::lock_guard<std::mutex> lk(registry_mu());
    SPOUTHANDLE s = registry();
    if (!s) return -1;
    return s->FindSenderName(name) ? 1 : 0;
}

int rave_spout_sender_name(int idx, char* out, int cap) {
    if (!out || cap <= 0) return 0;
    std::lock_guard<std::mutex> lk(registry_mu());
    SPOUTHANDLE s = registry();
    if (!s) return 0;
    return s->GetSender(idx, out, cap) ? 1 : 0;
}

int rave_spout_sender_size(const char* name, unsigned int* w, unsigned int* h) {
    if (!name || !w || !h) return 0;
    std::lock_guard<std::mutex> lk(registry_mu());
    SPOUTHANDLE s = registry();
    if (!s) return 0;
    unsigned int ww = 0, hh = 0;
    HANDLE share = 0;
    DWORD fmt = 0;
    if (!s->GetSenderInfo(name, ww, hh, share, fmt)) return 0;
    if (!RAVE_SPOUT_DIM_OK(ww) || !RAVE_SPOUT_DIM_OK(hh)) return 0; // torn info: "no size yet"
    *w = ww;
    *h = hh;
    return 1;
}

// rave_spout_sender_share exports what GetSenderInfo already fetched and every other caller here
// throws away: the DX11 shared-texture handle + its DXGI format. The zero-copy encode path
// (zigmedia inc 1) passes these two scalars to the encoder child, which opens the texture on its
// own device - no readback, no host frame buffer. Same DIM validation as the rest of the shim:
// torn registry info must never reach a consumer that sizes anything from it.
int rave_spout_sender_share(const char* name, unsigned long long* share, unsigned int* fmt,
                            unsigned int* w, unsigned int* h) {
    if (!name || !share || !fmt || !w || !h) return 0;
    *share = 0;
    *fmt = 0;
    *w = 0;
    *h = 0;
    std::lock_guard<std::mutex> lk(registry_mu());
    SPOUTHANDLE s = registry();
    if (!s) return 0;
    unsigned int ww = 0, hh = 0;
    HANDLE sh = 0;
    DWORD f = 0;
    if (!s->GetSenderInfo(name, ww, hh, sh, f)) return 0;
    if (!RAVE_SPOUT_DIM_OK(ww) || !RAVE_SPOUT_DIM_OK(hh)) return 0; // torn info: "nothing yet"
    *share = (unsigned long long)(uintptr_t)sh;
    *fmt = (unsigned int)f;
    *w = ww;
    *h = hh;
    return 1;
}

// rave_spout_scan fills names (maxN slots of nameCap bytes, NUL-terminated) + dims (2 uints per
// slot: w,h) in one pass and returns the slot count (-1 = no DLL). One lock, one handle, zero
// allocations of COM objects - this replaces count + N name + N size calls per scan.
int rave_spout_scan(char* names, int nameCap, int maxN, unsigned int* dims) {
    if (!names || !dims || nameCap <= 0 || maxN <= 0) return -1;
    std::lock_guard<std::mutex> lk(registry_mu());
    SPOUTHANDLE s = registry();
    if (!s) return -1;
    int n = s->GetSenderCount();
    if (n < 0) return -1;
    if (n > maxN) n = maxN;
    int out = 0;
    for (int i = 0; i < n; i++) {
        char* slot = names + (size_t)out * (size_t)nameCap;
        slot[0] = 0;
        if (!s->GetSender(i, slot, nameCap) || slot[0] == 0) continue;
        slot[nameCap - 1] = 0;
        unsigned int w = 0, h = 0;
        HANDLE share = 0;
        DWORD fmt = 0;
        if (!s->GetSenderInfo(slot, w, h, share, fmt)) { w = 0; h = 0; }
        if (!RAVE_SPOUT_DIM_OK(w) || !RAVE_SPOUT_DIM_OK(h)) { w = 0; h = 0; } // torn info
        dims[(size_t)out * 2] = w;
        dims[(size_t)out * 2 + 1] = h;
        out++;
    }
    return out;
}

// -- D3D11 receiver (replaces SPOUTLIBRARY::ReceiveImage) ------------------------------------
//
// WHY the readback is done here instead of by ReceiveImage:
//
// The vendored SpoutLibrary.h and the shipped SpoutLibrary.dll are DIFFERENT revisions of the
// COM-like interface, and the mismatch is a WINDOW, not a uniform shift. Proven by execution on
// 2.007.017 (canary + ground-truth probes, see recvdiag_spout_test.go):
//
//   SendImage        (hdr slot   5)  aligns   - sending has always worked
//   GetHandle        (hdr slot  12)  SHIFTED  - returns 0 where the registry has a real handle
//   ReceiveImage     (hdr slot  19)  SHIFTED  - lands on ReceiveTexture(GLuint,...), so the pixel
//                                               POINTER is taken as a texture id: it returns true,
//                                               reports frame-new, and never writes one byte
//                                               (a 33 MB canary survived 12/12 attempts)
//   IsFrameNew       (hdr slot  22)  SHIFTED  - lands on IsConnected: permanently "true"
//   GetSenderWidth   (hdr slot  24)  SHIFTED  - returns GetSenderName's POINTER
//   GetSenderHeight  (hdr slot  25)  SHIFTED  - returns the real WIDTH
//   GetSenderFormat  (hdr slot  26)  SHIFTED  - returns the real HEIGHT
//   GetSenderCount / GetSender / FindSenderName / GetSenderInfo (slots 111-114)  align
//
// That is why the receive path reported healthy metadata for so long while delivering black, and
// why the PRE-rework code got no frames at all: its first call passed NULL, so ReceiveTexture(0,..)
// simply failed. Both shapes are the same bug.
//
// So the only Spout call left on this path is GetSenderInfo - which aligns, is a shared-memory read
// and already backs the zero-copy encode path. Everything else is plain D3D11: OpenSharedResource
// -> CopyResource into a STAGING texture -> Map -> row copy. No OpenGL context, no GL thread
// affinity, and no SDK ABI surface inside the window that broke.

#define RAVE_ACQUIRE_MS 3 // bounded mutex wait: never spin on a sender's access mutex
#define RAVE_FMT_BGRA 87  // DXGI_FORMAT_B8G8R8A8_UNORM - what Spout creates

struct RaveRecv {
    ID3D11Device* dev = nullptr;
    ID3D11DeviceContext* ctx = nullptr;
    ID3D11Texture2D* shared = nullptr; // the sender's texture, opened on our device
    ID3D11Texture2D* stage = nullptr;  // CPU-readable copy target
    HANDLE amutex = nullptr;           // "<name>_SpoutAccessMutex", the same guard the SDK uses
    HANDLE share = nullptr;            // handle currently open (a sender restart changes it)
    unsigned int w = 0, h = 0;
    DWORD fmt = 0;
    char name[256] = {0};
};

// recv_drop_texture releases the opened sender texture + staging pair (sender changed or closing).
static void recv_drop_texture(RaveRecv* r) {
    if (r->stage) { r->stage->Release(); r->stage = nullptr; }
    if (r->shared) { r->shared->Release(); r->shared = nullptr; }
    if (r->amutex) { CloseHandle(r->amutex); r->amutex = nullptr; }
    r->share = nullptr;
    r->w = 0;
    r->h = 0;
    r->fmt = 0;
}

// recv_open_texture opens the sender's shared texture plus a matching staging texture.
static bool recv_open_texture(RaveRecv* r, const char* name, HANDLE share, unsigned int w,
                              unsigned int h) {
    recv_drop_texture(r);
    // Legacy SHARED handle (Spout hands out D3D11_RESOURCE_MISC_SHARED, not an NT handle).
    if (FAILED(r->dev->OpenSharedResource(share, IID_ID3D11Texture2D, (void**)&r->shared))) {
        return false;
    }
    D3D11_TEXTURE2D_DESC sd;
    memset(&sd, 0, sizeof(sd));
    r->shared->GetDesc(&sd);
    if (sd.Width != w || sd.Height != h) return false; // registry and texture disagree: refuse
    D3D11_TEXTURE2D_DESC td;
    memset(&td, 0, sizeof(td));
    td.Width = w;
    td.Height = h;
    td.MipLevels = 1;
    td.ArraySize = 1;
    td.Format = sd.Format; // copy in the sender's own format; swizzle during the row copy
    td.SampleDesc.Count = 1;
    td.Usage = D3D11_USAGE_STAGING;
    td.BindFlags = 0;
    td.CPUAccessFlags = D3D11_CPU_ACCESS_READ;
    if (FAILED(r->dev->CreateTexture2D(&td, nullptr, &r->stage))) return false;
    // Spout guards its texture with a NAMED mutex (name confirmed by execution in zigmedia inc 1).
    char mname[320];
    snprintf(mname, sizeof(mname), "%s_SpoutAccessMutex", name);
    r->amutex = OpenMutexA(SYNCHRONIZE | MUTEX_MODIFY_STATE, FALSE, mname);
    r->share = share;
    r->w = w;
    r->h = h;
    r->fmt = sd.Format;
    snprintf(r->name, sizeof(r->name), "%s", name);
    return true;
}

void* rave_spout_recv_create(void) {
    RaveRecv* r = new RaveRecv();
    // No GL, no swap chain, no window - just a device that can open the sender's texture.
    HRESULT hr = D3D11CreateDevice(nullptr, D3D_DRIVER_TYPE_HARDWARE, nullptr,
                                   D3D11_CREATE_DEVICE_BGRA_SUPPORT, nullptr, 0, D3D11_SDK_VERSION,
                                   &r->dev, nullptr, &r->ctx);
    if (FAILED(hr) || !r->dev || !r->ctx) {
        if (r->ctx) r->ctx->Release();
        if (r->dev) r->dev->Release();
        delete r;
        return nullptr;
    }
    return (void*)r;
}

void rave_spout_recv_release(void* h) {
    if (!h) return;
    RaveRecv* r = (RaveRecv*)h;
    recv_drop_texture(r);
    if (r->ctx) r->ctx->Release();
    if (r->dev) r->dev->Release();
    delete r;
}

// copy_rows moves mapped staging rows into the caller's tightly packed RGBA buffer, honouring
// RowPitch and swizzling B<->R when the sender's texture is BGRA (which it normally is). The old
// path asked ReceiveImage for GL_RGBA and the SDK did this conversion internally - same work.
static void copy_rows(unsigned char* dst, const unsigned char* src, size_t pitch, unsigned int w,
                      unsigned int h, bool swizzle) {
    const size_t row = (size_t)w * 4;
    for (unsigned int y = 0; y < h; y++) {
        const unsigned char* in = src + (size_t)y * pitch;
        unsigned char* out = dst + (size_t)y * row;
        if (!swizzle) {
            memcpy(out, in, row);
            continue;
        }
        const uint32_t* ip = (const uint32_t*)in;
        uint32_t* op = (uint32_t*)out;
        for (unsigned int x = 0; x < w; x++) {
            const uint32_t v = ip[x]; // memory order B,G,R,A
            op[x] = (v & 0xFF00FF00u) | ((v & 0x00FF0000u) >> 16) | ((v & 0x000000FFu) << 16);
        }
    }
}

// rave_spout_recv copies the named sender's current texture content into pixels (RGBA).
// Return codes are UNCHANGED from the SPOUTLIBRARY implementation, so recvpoll.go's state machine,
// its geometry validation and the bounded pixel pool keep working exactly as before:
//   -1 no sender / open or copy failed       2 sender (re)connected or resized: caller re-sizes
//    0 connected, nothing copied             3 buffer absent/undersized: caller re-sizes quietly
//    1 a frame was copied into pixels
int rave_spout_recv(void* h, const char* name, unsigned char* pixels, unsigned int cap,
                    unsigned int* w, unsigned int* hgt) {
    if (!h || !name || !w || !hgt) return -1;
    RaveRecv* r = (RaveRecv*)h;
    *w = 0;
    *hgt = 0;

    unsigned int sw = 0, sh = 0;
    HANDLE share = 0;
    DWORD fmt = 0;
    {
        std::lock_guard<std::mutex> lk(registry_mu());
        SPOUTHANDLE s = registry();
        if (!s || !s->GetSenderInfo(name, sw, sh, share, fmt)) return -1;
    }
    // Torn registry info or a sender with no DX11 texture (DX9 / CPU memoryshare): nothing to read.
    if (!RAVE_SPOUT_DIM_OK(sw) || !RAVE_SPOUT_DIM_OK(sh) || !share) return -1;
    *w = sw;
    *hgt = sh;

    // (Re)open on first use, on a resize, or when the sender was RE-CREATED (a new share handle -
    // reusing a dead texture is what makes a "healthy" route ship a frozen or blank picture).
    if (!r->shared || r->share != share || r->w != sw || r->h != sh || strcmp(r->name, name) != 0) {
        if (!recv_open_texture(r, name, share, sw, sh)) return -1;
        return 2; // real sender activity: caller sizes its buffer, the frame comes next poll
    }
    if (!pixels || cap < (unsigned int)((size_t)sw * sh * 4)) return 3;

    // ONE bounded acquire around ONE GPU copy: holding or hammering a sender's access mutex
    // serialises against the sending app's and DWM's submissions (documented pointer-lag hazard).
    bool held = false;
    if (r->amutex) {
        DWORD wr = WaitForSingleObject(r->amutex, RAVE_ACQUIRE_MS);
        if (wr != WAIT_OBJECT_0 && wr != WAIT_ABANDONED) return 0; // busy: skip this tick
        held = true;
    }
    r->ctx->CopyResource(r->stage, r->shared);
    if (held) ReleaseMutex(r->amutex);

    D3D11_MAPPED_SUBRESOURCE m;
    memset(&m, 0, sizeof(m));
    // Map(READ) on a staging texture waits for the copy to land - no explicit Flush needed.
    if (FAILED(r->ctx->Map(r->stage, 0, D3D11_MAP_READ, 0, &m)) || !m.pData) return -1;
    copy_rows(pixels, (const unsigned char*)m.pData, m.RowPitch, sw, sh, r->fmt == RAVE_FMT_BGRA);
    r->ctx->Unmap(r->stage, 0);
    return 1;
}

// rave_spout_recv: -1 receive failed, 0 no new frame, 1 new frame in pixels, 2 sender
// (re)connected/resized (IsUpdated - real activity, one prompt resize pass), 3 pixels
// absent/undersized with NO sender update (caller resizes quietly - reporting this as 2
// used to re-arm the receiver's 250 Hz poll forever against a stale/0x0 sender). Codes
// mirrored in recvpoll.go.
// rave_spout_recv_diag - see the header. Deliberately does NOT pre-size or early-return: it calls
// ReceiveImage exactly once with whatever the caller passed, so the caller's canary tells us whether
// the SDK wrote anything at all.
int rave_spout_recv_diag(void* h, const char* name, unsigned char* pixels, unsigned int cap,
                         rave_spout_diag* out) {
    if (!h || !out) return 0;
    SPOUTHANDLE s = (SPOUTHANDLE)h;
    memset(out, 0, sizeof(*out));
    if (name && name[0]) s->SetReceiverName(name);
    out->recv_ok = s->ReceiveImage(pixels, 0x1908 /*GL_RGBA*/, false, 0) ? 1 : 0;
    out->updated = s->IsUpdated() ? 1 : 0;
    out->frame_new = s->IsFrameNew() ? 1 : 0;
    out->connected = s->IsConnected() ? 1 : 0;
    out->sw = s->GetSenderWidth();
    out->sh = s->GetSenderHeight();
    out->sfmt = (unsigned int)s->GetSenderFormat();
    out->cpu = s->GetSenderCPU() ? 1 : 0;
    out->gldx = s->GetSenderGLDX() ? 1 : 0;
    out->frame = (long long)s->GetSenderFrame();
    out->handle = (unsigned long long)(uintptr_t)s->GetSenderHandle();
    (void)cap;
    return 1;
}

} // extern "C"