# UDP_TRANSPORT.md - §2.5 UDP datagram media profile (P8 transport half)

Realtime, congestion-safe LAN media transport for medialink. Owner ask: fix video send/receive over
LAN/peerlink, drop encryption overhead on the LAN, use realtime UDP to cut latency + be congestion
safe, beat NDI. Branch `feat/udp-transport`. Pure-Go stdlib, zero new deps.

## What shipped

A UDP datagram transport that is a drop-in `frameConn` beside the TCP `*Conn`, negotiated per route
with automatic TCP fallback, plus a three-state LAN encryption default and a loss+delay congestion
controller. Files (all `internal/medialink` unless noted):

- `udpframe.go` - datagram wire, fragmentation/reassembly, per-datagram AEAD, route token, routeToken.
- `udpconn.go` - `udpConn` (frameConn): pacer, fragment NACK/retransmit, deadline drops, keepalive.
- `udpstate.go` - replay/loss window + the retransmit ring.
- `pacer.go` - token-bucket send pacer.
- `congest.go` - delay-gradient + loss AIMD controller + the recv-side governor.
- `udpmux.go` - shared answerer UDP socket + token demux.
- `router_udp.go` - transport negotiation, reachability handshake, TCP fallback, mux serve.
- `enc.go` / `negotiate.go` - enc tri-state (auto/on/off) + resolveEnc + same-subnet test.
- Wiring outside: `peers` (EncMode 3-state + migration), `peerlink` (MediaEncMode), `featurehost`
  (peerSecret Addr/EncMode → child sameSubnet), `app.go`/`feat_media.go` (`UDP: true`),
  `webui/render_peers.go` (transport + UDP telemetry line).

## Wire (deviations from the §2.5 spec, recorded)

Spec: `[8B seq][2B stream][2B frag-idx][2B frag-count][AEAD]`. Shipped 24-byte header adds four
fields; each justified:

| off | sz | field | why (vs spec) |
|---|---|---|---|
| 0 | 1 | Ver<<4\|Type | keepalive/ping/pong/NACK ride the same socket; wire is versioned |
| 1 | 1 | Flags | AEAD bit is self-describing on the wire |
| 2 | 4 | Token | brief req 5: reject off-path/cross-route datagrams at zero crypto cost |
| 6 | 8 | Seq | the spec's datagram seq: AEAD nonce + loss/replay/jitter unit |
| 14 | 2 | Stream | = spec |
| 16 | 4 | FrameSeq | NEW: reassembly + per-frame NACK need the FRAME id; datagram-seq ranges can't carry it robustly under loss/retransmit |
| 20 | 2 | FragIdx | = spec |
| 22 | 2 | FragCount | = spec |

Control datagram types (Type field): media=0, ping=1, pong=2, fragment-NACK=3.

## Decisions

- **D-UDP-1 Fragmentation in user space.** Default 1200 B body (MTU-safe), never IP fragmentation
  (one lost fragment would drop the whole datagram). Reassembly bounded by frames (64) AND bytes
  (32 MiB), drop-oldest + drop-expired-incomplete. FragCount capped (8192) + every wire field
  validated as hostile input.
- **D-UDP-2 AEAD = explicit-nonce per datagram** (SRTP/RFC 3711 shape), NOT the stream Sealer's
  implicit lockstep counter (which only works on an ordered stream). Nonce = per-direction 4 B
  prefix ‖ 8 B datagram seq, unique under a per-connection key. 24 B header is the AAD. Built on
  stdlib `crypto/aes`+`cipher` directly (no churn in shared wirecrypto).
