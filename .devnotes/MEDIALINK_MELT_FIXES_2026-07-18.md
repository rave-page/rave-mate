# Medialink spout-over-peerlink melt - fixes (2026-07-18)

Incident: source PC unresponsive → hard reset mid-set while sharing a Spout source over
peerlink. Spout→NDI with the same source was fine.

## Root causes → fixes (shipped a57243c)

| Cause | Fix |
|---|---|
| Raw NRGBA route (no codec overlap / ffmpeg missing / probe unfinished): AES-GCM + TCP on every full frame, 0.5-2 GB/s | raw video > 320×240 refused BOTH sides (`rawVideoOK`); requester errors at the click, sender refuses with reason; toast in `mediaReceive` |
| libx264 at native 4K60 pins all cores | `NegotiateCodecFor(pixelRate)`: tier-4 SW encoders only ≤1080p60; above → mjpeg tier (NDI-class SIMD intra) at native res |
| Per-frame 8-33 MB alloc+copy in capture | zero-copy readback handoff + `videoshare` pix pool; release via `Frame.Release` (encode feed + runSend; rebuf retains → no release) |
| No fps/res governance | `MaxFPS` (0=60) dropped at source pre-encode; `MaxHeight` policy 0=auto → native on HW tiers, 1080p ONLY on tier-4 SW; -1=never; Settings → Streaming |

Product rule (user directive): hardware encoders take native 4K - never downscale by
default; the cap exists only for the x264 fallback + explicit bandwidth saving.

## Still open

- **mfenc (in progress)**: native MF hardware encoder replaces the ffmpeg child+pipe;
  Phase B = D3D11 shared-texture direct source (share handle already available via
  `GetSenderInfo`) - true zero-copy, no CPU readback at all.
- Per-subscriber duplication (N peers on one source = N capture+encode stacks) → FIXED
  2026-07-25 by the capture wave (shared refcounted capture); the encode stack is still per route.
- ~~`MediaLink.Subprocess` isolation child: still default-off~~ → default ON since 2026-07-26 (see below); cross-PC rig pass still outstanding.
- ~~`mediaroute.scan` builds/releases a Spout runtime object per `ListSenders`/`SenderSize` call
  every 2 s~~ → FIXED 2026-07-25 (one registry handle + cached one-shot scan).


## 2026-07-26 - QoS wave (branch `feature/medialink-qos`, WP-4 + WP-6)

User demand restated: *"the spout over network stream is killing my sending pc's performance
still … make this performant, not kill my whole system and other media encoding streams!"*

### Job/priority discipline (WP-4.1)

`sysexec.AssignToJob(p, bool)` gains a class API (`AssignToJobClass` / `JobClass`); the bool
keeps its old meaning (false→realtime, true→batch).

| Class | Cap | Children |
|---|---|---|
| `JobRealtime` | none | medialink encode + decode (`mediapipe`) |
| `JobBatch` | 10% aggregate hard | codec-probe test-encodes, gridfix engine+trainer, worker background pool |
| `JobMedia` | 70% aggregate hard | webcam capture, vrslstream, mocap ffmpeg |

Why: encode/decode sat in the shared 10% bucket. Capping a REALTIME encoder saves nothing -
the sender still pays full Spout readback + pipe writes for frames the starved child never
drains, which is the melt. Bounding cost belongs upstream (fps/pixel-rate gates). Precedent:
`mediaplayer/player.go` spawns realtime children uncapped. Live captures also no longer share
one 10% bucket with a gridfix sweep (they were throttled into dropping frames), but stay
bounded so a runaway ffmpeg can't take the box.

### Probe gating (WP-4.2)

The §3.2 probe test-encoded every HW encoder 4-concurrently at launch, ungoverned - each test
encode takes a real NVENC/AMF session, so starting rave-mate mid-stream could fail OBS's own
encoder. Now (`internal/app/mediacaps.go`):

