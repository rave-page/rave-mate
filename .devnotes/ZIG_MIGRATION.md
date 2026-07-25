# Zig migration — strategy + phase plan

Goal: rewrite rave-mate incrementally in Zig, realtime + audio + video processing first.
Method: **strangler pattern, not big-bang** — Zig code enters as C-ABI static libs linked
into the existing Go process tree via cgo, then as whole subprocess replacements. Every
step keeps the shipped app green; pure-Go fallback stays until a port soaks.

## Interop contract (P0, SHIPPED)

- `native/zigcore/` — zig (>= 0.16) static lib. `zig build -Drelease` →
  `zig-out/lib/libravezig.a`. Header: `include/ravezig.h`. All exports `rz_*`.
- `internal/zignative` — cgo binding, tag `zigdsp` (+ pure-Go stub `!zigdsp`), the
  abletonlink pattern. Untagged builds never need the artifact.
- ABI version gate: `rz_abi_version()` asserted by `Available()` — stale lib = fallback.
- Ownership: Go allocates all in/out buffers; opaque handles (`RzResampler`) freed via
  `rz_*_free` + `runtime.SetFinalizer` backstop. Allocator = `c_allocator` (mingw libc).
- Float determinism: Zig default float mode is strict → byte-exact ports are testable
  (`TestZigBucketParity`). Never `@setFloatMode(.optimized)` in ported kernels.
- Build: `make zig` (scripts/build-zig.{sh,ps1}), `make build-zig` / `ZIG=1`.

## Verification rules (every port)

1. Byte-exact port → golden parity test vs the Go original (which stays authoritative
   until retired).
2. Intentionally-better output (e.g. sinc vs linear resampler) → behavioral/quality
   gates (SNR, output-count ratio, reset semantics).
3. Benchmark Go vs Zig in the PR (bands kernel: 32.3ms → 17.8ms, 1.8x).
4. Live ctl verify after merge like any feature.

## Phases

- **VR overlay raster DONE (feature/zig-vroverlay):** vroverlay's hot renders
  (Panel/Menu/Stats) execute as a Zig display list (`native/zigvr`, tag `zigvr`),
  pixel-identical (glyph masks stay Go-rasterized), 3.1-3.6x faster. ZIG_VR_OVERLAY.md.
- **P0 DONE (a35d23c):** toolchain, zigcore scaffold, polyphase Kaiser-sinc resampler
  (playback quality item closed: >70dB SNR vs ~35dB linear, zero added latency),
  bucketPeaks/bucketBands byte-exact kernels, seams in audio/source.go + worker/probe.go.
- **P1 realtime audio:** engine inner loops — `writeBytes` f32→LE device feed, decode
  per-sample converters (wav/aiff/mp3/flac/ffmpeg), envelope RMS (`probe.envelope`),
  `waveform.bucketPeaks` (overlay resolver), giokit `WaveColumns`, deckcard envelope.
- **P2 decoders:** WAV/AIFF decode in Zig (hand-written Go ports exist as goldens);
  evaluate FLAC frame decode. MP3/Vorbis/AAC stay Go/ffmpeg until Zig codecs are vetted
  (supply-chain: no unsoaked Zig deps).
- **P3 video:** pixel convert/scale kernels (videoshare pool, mediapipe pre-encode),
  mp4frag hot loops. `mfenc` (COM/D3D11 MFT) stays as-is — Zig can speak COM but a port
  buys nothing until the surrounding pipeline is Zig.
- **P4 worker replacement (the real lever):** worker/featurehost children speak
  newline-JSON stdio — language-agnostic by design. Replace whole children with Zig
  executables one at a time (first candidate: `probe` worker), zero daemon changes.
  Subprocess isolation rule carries over unchanged.
- **P5 networking children (twitch etc.):** assessed on user ask — Twitch chat is
  I/O-bound (TLS WebSocket + OAuth + JSON); Zig std has no vetted TLS/WS client yet, so
  a Zig port adds risk, no wins. Stays a Go featurehost child; revisit when std TLS/http
  matures or a soaked Zig dep is chosen. P4's stdio contract makes the swap mechanical
  later.
- **P6 UI:** see ZIG_UI_GUIDE.md — webview C-ABI port, HTML/CSS/DS assets unchanged.

## CI

Untagged builds unaffected (deliberate). After local soak: add zig install (pinned
version, checksum) + `make zig` + `ZIG=1` to release/nightly + GitLab build:windows.
Track in the release checklist; do NOT flip CI and default tags in the same change.

## Supply chain

Zig toolchain = build-time only, pinned >= 0.16 (winget zig.zig). No third-party Zig
packages yet; any future `build.zig.zon` dep needs a SUPPLY_CHAIN.md row + 7-day soak.
