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

### Security + retransmit-amplification fixes (same unit, after review + more live data)

- **[SEC] Replay could rebind the remote / refresh liveness.** The receive path ran `bindRemote(src)`
  + liveness refresh + reach signal AFTER authentication but BEFORE the replay/sequence check, so a
  captured authenticated datagram replayed from another address could redirect the send target
  (hijack/DoS) - on AEAD routes too (AEAD does not prevent replay). Fix: the replay check runs FIRST;
  a duplicate/too-old datagram changes nothing (no rebind, no liveness, no reach, not even delivered),
  and ONLY a STRICTLY-NEWEST datagram (`stNew` - a replay can't forge one) may rebind (still
  rate-limited) / refresh liveness / signal reach. A gap-filling retransmit (`stFill`, new but below
  the highest) is delivered for recovery but grants none of that leverage. Cleartext on-path forgery
  stays the documented trade-off, but pure replay now works in neither mode. Test:
  `TestReplayDoesNotRebindOrRefreshLiveness` (replay from a new addr: no rebind, no liveness; a newer
  datagram from a new addr: rebind, rate-limited).

- **Retransmit amplification (live: ~72 retransmit datagrams per NACK at 0% loss; six-figure retrans
  on a 4K source stall).** Two causes, both fixed: (1) the spurious NACKs (item 1) listed a whole
  in-flight frame's not-yet-arrived fragments (~72 items) → the sender resent them all; the scanNack
  fix stops NACKing in-flight frames. (2) the retransmit BUDGET (≤25% of send rate) was NOT enforced
  when the sender was UNPACED (`retr` rate = PacingRate×0.25 = 0 → tryReserve always allowed); the
  sender-pacing change (item 2) sets a real PacingRate, so the budget now caps retransmit bursts.
  onNack already resends exactly the NACK'd (stream,frameSeq,fragIdx) datagrams one-for-one (never
  whole frames/ranges). Stalled source: with no newer frame, scanNack no longer loops - it emits one
  last-chance NACK near the deadline (rate-limited), then the sweep drops the frame. Tests:
  `TestRetransmitBudgetEnforced`, `TestScanNackStalledSourceNoLoop`.
  Expected post-deploy: sender `retrans` per NACK ≈ 1 (not ~72); retrans bounded by the 25% budget;
  no retransmit storm on a stalled encode.

Operational note: during live diagnosis a `ctl route-stop <peer> all` also stopped a separate user
"Spout2PW (VRChat)" route (already starving, fps 2/0). Use a specific session id, not `all`.

## Post-deploy round 2 (2026-11-12, both PCs on d6dc2fb): duplicate routes + stop-all

Live logs (real 4K VRChat route sus↔this PC) nailed the sequence behind the three route-lifecycle
findings - a single captured timeline:

```
recv up  session=4c7f… stream=2 (udp, congestion governor runs)
recv end session=4c7f… "udp route blackout" (BOTH ends' liveness fired ~3s apart)
recv up  session=2f03… stream=3 (TCP) ← the RouteManager blackout RE-OFFER (untracked: rm.OfferRoute, NOT StartReceive)
recv up  session=b21e… stream=4 (udp) ← mediaroute auto-resume re-offer ~30s later (tracked)
→ stream 3 (tcp) + stream 4 (udp) BOTH LIVE for one source = the duplicate
```

- **Duplicate route for one (peer, source) [fixed].** Two independent re-offers raced: the blackout
  re-offer (untracked TCP) + the auto-resume reconcile re-offer (tracked UDP). Root fix: the
  RouteManager now enforces ONE recv route per (peer, sourceID) and REPLACES atomically -
  `dropDuplicateRecv` closes any other live recv route for the same (peer, source) when a new one
  comes up (tear down the loser; its decoder + Spout sink follow the conn close). Never two live
  routes / two 4K decodes into one sink. Test: `TestDropDuplicateRecv`. ALSO removed the RouteManager
  blackout RE-OFFER (it created the untracked route): on a UDP blackout the route just ends; the
  RECEIVE layer re-establishes via StartReceive (tracked), and dialConn's reachability probe falls
  back to TCP if UDP is still blocked - so the TCP escape is kept, without an untracked route.
