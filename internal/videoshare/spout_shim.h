//go:build spout

// Flat C API over the Spout SDK's COM-like SPOUTLIBRARY handle. Lets cgo (sender_spout.go)
// drive Spout without touching C++ vtables. Implemented in spout_shim.cpp (compiled by g++).
#ifndef RAVE_SPOUT_SHIM_H
#define RAVE_SPOUT_SHIM_H

#ifdef __cplusplus
extern "C" {
#endif

// Report whether SpoutLibrary.dll loaded at runtime (1) or is absent/missing GetSpout (0).
// The DLL is LoadLibrary'd lazily (not import-linked), so a missing DLL is non-fatal.
int rave_spout_available(void);

// Create a Spout handle with its own OpenGL context for the CALLING thread.
// Returns NULL if CreateOpenGL fails (e.g. no GPU/headless). Must be called on the
// goroutine (LockOSThread'd) that will own all subsequent rave_spout_send calls.
void* rave_spout_create(void);

// Publish one upright RGBA frame to the named sender via the handle h.
// pixels = tightly packed RGBA (w*h*4), GL_RGBA byte order. Returns 1 on success, 0 on failure.
int rave_spout_send(void* h, const char* name, const unsigned char* rgba,
                    unsigned int w, unsigned int height, int flip);

// rave_spout_open_sender initialises the named sender at the given geometry and returns its DX11
// SHARED-TEXTURE handle in *share, so a foreign D3D11 device (the decoder child, zigmedia inc 2)
// can render INTO it instead of pushing raw frames back through this process. fmt = DXGI format to
// request (0 = Spout's default). Call on h's owning thread, before any rave_spout_send.
//
// SPOUTLIBRARY has no CreateSender: a sender's texture is allocated by the first send. So this
// publishes ONE zeroed frame to force the allocation, then reads GetHandle(). That is one
// w*h*4 write per ROUTE (reusing the pooled flip buffer, no malloc), not per frame.
// The handle + the ACTUAL format come back out of the registry (GetSenderInfo), not GetHandle():
// on the shipped SDK pairing GetHandle() is NULL for a sender created through SendImage.
// 1 = ok and *share is non-zero; 0 = bad args; -1 = SendImage refused; -2 = no DX11 shared
// texture (a CPU/memoryshare sender). Anything but 1 means "keep the frame path".
int rave_spout_open_sender(void* h, const char* name, unsigned int w, unsigned int hgt,
                          unsigned int fmt, unsigned long long* share, unsigned int* out_fmt);

// Release the sender, close its OpenGL context, free the handle. Call on the owning thread.
void rave_spout_release(void* h);

// Test-only receiver helpers: spin up a throwaway handle just to query the global sender list.
// No OpenGL context needed for the name registry.
int rave_spout_sender_count(void);
int rave_spout_find(const char* name);

// Sender registry queries (shared process-wide handle, no GL): copy the idx-th sender name into
// out (cap bytes, NUL-terminated; 1 on success) / a named sender's current dimensions.
int rave_spout_sender_name(int idx, char* out, int cap);
int rave_spout_sender_size(const char* name, unsigned int* w, unsigned int* h);

// rave_spout_sender_share resolves a named sender's DX11 SHARED-TEXTURE handle + DXGI format +
// dims in ONE registry read (GetSenderInfo - the same shared-memory read rave_spout_scan uses).
// Lets a zero-copy consumer open that texture on its OWN D3D11 device instead of paying a
// GPU→CPU readback. 1 = ok. share == 0 → no DX11 shared texture (DX9 / CPU memoryshare sender):
// the caller must keep the readback path. Dims are validated like every other shim geometry.
int rave_spout_sender_share(const char* name, unsigned long long* share, unsigned int* fmt,
                           unsigned int* w, unsigned int* h);

// One-shot registry scan: names = maxN slots of nameCap bytes (NUL-terminated), dims = 2 uints per
// slot (w,h; 0 when the registry has no size). Returns the filled slot count, -1 without the DLL.
// Replaces count + N name + N size calls (which each built and released a Spout object).
int rave_spout_scan(char* names, int nameCap, int maxN, unsigned int* dims);

// Receiver: bind h (from rave_spout_create, on its owning thread) to the named sender.
void rave_spout_set_receiver(void* h, const char* name);

// Poll one frame into pixels (cap bytes, RGBA). Returns:
//   2 = sender (re)connected / dimensions changed - *w/*hgt set, pixels NOT written; resize + recall
//   1 = new frame copied into pixels (*w/*hgt set)
//   0 = connected, no new frame
//  -1 = no connection to the sender (yet)
// Per the Spout SDK contract, a dimension change sets the update flag WITHOUT writing pixels,
// so the caller reallocates before the next call. cap is a defensive double-check.
int rave_spout_recv(void* h, const char* name, unsigned char* pixels, unsigned int cap, unsigned int* w, unsigned int* hgt);

// Release the receiver binding + GL context + handle (owning thread).
void rave_spout_recv_release(void* h);

#ifdef __cplusplus
}
#endif

#endif // RAVE_SPOUT_SHIM_H
