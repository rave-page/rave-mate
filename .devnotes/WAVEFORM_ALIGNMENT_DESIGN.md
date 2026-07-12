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
1. **Seek origin** — DONE (#80): SEEK now adds the gapless encoder priming back so an auditioned cue
   lands on the from=0 origin. `mediatools.CodecLeadSkipMs(codec)` (aac 45 / mp3 25 / opus 6 / else 0)
   fed by a codec probe. `audioengine/ffmpegdecode.go start()` (the audition path via PlayerProxy) adds
   `leadSkipSec` to the input `-ss`; `mediaplayer/player.go startLocked` adds it to the audio `-ss` only
   when audio-only (video present → left uncompensated to keep A/V in lockstep). from=0 unchanged →
   peaks/playback origin preserved. Chose lead-skip compensation over output-seek: output-seek-from-0 is
   O(T) decode → unusable on multi-hour sets; modern ffmpeg input `-ss` is already decode-accurate to
   file-PTS, so priming was the only residual. Approximate constant (per-file priming is exact but buried
   in container metadata); residual few-ms << the ~25-48ms removed; lossless stays 0 (no regression).
2. **Stored contract** — DONE (#80): `mpPeakBlob` extended with `{Rate, Samp, Lead(ms), Ver}`; worker
   `probe.peaks` now returns `rate/samples/leadSkipMs`. Cache-miss check gained `Ver>=mpPeakContractVer
   && Rate>0 && Samp>0` (mirrors the band check) → pre-contract blobs re-decode. Tags `d/p` stay
   compatible with the Fyne `trackPeaks` blob sharing the same cache key.
3. **Anchor render** — DONE: `Grid.AnchorMs()` + mint anchor handle in `player.go mpWaveSVG`.
4. **Sanity check** — DONE (#80): `mpPeaksSanity` logs one terse line at peak load/decode
   (`samples/rate ≈ dur`, `drift` flag on >1%/>0.5s mismatch) via the run logger.
5. **BPM verify overlay (optional nicety)**: overlay gridfix detected beats/downbeats faintly under the
   grid; residual metric. Constant residual → phase (nudge); linear-growing → tempo (re-grid). De-scoped
   unless the render contract proves insufficient in practice.

## Done in this line of work
- Grid ticks/phrase/anchor render (short ticks, 16-bar phrase accent drop-anchored, mint anchor), drawn
  ON TOP of the wave. Band-coloured spectral waveform (low/mid/high biquads in the worker).