- **`route-stop … all` stopped only 1 of 2 [fixed by the above].** It iterates `m.receives`
  (StartReceive bookkeeping); the blackout re-offer's TCP route was created via `rm.OfferRoute`
  directly, so it was never in `m.receives` and was unmatched. Removing the untracked re-offer means
  every receiver route goes through StartReceive → `m.receives` → stop-all sees it; the atomic replace
  means there is only ever one to stop. (`route-stop` itself: `all` already matches every receive for
  the peer; the bug was the invisible route, not the matcher.)
- **First route came up TCP / the UDP route blacks out ~4.5s in [root-caused; cascade fixed, true
  reverse-path cause left for the lead].** It is not a skipped handshake and not the per-route-key /
  authenticate-before-claim change: both ends derive keys+token from the two route nonces + session
  BEFORE the first datagram (verified), the answerer registers the mux-expect before publishing the
  Answer, and the logs show the UDP route coming UP (handshake succeeded) then blacking out. Both the
  receiver AND the sender logged "udp route blackout (no datagrams within liveness window)" ~3s apart:
  the REVERSE control path (receiver→sus keepalives/reports) stopped refreshing sus's liveness, so sus
  stopped sending and the receiver then timed out. Most likely cause: the Windows firewall on the
  freshly-replaced exe blocking the dialer's EPHEMERAL UDP socket (the recv-route dialer binds a
  random port via net.ListenUDP, which the 47641-5 firewall rule does not cover), so the reverse
  datagrams never reach sus. The blackout re-offer then turned this transient into a stuck TCP route +
  a duplicate - both now fixed. LEAD TO CHECK after deploy: whether UDP stays up on a normal route;
  if it still blacks out, (a) confirm the inbound-UDP firewall rule covers the dialer path, and
  consider binding the recv-route dialer to the well-known media port range (or sharing the answerer's
  UDP socket) so one firewall rule covers both directions; (b) watch sus's udpConn liveness vs the
  receiver's keepalive arrival.
