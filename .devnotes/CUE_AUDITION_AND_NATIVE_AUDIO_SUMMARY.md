# Cue-audition fix + native audio engine unification

Branch `fix/cue-listen-hold-play`. Two intertwined changes: fix the hold-Space cue audition, then
make the native `internal/audio` engine the sole audio backend and drop beep.

## 1. Cue-audition "have to hit Stop first" bug (6afecff)

**Symptom:** In cue-edit mode, hold-Space plays from the cursor; on release it didn't reset — the
next Space did nothing until you clicked Stop, and there was a decode delay before sound.

**Root cause:** State desync in the daemon-side player mirror. The player engine runs in a `player`
featurehost CHILD; the daemon (`PlayerProxy`) keeps a mirror kept fresh by tick events. But the tick
payload carried only `cur/total`, and `onTickEvent` forced `Playing=true` while never touching
`Paused`; the fire-and-forget `previewRelease` (child pauses + snaps to cursor) also never updated
the mirror. So after release the mirror read `Playing && !Paused`, and the next hold-Space
(`ceAudition`) saw `paused=false`, seeked, but **skipped the unpause → silence**. Clicking Stop
cleared the mirror, forcing the slow `PreviewFrom` re-decode path on the next press.

**Fix:**
- `playerTick` carries `Paused`; `onTickEvent` syncs `mirror.Paused` every tick (durable backstop).
- `PlayerProxy.PreviewRelease` optimistically sets `mirror.Paused=true, Cur=fallback` so a spam
  re-press before the ~200ms confirming tick still takes the warm unpause path.
- `ceAudition` warm re-press seeks THEN unpauses on ONE act-worker call (ordered → no stale-position
  blip when the cursor moved between presses).

## 2. Native engine is the only audio path; ffmpeg fallback goes beep-free (4426091)

Previously two backends chosen per player child by `features.player.nativeDecode` (default OFF):
legacy beep+ffmpeg, or native `internal/audio` (oto ~15ms buffer, RAM preload, sample-accurate
seek, buffer-drop on preview). Native is strictly better for cue auditioning, so it becomes the sole
backend.

- **`internal/audio/ffmpeg.go`** — ported the ffmpeg fallback off beep. `ffmpegDecoder` implements
  `audio.Decoder` (`ReadFrames([]float32)` straight from the f32le pipe, no beep `[][2]float64`
  round-trip). ffmpeg emits 48k stereo f32le == device format, so `source.go` uses no resampler and
  native frame == device frame. Because `LoadDecoder` RAM-preloads any track whose decoded PCM ≤
  512MiB (~46 min), **AAC/M4A cue auditions are now instant RAM seeks — no ffmpeg respawn per press**
  (strictly better than the old beep path). `OpenFFmpeg`/`FFmpegPlayable`/`Playable` live here.
- **featurehost** — dropped the legacy beep branch; the child always runs the native engine. Wire
  `State` moved into the package (was `audioengine.State`). Removed the `nativeDecode` init flag +
  `PlayerProxy` param + `config.PlayerFeature.NativeDecode` (one engine now).
- **`native_engine.load`** — fall through to ffmpeg on `ErrUnsupported` OR any native failure on an
  `FFmpegPlayable` format. Fixes **opus**: Ogg-container opus sniffs as vorbis and the native decode
  fails; the old beep default played it via ffmpeg by extension, so native-as-default would have
  regressed it. A genuine native-format error (corrupt FLAC/MP3) still surfaces.
- Deleted `internal/audioengine` (beep engine + shim + beep ffmpegSource). Ported its real-AAC decode
  + playability tests to `internal/audio`.

## 3. mediaplayer (Fyne A/V trim player) → oto-direct (b0efaea)

The last beep user. Replaced `beep.Ctrl`+global `speaker` with a per-generation `oto.Player` fed the
ffmpeg s16le stream directly. `pcmStreamer` (beep.Streamer) → `pcmReader` (io.Reader that passes
s16le through + counts whole frames for the master clock; volume moves to `oto.Player.SetVolume`).
This player is the only oto user in the MAIN process — the native engine's oto context lives in the
player CHILD process, so the two never collide (`oto.NewContext` is once-per-process).

## 4. Drop beep (9c6f506)

