# Transcode / Preset Editor

Port of the Electron Local Studio encoding system to Go. Logical codec is decoupled from the
concrete ffmpeg encoder so a preset is portable across machines with different hardware.

## Pieces

- `internal/transcode/preset.go` - `Preset` model + builtins + `Job.Args()` (pure ffmpeg arg
  construction). Loudness fields: `LoudnessOn/I/TP/RaiseOnly`; `Job.GainDB` → single
  `volume=<g>dB` filter (the ONLY audio filter the package emits).
- `internal/transcode/loudness.go` - music-first linear normalization:
  - **Two-pass, no compression**: pass 1 measures the whole track (ffmpeg `loudnorm`
    analysis → EBU R128 integrated/true-peak/LRA), pass 2 applies ONE constant gain.
    Never loudnorm-dynamic / dynaudnorm / limiters - the mix is only scaled.
  - `PlanGain`: gain = target − measured I; positive gain capped at (ceiling − measured TP)
    so the true peak never clips (less gain, never a limiter; already-over-ceiling sources
    left untouched). `LoudnessRaiseOnly` skips tracks already at/above target. Silence skips.
  - `LoudnessTargets()`: Spotify/YouTube/Tidal/Amazon −14 · Apple −16 · Deezer −15 ·
    ReplayGain −18 · EBU R128 −23 · club/DJ −8. Target I + TP ceiling granular per preset.
  - `MigrateLoudness`: legacy profile strings (music-stream/…) map to the linear fields -
    old presets keep their targets but stop compressing.
  - `MeasureArgs` + `ParseLoudnormJSON` (−inf → −99 sentinel).
- `internal/transcode/encoder.go` - codec×accel → concrete encoder, gated on the machine's
  working set (`ResolveEncoder`). `auto` walks the HW vendor order; falls back to software.
- `internal/transcode/profiles.go` - resolution-only quality profiles (`ApplyProfile`).
- `internal/transcode/hints.go` - source-aware heuristics (ports the web PresetEditor):
  `ParseProbe` (ffprobe streams/format → `SourceInfo`), `RecommendVideoBitrateK`/
  `RecommendAudioBitrateK` (YouTube ladder × codec/profile factors, capped at source),
  `ApplyProfileSrc` (source-driven profile bitrate), `CompareQuality` (up-encode + remux
  warnings), `SourceInfo.Summary()`.

## Worker (out-of-process ffmpeg)

- `internal/worker/encoders.go` - `transcode.detect`: parses `ffmpeg -encoders` AND
  test-encodes each candidate (the only reliable HW signal). Drives auto-mode.
- `internal/worker/tcresolve.go` - headless callers (automations, peer remote-control, trim)
  resolve the HW encoder on the executing machine (cached test-encodes), so "auto" uses the
  GPU instead of silently going software.
- `internal/worker/transcode.go` - `transcode.run` accepts a resolved `Preset` (UI path) or a
  `presetId` (headless); resolves `EncoderOverride` when absent. Fixes the prior "unknown
  preset" error by passing full encoder settings through to ffmpeg.
  Loudness: runs the pass-1 measurement (same trim window), emits a `loudness` event
  (measured + planned gain + capped/skipped) before encoding and echoes it in the result.
  `transcode.measure` = measurement-only op (UI source detection, store-cached by mtime).

## UI (`internal/ui/view_studio_transcode.go`)

- Full preset editor + per-file transcode panel mirroring the web builder.
- Probes the selected source (`probe.streams` → `SourceInfo`, cached per path) and shows a
  source summary + up-encode/remux warnings under the builder.
- Quality profiles use `ApplyProfileSrc` for source-accurate target bitrates; audio bitrate
  field hints the closest ladder entry to the source.
- Capability gating: accel dropdown lists only vendors with a working encoder; Tune disabled
  for HW encoders; audio bitrate disabled for lossless/copy; MP3 VBR disabled off-mp3.
- LOUDNESS section: enable toggle, industry-target picker + granular LUFS / dBTP entries,
  raise-only checkbox, full how-it-works explanation, and a live readout of the selected
  file's measured loudness + the exact gain that will be applied ("+4.2 dB constant gain",
  "capped by the −1 dBTP ceiling", "left untouched (raise-only)"). Auto-measures via the
  transcode worker; cached across restarts (`store.KindLoudness`).

Verified live: −42 LUFS test tone → planned +28 dB → output measured −13.95 LUFS / −9.7 dBTP.
