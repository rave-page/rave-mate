# Update-checker UX (branch update-ux)

Self-update grew a periodic checker + full state-machine UX (2026-07-11).

## What

- `internal/updater`: Manager over `shared/selfupdate` - states
  `idle → available → downloading → downloaded(verified) → staged(needs-restart)`.
  5-min poll (30 s settle after launch); consecutive failures back off doubling,
  cap interval<<4 (5 m → 80 m); poll outcomes logged through `logbus.Gate`
  (transition + 1 h refresh). Errors set `Status.Err`, never advance state.
- First-detection notification exactly once per version: tray balloon (NIF_INFO,
  click = show window) + in-app toast; persisted `config.UpdateNotifiedFor`
  (additive field, no version bump) - survives restarts.
- `selfupdate.Apply` split: `Download(ctx, rel, progress) (*Staged, error)` stages
  exe+assets as `.new` (sha-verified, exe untouched) + `Staged.Install()` (atomic
  rename swap) + `Staged.Discard()`. `Apply` = Download+Install, kept for ctl
  SELF-UPDATE / peer remote update.
- Surfaces (all render one Manager):
  - nav rail bottom `#nav-update` block: head + `{version} · {channel}` + short
    note + ONE state-dependent action (action-bound choice), progress bar while
    downloading, verbose `app-updates` tooltip (channels, Ed25519 + SHA-256 +
    Authenticode, what each step does). Nothing rendered when up to date.
  - tray menu dynamic item (`tray.Options.UpdateLabel/OnUpdate`, label re-read per
    menu open): Download update X / Downloading… N% (=show) / Install update /
    Restart to finish update.
  - Settings → System → Updates `#inst-update` region + manual check.
- Restart path unchanged (`coord.NotifyRaveApp` + `selfupdate.Relaunch`).
- i18n ×7: `nav.update.*`, `tray.update.*`, `help.app-updates.*`; reworded
  `tray.updateNotifyBody`.

## Tests

`internal/updater/updater_test.go`: happy path, notify-once across "restart"
(shared persistence store), check-error surfacing + backoff curve, download
failure never installs, poll suppression while busy/staged, Run kick/stop,
OnChange dedup. `selfupdate`: Download stages without touching exe (win-gated).

## Live-verified (isolated instance, mock feed 127.0.0.1:47901, test Ed25519 key)

- Bad `.sig` → "Check failed: manifest signature invalid (not signed by the build
  key)"; no nav block, no install offered; warn gate-logged once.
- Good sig → toast + balloon + nav block + settings region; `updateNotifiedFor`
  persisted; RESTART of the instance re-detected the release with NO re-notification.
- Download (throttled server) → progress bar 35% shot; verified → Install →
  exe swapped on disk (`.old` present) → "Restart to apply".
- 400x800 + default: 0 overflow findings on the block (long labels wrap).
- NOT clicked: "Restart to apply" (would `coord.NotifyRaveApp` the user's real
  rave-app). Tray popup not screenshot-able via ctl; its download item was
  incidentally exercised live (user clicked it; download ran with no DOM act
  logged, matching the tray path).

## Gotchas

- helpTopics map is package-init - locale switch after boot keeps old tooltip
  language (pre-existing pattern).
- `#nav` is now flex column (block pinned via margin-top:auto); mobile ≤640px
  explicitly resets `flex-direction: row`.
- Manager skips polls while downloading/downloaded/staged; next release lands on
  the poll after restart.
