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

// rave_spout_send publishes one RGBA frame, applying a geometric flip first (flip bit0=horizontal,
// bit1=vertical). bInvert is always false - the explicit flip fully controls orientation so the
// user can pick the mode that lands upright in their receiver (RAVE_SPOUT_FLIP). deckcard.Render
// produces top-row-first RGBA.
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
    unsigned char* t = flip_buf;
    for (unsigned int y = 0; y < height; y++) {
        unsigned int sy = (flip & 2) ? (height - 1 - y) : y;
        for (unsigned int x = 0; x < w; x++) {
            unsigned int sx = (flip & 1) ? (w - 1 - x) : x;
            memcpy(t + ((size_t)y * w + x) * 4, rgba + ((size_t)sy * w + sx) * 4, 4);
        }
    }
    return s->SendImage(t, w, height, 0x1908 /*GL_RGBA*/, false) ? 1 : 0;
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

void rave_spout_set_receiver(void* h, const char* name) {
    if (!h) return;
    ((SPOUTHANDLE)h)->SetReceiverName(name);
}

// rave_spout_recv: -1 receive failed, 0 no new frame, 1 new frame in pixels, 2 sender
// (re)connected/resized (IsUpdated - real activity, one prompt resize pass), 3 pixels
// absent/undersized with NO sender update (caller resizes quietly - reporting this as 2
// used to re-arm the receiver's 250 Hz poll forever against a stale/0x0 sender). Codes
// mirrored in recvpoll.go.
// recv_dims resolves the named sender's current size. GetSenderInfo (a shared-memory registry
// read, the same call rave_spout_scan uses successfully) rather than
// GetSenderWidth/GetSenderHeight: those two vtable slots are SKEWED against the shipped
// SpoutLibrary.dll on at least one 2.007.x pairing - GetSenderWidth() dispatched to
// GetSenderName() and returned a truncated POINTER (139846784) while GetSenderHeight() returned
// the real width. Sizing a buffer from that made the media child allocate 2 TB and die with
// "runtime: cannot allocate memory". Falls back to the width/height slots (guarded) only when no
// name is known.
static bool recv_dims(SPOUTHANDLE s, const char* name, unsigned int* w, unsigned int* h) {
    unsigned int ww = 0, hh = 0;
    if (name && name[0]) {
        HANDLE share = 0;
        DWORD fmt = 0;
        if (!s->GetSenderInfo(name, ww, hh, share, fmt)) { ww = 0; hh = 0; }
    }
    if (!RAVE_SPOUT_DIM_OK(ww) || !RAVE_SPOUT_DIM_OK(hh)) {
        ww = s->GetSenderWidth();
        hh = s->GetSenderHeight();
    }
    if (!RAVE_SPOUT_DIM_OK(ww) || !RAVE_SPOUT_DIM_OK(hh)) {
        *w = 0;
        *h = 0;
        return false;
    }
    *w = ww;
    *h = hh;
    return true;
}

int rave_spout_recv(void* h, const char* name, unsigned char* pixels, unsigned int cap, unsigned int* w, unsigned int* hgt) {
    if (!h || !w || !hgt) return -1;
    SPOUTHANDLE s = (SPOUTHANDLE)h;
    // Size FIRST, receive second. ReceiveImage is never called without a correctly sized
    // buffer: the SDK's readback writes w*h*4 bytes, so an absent/undersized target is
    // either an overrun or (observed) an access violation inside the DLL on the NEXT call.
    // Dims come from the registry, which needs no connection - so the caller can allocate
    // before the first receive instead of probing with a NULL buffer.
    if (!recv_dims(s, name, w, hgt)) return 3; // no usable size yet: caller retries
    if (!pixels || cap < (size_t)(*w) * (*hgt) * 4) return 3; // caller must (re)size
    if (!s->ReceiveImage(pixels, 0x1908 /*GL_RGBA*/, false, 0)) return -1;
    if (s->IsUpdated()) return 2; // resized under us: caller re-sizes, frame comes next poll
    return s->IsFrameNew() ? 1 : 0;
}

void rave_spout_recv_release(void* h) {
    if (!h) return;
    SPOUTHANDLE s = (SPOUTHANDLE)h;
    s->ReleaseReceiver();
    s->CloseOpenGL();
    s->Release();
}

} // extern "C"