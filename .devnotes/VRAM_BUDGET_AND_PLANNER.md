# VRAM budget + deliberate encode/decode selection (2026-11-11)

Owner intent: "figure out the proper resource for each encode and decode. Instead of causing
interop errors on maxed-out VRAM, it should never cause high VRAM consumption."

Rig: DJ PC (RTX 3060 12 GB, Resolume ~8 GB, Spout sender/republisher) + VR PC (RX 7900 XTX,
Linux, sender only). Historic failure: NVIDIA refuses GL/DX interop with E_OUTOFVIDEOMEMORY
(0x8876017C) on a full card; decode reopen/recycle under saturation made it worse
(DECODE_DEST_CONTENDED_HOLD, SPOUT_INTEROP_VRAM_CHURN).

## What changed

ONE admission policy (`internal/gpubudget`) now gates every GPU-allocating open BEFORE it
allocates, replacing three independent static-768-MB floors.

- `Admit`: open iff `free - cost(op,geom) >= reserve` AND rave-mate's own GPU footprint +
  cost stays under a hard self-cap. Headroom unknown => fail OPEN (never gate blind), logged.
- Cost model: computed estimates (W*H*bpp * surface counts) REFINED by measurement -
  primary-adapter free-VRAM delta across a successful open, EMA-folded, persisted per
  adapter+driver in a bounded, versioned, corruption-safe `gpucost.json`.
- The three gates consume it via seams (nil => the old static-floor fallback): decode
  recycle/open (`mfenc.DecAdmit`), sender create (`mediaroute.AdmitSender`), encode open
  (`mfenc.EncAdmit`). Wired once per process in `featurehost.WireVramBudget` (daemon in-proc +
  media child).

### Cost estimates (MB), conservative; measurement overrides

- decode pipeline = 128 (device+MFT) + DPB(12 h264 / 10 hevc) * NV12 + 1 RGBA VP + 1 NV12 VP.
  4K h264 ~= 343 MB. The destination texture belongs to the sender op, not re-counted.
- sender = 48 (interop) + 1 RGBA. 4K ~= 81 MB.
- encode = 96 (device+MFT) + 4-NV12 pool + 1 RGBA. 4K ~= 196 MB.

### Reserve + self-cap defaults (chosen, justified)

- Reserve (free margin left for OTHER apps' interop after our alloc): floor 768 MB (keeps the
  DECODE_DEST floor as baseline), geometry-scaled up for larger frames: 1440p 896, 2160p 1152,
  4320p 1536. Configurable (`MediaLink.VramReserveMB` acts as a floor; geometry can raise it).
  Rationale: 768 is the empirically-safe decode-rebuild margin from the 2026-09-11 RCA; a 4K
  interop needs more headroom left than a 1080p one.
- Self-cap (rave-mate's OWN total GPU footprint): `min(1.5 GB, 12% of adapter budget)`. On the
  12 GB 3060 => 1440 MB. Rationale: a relay must be a LIGHT guest on the DJ's card (Resolume
  owns ~8/12 GB); capping our own footprint keeps rave-mate from being the process that tips the
  card into the interop refusal. Self-footprint = sum of our processes' dedicated VRAM from the
  gpumem sweep (async, 30 s TTL; unknown => cap axis skipped, free-reserve rule still applies).

## No-pin-under-pressure (closes DECODE_DEST_CONTENDED_HOLD follow-ups)

`recycleDest` restructured:
- Pre-alloc admission: can't afford the rebuild => HOLD the resident pipeline (freeze last
  frame), NO pin count.
- A reopen that fails under VRAM pressure (interop-OOM/timeout signature OR headroom now below
  floor) => HOLD, NOT counted toward `decMaxRecycles`. Only GENUINE capability failures count,
  and only those pin to the (heavier) ffmpeg CPU decode. A successful reopen clears the budget.
