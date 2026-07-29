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
- **Drive mode comes from `MF_TRANSFORM_ASYNC`, never from a successful
  `QI(IMFMediaEventGenerator)`.** Sync MFTs expose that interface too; taking its event
  generator and then waiting for `METransformNeedInput` blocks the whole per-frame budget
  and the parent reports `encode timeout`. ffmpeg's `mf_unlock_async` reads the same
  attribute for the same reason. The unlock is verified after setting it, not assumed.
- `IID_IMFMediaEventGenerator` ends `…1e7d`. The hand-rolled `…1e7b` variant gets
  E_NOINTERFACE → silently forces sync drive of an async MFT → E_UNEXPECTED storms +
  lost outputs. (Second time this exact typo bit this codebase.) Both this IID and
  `MF_TRANSFORM_ASYNC` are now pinned against the SDK by a Zig test.
- **ONE D3D11 device + `IMFDXGIDeviceManager` per CHILD, not per session.** The child is
  already per-adapter. A device per session meant two device managers on one adapter in one
  process, which no reference MF pipeline does (OBS, ffmpeg and the Media Engine all share
  one per adapter) and which AMF-backed MFTs reject: on AMD a single session is clean and a
  second one in the same child wedges it. Legal because the device carries
  `D3D11_CREATE_DEVICE_VIDEO_SUPPORT` + `SetMultithreadProtected(TRUE)`.
- **The child's per-frame wait must be far below the parent's `encodeWait`.** Both sat at
  2 s, so the parent's deadline expired at the same instant the child's did and a merely
  SATURATED encoder ended the route. Now `SUBMIT_WAIT_MS` = 250 ms returning `RC_BUSY`
  (counted as a drop, surfaced in `PipelineStats.Dropped`) against a 4 s parent deadline:
  saturation sheds frames, it does not kill routes.
- **NV12 pool textures are bound `RENDER_TARGET | SHADER_RESOURCE`.** AMD's and Intel's
  encoder MFTs create an SRV over the input surface; NVENC consumes the texture directly,
  so render-target-only worked here and only here. Falls back to render-target-only if a
  driver refuses the pair.
- **Teardown order is the documented one**: `FLUSH` → `NOTIFY_END_STREAMING` →
  `SET_D3D_MANAGER(0)` → `Release(IMFTransform)` → `IMFActivate::ShutdownObject`. The old
  short order dropped our last device references while the vendor MFT still held the device
  manager.
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

## Vendor portability matrix

The engine resolves every vendor-variable decision from the MFT's OWN attributes; nothing is
assumed per vendor.

**VERIFIED BY EXECUTION: NVIDIA and AMD only.** Everything else in this table is argued from the
API contract, not measured - treat it accordingly.

| Tier | Drive mode | Device manager | Input samples | Status |
|---|---|---|---|---|
| NVIDIA (`NVIDIA H.264 Encoder MFT`) | `async` (measured, from `MF_TRANSFORM_ASYNC`) | required | DXGI surface buffers over the NV12 pool | **VERIFIED BY EXECUTION** (dev box, 2× RTX 3060): 1080p60, exact 4K60 tuple, 2 concurrent sessions, 4K60+720p concurrent, zero-copy live with decoded-bitstream picture check |
| AMD (`AMDh264Encoder`, Radeon RX 7900 XTX) | `async` (measured: `async_attr=true`) | required | same | **VERIFIED BY EXECUTION** (field, 2026-07-27): the exact two-route repro - 4K60 spout + concurrent 720p webcam - ran 60+ s with zero failures, where it previously died in ~2.2 s with `0xc0000005` 3/3. `device=child(shared=true)` on both sessions |
| Intel (QSV MFT) | reported per open, not assumed | required | same | **ARGUED ONLY** - no Intel rig. The code path is the same one AMD and NVIDIA take; nothing about it is Intel-specific, but that is reasoning, not evidence |
| Software MF (`H264 Encoder MFT`) | `sync` (measured) | **never offered** | packed system-memory NV12 via a STAGING readback | **VERIFIED BY EXECUTION** (dev box, forced with `RAVE_MATE_MFENC_SW=1`): single + 2 concurrent sessions, real bitstream at 8.5 kB/frame |
| WARP fallback (software tier with no hardware video device) | inherits the software tier | never offered | same | **ARGUED ONLY** - this box always has a hardware video device, so the rung has never executed |

