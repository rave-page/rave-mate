# UI Render Architecture Analysis (ui-render-arch worktree)

Scope: internal/webui (default renderer) + Gio player + daemon coupling. Fyne excluded (deprecated).
Inputs: 4 code-mapping passes + 3 architecture proposals + 3 independent judges. Flaws below pre-verified against source.

## 1. Executive verdict

**Winner: decoupled-go ("snapshot renderer") — unanimous, 3/3 judges (totals 32/30/31 vs hybrid 26/25/26, js-render 24/24/23).** It is the only proposal addressing the FULL confirmed flaw set — including the daemon-integrity failures the others skip: terminate()'s os.Exit(0) dropping live streams/bbolt state on quit, recorder r.mu fsync contention on render paths, eventbus fanout parking peerlink readers, and the unsynchronized *config.Config race. It stays inside every non-negotiable (ctl parity byte-identical, i18n Go-only, no framework, single daemon) while finally making the bounded-queue rule true on the UI path, and it converts the no-I/O-in-handlers convention into compile-time structure (HandlerCtx: handlers can only Snapshot/Post/Job/Dirty/Toast — blocking becomes impossible, not discouraged). Its week-1 slice (gated ack'd flusher + bounded job pool + terminate fix + redispatch fix) is mechanical, touches no renderers, and alone retires every critical and most high findings. Concession: waveform drags run ≤30 fps Go-rendered overlay vs 60 fps client canvas — judges 1 & 3 note hybrid's canvas island remains a composable surgical follow-on for that one surface if measured drag latency on the OBS-contended reference machine misses budget. js-render loses on rule reversal + vendored-JS supply chain in a public AGPL repo + no TS in a zero-any culture + 3-month dual-mode window (the Fyne→webview precedent stalled once already). hybrid loses because it explicitly punts the whole daemon-blocks-ui family — record toggle, play/pause, settings toggles stay frozen paths after full effort spent.

## 2. Confirmed blocking/responsiveness flaws

All pre-verified. Duplicate reports of the same defect merged into one row (`×N` = times independently confirmed).

