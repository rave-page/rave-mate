/* ravezig — rave-mate native core (Zig), C ABI. Mirror of src/root.zig exports.
 * ABI v1. Go binding: internal/zignative. */
#ifndef RAVEZIG_H
#define RAVEZIG_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

uint32_t rz_abi_version(void);

/* Streaming polyphase windowed-sinc resampler; interleaved f32; zero added latency. */
typedef struct RzResampler RzResampler;
RzResampler *rz_resampler_new(uint32_t in_rate, uint32_t out_rate, uint32_t channels);
void rz_resampler_free(RzResampler *r);
void rz_resampler_reset(RzResampler *r);
size_t rz_resampler_out_cap(const RzResampler *r, size_t in_frames);
/* Returns frames written; SIZE_MAX on error (out too small / alloc fail). */
size_t rz_resampler_process(RzResampler *r, const float *in, size_t in_frames,
                            float *out, size_t out_cap_frames);

/* Waveform / gain kernels over interleaved f32. */
void rz_wave_bins(const float *in, size_t frames, uint32_t channels, size_t n_bins,
                  float *out_min, float *out_max, float *out_rms);
void rz_apply_gain(float *buf, size_t n, float gain);
float rz_peak_abs(const float *in, size_t n);

/* s16le mono analysis kernels — byte-exact ports of worker bucketPeaks/bucketBands. */
size_t rz_bucket_peaks(const uint8_t *pcm, size_t pcm_len, size_t n, uint8_t *out);
size_t rz_bucket_bands(const uint8_t *pcm, size_t pcm_len, size_t n, uint32_t fs, uint8_t *out);

#ifdef __cplusplus
}
#endif

#endif /* RAVEZIG_H */
