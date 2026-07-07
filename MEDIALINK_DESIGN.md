# MEDIALINK_DESIGN.md - professional-grade LAN media plane

Design for wiring `internal/medialink` into a working audio/video/timecode plane between
rave-mate instances. Grade target: live-music/events production - reliability, measured latency,
no silent quality loss. Scope: intended as an **open-source media plane for audio professionals
broadly** - multi-node (>2 peers) is a first-class assumption from day 1. Reference deployment
(not the ceiling): 2 gaming PCs, consumer 1–2.5 GbE LAN, no 10 GbE, no PTP-capable switches,
Windows primary, NVIDIA GPUs - but encoder capability is **negotiated, never assumed** (§3.2).
Deps: stdlib-first, 7-day soak,
ffmpeg-as-external-binary allowed (precedent: `internal/worker` probe/transcode, `internal/audiorec`,
`internal/stt` - all `mediatools.Resolve` + `exec.CommandContext` + `sysexec` job objects).

Engineering-honest rule used throughout: where a full standard is impractical on this hardware we
name the standard, the deviation, and the consequence.

---

## 1. Current state (built, unwired)

**Transport (`transport.go`, `frame.go`).** `Conn` = AEAD-sealed, length-framed media connection
over any reliable byte stream. Per frame: 4-byte length + AES-256-GCM ciphertext of a fixed 26-byte
binary header (stream id, kind, codec, flags, seq, PTS ns, packed SMPTE TC) + payload. 96-bit nonce
= per-direction monotonic counter, never transmitted (both ends step in lockstep on the ordered
stream); keys HKDF-SHA-256-derived per direction (`medialink c2s/s2c v1`) from a master secret.
Payload capped at 48 MiB (hostile-length guard). Fully unit-tested (round-trip, tamper, role
mismatch, Pump end-to-end). Nothing outside the tests constructs a `Conn`.

**Timecode + LTC (`timecode.go`, `ltc.go`).** SMPTE ST 12M frame-accurate `Timecode` at
24/25/30/29.97DF/50/60/59.94DF incl. drop-frame math (`Frames`/`TimecodeFromFrames` round-trip
tested), duration conversion, `HH:MM:SS;FF` formatting. `EncodeLTC`/`DecodeLTC` = real ST 12M-1
80-bit biphase-mark LTC as int16 PCM (drop flag, sync word, parity bit); >30 fps correctly refused
(needs ST 12M-2 field-doubling) rather than mis-encoded. No producer or audio egress wired.

**Spout local send (`internal/videoshare`).** Working Windows Spout2 sender (cgo shim,
runtime-loaded `SpoutLibrary.dll`, per-deck LockOSThread GL workers, graceful no-op degrade) -
today it only publishes deck cards. It proves the local video egress path medialink needs; a
generic "publish arbitrary NRGBA/BGRA frames as named sender" is a small refactor of `Sender`.
No Spout *receiver* exists yet (needed to ingest OBS/Resolume output as a medialink Source).

**Negotiate types (`negotiate.go`).** `Advert`/`Offer`/`Answer` + `media.advert|offer|answer`
topic constants designed to ride the eventbus (peerlink `ChanBus`). Pure types: nothing publishes
or subscribes them, nothing opens the answered media socket, and peerlink's `Manager` does not yet
expose the per-peer session secret the transport keys derive from - the one wiring gap in the
crypto chain (fix: export a purpose-bound secret, §2.1).

---

## 2. Standards mapping + gap analysis

### 2.1 RTP/RTCP (RFC 3550) vs custom framing

Keep the custom AEAD framing as the wire; adopt RTP *semantics* where they buy interop or
diagnostics; skip the parts that are theater on a mesh of pairwise-authenticated, encrypted links.

| RFC 3550 concept | medialink | Verdict |
|---|---|---|
| SSRC | `Frame.Stream` (uint16, negotiated per route) | Keep ours. SSRC collision detection is for uncoordinated multicast senders; every route is a pairwise authenticated `Conn` (even in a mesh) and stream ids are assigned per-Conn in the `Answer`. Compliance here = theater. |
| Sequence number | `Frame.Seq` (uint32, per-stream) | Keep; 32-bit beats RTP's 16-bit (no wrap ambiguity). Gap = loss signal for telemetry (§7). |
| Timestamp | `PTS` int64 ns on shared clock | Keep. ns beats 90 kHz ticks; one clock domain for A+V kills RTP's cross-SSRC sync dance (RTCP SR NTP mapping) entirely. |
| Marker bit / PT | `Flags` (keyframe/config/end) + `Codec` byte | Equivalent. |
| RTCP SR/RR | **Adopt the semantics**: periodic `KindMeta` report frames on stream 0 - sender: packet/octet counts, wall↔PTS anchor; receiver: fraction lost, cumulative lost, interarrival jitter computed exactly per RFC 3550 §6.4.1/A.8 so numbers are industry-comparable. ~1 report/s per direction. | Adopt (as JSON payload in a meta frame - point-to-point links, bandwidth irrelevant, debuggability wins). |
| SDES/BYE/compound rules | - | Skip: identity comes from the Ed25519 handshake; teardown is `FlagEnd` + socket close. |
| SRTP (RFC 3711) | AES-256-GCM channel already exceeds SRTP's guarantees on TCP. | A future **UDP** profile MUST carry an explicit nonce/seq and follow SRTP-style replay windows (already noted in package doc). |