- The software tier is a FIRST-CLASS rung, not a footnote: it is what a box with no usable
  hardware MFT - or one whose hardware MFT the failure ledger poisoned - encodes on. It keeps the
  D3D11 video processor for CSC + scale and falls back to a **WARP** device when no hardware
  adapter can host a video device at all, so the rung survives a headless / virtual-display-only
  rig. If even that fails, mediapipe substitutes an ffmpeg encoder.
- The software tier is NOT added to the advertisement. `Available()`/`ChildAvailable()` still gate
  on hardware, so a box with an ffmpeg hardware encoder but no hardware MFT keeps negotiating the
  better engine. Software MF is a within-session last rung, chosen at open time.
- Env knobs: `RAVE_MATE_MFENC_SW=0` hardware only (prove a hardware regression), `=1` software
  only (test the rung on a box that has silicon); `RAVE_MATE_MFENC_DEVICE=session` restores the
  per-session device for a live A/B of the concurrent-session theory.

## Concurrent sessions (the AMD 0xc0000005)

Field evidence, 3/3 correlated: a single AMD encode session is clean (real content, zero drops,
no fault) and a SECOND session opened in the same child wedges it - no `METransformNeedInput`,
the parent times out, the route ends and the child later takes an access violation. Every gate in
this package was single-session, which is why NVIDIA never showed it.

Two independent causes were addressed, because the evidence does not yet separate them:

1. **Two device managers on one adapter** (see the contract lessons) - fixed by the per-child
   device, A/B-able with `RAVE_MATE_MFENC_DEVICE`.
2. **Encoder saturation** - the failing runs always had a 4K60 50 Mbps route live, which is close
   to a Ryzen iGPU's VCN ceiling. The colliding timeout budgets turned that into a dead route;
   now it is counted drops.

**Field outcome (2026-07-27): FIXED.** The exact repro - 4K60 spout route plus a concurrent 720p
webcam route on a Radeon RX 7900 XTX - ran 60+ seconds with zero failures, no child exit and no
access violation, against 3/3 deaths in ~2.2 s before. Telemetry confirmed the active path:
`device=child(shared=true)` on both sessions and `async_attr=true`.

**The two fixes remain JOINTLY confirmed, not individually isolated.** Selecting the old
per-session device needs `RAVE_MATE_MFENC_DEVICE=session`, i.e. a process launch with custom env on
the target machine, and the AMD rig has no Go toolchain and no remote-exec - so
`TestProcTwoSessionsPerSessionDevice` cannot run there. We know the combination works; we do NOT
know whether the per-child device alone, or the saturation-budget change alone, would have been
enough. Do not describe either one as independently proven. Isolating them needs an AMD box with a
toolchain, or a user-run launch with that env var set.

`TestProcFieldTupleTwoSessions` (4K60 + 720p concurrent) is the same repro as a gate, and passes on
NVIDIA - where both device policies behave identically, so that box cannot distinguish them either.

## Crash attribution

A vendor AV happens inside a driver, often on its own worker thread: no Go stack, no reliable
stderr tail. The child therefore latches the stage it is about to enter into a shared-memory word
(`off_stage`, 112) with one relaxed store, so it is always on. The supervisor reads it after the
exit code and names the faulting call in the crash warning (`stageName`, `procfail_windows.go`).
The stage numbers are a cross-process contract, pinned by tests on BOTH sides. `feed()` errors are
no longer discarded either - they surface as an `encfail` event carrying the return code and the
stage, and on a zero-copy route they are reported as `encfail` rather than `srcgone` (which used
to spend the source-recycle budget and then pin a healthy sender to the readback path).

