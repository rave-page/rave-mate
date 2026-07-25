# Zig migration — strategy + phase plan

Goal: rewrite rave-mate incrementally in Zig, realtime + audio + video processing first.
Method: **strangler pattern, not big-bang** — Zig code enters as C-ABI static libs linked
into the existing Go process tree via cgo, then as whole subprocess replacements. Every
step keeps the shipped app green; pure-Go fallback stays until a port soaks.

## Why Zig — porting philosophy (GUIDELINE, applies to every port)

Go's runtime (GC pauses, goroutine scheduling jitter, cgo boundary cost) is the wrong
substrate for hard-realtime audio/video. Zig was created for exactly this domain — Andrew
Kelley started Zig to build a DAW without compromising on performance or UI usability.
Consequence: parts of our Go code are **workarounds for the Go runtime**, not the feature.
Recognize them when porting:

- sync.Pool / buffer-reuse rings + prealloc contortions that exist only to dodge GC
- channel hops / goroutine handoffs keeping the audio callback allocation-free
- batching APIs shaped to amortize the cgo boundary
- throttles/caching whose real trigger was GC or scheduler pressure, not the data rate

Rules:
1. **Parity port FIRST** — byte/behavior gates vs the Go original, workarounds replicated
   if they affect output. Keeps ports honest.
2. **Then a Zig-native pass may remove the workaround** (explicit allocators/arenas,
   comptime specialization, deterministic latency, no GC → simpler ownership). Lands only
   behind behavioral/SNR/bench gates + a note here naming the workaround removed + why safe.
3. Never carry a workaround into Zig when its only reason is the Go runtime — flag it in
   the port notes. Unsure whether it's load-bearing? Port faithfully + flag.

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

- **VR overlay raster DONE (feature/zig-vroverlay + feature/zig-vr-remainder):** the
  WHOLE vroverlay raster surface executes as a Zig display list (`native/zigvr`, tag
  `zigvr`), pixel-identical (glyph masks stay Go-rasterized): Panel/Menu/Stats
  (3.0-3.6x) plus path-orbit preview (7.3x), ghost (9.3x), strip (11.5x), tooltip,
  hover row, outline + the edit-mode border stamp. No new op kinds — border/line/disc
  rasters decompose into STORE runs; `rz_vr_render` became atomic (validate-then-write)
  so ops-only renders can fall back cleanly. Skips: `RenderDot` (per-pixel alpha,
  one-shot 64×64) and the wrist logo's fused CatmullRom blit (staged dispatch keeps the
  rest). ZIG_VR_OVERLAY.md.
- **P0 DONE (a35d23c):** toolchain, zigcore scaffold, polyphase Kaiser-sinc resampler
  (playback quality item closed: >70dB SNR vs ~35dB linear, zero added latency),
  bucketPeaks/bucketBands byte-exact kernels, seams in audio/source.go + worker/probe.go.
