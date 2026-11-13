# VRAM policy for a card another app keeps full (2026-11-13)

Owner: "if I have to free VRAM in Resolume, I won't be able to use rave-mate during my sets."
DJ PC RTX 3060 12 GB: Resolume Arena 8-10 GB is NORMAL. 2026-11-13 snapshot: 11859/12288 MiB used,
free 257 MiB (nvidia-smi); budget panel `free=140MB budget=12115MB cap=1453MB`. Live log:
`receive HELD ... free 646 MB - cost 80 MB < reserve 1152 MB`. Same route ran fine on the same full card
until rave-mate restarted (kept senders are never gated; only a NEW one is).

## Unit 0 - facts (K = known from logs/dumps/code, A = assumed)

### Where each historical failure happened

| Date | Process that failed | What failed | Trigger | Src |
|---|---|---|---|---|
| 08-04 x2, 08-10 | rave-mate DAEMON (Spout sink was in-proc then) | SpoutLibrary `LinkGLDXtextures` -> `wglDXOpenDeviceNV` ERROR_NOT_SUPPORTED -> wrapper `__fastfail` | deck sender worker (re)creating GL/DX interop after hours of per-track churn | K: dump, SPOUT_INTEROP_CRASH |
| same instant | Resolume | its own "failed to create OpenGL and DirectX interop" + zombie | system-wide interop refusal, then our senders vanished mid-frame | K: Resolume log |
| 08-29, 08-30 | rave-mate probe (survived) + Resolume dialog | interop refused `0x8876017C` E_OUTOFVIDEOMEMORY | ~25 deck sender destroy/create cycles; each forces every receiver to re-register | K: debug log; A: churn is causal (correlation + fix held) |
| 09-06/07 | Resolume dialog | interop | stale build churning deck + republish senders per route restart | K |
| 09-11 | DECODE CHILD (D3D11 only) | `recycleDest` close+reopen at free 169 MB -> open timeout x3 -> ffmpeg CPU path -> NEW SpoutLibrary GL interop on full card | our own recycle on a saturated card | K: DECODE_DEST_CONTENDED_HOLD |

Conclusions:
- K: every failure follows CHURN (repeated create/destroy of interop-registered textures) or a
  rebuild of a full pipeline on a saturated card. No log shows ONE sender creation at 300-900 MB free
  failing. Tonight's route ran at 370-940 MB free with no error.
- K: the only fatal-in-our-process path is the SpoutLibrary GL path (`wglDXOpenDeviceNV`/
  `wglDXRegisterObjectNV` inside the DLL; fastfail on error). The probe covers the observed mode.
- K: the decode child never touches GL; its failures are HRESULTs/timeouts in a contained child.
- A: interop refusal is driver-global, so pressure that makes OUR interop fail can make Resolume's fail.
  Hence: the fewer interop registrations exist/are created system-wide, the safer.

### What each sender backend allocates/registers in OUR processes

SpoutLibrary GL path (today, `videoshare.NewSharedSender` -> `rave_spout_create` + `rave_spout_open_sender`,
runs in the MEDIA CHILD):
- K: hidden window + OpenGL context (`CreateOpenGL`), process-wide registry handle.
- K: interop pre-flight: a SECOND transient D3D11 device + `wglDXOpenDeviceNV`/`Close` (per create/relink).
- K (Spout source): Spout's own D3D11 device, the shared BGRA texture (`MISC_SHARED`, legacy KMT handle),
  `wglDXOpenDeviceNV` interop device + `wglDXRegisterObjectNV` on the texture, named access mutex,
  `SpoutSenderNames` registration + `<name>` info map.
- A: Spout GL upload path may hold PBOs (w*h*4 each) - not measured.
- K: CPU: 33 MB `flip_buf` (zeroed open frame) + 33 MB transparent `blank` per kept 4K sender.
- Budget estimate: 48 MB interop/device + 31.6 MiB texture = 80 MB at 4K (estimate, never measured).

