# Waveform ↔ beatgrid ↔ audio alignment (design)

Goal (per owner): **reliably render** the already-analysed, in-file beatgrids so the waveform
sits correctly over the audio and the grid anchor is on the beat. Grids are NOT re-analysed here
(Traktor/Rekordbox NML/collection + our gridfix already produce them). BPM-aware: grid spacing =
60000/BPM must match the audio; anchor = first marker's ms.

## Three locks (all must hold)
- **Origin/phase** — where t=0 is (encoder-delay / edit-list / priming). Wrong → constant ms offset.
- **Scale/duration** — decoded length the peaks/grid stretch over. Wrong → drift growing to the end.
- **Tempo/BPM** — beat spacing. Wrong → beats walk off; NOT fixable by phase-nudge (needs re-grid).

## Current state (surface map, verified)
- **Peaks decode** `internal/worker/probe.go peaksHandler`: ffmpeg, now 16000 Hz mono s16le, **no -ss**,
  duration = samples/rate. (bumped from 8000 for band colour.)
- **Playback (primary)** `internal/audioengine/ffmpegdecode.go start()`: ffmpeg 48000 stereo f32le,
  **input-side `-ss`** when seeking. Same ffmpeg default gapless as peaks → shares origin at from=0.
- **Playback (fallback)** `audioengine.go decodeAudio`: beep-native mp3/flac when ffmpeg absent →
  does NOT trim LAME encoder delay → ~26 ms origin divergence. Self-limiting: no ffmpeg ⇒ no peaks ⇒
  no waveform to misalign. Guard: cue-edit requires ffmpeg-decoded peaks.
- **Beat detector** `internal/gridfix/runner.py`: soundfile (native rate) OR ffmpeg fallback 22050,
  no -ss, no gapless trim. Beats in seconds from first decoded sample.
- **Grid model** `internal/musiclib` Track.Beatgrid []GridMarker{PositionMs,BPM}; DurationSec.
  `cuepattern.NewGrid(markers, durMs)`; `Grid.AnchorMs()` = first marker.
- **Storage**: `libdb tracks.beatgrid` JSON, `tracks.fingerprint`, `tracks.file_mtime`. **No decode-
  contract / origin / rate / offset column anywhere.** Peaks blob `mpPeakBlob{D,P,B}` — no rate/samples.
- **Bias** `gridfix.Calibration.BiasExt` per-ext: measures detector-vs-DJ-software systematic offset,
  baked into `Plan.NewStartS` at plan time. Reconciles OUR grids to Traktor's convention; NOT applied
  on the display/nudge path.

## Gaps → plan
1. **Seek origin** (`ffmpegdecode.go:79`, `mediaplayer/player.go:315`): input `-ss` after gapless-trim
   lands ~encoder-delay early → auditioned cue drifts from the shown grid line. Fix: accurate seek for
   audition (output-seek or `-copyts` normalise), or compensate by the known lead-skip.
2. **No stored contract**: extend `mpPeakBlob` with `{rate, samples, leadSkipMs, contractVer}`; optional
   `AnalysisRef` alongside the grid. Makes origin drift detectable/reconcilable instead of silent.
3. **Anchor render** — DONE: `Grid.AnchorMs()` + mint anchor handle in `player.go mpWaveSVG`.
4. **BPM verify overlay (optional nicety)**: overlay gridfix detected beats/downbeats faintly under the
   grid; residual metric. Constant residual → phase (nudge); linear-growing → tempo (re-grid). De-scoped
   unless the render contract proves insufficient in practice.

## Done in this line of work
- Grid ticks/phrase/anchor render (short ticks, 16-bar phrase accent drop-anchored, mint anchor), drawn
  ON TOP of the wave. Band-coloured spectral waveform (low/mid/high biquads in the worker).