- **Sender `fps out=0` on the native engine [not my area - reported].** `out` = PipelineStats.OutFPS
  ("frames leaving the encoder child"), populated by the MF-native bridge (mf_bridge.go) but NOT by
  the Linux `h264_vaapi_native` send engine (the Linux agent's new engine). Frames clearly flow
  (receiver out=58-60); it is a send-side telemetry gap in the native VA-API engine's PipeStats.

`ctl logs` streams/follows (SubscribeLogs; stops only on client disconnect) - intended, but a
non-following `ctl logs [-n N]` ring-buffer dump would help; run it under a hard `timeout` meanwhile.

## Unit: UDP blackout root cause (2026-11-12) - hypothesis refuted; it is a sender-side end, not liveness starvation

Brief: find the REAL cause of the UDP blackout ~4.5 s after route-up. Lead hypothesis to verify or
refute: the tuning+replay build removed the spurious reverse NACKs that used to refresh the sender's
liveness, and the replay fix lets only a strictly-newest datagram refresh it, so a reverse direction
carrying only sparse keepalives starves the sender. Required: a one-directional 0-loss test on the
real udpConn pair for >=3x the liveness window that should FAIL on d6dc2fb.

**The hypothesis is refuted, three independent ways:**
1. `git diff d6dc2fb..HEAD -- udpconn.go udpstate.go udpframe.go` is EMPTY. 6cbfe5b (the blackout-era
   fix) touched only router.go + a test. The liveness/keepalive code is byte-identical to the build
   that blacked out, so any test result on HEAD holds for d6dc2fb.
2. `TestUDPConnOneDirectionalStaysUp` (one-way media, 0 loss, no NACKs, liveness 1 s / keepalive
   200 ms, run 4 s = 4x the window) PASSES. `maintLoop` runs on BOTH ends and pings every keepalive
   interval; a pure receiver's pings refresh the sender's liveness. It did NOT fail as predicted -
   kept as a regression proof that the one-directional liveness path is sound.
3. `SetPacingRate` is DEAD CODE (no caller anywhere). The pacer rate is fixed at
   `pacingRateFor(offer.Bitrate)`, floored at `pacerMinRateBytes` = 32 MB/s (~50 MB/s at the
   production 50 Mbps). A keepalive ping's `pacer.reserve()` is ~1 us and a full keyframe paces in
   ~10 ms, so the pacer CANNOT starve the liveness heartbeat for 2 s. (It will become starvable if the
   congestion controller is ever wired to `SetPacingRate` without a floor - see the EOS fix note.)

**Real cause (sender-side).** Transport-wise, both ends heartbeat every 500 ms and neither can be
starved for the 2 s liveness window, so a receiver blackout is equivalent to "the sender stopped
emitting ALL datagrams (media AND keepalives)". That happens only when the sender's `runSend` RETURNS
(router.go), which closes the conn and stops `maintLoop`. Both capture sources (`pwcapture.pwSource`
raw and native-encode `nextAU`) BLOCK on a transient stall (`rpw_wait_*` returns 0 -> continue), so a
mere 0 fps stall does NOT end the route; they return `io.EOF` only on a FATAL stream error (-1) or
Close. The prime trigger is the new pw-4k native VA-API encode path (ed385de/8207f6e) hitting a fatal
`-1` ~4.5 s in -> `nextAU` returns `io.EOF` -> `runSend` returns nil (graceful) -> conn closed with
NO notice to the receiver -> the receiver sees silence and reports `errUDPDead` (liveness blackout)
~2 s later. This matches the live timeline (receiver first, sus stopped emitting first) and the
~4.5 s-after-up timing of a crashing encoder. The native-encoder robustness is the Linux agent's
area; flagged for that owner.

**Live forensics (both PCs on d6dc2fb; owner's tcp route untouched).** The funnel-producer on sus
frequently sits at `render 0.0 fps` (observed: `udptune` stuck at 0 fps for 364 samples under a
`runuser`/`setsid` launch with no GPU/EGL context; `rm1080` spent its first ~20 s at 0 fps before
ramping to 60). sus's real daemon (pid 128747) logs only via its own ctl (not journald) - which is
exactly why a non-following `ctl logs -n N` was needed and is now added. NOTE: my `runuser -l
dymattic` login triggered a benign `rave-mate.service` restart loop on sus (a lingering user manager
retries the enabled unit every ~5 s; each spawn exits immediately on the single-instance lock, binding
nothing - it does not affect pid 128747 or the owner's route). Clears on a clean boot, or
`systemctl --user disable --now rave-mate.service` in that login.

**Transport fix shipped (attributability).** On UDP there is no FIN, so a receiver cannot tell a clean
sender-end from a dropped network path without an explicit signal. Added `dgTypeBye` (clean
end-of-stream): the sender calls `udpConn.sendBye()` just before `endRoute` closes a send route (source
EOF / stop), a SYNCHRONOUS direct-to-socket write that bypasses the pacer/sendLoop (which may be torn
down). The receiver ends promptly with `errUDPEnded` ("remote source ended") instead of waiting out
the liveness window and logging a (misleading) blackout. Security/compat: the bye is authenticated
like every control type (decodeBody token+AEAD gate) and honored ONLY on a strictly-newest (stNew)
datagram, so a wrong-token, replayed, or reordered bye can never tear down a live/re-established route;
the datagram-type switch has no default, so OLD peers ignore `dgTypeBye` and keep the old 2 s-blackout
behaviour (no regression on a mixed-version pair). A stalled/crashed sender that cannot send a bye
still surfaces as `errUDPDead` as before. Tests: `TestUDPConnByeEndsCleanlyNotBlackout` (end-to-end,
prompt EOS not a blackout), `TestByeAuthenticatedAndReplaySafe` (wrong-token rejected, valid honored,
stale-seq dropped).

**Small items shipped.**
- `ctl logs -n N` - non-following ring-buffer dump (`LOGS-N <n>` command -> `Control.LogTail`;
  `app.TailLogs`; `-n N`/`-nN` parsed in the CLI). The follow form is unchanged.
- Route line shows WHY a route is on TCP when UDP was possible: `RouteStat.TCPReason`
  (recv: "udp disabled locally" / "peer selected tcp (no udp support)" / "no per-route key (legacy
  peer)" / "udp unreachable (reachability handshake failed)"; send: adds "peer did not offer udp" /
  "udp unreachable (peer fell back to tcp)" / "negotiated tcp"). Shown as `[tcp: <reason>]` in the
  Peers panel and `(<reason>)` in `ctl routes`.

**Correction to the previous unit's note.** The earlier firewall hypothesis for the blackout is NOT
supported: the lead's evidence shows inbound ALLOW rules exist for both exes (TCP+UDP, all local
ports), and the three refutations above plus the sender-side `runSend`/source analysis point at a
clean/fatal sender-side source end, not a reverse-path block.

## Unit: UDP blackout = COLD-START RACE (2026-11-12, deployed 99e3d48, ctl logs -n gave the trace)

The blackout is neither a faulting encoder nor reverse-path starvation: it is a start-up race, proven
by the first route after both daemons restarted (clocks within a few ms):
- receiver 13:03:04.125 "receive offered" -> 04.840 "route up (recv)" -> 06.140 first frame AND
  "udp route blackout - route ended" in the SAME ms.
- sender 05.759 "vaapi-native encode engaged + route up (send)" (~0.9s after recv route-up: cold
  PipeWire capture + VA encoder open) -> emits its first frame at 06.140.
The receiver's liveness window started at dial (~04.1) and the 2s expired at ~06.1 - the instant the
cold sender's first frame landed. The 35s-later retry found a WARM sender and has stayed up on UDP
since (udp/plaintext, 60fps, loss 0, nack 0/0, jitterbuf depth 1 - the tuning numbers confirmed live).

**Fix 1 - liveness start-up grace (udpconn.go).** New `udpConfig.StartupGrace` (default
`udpDefStartupGrace` 15s): the liveness window is the generous grace UNTIL the route is proven
established end-to-end, then the normal 2s. "Established" (`udpConn.established`) is set on the first
authenticated MEDIA or peer keepalive-PING (not a one-shot reachability pong). A cold sender's
encoder-open and a cold receiver's DXVA-decoder-open (VRAM pressure) no longer look like a dead path;
once real traffic flows, dead-route detection is back to the tight 2s. The answerer now emits
keepalives densely (`Keepalive: 100ms`) from udpConn birth in serveUDPFirst - before pumpSource opens
the encoder - so a cold receiver sees the sender's liveness immediately. Mirror case (sender waiting on
a slow receiver) is structurally safe: the receiver's maintLoop pings from udpConn creation, a
separate goroutine from dialAndReceive's decoder open, and the grace covers it until establishment.
Tests: TestUDPConnColdStartGraceStaysUp (3s cold-start delay, keepalives off to isolate the grace -
stays up), TestUDPConnEstablishedThenSilentBlacksOutFast (tight window applies after establishment).

