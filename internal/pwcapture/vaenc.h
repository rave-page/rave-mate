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

/* rva_enc is a combined VPP+H.264 encode session: dmabuf -> VA import -> VPP NV12 (+ mirror/scale
 * to out_w x out_h) -> H.264 EncSlice -> Annex-B. All on the GPU; only the bitstream crosses out. */
typedef struct rva_enc rva_enc;

/* rva_enc_new builds a session on render node. codec 0=H.264, 1=HEVC. out_w/out_h = coded output
 * (scale when != source). quality_qp selects the rate-control regime:
 *   0  => legacy rate-controlled (VA_RC_CBR to bitrate_kbps as a hard ceiling - unchanged behaviour).
 *   >0 => constant-quality-with-ceiling (VA_RC_CQP): hold QP at quality_qp (the target from
 *         medialink.QualityToQP) so simple content undershoots, and let the software ceiling
 *         controller RAISE QP toward 51 only when the 1s rate would exceed bitrate_kbps. The target QP
 *         is the floor; the ceiling is bitrate_kbps.
 * Returns NULL on failure. */
rva_enc *rva_enc_new(const char *node, int codec, int out_w, int out_h, int fps, int bitrate_kbps, int quality_qp);

/* rva_enc_frame encodes one dmabuf frame. On success sets *au (session-owned, valid until the next
 * call/free) + *au_len (Annex-B, AUD + [SPS/PPS on IDR] + slice) + *keyframe. flip=1 mirrors
 * vertically (bottom-up GL producer); 0 keeps orientation (top-down DX/Spout). force_idr=1 forces an
 * IDR (PLI/reconnect). Returns 0 ok, <0 on error. */
int rva_enc_frame(rva_enc *e, uint32_t drm_fourcc, uint64_t modifier, int w, int h, int n_planes,
                  const int *fds, const unsigned *offsets, const unsigned *strides,
                  int flip, int force_idr, unsigned char **au, int *au_len, int *keyframe);

/* rva_enc_set_bitrate updates the live target bitrate (kbps): the CBR target in legacy mode, or the
 * constant-quality CEILING in quality mode. Applied on the next encoded frame (no reopen). */
void rva_enc_set_bitrate(rva_enc *e, int kbps);

/* rva_enc_set_quality updates the live constant-quality target QP (quality mode only; no reopen). A
 * higher target_qp = lower quality. cur_qp is re-floored to the new target. No-op in legacy mode. */
void rva_enc_set_quality(rva_enc *e, int target_qp);

/* rva_enc_feedback reports the per-second quality signal (adaptive budget). *present is 1 in quality
 * mode (the encoder exposes a real QP), 0 in legacy mode. *avg_qp is the mean quantizer over the last
 * ~1s (exact in CQP: the QP the frames were encoded at). *rate_limited is 1 when the bitrate ceiling
 * forced cur_qp above the quality target (content wants more than the ceiling allows). Any pointer
 * may be NULL. */
void rva_enc_feedback(rva_enc *e, int *present, double *avg_qp, int *rate_limited);

/* rva_qp_next is the PURE constant-quality ceiling controller (no VA; unit-tested with injected
 * rates). Given cur_qp and the quality floor target_qp, the bits emitted (win_bits) over `frames`
 * frames at `fps`, and the ceiling budget_bits (bits/sec), it returns the next QP in [target_qp, 51]:
 * raise promptly when the windowed rate exceeds the ceiling (protect the 1s peak), hold in a dead
 * band, and release one step toward target_qp only after a sustained comfortable run (*under streak -
 * hysteresis, no visible pumping). */
int rva_qp_next(int cur_qp, int target_qp, long long win_bits, int frames, int fps,
                long long budget_bits, int *under);

/* rva_enc_free tears down the session (in order). */
void rva_enc_free(rva_enc *e);

#endif
