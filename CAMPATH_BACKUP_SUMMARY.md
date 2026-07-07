# Camera-path backup + auto-restore

Crash-resilience for DJ sets: back up every VRChat dolly path rave-mate plays, and reload the right
one automatically after a crash/rejoin while a set is live.

## Flow

- **Backup on play** - `vrctools.Service.LoadCamPath` (the single choke point for the in-VR menu +
  desktop editor) → after `/dolly/Import`+`/dolly/Play`, copies the exact JSON into the current
  world's backup slot (`vrccampaths.Backup`), keyed by `WorldID` from the location timeline. One
  latest copy per world (overwritten) + a world sidecar; `index.json` maps `worldID → BackupEntry`.
  Gated on `AutoBackupCamPaths`.
- **Auto-restore on world join** - `Service.onLocation` → `maybeAutoRestore`: when `AutoRestoreCamPaths`
  AND a set is live AND a backup exists for the joined `WorldID`, reload it `restoreDelay` (5s, lets
  VRChat's OSC come up) after join. Re-validates world+live at fire time (world/set may have changed).
- **Live detection** - `Start` subscribes on the eventbus to `obscontrol.TopicStatus`
  (Streaming/Recording per source) + `twitch.TopicViewers` (Live), cached under a mutex; `isLive()`.

## Feedback-loop guard

Restore calls `loadCamPath(file, backup=false)` - a separate entry point that skips backup, so
restoring a backup never overwrites that backup with itself. Debounce = `onLocation`'s per-instance
dedup (fires once per join).

## Config (`VRCToolsFeature`)

`AutoBackupCamPaths` / `AutoRestoreCamPaths` (both default **on**), `CamPathBackupDir` (empty =
`<dataDir>/campath_backups`, kept outside `CamPathsDir` so backups don't pollute `Scan`). UI toggles
in the VRChat-tools settings card.

## Limitation

VRChat exposes **no OSC readback** for dolly paths. We can only back up paths rave-mate itself plays -
**not** paths the user triggers inside VRChat's own dolly UI. Those are not captured.

## Files

`internal/vrccampaths/backup.go` (+`backup_test.go`), `internal/vrctools/vrctools.go`,
`internal/config/config.go`, `internal/ui/view_settings_vrctools.go`.
