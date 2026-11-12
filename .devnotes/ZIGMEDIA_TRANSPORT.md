# zigmedia — native realtime media TRANSPORT (Zig)

Owner directive 2026-11-12 (CLAUDE.md "Realtime paths are native"): the per-packet/per-frame transport
leaves Go. Measured trigger (Linux agent, 2026-11-12): the capture+VA-encode engine hits a flawless
60.00 fps as a second read-only consumer, but the INSTALLED route shows `dropped=30`/`out=59` — the
drop is the encoder→Go hand-off (single-slot AU overwritten whenever the Go packetize/pace/send side
stalls past one frame period), identical on UDP and TCP. So the SENDER native path is first.

This is the TRANSPORT port (framing/pacing/socket/NACK/crypto), complementary to ZIGMEDIA_DESIGN.md
(the pixel/encode path). Go keeps control only: negotiation, key agreement + per-route salt, route
lifecycle, rate hints, 1 Hz stats, ctl.

## Wire format — source of truth = Go

Byte-exact with `internal/medialink`. Goldens are generated from Go
(`go test ./internal/medialink -run TestZigGoldenVectors -v`) and asserted in Zig
(`native/zigmedia/src/goldens_test.zig`). Summary (big-endian):

- **UDP datagram header (24 B, clear, = AEAD AAD)** — udpframe.go:
  `[Ver<<4|Type(1)][Flags(1)][Token(4)][Seq(8)][Stream(2)][FrameSeq(4)][FragIdx(2)][FragCount(2)]`.
  Types: 0 media, 1 ping, 2 pong, 3 nack, 4 bye. Flag bit0 = AEAD body.
- **Inner Frame header (26 B) + payload** — frame.go:
  `[Stream(2)][Kind(1)][Codec(1)][Flags(1)][TCRate(1)][Seq(4)][TCFrames(4)][PTS(8,i64 as u64)][PayloadLen(4)]`.
  Fragmented at `body_max` (default 1200, MTU-safe); a frame = N datagrams.
- **Per-route key schedule** — HKDF-SHA256 over salt = `offerNonce(16)‖answerNonce(16)‖session`:
  token = `HKDF(secret, salt, "medialink-udp-token", 4)` big-endian u32; direction keys =
  `HKDF(master, salt, "medialink-udp c2s/s2c v1", 36)` → 32 B key + 4 B nonce prefix. Initiator seals
  c2s/opens s2c; responder mirrors. AES-256-GCM, nonce = `prefix(4)‖seq(8)`, tag appended (SRTP shape).
- **TCP path** — transport.go: `[len(4)][sealed-or-plain frame]`, AES-256-GCM with the *implicit*
  lockstep counter nonce (wirecrypto.Sealer, labels "medialink c2s/s2c v1"). The native TCP sender must
  replicate the counter-nonce construction (different labels + implicit nonce) — distinct from UDP.
- **NACK body**: `[count(2)][count × (Stream(2),FrameSeq(4),FragIdx(2))]`, max 85. Replay window,
  start-up grace, keepalive, bye: all per the Go `udpConn` (udpconn.go) semantics, already on dev.

## Status (this commit)

- **Wire core (Zig) — DONE, byte-exact.** `src/keys.zig` (HKDF token + per-route schedule + nonce),
  `src/aead.zig` (AES-256-GCM seal/open), `src/wire.zig` (24 B header encode/decode + hostile-input
  rejection, 26 B frame marshal, fragment count). 7 Zig tests incl. goldens vs Go pass
  (`cd native/zigmedia && zig build test`). `rm_wire_selftest()` exposes it to C.
- **SENDER C ABI — STABLE, published.** `include/ravemedia.h` (ABI v1): `rm_tx_open` (route params +
  derived keys/token + peer addr or handed socket fd + udp|tcp) / `rm_tx_submit_au` (non-blocking,
  bounded queue, drop→IDR callback) / `rm_tx_set_rate` / `rm_tx_stats_get` (lock-free block) /
  `rm_tx_close` (bye + stop). Compiles under gcc; the lib links. **The Linux agent can integrate the
  shim's encode thread against this header now.**

## Next increments

- **2b — sender runtime (the owner's pain).** Implement `rm_tx_*`: a dedicated TX thread (elevated
  priority), the bounded SPSC AU queue (drop-oldest-non-keyframe → `on_need_idr`), packetize→pacer
  (deadline-based)→`sendmmsg`/batched send, the retransmit ring + UDP control-RX thread
  (NACK/ping/keepalive/bye), and the TCP length-prefixed framing. All allocation-free in steady state,
  fixed pools sized at open. Replaces the single-slot AU hand-off that drops today.
- **3 — Windows receiver** (socket→reassembly→jitter→decoder inside the native child).
- **4 — Windows sender** (MF encode child TX). **5 — soak** with the STEP-0 histograms as acceptance
  (no receive-stage interval > 2 frame periods; zero steady-state allocations).

## Selection / fallback (STEP 3)

Native transport is used when the local build has it AND the route's engines are native; else the Go
path (unchanged, wire-compatible, so native↔Go interoperate). Route telemetry says which
(`transport-impl: native|go`); config escape hatch forces Go.