- Initial decode open (`OpenProcDecSession`, GPU-dest only) gated too: a VRAM refusal returns
  `ErrDecodeHeldVRAM`; the mediapipe factory HOLDS the route instead of demoting to ffmpeg CPU
  decode (which re-uploads + re-registers Spout interop on a full card = the crash). Encode open
  is symmetric (`ErrEncodeHeldVRAM`; ffmpeg substitute is no lighter on one GPU). Held routes
  re-establish on renegotiation when headroom returns.

Pure classifiers (`vramPressureSignature`, `reopenFailedForVRAM`) are table-tested off-GPU.

## Degradation: resolution as the last resort (Decision 2026-11-11)

SUPERSEDES the earlier owner rule "the ladder never cuts resolution". The receiver VRAM rate
governor now caps resolution to 1080p at the DEEPEST tier only (after bitrate, then fps). A
route already <=1080p is unaffected (the sender only scales a taller source), so the most-visible
degradation hits only the highest-res routes under the worst pressure, where the alternative is a
held/frozen pipeline. `RateHint.MaxHeight` is omitempty (old peers ignore it) and composes with
the transport congestion controller's hint via the receiver's `min()`.

Apply sites: ffmpeg substitute honours MaxHeight LIVE (lowers the downscale ceiling + respawns
the child so the encode-args scaler rebuilds; a cleared hint restores the base, converging).
Native MF applies bitrate live; its fps/resolution caps stay OPEN-TIME only - a gap (needs a Zig
live control op to re-type the MFT output). Reported, not silently dropped.

## Resolution cap = coded resolution only; destination geometry is invariant (Decision 2026-11-11)