## Crash-loop safety net (the failure ledger)

`procfail_windows.go`. Keyed on **(adapter, encoder)** - the thing that is actually broken - and
the streak is measured **crash to crash**. The previous counter was measured from the child's last
SPAWN, and the supervisor respawns immediately after every crash, so a human-paced route retry
always reset it: the field log read `consecutive fails 1` on every route and poisoning could never
fire. It also only wrote poison entries for sessions still registered at crash time, so a
teardown-time AV poisoned nothing even at the limit. Both are fixed and covered by tests that fail
against the old behaviour.

Reset policy, explicit: a crash extends the streak inside `failWindow` (10 min) and starts a new
one outside it; `maxConsecFails` (3) poisons; a poisoned tuple clears ONLY on proof of health (a
session on it really delivered an AU) plus `forgetAfter` (30 min) of quiet - time alone never
clears it, because that re-arms a crash loop on a timer. `ResetPoison()` is the user action.

**A poisoned hardware tuple degrades to the software tier, not to no video.**

## Degrade never black-frames

A mid-route native failure used to END the route: the ffmpeg substitution only existed at OPEN
time, so the peer was left with a frozen picture while every counter still read healthy. The
bridge now substitutes a supervised ffmpeg H.264 encoder **in place** over the same inner source
(`mfBridge.substitute`); `Next()`, `RequestKeyframe()` and `PipeStats()` delegate to it, so the
consumer never learns the engine changed. This covers zero-copy routes too, where there is no feed
goroutine and the failure only shows up as the AU stream ending.

Every degrade carries a reason to the route panel: `PipelineStats.DegradeReason` (empty is the
only healthy value) plus `Drive` and `SoftwareEncode`. Saturation drops land in
`PipelineStats.Dropped`. Judging a route healthy by counters alone is exactly what hid the
original defect, so the concurrent + software gates assert **bytes per frame**, not just survival.

## Fallback rules

| Condition | Engine |
|---|---|
| Negotiated encoder == `medialink.EncoderMFNative` (only advertised when `Available()`) | mfenc (native) |
| No usable hardware MFT, or its (adapter, encoder) tuple is poisoned | mfenc **software MF tier** (still the native engine) |
| mfenc open/feed failure | ffmpeg child on a probed H.264 encoder (spec rewritten, warned) |
| mfenc failure MID-ROUTE | ffmpeg H.264 substituted in place on the SAME route (never a dead route) |
| Any other encoder name - incl. ffmpeg's own `h264_mf`, `h264_nvenc`, `libx264` | ffmpeg child |
| HEVC / AV1 / MJPEG | ffmpeg child |
| non-Windows / no cgo | ffmpeg child (stub `Available()=false`, name never advertised) |
| `MediaLink.SWOnly` | native name filtered out of the advertisement → software tier really is software |

## Zero-copy source (zigmedia increment 1, **DEFAULT since increment 5**)

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

- **Gate:** `config.MediaLink.zigCapture` (tri-state, **default ON** since increment 5; only an
  explicit `false` opts out) or `RAVE_MATE_ZIGMEDIA_CAPTURE=1|0`. The decision is PER SOURCE
  (`zcVerdict`): requested only when the source implements `medialink.ZeroCopySource` with a
  non-zero handle matching the negotiated geometry, the child answers `hello.ver >= 2`, and the
  sender is not pinned to the readback path. A source that CANNOT ever do zero-copy (webcam /
  DirectShow / non-Spout - there is no texture) takes the readback silently and is not counted as a
  downgrade; a source that COULD have qualified and did not gets one WARN naming the reason plus a
  counted downgrade, so a rig that always falls back is visible rather than mysteriously slow.
