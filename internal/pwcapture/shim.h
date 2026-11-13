//go:build linux && pipewire

/* shim.h - C surface of the PipeWire capture shim (see shim.c). All PipeWire access is via
 * dlopen at runtime, so a binary built with -tags pipewire still launches where libpipewire-0.3
 * is absent (rpw_available() == 0). */
#ifndef RAVE_PWCAPTURE_SHIM_H
#define RAVE_PWCAPTURE_SHIM_H

#include <ravemedia.h> /* native TX stats type (rm_tx_stats); -I set in capture_pipewire.go cgo CFLAGS */

struct rpw_capture; /* opaque; defined in shim.c */

/* rpw_available dlopens libpipewire-0.3 and resolves the symbols we need. 1 = usable. */
int rpw_available(void);

/* rpw_start begins capturing the PipeWire node named target_node. Returns a handle, or NULL on
 * failure. The stream auto-connects + auto-reconnects by name, so a producer that restarts
 * (VRChat) is re-captured without a new handle. */
struct rpw_capture *rpw_start(const char *target_node);

/* rpw_wait_frame blocks up to timeout_ms for the next frame, converts it to RGBA (Go NRGBA order)
 * into dst. Sets the capture PTS (ns, 0 if none) in *pts. Returns bytes written (>0, == *w * *h * 4),
 * 0 on timeout, -1 on fatal stream error, -2 when cap is too small (sets *w,*h; frame kept). */
int rpw_wait_frame(struct rpw_capture *c, unsigned char *dst, int cap, int *w, int *h, long long *pts, long timeout_ms);

/* rpw_dropped returns the drop-oldest counter (frames the producer delivered that no Next consumed,
 * plus frames skipped because their buffer type isn't supported - e.g. DMA-BUF before EGL). */
long rpw_dropped(struct rpw_capture *c);

/* rpw_dmabuf_skipped returns how many frames were DMA-BUF (not handled by the SHM path). Nonzero
 * means the producer is DMA-BUF only and the EGL import path is required. */
long rpw_dmabuf_skipped(struct rpw_capture *c);

/* ---- vaapi-native encode mode (zero-copy dmabuf -> VA -> H.264/HEVC AU) ---- */

/* rpw_set_encode switches the capture to fused in-process VA encode (codec 0=H.264, 1=HEVC), coded
 * height capped by max_h (0 = native). quality_qp selects rate control: 0 = legacy rate-controlled
 * (CBR to kbps as a hard ceiling); >0 = constant-quality-with-ceiling (CQP at quality_qp, kbps the
 * ceiling). Call before consuming any AU. 1 = VA usable (use rpw_wait_au), 0 = VA unavailable (fall
 * back to rpw_wait_frame + ffmpeg). */
int rpw_set_encode(struct rpw_capture *c, int codec, int kbps, int max_h, int fps, const char *node, int quality_qp);

/* rpw_wait_au returns one Annex-B AU into dst (blocks up to timeout_ms), sets *keyframe, the CODED
 * dims in *w/*h, and the capture PTS (ns, 0 if the buffer carried none) in *pts. >0 bytes, 0 timeout,
 * -1 fatal, -2 cap too small (dims set, AU kept). */
int rpw_wait_au(struct rpw_capture *c, unsigned char *dst, int cap, int *w, int *h, int *keyframe, long long *pts, long timeout_ms);

/* rpw_request_keyframe forces an IDR on the next encoded frame (PLI / route start / reconnect). */
void rpw_request_keyframe(struct rpw_capture *c);

/* rpw_set_bitrate sets the live target bitrate (kbps) - the CBR target or the CQP ceiling - applied on
 * the next encoded frame. */
void rpw_set_bitrate(struct rpw_capture *c, int kbps);

/* rpw_set_quality sets the live constant-quality target QP (quality mode), applied on the next frame
 * without a reopen. If the encoder is currently legacy-mode it is re-created in quality mode. */
void rpw_set_quality(struct rpw_capture *c, int target_qp);

