# Adaptive per-route bitrate budget (2026-11-12)

Owner: "the budget should be adaptive so it gets the best quality value per system it runs on."
Replaces the flat `Offer.Bitrate = config.bitrateKbps` target with a per-route, continuous AUTO
budget that picks the bitrate giving the best quality for what both systems + the link sustain.

## OS-neutral interface for the Linux VA engine (publish first — hand this to the Linux agent)

All in `internal/medialink/pipeline.go` + `metaframe.go` (committed before the controller):

- `EncodeSpec.QualityTarget int` — enables constant-quality-with-ceiling rate control. 0 = disabled
  (legacy CBR/VBR to `BitrateKbps`, unchanged). 1..100 = target visual quality (higher = better;
  `medialink.QualityDefault = 70`); `BitrateKbps` is the MAX. The engine maps this to its own RC:
  MF = QVBR / quality mode with max bitrate; ffmpeg = `-rc`/`-cq`/QVBR per family; VA = constant-QP
  or constant-quality (ca. QP = map(QualityTarget)) with the enforced bitrate ceiling as the cap.
  Simple content undershoots the ceiling; complex content spends up to it.
- `RateHint.QualityTarget int` (`json:"q,omitempty"`) — a live quality-level change (0 = none).
  Rarely used; the budget normally moves the bitrate ceiling (`MaxBitrateKbps`), not the quality
  level. Additive + omitempty: old peers ignore it and keep reading the single `br` number.
- `PipelineStats.Feedback EncoderFeedback{Present bool; AvgQP float64; RateLimited bool}` — the
  per-second quality signal. `Present=false` = the encoder exposes no QP (controller uses the model
  target alone). `AvgQP` = mean quantizer over the last ~1 s. `RateLimited=true` = the bitrate
  ceiling forced QP above the quality target (content wants more than the ceiling allows). The VA
  engine should fill this from its rate-control stats; it already enforces the bitrate ceiling, so
  `RateLimited` = "clamped to the ceiling AND QP above the target-quality QP".

Wire compatibility: everything above is additive/omitempty. The computed bitrate target still rides
the existing single `br` number (`Offer.Bitrate` and `RateHint.MaxBitrateKbps`), so an old peer that
only understands one number keeps working.

## Design

Receiver-authoritative, old-peer-safe. The RECEIVER computes the per-route bitrate TARGET and sends
it as the single `br` number (Offer.Bitrate + RateHint.MaxBitrateKbps); the sender encodes to it,
constant-quality-capped when `QualityMode` is set. Three layers compose:

1. **Quality model (`QualityTargetKbps`)** - the "good quality" bitrate in bits-per-pixel, seeded
   from measured points (4K60 ~35/22 Mbps H.264/HEVC, 1080p60 ~12/8). bpp drops with resolution
   (compression efficiency rises); codec (av1<hevc<h264), family (software x264 +30%) and content
   scale it. Monotonic in pixel rate + fps. Follows the CODED size (a cap lowers it, a restore
   raises it).
2. **Ceilings, min()** - `AdaptiveBudget.Step` = min(user ceiling, static link ceiling, model [+QP
   bump]). The binding one is the `Reason`. The LINK adaptation is the existing congestion
   controller (`congest.go`): its ceiling is now this model target (so it probes only as far as
   quality needs, brief 3b) and it backs off on loss/delay. The DECODER-VRAM ceiling composes via
   the VRAM `rateLadder`'s min() already in the governors (a resolution cap there also trims the
   bitrate). The ENCODER-capability ceiling affects fps/resolution (the planner), not the bitrate
   target directly - documented.
3. **Quality-driven refinement** - the sender's encoder runs constant-quality-with-ceiling
   (`EncodeSpec.QualityTarget`), so simple content UNDERSHOOTS and complex content spends up to the
   ceiling: the "fall when not needed, rise when needed" for free, no feedback loop. The QP BUMP
   (raise the ceiling above the model when even the model is rate-limited) is in `Step` with
   hysteresis; it needs the sender's `EncoderFeedback` to reach the controller (see deferred).

