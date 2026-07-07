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
