//go:build spout

// Flat C wrappers over the Spout SDK (SpoutLibrary.h pulls in <windows.h>, <d3d11.h>,
// <string>, <vector> - compiled by g++ via cgo). One SPOUTLIBRARY handle per deck/thread.
#include <cstdlib>
#include <cstring>
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
    SPOUTHANDLE s = make_spout();
    if (!s) return -1;
    int n = s->GetSenderCount();
    s->Release();
    return n;
}

int rave_spout_find(const char* name) {
    SPOUTHANDLE s = make_spout();
    if (!s) return -1;
    int found = s->FindSenderName(name) ? 1 : 0;
    s->Release();
    return found;
}

int rave_spout_sender_name(int idx, char* out, int cap) {
    if (!out || cap <= 0) return 0;
    SPOUTHANDLE s = make_spout();
    if (!s) return 0;
    int ok = s->GetSender(idx, out, cap) ? 1 : 0;
    s->Release();
    return ok;
}

int rave_spout_sender_size(const char* name, unsigned int* w, unsigned int* h) {
    if (!name || !w || !h) return 0;
    SPOUTHANDLE s = make_spout();
    if (!s) return 0;
    unsigned int ww = 0, hh = 0;
    HANDLE share = 0;
    DWORD fmt = 0;
    int ok = s->GetSenderInfo(name, ww, hh, share, fmt) ? 1 : 0;
    s->Release();
    if (ok) { *w = ww; *h = hh; }
    return ok;
}

void rave_spout_set_receiver(void* h, const char* name) {
    if (!h) return;
    ((SPOUTHANDLE)h)->SetReceiverName(name);
}

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
    if (!dst || cap < (size_t)(*w) * (*hgt) * 4) return 2; // caller must (re)size
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