A route has a NOMINAL geometry = the source's real size. The republished destination (Spout
sender / shared texture) is created at the nominal geometry and KEEPS it for the whole route,
whatever cap is active - a Spout sender that changes size mid-set breaks Resolume
compositions/mappings. A cap lowers only the CODED resolution (sender scales down before encode,
so coded size + bitrate + the receiver's decode DPB/surfaces all shrink), and the receiver scales
the decoded picture back UP into the unchanged nominal destination. No sender recreate, no interop
re-registration, no `SenderSize` change when a cap engages/lifts.

Audit of bb33db2 (the shipped ffmpeg-substitute cap): CORRECT under this rule. The cap lives on
the ENCODER (sender side); it has no Spout destination, so nothing re-creates a sink. The receiver
sink is created once at `desc.Width/Height` (nominal, from the advert) and the immortal-sender
logic keeps it across any source/coded size change (mediaroute `openReceiveSink`). Test
`TestDestinationGeometryInvariantAcrossCodedResize` locks it (reopen at a smaller size keeps the
nominal sender, no new interop).

Nominal on the wire: already carried. The advert's `SourceDesc.Width/Height` is the nominal size,
independent of the coded size (which is in-band SPS only). No negotiate.go field needed; an in-band
SPS size change is a coded-size change only.

Decode re-init tolerance (Go-side, Zig-free): when the rate governor changes the cap it calls
`medialink.OnResolutionCapChange` -> `mfenc.NoteDecodeResizeGrace`, opening a 5 s window during
which `decCheck` treats a publish stall (handle unchanged) as an expected decoder re-init, NOT a
frozen-destination wedge - so a coded-size change never triggers `recycleDest` / the ffmpeg-CPU
demotion. Tested (`decCheck` CODED-RESIZE case + the grace window). A handle CHANGE still recycles.

DEFERRED (need GPU verification - blocked by a live card, see below - and are risky unverified
native MFT code):
- Native encoder LIVE cap (Zig control op): the MF encode child must change its VP output /
  encoder input size on the fly (dynamic resolution change where the MFT supports it, else re-init
  the encoder MFT in place; force an IDR with new SPS/PPS; no gpubudget "new" allocation, avoid
  holding both pools at once). Bounded switch rate (hysteresis: >=1 change / N s; restore only after
  sustained healthy headroom). Today only the ffmpeg substitute applies the cap live; the native MF
  path applies MaxHeight at OPEN only.
- Native decoder in-place stream-change (Zig): handle the decoder MFT output-type change for a new
  coded size without recycling; re-allocate the DPB/surfaces at the smaller size (this is where the
  decode-side VRAM actually drops - the point of the cap). The Go-side grace above already prevents
  the recycle; the Zig child still needs to emit/handle the stream-change and shrink the pool.
- ffmpeg DECODE-path upscale to nominal: under a cap the ffmpeg CPU decoder outputs the coded size;
  the current sink drops the mismatched frame (stale picture) rather than upscaling. The invariant
  HOLDS (sink geometry unchanged) but the picture freezes on that path. A decode-side scale filter
  belongs in `internal/mediapipe/args.go` (owned by another agent) - left as a small follow-up.

## Live plan-driven `auto` + the throughput benchmark that feeds it (2026-11-11, follow-up)

- **Benchmark runner** (`internal/gpubench`): OS-neutral, gated, serialized, wall-clock-bounded.
  `Want`/`Tick` lazy path runs ONE bucket only when the adapter is idle (no live route) and
  `BenchAllowed` passes (headroom above reserve, machine idle >=30s, no protected OBS/Parsec
  encode, room for the bench's own cost); the GPU timing is an injected `Timer`
  (`featurehost.encodeBenchTimer` = an mfenc ProcSession fed synthetic frames, admitted through
  gpubudget, ctx/timeout-killed). The media child's telemetry loop enqueues the working HW
  encoder's standard buckets (1080p/1440p/2160p) when idle and Ticks. `RunAll` is the explicit
  path. Measured throughput persists per adapter+driver.
- **Cache key**: `vendor | model@driverVersion | op | codec | bucket`. The model distinguishes
  GPUs; the registry DriverVersion (display-adapter class key; "" fallback) distinguishes driver
  updates. Justified fallback: when the registry read fails, the model name alone keys it.
- **Live `auto`** (`encoderscan.ResolveDevicePlan` via `featurehost.NewEncodeDeviceResolver`):
  once the candidate silicon is MEASURED, `auto` picks the encode adapter from `PlanEncodeFor`
  (sustains the pixel rate + most headroom + VRAM); UNMEASURED stays at the engine default
  (adapter 0 / native-first) so a fresh machine is exactly as before. `pin`/`avoid-busiest`
  unchanged. Negotiated codec wins (the planner picks the adapter within what the peer accepted).
  The decision is per open; a re-plan happens on reopen/renegotiation. A plan HOLD/pause degrades
  to the engine default (the VRAM HOLD proper is already enforced by `mfenc.EncAdmit`).
- **Explainability**: `ctl encoder-scan` reloads the shared cost file and the budget panel lists
  measured throughput (Mpps) + learned costs + the last per-op admit decision; the per-route
  chosen adapter/encoder + reqMpps + why is logged by the resolver (medialink log stream).

Real benchmark + `TestMFBridgeHardware` NOT RUN: at execution the card had **Resolume Arena live
at ~80% GPU (sm), encoder ~4%**, plus **OBS64 and parsecd** present (nvidia-smi pmon). That is
exactly the protected/live condition the brief says to skip under - a GPU benchmark would contend
with the user's live visuals and a possible live Parsec session. `native/zigenc` was therefore not
built/run either (the exe only matters for a GPU run). `encodeBenchTimer` compiles against the
Windows mfenc build but is GPU-unverified. The lead should run it on an idle card: close
Resolume/OBS, leave the machine idle ~30s, then the media child's lazy sweep measures the HW
encoder; `ctl encoder-scan` then shows the throughput table. Multi-GPU placement cannot be verified
on this single-dGPU PC - on a two-adapter machine the lead should confirm `auto` picks the
measured, least-busy, sustaining adapter and that non-primary throughput keys correctly (today
non-primary throughput falls back to headroom-based selection; see the follow-up below).

Follow-ups (bounded, documented): a forced synchronous `ctl encoder-bench` needs a daemon->child
proxy (the lazy sweep + encoder-scan table cover the need today); the decode benchmark needs a
throwaway GPU destination + pre-encoded AUs (encode is measured, decode throughput stays
unmeasured => its sustain gate is skipped); multi-GPU per-adapter throughput keying (the budget
resolves the PRIMARY adapter, so a non-primary encoder's measured rate is not retrieved - it
degrades to headroom-based `auto`, no worse than before).

## Deliberate selection (`internal/encoderscan`, OS-neutral)

- `PlanEncodeFor` adds a measured-throughput sustain gate (pixels/sec >= W*H*fps * 1.15); `auto`
  never places a route on silicon that can't keep up. `PlanDecode` chooses the decode
  adapter/path: least-busy VRAM-admitted hardware that sustains the rate; else CPU software; else
  HOLD for a VRAM-pressured GPU-destination route. `BenchAllowed` gates the micro-benchmark.
- Inputs are plain structs (adapters, util, measured pps, VRAM admit bool) so a Linux VAAPI
  sender feeds the same functions. Measured throughput persists per adapter+driver in the same
  store.

## Explainability

`ctl encoder-scan` gains a VRAM-budget panel (free/budget/cap/reserve, last per-op decision,
learned costs). Per-route admit/hold decisions log in the MEDIA CHILD's stream (isolated media
plane), not the daemon ctl.

## Verified vs unverified

Verified: `go build/vet/test ./...` green except the pre-existing `TestMFBridgeHardware` (needs
a built `rave-mate-enc.exe`). New budget/planner packages linux-vet clean. Admission,
degradation order, no-pin-under-pressure, backoff, cost-file corruption, planner choices and the
bench gate are table-tested. Free VRAM probe at authoring: 7288 MiB free on the 3060.

UNVERIFIED / gaps:
1. Live GPU micro-benchmark runner is NOT wired (no safe-to-run GPU session here + no native
   enc exe). Harness (`BenchAllowed`) + throughput cache are ready; a runner must spawn a short
   encode/decode in the supervised child, time it, and call `Budget.ObserveThroughput`.
2. Native MF live resolution cap (open-time only - Zig control-op gap).
3. Measurement attribution uses primary-adapter free-delta (cheap, EMA-smoothed) not per-child
   process VRAM - confounded by other apps allocating concurrently; smooths over samples.
4. Cost/throughput file is shared by daemon + media child; atomic writes make concurrent writes
   safe (last-wins), but in child mode only the child writes.
5. Driver-version in the cost key is "?" (no cheap in-process DXGI driver-version source);
   keyed by vendor+model+op+codec+bucket. Fine until two drivers differ enough to matter.
6. No real-rig run (the crash only bites at saturation).
7. `PlanEncodeFor`/`PlanDecode` are built + tested + fed into advertisement/diagnostics, but the
   LIVE per-route `auto` device pin still defers to the engine / `avoid-busiest`. Rewiring
   `auto` to consult the plan for each live route is the integration step - it needs the
   measured-throughput cache populated (gap 1) and a multi-GPU real-rig validation first, since it
   changes default device placement. Lower-risk to land once throughput data exists.

## Rig verification (lead, on the DJ PC)

Build + deploy a dev build (`pwsh scripts/deploy-local.ps1 -Build`), then under a loaded card
(Resolume ~8 GB): `rave-mate ctl encoder-scan` -> the "vram budget:" panel shows free/cap/reserve
and (after routes run) learned costs. Start a 4K peer receive while VRAM is low:
- expect `receive sink NOT opened - VRAM budget refuses a new Spout sender ...` (sender gate) OR
  `native decode destination ... recycle HELD` / `HELD under VRAM pressure` - and NO
  `pinning this sender to the ffmpeg decode path`, NO Resolume interop error.
- route decode telemetry stays `engine:mf-native-decode gpuPublish:true` (never flips to
  ffmpeg-decode) under pressure.
- as free VRAM drops past floor/2, the sender's `rate governor` log shows `maxHeight` set and
  (on an ffmpeg-substitute route) a resolution cap applied via child respawn.
