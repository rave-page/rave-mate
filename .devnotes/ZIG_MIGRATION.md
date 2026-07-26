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
- **P6 UI phase B-1a (SHIPPED):** the shared loudness block (`components.go` `loudnessFields`) —
  which phase A left riding through four state contracts as pre-rendered raw HTML — is structured
  state (`loudSt`) rendered by `components.zig loudnessFields` in all four consumers (library
  encode builder, export preset editor, automation transcode step, player export pane). No raw
  loudness markup crosses the ABI any more; only its own tooltip + the caller's extraHTML stay
  raw. Detail: ZIG_UI_GUIDE.md "B-1a: the shared loudness block".
- **P6 UI phase B-1b (shard 1 SHIPPED):** `tooltip.go` `renderTip` — the 70-call-site, 18-file
  tooltip primitive — now crosses as structured `tipSt` (all locale/registry resolution Go-side)
  and renders in `components.zig renderTip`, byte-identical over 527 subtests (73 topics × 7
  locales + 16 edge fixtures). Migrated consumers: settings (13 sites), player (4), automations
  editor (8), automations schedules (7). The other 14 files keep the raw pre-rendered string over
  a dual-field bridge (`tipOr`) and are untouched — wave B-2 flips them. Detail + rules:
  ZIG_UI_GUIDE.md "tipTopic → structured tipSt".
- **P6 UI phase B-1b (shard 2 SHIPPED, wave B-2):** the remaining 14 files are flipped, so NO
  pre-rendered tooltip markup is produced by a state builder any more — `tipTopic(` has zero
  production callers (source-scanned by `TestNoProductionCallerShipsRawTooltipMarkup`; the two
  Go-only surfaces, the nav rail and the pre-listen row inside the loudness block's `extraHTML`,
  call `tipTopicHTML`). The select-with-tooltip **ss-label** became state too (`ssLabelSt` /
  `components.zig SsLabel`), collapsing four pre-rendered label literals. The raw `tip` fields and
  `tipOr`'s raw arm stay for one more step — the post-merge cleanup drops them. 144 new
  byte-equality subtests (`zigui_golden_tip2_test.go`: 18 surfaces × 4 tooltip shapes × 2 locales,
  each with its raw-bridge twin). Detail: ZIG_UI_GUIDE.md "shard 2".
- **P6 UI phase B (wave B-1, in progress):** the per-render state→JSON→parse round trip is
  being replaced by RZW1, a length-prefixed TLV wire whose Go encoder and Zig decoder are
  GENERATED FROM ONE SCHEMA (`internal/zigui/wiregen`) - appgroups + logs pilots land as
  `_v2` exports beside the JSON ones (dispatch v2 → v1 → Go, downgrades counted). -61% on the
  ~1 Hz `#log-view` tick, documents 17.8% of the JSON they replace, decoder fuzzed over 1575
  mutated buffers with a poison-pad OOB canary. Details + the wave B-2 recipe: ZIG_UI_GUIDE.md
  "Phase B - RZW1 binary state wire". Go-runtime workarounds stay flagged, not blind-copied.
- **P6 UI phase B (wave B-2 wire fan-out, SHIPPED):** RZW1 now serves every benched view -
  live (+ its ten ~1 Hz fragments), motion, publish, settings, library, player, automations,
  peers: **174 messages, root ids 10-44, 31 exported `_v2` symbols covering 40 render
  surfaces** (one is kind-dispatched over live's ten fragments), all from schema rows (no
  hand-written codec per tab). Four kinds were added to the generator (`kStrAlways` for Zig
  fields whose default is not the zero value, `kOptPtr`/`kOptVal` for `?T`, `kStrList` for
  `[]string`) and the encoder now sizes its buffers per message - a flat 1 KiB made the
  smallest tick fragment SLOWER than the JSON it replaces. Whole-dispatch deltas **-27% to
  -69%** per view, documents 26-78% of the JSON, decoder fuzzed over **288 135** mutated
  buffers (40 exports x 360 base documents, cross-fed, poison-pad OOB canary + determinism
  canary). Gate per view: Go == v1 == v2 over the FULL golden fixture set, full document and
  every fragment, with `FallbackCounts()` asserted exactly (player's legitimately-empty
  fragments make "no fallbacks" the wrong assertion). Numbers: PHASEB_BASELINE.md.
  Composed with tip2 (shard 2) on merge: 11 structured tooltip/label fields appended inside the
  affected messages + the shared `ssLabelSt` row, and a `wireTipSweep` gate for the surfaces whose
  fixtures leave those fields nil - the merge had ZERO textual conflicts and still broke three
  tabs, while live/motion stayed green with v2 dropping every tooltip until that sweep existed.
