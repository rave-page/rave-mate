//go:build linux && pipewire

/* vaenc.h - VA-API zero-copy dmabuf -> NV12 (VPP) -> H.264/HEVC encode, foundation of the
 * Linux "vaapi-native" engine. libva is dlopen'd at runtime (ABI-stable); absent => unavailable.
 * This header currently exposes the import+VPP PROBE; the encoder surface follows once the probe
 * proves radeonsi's DRM_PRIME_2 import handles the multi-plane tiled/DCC layout EGL rejected. */
#ifndef RAVE_PWCAPTURE_VAENC_H
#define RAVE_PWCAPTURE_VAENC_H

#include <stdint.h>

/* rva_available dlopens libva + libva-drm. 1 = usable. */
int rva_available(void);

/* rva_import_probe imports a dmabuf (DRM fourcc + modifier + planes) as a VA surface via
 * DRM_PRIME_2, VPP-converts it to NV12, derives the NV12 image and reads the 4 corner pixels back
 * (converted to RGB) into corners[12] = {TL,TR,BL,BR} x {R,G,B}. node = render node. Returns 0 on
 * success, <0 on failure (the failing VA step + status string go to stderr). Self-contained: it
 * builds + tears down its own VADisplay. Proves import + VPP + pixel-correctness at any size. */
int rva_import_probe(const char *node, uint32_t drm_fourcc, uint64_t modifier, int w, int h,
                     int n_planes, const int *fds, const unsigned *offsets, const unsigned *strides,
                     unsigned char corners[12]);

/* rva_log_enc_caps logs the H.264 encode caps (rate control / packed headers / max refs). */
void rva_log_enc_caps(const char *node);

#endif
