# Native MF hardware encode (internal/mfenc + native/zigenc)

The preferred H.264 engine on Windows for medialink video routes. No ffmpeg child, no
multi-GB/s rawvideo stdin pipe - frames go to the GPU once and come back as annex-B AUs.

**Architecture (since the 4K60 crash epic): the MF pipeline runs in a dedicated
per-adapter Zig subprocess** (`native/zigenc` → `rave-mate-enc.exe`, no cgo). The media
child talks to it over a per-session shared-memory ring + named events (data plane) and
newline-JSON stdio (control plane: `open`/`close`/`bitrate`/`idr`/`quit`, session IDs -
N sessions multiplex in one child). Rationale: vendor MFTs can raise access violations
on THEIR OWN worker threads during open (the 4K60 field crash) - no in-process guard can
catch that (VEH is process-wide but longjmp context is thread-local; Go takes the fault
as fatal). Process boundary = the containment. A driver AV kills only the encoder child;
the supervisor (`internal/mfenc/procparent_windows.go`) reports which sessions died,
restarts with bounded backoff (0.5s→2s, RestartPolicy plug for the Phase-2 governor),
re-places the sessions + forces an IDR - the route survives; frames during recovery are
dropped, never errored. Only after 3 consecutive fast-fails is the (adapter, geometry)
tuple poisoned and the child refused (crash-loop guard) - routes then land on the ffmpeg
engine via the existing spec-rewrite (transient safety net, not the goal).

The cgo shim (`mf_shim_windows.cpp` + `mfenc_windows.go` Encoder) stays as LAB BENCH +
in-proc reference (tests exercise both); the production path is the Zig child
(`mediapipe/mf_bridge.go` → `mfenc.OpenProcSession`).

**Child delivery (field #166): the exe is EMBEDDED in rave-mate.exe** (`encembed` tag;
`build-zig.sh` stages it into `internal/mfenc/embedded/`, CI Windows builds carry it) and
extracted on demand to `%LocalAppData%/rave-mate/proc/rave-mate-enc-<hash>.exe`
(content-hash-stamped, atomic write+rename, stale versions pruned) - the self-updater
swaps only the main exe, so sidecar-only shipping never reaches self-updated installs,
and the hash stamp kills main-exe/child version skew. Resolution order:
`RAVE_MATE_ENC_EXE` → staged embed → sidecar next to the exe (NSIS) → repo zig-out (dev).

**Advertisement gate:** `mfenc.ChildAvailable()` (not `Available()`) gates every
h264_mf_native advertisement (app caps, mfOnly, settings list, mediapipe engine): cgo HW
probe AND child exe resolvable AND it spawns + answers hello within 3s. A rig without a
working child negotiates as if the native engine does not exist.

**Crash forensics:** featurehost logs a consolidated `feature crash forensics` entry on
child death - latched fatal header (`panic:`/`fatal error:`/`Exception 0x`) + bounded
last-64-line stderr tail - because per-line streaming lets goroutine dumps evict the
header from the log ring.

Per-session telemetry (Phase-2 governor inputs): submit→AU latency p50/p99, queue
depth, child CPU%, ring drops - surfaced through `medialink.PipelineStats`. Measured on
NVIDIA RTX (dev box): 1080p sustained submit 335 fps, p50 5.4 ms / p99 22 ms submit→AU;
4K60/50Mbps/gop120 (the field crash tuple) encodes through the child.

Hard-won contract lessons (cost real debugging, keep them):
- `IID_IMFMediaEventGenerator` ends `…1e7d`. The hand-rolled `…1e7b` variant gets
  E_NOINTERFACE → silently forces sync drive of an async MFT → E_UNEXPECTED storms +
  lost outputs. (Second time this exact typo bit this codebase.)
- NEVER resubmit a pool NV12 sample the encoder still queues: in-flight cap =
  NVPOOL-1 in feed (both Zig + C++ now). Reuse corrupts the MFT input queue.