/* rpw_enc_feedback reports the adaptive-budget quality signal: *present (1 in quality mode), *avg_qp
 * (mean QP over ~1s), *rate_limited (ceiling forced QP above the quality target). */
void rpw_enc_feedback(struct rpw_capture *c, int *present, double *avg_qp, int *rate_limited);

/* rpw_set_cap_height applies a live CODED-height cap (0 = native); the encoder re-creates at the new
 * coded size on the next frame if changed. Nominal source geometry unchanged (receiver upscales). */
void rpw_set_cap_height(struct rpw_capture *c, int max_h);

/* encode telemetry getters. */
long rpw_enc_aus(struct rpw_capture *c);
long rpw_enc_drops(struct rpw_capture *c);
long rpw_cap_frames(struct rpw_capture *c);
long rpw_reconnects(struct rpw_capture *c);
/* rpw_egl_ready: 1 = dmabuf (EGL modifier) offer built; 0 = SHM-only offer (a dmabuf-only producer
 * such as libfunnel/Spout2PW can never link). */
int rpw_egl_ready(struct rpw_capture *c);
/* robustness counters: cadence repeats (static/slow/hitching source), pre-encode skips on a full AU
 * ring (downstream stall; no reference break), start-up holds (first AU unconsumed), forced IDRs. */
long rpw_repeats(struct rpw_capture *c);
long rpw_ring_skips(struct rpw_capture *c);
long rpw_start_holds(struct rpw_capture *c);
long rpw_forced_idrs(struct rpw_capture *c);

/* native-path telemetry, sampled by Go at <= 1 Hz: one stage's per-AU interval distribution (ms),
 * computed natively from a lock-free histogram (same buckets as medialink.StageHist). */
typedef struct {
	unsigned long long count, over25, over50, over100;
	double mean_ms, p50_ms, p99_ms, max_ms;
} rpw_stage_snap;
/* rpw_native_stages: enc = AUs leaving the encoder, tx = AUs queued to the native sender. */
void rpw_native_stages(struct rpw_capture *c, rpw_stage_snap *enc, rpw_stage_snap *tx);
long rpw_enc_keyframes(struct rpw_capture *c);
long long rpw_enc_bytes(struct rpw_capture *c);
int rpw_import_ok(struct rpw_capture *c);
int rpw_coded_w(struct rpw_capture *c);
int rpw_coded_h(struct rpw_capture *c);
/* AU hand-off ring telemetry: current fill, high-water mark, overflow drops. */
int rpw_ring_depth(struct rpw_capture *c);
int rpw_ring_hiwater(struct rpw_capture *c);
long rpw_ring_overflow(struct rpw_capture *c);
long rpw_cap_drops(struct rpw_capture *c);
long rpw_grid_drops(struct rpw_capture *c);

/* ---- native realtime TX (unit 3) ---- */
/* rpw_attach_native_tx opens a native sender + routes the encode thread to it. 1 = native engaged,
 * 0 = failure (keep the Go-ring fallback). transport 0=UDP/1=TCP; codec 4=H264/5=HEVC/6=AV1. */
int rpw_attach_native_tx(struct rpw_capture *c, int transport, int codec, int stream, int body_max,
                         const char *peer_host, const char *peer_port, int encrypt,
                         const unsigned char *send_key, unsigned send_prefix,
                         const unsigned char *recv_key, unsigned recv_prefix,
                         unsigned token, double initial_rate_bps,
                         unsigned long long initial_seq, int fps, long long startup_grace_ns,
                         long long media_clock_ns);
void rpw_native_tx_set_rate(struct rpw_capture *c, double bytes_per_sec);
void rpw_native_tx_stats(struct rpw_capture *c, rm_tx_stats *out);
void rpw_native_tx_rate_hint(struct rpw_capture *c, rm_rate_hint *out);
void rpw_set_cap_fps(struct rpw_capture *c, int fps);
void rpw_native_tx_close(struct rpw_capture *c);

/* rpw_stop disconnects + frees the capture. */
void rpw_stop(struct rpw_capture *c);

#endif