- **What promoted it** (`.devnotes/ZIGMEDIA_INC5_STATUS.md`): the fallback ladder carries real
  pixels at every rung and logs once; the content oracle (bytes/frame) and `DegradeReason` now
  reach BOTH route panels, so "healthy counters over a black stream" is a rendered number rather
  than an unsayable state; and the branches that had never executed anywhere -
  `IDXGIKeyedMutex` (R3), TYPELESS/exotic formats (R4), a restarted sender's changed handle (R1) -
  are gated by execution on hardware with the decoded PICTURE asserted.
- **Still open at the flip:** a 2-PC wire pass with the flag on, a 7-day soak, a restart/resize of
  a real sending app (OBS), a heterogeneous multi-GPU rig, and the sender-PC pointer-lag question
  (design §13.1 item 4). Containment: the child never polls faster than the negotiated fps, mutex
  acquires are bounded 1..4 ms, and the READBACK path acquires the same named mutex at the shared
  capture's rate - so this path contends no harder than the one it replaces.
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

### Adapter affinity (zigmedia increment 3, flag OFF by default - deliberately NOT promoted)

`OpenSharedResource` only works on the adapter that CREATED the texture, so a sender produced by
an app on GPU B is invisible to an encoder child pinned to GPU A - the child refuses with
`open_shared` and the route pays the full readback. Measured on the dev rig: with two adapters
present, one accepts a sender the other refuses.

Nothing in DXGI answers "which adapter owns this share handle", so resolution is a bounded PROBE:
on a source-side refusal, try the other adapters once, first success wins, cache the answer per
sender (the cached adapter is probed first next time, so a second route pays nothing).

- **Gate:** `config.MediaLink.zigAffinity` / `RAVE_MATE_ZIGMEDIA_AFFINITY=1|0`, default OFF. It stayed
  off through increment 5's flip: the re-place is live-verified only between two IDENTICAL GPUs
  (2x RTX 3060), so a heterogeneous iGPU+dGPU rig - where the re-placed adapter may have a much
  worse encoder or none - is unexercised. The cost of leaving it off is a VISIBLE downgrade to a
  working readback, and the refusal WARN now names this key when the host has more than one GPU and
  the refusal was `open_shared`, so it is a fixable message instead of a silent miss.
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

### Produce paths (zigmedia increment 4)

The produce side (webcam, deckcard, VRSL) publishes INTO Spout, so increment 1 already covers a
route CONSUMING it. Increment 4 was about how those pixels get there, and it ends up being two
defect fixes plus a measured refusal - details in `.devnotes/ZIGMEDIA_INC4_STATUS.md`.

- **Capture buffers are pooled + refcounted.** `webcam/framepipe` allocated a fresh full frame per
  capture (~250 MB/s of garbage at 1080p30). Buffers now come from videoshare's bounded pool via
  `videoshare.PixRef`, because one frame fans out to the preview sink AND N taps that each drop
  independently - the lifetime is a refcount, not a scope. At the pool ceiling the capture ALLOCATES
  (counted as `PoolMiss`) rather than dropping every frame, so a leaked reference degrades the
  optimisation instead of wedging a live camera.
- **The send-path flip is row-wise.** `RAVE_SPOUT_FLIP != 0` was one 4-byte `memcpy` per PIXEL:
  at 4K, vertical 11.33 → 2.16 ms/frame, horizontal 11.28 → 2.91 ms. `flip == 0` (default) costs no
  host pass at all. Gated by a byte-for-byte comparison against the old algorithm
  (`rave_spout_flip_rows`, 8 geometries × 4 modes) and, live, by `TestFlipLiveOrientation` - which is
  also the first thing in this repo to establish what each `RAVE_SPOUT_FLIP` mode actually does.
