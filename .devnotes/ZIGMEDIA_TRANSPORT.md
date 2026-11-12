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

## ABI v2 (2026-11-12) + Go-receiver cgo interop DONE

`rm_open_params` grew `struct_size` (layout-skew guard), `initial_seq`, `fps`, `startup_grace_ns`,
`RM_ABI_VERSION` → 2. **Why:** the native sender started at datagram seq 0; a real `udpConn` whose
replay window was already advanced by the Go handshake rejected those datagrams as too-old and never
rebound → no media. `initial_seq` = `udpConn.sendSeq()` continues the seq space so the first native
datagram is strictly-newest → the receiver rebinds its remote to the native socket. A v1 caller
(abi_version=1) or any struct-size skew is rejected cleanly (RM_ERR_ARGS) before any field use.

**Go-receiver cgo interop (item B) — DONE.** `internal/medialink/nativesender_{cgo,interop}_linux_*.go`
(`//go:build linux && zignative`) links `libravemedia.a` into a Go test: native sender → a REAL
`udpConn` over loopback. Proven (Linux build container, Go 1.26.6 + gcc + CGO): continuing InitialSeq →
receiver rebinds + media flows + a dropped fragment recovered via NACK→retransmit + `rm_tx_close` bye
ends the route with errUDPEnded; and initial_seq=0 vs an advanced window is REJECTED (no rebind, no
media) while initial_seq=highSeq+1 is accepted over the same window. Build + run:
`cd native/zigmedia && zig build -Dtarget=x86_64-linux-gnu --release=fast`, then
`go test -tags zignative -run NativeSender ./internal/medialink/` in the container.
**The Go RECEIVER needs NO change for a native sender** — it is still the Go `udpConn` until the native
Windows receiver (increment 3) lands; the native sender is wire-compatible with it.

**Linux agent's call site (branch feat/pw-4k, commit ecb0003):** its `rm_open_params` construction must
add, to clear the v1→v2 skew rejection: `struct_size = sizeof(rm_open_params)`, `initial_seq =
NativeTXParams.InitialSeq` (`udpConn.sendSeq()`), `fps = NativeTXParams.FPS`, `startup_grace_ns =
int64(NativeTXParams.StartupGrace)` (0 → native 15s default), and bump its `abi_version` to 2 /
`RM_ABI_VERSION`. (The call site is NOT on feat/udp-transport, so it is not edited here.)

## Next increments

- **2b-cont — TCP path** (length-prefixed, implicit-counter GCM, wirecrypto.Sealer labels) behind the
  same ABI (`rm_tx_open` transport=TCP currently returns RM_ERR_UNIMPL), and
  the **4K60 10-minute soak** with the STEP-0 stage histograms on the Go receiver (acceptance: no
  receive-stage interval > 2 frame periods; zero steady-state allocations — assert via the histograms +
  a failing-allocator guard). Elevated-priority tuning (MMCSS/SCHED_FIFO) pass.
- **3 — Windows receiver (STARTED): PTS-locked playout core done + verified.** Live STEP-0
  (2026-11-12, TCP, 4K60) showed GC is NOT the cause; frames leave the sender evenly (tx max 19.5 ms)
  but RELEASE unevenly at the receiver (jb tail: max 43-121 ms, dozens > 25 ms) - the Go path releases
  on arrival / a coarse Windows timer (time.Timer ~15.6 ms). The native receiver fixes it:
  - `playout.zig` — PTS-locked scheduler. Release deadline = `pts + base + margin` on a monotonic clock,
    where base = windowed-MIN transit (cancels the clock-epoch offset AND ignores the cold-start
    straggler, so the epoch settles to steady state, not the first frame; slow-tracks drift) and margin
    = windowed-MAX transit SPREAD + headroom (the delay is sized from the MEASURED arrival-jitter, not a
    frame count: a 42 Mbps burstier stream gets a bigger margin than the even bar; clamped [1 frame,
    cap]). Release intervals then equal the source cadence regardless of arrival bunching. Allocation-
    free, monotonic clock injected. Tests: PTS-locked spacing under bunched arrivals, base settles past
    a 500 ms cold-start straggler, margin sizes to the spread, gap handling.
  - `hrtimer.zig` — high-resolution wait: Linux `clock_nanosleep(ABSTIME, CLOCK_MONOTONIC)`, verified
    sub-2 ms in WSL (vs the Go ~15 ms coarse timer). Windows `CreateWaitableTimerEx(HIGH_RESOLUTION)`
    lands with the Windows socket layer.
  - `reassembler.zig` — receiver reassembly (Zig port of the Go reassembler): fixed entry pool, drop-
    oldest, bounded done-set (no straggler resurrection); fragment i at offset i*body_max (contiguous).
    Allocation-free. Tests: single/multi-fragment, out-of-order, duplicate, drop-oldest.
  - `histogram.zig` — lock-free inter-event interval histogram for the in-child PUBLISH metric
    (acceptance: publish p99 ≤ 18 ms, zero > 25 ms on the bar over 10 min), 1 ms buckets around a frame.
  - Remaining: the Windows platform wiring — Winsock RX (first-byte timestamps from the socket layer)
    → reassembler → playout → D3D11 decode → Spout publish in the native child, with the publish
    histogram on the publish thread; CreateWaitableTimerEx(HIGH_RESOLUTION) for the wait.
  - Sender start-up note (confirmed from sus `enc max=502ms dropped=36`): the cold capture/encoder open
    emits a ~500 ms first-second gap + a burst; the receiver's `base = min transit` already ignores it
    (the playout epoch settles to steady state), so no extra start-up handling is needed on the receiver.
- **4 — Windows sender** (MF encode child TX; Windows socket layer).

## Selection / fallback (STEP 3)

Native transport is used when the local build has it AND the route's engines are native; else the Go
path (unchanged, wire-compatible, so native↔Go interoperate). Route telemetry says which
(`transport-impl: native|go`); config escape hatch forces Go.