- **P1 realtime audio DONE:** engine inner loops as additive kernels (ABI stays v1),
  all byte/bit-exact parity-tested vs the Go originals (which stay as fallback +
  golden reference; dispatch via `zignative.Available()`):
  - `rz_f32_to_le` — source.writeBytes f32→LE device feed (gain 0/1 = memcpy
    fast path). 12.9µs → 5.5µs / 8192-sample pull, 2.3x.
  - `rz_fold_stereo` — source.toDeviceStereo channel fold (2ch stays Go-side
    zero-copy). 2.8µs → 2.2µs, 1.3x.
  - `rz_pcm_to_f32` — batch packed-PCM→f32 for wav decodeSample + aiff
    decodeSampleBE (8/16/24/32 int, 32/64 float, LE+BE, padded block-align;
    comptime-specialized loops). Decoders now convert one kernel call per block
    instead of per-sample closures. s16: 38.7µs → 6.4µs (6.0x); s24: 48.3µs →
    7.8µs (6.2x) / 4096-frame block. MP3/FLAC/Vorbis/ffmpeg loops skipped:
    library-output copies with per-frame state, no per-sample math to lift.
  - waveform resolver `bucketPeaks` — reuses existing `rz_bucket_peaks`
    (semantics identical to worker's). 2.36ms → 1.45ms / 5-min file, 1.6x.
  - `rz_wave_columns` — giokit WaveColumns bucket fold. Parity-neutral perf
    (6.6µs vs 6.9µs; ~4-bucket spans, reduction can't vectorize wider).
  - `rz_wave_env` — deckcard buildEnv smoothed envelope, f64 bit-exact
    (truncation/ceil/3-pass binomial replicated; 56µs → 52µs, cached anyway).
  - `probe.envelope` RMS stays Go: streamed per-bucket accumulator interleaved
    with 64KiB stdin reads — no batch boundary to hand a kernel without
    restructuring the streaming loop (revisit in P2/P4).
- **P2 decoders — WAV/AIFF DONE:** full container decoders in Zig
  (`src/{pcmdec,wavdec,aiffdec}.zig`, exports `rz_wavdec_new`/`rz_aiffdec_new` +
  shared `rz_pcmdec_{feed,info,seek_off,set_pos,plan,decode,free}`, ABI stays v1).
  Seam: Go owns file I/O — feed protocol requests absolute byte windows (16 MiB
  header-chunk cap Go-side), Zig owns chunk/COMM/SSND parse (incl.
  WAVE_FORMAT_EXTENSIBLE, 80-bit extended rate, AIFC NONE/twos/sowt/fl32/fl64),
  frame math, seek clamp, and PCM→f32 via the P1 kernel. Zero Zig-side buffering:
  Go's per-chunk body allocs + reuse buffer were GC workarounds, not replicated.
  `audio.Open` dispatches when `zignative.Available()`; Zig open failure rewinds →
  Go decoder (fallback + golden). Replicated Go quirks: fmt chunk NOT pad-aligned,
  sowt 8-bit decoded unsigned, amd64 `int(f64)` trunc (NaN/Inf → min-i64), int64
  PCM = silence. Hardening (both sides): reject `blockAlign` < frame size — the Go
  decoder panicked / Zig kernel read OOB on crafted files. Parity gates
  (`dec_zig_test.go`): bit-exact full matrix + container variants + seek matrix
  (negative/EOF/past-EOF/mid-read) + truncated data + malformed corpus + 400×2
  mutation fuzz. Bench 30s s16 stereo: pure Go 525/473 MB/s → Zig 2046/1956 MB/s
  (~4x; equal to the P1-kernel path — decode is conversion-dominated, the win is
  the parse/error surface moving to Zig).
  Remaining P2: evaluate FLAC frame decode. MP3/Vorbis/AAC stay Go/ffmpeg until
  Zig codecs are vetted (supply-chain: no unsoaked Zig deps).
- **P3 video DONE:** enumerated the whole video plane first — the named modules turned
  out to delegate their per-pixel math (ffmpeg swscale / GPU / C++ shim), so the actual
  Go pixel loops sit on the adjacent producer/consumer seams. Additive kernels (ABI v1),
  byte-exact, Go originals stay fallback + golden:
  - `rz_rgba_to_rgb24` — strided RGBA→RGB24 (mocapnode frameFromNRGBA, the
    videoshare-receiver consumer). 1080p: 3.25ms → 0.78ms (4.1x).
  - `rz_px_label` — per-pixel 5-target ±tol colour classify (mocapnode scanBlobs
    pass 1, RGB24+BGRA; blob BFS stays Go — pointer-chasing, no batch win).
    1080p BGRA: 22.0ms → 12.3ms (1.8x).
  - `rz_fill_cells` — batched square-cell raster (vrslgrid Render/RenderComposite,
    the vrslstream ffmpeg pre-encode + videoshare FrameSender producer; replaces
    ~400k SetRGBA calls/frame with one call; flush before Overlay so painters see
    finished grids). Extended 9-uni composite: 4.51ms → 1.90ms (2.4x).
  Parity gates: `internal/mocapnode/zigpx_parity_test.go` (TestZigRGBAToRGB24Parity/
  Fuzz, TestZigPxLabelParity/Fuzz), `internal/vrslgrid/zigfill_parity_test.go`
  (TestZigRenderParity, TestZigCompositeParity incl. overlay ordering,
  TestZigCompositeFuzz) — sizes/odd dims/padded strides + seeded fuzz.
  Enumerated formats: capture/decode request `rgba` from ffmpeg by design ("zero
  per-frame swizzle", webcam/capture.go); mocap ffmpeg sources emit `bgra` consumed
  in place via Frame.RGB; RGBA→BGRA swizzle + NV12 CSC live in the mfenc C++ shim /
  VideoProcessorBlt GPU (out of scope, unchanged).
  Skips (no Go per-pixel compute to lift): mediapipe encode/decode + medialink
  frame path (I/O piping + 26-byte header math + payload memcpy only); mp4frag
  (header-only box parse, moov KBs + mfra tens of KB — I/O-bound, the P1 MP3/FLAC
  precedent); videoshare send/recv (zero-copy GL readback, flip in shim);
  deckcard.RenderScaled (videoshare's 30fps producer, but a full card renderer —
  zigui-class display-list work, not a convert kernel); mocappanel.Encode
  (reference/test-only) + mocapmaster region fill (already direct-Pix; can adopt
  rz_fill_cells later).
  Go-runtime workarounds NOT ported (stay Go-side as the I/O seam):
  videoshare/pool.go pixPool (GC-dodging frame-buffer pool), the newest-wins cap-1
  frame channels (scheduler seam), webcam/framepipe.go fresh-buffer handoff.
  `mfenc` (COM/D3D11 MFT) stays as-is — Zig can speak COM but a port buys nothing
  until the surrounding pipeline is Zig.
- **P4 worker replacement (the real lever) — STARTED: probe worker implemented, opt-in.**
  worker/featurehost children speak newline-JSON stdio — language-agnostic by design.
  Replace whole children with Zig executables one at a time, zero daemon changes.
  Subprocess isolation rule carries over unchanged.
  - `rave-probe` (native/zigcore/src/probe_main.zig → zig-out/bin/rave-probe.exe): full
    probe worker — ping + probe.{duration,streams,tags,artwork,waveform,peaks,envelope},
    same protocol/field names, bands.zig kernels reused → peaks/bands AND envelope
    byte-identical (golden cross-test `TestZigProbeParity` in internal/worker, skips
    without the exe or ffmpeg).
  - Opt-in seam: config `features.workers.probeExe` (additive at v34, no bump) →
    `Supervisor.SetExternal("probe", exe)`; spawn keeps Hide/Named/LowPriority/job
    object/KillTree; log line carries `backend: go|external`. Missing exe → builtin +
    warn. Empty config = zero behavior change.
  - Known deltas: tags/artwork read via ffprobe/ffmpeg (Go uses dhowden/tag) — format
    fields match, embedded-tag values may differ on exotic containers; malformed request
    JSON → error Response instead of Go's exit 1.
  - Bounded buffers: 64 KiB request line, 1 GiB peaks PCM slurp, 8M-f32 envelope cap,
    64 MiB tool output — fail with protocol error, never accumulate.
- **P5 networking children (twitch etc.):** assessed on user ask — Twitch chat is
  I/O-bound (TLS WebSocket + OAuth + JSON); Zig std has no vetted TLS/WS client yet, so
  a Zig port adds risk, no wins. Stays a Go featurehost child; revisit when std TLS/http
  matures or a soaked Zig dep is chosen. P4's stdio contract makes the swap mechanical
  later.
- **P6 UI (phase A SHIPPED):** see ZIG_UI_GUIDE.md — render-layer bridge live:
  `native/zigui` (libraveui.a, `rz_ui_*`) + `internal/zigui` (tag `zigui`) render
  migrated tabs byte-identical to the Go renderers (golden-tested); first tab:
  appgroups. Shell/actions/transport stay Go until phase B.
- **P6 phase B (B0 baseline MEASURED):** `.devnotes/PHASEB_BASELINE.md` - render benchmarks
  (Go vs Zig vs bridge, 10 tabs) + live counters (`zigui.PerfCounts()`, `ctl perf` `[zigui]`).
  Headline: the phase-A bridge costs **1.2-2.9× pure Go** per full-tab render, and only ~21% of
  that is the Go marshal - 75-80% is the Zig-side `std.json` parse (6.9 ns per state byte, 5×
  Go's marshal slope). A binary wire must kill the PARSE, not just the marshal.

## CI

FLIPPED 2026-07-25 (user directive: all builds ship ZIG=1 + CI validates zig):
- **GitHub ci.yml** (both OSes): pinned zig 0.16.0 (sha256-verified download, no
  third-party action), per-lib `zig build test`, `build-zig.sh`, rz_ export-floor
  check (28/111/2 actual; floors 15/60/2 — zigvr funnels everything through
  `rz_vr_render`), then tagged build+vet+`test -count=1` (archives aren't in go's
  test cache key). Untagged build/test kept — the stub fallback path must stay green.
- **nightly.yml / release.yml**: zig install + `GOOS=windows build-zig.sh` cross
  (windows-gnu libs via the linux zig) → exe tags now
  `spout vr abletonlink zigdsp zigui zigvr`; linux binaries tagged too.
- **GitLab .gitlab-ci.yml** (rave-suite): `.zig_install` anchor; test job runs zig
  tests + tagged suite; build:linux/build:windows build libs + tagged binaries.
- Gotchas fixed at flip time: `#cgo linux LDFLAGS: -lquadmath` (linux libraveui.a
  has undefined roundq/__multf3, same f128 story as mingw); build-zig.sh `.lib→.a`
  copy now windows-target-only (a stale windows .lib in a dirty tree used to clobber
  the fresh ELF .a; and under `set -e` the bare `[ -f x ] && cp` would have killed
  the script on linux where no .lib exists).

## Supply chain

Zig toolchain = build-time only, pinned >= 0.16 (winget zig.zig). No third-party Zig
packages yet; any future `build.zig.zon` dep needs a SUPPLY_CHAIN.md row + 7-day soak.