- **D-UDP-3 Route mode is authoritative over the per-datagram AEAD flag** (security fix after
  review): `decodeBody` rejects `(crypto!=nil) != aead-flag` with errDgMode BEFORE the token check,
  closing a downgrade where an attacker who observed the token injects a clear body on an AEAD
  route. Control datagrams are sealed on AEAD routes. A flood of rejected datagrams never updates
  liveness (can't keep a dead route alive) and never grows state.
- **D-UDP-4 Fragment NACK + retransmit, budgeted.** Receiver NACKs missing fragments of stale
  frames (rate-limited per frame); sender re-sends the EXACT datagram (same seq → the peer's replay
  window dedups) from a bounded ring (2048 dg / 4 MiB), budgeted ≤ 25% of the send rate, abandoned
  past the frame deadline. Deadline-dropped frames surface as a frame gap to the router, whose
  existing jitter-buffer PLI handles inter-coded recovery - clean layering, no new PLI path.
- **D-UDP-5 Pacer.** Token-bucket at the live congestion target × a gain, with a small burst, so a
  4K keyframe's hundreds of datagrams don't microburst the NIC/switch. SO_SND/RCVBUF raised
  best-effort.
- **D-UDP-6 Congestion control.** Receiver-side AIMD from datagram loss + a simplified GCC
  delay-gradient over-use detector, bounded by the negotiated [floor, ceiling]. Bitrate first, then
  fps; resolution untouched (VRAM governor's last resort). min()-combined with the VRAM governor so
  both compose (new `MetaRate` field composes - the VRAM agent owns `ratectl.go`). Loss < 5% is left
  to NACK (2% random loss does NOT back off); sub-ms LAN delay jitter ignored. Clean LAN → holds the
  ceiling, no oscillation.
- **D-UDP-7 Negotiation + fallback.** Offer lists `Transports` in preference order (udp,tcp;
  additive/omitempty - old peers read `Transport="tcp"`). Answerer picks UDP only when both support
  it + it can derive the token. Shared UDP socket on the SAME port as TCP (47641-5), demuxed by
  token. Dialer runs a reachability handshake (keepalive ping must draw a reply within 1 s); failure
  → TCP fallback on the same addr (the answerer keeps both the TCP accept and the UDP mux pending;
  whichever the dialer reaches first atomically claims the answer). Mid-route blackout (errUDPDead)
  → renegotiate once on TCP. Old peers stay on TCP, compat-tested both directions.
- **D-UDP-8 Encryption tri-state (auto/on/off), the LAN speed-up.** resolveEnc: `on`/legacy-absent
  forces AEAD; auto/off go cleartext ONLY when both ends confirm the peer is on a directly-connected
  private subnet (the ANDed Offer/Answer `local` flag). Deterministic + symmetric → no key mismatch.
  Cleartext still carries the token (off-path rejection) but gives no confidentiality / no on-path
  integrity (documented in code + `docs/user/multi-pc.md`). Applied to TCP + UDP. Legacy opt-out
  migrates to `off`. `off+off` on a routed path now encrypts (safety tightening of the legacy
  always-clear rule).

## Key schedule + peer binding (security hardening, 2026-11-12)

Two findings on the first cut, both fixed:

**[HIGH] Per-route key schedule (was: AES-GCM key/nonce reuse across routes).** The UDP keys + nonce
prefix (and the TCP keys) were HKDF(MediaSecret, label) only, and each route started its sequence at
0. Two routes to the same peer in one peerlink session (video+audio+webcam), a route restart, a
TCP-fallback-then-UDP, or any renegotiation therefore encrypted different plaintext under the SAME
(key, nonce) - catastrophic for GCM. Fix:

- **Per-ROUTE key salt.** Offer + Answer each carry a fresh 128-bit `RouteNonce` (`rn`, optional).
  `routeKeySalt = offerNonce ‖ answerNonce ‖ session` is the HKDF salt for the keys, the 4 B nonce
  prefix AND the token, so every route instance (and every restart - fresh nonces) derives distinct
  material. BOTH sides contribute 128 bits, so neither alone can force a collision.
- **Sequence is per-key, monotonic, never reset.** One `dgEncoder` per route/key; seq only
  increments (encoder respawn / source swap keep the same udpConn → same monotonic seq; retransmits
  re-send the ORIGINAL datagram bytes, never re-seal a seq). A reset means a NEW route (new nonce →
  new key). A **hard stop** at `dgSeqHardStop = 2^32` datagrams per key makes encode error + control
  stop (→ renegotiate for fresh keys) long before the 64-bit counter can approach wrap, aligned with
  the §2.1 7-day/2^31 rekey budget + the NIST SP 800-38D GCM invocation margin.
- **Token is per-route** (`routeToken(secret, routeKeySalt)`), so cleartext-mode tokens don't repeat
  across routes either.

**TCP-path audit verdict (pre-existing, WAS exploitable).** `transport.go` derived per-direction keys
from `MediaSecret` with `salt=nil` and an implicit counter nonce starting at 0 per `Conn`. Since
`MediaSecret` is per peerlink CONNECTION (shared by every media route to that peer in a session), two
concurrent TCP media routes - or a single route that reconnects - reused one key with a
counter-from-0 nonce on different plaintext: the same catastrophic GCM reuse, pre-existing and
exploitable in any deployment with >1 route or a reconnect. Fix: `NewConn` now takes the per-route
salt; **new↔new TCP routes get per-route keys** (safe). **Old peers can't negotiate it** (they send
no `RouteNonce`): `routeKeySalt` returns nil → `mediaConn` falls back to the LEGACY session-less
derivation AND logs a warning naming the key-reuse risk. Choice + rationale: refusing AEAD with a
pre-fix peer has no safe substitute (cleartext is worse) and would break interop; the realistic
deployment is new↔new (both the owner's updated rave-mate), which is fully fixed; an old peer can't be
made safe without its cooperation. UDP is always new↔new (old peers never negotiate it), so UDP is
always per-route-keyed.

**[MEDIUM] Authenticate before binding/claiming (was: unauthenticated peer binding).** The mux claimed
the pending answer + bound the remote from the FIRST datagram, pre-auth. Fix: `serveUDPFirst` peeks
the pending answer, AUTHENTICATES the datagram with the route's keys/token (AEAD open on encrypted
routes; token + route-mode on clear) and only THEN atomically claims + builds the udpConn. The send
target binds (and later rebinds) ONLY from an authenticated datagram, rate-limited (`udpRebindMin`
1 s). An unauthenticated datagram creates NO state and does not consume the pending answer (which
survives for a legitimate retry); the pending table is populated only by our own `onOffer` (capped by
`maxRoutes`, expired by the pending-answer TTL). An OFF-path attacker without the per-route token
cannot reach `onFirst` at all; an on-path attacker in CLEARTEXT mode can still spoof - the documented
cleartext trade-off. Rejected datagrams never refresh liveness.

**Asymmetric-reachability edge: NOT removed** (honest answer). The answerer still claims on the first
AUTHENTICATED datagram, not after confirmed BIDIRECTIONAL reachability, so the rare case (dialer→
answerer UDP works but the return is blocked) can still claim UDP while the dialer falls back to TCP.
A bidirectional-confirm handshake (claim only after an ack-reach datagram) would remove it; I chose
not to add that protocol complexity for a case that stateful firewalls make rare, and it self-heals
on the next offer cycle via the blackout fallback. Documented residual.

**Wire changes:** `Offer.RouteNonce` + `Answer.RouteNonce` (`rn`, optional/omitempty). Internal
signatures: `NewConn(rw, master, salt, initiator)`, `newDgCrypto(master, salt, initiator)`,
`routeToken(secret, salt)`, `udpConfig.Salt`. `UDPStats.Rebinds` added (authenticated NAT rebinds).

**Security tests:** `TestPerRouteKeyScheduleDiffers` (distinct keys/nonce-prefix/token; route-A
datagram fails to open on route B), `TestRouteKeySaltFreshOnRestart`, `TestDatagramSeqMonotonicAndHardStop`,
`TestDatagramNonceUniqueness` (~90k datagrams, all unique), `TestBindRemoteOnlyOnAuthRateLimited`,
`TestMuxUnknownTokenNoState` (off-path/forged = no state, pending retained), `TestForgedFirstDatagramNoClaim`
(cleartext-on-AEAD + wrong-key forgeries don't claim; real datagram does). Race clean; fuzzers clean.

## Measurements (loopback, AMD 5950X, `-bench BenchmarkTransport -benchmem`)

Per-frame round-trip, 80 KB P-frame / 500 KB keyframe (4K60-class):

- **AEAD is not free at these sizes**: TCP+AEAD ~107 µs vs TCP+clear ~37 µs for 80 KB (~3×). This is
  the measured justification for the enc=auto LAN-cleartext default.
- On **loopback**, TCP beats UDP for bulk (one buffered write vs hundreds of per-datagram syscalls),
  and an UNPACED UDP burst of a 500 KB keyframe overflows the loopback socket buffer → drops → NACK
  recovery - exactly the microburst the pacer prevents on a real NIC. UDP's win is latency-under-loss
  on a real network, not loopback throughput. **Re-take on the 2-PC rig** (loopback is not
  representative).
- Hot-path allocs reduced (retransmit ring shares the encode buffer; clear recv returns a sub-slice).
  Full zero-alloc (pooled datagram buffers) is a follow-up - the ring retains buffers past send, so
  pooling needs deadline-gc return.

## Status vs MEDIALINK_DESIGN §8 P8

Done: UDP+NACK datagram profile (SRTP-style replay), pacing, congestion control, negotiation +
reachability + TCP fallback, enc tri-state. NOT in scope here: per-stream FEC (`KindRepair` still
reserved, D9 open), PTP + follow-master clock tiers, NVENC-direct zero-copy.

## Unverified (needs the 2-PC rig - say so, don't claim)

- Real cross-PC UDP vs TCP latency/throughput + the "beat NDI" bandwidth claim (loopback numbers are
  not representative).
- Firewall fallback on a real blocked-UDP network (tested in-process via a dead peer).
- enc=auto cleartext actually engaging across two real PCs on one subnet (needs PeerAddr from the
  live peerlink; in-worktree the loopback route has no PeerAddr → stays AEAD).
- Congestion convergence under a real bandwidth cap (controller unit-tested; udpConn loss-recovery
  tested; full under-impairment route integration is a follow-up).
- Installer/firewall: an **inbound UDP** allow rule on ports 47641-47645 is needed (same ports as
  the existing TCP media rule). See the lead follow-ups.

## Live tuning from the first two-PC run (2026-11-12)

First live run: sus (Linux, hevc_vaapi) → Windows (mf-native-decode), 1080p60, wired GbE, route up
`udp/plaintext`, picture pixel-correct. `ctl routes` at ~40 s showed the issues below. Fixes land
with a failing-first test each; they could not be re-measured live by me (same build on both PCs) -
the lead must re-measure after deploy.

- **Item 1 - 279 spurious NACKs at 0% loss.** Root cause: `scanNack` NACKed any in-flight frame >3 ms
  old with a hole - i.e. the frame CURRENTLY arriving (bursted/paced over a few ms, or briefly
  reordered within itself). Fix: a hole is only NACKed once PROVEN missing - a later frame on the
  stream has started arriving (`reassembler.maxFrameSeq`; the sender transmits a frame fully before
  the next, so an OVERTAKEN incomplete frame really lost fragments) or the frame is within
  `udpNackDeadlineLead` of its reassembly deadline (tail-of-stream recovery). The newest in-flight
  frame is never NACKed. Why the sender "retransmitted nothing": the NACK'd fragments were in flight,
  not lost, so they arrived anyway; the sender (unpaced, no budget block) would have retransmitted
  from the ring, producing DUPLICATE datagrams the receiver's replay window discarded (DupDgram, not
  shown on that ctl line) - the fix removes both the spurious NACK and the wasteful dup-retransmit.
  Tests: `TestScanNackOnlyProvenHoles`, `TestScanNackDeadlineLastChance`,
  `TestUDPConnNoSpuriousNacksOnReorder` (0% loss + reorder → ~0 NACKs).
- **Item 2 - jitter buffer at 3-4 frames (50-67 ms) on a clean LAN, lateRate 0.** Root cause: STARTUP
  transients (encoder/pipeline fill, first IDR, clock-base settling) made early frames transiently
  late → grew the depth to 4 in the first ~12 s, then the §3.3 30 s/step decay left it stuck at 3-4.
  Fix: a `jbWarmupNs` (2 s) no-grow window kills the startup grow, and `jbDecayCleanNs` dropped 30 s →
  8 s so any genuine transient grow recovers within seconds (decay still only fires when late-rate <
  0.1%, so no oscillation). Pacer half: the SENDER now paces at `pacingGain`×budget (not the bare
  target, which would stretch a frame over several intervals) with a floor, so each frame finishes
  well inside one frame interval on GbE while a 4K-keyframe microburst is smoothed. Tests:
  `TestJitterBufferWarmupNoGrow`, `TestPacerFrameWithinInterval`.
- **Item 3 - 50 Mbps on a flat-colour source.** The negotiated budget flows correctly
  (Offer.Bitrate = config `MediaLink.Bitrate()` → `EncodeSpec.BitrateKbps`, respected). The 50 Mbps
  is the configured budget being CBR-PADDED: the VA-API encoder used `-rc_mode CBR`, which fills
  static content to the full bitrate. Fix (minimal, in the Linux agent's `internal/mediapipe/args.go`
  vaapi case): `-rc_mode CBR` → `VBR` (constrained VBR - the budget becomes a peak CEILING, flat
  content undershoots it). NOT verifiable by me (can't deploy to sus); LINUX AGENT to confirm the
  encode still opens on radeonsi/iHD and that a flat source now emits well under budget (fall back to CBR or
  try QVBR if a driver rejects VBR). Test updated: `TestEncodeArgsVAAPI` pins `-rc_mode VBR`.
- **Item 4 - owd shown -39 ms.** `owdNs` was the raw signed transit (arrival−PTS), which carries the
  peers' clock offset. Fix: `owdNs` is now QUEUING delay above a windowed-min transit baseline
  (offset-free, ≥0); the delay-gradient congestion detector uses its trend, which a constant offset
  cancels (confirmed - it never depended on absolute clock agreement). Peers panel relabelled `owdq`.
  Test: `TestOwdQueuingDelayOffsetFree`.
- **Item 5 - daemon CPU 68.7%.** NOT a UDP-transport bug and NOT media bytes transiting the daemon.
  Evidence: a heap profile during a route shows only normal DB/library/i18n allocations (no frame
  buffers in the daemon); the goroutine snapshot shows all per-child stdio readers BLOCKED in
  ReadFile (not spinning); the Windows Go CPU profiler is unreliable here and mis-counted those 8-9
  blocked read threads as "99.8% cgocall under os.File.Read" (318% total samples). The UDP router +
  mux + udpConn + decode all run in the featurehost CHILD; only 1 Hz telemetry, 1 Hz clock-offset,
  ~4 Hz TC and low-rate bus events cross to the daemon (media child logs are ≤1/10 s). Recommend the
  lead: confirm WHICH rave-mate.exe is at 68.7% (daemon vs the media child doing legitimate
  recv/reassembly/decode-feed vs the shell), and re-profile on Linux (sus) for a reliable flame graph.
- **Item 6** (not my area, noted): did not inspect the encoder-scan VRAM-budget `self=?` line.

### Expected numbers for the lead to re-measure after deploy
- jitterbuf depth ≤ 2 frames steady on the clean LAN (ideally 1), after the ~2 s warmup; decays
  within ~8 s if it ever grows.
- `nack(s/r)` ≈ 0 at 0% loss (was ~4.5/s); `DupDgram` on the receiver ≈ 0.
- owd (now `owdq`) ≥ 0, sub-ms-to-few-ms queuing (never the clock offset).
- bitrate ≤ the negotiated budget, and well under budget on a flat/static source (VBR), once the Linux agent
  confirms the VA-API VBR change.
- daemon CPU < 5% during a route IF the 68.7% was the daemon (confirm the process first; the UDP
  transport added no daemon-side per-frame work).