Wiring: `mediaroute.OfferRoute` sets Offer.Bitrate = `AdaptiveBaseKbps` (auto: model at nominal
capped by user; fixed: config) + `QualityMode`. The governor ceiling is `rm.adaptiveBase(codec,
w,h,fps)` (same model, actual negotiated codec). `router.go` sets `spec.QualityTarget` from the
offer. All additive; an old sender ignores `QualityMode` and encodes to the number.

## Config + migration

`mediaLink.bitrateMode` = `auto` (default, ""→auto) | `fixed`. Auto: `bitrateKbps` is the CEILING
(50000 keeps meaning "never above 50"); fixed: `bitrateKbps` is the exact target (today's behaviour,
byte-identical). Migration: an existing config with only `bitrateKbps` becomes AUTO with that value
as the ceiling (lead's choice) - better quality within the same cap, no action needed. `fixed`
configs are untouched.

## Codec choice (quality-per-bit)

Already satisfied by the negotiation matrix (`codec.go`): priority AV1(1) > HEVC(2) > H264(3), so
when both ends advertise a more-efficient codec it is chosen - better quality per bit at equal
ceilings. The receiver offers only decoders it holds (`preferDecoders`), and decode VRAM is gated by
`mfenc.DecAdmit`/gpubudget (HEVC-4K vs H.264 learned costs). A decode-VRAM-aware codec PREFERENCE
(drop to H.264 when HEVC-4K decode would be too costly) is a refinement, not wired here.

## Defaults chosen (reasoning)

- `auto` is the new default: the owner wants best-quality-per-system; auto delivers it while
  respecting the existing `bitrateKbps` as a ceiling, so nobody's cap is exceeded.
- `QualityDefault = 70` (1..100): a good visual quality the encoder spends up to the ceiling to hold.
- bpp anchors from the measured rig points; `qpBumpFactor 1.5`, hysteresis 3 up / 5 down ticks
  (anti-flap); budget clamp [1, 80] Mbps.

## Deferred / follow-ups

- **QP-bump continuous wiring**: `Step`'s rate-limited bump needs the SENDER's `EncoderFeedback`
  (QP/RateLimited) at the RECEIVER's governor. The sender exposes it in `PipelineStats.Feedback`;
  a sender→receiver feedback field (receiver reports are receiver→sender today) is needed. Until
  then the controller runs with `Feedback.Present=false` (model target stands) and the CQ-capped
  encoder provides the undershoot. The bump logic is tested in isolation.
- **Per-engine constant-quality mode**: the Linux VA engine implements `QualityTarget` against the
  published interface (Linux agent); Windows MF/Zig quality mode (QVBR/rate-control-mode property)
  is GPU-gated + unverified here (the owner's 4K VRChat route is live on this GPU) - deferred;
  ffmpeg `-cq`/QVBR args belong in `internal/mediapipe/args.go` (other agent). Today the budget
  NUMBER is adaptive on every engine; the CQ undershoot activates per engine as each lands.
- **Exact model-at-capped-height per tick**: today a MaxHeight cap lowers the bitrate via the VRAM
  ladder's coupled trim (coarse); the per-tick model recompute at the capped height (finer) is a
  governor refinement.
- **Aggregate per-link split** (`SplitLinkCeiling`, tested): the pure helper exists; tracking
  concurrent routes per link to feed it is not wired (single-route rigs unaffected).
- **ctl routes reason/model fields**: shows mode + ceiling + encoder QP today; the governor's full
  binding-reason + live model target need the decision stored on the route (the governor logs
  `maxKbps`).

## Stage 2 (2026-11-12): regression fix + end-to-end items

**REGRESSION FIX (commit b6da875).** Live 4K60 route got a 1000-kbps AUTO budget. Root cause:
`QualityTargetKbps` returned the 1000 floor when `fps<=0`, and a PipeWire source advertises
`3840x2160@0` (fps unknown until a capture is active), so BOTH the offer and the recv governor
(same model) computed 1000. Fix, robust by construction: `fps<=0 => 60`, unknown geometry =>
1080p, unknown codec => h264 (least efficient). The auto budget never drops below the geometry's
quality-safe value on its own account; only user/link/VRAM/congestion may, with a visible reason.
`assertBudgetSane` logs when a model target is <2000 kbps. 4K60 HEVC auto with a 50000 ceiling now
= ~22 Mbps (fps 0 or 60); H264 ~35. Tests cover fps-0 offer, unknown geometry, 4K60 >=15/~22.

**Item 2 (commit 4731ff7)** budget follows the CODED size per tick: the governors take a
`budget(codedH)` closure; `capBudgetToCoded` lowers the emitted bitrate to the coded-size model
under a cap and restores on lift.
**Item 4 (commit 1e1d07f)** static link ceiling = interface speed x `linkFracPct` (default 50%),
folded into the budget closure; Linux reads /sys/class/net/<if>/speed, Windows returns 0 (congestion
controller bounds it; GetAdaptersAddresses is a follow-up).
**Item 5 (commit d7ad691)** the recv governor records its decision LOCK-FREE (atomic pointer, not
the per-frame st.mu) and ctl routes shows `budget mode=auto target=.. model=.. reason=..`.
**Item 6 (commit d58e0f8)** ffmpeg constant-quality-with-ceiling (`rcFor`): nvenc -cq, amf QVBR,
qsv -global_quality, x264/x265/svtav1 -crf, capped at maxrate; VA/MF keep their paths.
**Item 7 (commit 0edb9c7)** gpubudget self footprint: sweep ALL processes (not top-24) + a
synchronous sample for ctl, so `self=<MB>` instead of `self=?`.

### Realtime-path rule (CLAUDE.md 2026-11-12)
All budget work is control/policy at ~1 Hz (governor tick, meta/stats channels) - off the frame
path. Item 5's governor->ctl handoff is a lock-free atomic pointer (no shared lock with per-frame
count()). Encoder per-frame rate control stays native (the ffmpeg child's own RC, or the VA/MF
child). The deferred feedback wire field (item 1) is designed as PLAIN NUMBERS so a native Zig
transport can apply it via a small control op.

### Deferred items 1 + 3 (with design)
- **Item 1 (feedback loop live)**: the AdaptiveBudget QP bump needs the SENDER's `EncoderFeedback`
  (AvgQP, RateLimited) at the RECEIVER's governor. DESIGN (decision: keep receiver-authoritative -
  the Linux agent is building VA to fill PipelineStats.Feedback for the receiver + consume
  QualityTarget; a sender->receiver meta field is the smallest change): add a `MetaFeedback` meta
  type carrying two plain numbers (`qp int`, `rl uint8` 0/1) - NO Go types on the wire, so a native
  Zig transport applies it via a control op - sent sender->receiver on the report cadence (<=few Hz),
  authenticated like all meta on AEAD routes. The receiver's governor holds a PERSISTENT
  `*AdaptiveBudget` (for hysteresis) and calls `Step(..., Feedback{Present,AvgQP,RateLimited})` each
  tick, bumping the ceiling toward the user/link limit while RateLimited (tested in isolation).
  Not wired: the meta type + the sender send point + the receiver store + the persistent controller
  in the governor (the governor's `budget(codedH)` closure currently creates a fresh stateless model
  per call). ~1 governor refactor + 1 meta field.
- **Item 3 (aggregate link sharing)**: `SplitLinkCeiling` (tested) is ready; wiring needs tracking
  concurrent routes per peer/link and re-splitting on start/stop, feeding each route's governor a
  per-route link ceiling. Not wired.

## Verify live (lead)

On the rig (sus Linux → DJ PC, 4K60 VRChat): `rave-mate ctl routes` shows per route `budget
mode=auto (quality q70, ...) ceil=<kbps>` and, once the VA engine reports it, `encoder qp=.. [RATE-
LIMITED]`. Default config (bitrateKbps 50000, no mode) → auto, target ~35 Mbps H.264 / ~22 HEVC at
4K60 (below the 50 ceiling), dropping on simple content. Set `bitrateMode: fixed` → the exact 50000
as today. A VRAM/network MaxHeight cap lowers the bitrate; a restore raises it back toward the model.