- governor forbids background work → `mediapipe.ProbeListing` (listing only: `-encoders/
  -decoders/-hwaccels`, no GPU work, no session) is advertised with `Caps.Validated=false`,
  then the goroutine parks on `governor.WaitWhileBusy` and upgrades to the validated set when
  the stream ends. Deferring the ADVERTISEMENT itself would be worse: a sender with no
  encoders makes the far end refuse the route, or fall back to raw video.
- Listing results are cached separately - they never poison `mediapipe.Cached()`, so
  "encoders working" in diagnostics still means test-encode-proven.

### PlanEncode wired into the real decision (WP-4.3)

`encoderscan.PlanAdvertise` (new, pure + table-tested) now shapes what we advertise, and
re-runs on every governor STREAMING edge (focus/drag edges ignored - they flap and a re-plan
costs a process+PDH scan). Unchanged lists are not re-pushed (`SetCodecCaps` re-advertises to
all peers).

- **WITHHOLD** an encoder whose silicon a CRITICAL consumer holds (OBS live / Parsec), but
  ONLY when a non-protected HARDWARE encoder remains. Never degrade to software-only or to an
  empty list: a CPU tier at 1080p60, or a refused route falling back to raw, hurts the machine
  more than sharing an encode engine. `ActionPause` never empties the advertisement either.
- **ORDER** = plan pick first, then descending headroom. NOTE: `medialink.NegotiateCodecFor`
  treats the advertised list as a SET (tier order is fixed in `codecTiers`), so ordering has
  no effect on today's negotiation - it is the seam the engine wave (sender-side preference)
  builds on. Only withholding changes the outcome right now.
- Real inputs added: per-adapter free VRAM from DXGI `IDXGIAdapter3::QueryVideoMemoryInfo`
  (Budget-CurrentUsage, LOCAL segment - already nets out what OBS/Parsec hold; pure syscall,
  no dep), and each encoder bound to a real adapter by VENDOR NAME (nvenc→NVIDIA, amf→AMD/
  Radeon, qsv→Intel) so LoadPct/VRAMFree are that device's numbers instead of "the busiest".
- **Stays unknown**: HW encode SESSION counts (`Device.Sessions = -1` always) - NVML/AMF/
  oneVPL only, and no new deps; the planner skips that ceiling. Ambiguous joins (two
  same-vendor GPUs) and signature-less families (MF wraps any MFT) fall back to busiest-adapter
  load + unknown VRAM. Exact per-encoder LUID pinning is the device-selection wave's job.
- `ctl encoder-scan` now reports the ADVERTISED set with per-candidate adapter/load/free-VRAM
  + the withheld list, instead of reading the in-proc router (which holds no caps at all once
  the plane is isolated). Its stale "VRAM+session probes next" note is corrected.

### Media plane isolated by default + child backstops (WP-6)

`features.mediaLink.subprocess` is tri-state (`*bool`): absent = ON, explicit `false` =
legacy in-proc, explicit `true` = ON. Migration is provable: the pre-flip schema was a plain
bool WITH `omitempty`, so an existing file can only carry `true` or nothing - no user is
silently pinned to the old path, and a hand-edited `false` now survives save/load (tested).

The reason the default had to move: the governor demotes the WHOLE daemon to below-normal
while a stream is live, which throttles in-proc routes exactly when it matters. `governor`'s
package doc claimed the media plane already ran in a child - now true, with the in-proc
escape hatch documented as the exception.

Also: `HeartbeatTimeout: 5s` on the media Host (the child beats from its 1 Hz telemetry loop;
a wedged cgo capture stops beating while still holding the camera), and the job-object RAM cap
rides the spawn snapshot so the child sets its own Go soft limit at 80% of it - the GC fights
a frame runaway before the OS kills the process.

### Needs the 2-PC rig (NOT verified here)

- Subprocess-by-default cross-PC routing: frames actually pulled through the child,
  caps/secrets propagation, SMPTE TC still in the media-clock domain. #44 Phase 3 verified
  spawn/respawn/telemetry single-PC only; this wave flips the default before that rig pass.