- **NOT built: a D3D11 publish path for the produce direction.** Two independent reasons. It is
  unreachable as specified (SPOUTLIBRARY can only create a sender's texture via `SendImage`/
  `SendTexture`, both of which need GL - which is why inc 2 publishes one zeroed frame to force the
  texture), so GL cannot leave this path, only the per-frame upload could. And that upload is
  already at hardware transfer speed: 0.70 ms at 720p, 0.98 ms at 1080p, 3.62 ms at 4K = 8.7 GB/s.
  Replacing it with host→SHM + `UpdateSubresource` + `Blt` adds a host copy (~3.2 ms at 4K) or, with
  the producer writing straight into SHM, ties - for the price of a third protocol direction.
- **NOT done: the `rave-mate-media.exe` rename.** 59 references across 25 files including two CI
  workflows and the NSIS installer, which cannot be verified from a dev box; zero functional gain.
  If you pick it up: `encExePath()`'s beside-the-exe rung must accept BOTH names, or a self-updated
  install (which replaces only `rave-mate.exe`) silently loses the native engine.

Still open after the increment-5 flip: the 7-day soak and the 2-PC pass (§13.1 of the design - the
single-box soak does not prove the wire, and the sender PC's pointer lag is only observable on a
real rig with a real sending app). The flip did not wait on those because every rung of the ladder
below carries real pixels and is now visible in the panel, and because the readback it replaces
spent the whole life of the vendored SpoutLibrary pairing returning BLACK frames.

## Native decode (zigmedia increment 2, **DEFAULT since increment 5**)

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

- **Gate:** `config.MediaLink.zigDecode` (tri-state, **default ON** since increment 5; only an
  explicit `false` opts out) or `RAVE_MATE_ZIGMEDIA_DECODE=1|0`. Requested only for H.264/HEVC, when
  the sink implements `medialink.ZeroCopySink` with a non-zero handle, the child answers
  `hello.ver >= 3`, and the destination is not pinned to the frame path.
- **What promoted it: a MEASUREMENT, not a soak.** On the field rig a 4K route's local republish
  delivered **~13.5 distinct frames/s while the source encoded at 37** - the CPU `SendImage` upload
  of 33 MB/frame is the capacity ceiling. Leaving this off preserved a measured 3x frame loss, so
  "off" was not the safe default it looked like. Design §10's "live-verify against OBS's Spout input
  and Resolume" is now discharged as far as an INDEPENDENT PROCESS with its own D3D11 device reading
  the published texture with correct row and channel order - the mechanism OBS uses, but not OBS.
- **Still open on the receive side:** no real end-to-end route (peer → jitterbuf → `mfDecoder`) has
  been driven - the live gate feeds `ProcDecSession` directly; no 4K60 receive soak; no HEVC
  bitstream decoded; and no TRUE hardware decoder MFT exists on the rig that verified it (the MS
  D3D11-aware software MFT carries the passing run). Each of those lands on a rung that keeps real
  pixels (open refusal → ffmpeg with one WARN; mid-route `dstgone`/staleness → recycle, then pin).
- **Falsifiability, since it is now a default:** `route decode telemetry` logs once per 10 s with
  `decode: native|ffmpeg`, `published`, `publishedFps` and `outFps`. Both paths report `PubFrames`
  through the same wrapper chain, so `publishedFps` beside `outFps` shows the frame-path ceiling
  directly - no probe tool needed, and a silent fallback cannot hide.
- **Instrument note for whoever finishes it.** Increment 2 recorded that Spout's own receive side
  "cannot see a foreign-device write on this rig" and abandoned the cross-process picture gate.
  That was wrong twice over: the P0 vtable work explained the first half (`ReceiveImage` dispatched
  to `ReceiveTexture`), and the second half was that the harness published ONCE. A single
  `SendImage` is not visible to another process's D3D11 device - the GL/DX interop write is not
  flushed until further GL work is submitted. Publishing continuously (as every real sender does)
  makes the read-back oracle work, which is how the gate reads
  `published bands: top r=255 b=0, bottom r=1 b=255` today. The product was never affected.
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

