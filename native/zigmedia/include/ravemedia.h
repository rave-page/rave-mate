/* ravemedia — native realtime media transport, SENDER C ABI. Mirror of src/root.zig exports.
 * ABI v2 (owner directive 2026-11-12: the per-frame path is native, no Go runtime in the loop).
 * ABI v2 (2026-11-12): rm_open_params gained struct_size (mismatched-caller guard), initial_seq
 *   (the native sender CONTINUES the Go handshake conn's datagram seq space — without it the sender
 *   started at 0, the receiver's replay window rejected it as too-old and never rebound → no media),
 *   fps and startup_grace_ns (mirror NativeTXParams field-for-field). A caller built against v1 sets
 *   abi_version=1 and is rejected cleanly (RM_ERR_ARGS); any struct-size skew is caught too.
 * ABI v3 (2026-11-12): rm_open_params.media_clock_ns (PTS rebased into the Go media-clock domain,
 *   0 = sender-local monotonic as in v2); rm_tx_stats.nacks_recv. Callers recompile; no call-site
 *   change is required (both new fields keep v2 behaviour when zeroed).
 * ABI v4 (2026-11-13): rm_tx_rate_hint - the receiver's MetaRate (congestion AIMD + VRAM governor +
 *   adaptive budget) reaches the encoder on a native route; the sender also answers MetaSync and PLI
 *   natively. Callers recompile; poll rm_tx_rate_hint (~10 Hz) and apply on a gen change.
 * ABI v5 (2026-11-13): RECEIVER rm_rx_* (native UDP receive: reassembly, NACK, adaptive PTS-locked
 *   playout, PLI; frames delivered in cadence via callback). Sender API unchanged.
 *
 * Ownership split: Go negotiates the route (Offer/Answer), derives the per-route keys + token, then
 * opens a native sender and hands it the derived material. The encode thread (Linux VA shim /
 * Windows MF child) calls rm_tx_submit_au directly — non-blocking, no Go on the per-frame path. The
 * sender owns its own TX thread(s) + a control-RX thread (UDP NACK/keepalive). Go only samples the
 * lock-free stats block at ~1 Hz and pushes rate hints.
 *
 * Wire format is byte-exact with internal/medialink (udpframe.go / frame.go / transport.go); see
 * .devnotes/ZIGMEDIA_TRANSPORT.md. Supports BOTH transports: UDP datagram profile (explicit-nonce
 * AES-256-GCM, user-space fragmentation, NACK) and TCP (length-prefixed, implicit-counter GCM).
 *
 * ABI contract (confirmed 2026-11-12):
 *  - DUAL PATH: a source either hands its AU stream to the native sender here (rm_tx_submit_au), OR,
 *    when no native sender is attached, pushes into its own AU ring that the Go pump reads. The Go
 *    hook (internal/medialink NativeTXSource.AttachNativeTX) decides per route and falls back to the
 *    Go pump for anything unsupported (TCP until it lands, legacy peer, attach error).
 *  - CONCURRENCY: rm_tx_set_rate and rm_tx_stats_get are safe to call concurrently with
 *    rm_tx_submit_au (the stats block is lock-free; the rate is a benign racy write applied on the
 *    next pacer reserve).
 *  - AU FORMAT: submit AUs exactly as medialink.Frame.Payload carries them today - Annex-B with the
 *    in-band parameter sets, the caller sets RM_FLAG_KEYFRAME on IDRs, NO separate CONFIG AU is
 *    required (SPS/PPS ride the keyframe AU).
 *  - LIFECYCLE: the IDR callback NEVER fires after rm_tx_close returns.
 *  - SOCKET (UDP answerer): the native sender opens its OWN socket toward the receiver's address
 *    (rm_open_params.peer_addr, socket_fd < 0) and starts at InitialSeq (continuing the Go handshake
 *    conn's seq space), so its first datagram is strictly-newest at the receiver, which rebinds its
 *    remote to the native socket (existing replay/rebind rule) - receiver->sender control (NACK/ping/
 *    bye) then reaches the native socket. Alternatively Go hands a dup'd connected fd (socket_fd >= 0).
 */
#ifndef RAVEMEDIA_H
#define RAVEMEDIA_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define RM_ABI_VERSION 5u

uint32_t rm_abi_version(void);
int rm_wire_selftest(void); /* 0 = wire core matches its goldens (integration sanity) */

typedef enum { RM_TRANSPORT_UDP = 0, RM_TRANSPORT_TCP = 1 } rm_transport;

/* = medialink.Codec (frame header codec byte). */
typedef enum { RM_CODEC_H264 = 4, RM_CODEC_HEVC = 5, RM_CODEC_AV1 = 6 } rm_codec;

/* Frame flags (= medialink.Flag); OR together. */
#define RM_FLAG_KEYFRAME 0x01u
#define RM_FLAG_CONFIG 0x02u /* SPS/PPS/extradata, not displayable */
#define RM_FLAG_END 0x04u