- That a realtime (uncapped) encode child measurably restores sender FPS/CPU headroom vs the
  10%-capped one, and that OBS's stream is unharmed with a route live.
- Withholding behaviour with a REAL live OBS stream: PDH `AdapterEncPct` + `ProtectedFamily`
  populated, nvenc withheld, route negotiated onto the other adapter.
- Listing-only advertisement path: start rave-mate mid-stream, confirm no test encode runs
  (`ctl encoder-scan` note), routes still negotiate, and the validated probe replaces it when
  the stream ends.

## 2026-07-25 - Capture wave (branch `feature/medialink-capture-path`, WP-5)

Second wave, same incident. WP-1/3 (engine + device select) and WP-4/6 (QoS + isolation)
land alongside; this section is the CAPTURE path only.

| Cause | Fix |
|---|---|
| `MaxFPS` applied AFTER the readback: a 120 fps source capped to 60 still paid 120 full-frame GPU→CPU copies/s and dropped half | `fpsGate` inside the Spout poll loop (`videoshare/fpsgate.go`): over-budget ticks skip `ReceiveImage` entirely. `RecvOptions.MaxFPS` + live `FPSLimiter.SetMaxFPS`. Connect/resize detection stays at the fast poll rate |
| Per-route capture duplication: each route = own FrameReceiver + GL context + 250 Hz poll of the SAME texture | `mediaroute/capture.go` `captureHub`: refcounted capture per sender name, fanned out to N routes. Capture rate = the FASTEST subscriber (uncapped wins), re-rated live on attach/detach; each route drops to its own cap downstream. One pooled buffer, N refs (`pixRef`), pool gets it back exactly once |
| `rebuf.add(f)` retained pooled capture buffers with NACK armed (always) → the pool never refilled, every readback re-allocated 8/33 MB | `routeIO.retainOrRelease` (nack.go): unpooled AUs retained as before; pooled compressed AUs COPIED into the window (≤1 MiB); pooled raw pixels exempt (intra + one 4K frame evicts the 16 MB window anyway) |
| Registry scan churned 1+2N COM objects every 2 s | ONE process-wide `registry()` handle in the shim + `rave_spout_scan` (names+dims in one call) + 1 s TTL cache in `videoshare/scan.go` |
| Explicit `MaxHeight` on a HW tier resized with CPU swscale | `scaleFilter`: `scale_cuda` (NVENC) / `scale_qsv` (QSV); AMF + software keep swscale (scale_amf/scale_d3d11 are ffmpeg 7.1+). Early-fail demotion pins swscale if the local ffmpeg rejects the chain |
| Receive side allocated a full raw frame per frame and moved it ~3× (append → copy → memmove) | `decoder.pumpFrames`: reads land directly in the frame buffer capped at the missing bytes (one copy, no memmove), buffers from a bounded ring recycled after `Write`. `Sink.Write` contract: must not retain `Payload` |
| mocapnode never returned pooled readback buffers | `PutPix` after the RGB24 conversion copy |