## Remote diagnosis (no toolchain on the target rig)

The AMD field box is a user's OBS machine: no repo, no Go toolchain, and the only access is the
app's ctl socket, which has no remote-exec. **None of the gates in `internal/mfenc` can be run
there.** So every fact needed to diagnose a run has to be in the LOG STREAM and the rendered stats,
on the HEALTHY path - a passing run that cannot name its own configuration proves nothing.

Emitted per encoder-child incarnation (`mfenc.Infof` → logbus `Info`):

```
mfenc: encoder child up (adapter 0x…) proto v3 device-policy=child sw-policy=auto
mfenc: session 1 open: encoder="…" drive=async async_attr=true tier=hardware
       device=child(shared=true) adapter=0x10540 "NVIDIA GeForce RTX 3060" requested=0x0
       ledger-fails=0
mfenc: encoder child open trace (adapter 0x…):      ← the child's own stage trace, ONCE per
  mfenc stage: bound … drive=async tier=hw aware=1     incarnation. Previously this reached a
  mfenc stage: drive=async async_attr=true evgen=true  log only on a CRASH.
```

- `async_attr` is the RAW `MF_TRANSFORM_ASYNC` value; `drive` is what was derived from it. Both are
  printed so a reader never has to trust the derivation.
- `device=child(shared=true)` names which device path a passing run actually exercised.
- `adapter=` is RESOLVED, `requested=` is what config asked for. A mismatch means the configured
  LUID no longer exists (LUIDs do not survive a driver reset); a MATCH plus a surprising GPU name
  means the config was right and something else was wrong. On the field box these matched
  (`0x163a8` = a Radeon RX 7900 XTX): there was NO stale-LUID problem - the anomaly was
  `ctl encoder-scan` omitting that adapter entirely, which was a bug in the scan's adapter
  rendering, not in the encode path. See `internal/encoderscan` (adapters are now listed from the
  DXGI enumeration, not from PDH utilization counters).

Per route, every 10 s (`route encode telemetry`), the **content oracle**:

```
engine=… tier=… drive=… capture=… device=… adapter=…
aus=300 bytesPerFrame=35810 kbps=8594 fps=30.0
busyDrops=0 encFails=0 dropped=0 ledgerFails=0 poisoned=false degraded=""
```

`bytesPerFrame` is the thing that separates a live picture from a black one; `OutFPS`
cannot (a black 4K route reported healthy on frame counters for 12 minutes). This is logged rather
than only rendered because the panel's counters have been observed frozen for 25 minutes on a
demonstrably live route.

### bytesPerFrame cannot see a FROZEN picture - only a black one

A 4K route republished ONE bit-identical frame for 48 minutes with `fps 58.5`, `capStaleMs 16`,
`dropped 0`, `encFails 0`, published frames climbing - and `bytesPerFrame` sitting at a healthy
3-5 kB, because periodic keyframes account for that on their own whether or not the picture moves.
(A *moving* 720p synthetic source measured 184 B/AU, so byte volume does not even order correctly.)
`capStaleMs`/`DecStaleMs` time OUR tick, not the content, and a frozen source still delivers frames
perfectly on time. `spoutCheck`'s R1 fires on a CHANGED share handle or a stopped capture clock; the
field had a stable handle and a healthy 59 fps.

The oracle that works is a CONTENT HASH - `internal/framedebug`, reporting the age of the last
CHANGE rather than of the last frame. Per route: `pubStalledMs` + `pubChanges` in
`route decode telemetry`, and "picture frozen Ns" on the route panel past 2 s.

To attribute a freeze to a STAGE, sample the sender's own texture:

```
rave-mate ctl frame-shot        out.png 8 "OBS_Spout"              # this machine
rave-mate ctl remote-frame-shot out.png 8 [@node] "OBS_SUS_Spout"  # the PEER's own sender
rave-mate ctl frame-shot        out.png 1 3400,2040,440,120 "…"    # FULL-RES crop
```