typedef enum {
    RM_OK = 0,
    RM_ERR_ARGS = -1,          /* bad params / abi mismatch */
    RM_ERR_CRYPTO = -2,        /* key/cipher setup failed */
    RM_ERR_SOCKET = -3,        /* socket/connect failed */
    RM_ERR_SEQ_EXHAUSTED = -4, /* per-key datagram seq budget spent -> control plane renegotiates */
    RM_ERR_CLOSED = -5,        /* sender already closed */
    RM_ERR_UNIMPL = -6         /* not yet implemented (increment 2b) */
} rm_status;

/* rm_keys: the per-route AEAD material Go derived (routeKeySalt -> HKDF). The native side never sees
 * the peerlink master secret. send_* = this sender's direction; recv_* authenticates the receiver's
 * control datagrams (UDP NACK/ping). UDP uses the explicit nonce prefix; TCP ignores it and uses the
 * lockstep counter nonce (the `initiator` parity selects the counter direction). encrypt=0 ->
 * plaintext (both-local opt-out, token-gated only). */
typedef struct {
    uint8_t send_key[32];
    uint32_t send_nonce_prefix; /* UDP explicit-nonce prefix; TCP: ignored */
    uint8_t recv_key[32];
    uint32_t recv_nonce_prefix;
    uint32_t token;    /* routeToken: per-route demux + anti-injection */
    int32_t encrypt;   /* 0 = plaintext, 1 = AES-256-GCM */
    int32_t initiator; /* direction parity (TCP counter nonce; UDP uses the derived direction) */
} rm_keys;

/* rm_open_params fully specifies one sender route. Peer target is EITHER an already-open OS socket
 * handed from Go (socket_fd >= 0; native takes ownership), OR native opens one to peer_addr
 * (socket_fd < 0; peer_addr = sockaddr bytes). The UDP mux port the receiver dials is the one the
 * Answer advertised, so when Go hands a bound/connected socket its local port MUST match that. */
typedef struct {
    uint32_t abi_version;    /* = RM_ABI_VERSION (mismatch -> RM_ERR_ARGS, no struct access) */
    uint32_t struct_size;    /* = sizeof(rm_open_params); layout-skew guard -> RM_ERR_ARGS */
    rm_transport transport;  /* UDP or TCP (the owner's route may be either) */
    rm_codec codec;          /* payload codec -> frame header */
    uint16_t stream;         /* negotiated media stream id */
    int32_t body_max;        /* UDP fragment body cap (0 -> 1200, MTU-safe); ignored for TCP */
    int64_t socket_fd;       /* OS socket handle (Windows SOCKET / POSIX fd); < 0 -> native opens one */
    const uint8_t *peer_addr;/* sockaddr_in/in6 bytes when socket_fd < 0; else NULL */
    int32_t peer_addr_len;
    rm_keys keys;
    double initial_rate_bytes_per_sec; /* pacer rate (0 = unpaced) */
    uint64_t initial_seq;    /* first datagram seq: CONTINUE the Go conn's seq space (udpConn.sendSeq())
                              * so the first native datagram is strictly-newest at the receiver, which
                              * rebinds its remote to the native socket. 0 against an advanced receiver
                              * window is rejected as too-old (no rebind, no media) - by design. */
    int32_t fps;             /* nominal frame-rate rung (0 = unknown); mirrors NativeTXParams.FPS */
    int64_t startup_grace_ns;/* liveness grace before the route establishes (0 -> native default 15s) */
    int64_t media_clock_ns;  /* Go media clock (MediaClock.Now) sampled just before rm_tx_open; PTS are
                              * rebased into that domain. 0 -> sender-local monotonic (v2 behaviour). */
} rm_open_params;

typedef struct rm_sender rm_sender; /* opaque */

/* rm_idr_cb fires (off the submit thread) when the TX queue had to drop an AU -> the encoder should
 * emit an IDR so the receiver resyncs. `user` is the opaque passed to rm_tx_open. */
typedef void (*rm_idr_cb)(void *user);

/* rm_tx_stats: lock-free snapshot Go samples at ~1 Hz (never crosses the frame path). */
typedef struct {
    uint64_t aus_submitted;
    uint64_t aus_sent;
    uint64_t aus_dropped;    /* queue-full drops (-> IDR requested) */
    uint64_t datagrams_sent; /* UDP */
    uint64_t bytes_sent;
    uint64_t retransmits;    /* UDP NACK responses */
    uint64_t seq;            /* current datagram seq (UDP) -> seq-budget watch */
    int32_t queue_depth;     /* AUs queued now */
    int32_t dead;            /* 1 = TX thread exited (seq exhausted / fatal socket) */
    uint64_t nacks_recv;     /* authenticated NACK datagrams received (UDP) */
} rm_tx_stats;

/* rm_tx_open creates + starts a sender (its own TX thread + UDP control-RX). RM_OK + *out on success. */
rm_status rm_tx_open(const rm_open_params *p, rm_idr_cb on_need_idr, void *user, rm_sender **out);

