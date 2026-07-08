# Activity governor (good-neighbour perf) - design summary

Problem: rave-mate degraded users' live OBS streams (AAC audio artifacts + video bitrate drop at
5000 kbps NVENC; fine at 3500 or with rave-mate closed). Root causes, ranked:

1. **GPU contention (top).** WebView2/Chromium compositor hardware-accelerates by default and
   competes with NVENC for GPU/VRAM/PCIe bandwidth. Explains the 3500-ok/5000-bad boundary (higher
   NVENC bitrate needs more GPU headroom) and the whole-stream symptom (encoder stalls → mux/audio
   thread starves too). Fix: WebView2 GPU compositing OFF by default via env
   `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--disable-gpu --disable-gpu-compositing
   --disable-software-rasterizer` (shell_cgo.go). Escape hatch: `features.ui.webviewGpu=true`.
2. **Process priority.** Every featurehost child + the main process ran at NORMAL_PRIORITY_CLASS
   (host.go:390 `AssignToJob(_, false)`), competing with OBS's elevated audio-encoder thread. No
   `SetPriorityClass` existed anywhere. Fix: `sysexec.SetSelfBelowNormal` (governor drops the whole
   process to BELOW_NORMAL when streaming or unfocused), `sysexec.BelowNormalPriority` +
   `featurehost.Options.LowPriority` (Icecast capture child → BELOW_NORMAL). Existing `LowPriority`
   (IDLE) already covered fingerprint/transcode/waveform/backfill ffmpeg.
3. **Non-essential CPU during a stream.** Fingerprinting (fpcalc/chromaprint via setfp), library
   sweeps, catalog hydration are the CPU offenders that starve the AAC encoder. Fix: governor
   suspends them while a stream is live; the fingerprint+identify of a finished capture is deferred
   (`governor.WhenBackgroundAllowed`, keyed, re-run on stream end). Icecast CAPTURE keeps recording.
4. **UI tick loop.** livePush already skipped during size-move but not when unfocused/minimized.
   Now gated on `governor.UIAnimAllowed()` (paused when hidden/unfocused/dragging/streaming). No code
   raises the global timer resolution (verified: no `timeBeginPeriod` in rave-suite).

## Architecture

`internal/governor` = single source of truth. Signals: Focused/Minimized/SizeMove (fed from the
webui Windows window subclass - WM_ACTIVATE/WM_SIZE/WM_ENTER|EXITSIZEMOVE) + Streaming (fed from
`app.watchStreaming`, 3s poll: obs-websocket `GetStreamStatus` authoritative, OBS process presence
fallback). Applies process priority on every transition; releases parked background work when a
stream ends. Zero config; safe-by-default. No toggle - only an advanced GPU escape hatch whose
default is the low-impact behaviour.

## Verification done in-sandbox

- `GOWORK=off go build ./...`, `go vet`, `go test` on changed packages: green.
- New `governor` unit tests cover UI/background gating + defer-and-resume + key dedup. (Found +
  fixed a real bug: `wasStreaming` was read after the aliased field was mutated → deferred work
  would never resume.)
- Could NOT reproduce the real OBS+NVENC scenario (no OBS/GPU encode in sandbox). User checklist in
  docs/PERFORMANCE.md.

## Follow-ups / not-yet-wired

- Streaming-gate is wired at the highest-value site (fingerprint/identify deferral) +
  process-priority + GPU + UI ticks. Library sync sweeps / catalog hydration / tagsync / assetsync
  already run their ffmpeg at IDLE priority and drop below-normal with the main process while
  streaming; explicit `BackgroundAllowed()` gates on their schedulers are a cheap follow-up if any
  still shows up in a stream-time CPU trace.
- Wiki page mirroring docs/PERFORMANCE.md to be published (separate wiki repo; not pushed from the
  sandbox).
