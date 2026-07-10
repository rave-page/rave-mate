# Performance & the good-neighbour model

rave-mate runs on the same machine as your live rig - DJ software, OBS, SteamVR, Parsec. It is
designed to **never impair those apps**, especially a live stream, with **zero configuration**. The
safe, low-impact behaviour is the default; there is no "streaming mode" to switch on.

## What rave-mate does automatically

An internal *activity governor* (`internal/governor`) watches two things and adapts:

**1. Is a stream live on this machine?**
Detected via obs-websocket (`GetStreamStatus`, authoritative - OBS merely being open does not count)
with OBS process presence as a fallback. While a stream is live, rave-mate:

- **Keeps running (stream-critical):** Spout video output, peerlink media exchange (webcam/Spout
  shared to a paired instance), MIDI / now-playing sources (deck + track metadata for overlays),
  and the stream overlays. These feed the stream and are never paused.
- **Keeps recording your set:** the Icecast set-capture receiver keeps writing the broadcast to
  disk - your recording is never sacrificed.
- **Suspends non-essential heavy work:** audio fingerprinting / track identification (fpcalc /
  chromaprint), library indexing + sync sweeps, catalog hydration, and other periodic batch jobs.
  These are **deferred, not dropped** - e.g. fingerprinting a just-finished capture is queued and
  runs automatically when the stream ends.

**2. Are you actually looking at the rave-mate window?**
When the window is unfocused, minimized, or being dragged/resized, rave-mate pauses its own ~1 Hz
dashboard graph/tick refresh (nothing needs repainting when you can't see it) and drops the whole
process to **BELOW_NORMAL** priority so it yields the CPU to your foreground app and any encoder.

**Smooth window dragging.** WebView2 repaints the window on the CPU (GPU compositing is off - see
below), so while you drag or resize the window rave-mate additionally: (a) drops to BELOW_NORMAL for
the duration of the drag, and (b) *pauses* its heavy in-process batch loops - library sync merges,
set fingerprinting, and the motion-preview raster - the same way it does while streaming. They resume
the instant you let go. This keeps the window tracking the cursor even on a busy machine mid-sweep.
(Library search boxes also coalesce keystrokes ~150ms so a big filtered list re-renders once, not per
character.)

## Idle CPU discipline

Beyond the governor, every polling loop backs off when its target is absent or nobody is
watching:

- **OBS control** skips its per-second status poll while OBS is disconnected (the obs child
  keeps its own slow reconnect; direct LAN remotes redial throttled). Media-sync ticks are a
  no-op while sync is off.
- **Spout receive** polls at 4 ms only while frames flow; a quiet sender drops it to 50 ms.
- **Perf monitor** samples at 1 Hz only while a perf card / `ctl perf` was read in the last
  2 min, else every 5 s.
- **In-VR config flush** wakes only while edits are in flight (no idle ticking).

Absent services also don't spam the log: retry loops (OBS, Serato Live page, Twitch polls,
VRChat pipeline, peer dials, MIDI output) log the first failure and state changes only,
with a `suppressed` count on the next line. Log volume when idle ≈ startup lines only.

## Priority model

| Work | Windows priority |
|---|---|
| Main process, focused & idle & not streaming | NORMAL |
| Main process, streaming OR unfocused/minimized OR mid drag/resize | BELOW_NORMAL |
| Icecast set-capture child (background receive+write) | BELOW_NORMAL |
| Fingerprint / transcode / waveform / backfill ffmpeg jobs | IDLE |
| Actual audio playback decoder (the player) | NORMAL (real-time - must not starve) |

Lowering the whole main process covers every in-process worker goroutine at once, so none can
out-schedule OBS's (elevated) audio-encoder thread.

## GPU compositing

WebView2 (the Chromium engine behind the UI) hardware-accelerates by default, which makes its
compositor compete with NVENC for GPU/VRAM/PCIe bandwidth - the prime suspect for a stream's bitrate
collapsing while rave-mate is open. rave-mate therefore runs WebView2 with **GPU compositing off by
default** (`--disable-gpu --disable-gpu-compositing --disable-software-rasterizer`). The UI is a
light, Go-patched DOM (no video, cheap SVG), so software compositing is imperceptible.

Advanced escape hatch (default = off): set `features.ui.webviewGpu = true` in the config to re-enable
GPU acceleration if you prefer snappier UI over guaranteed non-interference.

rave-mate does **not** raise the global Windows timer resolution (`timeBeginPeriod`), which would
worsen scheduling machine-wide.

## Verifying the OBS-audio fix on your rig

1. Start rave-mate (webview UI) and your usual OBS profile (video 5000 kbps NVENC + AAC 320 kbps).
2. **Before** starting the stream, confirm in Task Manager: `rave-mate.exe` and
   `rave-mate-feature-icecast.exe` show **Below normal** priority when the window is unfocused;
   fingerprint/transcode helper processes show **Low**. WebView2 GPU usage should be ~0 (software
   compositing).
3. Start streaming in OBS. Within ~3 s, `rave-mate ctl logs` should show
   `governor: streaming changed value=true`. Confirm fingerprinting/indexing pause while Spout,
   peerlink, MIDI/now-playing and overlays keep working.
4. Play a full set at 5000 kbps and listen: the AAC audio artifacts and bitrate drop should be gone.
5. Stop streaming. `governor: streaming changed value=false` appears and any deferred fingerprinting
   ("background work deferred (stream live)" → runs now) resumes automatically.

If you still see contention at 5000 kbps, capture `rave-mate ctl logs` around the stream start and
check GPU usage of the `msedgewebview2` processes - they should be near zero.
