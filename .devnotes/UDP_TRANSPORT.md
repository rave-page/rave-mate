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