`go mod tidy` removes `github.com/gopxl/beep/v2` + its transitive-only deps (`pkg/errors`,
`writerseeker`). The pure-Go decoders (go-mp3/mewkiz-flac/oggvorbis) + oto stay (imported directly by
`internal/audio`). SUPPLY_CHAIN updated.

## Verification

- `go build ./... && go vet ./... && go test ./...` clean (module-wide).
- `internal/audio`: `TestFFmpegDecodeRealAAC` (generates real AAC, decodes via the ported
  `audio.Decoder`, asserts format/total/seek) + `TestPlayable` — PASS (not skipped; ffmpeg present).
- `internal/featurehost` (`-tags manual`, real audio device + real IPC child, NATIVE engine):
  `TestPlayerPreviewPauseCycle` — previewFrom → previewRelease lands a `Paused` tick → togglePause
  resumes. `TestPlayerChildPlaySeekStop` — play/tick/seek/stop. Both PASS.
- `TestPlayerMirrorTracksPause` (deterministic, no device) — mirror tracks Paused across
  tick + optimistic release. PASS.
- Live app / audio "feel" (spam-Space instant + jump-back): confirm by ear — automated tests prove
  the engine/wire mechanism but can't measure perceived latency.

## 5. Adversarial review + follow-up fixes

A 5-lens adversarial review (concurrency / ffmpeg-decoder / av-sync / cue-ux / removal) over the 4
commits surfaced **10 findings, all CONFIRMED** (0 false positives) after per-finding verification.
Fixed in 4 follow-up commits:

- **`8c16e37` mediaplayer buffer (HIGH+MED):** the oto player kept its ~0.5s default per-player
  buffer (no `SetBufferSize`). `pcmReader.Read` (the samples master clock) counted at read-ahead into
  that buffer, not DAC consumption → `positionLocked` led audio ~0.5s and the video pacer ran ~0.5s
  ahead, dropping seek/startup frames. On Windows `playImpl` also prime-blocked `Play()` ~0.5s under
  `p.mu`, hitching scrubs. Fix: `SetBufferSize(~100ms)` (beep-path parity) + the existing
  `BufferedSize()` subtraction in `positionLocked`.
- **`25a5cc3` native cue-audition child (MED+LOW×3):**
  - Natural-end used a dead `wasPlay && cur>=total-0.1` heuristic (`wasPlay` never cleared), so a
    hold-release snapping within ~100ms of the tail read as EOF — wiped the mirror, killed the tick
    loop, dropped the next press off the warm path + defeated idle-stop. Replaced with an
    authoritative `source.ended` EOF flag (set only on real read-to-EOF, cleared by SeekTo).
  - A background Preload racing the first hold-Space double-decoded to RAM and could clobber the
    playing audition (late source-swap `Close()`s it). Fix: per-path in-flight load dedup.
  - `onTickEvent` wrote `Paused` unconditionally, so a stale poll-tick could clobber a confirmed
    pause → the original "must hit Stop" bug, still reachable. Fix: ticks are upgrade-only on Paused
    (every real resume goes through an RPC that rewrites the mirror).
- **`e9a5cd7` ffmpeg large-file (MED×2):** restored the 0.5s non-explicit seek-coalescing guard
  (streamed ffmpeg follow-slider respawned ffmpeg ~1×/s → choppy) by threading an `explicit` flag
  through `Engine`/`source` SeekTo; raised the child-Call timeouts (10→60s play/preview, 15→120s
  preload) so a long AAC RAM-decode doesn't spuriously fail with a false error + late audio.
- **`de0fd18` test + docs:** `.aiff` is native-Openable now (not ffmpeg-only) → moved it to the
  always-playable tier so `TestIsPlayable` passes without ffmpeg on PATH; pointed the stale
  `docs/VIDEO_PLAYER_DESIGN.md` `audioengine` references at `internal/audio`.

**Residual (documented, not fixed):** a 15–46 min AAC/M4A that RAM-decodes >60s under extreme
concurrent load could still time out the first press if the background Preload hasn't cached it yet
(the timeout raise covers the realistic case; a fuller fix = async decode-readiness event).

Re-verified: full-module `build`/`vet`/`test` green; device-level `TestPlayerPreviewPauseCycle` +
`TestPlayerChildPlaySeekStop` pass; isolated live boot confirmed `audio engine = native`, library
indexed, zero errors, clean `ctl quit`.