Answers `N grabs / M changed` **plus the peak fraction of the frame that moved** - because "it
changed" is not "it is moving". A 4K desktop capture whose only live element is a tray clock changes
several times a second at ~0.5% of frame and looks like a still image to a human and to Resolume; a
live 720p webcam on the same path moved 6.5%, and Resolume's own composition 50%. The verdict names
all three states: FROZEN, STATIC SOURCE with a small live element (under `framedebug.StaticFrac`),
or LIVE. **0 changed over >=3 grabs is FROZEN AT THE SOURCE** - a verdict
formed upstream of encode, network, decode and republish, so none of them can confound it. The
remote form runs on the peer and ships the PNG back: no physical access to the sending machine.

Two techniques that cracked the 48-minute case, worth reusing:

- **An in-frame OS clock is an in-band timestamp.** Captured desktop content carries the sending
  machine's tray clock; compare it to the peer's own log timestamps (`ctl remote-logs`). 11:05 in
  the picture against 23:19 in the logs is proof the CONTENT is old, and it needs no measurement of
  our own pipeline - which is why the crop is full-resolution, since a 4x downsample makes the clock
  unreadable.
- **Re-open, then re-read.** A frozen READ cannot survive a fresh open. If a brand-new route (new
  encoder child, new D3D11 device, new `OpenSharedResource`, new VP view) still reads the same
  content, the texture is stale, not the read.

Beware the inverse trap: measuring the republished output and concluding "the source must be static"
is circular, because that measurement sits downstream of the very capture under suspicion.

## The testcard: deterministic loss attribution end to end

Hashes say WHETHER the picture changed; the testcard (`internal/testcard`) says WHICH frames were
skipped, repeated or delayed. Every frame the generator publishes carries its own identity IN THE
PIXELS: seq + wall-ms timestamp + session id + target fps + a "generator overran" flag, packed as a
16x7 grid of large black/white cells with a CRC16. Cells are placed on a relative 48x27 lattice and
sampled at centers, so the grid survives H.264, video-range squeeze and OBS scaling the card to its
canvas (roundtrip-tested through blur + range squeeze + rescale). Damage fails the CRC - a wrong
seq is never returned as valid.

```
rave-mate ctl testcard start [WxH@fps]     # publishes Spout sender "rave-mate testcard" (default 1280x720@30)
rave-mate ctl remote-testcard [@node] start  # start it on the SENDING machine over the peer link
rave-mate ctl testcard stats               # generator ground truth + every verifier stage
rave-mate ctl testcard reset|stop
```