/* rm_tx_submit_au hands ONE encoded access unit to the sender. NON-BLOCKING: copies into the bounded
 * TX queue and returns at once; a full queue drops the OLDEST non-keyframe AU and fires on_need_idr.
 * pts_ns = capture/encode PTS; flags = RM_FLAG_*. Safe from the encode thread. */
rm_status rm_tx_submit_au(rm_sender *s, const uint8_t *au, size_t len, int64_t pts_ns, uint32_t flags);

/* rm_tx_set_rate updates the pacer rate live (bytes/sec; 0 = unpaced) — the Go rate-hint path. */
void rm_tx_set_rate(rm_sender *s, double bytes_per_sec);

/* rm_tx_stats_get copies the current stats (lock-free). */
void rm_tx_stats_get(rm_sender *s, rm_tx_stats *out);

/* rm_rate_hint = medialink.RateHint (0 field = no cap on that axis / clear it) + generation counter. */
typedef struct {
    uint64_t gen;             /* increments per received MetaRate; 0 = none received yet */
    int32_t max_bitrate_kbps; /* reduced FIRST */
    int32_t max_fps;          /* reduced SECOND */
    int32_t max_height;       /* LAST resort; 0 = native coded height */
    int32_t quality;          /* constant-quality target 1..100; 0 = no change */
} rm_rate_hint;

/* rm_tx_rate_hint copies the last receiver rate hint (lock-free; any thread). */
void rm_tx_rate_hint(rm_sender *s, rm_rate_hint *out);

/* rm_tx_close stops the TX thread(s), sends a clean bye, and frees the sender. No callback fires
 * after it returns. */
void rm_tx_close(rm_sender *s);

/* ── receiver (ABI v5) ─────────────────────────────────────────────────────────────────────────────
 * Go negotiates + runs the reachability handshake on its UDP socket, then hands a DUPLICATE of that
 * socket (socket_fd; Windows: WSADuplicateSocket -> WSASocket FROM_PROTOCOL_INFO in the target
 * process) and the derived keys; the native receiver owns the RX path from then on. Control the
 * receiver sends (NACK/PLI/ping/meta) continues initial_seq (the Go conn's send seq). Go-originated
 * meta (MetaRate, reports, sync) goes through rm_rx_send_meta so ONE seq/nonce counter seals it. */
typedef struct rm_receiver rm_receiver; /* opaque */

typedef struct {
    uint32_t abi_version;     /* = RM_ABI_VERSION */
    uint32_t struct_size;     /* = sizeof(rm_rx_params) */
    int64_t socket_fd;        /* bound UDP socket; native takes ownership */
    const uint8_t *peer_addr; /* sender sockaddr_in (optional, NULL = learn from the first datagram) */
    int32_t peer_addr_len;
    rm_keys keys;             /* send_* seals OUR control; recv_* opens the sender's media */
    int32_t body_max;         /* fragment body cap (0 -> 1200) */
    int32_t fps;              /* nominal frame rate (0 -> 60) */
    int32_t preset;           /* playout: 0 balanced, 1 low-latency, 2 smooth */
    uint16_t stream;          /* media stream id (PLI target) */
    uint64_t initial_seq;     /* first control datagram seq */
    int32_t max_frame_bytes;  /* largest AU (0 -> 4 MiB) */
} rm_rx_params;

/* rm_rx_frame_cb delivers ONE frame at its playout deadline on the receiver thread; au is valid only
 * during the call (copy or consume synchronously). key = decodable alone. */
typedef void (*rm_rx_frame_cb)(void *user, const uint8_t *au, size_t len, int64_t pts_ns, int32_t key);

typedef struct {
    uint64_t datagrams, delivered, nacks_sent, plis_sent, reasm_drops, slot_drops, metas_sent;
    double loss_pct;          /* congestion inputs, = medialink.UDPStats semantics */
    int64_t owd_ns, owd_jitter_ns, rtt_ns;
    double playout_ms, target_ms, max_ms; /* adaptive playout */
    uint64_t misses, misses10m, repeats, repeats10m, grows, decays, rebases, gap_drops;
    uint64_t pub_count;       /* release (publish) interval histogram */
    double pub_p50_ms, pub_p99_ms, pub_max_ms;
    uint64_t pub_over25, pub_over50;
} rm_rx_stats;

rm_status rm_rx_open(const rm_rx_params *p, rm_rx_frame_cb cb, void *user, rm_receiver **out);
void rm_rx_stats_get(rm_receiver *r, rm_rx_stats *out);
/* rm_rx_send_meta queues one stream-0 meta frame (JSON <= 256 B); RM_ERR_ARGS when full/too large. */
rm_status rm_rx_send_meta(rm_receiver *r, const uint8_t *json, size_t len);
/* rm_rx_close stops the worker and frees; no callback fires after it returns. Closes socket_fd. */
void rm_rx_close(rm_receiver *r);

#ifdef __cplusplus
}
#endif
#endif /* RAVEMEDIA_H */