- **P6 UI phase B3 (fragment scheduler, pilots SHIPPED):** the ~1 Hz tick no longer crosses the
  ABI once per FRAGMENT. The surface's whole state + the hash of what Go last pushed per fragment
  cross ONCE (RZW1 root ids 100/101, riding wave B-2's LiveState/LogsLines messages); `native/zigui/src/tick.zig` renders every fragment, drops the
  unchanged ones (Wyhash-64, `tickPatch` semantics) and returns a packed RZF1 changed-fragment list
  Go turns into ONE batched Eval - unchanged HTML never crosses the ABI. Pilots: the Live tab tick
  (12 -> 1 call) + `#log-view`. Exports stay STATELESS (hashes travel in the document; a Zig-side
  cache was rejected - reasoning in ZIG_UI_GUIDE.md "Phase B - B3 fragment scheduler"). Gate: the
  scheduler and the legacy per-fragment path, driven from ONE state, must emit the IDENTICAL ordered
  set of __patch calls (proven non-vacuous by execution).
  Composed with wave B-2 on merge: the pilot's own Tk* mirrors of the live states were DELETED -
  the tick envelope now references B-2's canonical messages, so the tooltip fields tip2 added ride
  the tick documents too (gated by a tips fixture + a document-grows assertion, proven by execution).
  Re-measured against B-2's per-fragment BINARY path (the JSON one is gone): Live tick **-29%**
  dispatch / **-42%** steady state / -27% quoted, allocs 196 -> 34 -> 9, and `sched_all` now matches
  pure Go while `sched_same` beats it - the first surface where the Zig path is not a loss.
- **P6 UI phase B4a (player retained state, SHIPPED):** the two Go-runtime workarounds the player
  port flagged are gone. (a) The transport was re-sampled per CONSUMER - four samples in one
  component render, five in one tick - over a mirror the audio child rewrites ~5 Hz and an
  optimistic override that expires on a wall clock, so one DOM could show a moving playhead over an
  idle transport. `mpSt.eng` holds ONE sample per snapshot (`mpMut`/`mpSnap` → `mpCopy`, taken with
  `mpMu` released). (b) `mpResync` re-rendered every embedded player after EVERY container patch to
  survive a mid-build mutation; `mpSt.pgen` + `mpOrdered` (mark → build → enqueue → heal) decides
  the race instead, and the heal is enqueued uncoalesced because the retired workaround's keyed
  patch folded ahead of the container patch it had to beat - i.e. it never closed its own race.
  Gates: a moving-mirror byte-equality + sample-count gate, and a mutation driven between build and
  enqueue (both proven non-vacuous by execution). DOM: the 178 player goldens unchanged +
  `TestZigPlayerEngineGolden` (330 new surfaces, Go == v1 == v2, over the 11 engine states no
  fixture could reach before). Numbers: a quiet container patch **1 152 µs → 76.6 ns**, 9 939 → 0
  allocs; the sample collapse is 0.018% of a tick - a correctness fix, not a speed-up. No Zig
  change, no schema row (`mpSt` never crosses the ABI). Detail: ZIG_UI_GUIDE.md "Phase B — B4a".
- **P6 UI phase B4b (Library retained state, SHIPPED):** the Library tab's Go-runtime retained-state
  workarounds are gone - `collViewSig`, `plRowsVer`, `smartCounts*`'s FNV-over-every-rule-set, the
  5s on-disk TTL and the 2s browse TTL - replaced by a comparable key (`libDerivKey`) + copy-on-write
  controls + computation on `u.bg` (`internal/webui/library_deriv.go`). `libdb.LibraryVersion()` also
  stopped being a `SELECT MAX(seq)` per call: it is an in-memory epoch seeded from the table. Nothing
  crosses the ABI differently and no state struct changed, so the wire schema and every library
  golden/wire gate are untouched. Handler-lane occupancy for a steady-state collection render
  **30.6 us -> 126 ns** (43 -> 4 allocs); worst case (a control moved) **47.9 ms -> 73 ns** on the
  lane, because the ~23k filter+sort moved off it; the on-disk sweep the TTL re-ran blind
  **3.53 ms -> 56 us**, and filesystem freshness improved from "within the TTL" to "next render".
  Gates: a differential missed-invalidation test over every control action, off-lane proofs via a
  runner seam, change-gate counters, and a byte-identity gate on `#lib-body` (retained vs cold) -
  each proven non-vacuous by execution. Detail: ZIG_UI_GUIDE.md "Phase B - B4b".

- **P6 UI phase B4 (retained-state pass, settings arm SHIPPED):** B4 deletes retained-state
  workarounds that existed only for the Go runtime; the DOM must stay identical while the inputs and
  timing change. Settings (B4c+B4d) is Go-side only - matching is not rendering and a probe schedule
  is not renderer state, so no schema row and no `_v2` export (wire ids 170-179 unused).
  **B4c:** `settingsProbes`/`maybeRefreshProbes`/`probeTTL`/`invalidateProbes` are gone. One `busy`
  flag serialized eight probes behind their slowest member (and published every slot only after the
  last returned); the 10 s TTL bounded that pass. Now: per-probe single-flight, per-probe goroutine,
  per-probe slot commit, one COALESCED re-render (the eval queue cannot witness that - it coalesces
  by fragment id - so the cache counts its own re-renders), and per-probe gate readiness. The only
  gate left is cost-proportional (`probeBudget` x the probe's own measured duration, <=5% of a core),
  not a TTL: six of eight probes now refresh at the 1 Hz demand rate while the 303 ms STT mic
  enumeration - the probe that motivated the TTL - prices itself out to ~6 s. Cold fill 370 -> 303 ms
  (slowest member, not the sum); worst-case staleness 10.37 s -> that probe's own cost.
  **B4d:** settings search matches the STRUCTURED `setBlock`/`setKid` state instead of
  `stripTags(setCardHTML(card))` (~40 card renders per keystroke on the handler lane): 2.10 -> 1.25 ms
  per keystroke (-40%). Identity is proven twice before the swap - exhaustively (mutual containment
  of both haystacks' whitespace-free runs decides EVERY possible term: 6069 cards, 15.9 M queries)
  and by enumeration (4248 derived queries x 357 cards = 1.5 M match decisions), plus a pane-level
  gate on the production path. Both arms falsified by execution. Detail: ZIG_UI_GUIDE.md
  "Phase B - B4 retained-state pass".
- **P6 UI phase B6 (Zig shell exe behind PSH1, SHIPPED behind a flag):** the B5 window child is now
  replaceable by a ZIG-owned exe - `rave-shell.exe` (native/zigui `src/shell/`, built by `zig build`
  alongside libraveui / `scripts/build-zig.sh`, installed to `native/zigui/zig-out/bin/`). It speaks
  the byte-identical PSH1 contract (featurehost newline-JSON stdio: init/stop handshake,
  doc/eval/xeval/act/resize/show/quit/streaming/screenshot in; ready/evalres/action/win/gone/shotres/
  heartbeat/log out) against an UNTOUCHED daemon side - shell_proc*.go changed only where the child
  cmd is picked. Child = Win32 window + WebView2 through hand-rolled COM vtables (slot orders copied
  from the shipped mswebview2 WebView2.h); loader = the same two-step webview_go uses (WebView2Loader.dll
  if present, else the Evergreen runtime's `EmbeddedBrowserWebView.dll`
  `CreateWebViewEnvironmentWithOptionsInternal` via registry `ClientState\{stable}` `EBWebView`,
  KEY_WOW64_32KEY, api >= 1150). Bindings: a document-start shim maps `window.rave` /
  `window.__rave_evalResult` onto `chrome.webview.postMessage` BEFORE the daemon's wire-delivered
  runtimeJS; `__beat` is consumed in-child as the heartbeat (UI-thread SetTimer 2 s).
  **B6 lesson (widget child window):** the controller MUST parent to a WS_CHILD "widget" window
  filling the client area (webview.h's structure), not the top-level directly - with GPU compositing
  OFF, a controller on the top-level renders fine ON SCREEN but PrintWindow captures the client area
  solid WHITE (DOM/ctl fully alive, so only a REAL-app dark-theme capture exposes it; the smoke's
  light page passes the >1%-non-black check either way). Found by live verify, fixed by replicating
  the widget arrangement; ctl captures then match the Go child pixel-for-pixel. Window behavior
  ports sizemove_windows.go 1:1 (enter/exit/sizing/moving/capture-changed + stale self-heal, activate,
  size/minimize, showwindow, close-to-tray hide + `win{hidden}`), governor below-normal rule applied
  in-child, screenshots captured in-child (PrintWindow PW_RENDERFULLCONTENT → own stored-deflate PNG
  writer, path+rect over the pipe, never pixels). **Reveal-on-first-ready is honoured** (window created
  hidden, SW_HIDE burned once, SW_SHOWNOACTIVATE at ready unless startHidden) - the B5 black-screenshot
  lesson is a hard requirement here.
  **Selection:** config `features.ui.shellImpl` = ""|"go" (default, Go child) | "zig", or
  `RAVE_MATE_SHELL=zig`; either implies the proc shell. Resolution is gated on the `zigui` build tag
  (shell_zig.go; untagged stub) + a runtime exe check (`RAVE_MATE_SHELL_EXE` override, else
  `rave-shell.exe` beside the daemon exe) - missing exe = loud log + Go child, never a broken UI.
  **Gates:** the ENTIRE B5 suite re-runs against the Zig child (shell_zigproc_test.go, tag zigui,
  skips when the exe isn't built): ctl suite, ordered-FIFO, direct-lane-vs-busy-child, wedged/crash/
  clean-quit shutdown paths BY EXECUTION (the exe implements the loopback page model + deaf/crash/slow
  test modes), round-trip cost (avg 47 µs vs Go child's 73 µs), plus the REAL windowed smoke
  (`RAVE_MATE_WEBVIEW_SMOKE=1 go test -tags zigui -run TestZigShellWindowedSmoke`) asserting
  **>1% non-black pixels** - measured **97.1% non-black, pixel-count-identical to the Go child's
  capture** (604235/621964 both), capture 35 ms, quit 21 ms, full snapshot/click/read/set/act/patch
  parity through the real WebView2 window. Live-verified on an isolated instance
  (RAVE_MATE_CONFIG_DIR + ctl :47695, shellImpl=zig): window visible, Live/Settings tabs render the
  full dark UI, ctl tab/click/set/screenshot parity, `taskkill` on the child → supervised restart +
  reattach re-render WITH state (tab + search text preserved), `ctl quit` exits everything cleanly;
  then flipped back to go impl - no regression.
  **Known deltas (documented, minor):** no embedded brand icon in the exe (title-bar/Alt-Tab icon is
  default; windowicon step pending), `NavigateToString`'s 2 MB limit applies as in webview_go, and a
  WebView2-runtime-missing box exits the child (host restarts w/ backoff) instead of degrading -
  daemon-side probeWebview still gates the renderer choice first. Default stays the GO child until
  parity soaks; flip = one config key.
- **P6 UI phase B5 (procShell + PSH1 protocol, SHIPPED behind a flag):** the WebView2 window can now
  live in a supervised featurehost child (`rave-mate feature webview`) with the daemon driving it over
  newline-JSON stdio. Third `shell` implementation, speaking the EXISTING virtualShell contract (doc
  HTML / eval JS / act payload) - **zero renderer changes**: the daemon builds the same document,
  patches and acts; only where the bytes go changes. Selection `RAVE_MATE_SHELL=proc`; **default stays
  cgo** and all three shells coexist in one binary (untagged + tagged both green). The PROTOCOL is the
  deliverable as much as the code - B6 swaps the child for a Zig exe behind it - and it is spec'd in
  ZIG_UI_GUIDE.md "Phase B - B5 procShell protocol" (framing, lanes, ack, reattach, shutdown, media,
  runtime JS, governor).
  Two lanes over one pipe: ORDERED (`doc`/`eval`, FIFO end-to-end, seq'd, cap 8, drop-oldest + fragment
  cache wipe) and DIRECT (`xeval`/`act`/`resize`/`show`/`quit`/`streaming`, drained FIRST, cap 32,
  refuse-on-full) - because `evalValue` bypasses the batching queue deliberately and would otherwise
  deadlock behind a flooded batch stream. The ack is IN-BAND: the daemon's existing
  `__rave_evalResult` round trip carries both batch acks and ctl results, so the ≤1-un-acked-batch
  bound is unchanged and the child never parses a script.
  The five scouted dependencies, each handled + gated: (1) mediahttp - the `<video>`/`__mse` fetches now
  originate in the child, so URLs carry a per-shell-session segment the daemon hands the child in
  `init` (2 generations valid; UNSESSIONED is byte-identical to the historic URLs, so the default path
  is untouched); (2) `__rt`/`__mse` runtime JS travels on the wire verbatim (byte-equality gate vs the
  in-proc bytes - B6's child has no copy); (3) ctl budgets MEASURED (round trip avg 73 µs / worst
  604 µs vs the 3 s `evalTimeout`) and therefore unchanged, noted at the constant; (4) `evalValue` got
  its own direct lane via a `directEvaler` seam; (5) shutdown backstops re-homed - the child force-exits
  itself, the daemon's backstop is child-kill (host stop + job object) plus a wedge watchdog on a
  blocked pipe write, so "webview wedged" means "child killed (+ relaunched)", never "daemon hangs".
  Crash → featurehost restart → full `patchMain` reattach (the virtualShell contract already made the
  UI derivable from state); liveness is a beat dispatched on the WINDOW's UI thread, so a wedged
  webview - not just a dead process - is detected. Governor inputs straddle the boundary without
  changing behaviour: window signals travel UP (daemon governor + eval gate; the DAEMON holds during a
  size-move, the child never buffers), `streaming` travels DOWN so the child's own governor reaches the
  same BELOW_NORMAL verdict. Screenshots do NOT cross the protocol - the HWND does, once, and the
  daemon PrintWindows the foreign window.
  Gates: the whole ctl suite (snapshot/click/tap/tap2/type/read/set/scroll/resize/act/tab/screenshot/
  screenshot-all) through a REAL child process, ordering through a real process, lane priority, cap
  policies, all three shutdown paths BY EXECUTION, runtime-JS byte equality, media session end-to-end
  over the real handler, plus one opt-in REAL WINDOWED smoke (`RAVE_MATE_WEBVIEW_SMOKE=1`). Wire schema
  untouched (regen = 0 drifted; 177 messages, hash 0x70698930).
  **Merge-time defect, FIXED:** `ctl screenshot`/`screenshot-all` returned solid-black PNGs for every tab
  under `RAVE_MATE_SHELL=proc`. Root cause was NOT the process boundary: featurehost spawns children with
  `sysexec.Hide` (SW_HIDE), Windows applies that to the process's first top-level window, so the child's
  WebView2 window came up HIDDEN - invisible UI and nothing for PrintWindow to return. Fix: the child
  reveals its window on ready (no focus steal; the daemon sends `show` on the first ready only, and
  `StartHidden` now actually works under proc), and the capture moved INTO the child over a new DIRECT-lane
  `screenshot`/`shotres` pair that carries a path + rect, never pixels (33-37 ms/capture vs the unchanged
  300 ms per-tab settle). Measured 0.00% -> 97.89% non-black on the real app; `screenshot-all` 10 tabs /
  0 errors. The gate that missed it was doubly weak - it skipped `sysexec.Hide` (so its child was never
  hidden) and asserted only "non-empty PNG" (a black file passes); both are fixed and the gate is
  falsified by execution.
- **P6 UI phase B7 (wire partition extension, in progress):** root ids **45-99** now belong to
  the remaining-tabs fan-out (the ~68 render surfaces B-2 left on the JSON bridge). First tab:
  overlays (ids 45-49, full + 4 fragments; UiStatus doubles as a root message) - dispatch -44%,
  documents 42.1% of the JSON, fuzz 382 715 cases. Registry: `zigui_wire_b7_test.go`. Still
  JSON-bridged: midi(4), vrchat family(22), worlds family(14), twitch(4), editor(2), dialogs_a(7),
  library modals/cueedit/fixers/remote/mirror/remotecue(12), motion pcv(2), publish remote(1),
  automations editor/runnow/schedules(3), update flow(1). Detail: ZIG_UI_GUIDE.md "Phase B - B7".
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
