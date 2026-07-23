# Publish export/transcode UX rework (2026-07-23)

Goal: make cut/transcode/normalize in the Publish (+ Library) player actually pleasant -
visible state, no silent surprises, no re-analysis, cancelable, presets editable in place.

## What shipped

- **Live gain plan** (`mpPlanFor`, player_actions.go): source LUFS over the ACTUAL trim
  window via `transcode.IntegrateMomentary` (BS.1770-gated integration of the cached
  loudtl timeline; pure CPU, instant). Readout under the loudness block:
  `-20.6 → -16.0 LUFS · gain capped at +4.6 dB: true peak -5.6 dBTP hits the -1.0 ceiling…`
  - `estimate` vs `measured` marker; flips to exact when a windowed measurement exists.
  - THE fix for "no audible difference whatever LUFS I set": the peak-cap was silently
    reducing gain to ~0; now it says so + how to fix (lower target/ceiling).
- **No re-analysis**: worker `transcode.run` takes `measured` (skips pass 1); the worker's
  `loudness` event is captured + store-cached under `KindLoudness` key
  `path\x1ftrimS\x1ftrimE` (mtime-keyed) - re-export of the same window never re-measures.
- **Waveform loudness layer** (edit mode): amber momentary curve, dashed mint target line,
  mint projected post-gain curve per band (`mpWaveLoudViz`/`mpLoudPath`).
- **Pre-listen**: `audio.Engine.SetPreGainDB` (sample-level, can boost, clamped ±1;
  featurehost `setPreGain` + PlayerProxy re-push on respawn). 🎧 chip applies the planned
  gain to live playback; follows target/trim changes; cleared on rebind/toggle-off.
- **Cancelable exports**: Cancel button beside the progress bar (`mp-excancel`) →
  `Hub.Cancel`/ctx cancel (supervisor KillTree kills ffmpeg); partial output removed;
  clean Canceled state + toast.
- **Extension follows container**: `mpExt` uses `Preset.Ext()` ("aac"→.m4a); preset switch
  rewrites a typed outPath's extension in place. New builtin `copy-audio`
  ("Lossless Copy (keep format)", Container "") is the audio-capture default -
  `ResolveSourceContainer` resolves "" against the input so a FLAC set stays .flac
  (remux-to-mp4 is no longer offered for audio media).
- **Compact dynamic export UI**: per-media bordered blocks, side-by-side ≥1100px; ONE row
  preset·summary·output·picker; summary chip = what-you-get (`FLAC · FLAC · → -14 LUFS ✎`)
  and doubles as the preset-editor button; output-size estimate next to Export; loudness
  quick-target chips (-14 Streaming … -8 Club) via `loudnessFields{compact}` (shared
  primitive extended, not forked); one-tap "switch to FLAC" when a copy preset blocks
  normalization.
- **Preset editor on the export surface** (pbuilder.go): modal seeded from the active
  preset; container-filtered codec options (`pbVideoCodecOptsFor`/`pbAudioCodecOptsFor`),
  per-codec bitrate ladder chips + "max Nk for CODEC", mp3 VBR + quality, loudness block,
  `CompareQuality` source hints; **Apply without saving** (inline preset, `• …` chip
  marker) or **Save preset** (upsert into config; builtin IDs auto-suffixed `-custom`).
  `applyPresetField` is the ONE field-mutation core - libPF delegates to it; the Library
  builder now also filters codecs by container.
- **ctl scroll <y>**: scrolls #main (webview) - lower-page states screenshotable
  (Control iface + SCROLL verb + appControl type-assert; Fyne untouched).

## Gotchas found while verifying

- `ctl set "<multi word label>" v` splits query on FIRST space (known) - drive the
  loudness toggles via `ctl act "mp-loud:publish\x1f0\x1floudon" true`.
- Loudness-affecting handlers must ALSO `mpPatchWave` (target line/projection live in the
  wave fragment, not just the export block).
- `IntegrateMomentary` upper bound is `ceil(end/step)` EXCLUSIVE - `int(end/step)+1`
  leaked one loud sample into a quiet window (caught by unit test).

## Verified (dev build, real data)

Dual FLAC+OBS set: copy-audio default + .flac out; target chips; capped plan line;
wave overlay; FLAC-switch; editor modal + Opus ladder; silent-capture export end-to-end
(`Loudness: skipped…`, `Done → …-cut.flac`); big-set export canceled mid-measure
("Analyzing loudness… 1%" → Cancel → idle, no partial); pre-listen chip at +4.6 dB.

## Follow-ups

- Plan-line "silence" wording shipped; consider surfacing window TP (measure-exact only).
- Automations transcode steps could reuse the compact loudness block + target chips.
- `pubExportOpen` (tracklist export) unrelated - untouched.