**Fix 2 - fast re-establish (mediaroute.go + mediaresume.go).** The ~33s recovery (06.140 -> 39.376)
was a DEAD session lingering in `m.receives` for `receiveGrace` 30s: both reconcile's active-check and
StartReceive's dedup keyed off mere membership, so the dead route looked "still receiving" and was
never re-offered until GC. Now: `Receives()` flags each receive with `Active` (true route liveness
from router.Stats()); reconcile re-offers any remembered source whose route is NOT live; StartReceive
reclaims a dead (peer,source) entry (CloseRoute+UnregisterSink) instead of returning the dead session;
the resume ticker is 1s (was 3s) with bounded exponential backoff per key (base 1s, x2, cap 30s),
reset once a route stays up >= 5s - immediate recovery after a blip, no retry storm when a source
stays gone. Recovery now ~1-2s. Tests: TestReconcileReoffersDeadSourceImmediately,
TestReconcileReoffersWhenReceiveLingersButDead, TestReconcileWaitsForAvailability,
TestReoffBackoffBoundedAndResets.

**Fix 4 - remembered-receive persistence (verified, already correct).** `ctl route-start` -> RouteStart
-> appControl.mediaRoutes which IS the `rememberingReceives` wrapper (app.go:1057 reassigns
`mediaRoutesCtl = rr`): StartReceive persists (peer,source) to `config.RememberedReceives` + cfg.Save().
On restart the daemon loads the config and runReceiveResume.reconcile re-offers at startup. The earlier
"no active routes until manual route-start" was the SAME lingering-dead-session bug (Fix 2), not a
persistence gap - the remembered entry was written; reconcile just never acted on it because the dead
session read as active. With Fix 2 the remembered source auto-reactivates as soon as the peer advertises
it again.

