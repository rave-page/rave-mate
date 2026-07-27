# Route panel: counters that lied (2026-07-27)

Branch `fix/route-counters-honest` off `development` @5c43ec2. Two field defects, both
operator-facing halves of "a shape that cannot express failure hides it".

## Defect 1 - the route panel froze while the route ran

**Not** a version-keyed render cache. `internal/webui/ui.go` `livePush` gates the WHOLE ~1 Hz tick
on `governor.UIAnimAllowed()` = `focused && !minimized && !sizeMove && !streaming`. Two
consequences, both live during the incident:

- `app.watchStreaming` (app.go) sets `governor.Streaming` from `streamLive()`, which falls back to
  **OBS process presence** when obs-websocket is not connected. OBS open ⇒ the tick is off for the
  whole session.
- Driving the app from a terminal via `ctl` means the rave-mate window is not focused ⇒ tick off.

`ctl snapshot` reads the live DOM (`webui/control.go` → `window.__snapshot`), so a stale DOM gives a
stale snapshot indefinitely. The occasional jump = a `ctl tab`/action running `patchMain` (a full
re-render), then frozen again. The Fyne renderer is unaffected (`ui/view_peers.go` refreshes on an
unconditional 2 s ticker); the daemon-side mirror (`featurehost/mediaproxy.go`) and the child's 1 Hz
`emitTelemetry` were always fresh - only the DOM was old.

**Fix.** `livePushOnce` gains a narrow exemption next to the existing cue-edit one: when the general
gate is closed and the Peers tab is on screen and `Media.Stats()` is non-empty, patch the single
fragment `#peers-media`. The media block got its own patch target in BOTH renderers
(`peerMediaHTML` / `renderMedia` in `native/zigui/src/peers.zig`, byte-identical - the Zig golden
pins the wrapper). No cache disabled, no tab repaint, nothing on the actWorker.

## Defect 1b - the rate was read-driven, and had no staleness

`routeStat.snapshot()` computed `rateBps` **inside the read**, refreshing a shared anchor. The value
therefore depended on who polled and how often: with the UI tick paused, the first read after the
pause divided the whole idle gap into "live bitrate"; with two pollers at different phases a reader
got a window it never measured. It also never decayed - a route that stopped kept advertising the
bitrate it had when it died.

`count()` now closes a 1 s window on the route's own goroutine (producer-driven); `snapshot()` is a
pure read that reports 0 once no frame has been counted for `rateStale` (3 s).

**Not found: an independent multiplicative error.** With the media plane in its child,
`router.Stats()` is called by `emitTelemetry` (1 Hz) and `mediaroute.cleanup` (0.5 Hz); every closed
window measured its own bytes over its own span, so the old code was not systematically ~9.5× low.
The 0.1 Mbps beside "3467 frames · 9.8 MB" is consistent with a frozen snapshot whose elapsed time
was ~15 min, not ~90 s - the 37-40 fps used to derive "≈950 kbps" is the ENCODER's `OutFPS`, a
different counter from the wire. That ambiguity was itself a missing instrument, so `RouteStat` now
carries **`WireFPS`** and both panels render `Mbps · wire N fps` beside `out N fps`. An encoder at
40 fps over a wire at 4 is now one glance, not an inference.

## Defect 1c - an epoch timestamp rendered as a latency

`latency 1785118072019.6 ms` = 1.785e18 ns = the wall epoch. Mechanism:
`recvMedia` computes `transit = mediaclock.Now() − f.PTS`. `SoftwareClock.Now()` is
`time.Since(processStart) + offset` (~1e12), while the native encode path stamps PTS on the **wall
epoch** (`mediapipe/mf_bridge.go feed` → `time.Now().UnixNano()`; zero-copy →
`mfenc/procparent_windows.go` `cmd.PTS0`). `transit` is therefore ≈ −1.785e18, and `fmtMs` takes the
absolute value - which is also why the panel printed p50 LARGER than p95 (more-negative sorts first).

Two fixes, because either alone leaves a hole:

1. **`runSend` owns the PTS domain.** A non-zero source PTS is rebased once per route onto the media
   clock (`shift = clock.Now() − firstPTS`, carried on every later frame), so inter-frame deltas -
   the jitter buffer's pacing input - survive. The jitter buffer was already domain-agnostic (it
   works off min-transit), so nothing there changes. Cost: the e2e figure no longer includes encode
   latency, which is separately reported as `LatP50Ms/LatP99Ms` in the same line.