| Sev | Location | Mechanism | User impact |
|---|---|---|---|
| CRIT ×3 | render_library.go:307 (callers library_actions.go:951, 1199, 1448) | libTracksBlocking `select{<-done, <-time.After(30s)}` on actWorker; acts chan (cap 64) fills then drop-newest | Relocate / smart-playlist / rule-modal before hydration = whole app dead ≤30 s, input silently discarded |
| HIGH ×3 | render_settings.go:1438-1455 (+ module/manager.go:130, host.go:234) | applyToggle: saveCfg disk write + Session.Reconcile + Modules.SetEnabled sync module start/stop (featurehost child spawn / UNBOUNDED child-exit wait) on actWorker | Any settings switch freezes all UI seconds — or indefinitely on a hung child; ~30 handlers share saveCfg pattern |
| HIGH ×3 | player_actions.go:1082, 1128 (mpWaveSVG player.go:162-231) | Each ~60 Hz pointermove rebuilds 40-80 KB 500-col SVG on actWorker + 1-2 ExecuteScripts; no coalescing; per-fragment eval = documented historic stutter class | Waveform trim/pan stutters, trails pointer; degrades to slideshow on long tracks |
| HIGH ×2 | ui.go:400 + shell_cgo.go:206 | Only livePush gated on inSizeMove/governor (ui.go:380); action patches, toasts, bg completions, browse 2 s re-read, ctl evals ExecuteScript inside WM_ENTERSIZEMOVE modal loop | Window drag/resize hitches whenever any async completion lands mid-drag — residual of the historic incident |
| HIGH ×2 | player_actions.go:290, 341 (playerproxy.go:230-269) | TogglePause/Stop = blocking stdio RPC to audioengine child, 3 s timeout, inline on actWorker (Play already bg'd) | Play/pause tap dead up to 3 s; all other input stalls behind it — the mpv tap-freeze class, reintroduced |
| HIGH ×2 | shell_cgo.go:186 (app.go:1627-1629, 1460-1474) | terminate() 1.5 s watchdog os.Exit(0) BEFORE app shutdown() — mods.StopAll, bbolt Close, stream End (8 s budget) all skipped when WebView2 unwinds slowly (its documented-unreliable case) | Quit during live session can drop the stream server-side, cut recorder/libdb bbolt mid-transaction |
| HIGH ×2 | render_library.go:668 (+ :603/:608, :718) | Every chip/sort/checkbox click: distinctCounts ×2 + full 23k filter + sortIdx + 300-row rebuild on actWorker; FilterSmart per smart playlist per render; only search debounced | Library feels rubbery; scroll resets; rapid clicks queue/drop |
| HIGH | shell_cgo.go:155 | acts cap-64 drop-newest can discard pointer 'up'; v.drag only cleared on up; mpTick refuses refresh while drag latched | After fast scrub player appears frozen (no playhead/clock), handle stuck to phantom drag |
| HIGH | actions.go:211-223 (audiorec.go:220-248) | arecToggle: ffmpeg 'q' + ≤6 s wait + KillTree + finalize (cue/tag/libdb) inline on actWorker | Record toggle freezes all UI up to 6+ s — mid-set critical control |
| HIGH | shell_cgo.go:206 (webview_go dispatch map) | Go→page Dispatch closures stored in global UNBOUNDED map; no producer backpressure; governor gate itself depends on the hung pump | Hung WebView2 thread → daemon RSS grows without bound → stream/recorder/featurehost supervision OOM with it |
| HIGH | pick_actions.go:65-71 | redispatch re-enters onAction on picker bg goroutine — two handlers concurrent; races mutex-less *config.Config against livePush/ctl reads | Flaky settings corruption, stale toggles, potential crash after any file-picker flow |
| HIGH | ui.go:318-323 | patchMain: 83-84 call sites rebuild ENTIRE tab (1900-line renderers) + innerHTML-swap #main per click; clears tick dedup cache | Every click: full-page flash, scroll-to-top, focus loss, tens-of-ms serial build on actWorker |
| MED | shell_cgo.go:87-98 | actWorker single serial goroutine; umbrella for all sync-handler findings; feeds the drop path | UI-wide dead clicks for any handler's duration |
| MED | live_ticks.go:45-50 | Logs tick bypasses tickPatch: rebuilds+swaps 400 lines/s unconditionally (ring re-filter, re-escape); parse/layout on UI thread | 1 Hz jank on Logs tab; text selection wiped every second |
| MED | twitch_actions.go:167-172 + live_ticks.go:53-54 | Twitch (2 evals/s), automations, appgroups ticks bypass tickPatch batching+dedup — un-deduped per-fragment evals, the exact historic stutter mechanism | Per-second layout churn while idle; hover/selection reset; stutter when landing mid-interaction |
| MED | shell.go:99 | __patch = bare innerHTML swap, no diff; focus/scroll/selection/pointer-capture survival is unwritten convention (fragment ids, lanes, debounces) | Any patch over a focused input/scrolled list destroys state; new views regress by default |
| MED | recorder.go:548-555 | persistLocked runs bbolt fsync while holding r.mu; render paths (render_publish.go ×5, player_actions.go:448) take same mutex; List() full bucket scan per render | Publish tab hitches for a disk fsync at every track confirm while recording |
| MED | actions.go:125-131 (recorder.go:394-416) | recFinish: StopRecording = 2 bbolt fsyncs under r.mu + full-tab patchMain, inline on actWorker | 'Finish set' = visible multi-hundred-ms freeze; queued clicks land on stale DOM |
| MED | eventbus.go:358-368 (render_twitch.go:33-60, app.go:611) | fanout runs webui subscribers sync on publisher goroutines; twMu also held during 250-row feed builds; peerlink HandleData feeds Bus.Inbound directly | Peer-link frame reads + twitch ingest inherit UI render latency; wedge amplifier |
| MED | ui.go:188-192 (reconcile.go:231) | RefreshRecordings runs full Publish render (bbolt reads + HTML build) on AutoReconciler goroutine, unserialized vs actWorker/ctl | Recording auto-link delayed by render durations; concurrent-render state risk |
| MED | control.go:146-178 + ui.go:114-145, 349-365 | Four goroutine families (actWorker, ctl, tray, livePush) drive setTab/patchMain/render state concurrently; no marshaling; patchMain clears dedup mid-tick | ctl screenshot-all / tray yanks active tab; stale-over-fresh fragments; race-panic risk |
| MED | actions.go:16-20 | u.bg = unbounded bare `go` (114 sites), no dedup, actx not derived from u.stop; completions Dispatch after webview Destroy | Double-click = duplicate backend ops; shutdown races / post-destroy crash risk |
| MED ×2 (+LOW dup) | settings_actions.go:1004, 1014 (also :584 VCC scans) | tcExtraModal enumerates winmm WaveOut/MidiOut devices sync on actWorker before modal opens; no probes-cache coverage | 'Extra outputs' click dead for driver-enumeration seconds — worst right after USB audio replug mid-gig |
| LOW | shell_cgo.go:110-121 (governor.go:66-75) | GPU compositing force-off on stale 'no video' assumption while player hosts software <video>; governor drops process to BELOW_NORMAL mid-drag incl. pump thread | Choppy drags with player visible on CPU-loaded (OBS-streaming) machines |
| LOW | control.go:146-178 | ScreenshotAll: setTab renders + 300 ms sleeps + 3 s eval timeouts per tab on ctl goroutine (~16 tabs) | ctl sweep pins control plane ~1 min against a busy UI; flips tabs under the user |
| LOW | live_ticks.go:49 (logbus.go:107-119) | Logs tick copies full 5000-entry ring under RLock/s; Bus.Log writers block behind copy | 1 Hz jitter injected into every logging daemon goroutine while Logs tab open; scales with ring cap |

## 3. Proposals + judge scores

| Proposal | Core idea | J1 | J2 | J3 | Σ |
|---|---|---|---|---|---|
| **decoupled-go** | Keep Go-rendered HTML; split into 4 bounded stages: uistate.Store (atomic immutable Snapshot + delta ingest), intent loop (HandlerCtx-only reducers — blocking structurally impossible), uijobs (4-worker pool, singleflight, ctx from u.stop), renderq (ONE render goroutine, dirty-regions) + ack'd one-in-flight gated patch flusher. terminate() → shutdown-first. ~30 lines JS growth, no framework. | 32 | 30 | 31 | **93** |
| **hybrid** | 3 vanilla-JS islands (liblist/player-canvas/logtail, go:embed, zero npm) + evalScheduler (bounded/coalesced/gated). Best point-fixes for waveform (60 fps canvas) + library (virtualized). Explicitly does NOT touch daemon-blocks-ui family. | 26 | 25 | 26 | 77 |
| **js-render** | Full reversal: vendored Preact+HTM view layer, Go = StateHub topics + CommandRunner; keyed diffing; client-side hot paths. Best end-state responsiveness (9/10 all judges) but rule reversal, JS supply chain in public AGPL repo, no TS, ~3-month dual-mode migration; leaves terminate/r.mu/cfg-race unfixed. | 24 | 24 | 23 | 71 |

No winner split — all three judges (live-DJ persona, maintainer persona, perf-engineer persona) picked decoupled-go independently. Sub-score splits: J2 docks decoupled-go maintainability to 7 (bespoke Elm-in-Go Snapshot machinery) where J1/J3 give 8; J1 gives hybrid migration_risk 8 vs J2/J3's 7; all agree js-render's responsiveness 9 is real but priced wrong. J3's composability note: decoupled-go + hybrid's canvas island compose; js-render composes with nothing.

## 4. Recommended path

### Phase 0 — no-regret fixes (parallel, disjoint files, days)
Correct under EITHER final architecture (both decoupled-go's uijobs and js-render's CommandRunner require exactly these handler conversions; all three proposals converge on the gated bounded flusher). Ordered by value:

1. **library_actions.go** — libRelocate (:951) + smart-playlist paths (:1199, :1448): stop calling libTracksBlocking on actWorker; run in bg with busy-guard + "resolving…" toast/state, re-patch on completion. Kills all 3 criticals.
2. **render_settings.go** — applyToggle (:1438) + saveCfg callers: render optimistic "pending" switch immediately; run saveCfg + Modules.SetEnabled in guarded bg job; confirm/revert patch on completion.
3. **player_actions.go** — TogglePause (:290) / Stop (:341) off actWorker with optimistic icon flip (the shipped mpv-path fix pattern); coalesce drag 'move:' (drain-to-newest before render, skip stale); defensively clear drag state on 'down' so a dropped 'up' can't latch.
4. **actions.go** — arecToggle (:211), recFinish (:125), autoToggle/autoDelete (:39) → guarded bg with pending state; derive u.actx from u.stop so Stop() cancels in-flight bg work.
5. **ui.go** — replace direct u.eval dispatch with bounded per-fragment newest-wins queue + single flusher gated on inSizeMove()/governor.UIAnimAllowed(), flush on exit-size-move; RefreshRecordings (:188) enqueues instead of rendering on the reconciler goroutine.
6. **shell_cgo.go** — terminate() watchdog: invoke injected graceful-shutdown hook (app.go wires shutdown()) before a 10 s whole-process force-exit backstop; never bare os.Exit(0) at 1.5 s.
7. **pick_actions.go** — redispatch (:70) re-enters via the acts pipeline (never direct onAction from bg goroutine); restores the serialization invariant.
8. **settings_actions.go** — tcExtraModal (:1004/:1014) + VCC scan (:584): open modal/panel instantly with spinner; probe in guarded bg; patch on completion.
9. **live_ticks.go** — route logs/automations/appgroups ticks through tickPatch dedup (skip unchanged HTML); logs: build only on ring-seq change.
10. **render_twitch.go** — subscribers append under a short-held lock only; feed renders snapshot rows first, build HTML outside twMu (decouples peerlink/twitch publishers from render cost).
11. **recorder.go** — persist outside r.mu (snapshot state, unlock, PutJSON); render-facing Active()/Get() never wait on a bbolt fsync.
12. **control.go** — ScreenshotAll/SelectTab post tab intents through the act pipeline (and restore prior tab after sweep) instead of driving setTab/patchMain on the ctl goroutine.

Phase 0 alone retires every critical and most high findings, in one language, zero parity risk.

### Phase 1 — foundation (week 2)
Formalize uijobs (bounded pool, per-key singleflight — replaces ad-hoc busy flags from Phase 0); ack'd one-in-flight flusher (page acks via existing __rave_evalResult machinery; ack-timeout + renderer-gone detector) — makes the Dispatch map truly bounded; immutable ConfigView republished on save (fixes the cfg race Phase 0 only narrows); failure-delta audit checklist for every optimistic state introduced in Phase 0.

### Phase 2 — uistate.Store + renderq (weeks 3-4)
Single atomic Snapshot + delta ingest chan (cap 128); convert daemon callbacks (twitch, reconciler, recorder, Notify) to PostDelta; stand up renderq (ONE render goroutine, dirty-region coalescing, universal per-fragment dedup); migrate live + settings + library tabs first, hard cutover per tab; delete livePush/live_ticks direct evals per migrated tab. Snapshot discipline: share tracks slice immutably, copy only visibleIdx/selection — else ingest becomes the new bottleneck.

### Phase 3 — hot surfaces (weeks 4-5)
Library: 23k filter+sort as uijobs derivation writing visibleIdx; checkbox clicks dirty lib-row-<i> fragments only. Waveform: static wave fragment per track/zoom + ~200 B overlay fragment for drags; moves via latest-wins mailbox (cap 1) sampled ≤30 fps; down/up on guaranteed cap-8 chan (bounded ≤5 ms block, then drop+log — never re-freeze the pump). __patch grows focus/selection/scroll preservation for [data-keep-scroll] + logs append op (capped, reviewed against the rule). Logs via logbus subscription deltas.

### Phase 4 — enforcement + parity (week 6)
HandlerCtx signature sweep over dispatch.go registrations (Snapshot/Post/Job/Dirty/Toast only — no *UI, no svc); ctl intent routing with reply chans (re-verify 300 ms settle + OVERFLOW semantics so verify-rave-mate-ui keeps passing; ctl evals bypass the size-move gate); CI grep gates: no u.eval outside renderq/flusher/control.go, no svc access in handlers; full ctl screenshot-all + snapshot pass on all 16 tabs.

### Contingency
If measured pointer-drag latency on the OBS-contended reference machine still misses budget after Phase 3: adopt hybrid's player canvas island for that ONE surface (client-side 60 fps, peaks via mediahttp /data/ route, data-value ctl mirrors). The proposals compose; do not pre-commit.

### Gio player
Retires with Fyne: playerwin/giokit/mpvembed's only caller is internal/ui/view_player.go:632 (deprecated path); webview HTML player + audioengine child is canonical. Preserve two lessons in the webview path: never create child windows / issue sync IPC from a render-owning thread (mpvembed/host_windows.go:146 deadlock; mpvplayer.go fire-and-forget contract), and the deckclock PLL idea for playhead interpolation.

## 5. Rule changes required (rave-mate CLAUDE.md)

1. **JS runtime scope**: amend "the only JS is one small transport/introspection runtime" to enumerate allowed duties incl. __patch focus/selection/scroll preservation + logs append op. Still no framework, no web server; any further JS addition requires explicit rule review.
2. **Handler contract (replaces the implicit no-I/O convention)**: action handlers receive HandlerCtx only (Snapshot/Post/Job/Dirty/Toast); direct svc access, u.eval, and blocking waits in handlers banned + CI-grepped.
3. **Single egress**: all Go→page output flows through renderq + the gated ack'd patch flusher; u.eval private to renderq/flusher/control.go. This makes the suite bounded-queue non-negotiable actually true on the UI path (ingest cap 128, patch queue 64 keys/4 MB newest-wins, jobs cap 32).
4. **Concurrency doctrine**: renders read only immutable uistate Snapshots; daemon code talks to the UI exclusively via PostDelta — never UI mutexes, never direct patchMain/RefreshRecordings. Update app.go frontend-seam docs.
5. **verify-rave-mate-ui skill notes**: SelectTab/ScreenshotAll now serialize through the intent loop; ctl evals bypass the size-move gate; re-baseline screenshot-all timing.

Interim (Phase 0, before renderq exists): "handlers must not perform disk/DB/RPC/device I/O or waits — bg job + optimistic state; every Go→page eval goes through the gated flusher" goes in CLAUDE.md immediately so parallel work doesn't reintroduce the pattern.