**Post-deploy verification (next deploy):** (1) a COLD route (both daemons fresh) comes up on UDP and
STAYS up through the sender's encoder-open - no "route up then blackout in the same ms"; `ctl routes`
shows udp, 60fps, loss 0. (2) Kill-and-recover: stop the sender's source mid-route, confirm the receiver
re-establishes within ~1-2s (not 33s) once the source re-advertises; repeated kills back off (1,2,4…)
but a stable route resets to a fast first retry. (3) After a daemon restart with a remembered receive,
the route auto-starts when the peer reconnects (no manual route-start).

## Unit: start-up DOUBLE-OFFER race (2026-11-12, deployed cba1d58 verified; auto-resume + 1.3s recovery good)

The cold-start grace + fast re-establish verified live (remembered route auto-resumed on UDP, recovery
~1.3s). One residual defect: a single blackout at start-up. Receiver log:
  35:42.728 route up (recv) 847b (stream 3)
  35:43.015 auto-reactivated remembered remote source (SECOND offer, 0.29s after the first came up)
  35:44.789 udp route blackout 847b (first route dies)
  35:46.168 route up (recv) 5bec (stream 4, stable since)
Sender: 847b route up (send) 43.704, 5bec route up (send) 47.703 - TWO send routes for one source.

Root cause: a start-up burst of offer triggers stacked a SECOND offer for a (peer,source) whose first
offer was still being established, and the duplicate disrupted the first route (two send routes for one
source; the first's datagrams stopped -> blackout 2s later). Offer triggers all funnel through
Manager.StartReceive: the resume ticker (runReceiveResume immediate + 1s tick), ctl route-start, the
media-child proxy (feat_media), and both UIs. The dedup was LIVENESS-only (my prior fix): an IN-FLIGHT
offer (receive present, route not yet live in router.Stats()) read as "not live", so a second trigger
during the offer->answer->dial->reachability->route-up window either re-offered (reconcile) or was
reclaimed-as-dead (StartReceive) - stacking a second route.

Fix (1) - one IN-FLIGHT-or-live receive per (peer,source) (mediaroute.go + mediaresume.go):
new `Receive.Occupied(now)` = Active OR (never-came-up AND within `receiveEstablishGrace` 8s). An
in-flight offer counts as occupied (no second offer, no reclaim) until it DEFINITIVELY fails (ages out)
or the route came up then DIED (EverUp && !Active -> re-offered fast). `Receives()` now also sets
`EverUp` once a session is live (so a later death re-offers fast instead of being mistaken for
in-flight). Both reconcile's occupied-check and StartReceive's dedup use Occupied.

Fix (2) - one SEND route per (peer,source), newest wins (router.go): new `dropDuplicateSend`, mirror of
dropDuplicateRecv, called when a send route comes up. A duplicate offer that is served ends the OLD send
route with a BYE first, so the receiver's old route ends CLEANLY (errUDPEnded) and switches to the new
one instead of a 2s liveness blackout. The sender never serves one source to one peer over two routes.

Tests: TestReconcileNoSecondOfferWhileInFlight (burst of triggers -> exactly one offer),
TestReconcileReoffersDeadRouteThatCameUp (died route re-offered fast), TestDropDuplicateSend (sender
leaves one send route), plus the existing reconcile/backoff suite.

Post-deploy verification: cold start of both daemons -> the remembered route comes up ONCE on UDP and
stays (no second "auto-reactivated" offer 0.3s after route-up, no start-up blackout); `ctl routes` shows
a single udp route per source on both ends; kill-and-recover still ~1-2s.