2. **The receiver refuses to believe an implausible transit** (`[-2s, +60s]`). Rejects are counted
   in `LatUnsynced`, and with an empty window both renderers print `off-clock` / `n/a` instead of a
   duration. That covers a peer on an older build, which the rebase cannot.

## Defect 2 - throttling summed into "dropped"

`mediaroute`'s shared capture does ONE readback per sender at `rateOf(subs)` = the MAXIMUM cap of
all attached routes (uncapped wins); each route re-applies its own cap in `spoutSource.Next`
(`minGap`) and discards the surplus before any encode/crypto cost. Those discards went into
`PipelineStats.Dropped` via `medialink.InnerDrops`, so `dropped:41902 and climbing` on a healthy
60→40 fps route was indistinguishable from catastrophic loss.

- `PipelineStats.RateCapped` added beside `Dropped`. **`Dropped` stays the TOTAL** (documented);
  `RealDrops()` = `Dropped − RateCapped` is the real loss.
- `medialink.InnerRateCapped` rides the wrapper chain exactly like `InnerDrops`; threaded through
  `mediapipe` `encoder`, `decoder`, `mfBridge` (native + substituted) and `mfDecoder`.
- Both renderers print `· rate-capped N` and `· dropped N` as separate segments; a purely capped
  route shows no drops at all.
- `route encode telemetry` gains `rateCapped` + `lost` beside the `dropped` total.
- `capture shared` is now logged on EVERY attach (was: only `n > 1`) with `routes`, `captureFps`,
  this route's `maxFps` and `routeFps` = every live subscriber's cap. Which route holds the capture
  open at 60 is now readable instead of inferred from the discard rate.

## Gates (non-vacuity proven by reverting only the fix)

| gate | proves | fails without the fix |
|---|---|---|
| `webui.TestRouteCountersAdvanceWhileStreaming` | rendered counters advance across ticks with `Streaming=true` | yes - `no route-counter patch while streaming: ""` |
| `medialink.TestRouteReportsFlow` | a wall-epoch source PTS still yields plausible e2e samples | yes - `latency window holds 0/16 samples`, `LatUnsynced:16` |
| `medialink.TestForeignPTSDomainIsNotALatency` | off-clock transits are counted, never averaged | yes - `an off-clock PTS was accepted as a latency sample (8)` |
| `medialink.TestRateWindowIsProducerDriven` | rate/fps measured without any reader; pure snapshot; stale decay | yes - `WireFPS = 0.0` (old code sets an anchor and returns 0) |
| `mediaroute.TestSpoutSourceFPSCapDrops` / `TestSpoutSourceReportsFPSCapDrops` | fps-cap discards arrive tagged, `RealDrops()==0` | yes - the field did not exist |
| `ui.TestFmtPipeLine` / `TestFmtRouteStat` | capped route renders no drops; no-sample latency is not a duration | yes |
| `zigui` peers golden | `#peers-media` wrapper exists in the Zig renderer too | yes |

## Neighbouring defects found, NOT fixed

1. `zig fmt --check native/zigui/src/` fails on `root.zig` and `wire_gen.zig` - **pre-existing on
   `origin/development`** (verified against `git show origin/development:...`). Not touched here.
2. `governor.UIAnimAllowed` gating the whole tick is a general hazard, not a Peers-tab one: any
   panel whose numbers matter DURING a stream (Live cockpit graphs, Publish progress) is frozen the
   same way. This branch exempts only the media routes; the pattern needs a decision, not more
   one-off exemptions.
3. `watchStreaming` treats "OBS is running" as "a stream is live" when obs-websocket is not
   connected. Conservative for CPU shedding, but it means the UI-freeze path is armed for anyone who
   simply has OBS open.
4. `mediapipe`'s `rate` helper (`OutFPS`/`CapFPS`/`DecFPS`) has the SAME read-driven window the
   route rate had: `rate.value()` closes the window on read, so a 10 s `routeTelemetry` line reports
   whatever span the last unrelated reader closed. Left alone here (it is inside the child's own
   stats and out of this branch's blast radius), but it is the same bug class.
5. `spoutSink.Write` returns `nil` for a dropped frame - a volume-shaped operation behind a
   boolean/error-shaped contract, the exact rule this branch's framing calls out. It does count the
   drop, so it is visible, but the contract still says "fine".