Semantics to remember: **shared capture is per Spout sender name, not per route.** Spout has
exactly one capture format (NRGBA at the sender's current size) and the receiver detects resize
itself, so nothing per-route needs reconciling there; per-route differences (fps, MaxHeight,
codec) all live downstream of the single readback.

Needs the 2-PC rig (cannot be unit-tested): real Spout readback rate at a cap, two peers on one
source showing ONE poll loop, `scale_cuda`/`scale_qsv` acceptance by the installed ffmpeg (and the
demotion warning when rejected), and the end-to-end sender-CPU/bandwidth delta while OBS streams.

## 2026-07-26 - Engine + device wave (branch `feature/medialink-engine-select`, WP-1 + WP-3)

Third wave, same incident. The user's words: "the encoding presets for the spout peer link share
don't let me choose which device i prefer. make this performant, not kill my whole system".

### WP-1: the pipe-free engine now WINS the negotiation

Ranked culprit #1 was the default codec walk (AV1 → HEVC → H.264): any rig with `hevc_nvenc`
negotiated HEVC, which bypasses `mfenc` and runs the ffmpeg child - raw RGBA over a 64 KB stdin
pipe (497 MB/s at 1080p60, 1.99 GB/s at 4K60) plus a CPU swscale RGBA→NV12 plus a GPU re-upload.
That memory traffic, not the codec, is what starved OBS.

| Change | Detail |
|---|---|
| Native engine gets its OWN capability name | `medialink.EncoderMFNative` = `"h264_mf_native"`, advertised only when `mfenc.Available()`. ffmpeg's `h264_mf` stays a normal pipe-fed tier-3 candidate, so "is the engine pipe-free?" is answerable from the advertised list alone - no medialink→mfenc dependency |
| Negotiation precedence | `Negotiate(enc, dec, NegotiateOpts)`: **pin** (`MediaLink.Encoder`) > **sender codec preference** (`MediaLink.PreferCodec` mirrored onto the send side) > **pipe-free preemption** > the §3.2 tier walk. `NegotiateCodec`/`NegotiateCodecFor` are thin wrappers - every existing caller and test is untouched |
| Unsatisfiable pin/preference FALLS THROUGH | Never a refused route: a peer that cannot decode the preference still gets the best common tier. Refusing would drop to the raw-video guard, which is the melt |
| Software gate still applies | A pinned/preferred `libx264` at 4K60 is still skipped (`swTier4MaxPixelRate`) - the pin cannot re-create the x264-pins-every-core failure |
| Engine keying bug fixed | `mediapipe.Factories` keyed on `Codec == CodecH264`, so a negotiated `libx264` ran on MF **hardware** silicon while the Answer + route stats reported tier-4 software - and `SWOnly` ("force software encode") silently didn't. Now `encodeEngine(spec)` keys on the ENCODER NAME: only `EncoderMFNative` runs mfenc |
| Native-open failure keeps the wire honest | The peer was answered H.264, so the fallback substitutes a *probed ffmpeg H.264* encoder (hw first, `libx264` last) instead of silently changing codec; a loud warn names the substitution |

When h264/mfenc wins, in one line: **whenever this sender has a working native MF encoder and the
requesting peer advertises `h264` decode** - unless the user pinned another encoder or preferred
another codec that is satisfiable. What the peer sees: `Answer.Codec = h264` and
`Answer.Caps.Encoders = ["h264_mf_native"]`; `EncoderTier` resolves that to tier 3 / hardware, so
its route stats read hardware H.264, and its decode path is plain H.264 (unchanged).

### WP-3: which GPU encodes

Config (additive, zero value = exactly the old behaviour): `devicePolicy` (`auto` | `pin` |
`avoid-busiest`), `encoderDevice` (DXGI adapter LUID key), `encoder` (hard encoder pin).

- `encoderscan.Adapters()` exported; `ResolveDevice(policy, pin, adapters, report)` is pure and
  degrades honestly - a pinned GPU that is gone returns "automatic" with a reason, NEVER a
  different device; `avoid-busiest` skips adapters a critical consumer holds and takes the least
  `AdapterEncPct`; a single-GPU box always resolves to automatic (nothing to choose).
- `NewDeviceSelector` TTL-caches (5 s) and only samples PDH for `avoid-busiest`; `auto` costs
  nothing. Called at route open, never on a UI lane.
- `medialink.Options.EncodeDevice/EncodePolicy` are read per offer / route open, so a settings
  change applies to the next route without a restart. `EncodeSpec.DeviceLUID/DeviceIndex` +
  `Device()`, which reports "engine default" unless BOTH are resolved - every pre-existing spec
  builder keeps emitting no device flags.
- **mfenc is the accurate path**: `mf_enc_open`'s dead `codec` param became `adapterLuid` →
  `D3D11CreateDevice(pAdapter, D3D_DRIVER_TYPE_UNKNOWN, …)`, plus vendor-first `MFTEnumEx`
  candidate ordering with `SET_D3D_MANAGER` as the hard gate (an MFT that refuses this adapter's
  device manager is skipped) instead of blind `acts[0]`. An unusable adapter degrades to the
  default INSIDE the shim, so a device preference can never kill a route.
- ffmpeg children: `planEncodeDevice` owns the child's SINGLE `-init_hw_device`/`-filter_hw_device`
  pair. With the capture wave's GPU scaler active the adapter folds into that device spec
  (`cuda=rvcu:1`, `qsv=rvqsv,child_device=2`); without it the per-encoder option does the work
  (`-gpu` NVENC, `-qsv_device` QSV) and AMF/`*_mf` - which expose no device option in ffmpeg at
  all - get a `d3d11va` context. Deliberate caveat: NVENC's ordinal is a CUDA ordinal, which
  usually but not always equals the DXGI one; mfenc's LUID binding has no such ambiguity.

### Settings UI + the split nobody documented

MediaLink settings act on DIFFERENT PCs: `PreferCodec` + `BitrateKbps` are read where the route is
REQUESTED (they travel in the Offer), everything else where it is SERVED. Setting the wrong one on
the wrong box silently did nothing, and the card said nothing about it. The card is now a sender
group (encode GPU, encode engine, fps cap, resolution cap, force-software) and a receiver group
(codec, bitrate), each behind a note naming its PC, plus the media-plane isolation toggle. Every
help body states its side. `PreferCodec` is ALSO mirrored onto the send side (`EncodePolicy`), so
pinning h264 on the capturing PC steers what it negotiates - documented in `help.ml-accel`.

The encode-GPU picker's options come from a new settings probe (`pkGPUEnc`): DXGI always (~1 ms),
the PDH load / "who is on it" join only on a multi-adapter box. The render path reads the retained
slot only - the actWorker never blocks. New help topics `ml-device`, `ml-engine`, `ml-isolation`
(long, with authoritative links) in all 7 locales; `es/fr/ja/ru/uk` also gained the medialink
labels they were missing.

### Flagged, not fixed here

- **Family classification vs. real silicon.** `EncoderMFNative` classifies as `FamilyMF`, so the
  QoS wave's `PlanAdvertise` does NOT withhold it while OBS holds `FamilyNVENC` - yet mfenc may run
  on that same NVIDIA silicon. Withholding it would be worse (the alternatives are the pipe or the
  CPU), so the remedy is `avoid-busiest` / pin. A future pass could bind the native engine's family
  to the adapter it actually opened - the shim knows the vendor.
- `encoderscan.Devices()`'s vendor-name join is still per-encoder-FAMILY while the resolved
  DeviceChoice is a global preference. They agree in practice but are two mechanisms.
- With an UNVALIDATED (listing-only) advertisement the ffmpeg H.264 substitute may name an encoder
  that fails at open; the route then dies as it did before. `mfenc.Available()` is a real probe, so
  the preemption itself is never affected.

### Needs the 2-PC rig (NOT verified here)

- That H.264/mfenc actually wins on a live pair, that the peer's route stats read
  `h264_mf_native` tier 3 hardware, and the sender-side CPU / memory-bandwidth delta versus the
  old HEVC ffmpeg-child path while OBS streams.
- Adapter pinning on real multi-GPU hardware: `D3D11CreateDevice` on adapter N, the vendor-first
  MFT actually bound to that adapter (the "native MF hardware encode" log line carries `device`),
  and the degrade path when the pinned adapter cannot host the pipeline.
- `-gpu` / `-qsv_device` / `d3d11va=rvd3d:N` accepted by the installed ffmpeg, and whether NVENC's
  CUDA ordinal matches the DXGI ordinal on a two-NVIDIA box.
- `avoid-busiest` with a REAL live OBS stream: PDH populates `AdapterEncPct`, the route lands on
  the other adapter, and the picker shows the load + holder.
- The settings card at both window widths plus a locale sweep (the help bodies are long by design).
