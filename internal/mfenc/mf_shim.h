// mf_shim.h - C ABI over the Media Foundation hardware H.264 encode pipeline:
// RGBA upload -> D3D11 -> VideoProcessorMFT (CSC+scale, GPU) -> async HW encoder MFT.
// One handle = one pipeline; ALL calls for a handle must come from ONE thread (the Go
// side locks a goroutine to an OS thread; COM is initialized MTA on it).
#ifndef RAVE_MF_SHIM_H
#define RAVE_MF_SHIM_H
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct mfenc mfenc;

// mf_shim_available: 1 = D3D11 hardware device + >=1 hardware H.264 encoder MFT exist.
int mf_shim_available(void);

// mf_enc_open builds the pipeline (H.264 only). adapterLuid pins the GPU: DXGI
// AdapterLuid packed HighPart<<32|LowPart, 0 = default adapter. An unknown/unusable
// LUID degrades to the default adapter rather than failing. in dims = source; out dims
// = encode target (VP scales when different; caller pre-clamps to even). fps as
// rational. Returns NULL + errbuf ("stage hr=0x...") on failure.
mfenc* mf_enc_open(int64_t adapterLuid, int inW, int inH, int outW, int outH,
                   int fpsN, int fpsD, int bitrateKbps, int gopFrames,
                   char* errbuf, int errcap);

// mf_enc_feed uploads + converts + submits one RGBA frame (R,G,B,A byte order, stride
// bytes/row). pts100 = presentation time in 100ns. Blocks until the encoder accepts
// input (bounded internal wait). <0 = error.
int mf_enc_feed(mfenc* e, const uint8_t* rgba, int stride, int64_t pts100);

// mf_enc_next pops one pending access unit into out (annex-B). Returns size, 0 = none
// pending, -1 = error; size > cap: NOTHING consumed, returns -(size) so caller regrows
// (cap must then be >= size).
int mf_enc_next(mfenc* e, uint8_t* out, int cap, int64_t* pts100, int* keyframe);

// mf_enc_force_idr requests an IDR on the next fed frame (live, no restart).
int mf_enc_force_idr(mfenc* e);

// mf_enc_drain flushes the encoder; keep calling mf_enc_next until 0 afterwards.
int mf_enc_drain(mfenc* e);

void mf_enc_close(mfenc* e);

// mf_enc_input_is_bgra: 1 = VP negotiated ARGB32 (BGRA memory) and the shim swizzles
// RGBA rows during upload (diagnostic; color correctness is handled either way).
int mf_enc_input_is_bgra(mfenc* e);

// mf_enc_last_hr: HRESULT of the last pipeline failure (0 = none; diagnostic).
int64_t mf_enc_last_hr(mfenc* e);

// mf_enc_name copies the active encoder MFT's friendly name (diagnostic).
void mf_enc_name(mfenc* e, char* out, int cap);

// mf_swizzle_rgba_bgra converts n pixels RGBA->BGRA (exposed for the Go unit test).
void mf_swizzle_rgba_bgra(uint8_t* dst, const uint8_t* src, int npx);

#ifdef __cplusplus
}
#endif
#endif
