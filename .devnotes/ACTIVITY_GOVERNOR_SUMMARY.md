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

## Window-drag lag pass (2026-07, size-move / focus throttling)

The original governor only *fully* throttled when OBS was **streaming**. On a non-streaming machine a
window drag left every in-proc module goroutine at NORMAL priority, starving the software-composited
WebView2 window → the window trails the cursor (machine/module-dependent lag). WebView2 runs a Win32
modal move loop on a LockOSThread'd thread and renders CPU-side (GPU compositing is OFF, see §1), so
CPU contention alone produces visible drag lag. Fix = fold **size-move** (and focus) into the same
throttles the streaming path already uses:

- **Process priority** (`governor.apply`): below-normal now also when `sizeMove` (was: streaming ||
  minimized || !focused). A drag drops the whole process to BELOW_NORMAL for its duration.
- **`BackgroundAllowed()`** now returns false during `sizeMove` too (was: streaming only). So all
  existing `WhenBackgroundAllowed`-gated work (fingerprint/identify deferral) also parks mid-drag.
- **Heavy in-proc sweeps consult the governor per iteration.** New `governor.WaitWhileBusy(ctx)`
  parks a loop in 150ms slices while background work is disallowed (streaming OR mid-drag). Wired at
  the head of: libsync merge loop + tag-apply loop (`internal/libsync/engine.go`, amortized every
  1024/256 items over the ~23k-track library) and the setfp per-track fingerprint loop
  (`internal/setfp`). Yields the CPU to the UI thread during a drag; resumes automatically.
- **Motion preview** (`internal/webui/motion_actions.go` `moRunPreview`): the ~15fps CPU
  raster→JPEG→DOM-swap now skips when `inSizeMove() || !governor.UIAnimAllowed()` (mirrors livePush).
  The OSC/VMC network output loop is left running - it's cheap UDP output, not a CPU/DOM offender.
- **Library search debounce** (`internal/webui/library_actions.go`): `lib-search` + `lib-coll-search`
  coalesce keystrokes ~150ms before the full filtered-list re-render, so a 23k-row innerHTML swap
  doesn't fire per keystroke.

Streaming behaviour is unchanged - size-move/focus are *additional* throttle triggers, never a
replacement for the streaming one.

## Related fail-closed hardening (media plane)

Independent of drag lag but shipped alongside: when `MediaLink.Subprocess` is set but the memory-
capped media child fails to spawn, the daemon used to silently fall back to running the
frame-churning media route/webcam plane **in-proc, ungoverned** (the exact pattern that once OOM'd a
host + killed Parsec). It now fails **closed**: `mediaInProc` stays false, the route/webcam ctls stay
nil (UI renders nothing, already nil-guarded), and an error is logged. In-proc only runs when
isolation was never requested (config default).

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
  process-priority + GPU + UI ticks. Library sync sweeps + set fingerprinting now also carry
  explicit `governor.WaitWhileBusy` gates (2026-07 pass above); catalog hydration in rave-mate is a
  one-shot on-demand DB load (`libEnsureTracks`), not a continuous sweep, so it rides the
  process-priority drop rather than a per-iteration gate.
- Wiki page mirroring docs/PERFORMANCE.md to be published (separate wiki repo; not pushed from the
  sandbox).
