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

## Design (controller, model, ceilings)

See the controller in `internal/medialink/adaptbudget.go` and the sections below (filled as the
units land).