Pure-D3D11 sender (SpoutDX protocol, `spoutDirectX::CreateSharedDX11Texture` + `spoutSenderNames::CreateSender`
+ `spoutFrameCount::CreateAccessMutex`, source in spout2pw/subprojects/spoutdxtoc/Spout2/SPOUTSDK):
- K: one D3D11 device, one `B8G8R8A8` texture `BIND_RENDER_TARGET|BIND_SHADER_RESOURCE`, `MISC_SHARED`,
  legacy share handle (32-bit, cross-process), `CreateFileMappingA("SpoutSenderNames", 64*256)` name list
  (mutex `SpoutSenderNames_mutex`), `CreateFileMappingA(<name>, 280)` `SharedTextureInfo`
  {shareHandle,width,height,format,usage,description[256],partnerId} (mutex `<name>_mutex`),
  `ActiveSenderName` map, `<name>_SpoutAccessMutex`. NO GL, NO interop, NO PBO. This is exactly what
  SpoutDX senders (and receivers like Resolume consume) use.
- Cost: texture 31.6 MiB + device overhead (A: low single-digit MB).

Native decode destination (`native/zigenc/src/dec.zig` `openDest`):
- K: pure D3D11. `OpenSharedResource(handle)` on the decode child's own device, VP output view over it,
  sync via keyed mutex or `<name>_SpoutAccessMutex` (3 ms bounded). Opening an EXISTING handle allocates
  no new texture memory. It does not care which process created the texture -> works unchanged with a
  helper-owned texture.

### What a Spout RECEIVER (Resolume) does when a sender appears (outside our control)

- K (Spout source, receiver side): reads `SpoutSenderNames`/`<name>` info, `OpenSharedResource` on its D3D11
  device (no new texture memory), and on a GL host (Resolume) `wglDXRegisterObjectNV` on that texture (+ A:
  its own copy/texture for the composition: ~1 frame of RGBA).
- K: this happens IN RESOLUME'S PROCESS, triggered by us creating a NEW sender name/handle. A kept sender
  (same handle) triggers it exactly once per sender lifetime.
- A: a full card can make that registration fail in Resolume. Not observed for a single create; observed
  after churn. Our lever is therefore: create rarely (once), keep forever, never churn.

### The survivable pre-flight probe (`spout_shim.cpp interop_probe`)

- Runs in whichever process creates the GL Spout sender (media child today; daemon before isolation).
- Tests: `wglDXOpenDeviceNV` on a fresh hardware D3D11 device succeeds right now. 
- Proves: the DLL's fatal `LinkGLDXtextures` error path for interop-DEVICE creation would not be hit at this
  instant. Does NOT prove `wglDXRegisterObjectNV` of a 4K texture succeeds, says nothing about Resolume,
  and is itself an interop open/close (one more registration event).
- A pure-D3D11 sender needs no probe (no GL on our side at all).

### WDDM "free"

- K: `free = budget - BytesResident` (gpumem, adapter-wide D3DKMT). A: WDDM can still allocate above it by
  demoting other allocations to system memory (slower for Resolume, not a failure); hard failures come
  from allocations that must be resident (interop, some driver objects). So "free" is a pressure signal,
  not a hard limit; the fixed 768-1536 MB reserve has no measurement behind it (VRAM_BUDGET_AND_PLANNER
  "chosen" values).

## Design (built in this branch)

1. **Evidence-based admission** (`gpubudget`): need = learned/estimated cost of what OUR processes
   allocate (+ the receiver-follow term: one RGBA frame Resolume copies when a NEW sender appears; a kept
   sender costs nothing new). margin = max(64 MB, 25 % need), configurable. `mediaLink.vramPolicy`:
   - `strict`: old geometry reserve (768-1536 MB).
   - `balanced` (default): admit at free - need >= margin; ATTEMPT band free - need >= margin/2;
     below -> HOLD.
   - `permissive`: attempt whenever free >= need.
   Unknown headroom -> fail open (unchanged). Self-cap kept.
2. **Crash-safe attempt**: an attempt runs only where failure is an error code (decode child, pure-D3D11
   helper, or the probe-guarded GL path in the media child - never the daemon); after it, headroom is
   re-read: open failed or free collapsed below `collapse floor` -> back out, HOLD, bounded attempt cadence.
3. **HELD visible**: per-sender hold record (free, need, margin, policy, last attempt + result, next retry)
   in `ctl routes`, `ctl encoder-scan`, Peers UI text. Retry on the existing 2 s local re-check (no offer,
   no peer cost); log on entry + once a minute; never CPU fallback for a GPU destination.
4. **Claim early, keep forever**: remembered receives persist nominal geometry (w,h,fps); on media-route
   start the destination is claimed for each (admission-gated) and parked with NO TTL; only an explicit
   Stop (which forgets the remembered receive) destroys it.
