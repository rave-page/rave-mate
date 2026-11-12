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

## Status — increment 2b (UDP sender runtime) DONE, Linux, Zig-interop verified

`rm_tx_*` implemented for UDP (sender.zig + auqueue.zig + retxring.zig + pacer.zig). ONE worker thread
(SCHED_RR best-effort) owns the socket, retransmit ring, pacer and seq counters; the encode thread only
calls `submit()`. **Allocation-free in steady state** — all pools (AU queue 6×4 MiB, retx ring
2048×1500 B, frame/dg/rx scratch) are sized at open; submit/send/worker never allocate. The bounded AU
queue drops the oldest NON-keyframe and fires `on_need_idr` — this replaces the single-slot hand-off
that dropped (the owner's finding). Worker loop: drain control (NACK→retransmit from the ring,
ping→pong, bye/pong→liveness), send paced media (packetize→fragment→seal→send→ring), keepalive (100 ms),
start-up-grace liveness.

Verified on Linux (built with Windows zig `-Dtarget=x86_64-linux-gnu`, run in WSL Debian) — 16 Zig
tests pass incl. native-sender→receiver loopback (the receiver uses the goldens-pinned wire core, so it
is equivalent to the Go receiver): AEAD media decodes IN ORDER, large AU fragments + reassembles, CLEAR
(plaintext) media, receiver-NACK→sender-retransmit. Windows host build runs the 12 non-socket tests
(interop is Linux-only). Socket I/O uses `std.os.linux` raw syscalls (this dev-Zig moved it out of
`std.posix`); Windows sockets are increment 4. ABI unchanged (no version bump).

## Next increments

- **2b-cont — TCP path** (length-prefixed, implicit-counter GCM, wirecrypto.Sealer labels) behind the
  same ABI (`rm_tx_open` transport=TCP currently returns RM_ERR_UNIMPL). **Go-receiver cgo interop**
  (link `libravemedia.a` into a Go test, native sender → a real `udpConn`) + the impairment shim, and
  the **4K60 10-minute soak** with the STEP-0 stage histograms on the Go receiver (acceptance: no
  receive-stage interval > 2 frame periods; zero steady-state allocations — assert via the histograms +
  a failing-allocator guard). Elevated-priority tuning (MMCSS/SCHED_FIFO) pass.
- **3 — Windows receiver (STARTED): PTS-locked playout core done + verified.** Live STEP-0
  (2026-11-12, TCP, 4K60) showed GC is NOT the cause; frames leave the sender evenly (tx max 19.5 ms)
  but RELEASE unevenly at the receiver (jb tail: max 43-121 ms, dozens > 25 ms) - the Go path releases
  on arrival / a coarse Windows timer (time.Timer ~15.6 ms). The native receiver fixes it:
  - `playout.zig` — PTS-locked scheduler: frame N releases at `t0_mono + (pts_N - pts0) + depth·interval`
    on a monotonic clock, so release intervals equal the source cadence regardless of arrival bunching.
    Bounded seq-ordered reorder buffer, wrap-aware; gap → hold-to-deadline then skip; adaptive depth
    (late-rate, target ≤ 2). Allocation-free, clock injected. Tests prove PTS-locked spacing under
    bunched arrivals, gap skip, and depth adaptation.
  - `hrtimer.zig` — high-resolution wait: Linux `clock_nanosleep(ABSTIME, CLOCK_MONOTONIC)`, verified
    sub-2 ms in WSL (vs the Go ~15 ms coarse timer). Windows `CreateWaitableTimerEx(HIGH_RESOLUTION)`
    lands with the Windows socket layer.
  - Remaining: Windows socket RX (Winsock in Zig) → reuse the wire core's reassembly → playout →
    D3D11 decode → Spout publish in the native child, plus a TRUE `publish` interval histogram INSIDE
    the child (acceptance: publish p99 ≤ 18 ms, zero > 25 ms on the bar source over 10 min).
- **4 — Windows sender** (MF encode child TX; Windows socket layer).

## Selection / fallback (STEP 3)

Native transport is used when the local build has it AND the route's engines are native; else the Go
path (unchanged, wire-compatible, so native↔Go interoperate). Route telemetry says which
(`transport-impl: native|go`); config escape hatch forces Go.