- The encoder child calls `timeBeginPeriod(1)` - without it Sleep(1) waits ~15.6 ms
  and throughput collapses (Go's runtime sets this for the parent, Zig does not).
- MF pts are 100ns-quantized: latency maps must key on quantized pts.
- Some vendor MFTs NEVER deliver METransformDrainComplete: drain also exits when
  fed_n == out_n (every submitted frame returned) or every close burns the full
  2 s FEED_WAIT_MS.
- Teardown discipline (review CRITICAL): the parent NEVER unmaps a session's shm
  until its pump goroutine has exited (Close blocks on pumpDone, no timeout
  fallback), and the pump never blocks on AU delivery once teardown began - an
  abandoned Output() consumer must cost drops, not a UAF. Child close runs on a
  detached closer thread (session independence) and releases view + mapping + all
  event handles on every exit path.

## Pipeline

```
RGBA []byte ── UpdateSubresource ──> D3D11 texture (R8G8B8A8, or B8G8R8A8+swizzle when
    the VP rejects RGBA input - probed via CheckVideoProcessorFormat)
  ── ID3D11VideoContext::VideoProcessorBlt ──> NV12 texture pool (8, round-robin;
    CSC BT.709 studio + optional scale, all on GPU)
  ── MFCreateDXGISurfaceBuffer sample ──> hardware encoder MFT (MFTEnumEx
    MFT_ENUM_FLAG_HARDWARE; NVENC/AMF/QSV silicon) ──> annex-B H.264 AUs
```

- **Not** the VideoProcessorMFT (XVP): its D3D sample negotiation is vendor-flaky
  (E_INVALIDARG on ProcessOutput with both caller and provider samples on NVIDIA).
  The D3D11 Video API (`VideoProcessorBlt`) is the layer below it - deterministic.
- Encoder MFT runs **async** (IMFMediaEventGenerator: NeedInput credits, ONE
  ProcessOutput per HaveOutput event - more = E_UNEXPECTED); sync MFTs are also
  handled (drain-on-NOTACCEPTING) for vendor variance.
- ICodecAPI: CBR, mean bitrate, ~2 s GOP, no B-frames, low-latency;
  `AVEncVideoForceKeyFrame` = **live forced IDR** (KeyframeSource without the ffmpeg
  child's respawn + stream hole).
- COM/MF affinity: one LockOSThread goroutine per encoder owns every call (MTA).
- GUID hygiene: interface IIDs via `__uuidof` (hand-rolled IIDs caused two real bugs:
  wrong IMFMediaEventGenerator IID silently forced sync mode; wrong
  AVEncVideoForceKeyFrame GUID no-opped forced IDRs). Attribute/format GUIDs are
  TU-local DEFINE_GUIDs (INITGUID) so MinGW import-lib gaps can't break linking.

## Integration (internal/mediapipe/mf_bridge.go)

`Factories` dispatches on the NEGOTIATED ENCODER NAME (`encodeEngine`), not the codec:
only `medialink.EncoderMFNative` (`"h264_mf_native"`) runs this engine. That name is
advertised only when `mfenc.Available()`, and the §3.2 matrix ranks it FIRST in the H.264
tier, so it wins over the pipe-fed tiers whenever the peer decodes H.264 (see
`medialink.Negotiate`). Keying on `Codec == CodecH264` was a bug: a negotiated `libx264`
ran here on hardware while the Answer + route stats reported tier-4 software, and `SWOnly`
stopped forcing software at all.

An open failure falls back to the ffmpeg child, but the SPEC IS REWRITTEN to a probed
ffmpeg H.264 encoder first (hardware preferred, `libx264` last) - the peer was answered
H.264, so the wire codec must stay H.264. One loud warn names the substitution.

`spec.MaxHeight` rides the same Blt (4K→1080p costs nothing extra); `MaxHeight 0` = native
res - a 4K source encodes at full 4K on the silicon. PTS is preserved by MF (no ptsq remap);
TC carried FIFO (no B-frames = output order == input order).

**Device selection (WP-3).** `mfenc.NewOn(adapterLUID, …)` pins the pipeline to one GPU:
`mf_enc_open`'s formerly dead `codec` parameter is now `adapterLuid` (DXGI `AdapterLuid`
packed `HighPart<<32|LowPart`, 0 = default adapter). The shim resolves it via
`CreateDXGIFactory1`/`EnumAdapters1`, creates the device with
`D3D11CreateDevice(pAdapter, D3D_DRIVER_TYPE_UNKNOWN, …)` - mandatory when an adapter is
passed - and then picks the encoder MFT for THAT adapter: candidates whose friendly name
carries the adapter's vendor tag (from its PCI VendorID) are tried first, and
`MFT_MESSAGE_SET_D3D_MANAGER` is the hard gate (an MFT that refuses this adapter's device
manager is skipped instead of failing the pipeline). No more blind `acts[0]`. An adapter that
cannot host the pipeline degrades to the default one INSIDE the shim, so a device preference
can never kill a route. The LUID comes from `config.MediaLink.EncoderDevice` via
`encoderscan.ResolveDevice` → `EncodeSpec.DeviceLUID` → `encoderscan.LUIDInt64`.

This is the ACCURATE device path: ffmpeg has `-gpu` (NVENC) and `-qsv_device` (QSV) but no
device option for AMF at all, and NVENC's ordinal is a CUDA ordinal that need not match the
DXGI one. Here the adapter is identified by LUID.

**4K60 open-crash hardening (P0 field fix).** Build 157 crashed the media child 100% inside
`mf_enc_open` at 3840×2160@60 (c0000005 in a vendor MFT; goroutine locked-to-thread, exit 2)
on a rig with OBS live + a Parsec Virtual Display Adapter. Root-cause class: luid 0 used
blind adapter 0 (`D3D11CreateDevice(NULL, HARDWARE)` follows the primary display - on such
rigs a device no encoder silicon lives on), and a vendor MFT handed a foreign device's
manager via `SET_D3D_MANAGER` can FAULT in type negotiation instead of refusing the message -
an AV no HRESULT check catches, killing the child before the ffmpeg fallback runs
(`Available()` passes: the probe never binds a device manager). Defenses, all in the shim:

- `pickDefaultAdapter`: luid 0 picks a DELIBERATE default - first non-software adapter of a
  known encode vendor (NVIDIA/AMD/Intel), else first non-software - never blind adapter 0.
  Also sets the vendor hint for default opens (was pinned-only).
- Video-capability gate: `openDevice` QIs `ID3D11VideoDevice` + creates the VP enumerator
  for the route geometry BEFORE any MFT is offered the device manager; an unusable adapter
  degrades to the system default, then fails clean.
- Cross-vendor guard: an MFT whose friendly name carries a DIFFERENT known vendor than the
  device's adapter is never offered the manager (`vendorMismatch`); rejected activations get
  `IMFActivate::ShutdownObject`.
- Driver-fault guard: a scoped VEH + `setjmp`/`longjmp` (Frame=0, no unwind through driver
  frames) wraps `mf_shim_available` and `mf_enc_open`. Any AV-class fault returns a clean
  error to Go ("driver fault during open"), LEAKS the partial pipeline (Release into a
  faulted driver can fault again) and POISONS the shim - later opens fast-fail, the route
  degrades to the ffmpeg child instead of dying every attempt.
- CPU output buffers scale with frame area (4K IDR AUs exceed the old 1 MB floor).

Env knobs: `RAVE_MATE_MFENC_OPEN_FAIL` (Go-side kill-switch: every native open fails clean →
ffmpeg substitution; also the degrade-path test hook), `RAVE_MATE_MFENC_FAULT_INJECT`
(deliberate AV inside open; read at CRT startup - set BEFORE process launch; guard-path test
hook). Tests: `TestOpenCrashTuple4K60` (exact field tuple), `TestOpenSizeTable` (odd/edge
geometry sweep), `TestFaultGuardSubprocess` (real AV → clean error + poison, in a child test
process), `TestNativeOpenFailDegradesToFfmpeg` (mediapipe: substitution runs a LIVE ffmpeg
H.264 child, wire codec unchanged).

Verified on hardware (NVIDIA H.264 Encoder MFT): 720p60 60/60 AUs with SPS+IDR first AU
+ mid-stream forced IDR (cgo bench); via the Zig child: 1080p60 60/60 with live bitrate
retarget + forced IDR, exact 4K60 field tuple, mid-route child AV → restart → SAME
session resumes (route continuity proven by execution), startup crash-loop → clean
refusal. `go test ./internal/mfenc/ ./internal/mediapipe/ -run 'TestEncode|TestMFBridge|TestProc'`.

## Fallback rules

| Condition | Engine |
|---|---|
| Negotiated encoder == `medialink.EncoderMFNative` (only advertised when `Available()`) | mfenc (native) |
| mfenc open/feed failure | ffmpeg child on a probed H.264 encoder (spec rewritten, warned) |
| Any other encoder name - incl. ffmpeg's own `h264_mf`, `h264_nvenc`, `libx264` | ffmpeg child |
| HEVC / AV1 / MJPEG | ffmpeg child |
| non-Windows / no cgo | ffmpeg child (stub `Available()=false`, name never advertised) |
| `MediaLink.SWOnly` | native name filtered out of the advertisement → software tier really is software |

## Zero-copy source (zigmedia increment 1, flag OFF by default)

**Landed.** A Spout sender IS a DX11 shared texture, so the child opens it on its own device
and hands it to the video processor as the INPUT VIEW - the GPU→CPU readback, the pooled host
frame buffer and the SHM frame slot all disappear. Go passes two scalars (share handle + DXGI
format) and never a pixel. Spec + risk register: `.devnotes/ZIGMEDIA_DESIGN.md`.

```
Spout sender's shared texture (GPU)
  │  handle + format resolved ONCE by Go (registry read, no GL)
  ▼
rave-mate-enc.exe session thread: pace(fps) → acquire mutex → VideoProcessorBlt (CSC+scale,
  NV12 pool) → release → MFT submit → annex-B AU → AU ring (SHM) → SetEvent(-a)
  ▼
Go: pump AUs (~100 KB/frame) → wire crypto → socket
```

- **Gate:** `config.MediaLink.zigCapture` (tri-state, default OFF) or
  `RAVE_MATE_ZIGMEDIA_CAPTURE=1|0`. Requested only when the source implements
  `medialink.ZeroCopySource` with a non-zero handle matching the negotiated geometry, the child
  answers `hello.ver >= 2`, and the sender is not pinned to the readback path.
- **Protocol:** `open` gains `src`/`sh`/`sfmt`/`sname`/`cap_n`/`cap_d`/`ring_kb`/`pts0`; header
  v2 adds child-written capture counters at 64..111; new `srcgone` event. A `spout` session's
  SHM is header + AU ring ONLY (no frame slot) and the ring is bitrate-derived
  (`clamp(kbps/16, 4 MiB, 16 MiB)`), so a sender resize costs zero SHM realloc.
- **Fallback ladder, never a dead route:** open-side refusal → `ErrZeroCopyRefused` → the same
  route reopens on the readback path with one WARN; mid-route `srcgone`/staleness → reopen with
  a freshly resolved handle, bounded at 3 attempts, then the sender is pinned to the readback
  path and the AU stream ends so the route re-establishes there.
- **R1, the worst failure mode:** after a sender restart `OpenSharedResource` can still succeed
  on a DEAD texture - frames look healthy and the picture is frozen. Two detectors (`spoutCheck`):
  a changed share handle on the 2 s registry rescan, and a capture clock that stopped while no
  new frames were counted.
- **`Encode()` is refused** on a zero-copy session: there is no frame slot to write into.
- Route panels now render the whole `PipelineStats` block (submit→AU p50/p99, queue depth, child
  CPU) plus the capture counters and a downgrade count. `EncBusyMs` replaces the percentiles on
  a zero-copy route, where the parent submits nothing and they are structurally empty.

Measured on this dev box (NVIDIA), real sender in a second process:

| | zero-copy | readback control |
|---|---|---|
| host RSS, 4K60 @ 50 Mbps | 29 → 30 MB | 206 → 207 MB |
| encoder-child RSS | 69 → 70 MB | 170 → 170 MB |
| AUs / 45 s | 2880 | 2864 |
| capture cost | 0.28 ms/frame | GPU→CPU readback + 33 MB memcpy |

`capFlags=0x5` on this rig = zero-copy live + Spout's NAMED access mutex. Orientation and
colour are gated by DECODING the bitstream (a red-top/blue-bottom probe), because the readback
path flips on the CPU and a silently inverted zero-copy path passes every counter.

### Adapter affinity (zigmedia increment 3, flag OFF by default)

`OpenSharedResource` only works on the adapter that CREATED the texture, so a sender produced by
an app on GPU B is invisible to an encoder child pinned to GPU A - the child refuses with
`open_shared` and the route pays the full readback. Measured on the dev rig: with two adapters
present, one accepts a sender the other refuses.

Nothing in DXGI answers "which adapter owns this share handle", so resolution is a bounded PROBE:
on a source-side refusal, try the other adapters once, first success wins, cache the answer per
sender (the cached adapter is probed first next time, so a second route pays nothing).

- **Gate:** `config.MediaLink.zigAffinity` / `RAVE_MATE_ZIGMEDIA_AFFINITY=1|0`, default OFF.
- **Never silently move adapters** (R7): mediapipe offers candidates ONLY when the gate is on and
  `EncodeSpec.Device()` resolves nothing. A pinned device - or one the governor chose via
  `avoid-busiest` - is policy and outranks the optimisation. Every move logs once and renders as
  "adapter re-placed" on the route panel (`PipelineStats.AdapterMoved`).
- **Bounded:** one attempt per candidate; the NEGATIVE is cached so a hopeless sender sweeps once;
  a non-source failure (poisoned tuple, crash-looping child) stops the loop instead of spawning a
  child per adapter; a single-adapter host gets no candidates at all.

Increment 3's other two items were measured and deliberately NOT built - see
`.devnotes/ZIGMEDIA_INC3_STATUS.md`. In short: mutex contention with 4 sessions on one sender is
zero at 720p60 AND 4K60 (the 4K cost is encoder saturation, which a shared capture copy does not
address and which would re-add 33 MB VRAM per adapter+sender), and a duplicate frame on a static
sender costs 49 bytes - 0.12% of a 20 Mbps route - while keeping the peer's jitter buffer fed.
Frame-new gating is also not reachable: this SpoutLibrary pairing returns junk from
`GetSenderFrame` through a metadata-only receiver (the same late-vtable skew already documented
for `GetSenderWidth`/`GetSenderHeight`).

Still open: 7-day soak before the flag defaults on, and the 2-PC pass (§13.1 of the design - the
single-box soak does not prove the wire, and the sender PC's pointer lag is only observable on a
real rig).

## Native decode (zigmedia increment 2, flag OFF by default)

**Landed.** The receive side is the same child, opposite direction: compressed AUs ride an
INBOUND shared-memory ring in, an MF decoder MFT decodes on the GPU, and the video processor
blits each frame straight into the destination video-share sender's shared texture. That deletes
the ffmpeg decode child's 33 MB-per-4K-frame stdout pipe AND the second upload Spout did on the
way back out. Status + measurements: `.devnotes/ZIGMEDIA_INC2_STATUS.md`.

```
wire AU → jitterbuf (Go) → inbound AU ring (SHM) → SetEvent(-f)
  ▼
rave-mate-enc.exe session thread: decoder MFT (D3D11) → NV12 surface → acquire the sender's
  mutex → VideoProcessorBlt into ITS shared texture → Flush → release → SetEvent(-c)
  ▼
external receivers (OBS/Resolume) copy the texture as usual
```

- **Gate:** `config.MediaLink.zigDecode` (tri-state, default OFF) or
  `RAVE_MATE_ZIGMEDIA_DECODE=1|0`. Requested only for H.264/HEVC, when the sink implements
  `medialink.ZeroCopySink` with a non-zero handle, the child answers `hello.ver >= 3`, and the
  destination is not pinned to the frame path.
- **Who owns the sender: GO.** A decoder cannot create one, so `mediaroute.openSpoutSink` opens
  it EAGERLY and exposes the handle. `SPOUTLIBRARY` has no `CreateSender`, so the shim publishes
  one zeroed frame to force the texture and reads the handle + real format back out of the
  registry (`GetSenderInfo`). One `w*h*4` write per ROUTE, into the pooled flip buffer.
- **Protocol:** `open` gains `dir`/`codec`/`dsh`/`dfmt`/`dname`/`in_ring_kb`; header 128..207 is
  the ring-counter block a second time plus decode telemetry; new `dstgone` event. A `dec`
  session's SHM is header + inbound ring ONLY, bitrate-derived like the outbound one.
- **Fallback ladder:** open-side refusal → `ErrDecodeRefused` → the route runs the ffmpeg decode
  child with one WARN; mid-route `dstgone`/staleness → reopen with a freshly resolved handle
  (both ring heads reset - stale bitstream is useless to a fresh decoder), bounded at 3 attempts,
  then the destination is pinned to the frame path.
- **Frozen-destination oracle** (`decCheck`): a changed destination handle is always a recycle,
  and "AUs arriving but nothing published for 3 s" is a recycle. An IDLE route (no AUs, nothing
  published) is healthy - treating silence alone as a fault would churn reopens.
- **Two load-bearing details.** A write into a texture another PROCESS reads needs an explicit
  `Flush` before the mutex is released (a named CPU mutex carries none, unlike
  `IDXGIKeyedMutex.ReleaseSync`) - without it the receiver reads pre-blit content, i.e. a blank
  picture with zero errors in every counter. And `VideoProcessorSetStreamSourceRect` must be set:
  a hardware decoder's NV12 surface is 16-row aligned (640x368 for 640x360) and the VP would
  otherwise squash those rows into the output.
- **`MF_SA_D3D11_AWARE` + MFT-provided samples are hard gates**: system-memory decoder output
  would mean uploading NV12 by hand, which is the host frame plane this increment removes, so it
  downgrades cleanly (`sw_decode_unsupported`).

Measured on this dev box (NVIDIA), real Spout sender, real child, 640x360:

| | value |
|---|---|
| decoder bound | "Microsoft H264 Video Decoder MFT" (D3D11-aware, sync) |
| AUs in / frames published | 40 / 40 |
| decode + publish cost | 0.8-1.0 ms per AU |
| ring drops / decode errors / mutex timeouts | 0 / 0 / 0 |
| `decFlags` | `0x5` = live + Spout's NAMED access mutex |
| destination texture read-back | top r=255 b=0, bottom r=1 b=255 (correct rows + channels) |

The picture is verified by a GPU read-back INSIDE the child (`RAVE_MATE_MFDEC_PROBE_BANDS=1`,
off by default): Spout's own receive side reports success and returns all-zero pixels for a
foreign-device write on this rig - and for an ordinary `SendImage` publish from another process
too - so it is not a usable oracle here.

Still open: OBS/Resolume verification of a natively decoded route (the design makes it a
precondition for flipping the flag), the 7-day soak, a 4K60 receive soak, a real end-to-end
route through `jitterbuf`, and HEVC.