Any stage with CPU pixels self-detects the card (6 samples on non-card frames, free) and tallies:
exact **gaps** (skipped seqs), **dups** (freeze runs + worst length in frames), reorders, session
restarts, delivered seq/s, and latency **drift** = lastDelta − minDelta, which is offset-free (raw
deltas mix both machines' clocks). `frame-shot` on a card sender also decodes per-grab seqs:
"seq 454→502 over 6/6 grabs, advancing 30.1 seq/s" is the origin-side answer to HOW a sender moves.

The experiment the card exists for - **bisect the chain**:

1. Route "rave-mate testcard" DIRECTLY over a media route → the receive sink stage
   (`out:rave-mate link …`) verifies capture→encode→wire→decode→republish with no third parties.
2. Add OBS in the middle (Spout2 Capture source, STRETCHED TO THE CANVAS - the decoder expects the
   card to fill the frame) and route OBS's sender.
3. Diff the two `testcard stats`. Direct clean + via-OBS frozen pins the loss to the OBS leg (their
   composition, or our capture of THEIR sender - the NT-handle suspect); both frozen pins our chain.
   Wire fps 60 with delivered seq/s near 0 = we are faithfully encoding a texture nobody writes.

Caveats: the receive-side verifier lives in the CPU sink, which the GPU zero-copy decode path
bypasses - disable zero-copy decode on the receiving side while verifying a route. The media child
is demand-gated (a connected peer or the webcam feature); `testcard start` answers "feature not
running" until one of those holds. Generator skips + the in-frame overran flag mean receiver gaps
are the GENERATOR's fault - judge the pipeline only against frames the generator actually sent.

`busyDrops` (saturation) and `encFails` (hard failures) are SEPARATE fields in `PipelineStats`:
"saturated but healthy" and "failing" are different incidents needing different responses, and
they are indistinguishable once summed into `Dropped` (which still carries both as a total).

Ledger state (`ledgerFails`, `poisoned`, plus `PoisonReason` in `ProcStats`) rides the same
channels, so a degrade is visible in live stats and not only in a log line that has scrolled away.

Gates: `TestTelemetryNamesTheCodePathOnSuccess` asserts all of the above is emitted on a
SUCCESSFUL open; `TestStatsSeparateSaturationFromFailure` and `TestStatsSurfacePoisonState` assert
the rendered side. **Note:** the live spout gates SKIP unless `SpoutLibrary.dll` is reachable
(beside the exe or in the managed bin dir under `RAVE_MATE_CONFIG_DIR`) - a "PASS" without it
staged is a skip, not a verification.

## The ladder must be retried per FAILURE, not per enumeration

The software rung was originally reachable only when MFT *enumeration* found nothing. A hardware
MFT that BINDS and then refuses to configure took the whole open down with the last rung never
tried. Reproduced deterministically by exhausting the encoder's concurrent-session cap: with 11-12
live 1080p60 sessions the next `SetOutputType` returns `MF_E_UNSUPPORTED_D3D_TYPE` (0xc00d6d76) on
a bound NVENC MFT, and the route died outright.

`configureEncoder` therefore covers the whole MFT setup (bind → output type → input type → ICodecAPI
→ stream info → drive mode) and is RETRIED on the software tier when a hardware tier fails anywhere
inside it. `releaseEncoder` undoes a partial bind (FLUSH, clear the device manager, release the MFT,
ShutdownObject the activate) so the retry reuses the same device and video processor.

Gate: `TestProcSoftwareLadderUnderHardwareExhaustion` holds hardware sessions until the silicon
refuses, then requires REAL BITSTREAM from whatever tier the automatic ladder lands on. Measured:
the 12th session degrades to `H264 Encoder MFT` and delivers 40 AUs at 8.1 kB/frame.

## Gate hygiene: hardware gates are rig-state sensitive

Hardware-MFT tests depend on what else holds the GPU, so the same commit can go green on an idle rig
and red on a busy one. Two rules, both learned from a merge sweep that ran with two live routes:

- **A gate that asserts the HARDWARE path SKIPS with a reason when the tier degraded**
  (`runConcurrentTier(..., requireHW: true)`). A hardware gate that silently re-aims at software
  either fails on timing or asserts the wrong thing; an honest skip beats both.
- **No fixed, hardware-calibrated sleeps.** Software encode is an order of magnitude slower and the
  sessions share one device lock for their GPU readback, so `time.Sleep(50ms)` after one frame
  reported "no AU" for a rung that was merely slow. `awaitAUs`/`settleForTier` wait for output with
  a tier-aware ceiling: fast on an idle rig, patient on a loaded one, and the assertion is unchanged.

**Success returns need an output-volume assertion behind them, not just an error check.** Three
separate components reported success while producing nothing in one night (a readback that wrote no
bytes and returned true; a route with healthy counters carrying black; a software encoder emitting
no AUs with `err=nil`). Every gate here asserts bytes-per-frame or AU counts, and every failure
message names the tier plus `busyDrops`/`encFails` so "accepted and swallowed" is distinguishable
from "saturated" and from "failed" in one read.
