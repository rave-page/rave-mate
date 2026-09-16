# Advanced schedule start conditions

Automation schedules gained two new trigger kinds + condition gates, on top of the existing
interval/daily. Edits flow over remotectl too (remote schedule editing gets them free).

## Trigger kinds (`Schedule.Kind`)
- `interval` - every N minutes (existing).
- `daily` - at HH:MM local (existing).
- **`cron`** - 5-field cron expression (`CronExpr`), e.g. `*/15 * * * *`, `0 9 * * 1-5`.
- **`idle`** - fires once when the system has been idle ≥ `IdleMinutes`; re-arms when active again.

## Condition gates (any kind; empty = off)
- `RequireIdleMinutes` - only fire if the system has been idle ≥ this.
- `RequireAppsRunning` - only fire if ALL listed apps are running.
- `ExcludeAppsRunning` - skip if ANY listed app is running (e.g. don't transcode while Traktor is open).

Idle + running-app detection is **Windows-only** today and **fails open** elsewhere (the gate is
ignored so automations still run on mac/linux).

## Implementation
- `internal/automation/cron.go` - minimal stdlib 5-field cron parser (`*`, lists, ranges, steps;
  Vixie dom/dow OR semantics) + `ValidateCron` for UI validation. No soak-gated dep.
- `internal/sysactivity` - `IdleDuration()` + `RunningProcesses()`. Windows: `GetLastInputInfo`
  (user32) + Toolhelp process snapshot (kernel32) via stdlib syscall (mirrors `internal/midi`
  winmm; no new dep). Non-Windows: no-op (`ok=false`).
- `internal/automation/scheduler.go` - interval/daily keep precise timers but route through
  `fireGated`; a single 30 s eval loop drives cron (minute-deduped) + idle (once per idle period).
  `gateBlock` consults `sysactivity` and fails open where unsupported.
- UI (`view_automations.go`) - the schedule editor's KIND adds cron/idle (+ a Conditions section);
  the schedule list shows kind + gate hints (`⊘exclude ⊙require ⏾idle`).

## Verification
- Tests: cron parse/match (incl. dom-OR-dow, 0/7 Sunday), gate matrix, cron per-minute dedupe,
  idle once-per-period + re-arm, app-gated idle; Windows live idle+process snapshot.
- `ctl`: schedule editor dialog renders, 0 overflow.

## Run coordinator (2026-09-16)

`internal/automation/coordinator.go` serializes + prioritizes every run so a schedule fire and a
manual "Run now" behave identically and stay a good neighbour. All run paths funnel through it:
schedule sweep (`onSchedule`), watch-file (`onWatchFile`), manual sweep (`RunSweep`), manual file
(`RunManual`) and the attended interactive `StartRun`.

Rules:
- **No self-overlap.** A sweep of an automation already running (or already holding a pending
  sweep) is COALESCED into ONE pending sweep, recorded "coalesced at HH:MM" on the card/schedule
  row - never dropped, never multiplied. A single-file run (`manual-file`) serializes behind a
  conflicting run but is never coalesced (a file the user hand-picked is not a duplicate of a sweep).
- **Path serialization.** Two automations whose watch dir - or an output/move/copy target dir - are
  equal or nested never run at once; the second waits (reason `path`, "waiting for <label>").
- **Resource classes.** A chain with a heavy action (transcode/trim-silence -> spawns ffmpeg) takes
  one of `MaxHeavyRuns` heavy slots (default 1) AND defers while the governor says background work
  is off (a live stream) - reusing `governor.BackgroundAllowed`, never a second gate. Light chains
  (move/copy/rename/delete) run alongside. `chainClass` derives the class from the action types.
- **Bounded queue.** Pending runs are capped (`queueCap`, 64) and coalesced per automation, so the
  queue can never grow with load; an over-cap submit folds. Queued runs are cancellable +
  context-aware - a run whose context is cancelled (the window closed) is dropped from the queue.

A tiny dispatcher goroutine owns the queue; jobs run on their own goroutines. `Status()` exposes
running + queued (with reason codes the webui maps to i18n) for the UI status region; every state
change bumps the Service version so the ~1 Hz Automations tick repaints the status region + card
badges. `RunSweep` returns immediately with `Queued`/`Coalesced` when it cannot start now (the run
happens async, surfaced by the tick) and blocks for the per-file `Runs` only when it started right
away; `onSchedule`/`onWatchFile` submit fire-and-forget (the shared scheduler eval loop is never
blocked on a run). `CoordConflict(id)` powers the Run-now modal's "Will wait -" line before you
press Run. `SetMaxHeavyRuns(n)` is the knob (default 1).

### Coordinator verification
- Unit (`coordinator_test.go`, `-race`): self-coalesce (one pending, exactly two runs), path-overlap
  serialization, heavy slot (two heavy -> one at a time; light alongside), governor deferral (real
  `governor.SetStreaming`), queue cap, cancellation, interactive slot, `Status()`/version bump.
- `RunSweep`/`Preview` equal what `onSchedule` sweeps (same fixtures); `RunManual` unchanged
  (single file, no eligibility, trigger `manual-file`).
- Render: run-now modal (rules / single-file / empty / conflict) + tab status region + card state
  badge + schedule coalesced pinned Go == Zig (v1 JSON + v2 RZW1 goldens).
