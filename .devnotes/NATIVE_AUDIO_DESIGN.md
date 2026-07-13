# Native audio decode + low-latency player (design)

Replaces the ffmpeg-subprocess + beep/oto path in `internal/audioengine` for the player.

## Why

- beep's native FLAC/MP3/OGG **seek rescans the whole file** to build an index -> a
  seektable-less recorder FLAC froze ~15s/seek.
- ffmpeg is spawned even for FLAC/MP3/OGG to dodge that -> per-seek subprocess respawn
  (~230ms) + pipe jitter + a whole ffmpeg process per track.
- beep speaker buffer = 100ms -> too high for "0 delay on Space".

## Package layout — `internal/audio`

- `pcm.go`     — `Format{SampleRate,Channels}`, interleaved **float32** frame math (helpers).
- `decoder.go` — `Decoder` interface (Format/TotalFrames/ReadFrames/Seek/Close) + `Open()`
                 dispatcher (ext + content sniff). Frame = one sample per channel.
- `wav.go`     — native RIFF/WAVE reader: PCM int 16/24/32, IEEE float 32/64, WAVE_FORMAT_EXTENSIBLE.
                 Seek = data_offset + frame*blockAlign (O(1), sample-accurate, no rescan).
- `aiff.go`    — native AIFF + AIFF-C reader: big-endian PCM (NONE/twos/sowt) + float (fl32/FL32),
                 80-bit IEEE-extended sample rate. Same O(1) seek.
- `flac.go`    — mewkiz/flac wrapper; uses the FLAC SEEKTABLE when present, else a **frame index**
                 built once by scanning frame headers (cheap vs full decode) for O(1) seek.
- `mp3.go`     — go-mp3 wrapper + our own frame/Xing-TOC index -> sample-accurate seek w/o rescan.
- `vorbis.go`  — jfreymuth/oggvorbis wrapper + Ogg page/granule index for seek.
- `aac.go`     — see AAC note. Stub + ffmpeg fallback until a redistribution-clean codec is chosen.
- `engine.go`  — oto/v3 low-latency output (BufferSize ~15ms, WASAPI on Windows), sample clock
                 (reported pos == audible pos), RAM-preload, streaming fallback, transport.

## Decoder contract

    type Decoder interface {
        Format() Format
        TotalFrames() int64          // -1 if truly unknown (rare; we index instead)
        ReadFrames(dst []float32) (int, error) // interleaved, len=frames*Channels; 0,io.EOF at end
        Seek(frame int64) error      // sample-accurate, O(1) or O(log n) via index — never full rescan
        Close() error
    }

All decoders normalize to interleaved float32 in [-1,1] at the file's native rate; the engine
resamples to the device rate only if they differ (linear; codecs mostly already 44.1/48k).

## Engine + transport

- Single oto Context @ device rate, f32, small BufferSize (~15ms) => low output latency.
- One Player fed by a `source` reader that pulls float32 from the active Decoder **or** a
  RAM PCM buffer, advancing an atomic frame clock as bytes are handed to oto. Position =
  clock/rate — matches what is audible (oto's own buffer is the only slack, ~15ms).
- **RAM-preload** (cue-edit): decode the whole file into `[]float32` up front. Cap =
  `preloadMaxBytes` (default 512MiB ≈ 46 min stereo f32); larger files fall back to indexed
  streaming. Preloaded => Seek is a slice-index change (0 latency), Space is instant.
- **Transport** (cue-edit preview / hold-Space):
  - `PlayFrom(frame)` starts output at frame.
  - `PreviewFrom(frame)`: remembers `returnFrame=frame`, plays from it (playhead advances).
  - `PreviewRelease()`: stops output, position snaps back to `returnFrame` (jump-back).
  - Cursor/playhead moves ONLY via mouse/arrow (caller sets frame); playback never moves it.

## Bounded buffers (rave-mate rule)

- RAM preload: hard cap `preloadMaxBytes` (bytes) — over cap => stream, never allocate.
- Streaming read-ahead ring: cap `streamAheadFrames` (frames AND bytes stated in code) with
  drop-oldest on producer overrun (can't happen for local file reads, but bounded regardless).

## AAC decision

No mature **pure-Go** AAC decoder exists. Options: (a) cgo bind faad2 — but faad2 is GPL, and
rave-mate ships **AGPL-3.0** binaries publicly; GPLv2 faad2 is AGPL-incompatible for
distribution (flag for user). (b) a permissive C AAC decoder — licensing varies. (c) keep
ffmpeg **only** for AAC/M4A until a clean codec is picked. Shipping default: **(c)** — native
path owns WAV/AIFF/FLAC/MP3/Vorbis; AAC returns a clear "native AAC pending codec-license
decision" and uses the existing ffmpeg fallback. USER DECISION NEEDED: pick the AAC codec +
accept its license, or accept ffmpeg-for-AAC.

## Rollout

New path behind `features.player.nativeDecode` (default off first), wired into
`audioengine`/`feat_player`. beep+ffmpeg stays as the fallback until parity is proven, then
becomes AAC-only.
