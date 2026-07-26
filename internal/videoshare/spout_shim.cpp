//go:build spout

// Flat C wrappers over the Spout SDK (SpoutLibrary.h pulls in <windows.h>, <d3d11.h>,
// <string>, <vector> - compiled by g++ via cgo). One SPOUTLIBRARY handle per deck/thread.
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

// rave_spout_send publishes one RGBA frame, applying a geometric flip first (flip bit0=horizontal,
// bit1=vertical). bInvert is always false - the explicit flip fully controls orientation so the
// user can pick the mode that lands upright in their receiver (RAVE_SPOUT_FLIP). deckcard.Render
// produces top-row-first RGBA.
int rave_spout_send(void* h, const char* name, const unsigned char* rgba,
                    unsigned int w, unsigned int height, int flip) {
    if (!h || !rgba) return 0;
    SPOUTHANDLE s = (SPOUTHANDLE)h;
    s->SetSenderName(name);
    if (flip == 0) {
        return s->SendImage(rgba, w, height, 0x1908 /*GL_RGBA*/, false) ? 1 : 0;
    }
    const size_t n = (size_t)w * height;
    unsigned char* t = (unsigned char*)malloc(n * 4);
    if (!t) return 0;
    for (unsigned int y = 0; y < height; y++) {
        unsigned int sy = (flip & 2) ? (height - 1 - y) : y;
        for (unsigned int x = 0; x < w; x++) {
            unsigned int sx = (flip & 1) ? (w - 1 - x) : x;
            memcpy(t + ((size_t)y * w + x) * 4, rgba + ((size_t)sy * w + sx) * 4, 4);
        }
    }
    int ok = s->SendImage(t, w, height, 0x1908 /*GL_RGBA*/, false) ? 1 : 0;
    free(t);
    return ok;
}

void rave_spout_release(void* h) {
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
int rave_spout_recv(void* h, unsigned char* pixels, unsigned int cap, unsigned int* w, unsigned int* hgt) {
    if (!h || !w || !hgt) return -1;
    SPOUTHANDLE s = (SPOUTHANDLE)h;
    // Defensive: never hand ReceiveImage a buffer smaller than the sender's current frame -
    // the SDK skips the copy on a dimension change, but a stale-size race would overrun.
    unsigned char* dst = pixels;
    unsigned int sw = s->GetSenderWidth(), sh = s->GetSenderHeight();
    if (!dst || (sw && sh && cap < (size_t)sw * sh * 4)) dst = NULL;
    if (!s->ReceiveImage(dst, 0x1908 /*GL_RGBA*/, false, 0)) return -1;
    *w = s->GetSenderWidth();
    *hgt = s->GetSenderHeight();
    if (s->IsUpdated()) return 2;
    if (!dst || cap < (size_t)(*w) * (*hgt) * 4) return 3; // caller must (re)size
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