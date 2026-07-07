# Feature isolation (featurehost)

Goal: no feature can crash the daemon. Two layers:

## 1. Panic guards everywhere (in-proc features)
Every goroutine in the daemon is wrapped (`debuglog.Go` / `Recover`) - a panic logs
stack to logbus + `rave-mate-debug.log` instead of exiting. Covers UI views (`goUI`),
studio, automation, peers, jobs, ctl socket, sinks. DB-bound features (studio,
automation, peers, recorder, libdb/store) stay in-proc: bbolt + sqlite are
single-writer; studio needs native pickers + the jobs hub.

## 2. Feature subprocesses (`internal/featurehost`)
External-input features run as supervised children - `rave-mate feature <name>`:

| Feature | Child owns | Daemon proxy |
|---|---|---|
| traktor | HTTP listener :8080 + traktorsrc | `TraktorProxy` (session.Source, Listening/SetLogging) |
| midi    | winmm driver + decoders | `MidiProxy` (session.Source; host rides the source slot) |
| icecast | TCP listener :8000 + capture files | `IcecastProxy` (Snapshot/SubscribeCapture; libdb linking stays daemon-side) |
| stream  | publisher + API calls + publish token | `StreamProxy` (Start/End/Status; lazy spawn on go-live) |
| vr      | ALL OpenVR/cgo: init, overlays, textures, pointer, IVRInput, in-VR editor, motion | `VrOverlayProxy` (implements `vroverlay.Surface` - UI/keybinds/ctl vrinput unchanged) |

Wire: duplex newline-JSON over stdio (`frame`: method=request, event=unsolicited,
else response). Child→parent events: `obs` (session.Observation), `log` (child logbus
→ daemon bus, `proc` tagged), `mon` (monitor buses), `state`, `capture`, `status`,
`midi`. Parent→child: control requests (`Call`) + fire-and-forget events (`Send`,
e.g. merged updates for stream).

Supervision (`Host`): spawn → init handshake (= ready; bind clash fails here) →
event pump. Child death → log + toast + restart with backoff 1s→2s→5s→15s→30s
(reset after 60s healthy). Stop = stop request + 3s grace + KillTree. Windows job
object (kill-on-close, `sysexec.AssignToJob`) backstops orphans. Analysis
(probe/fingerprint/transcode) was already isolated via `internal/worker`.

Config edits apply on module restart - the `Init` closure re-reads live cfg per
(re)spawn. Tests: in-mem pipe units + re-exec subprocess integration (`crash`
feature: panic/exit/hang) + traktor HTTP e2e.

### vr child (task #4)

Default on vr builds (`VROverlayFeature.SubprocessEnabled`; `inProc` config key opts
out; non-vr builds stay in-proc stub). Wire is declarative full-state, idempotent:
parent→child pushes `config` (full VROverlayFeature, ~2s change-detected), `world`,
`campaths` (list+geometry), `stats` (perf ring + NaN-safe net snapshot, 1 Hz while a
stats overlay is enabled), `bus` (twitch chat/alerts/viewers/chatters + obs status,
Origin/Local preserved). Child→parent: `state` (Available/BindingStatus mirror),
`bus` (vr.perf telemetry, obs commands → republished on the mesh), `action` (VR
slot/quick-button → daemon keybinds), `campath` (load via vrctools), `config` (in-VR
edit → persisted). Requests: inputDiag/bindingStatus/actionBinding/openBindingUI/
toggles/perfProbe/snapshot. On every (re)spawn `OnReady` re-pushes everything + a
50-deep chat/alert replay, so overlays rebuild after a crash. SteamVR exit is NOT
fatal (Manager's supervise loop waits + re-inits inside the child); a wedged cgo call
is caught by the 45s heartbeat (`Manager.SetBeat`). `vroverlay.Surface` +
`vrSurface` router keep UI/keybinds/ctl `vrinput`/`remote-vrinput` mode-agnostic.

Verified live: killing `feature traktor/midi/icecast` children → daemon unaffected,
auto-restart ≤1s, dashboard/session/logs stay honest; clean quit reaps all children.