Deviation named: the wire is proprietary (like NDI's). Consequence: no third-party RTP tool can
tap the link mid-wire; interop happens at the standards-compliant *edges* (Spout, audio devices,
LTC/MTC out, optional AES67-shaped egress §2.2). Accepted - the link is encrypted precisely so
nothing else can read it.

**Framing: raw TCP vs native WebSocket.** `coder/websocket` is already a dep (peerlink), so WS
was evaluated as the media channel: WS binary messages would replace our 4-byte length prefix
(message boundaries are WS's job) and reuse peerlink's listener/upgrade path. Against: RFC 6455
mandates client→server masking - an extra XOR pass over every multi-MB video frame - plus
per-message header/bookkeeping, and it buys nothing on an already-authenticated raw socket.
Rule either way: **never double-frame** (sealed frame inside a WS message inside a length prefix).
Decision: control/negotiation stays on the existing peerlink WS bus (that's the "WS native" part);
media data rides the raw TCP `Conn` (later UDP, §2.5). Honest note: framing overhead is <0.1% of
bandwidth in every variant - any "more efficient than NDI" claim is **codec-driven** (§3.2), never
framing-driven.

**Wire reservations (v1 freeze, day 1).** So UDP/NACK/FEC (§2.5) bolt on without a wire break:
`Kind 3 = KindRepair` (FEC repair frames), meta-frame types `nack`/`report` on stream 0, and
`Offer`/`Answer` gain optional `transport:"tcp"|"udp"`, `nack:bool`, `fec:{scheme,k,n}` fields
(absent = TCP, off, off). The UDP datagram layout is specified now (§2.5) even though P1–P7 ship
TCP only.

**Crypto wiring gap + rekey.** peerlink `Result.SessionKey` is held inside `Manager.conns` and not
exported. Add `Manager.MediaSecret(nodeID) ([]byte, bool)` returning
`HKDF(SessionKey, transcript, "rave-peer-media-v1", 32)` - never the raw session key (domain
separation like `frameInfo`). Keys are per-connection, so every reconnect rekeys. GCM invocation
budget: deterministic nonces, ~1.1 k frames/s worst case (60 fps video + 5 ms audio + meta) →
2³² frames ≈ 45 days continuous; enforce rekey (reconnect the media socket) at 2³¹ frames or 7
days, whichever first. Documented, generous, and no protocol machinery needed.

### 2.2 AES67 audio

AES67 = 48 kHz L16/L24 RTP (RFC 3190), 1 ms packet time, IEEE 1588-2008 PTP media clock, SDP,
multicast. On this hardware:

- **Achievable subset:** 48 kHz L24 (`CodecPCMS24`) interleaved payloads, 1–5 ms framing, RFC
  3550-comparable jitter stats. Internally that's all AES67's payload buys us.
- **Not achievable / not claimed:** PTP media clock (no PTP switches, §2.3), multicast + SAP/SDP
  discovery (we have authenticated unicast + our own negotiation). **Consequence:** medialink audio
  is "AES67-shaped", not AES67. An optional future *egress* sink can emit real RTP/UDP L24
  multicast for AES67-ish receivers, but without a PTP lock, compliant devices will flag clock
  domain mismatch - usable with gear that accepts async sources (many software receivers), not
  with strict ST 2110-30 gear. Say "AES67 payload-compatible egress", never "AES67-compliant".

### 2.3 Clocking: three-tier clock source

`mediaclock` (the shared PTS domain) comes from a pluggable `ClockSource`; selection is
detect-then-configure with priority **PTP > software-sync > follow-master**. Active tier + lock
quality are telemetry (§7); the timecode plane consumes whichever tier is live.

**Tier 1 - IEEE 1588 PTP (when hardware supports it).** Detect: NIC hardware-timestamp caps
(Linux: PHC present / `ethtool -T`; Windows: PTP-capable NIC + the OS/vendor PTP client) and a
reachable PTP master. medialink then *reads* the PTP-disciplined clock - it never implements PTP
itself (ptp4l / W32Time own that). Accuracy: ≤1 µs with hw timestamps + PTP-aware switch,
10–100 µs software-timestamped PTP on a quiet LAN. This is the tier real ST 2110/AES67 interop
requires; without it those labels stay off (§2.2).

**Tier 2 - software sync (default; the reference LAN has no PTP).**
- NTP-style (RFC 5905 on-wire algorithm, pairwise) request/response bursts on the medialink meta
  stream: t1..t4 → offset + RTT; keep min-RTT samples over a sliding window (Cristian/NTP
  clock-filter style), slew a local offset (no OS clock touched - `mediaclock` =
  `monotonic + offset`).
- Burst 8 probes / 2 s during lock, 1/10 s steady-state; disqualify samples with RTT > 2× window
  min (queueing).
- **Accuracy, honestly:** idle GbE ±0.1–1 ms typical; ±1–5 ms under bulk transfer (asymmetric
  queueing is the unmeasurable error floor, RFC 5905 §8 caveat). ~3 orders worse than PTP.
- **Why that's enough:** consumers are frame-based. Half a frame @60 fps = 8.3 ms; LTC chase
  tolerance ±½ frame; lip-sync detectability ≈ +40/−60 ms (EBU R37). ±1 ms keeps every TC rate
  frame-accurate with 8× margin. NOT given: sample-accurate (≤20 µs) cross-PC audio - never
  promise phase-coherent multi-PC summing on this tier.

**Tier 3 - follow-master audio clock.** Slave disciplines `mediaclock` to the master's *audio
device* clock, recovered from the received audio stream: long-term received-sample-rate vs local
monotonic → skew estimate; sinks apply asynchronous sample-rate conversion. For rigs where the
master's audio interface is the house clock. Property: drift-free *relative to master audio*
(the point); absolute offset still seeded once via a tier-2 measurement.

All tiers: sync quality gates the timecode plane - TC slaves freewheel (holdover on last rate)
when quality degrades past ±half-frame, with a UI flag; tier changes are logged + surfaced.

### 2.4 SMPTE ST 2022-7 (dual-path redundancy)

Requires two independent network paths; each PC has one NIC on one switch. **Out of scope.**
Consequence: a cable/switch failure drops the plane; mitigation is fast reconnect (peerlink already
silent-reconnects; media routes re-negotiate on reconnect, §8 P6) not seamless switchover. Named,
accepted.

### 2.5 Loss handling: TCP now; UDP+NACK and per-stream FEC designed in from day 1

Open-source scope changes the calculus: beyond the reference switched LAN (loss ≈ 0, congestion
self-inflicted) users will run this over Wi-Fi and worse. So the protocol reserves the loss
machinery in v1 (§2.1 wire reservations) even though the LAN needs none; implementation is phased.

1. **Now (P1–P7): TCP** + `TCP_NODELAY` + bounded send queue with *policy drops at the application
   edge*: on socket back-pressure, drop stale non-keyframe video frames (counted + logged, §7),
   never audio/meta/timecode. Latency-bounded, loss-visible, zero new machinery. Known cost:
   head-of-line blocking under real loss - acceptable on the LAN, the reason UDP exists below.
2. **UDP+NACK profile - specified now, implemented P8.** Datagram layout (reserved):
   `[8B seq][2B stream][2B frag-idx][2B frag-count][AEAD ciphertext]`, explicit nonce =
   direction ‖ seq with an SRTP-style replay window (RFC 3711); video frames fragment to MTU.
   NACK meta frames with RFC 4585 semantics: seq-range NACK → selective retransmit (window ≤ 1
   frame interval on LAN, configurable to ~100 ms on lossy links); unrecoverable → PLI-style
   keyframe request. Audio concealment = silence insertion, counted.
3. **FEC - optional, activatable per stream, off by default.** Negotiated via the reserved
   `fec:{scheme,k,n}` (§2.1); repair data rides `KindRepair` frames. Scheme: XOR-interleaved
   (SMPTE 2022-1-style) first, Reed-Solomon (RFC 8627-style) if field data demands it - open (§9
   D9). Honest guidance stays: FEC costs n/k bandwidth + block latency and loses to NACK on
   sub-ms-RTT LANs; it earns its keep only where retransmit RTT can't (Wi-Fi bursts, one-way-ish
   links). Default-off on LAN, one toggle away elsewhere.

Multi-node: routes remain pairwise `Conn`s inside the mesh; NACK/FEC/replay state is per-route,
so N nodes add state linearly, no protocol change.

---

## 3. Video

### 3.1 Bandwidth math

BGRA = 32 bpp; NV12 (4:2:0) = 12 bpp. Usable line rate ≈ 940 Mbps (1 GbE) / 2.35 Gbps (2.5 GbE)
after Ethernet/IP/TCP+AEAD overhead (~6%).

| Format @fps | Uncompressed BGRA | Uncompressed NV12 | NVENC HEVC LL | NVENC AV1 LL | JPEG XS (~10:1 of BGRA) |
|---|---|---|---|---|---|
| 1080p30 | 1.99 Gbps | 0.75 Gbps | 8–15 Mbps | 6–12 Mbps | ~200 Mbps |
| 1080p60 | 3.98 Gbps | 1.49 Gbps | 15–25 Mbps | 10–20 Mbps | ~400 Mbps |
| 1440p30 | 3.54 Gbps | 1.33 Gbps | 15–25 Mbps | 10–18 Mbps | ~350 Mbps |
| 1440p60 | 7.08 Gbps | 2.65 Gbps | 25–40 Mbps | 18–30 Mbps | ~700 Mbps |
| 4K30 | 7.96 Gbps | 2.99 Gbps | 25–50 Mbps | 20–40 Mbps | ~800 Mbps |
| 4K60 | 15.93 Gbps | 5.97 Gbps | 40–80 Mbps | 30–60 Mbps | ~1.6 Gbps |

Fit: on **1 GbE** nothing uncompressed fits, JPEG XS fits ≤1440p30; NVENC fits everything with
>90% headroom. On **2.5 GbE** NV12 1080p60 fits (near-zero-latency option worth keeping in mind),
JPEG XS fits ≤4K30; NVENC fits everything many times over.

### 3.2 Codec: negotiated capability matrix (mandatory)

Not every target machine has NVENC - codec choice is **negotiated per route, never assumed**.
`Advert` carries each node's probed **working** encoders *and decoders* (`worker/encoders.go`
already test-encodes NVENC/QSV/AMF/VideoToolbox + x264/x265/SVT-AV1; add the matching hw-decode
probe). Offer/Answer pick the highest common tier:

| Tier | Encoder | Decode req. | Notes |
|---|---|---|---|
| 1 | `av1_nvenc` / `av1_qsv` / `av1_amf` | hw AV1 | best bandwidth; only when both ends hw |
| 2 | `hevc_nvenc` / `hevc_qsv` / `hevc_amf` | hw HEVC | default when both ends hw |
| 3 | `h264_nvenc` / `h264_qsv` / `h264_amf` | hw H264 | universal hw floor |
| 4 | `libx264 -preset superfast -tune zerolatency` (or SVT-AV1 preset ≥10) | any | software fallback - **explicit CPU-budget warning in UI** (1080p60 x264 superfast ≈ 4–8 cores busy); >1080p60 sw refused by default |
| 5 | MJPEG (`CodecJPEG`) | any | intra-only diagnostic / loss-tolerant fallback, ~300–500 Mbps @1080p60 |

LL settings all tiers: no B-frames, zerolatency tuning, ~2 s GOP, capped VBV. JPEG XS
(ISO/IEC 21122) - the "right" broadcast intra codec - stays **unavailable**: no Go impl,
licensing on encoders, no ffmpeg encoder. Named, dropped.

**"More efficient than NDI" - the honest version.** NDI High Bandwidth is SpeedHQ, intra-only,
~100–165 Mbps @1080p60. Inter-coded HEVC/AV1 at LL settings reaches similar perceptual quality at
15–40 Mbps - **3–6× less bandwidth. That claim is codec-driven; framing/transport choice (§2.1)
is noise.** Tradeoff owned: intra-only buys NDI sub-frame codec latency + per-frame loss recovery;
our LL inter pays ~10–20 ms encode+decode and needs keyframe recovery on loss (§2.5) - the right
trade at 1 GbE.

**Encoder integration path** (ffmpeg skepticism heard - compared honestly):

| Path | Latency | Supply chain / cost | Verdict |
|---|---|---|---|
| ffmpeg rawvideo↔Annex-B pipe | +0.5–1 frame (pipe copy + internal buffering; tunable: `-fflags nobuffer -flush_packets 1`) | zero new deps; binary already shipped, probed, precedented; ONE code path covers all 5 tiers + every vendor | **P4 default** |
| Go libav bindings (cgo) | ≈ pipe minus IPC (~1–3 ms) | imports LGPL/GPL C libs into our build + a binding dep (7-day soak); marginal win | rejected |
| Vendor SDK shims - nvEncodeAPI / AMF / libvpl, runtime-loaded DLLs (spout/openvr pattern) | best: zero-copy D3D11 texture→bitstream, skips the GPU→CPU→GPU roundtrip (~5–10 ms saved) + enables Spout-texture-direct | headers vendorable (nv-codec-headers = MIT); no new Go deps; cost = ×3 vendor code paths to own + maintain | **P8 optimization**, NVENC first, gated on P4 measurements showing the 5–10 ms matters |

Software fallback rides ffmpeg regardless of path. Pipeline shape (P4): Spout receiver (new cgo
shim, mirror of the sender) → BGRA/NV12 → ffmpeg encode child (`sysexec` job-object, supervised
featurehost-style) → `Frame{CodecHEVC…}` over `Conn` → peer ffmpeg decode child → Spout sender.
Same-PC routes bypass all of it (Spout→Spout stays on the GPU, as the package doc promises).

### 3.3 End-to-end latency budget (1080p60, NVENC HEVC, target)

| Stage | Budget |
|---|---|
| Spout receive + GPU→CPU readback | 3–8 ms |
| NVENC encode (ull, no B-frames) | 5–10 ms |
| AEAD seal + TCP + LAN wire (25 KB frame) | 1–3 ms |
| Receiver jitter buffer (default 1 frame) | 16.7 ms |
| NVDEC decode | 5–10 ms |
| CPU→GPU upload + Spout present | 2–5 ms |
| **Total** | **33–53 ms** |

**Target: ≤ 60 ms glass-to-glass @1080p60** (≈3.5 frames), measured (§7), alert above it. Software
tiers (§3.2 tier 4) add ~10–30 ms encode - budget then ≤ 90 ms, warned in UI. Honest note: this
beats typical NDI (~1–2 frames + net) only marginally on latency; the wins are bandwidth (§3.2),
encryption, and telemetry.

**Adaptive jitter buffer (D7: safe by default, auto-sized).** Depth `B` starts at 1 frame video /
10 ms audio. A frame is *late* when `arrival_mediaclock > PTS + B`. Algorithm, per stream:
- **Grow (fast):** over a sliding 2 s window, late-rate > 2% **or** any frame late by more than
  one frame interval → `B += 1` frame (audio +10 ms), applied immediately. Caps: 4 frames video /
  100 ms audio.
- **Decay (slow):** late-rate < 0.1% sustained for 30 s → `B −= 1` frame (audio −10 ms). Floors:
  1 frame / 10 ms.
- Hysteresis by construction (grow-fast/decay-slow); every resize is logged + counted; current `B`
  and late-rate are telemetry (§7). Zero-buffer stretch (~35 ms glass-to-glass) remains a manual
  override, never automatic.

---

## 4. Timecode plane

**Clock master election.** One node is TC master; its `mediaclock` (§2.3) is authoritative.
Policy: user-pinned master in config wins; else deterministic auto-election = lowest NodeID among
peers advertising cap `media.clock` on the eventbus (BMCA is overkill for ≤3 nodes; ties are
impossible on unique NodeIDs). Master loss → slaves freewheel (holdover) at last rate + flag UI;
re-election after 5 s. Slave TC = `TimecodeFromDuration(mediaclock − epoch, rate)`.

**ST 12M generation.** Master picks epoch: **time-of-day** (TC = local wall clock, jam-synced -
standard events practice) or **session-zero** (00:00:00:00 at session start) - config choice, open
decision D5. Rate from config (default 30 ndf; 29.97DF/25/24 supported; 50/60 refused for LTC per
`ltcSupported`, still fine on MTC full-frame? no - MTC also caps at 30, so >30 fps TC is
wire+overlay only, named limitation).

**Egress sinks:**
- **LTC as audio out:** `EncodeLTC` frames concatenated → chosen output device via the existing
  `gopxl/beep`/oto stack (already a direct dep; audio out path exists in-app for the player).
  48 kHz mono int16, level configurable (~−18 dBFS default so it doesn't clip consumer gear).
  Feeds Resolume/lighting/DAW chase inputs - this is the standards-compliant surface.
- **MTC via MIDI out:** `internal/midi` is input-only today (winmm `midiIn*` syscalls); add the
  mirror `midiOutOpen/midiOutShortMsg/midiOutClose` - stdlib syscall, **no new dep**. Quarter-frame
  details: 8 QF messages per 2 frames, `F1 <piece<<4 | nibble>`; pieces 0–7 = frame-lo, frame-hi,
  sec-lo, sec-hi, min-lo, min-hi, hr-lo, hr-hi+rate (rate bits 1–2 of piece 7: 00=24, 01=25,
  10=29.97DF, 11=30). Receiver-perceived TC lags 2 frames (spec) - emit pieces for `TC+2` so
  chasers land on time. On locate/jump or start: full-frame SysEx `F0 7F 7F 01 01 hh mm ss ff F7`
  (hh top bits carry rate), then resume QF.

**Consumers:**
- **OBS overlay stamp:** overlayserver/deckcard render the live TC string (existing render modes
  pick it up for free).
- **Recordings:** audiorec/OBS captures tag start-TC + rate in the libdb `set_recordings` row +
  file tags → post-session conform against other machines' recordings.
- **Spout metadata - named deviation:** Spout has no per-frame metadata channel (NDI does).
  Consequence: TC cannot ride the Spout texture; local consumers get TC via the eventbus
  (`media.tc` topic, ~4 Hz + on-jump) or burn-in. Cross-PC frames DO carry per-frame TC in the
  medialink header, so anything consuming from medialink itself is frame-accurate.

---

## 5. Webcam / UVC source

Goal: a camera on either PC as a medialink `Source` → local Spout sender + network route (VR-PC
webcam on the stream PC's OBS, per the crash-resilience plan).

- **Capture:** ffmpeg dshow rawvideo pipe (`-f dshow -i video="<name>" -pix_fmt bgra|nv12 -f
  rawvideo -`) - exact precedent: `audiorec` (dshow audio), `stt/capture.go`. Enumerate via
  `-list_devices` (precedent: `audiorec.listDevices`). Supervised child, job-object kill, restart
  with backoff. Native Media Foundation (`IMFSourceReader`) is the *later* upgrade (lower latency,
  no child process, MJPEG/NV12 native dequeue) - COM via syscall is heavy; do it only if the ffmpeg
  path's ~1-frame extra latency matters in practice.
- **PTZ/exposure control:** ffmpeg exposes none of it, so a small COM shim (syscall, no cgo
  required - vtable calls like other COM-via-syscall precedents, or one cgo shim mirroring
  `spout_shim`) binds the DirectShow filter for the same device and drives `IAMCameraControl`
  (pan/tilt/zoom/focus/exposure = `KSPROPERTY_CAMERACONTROL_*`) + `IAMVideoProcAmp`
  (brightness/contrast/WB/gain). Control interface open is independent of the capture stream;
  risk: some drivers exclusive-lock - mitigation: open control first, degrade to capture-only with
  a UI note. Controls are surfaced on the eventbus (`media.cam.ctl`) so either PC's UI (or a VR
  overlay) can drive the remote camera.
- Source advertises via `Advert.Sources` (kind=video, native codec NV12/BGRA, probed
  width/height/fps); routing/encode identical to §3 - the webcam is just another Source.

---

## 6. Local video surfaces (per-OS)

The network transport is OS-agnostic (frames = BGRA/NV12 or encoded bitstream + PTS/TC); only the
*local* materialization surface is per-OS native. One abstraction, backends behind build tags -
the exact `videoshare` pattern (`Sender` interface, `spout`/`syphon`/`pipewire`/no-op tags already
reserved in its package doc, runtime-loaded native lib with graceful degrade). A Windows Spout
sender must land as a PipeWire node on a Linux receiver and vice versa with zero protocol change.

**Abstraction (day 1, in medialink):**
- `VideoSurfaceSink` - present decoded frames as a named local output (`Send(name, frame)` /
  `Remove` / `Close`) - generalizes `videoshare.Sender` (which then becomes one client of it).
- `VideoSurfaceSource` - ingest a named local producer (enumerate + subscribe frames) - the
  medialink `Source` for §3's pipeline.
Both are pure interfaces in medialink; hardware backends live behind build tags, no-op default, so
the protocol/tests stay hardware-free. Negotiation is already surface-neutral (`SinkDesc`/
`SourceDesc` carry ids + formats, not OS types).

**Per-OS backends + copy cost:**

| OS | Surface | Integration | Copy cost today | Zero-copy path (later) |
|---|---|---|---|---|
| Windows | Spout2 (DX11 shared texture) | existing cgo shim, runtime `LoadLibrary` | CPU BGRA → GL upload, ~2–5 ms @1080p (ffmpeg-pipe decode forces GPU→CPU→GPU) | NVDEC D3D11 output surface → Spout DX11 texture direct (needs native decode session, drops the CPU roundtrip) |
| Linux | PipeWire video source node | cgo shim over `libpipewire-0.3` (below) | SHM buffer memcpy per frame (~250 MB/s @1080p60 NV12 - fine) | DMA-BUF buffers (VAAPI/NVDEC → DMA-BUF → PipeWire), driver-dependent |
| macOS | Syphon (IOSurface/Metal) | ObjC shim via cgo, weak/runtime-linked Syphon.framework | CPU → `MTLTexture` upload per frame | VideoToolbox decode into IOSurface → publish directly |

**PipeWire integration choice (stdlib-first + 7-day soak):**
- **cgo `libpipewire-0.3` shim (recommended):** mirror `spout_shim` - header-only at build,
  `dlopen("libpipewire-0.3.so.0")` at runtime so a machine without PipeWire degrades to no-op
  instead of failing to launch. It's a *system* library (distro-pinned, not a go.mod dep) → no new
  module, supply-chain rule satisfied trivially; SUPPLY_CHAIN.md gets a row for the shim anyway.
- **Pure-Go PipeWire protocol lib:** none mature exists; the native protocol (SPA POD
  serialization) is under-documented - writing one is its own project. Rejected for scope, not
  principle; revisit if a soaked pure-Go client appears.
- **GStreamer: NOT wanted** - pulls a huge dependency tree to do what one PipeWire stream node
  does; only justification would be needing its codec plumbing, which we already get from ffmpeg.
  Named + rejected.
- `pw-cli`/subprocess: can't push frames; not an option.

**Syphon:** requires ObjC (`SyphonMetalServer`, IOSurface-backed `MTLTexture`) - small `.m` shim
compiled via cgo, same degrade-if-absent pattern. macOS is last in line (no current target
machine); the abstraction just guarantees it slots in.

**Order:** Windows first - both current PCs are Windows; Spout backend ships with the video phase
(§8 P4). Linux PipeWire + macOS Syphon land as a later phase (§8 P7) against the day-1 interfaces.

## 7. Telemetry / quality gates

Per-stream, both ends, exported to UI (Peers → Media tab) + logbus:

- **Loss:** seq-gap count, policy-drop count *by reason* (backpressure-stale, decode-fail,
  jitter-late) - **no silent frame drops**: every drop increments a counter and emits a throttled
  log line. A drop without a counter is a bug by definition.
- **Jitter:** interarrival jitter per RFC 3550 A.8 (comparable to any RTP tool's numbers).
- **Latency:** per-frame e2e = `receiver mediaclock − PTS` (valid because of §2.3 sync; its own
  accuracy ±offset-error is displayed alongside). Rolling p50/p95/max.
- **Clock sync quality:** active tier (PTP / software / follow-master), current offset, RTT,
  sample jitter, lock state (locked / degraded / holdover) - gates TC plane (§2.3).
- **Buffer:** current adaptive depth `B`, late-frame rate, resize events (drives + proves the
  §3.3 algorithm).
- **A/V offset:** per session, delta of presented-PTS between the audio and video sinks of the same
  route; alert outside +40/−60 ms (EBU R37). Measured at the sink hand-off (device write / Spout
  send), honest caveat: excludes device/display latency after our edge.
- **Rates:** fps in/out, Mbps, encoder queue depth, ffmpeg child restarts.
- Quality gates (release criteria per phase, §8): 30-min soak with zero unexplained drops, e2e
  latency ≤ target, sync locked ≥ 99% of samples.

---

## 8. Phased implementation plan

Each phase ships + verifies independently (ctl-driven per suite CLAUDE.md; two local instances on
one PC - loopback discovery already works - plus the real 2-PC check).

**P1 - Negotiation wiring + audio proving ground.**
Wire `media.advert|offer|answer` onto the eventbus; add `peerlink.Manager.MediaSecret` (§2.1);
media listener (TCP, port range 47641–47645, `TCP_NODELAY`); route manager (offer→answer→dial→
`NewConn`→`Pump`). First route: audio capture (ffmpeg dshow, `CodecPCMS16/24` 5 ms frames) → peer
audio out (beep/oto). v1 wire freezes WITH the §2.1 reservations (`KindRepair`, nack/report meta
types, `transport`/`nack`/`fec` negotiate fields). *Accept:* two instances negotiate + stream
audio for 30 min; zero seq gaps; audible latency < 50 ms; teardown + re-offer works; adverts
propagate across ≥3 nodes (eventbus relay) with routes staying pairwise; `go test` covers
negotiate round-trip on a fake bus incl. reserved fields.

**P2 - Clock sync + telemetry.**
Pluggable `ClockSource` interface + tier detection scaffolding (§2.3); software tier implemented;
PTP + follow-master tiers land P8. RFC 3550-style report frames + counters + UI panel (§7).
*Accept:* steady-state offset estimate stable within ±1 ms (idle LAN) shown in UI with active
tier; jitter/loss/latency visible per stream; drop counters proven by fault injection (kill a
sink, see counted drops, no silence).

**P3 - Timecode plane.**
Master election + ST 12M generation off synced clock; LTC audio-device egress; MTC out (winmm
midiOut, QF + full-frame); `media.tc` bus topic; overlay stamp + recording start-TC tagging.
*Accept:* DAW (Reaper/Ableton) chases LTC within ±1 frame; both PCs' overlays show identical TC
(±1 frame on camera); pulling LAN cable → slave freewheels + flags, recovers on reconnect.

**P4 - Video route (Spout↔Spout cross-PC, Windows).** → shipped (code-complete), §14.
Define `VideoSurfaceSink`/`VideoSurfaceSource` interfaces + no-op backend (§6, day 1); Spout
receiver shim + Spout backend of both; ffmpeg encode child ↔ decode child (worker/featurehost
pattern); **full codec matrix negotiation** (§3.2: NVENC/QSV/AMF tiers + sw fallback with CPU
warning; decoder probe added); adaptive jitter buffer (§3.3) + keyframe-aware policy drops.
*Accept:* 1080p60 OBS→PC2→Resolume glass-to-glass ≤ 60 ms (measured via clapper/flash frame);
30-min soak, all drops counted + explained; bitrate within configured budget; same-PC route stays
GPU-only; forcing sw-only on one end negotiates tier 4 + shows the CPU warning; buffer auto-grows
under injected jitter and decays back.

**P5 - Webcam/UVC source.** → first slice shipped, §13.
dshow capture source + device enumeration; PTZ/exposure COM shim + `media.cam.ctl` bus surface +
UI; webcam → local Spout + network route.
*Accept:* Brio on PC1 visible in PC2's OBS ≤ 100 ms; zoom/focus/exposure driveable from either PC;
device unplug/replug recovers.

**P6 - Hardening + multi-node.**
Reconnect-resume of active routes after peerlink drop; rekey-on-schedule; multi-stream (audio +
video + TC concurrently) soak under game load; 3-node mesh soak (routes fan out pairwise);
optional AES67-payload egress sink.
*Accept:* 4-hour mixed soak, VRChat running, zero route deaths without auto-recovery; 3-node
mesh stable with per-route telemetry; documented measured numbers replace the estimates in this
doc.

**P7 - Cross-OS surfaces (Linux PipeWire, macOS Syphon).**
PipeWire cgo shim (dlopen, no-op degrade) implementing `VideoSurfaceSink`/`Source`; Syphon ObjC
shim; CI builds all three tag sets (per the all-feature-tags CI rule).
*Accept:* Windows Spout sender → Linux receiver shows the stream as a PipeWire node consumable in
OBS-Linux; reverse direction Linux→Windows lands in Spout; no protocol/negotiation change needed;
machines without PipeWire/Syphon degrade to no-op with a logged reason.

**P8 - Advanced transport + clock tiers.**
UDP+NACK profile (§2.5 layout, SRTP-style replay); per-stream FEC (scheme per D9); PTP +
follow-master clock tiers behind the P2 `ClockSource` interface; NVENC-direct zero-copy shim if
P4 measurements justify (~5–10 ms).
*Accept:* injected 1% random loss on UDP → no visible artifact longer than 1 frame, NACK counters
tick; FEC toggles live per stream with measured bandwidth cost; on PTP-capable hw the tier
auto-selects and shows µs-class offset; follow-master mode holds a drift-free audio lock over 1 h.

---

## 9. Decisions

**Resolved (user, 2026-07-01):**
- **D1 - Codec:** capability negotiation **mandatory**; full matrix NVENC + AMF + QSV + software
  x264/SVT-AV1 fallback with explicit CPU warning (§3.2). "More efficient than NDI" =
  codec-driven (HEVC/AV1 inter vs SpeedHQ intra, 3–6× bandwidth), not framing-driven; media data
  on raw TCP `Conn`, control on the existing peerlink WS bus, never double-framed (§2.1).
  ffmpeg-pipe is the P4 path; vendor-SDK shims (NVENC first) are the P8 candidate.
- **D2 - Clock:** three-tier `ClockSource`: PTP > software-sync (default) > follow-master audio
  clock; detect/configure per §2.3.
- **D3 - Loss / scope:** open-source, multi-node first-class. TCP now; UDP+NACK + per-stream
  optional FEC reserved in the v1 wire from day 1 (§2.1, §2.5), implemented P8.
- **D7 - Latency:** safe adaptive jitter buffer with grow-fast/decay-slow algorithm (§3.3);
  zero-buffer stays a manual override.

**Still open:**
- **D4 - New deps:** zero new Go deps holds through P7 (stdlib + beep/oto + winmm syscalls +
  ffmpeg external + cgo shims). P8 vendor shims vendor C headers (nv-codec-headers = MIT) -
  sign off at P8. `malgo` (WASAPI) only if beep/oto latency disappoints; soak not started.
- **D5 - TC epoch:** time-of-day (jam-sync, events convention; recommended) vs session-zero;
  per-session override either way.
- **D6 - Master election:** user-pinned with lowest-NodeID auto-fallback (recommended) vs
  strictly manual.
- **D8 - PipeWire binding:** cgo shim over system `libpipewire-0.3`, runtime dlopen (recommended;
  no go.mod dep, GStreamer rejected, no viable pure-Go client) - adds a second cgo shim + Linux
  build-time headers.
- **D9 - FEC scheme:** XOR-interleaved (SMPTE 2022-1-style, cheap) vs Reed-Solomon (RFC
  8627-style, stronger) - decide before P8; wire reserves `fec:{scheme,k,n}` so both fit.
- **D10 - OSS license:** project license for the open-source release (constrains vendored headers
  + shim licensing choices).

---

## 10. P1 decisions (implemented 2026-07)

P1 shipped the transport + session layer, the v1 wire freeze (incl. reservations), the clock seam,
and the source/sink attach API - all pure-Go, unit + golden tested, zero new deps. Where the doc was
ambiguous the RFC-compliant reading was taken and recorded here. **Done vs seamed** at the end.

- **P1a - Media socket correlation (new, doc was silent).** A broadcast-bus `Answer` can't bind a
  raw TCP dial to a route. The dialer sends a plaintext length-prefixed **session-id preamble**
  (`[2B len][id]`, ≤128 B) before any AEAD frame; the listener maps it → pending route → peer node
  id → `MediaSecret`. Not secret (authenticity is the AEAD channel - a wrong id just fails to
  correlate or fails `Open`). SRTP-style replay concerns stay deferred to the UDP profile (§2.5).
- **P1b - Offer/Answer roles (resolves §1's "one ID may be empty").** Concrete **pull** model:
  the **requester owns the Sink and dials** (medialink initiator, keys c2s per transport.go); the
  **Target owns the Source and listens** (responder). Deterministic per-direction keys, one Conn per
  route (RFC 3550 SSRC-per-Conn, §2.1). Push (local source → remote sink) is the same machinery with
  roles swapped - left for when a producer needs it; not required by P1's audio proving ground.
- **P1c - `Offer.Target` added.** Source ids aren't unique mesh-wide, so an offer names the owning
  node explicitly → exactly one answerer. A missing/empty Target falls back to "whoever owns SourceID".
- **P1d - PTS domain.** `mediaclock` = local **monotonic ns** (`TierMonotonic`), the P1 tier behind
  the `ClockSource` seam. A source may pre-stamp `PTS` (capture time); the route fills it from the
  clock only when zero. ns timestamps (not RTP 90 kHz ticks) per §2.1; AES67 audio framing rides as
  PTS ns. The P2 software-sync tier slews via `MonotonicClock.SetOffset` - no wire/timecode change.
- **P1e - Meta-frames (RFC 3550 §6.4 / RFC 4585 semantics).** `KindMeta` JSON on **stream 0** (RTP
  SSRC-0 analogue), tagged by a `"t"` field: `MetaReport` (SR/RR: packet/octet counts, wall↔PTS
  anchor, fraction/cumulative lost, §A.8 jitter) and `MetaNACK` (seq-range + PLI). Interarrival
  jitter is carried in **PTS ns**, not 90 kHz ticks (one clock domain, §2.1) - numerically
  comparable to RTP tools after unit scaling. Layout frozen + golden-tested now; **generation is P2
  (report) / P8 (nack)** - P1 only defines + parses them so a newer peer degrades cleanly.
- **P1f - `KindRepair` = Kind 3** reserved for FEC (§2.5); frozen in the golden wire, generated P8.
- **P1g - Stream-id allocator** starts at 1 (0 = meta), assigned per route by the answerer; carried
  in `Answer.Stream`.
- **P1h - `MediaSecret` gating.** `peerlink.Manager.MediaSecret(nodeID)` = `HKDF(SessionKey,
  transcript, "rave-peer-media-v1", 32)`, returned only for a **connected** peer, **per-connection**
  (reconnect rekeys, §2.1 budget). Both ends derive identically (symmetric session key + transcript).
- **P1i - Listener.** TCP ports **47641–47645**, `TCP_NODELAY` on every socket (§2.5 latency bound);
  pending routes GC after 30 s if never dialed; ephemeral-port mode for tests (no fixed-port binds).

**Wire freeze (v1):** `KindRepair`; stream-0 meta types `report`/`nack`; `Offer`/`Answer` optional
`transport`/`nack`/`fec` (absent = tcp,off,off). All golden-pinned - changing any is a version bump.

**Done in P1:** AEAD transport (pre-existing) + session/route layer (`router.go`), media listener,
offer→answer→dial→pump wired onto the eventbus, `MediaSecret`, monotonic `ClockSource` + PTS
stamping, meta/negotiate wire reservations, per-route loss/traffic counters (§7 seam),
`RegisterSource`/`RegisterSink` attach API. Tests: peerlink `MediaSecret` parity + domain
separation; medialink frame + meta golden (incl. KindRepair/report/nack), clock, negotiate
reservations, and a full **loopback-TCP** negotiation (advert → offer → answer → dial → 32-frame
stream, seq/stream/PTS asserted, zero gaps) + reject path.

**Seamed for later phases (P1 references, interface only):** hardware sources/sinks - dshow audio
capture (`CodecPCMS16/24`, 5 ms frames) + beep/oto audio out - attach via the registry (P1 ships
none). Report/NACK **generation** + jitter/latency telemetry + UI panel (P2). Codec matrix
negotiation (P4). UDP+NACK, FEC, PTP/follow-master tiers (P8).

**Needs 2-machine live verification (not doable in-worktree):** P1 accept criteria - two instances
negotiate + stream real audio 30 min, audible latency < 50 ms, zero seq gaps, teardown + re-offer,
adverts across ≥3 nodes - all require the hardware audio source/sink + real paired peers +
`Answer.Addr` LAN-IP autodetect on a real NIC. Verified so far only over loopback TCP with fakes.

---

## 11. P2 decisions (implemented 2026-07)

P2 shipped telemetry + reports, the software clock-sync tier, the §2.5 NACK/retransmit machinery
(TCP profile, ahead of the P8 UDP profile that reuses it), and the §3.2 codec-negotiation
groundwork. All pure-Go, unit + golden tested, zero new deps. Wire discipline held: only **additive
negotiated extensions + new meta types** - the frozen v1 frames are untouched.

- **P2a - `Caps` session extension (new, negotiated).** Optional `caps{report,sync,enc,dec}` on
  `Advert`/`Offer`/`Answer` (omit-when-empty). Offer = requester's support; Answer = granted
  intersection; each side emits only what the other granted. **Absent = P1 peer = pure P1 wire**
  (compat-tested by raw-dialing a route as a P1 peer and asserting silence). `Offer.NACK`
  (reserved since v1) now actually negotiates.
- **P2b - Reports generated both ways (§7).** Receiver RR: RFC 3550 §A.8 interarrival jitter (ns
  units per §2.1), §A.3 cumulative + interval fraction lost; sender SR: packet/octet counts +
  wall↔PTS anchor. ~1/s on stream 0; consumed into `RouteStat.Remote`. Stream-0 meta is consumed
  by the route loops, never delivered to media sinks. e2e latency (`receiver mediaclock − PTS`)
  kept as rolling p50/p95/max over a 256-sample window.
- **P2c - Software clock tier (§2.3 tier 2).** New additive meta types `sync`/`syncr` (golden-
  pinned; caps.sync-gated) carry NTP-style t1..t4. `OffsetEstimator` = clock filter: 32-sample /
  60 s window, min-RTT sample wins, RTT > 2× window-min disqualified, lock ≥3 qualifying fresh
  samples, 30 s staleness → holdover. `SoftwareClock` (TierSoftware) slews a monotonic base from
  filtered residuals (absolute-offset window → no feedback double-correction). Requester (recv
  side) probes: 250 ms burst until locked → 10 s steady. `Options.SyncPeer` pins the discipline
  master (D6 groundwork; auto-election = P3 master election). Telemetry: per-peer `SyncStats()`
  (offset/RTT/dispersion/lock) + `ClockQuality()`. Loopback-verified: +500 ms peer disciplined to
  <20 ms.
- **P2d - NACK/retransmit (§2.5, TCP profile).** Receiver NACKs seq gaps (frozen `nack` meta);
  sender selectively retransmits from a bounded FIFO (512 frames / 16 MiB) with original
  Stream/Seq; late arrivals decrement `LostEst` (`Recovered`). PLI (inverted range + `pli`) →
  `KeyframeSource.RequestKeyframe()` optional-interface seam for P4 encoder sources. On ordered
  TCP gaps only arise from application-edge policy drops - exactly what the net.Pipe test injects;
  P8's UDP profile reuses this machinery unchanged.
- **P2e - Codec groundwork (§3.2).** `Caps.Encoders/Decoders` advertise probed sets;
  `NegotiateCodec` walks the tier matrix (av1 hw → hevc hw → h264 hw → libx264/libsvtav1 sw with
  CPU `Warning()` → mjpeg floor); onOffer applies it to video routes, Answer records the chosen
  encoder. ffmpeg encode/decode children + `worker/encoders.go` probe wiring land P4.

**Done in P2:** report generation/consumption, §A.8 jitter + §A.3 loss + latency accounting,
software-sync tier + estimator + discipline seam, NACK/retransmit + PLI seam, codec capability
advertisement + matrix. Tests: golden (sync meta), jitter/loss/latency math, clock filter, pipe
loss-loop, loopback report/sync/negotiation e2e, P1 compat.

**Deferred / remaining:**
- **UI panel (§7/P2 accept):** `Stats()`/`SyncStats()`/`ClockQuality()` are API-complete; the
  Peers → Media Fyne panel + fault-injection soak against the running app remain (not verifiable
  in-worktree; needs the live ctl workflow).
- **App wiring:** `Options.SyncPeer` + `Encoders`/`Decoders` are not yet fed from config /
  encoder probe (P3/P4 wiring).
- **P3:** TC master election (auto lowest-NodeID; `SyncPeer` pinning exists), ST 12M off the
  synced clock, LTC/MTC egress, `media.tc` topic. → shipped, §12.
- **P8:** UDP+NACK datagram profile, FEC generation (`KindRepair`), PTP + follow-master tiers
  behind the same seams.

---

## 12. P3 decisions (implemented 2026-07)

P3 shipped the §4 timecode plane: master election, ST 12M generation off the synced clock,
`media.tc`, egress wiring onto the disciplined clock, and the §7 Peers → Media UI panel. Pure Go,
unit-tested, zero new deps. Wire discipline held: **one new omitempty caps flag + one new bus
topic** - every frozen v1 frame and P1/P2 negotiation blob is byte-identical (golden + compat
tests).

- **P3a - `Caps.Clock` (advert-level, additive).** The §4 "cap `media.clock`" is `Caps.Clock`
  (omitempty) on the Advert; election candidates = self + peers advertising it. P2-era caps blobs
  marshal unchanged; a P2 advert (`clock` absent) is never a candidate even with the lowest NodeID
  (compat-tested). Offer/Answer never carry it (route caps unaffected).
- **P3b - `media.tc` announce (bus topic, not AEAD wire).** `TCAnnounce{node, running, rate,
  drop, frame, anchor_ns}` on `TopicTC`, golden-pinned. Cadence ~4 Hz (`tcAnnounceEvery` 250 ms) +
  forced on start/stop/jam (the §4 on-jump announce). Anchor pair (frame, master-mediaclock-ns)
  instead of a bare TC string so slaves project frame-exactly between announces. P1/P2 peers never
  subscribe the topic - compat by omission.
- **P3c - Election (D6 resolved as recommended).** User-pinned master wins (pin must match on
  every node - an asymmetric pin can deadlock mastership, named limitation until config wiring);
  else lowest NodeID among clock-capable candidates. Convergence by yield: every node that believes
  itself master announces; receiving an announce from a better-ranked node deposes the belief.
  Master silence > `tcStale` (5 s) → slave flags **holdover**, freewheels on the last anchor
  (§4), deposes the master, re-elects (self-takeover included). `PeerGone` hook drops departed
  peers immediately.
- **P3d - Slave TC domain.** Slave frame-now = `ann.frame + (mediaclock − anchor)·fps`, where
  anchor = the master's `anchor_ns` when the local clock is disciplined into the master's domain
  (`ClockQuality.Locked` on a non-monotonic tier), else the local receipt time (error = bus
  latency, ≪ ½ frame on a LAN). Election feeds discipline: `TCPlane.OnMaster` →
  `RouteManager.SetSyncPeer(master)` (self-master → ""), resolving §11's "auto-election = P3"
  note - the elected TC master IS the sync master.
- **P3e - Egress wiring (reuse, not duplication).** `internal/timecode` stays the one house
  generator/egress stack. `Generator.SetNow` + `Service.FollowClock(mediaClock.Now)` re-base its
  frame counter onto the medialink clock - LTC/MTC/Art-Net sinks already resync per tick off the
  generator, so all egress follows the disciplined domain with no sink changes. A slew of the
  disciplined clock shifts TC with it (tested).
- **P3f - Master/slave glue.** `Service.AttachPlane`: the generator is the plane's announce
  source; as slave a *running* local clock chase-jams onto the master past a ±1-frame dead band
  (the §8 P3 accept tolerance), adopting the master's rate - sinks reseed via the existing `Jam`
  path (LTC counter reseed + MTC full-frame locate). A stopped local clock is never auto-started
  (starting sinks stays a user action).
- **P3g - App wiring.** The app's media clock is now a `SoftwareClock` (was implicit monotonic);
  TCPlane starts/stops with the peers module; `ctl tc-status` gains `tcmaster=self|<node>`
  (+`(holdover)`) with no new ctl verbs.
- **P3h - UI (§7 panel + P2 deferred item).** Peers tab gains a "Media plane" block: per-route
  RouteStat (frames/bytes, loss + recovered, §A.8 jitter, latency p50/p95, NACK/retx/PLI, remote
  RR for send routes), clock tier/lock/offset + per-peer sync estimates, TC master line with
  holdover flag - each with ?-help tooltips. Formatters are pure + unit-tested.

**Done in P3:** election + announce/follow/holdover (`tcplane.go`), `Caps.Clock`, `SetSyncPeer`
live retarget, timecode egress on the disciplined clock, service↔plane glue, Peers → Media panel.
Tests: TCAnnounce/caps golden + P2-shape compat, election (auto/pinned/non-candidate/peer-gone),
follow math (receipt + shared domain), holdover freewheel, takeover + OnMaster, chase callback,
FollowClock/chase-jam/status-line, UI formatters.

**Deferred / remaining:**
- **Live accept criteria (§8 P3):** DAW chases LTC ±1 frame, cross-instance overlay TC identical,
  cable-pull freewheel/recover - need the 2-instance ctl workflow + real devices, not doable
  in-worktree.
- **Overlay TC stamp + recording start-TC tagging (§4 consumers):** overlay render modes +
  `set_recordings` schema touch; land with live verification.
- **Config for D6 pin:** `TCPlaneOptions.Master` isn't fed from config yet. The probed
  encoder/decoder feed shipped with P4 (mediapipe probe → `SetCodecCaps`, §14).
- **P8 unchanged:** UDP+NACK profile, FEC generation, PTP + follow-master tiers.

---

## 13. P5 decisions (first slice implemented 2026-07)

P5's **local slice** shipped (`internal/webcam` + `webcam` module + Peers-tab UI + config v24):
device enumeration, supervised dshow capture → local Spout sender, native UVC PTZ/exposure COM
shim, and the cross-instance `media.cam.*` bus surface. The **network video route waits for P4**
(encode children + codec matrix) - the capture already implements `medialink.Source`, so P4 wiring
is `RegisterSource` + fan-out, no capture change.

- **P5a - Capture = supervised in-proc ffmpeg child** (rtspserve/audiorec pattern:
  `mediatools.Resolve` + `exec.CommandContext` + `sysexec.Hide`, restart with capped 1→10 s
  backoff, stderr ring tail). Not a `worker`/`featurehost` child: those add a JSON-stdio protocol
  layer a raw frame pipe doesn't want. Unplug/replug = restart loop until frames flow again.
- **P5b - Pixel format: RGBA, not BGRA (named deviation from §5).** ffmpeg's swscale converts
  the camera's native format either way at identical cost, and the existing Spout shim sends
  GL_RGBA - piping `-pix_fmt rgba` (`CodecNRGBA`) feeds it zero-swizzle/zero-copy (fresh pipe-read
  buffer wrapped as `image.NRGBA`). A BGRA wire codec stays available for P4 if a receiver wants it.
- **P5c - Local route runs through the medialink seams:** capture (`medialink.Source`, cap-1
  newest-wins channel + drop counter) → `medialink.Pump` → `spoutSink` (`medialink.Sink` over
  `videoshare.FrameSender`, sender `"rave-mate cam <device>"`). Non-spout builds: FrameSender
  errors → feature reports the reason string, capture never starts. PTZ control stays available
  (independent of the video path).
- **P5d - UVC COM shim = stdlib syscall, stateless per call** (midi/winmm precedent, no cgo/new
  deps): each op locks an OS thread, `CoInitializeEx(APARTMENTTHREADED)`, SystemDeviceEnum →
  VideoInputDeviceCategory → FriendlyName match (same names ffmpeg uses) → `IBaseFilter` → QI
  `IAMCameraControl`/`IAMVideoProcAmp`, then releases everything - no COM object crosses threads,
  no held handle to exclusive-lock a driver. Surfaced props: pan/tilt/zoom/focus/exposure +
  brightness/contrast/saturation/whiteBalance/gain; per-prop range/step/default/auto-cap; sets are
  clamped + step-snapped; missing props are omitted (graceful no-op).
- **P5e - `media.cam.ctl` realized as two topics** (obscontrol pattern): `media.cam.status`
  broadcast (~2 s + on change: enabled/running/device+modes/sender/props/err) + `media.cam.cmd`
  directed (`cam.start|stop|set|refresh`, Target = node id); capability `media.cam`. The camera
  executes on its owning instance; any paired instance's UI drives it. Remote view/control needs
  the Webcam feature enabled on both ends (module gating = zero-footprint rule).
- **P5f - Config/UI:** `WebcamFeature{enabled, device, width/height/fps, autoStart}` (v24,
  additive, off by default; autoStart = crash-recovery rigs). Peers tab gains a "Webcam" block -
  local + each paired instance's camera: device/mode pick (parsed from `-list_options`),
  Start/Stop, copyable Spout sender name, prop sliders with auto checkboxes; panels persist
  across the tab's 2 s rebuilds. Settings ▸ Streaming & remote gains the feature card.

**Done in P5 (this slice):** enumeration (`-list_devices`/`-list_options` stderr parsers),
capture + frame-pipe stride framing, Spout sink, UVC shim, bus surface, module + config + UI.
Tests: parser fixtures (tagged + sectioned ffmpeg formats), frame framing (half-reads, torn tail,
fresh buffers), clamp/step-snap, bus ctl round-trip across two linked eventbuses (start w/ mode →
status visible remotely → set prop → stop), config gating, capability advertise/retract, UI
formatters.

**Deferred / remaining (P5 accept §8):** network route wiring shipped with P4 (§14 - webcam
registers as source "webcam", capture fans out to local Spout + N route taps); live checks not
doable in-worktree - real-webcam capture, OBS Spout pickup, PTZ from a paired instance,
unplug/replug recovery, webcam → peer OBS ≤ 100 ms.

---

## 14. P4 decisions (implemented 2026-07)

P4 shipped the encode/decode pipeline: ffmpeg children (`internal/mediapipe`), the §3.3 adaptive
jitter buffer + keyframe policy, real §3.2 negotiation off probed caps, the Spout receiver, and
the route-creation surface (`internal/mediaroute` + Peers tab + Settings card). Wire discipline
held: ONE additive Offer field (`br`, omitempty) - frozen v1 frames byte-identical.

- **P4a - AV1 excluded from the probe (scope cut, honest).** Raw AV1 has no Annex-B/AUD framing;
  per-frame splitting needs OBU parsing or an IVF mux we don't parse yet. Tiers 1/4b are therefore
  unreachable: the probe advertises only h264/hevc (nvenc/qsv/amf/libx264) + mjpeg, and
  `DecodeAV1` is never advertised (a future AV1-encoding peer must not target us). Revisit with
  the P8 vendor-SDK pass. The §3.2 matrix itself is unchanged.
- **P4b - AU framing = inserted AUDs + repeated parameter sets.** Encode children emit
  `h264_metadata|hevc_metadata=aud=insert,dump_extra=freq=keyframe`: the stdout stream splits at
  AUD start codes (pure splitter, `ausplit.go`), keyframe/config classified from NAL types, and
  every keyframe carries SPS/PPS - decoder restart / mid-stream join needs no side channel.
  Honest cost: an AU is complete only when the NEXT AUD arrives → ~1 frame extra latency on the
  encode path (within the §3.2 pipe-path budget). MJPEG self-frames on SOI/EOI (zero extra).
  Decode children read with `-probesize 32 -analyzeduration 0` (format is forced; probing must
  never stall a live pipe - found the hard way).
- **P4c - Encode/decode children live in `internal/mediapipe`, medialink stays pure.**
  `Options.Encoder/Decoder` factory seams (`pipeline.go`); nil = P1–P3 passthrough; route tests
  run with fakes, no ffmpeg. Children mirror webcam/capture.go supervision (KillTree + job
  object, capped 1→10 s backoff). Decode side walks probed hwaccels (cuda→qsv→d3d11va→dxva2→sw);
  a child dying unframed within 3 s demotes one tier. PTS/TC map input→output through a FIFO
  (valid because every tier runs `-bf 0`).
- **P4d - PLI = rate-limited encoder-child restart.** ffmpeg's CLI has no live force-IDR channel,
  so `RequestKeyframe` respawns the child (fresh child opens with IDR+params) - skipped when a
  keyframe is <500 ms old, min 2 s between kicks. Cost ~100 ms hole, covered by the receiver's
  waitKey policy. A vendor-SDK encode session (P8) does this properly.
- **P4e - Jitter buffer deadline domain is self-calibrating.** Release at
  `PTS + base + depth·interval` where `base` = windowed min transit (4 × 2 s buckets) - works
  even when the clocks share no epoch (monotonic tier), and the §3.3 grow/decay + late-rate
  definitions apply unchanged. Keyframe policy: unrecovered gap at deadline → drop-to-keyframe +
  PLI (250 ms rate limit); stale head with a buffered resync point → catch-up skip; hard memory
  guard 120 frames / 256 MiB. Audio buffering stays out of P4 (video-only; audio routes ride the
  P1 path).
- **P4f - Bitrate rides the Offer (`br`, kbps, additive).** The requester budgets the route
  (config default 20 Mbps, §3.1-scaled per-resolution encoder default when absent); LL settings
  per tier: x264 zerolatency/superfast, nvenc p1+ull+delay 0, qsv async_depth 1, amf
  ultralowlatency; all `-bf 0`, 2 s GOP, VBV = rate/2.
- **P4g - Same-PC guard at the creation surface.** A medialink route is never created toward a
  peer on the same machine (`peerIsLocalhost`: link address is loopback/local-interface) - the
  Spout sender is already visible there (§3 "never encode locally"). Raw-codec routes (no caps
  overlap → P1 echo) stay passthrough: no encode child, decoder bypasses foreign codecs.
- **P4h - Route surface (`internal/mediaroute`).** Opt-in `MediaLink.ShareVideo` scans local
  Spout senders (2 s) → `RegisterSource("spout:<name>")`; received routes materialize as local
  senders `"rave-mate link <source>"` (prefix excluded from sharing - loop guard). Receives
  register a per-request sink (`UnregisterSink` added; grace-cleaned when the route never comes
  up). Preferred codec narrows the offered decode caps only when the target adverts a matching
  encoder - never into a broken raw fallback. Webcam: `SetRouter` + capture fan-out (shallow
  frame copies per tap - route stamping never mutates a shared frame) resolves §13's P4 seam.
- **P4i - Telemetry (§7).** RouteStat gains encoder/tier/software (CPU warning surfaced on BOTH
  ends), keyframes, rolling bitrate, `JB` (depth/late-rate/policy-drops/grows/decays/PLIs) and
  `Pipe` (out-fps/hwaccel/restarts) - rendered in the Peers tab; Settings ▸ Streaming & remote
  gains the "Media link video" card (share/codec/bitrate/sw-only).

**Done in P4:** mediapipe (probe/cache, encode+decode children, AU splitters, LL argv), medialink
jitter buffer + factory seams + negotiation wiring + OfferRoute/CloseRoute/SetCodecCaps/
UnregisterSink, Spout receiver shim (`rave_spout_recv` + registry queries) + videoshare
FrameReceiver, mediaroute, webcam fan-out, config (additive, v24 kept), app + UI wiring.
Tests: probe parsers (fixtures), argv shapes, AU splitters (torn feed/join/config/flush), live
ffmpeg encode→decode pipe pair (skips cleanly when absent), JB grow/decay/keyframe-policy/
retransmit-fill/catch-up/hard-cap, fake-children route wiring (tier-2 hw, sw-only→tier 4 +
Software flag both ends, raw echo passthrough), mediaroute share/receive/guards/cleanup +
preferDecoders, webcam taps, UI formatters.

**Deferred / remaining (P4 accept §8 - needs the 2-instance live rig, not doable in-worktree):**
1080p60 OBS→PC2→Resolume glass-to-glass ≤ 60 ms (clapper-measured), 30-min soak with all drops
counted, bitrate-within-budget check, buffer auto-grow under injected jitter, real NVENC/AMF
probe results on both PCs, Spout receiver against OBS/Resolume senders (SDK ReceiveImage
NULL-pixel connect contract verified live).