5. **Adaptive decode mode HW <-> CPU <-> HELD (1e, owner directive 2026-11-13: "fallback to cpu
   decoding if we don't have enough vram and return back to hw decoding once we got enough vram with a
   buffer")** - see section 1e below.
6. **Sender keeper** (`native/zigkeeper`, `rave-mate-keeper.exe`): pure-D3D11 Spout sender host
   (protocol above), single instance per session (named mutex), named-pipe newline-JSON protocol
   (`hello` -> proto/version, `ensure{name,w,h}`, `list`, `clear{name}`, `release{name}`), started
   detached from the job object so it outlives daemon/media child/self-update; app adopts existing
   senders (same handle -> Resolume sees nothing new). Keeper death -> one guarded re-create through
   admission. Opt-in until rig-verified.

## 1e - adaptive decode mode (design, 2026-11-13)

Supersedes "never fall to the ffmpeg/CPU path under VRAM pressure". That rule targeted the OLD
fallback (ffmpeg child -> 33 MB RGBA/frame through Go -> SpoutLibrary GL SendImage = a NEW GL/DX
interop registration + host buffers): heavier on the GPU, not lighter. It stays forbidden. The new CPU
mode is a different path that is genuinely lighter.

### CPU mode = software decode in the EXISTING decode child, into the EXISTING destination

- Where: `native/zigenc` decode child, `sw` open flag. MF software H.264 decoder (Microsoft H264 Video
  Decoder MFT, in-box on every Windows 10/11 - no new dependency; HEVC software MFT only when the HEVC
  extension is installed, else the route renegotiates to H.264). NO D3D manager handed to it => no DXVA
  decoder, no GPU DPB/surface pool. Worker threads bounded via ICodecAPI
  `CODECAPI_AVDecNumWorkerThreads` (default 4 = conservative on the DJ PC; config
  `mediaLink.cpuDecodeThreads`).
- Upload: decoded NV12 (system memory, CODED size) -> ONE `UpdateSubresource` into a coded-size NV12
  DEFAULT texture on the child's device -> the existing video processor scales + colour-converts it into
  the nominal-size destination (opened by handle, as today). Doing the BGRA conversion + 4K upscale on
  the CPU would cost a 33 MB write per frame (2 GB/s at 60 fps) - the GPU VP does it for ~nothing.
  Per-frame work never enters Go (AU ring in, texture out - same as HW mode).
- VRAM per 4K route (estimates; HW learned on the rig):
  | mode | ours | notes |
  |---|---|---|
  | HW (DXVA HEVC 4K) | 114 MB learned (estimate 343) | DPB + VP + device |
  | CPU, coded 1080p | ~16 MB est: NV12 1080p 3 MB + VP intermediates + device | destination exists anyway |
  | CPU, coded 720p | ~10 MB est | |
  Destination (31.6 MiB at 4K) is shared by both modes and is not re-counted.
  Target "CPU mode ~ destination only": met to within the coded-size NV12 upload texture + VP; a pure CPU
  BGRA path would save those ~10 MB but cost ~2 GB/s memory bandwidth - rejected.

### Sustainable CPU decode

- Entering CPU mode sends the sender a cheaper stream through the existing receiver rate hint:
  `RateHint.MaxHeight` = the largest coded height the MEASURED software decode sustains at the route fps
  with 25 % margin (default before measurement: 720p - the historical 13.5 fps 4K ffmpeg decode
  extrapolates to ~120 fps at 720p single-thread-ish, unmeasured), and the route re-offers preferring
  H.264 (software HEVC is 2-3x costlier; no live codec switch exists in the encoder, so the codec change
  rides a re-offer - the destination is a kept sender, so Resolume sees no new sender; the park keeps
  the LAST picture, not a transparent frame, for a mode switch).
- Software decode throughput: bounded micro-benchmark (synthetic H.264 AUs from the existing encode
  bench pipeline is GPU work - so instead: time the first N decoded frames of the live CPU route and
  fold them into the gpubudget throughput store under op `decode-sw`, keyed per codec+bucket). The cap
  is lowered/raised one bucket at a time from that data (1080p <-> 720p <-> 540p).

### State machine (Go, policy only, <= 1 Hz) - `internal/decmode`

States per route: `hw`, `cpu`, `held`. Inputs per tick: headroom, HW need (learned decode cost), CPU
need, destination exists, CPU decode fps vs route fps, keyframe seen.
- hw -> cpu: HW admission refuses (or a verified attempt / HW open fails for VRAM) AND the CPU need fits.
- cpu -> hw (promotion): free >= HW need + buffer (default max(512 MB, 2 x HW need), config
  `mediaLink.hwPromoteBufferMB`) continuously for dwell (default 45 s); min residency 30 s in any mode;
  a failed promotion doubles the dwell (cap 10 min), a stable HW minute resets it.
- any -> held: only when even the destination cannot exist (new destination refused and none kept).
- held -> cpu/hw: re-checked on the existing 2 s cadence.
- Swap protocol: request IDR (existing PLI), start the new decoder, feed it only from the next keyframe
  (never a P-frame on a fresh decoder), first published frame swaps the source feeding the destination,
  then release the old decoder. Cap lifted on promotion (MaxHeight cleared in the hint).
- Visible: mode, why, VRAM numbers, coded cap/codec, CPU decode fps, CPU %, last switch + reason in
  `ctl routes` / `ctl encoder-scan`.

### Delivery order + honest scope

1e-1 the Go state machine + tests (pure, GPU-free); 1e-2 the `sw` mode in the decode child; 1e-3 wiring
(admission fallback to CPU need instead of HELD, rate-hint cap, H.264 re-offer, telemetry). GPU-free
tests cover transitions/hysteresis/flapping/keyframe alignment/cap/CPU budget; the native sw path needs a
rig run (card full now - not run).

## Status 2026-11-13 (feat/vram-fullcard)

| Unit | Commit | State |
|---|---|---|
| 0 facts + design | 09cb6422 | done |
| 1c admission (vramPolicy, need + follow + margin, verified attempt, rate-ladder floor) | 3145e760 | merged to development (983732a1) |
| 1d HELD visible (ctl routes / encoder-scan, hold JSON across the child RPC) | 3145e760 | merged |
| 1e adaptive HW/CPU decode (zigenc proto v6 sw + keygate, decmode, sender cap) | 05e0db48, 169b6517 | GPU-unverified |
| 1a claim remembered destinations at start, no TTL | e5de9e2f | GPU-unverified |
| 1b sender keeper (rave-mate-keeper.exe, opt-in) | 12ea21e2 | GPU-unverified, default OFF |

### Our VRAM per 4K route (estimates unless marked learned)

| Path | Destination | Decode | Other | Total ours |
|---|---|---|---|---|
| before (SpoutLibrary GL sender + DXVA) | 31.6 MiB texture + ~48 GL ctx/interop/2 devices (est) | 114 learned (HEVC) | 2 x 33 MB host RAM (flip + park blank) | ~194 MB VRAM |
| keeper + HW | 31.6 + device (few MB) in the keeper | 114 learned | none | ~150 MB |
| keeper + CPU mode (coded 720p) | 31.6 | ~13 (NV12 upload + VP + device) | none | ~45 MB |
The budget still estimates a NEW sender at 80 MB (GL path); with the keeper on that over-reserves ~45 MB
until a measured sender cost exists (follow-up: sender cost observation / keeper-aware estimate).

### Known gaps (honest)

- Nothing in 1e/1a/1b ran on a GPU: the card was full (11.9/12.3 GB, Resolume live) the whole session.
  No probe was run - the keeper's sender registration would appear in Resolume's source list, which is
  not the "cannot disturb Resolume" class.
- 1e mode switch = close + reopen on the same child session (destination kept, last picture held,
  keyframe-gated) - not a dual-decoder hot swap; the new decoder waits for the sender's next natural IDR
  (no explicit PLI from the decode child yet). H.264 renegotiation for CPU mode not implemented (the HEVC
  software MFT from the installed HEVC Video Extension 2.5.48 is used; slower than H.264).
- DecodeCapHeight is global (routeIO carries no destination identity): one CPU-mode route caps every
  route's coded height. Fine for the one-heavy-route rig.
- Sender live coded-height cap support on the Linux VAAPI sender (sus) is unverified - if it applies
  MaxHeight only at open, CPU mode runs at the full coded size until the next re-offer.
- Keeper: not embedded for self-update (sidecar must be shipped beside rave-mate.exe); Spout max-senders
  fixed at 64 (registry MaxSenders not read); frame-count semaphore not created (receivers treat it as
  "always new", same as the existing decode-into-sender path).
- 1a only runs while the media child runs (it is demand-gated on a connected peer); with the keeper on,
  destinations persist across child restarts anyway.

### Rig checks (lead, DJ PC; build with `pwsh scripts/deploy-local.ps1 -Build` after `make zig`)

1. 1c/1d: `ctl encoder-scan` header `policy=balanced ... margin=max(64MB,25% need)`. With Resolume loaded
   and >= ~290 MB free the remembered 4K route opens (log `receive hold released` or `opened as a verified
   attempt`); below it `ctl routes` shows `HELD ... free/needs/required/policy`. No Resolume interop dialog.
2. 1e (default on): with the HW pipeline refused, log `opening in CPU decode mode` / `decode ... switching
   hw -> cpu`; `ctl routes` engine `hwaccel=software (cpu mode)` + `degraded: VRAM: decode mode cpu ...
   sender coded height capped at 720p`; picture moves in Resolume (bands correct: `RAVE_MATE_MFDEC_PROBE_BANDS=1`
   once). Free VRAM back above HW need + 512 MB for 45 s -> `switching cpu -> hw`, cap lifted. Switch off
   with `mediaLink.cpuDecodeFallback=false` if anything looks wrong.
3. 1a: restart the app with Resolume open: log `remembered destination claimed at start` once per
   remembered receive; Resolume's sender list keeps ONE `rave-mate link ...`.
4. 1b (opt-in `mediaLink.senderKeeper=true`, ship `native/zigkeeper/zig-out/bin/rave-mate-keeper.exe` beside
   rave-mate.exe): log `sender keeper up`; Spout sender list shows the destination with host path
   rave-mate-keeper.exe; restart rave-mate -> `receive sink open (keeper-hosted GPU destination) adopted:true`,
   Resolume does NOT re-follow (no flicker); kill the keeper -> one re-create, a second kill -> HELD.

## Rework after the Parsec incident (2026-11-13, `INCIDENT_GPU_HANG_PARSEC_2026-11-13.md`)

The first `balanced` (need + 25 % margin) admitted a 4K route at ~350 MB free; rave-mate added ~215 MB
(decode child 109 + media child 74 + sender ~32) and Parsec's NVENC failed twice in the 150-400 MB band.
Measured: Parsec encode latency 1.7 ms @ 5.1 GB free, 3.7 ms @ 0.69 GB, 6.8 ms @ 0.29 GB; >300 ms stalls
only below ~700 MB average free. Criterion changed: OTHER apps need headroom; rave-mate must never be
the process that takes the card below it.

### Presets - what each guarantees

| Preset | Full allocation (HW decode, new sender) | Light path (CPU decode into the destination) | Running HW decoder |
|---|---|---|---|
| `strict` | free - cost >= geometry reserve (768-1536 MB) | allowed while free - need >= hard floor (256) when `cpuDecodeFallback` | yields below the balanced system floor (700) |
| `balanced` (default) | free - need >= **system floor 700 MB** (`vramSystemHeadroomMB`) | free - need >= **hard floor 256 MB** (`vramHardFloorMB`) | demoted to CPU when free averaged over 10 s stays < 700 MB for 15 s |
| `permissive` | free - need >= 400 MB; verified attempt down to 256 | same as balanced | yields below 400 MB |

Promotion CPU -> HW: free (instantaneous AND 10 s mean) >= system floor + HW need + max(256, need)
(= 1070 MB for 4K HEVC on the rig) continuously for the dwell (45 s), min residency 30 s, failed
promotion doubles the dwell. Below the hard floor even the light path holds (HELD with numbers).

### Where our MB go and how the light path stays light

- Media child 74 MB (incident) = the SpoutLibrary GL sender: GL context + Spout's D3D11 device + interop
  device + the 4K texture (31.6 MiB) (+ the transient probe device). The decode lives in the enc child, so
  the media child holds NO GPU memory on the keeper path (`mediaLink.senderKeeper`): there the destination
  is ~32 MB in the keeper and nothing else.
- Receiver follow (~32 MB in Resolume per NEW sender) is only paid at creation. Remembered destinations
  are claimed with the FULL rule (system floor) - i.e. created while the card still has headroom - and
  adopted later at no cost (claim at start, keeper across restarts). On a tight card the light path only
  creates a destination when the hard floor still holds after it.
- CPU mode (software decode, 720p coded after the per-route cap): coded NV12 upload 1.3 MB + VP output /
  intermediates (~3.5 MB est) + device ~8 MB est = ~13 MB; at 4K coded before the cap engages ~52 MB est.
  Admission uses the nominal 4K figure (conservative). Unmeasured on the GPU.
- HW decoder never opens and never stays below the floor: admission (system floor) + the yield rule.

### Also in this rework

- Receive rate governors read free VRAM averaged over 10 s (`featurehost.SmoothedFree`): the 1 s
  150<->700 MB swing no longer flips 30<->50 Mbps every second.
- Height cap is PER ROUTE (`medialink.CodedCapper` on the route's decode sink), not global.
- Mode switch requests an IDR at once (`medialink.KeyframeRequestable` -> route PLI; native-rx routes
  send it over the native control seq); the child still gates the fresh decoder on the first keyframe.
- Linux sender applies MaxHeight live (`SetRateHint.MaxHeight` -> `rpw_set_cap_height`, verified by the
  Linux agent at 4K -> 1080 -> 4K).
- Logging: fixed rotating log `%LOCALAPPDATA%\rave-mate\logs\rave-mate.log` (5 x 50 MB, ISO-dated lines,
  `RAVE_MATE_LOG_DIR` override; non-Windows `$XDG_STATE_HOME/rave-mate/logs`) + `gpumem.csv` beside it
  (adapter + per-process rows every gpumem sample = 1/min, 2 x 20 MB).

## GPU verification 2026-11-13 (card 1.4/12.3 GB after the Arena restart; gates env-gated)

`RAVE_MATE_SWDEC_GPU=1 RAVE_MATE_ENC_EXE=<zigenc>/zig-out/bin/rave-mate-enc.exe go test -tags spout ./internal/mfenc -run 'TestSWDecode|TestKeeperGPU' -v`
(SpoutLibrary.dll on PATH: `scripts/fetch-spout.ps1`).

Software vs HW decode, H.264 band pattern, 120 frames paced 60 fps, destination nominal 3840x2160,
`cpuDecodeThreads` 4. VRAM = decode child over its 39 MB device baseline. Capacity = frames / child
decode+publish busy time (synthetic static content: real content decodes slower in software).

| coded | HW: busy / VRAM | CPU: busy / capacity / child CPU / VRAM | picture (child read-back) |
|---|---|---|---|
| 3840x2160 | 0.58 ms / +84 MB | 7.2 ms / 139 fps / 66 % / +14 MB | correct both |
| 1920x1080 | 0.53 ms / +23 MB | 2.2 ms / 457 fps / 18 % / +5 MB | correct both |
| 1280x720 | 0.55 ms / +12 MB | 1.3 ms / 754 fps / 19 % / +3 MB | correct both |

Defects found + fixed by the gate (f1686bc1): plain-buffer output had no IMF2DBuffer (pitch derived),
the NV12 upload texture needs `BIND_DECODER` for a VP input view on this driver, the reused output
buffer must be emptied before each ProcessOutput. `CPUDecodeCostMB` recalibrated (8 MB + coded NV12).

Mode switching, 1080p coded live stream, 5 x HW->CPU->HW on one session: switch call 50-70 ms, first
new publish 94-117 ms after the switch, ~1 AU gated per switch (keyframe requested each time, decoder
fed from the requested IDR), no restart, no decode error, destination handle unchanged, child VRAM
114 (HW) <-> 96 (CPU) MB, back to baseline after 10 switches. HEVC not covered: the encode session the
gates use is H.264-only (HEVC software decode = HEVC Video Extension 2.5.48 installed; unverified).

Keeper: idle 0 MB VRAM / 6 MB RAM; with a 1080p sender 16 MB VRAM, + 4K sender 48 MB total, 33 MB RAM.
Spout SDK lists it with identical handle/size/format; decode child publishes into it (named access
mutex); a SpoutLibrary receiver reads the correct picture; client restart adopts the same handle; keeper
death -> one re-create, second -> hold. Found + fixed: keeper handles must be sign-extended like Spout's
LongToHandle (7ba128a0). The media child was not part of this gate: on the keeper path it creates no
sender, so it holds no GPU allocation for the destination (its 74 MB in the incident was the GL sender).

Native receive gate `TestNetRXDecodeLive4K`: acceptance now max <= 1.5 frame periods, p99 <= 20 ms
(bucket upper edge), zero > 25 ms; re-run PASS (p50/p99/max 17.0/18.0/17.2 ms, 0 > 25 ms, 10 resizes,
0 restarts).
