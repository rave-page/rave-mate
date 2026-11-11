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
