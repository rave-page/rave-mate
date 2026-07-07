# Remote control of paired instances (Automations + Library)

Quick-switch a paired rave-mate instance from the Automations + Library managers and drive it:
list/create/edit/delete/run its automations, browse + tag-edit its collection. Same method
system as the Local Studio "browser control" channel - peer transport.

## How it works

Each manager has a **"Controlling: [This computer | peer]"** switcher (shows only when a peer is
connected). Pick a peer → the manager's reads + mutations route to that peer over `remotectl`
instead of the local engine. Carried on the peerlink `ChanControl` channel, so authz rides the
existing SAS-pairing + per-frame MAC (only paired peers can drive control).

## What ships

### `internal/localmedia` - shared file-browse
`listDirectory` + `getDefaults` extracted from `internal/studio` (typed, web-byte-exact). One impl
backs the Local Studio WS server **and** peer control, so remote file-browse streams the
controlled machine's filesystem instead of popping a native dialog.

### `internal/remotectl` - peer-control RPC
Request/response over `ChanControl`, mirroring the studio wire body (`{t,id,method,params}` /
`{t,id,ok,result,error}`) + namespaces (`localMedia.*`, `automations.*`, `library.*`). `Endpoint`
(Call/OnControl/Register) + typed `Client` + server handlers over `automation.Manager` and
`*libdb.DB`. Wired in `app.go`: `peerBridge.SetControlSink(ep.OnControl)`, `SendTo` on ChanControl.
Library tracks are server-side **paged + filtered** (a page at a time under the 768 KB frame cap).

### Native UI
- `view_remote.go`: the switcher + `remoteBrowser` (streamed in-app directory picker fed by
  `localMedia.listDirectory` - never a native dialog on either box).
- Automations tab: target-aware (`dsList/dsSave/dsRunManual/…`); run-now picks the input from the
  controlled machine's streamed fs.
- Library tab: a peer target swaps in `remoteStudioView` (`view_remote_library.go`) - the SAME
  redesigned shell as local (rail/toolstrip/list+grid/inspector/status): full browse (kind/sort/
  name filter, breadcrumb, defaults chips), Collection (paged/searched + remote tag write/revert),
  remote transcode; Favorites/Playlists/History/Queue/Presets/ID Marks degrade explicitly
  (disabled control + reason). Switcher is LIVE (rebuilds on peer connect/disconnect; falls back
  to local with a toast - the old build-once switcher never appeared at launch and went stale).
- Row context menu (List+Grid, local AND remote via `mediaOps`): Rename/Duplicate/Move to…/Delete
  (confirm)/Copy path/Reveal (local-only)/Mark as ID (local-only)/Send to paired instance…
  (`ui.FileXfer`, hidden while backend nil). Remote fs ops = `localMedia.rename/move/duplicate/
  delete` (same `internal/localmedia` impl both sides).

### Web (browser control parity)
`RemoteFilePicker.tsx` - the Automations "Run / Step through" buttons now pick a file via the
in-app streamed `localMedia.listDirectory` browser instead of being dead without a Browse-tab
selection (a native `pickFile` would block the *controlled* machine). Works for local Electron +
remote rave-mate alike.

## Perf diagnosis (`ctl perf` / `ctl remote-perf [nodeID]`)
"Did the update slow us down - or is it something else on that box?" answered locally or from a
paired desk instance (remote mirrors `vrinput`/`remote-vrinput` end-to-end via `app.perf`):
- `internal/perfmon`: always-on 1 Hz collector - ~10-min ring of process cpu%/rss (procstat) +
  goroutines/heap/GC (runtime/metrics, no ReadMemStats STW); report = now / 1m-avg / 10m-max.
- `[system]` (Windows): GetSystemTimes cpu%, GlobalMemoryStatusEx, top ~8 processes by per-PID
  GetProcessTimes deltas over a ~1s on-demand pass. `[featurehost children]`: pid/ready/restarts
  + cpu/ws from the same pass.
- Probe registry: subsystems register named sections - `[vroverlay]` (100ms render tick + ~90Hz
  input-loop EWMA/max/p99/over-budget, pointer-cast cost incl. touchCast pass count, texUploads),
  `[eventbus]`, `[session.merger]`, `[logbus]` (WARN/ERROR by source, last 10 min).

## Notes
- Remote run-now uses a 5-min client timeout; if a transcode chain runs longer the run still
  completes on the peer and surfaces in its run history.
- The switcher only appears with a live connection; single-instance installs are unchanged.
- Verified via `rave-mate ctl`: both tabs 0-overflow; RPC round-trip/error/timeout/CRUD covered by
  `internal/remotectl` loopback tests; `internal/localmedia` byte-shape tests.
