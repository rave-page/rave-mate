# GPU driver-reset (TDR) resilience - plan

## Implemented
- **GPU-fault watchdog + auto self-restart** (`internal/gpuwatch`, `internal/app/gpurecover.go`).
  OS-level detection (not an in-app heartbeat - the failure mode is a live-but-frozen render
  thread whose event queue still drains): (1) `IsHungAppWindow` on our own top-level window →
  `FaultHungWindow`; (2) Windows System event-log watcher (wevtapi, provider Display/nvlddmkm/
  amdkmdag EventID 4101/4098/4099/13/14) → `FaultTDR`. A confirmed UI hang → clean relaunch
  (`selfupdate.Relaunch` + graceful shutdown, hard-exit backstop if the wedged thread stalls it),
  guarded by a persisted budget (≤3 restarts / 5 min, else pause + toast "fix the driver"). A
  logged TDR alone does NOT restart (many are transparently recovered) - it's toasted + fanned
  out to in-daemon GPU consumers (VR) to reinit in place. Prove it on demand: `ctl gpu-selftest`.

Goal: a Windows TDR / GPU driver crash must NOT kill the whole rave-mate app. GPU-dependent parts
restart fast or fail recoverably. Today the daemon is ONE GPU fault domain: Fyne GL window + Spout
cgo + OpenVR cgo all in-process → any device-lost can fault the tray app + capture + stream + session.

Target: daemon = GPU-free supervisor; every GPU consumer runs in a supervised child (featurehost/
worker) that restarts with backoff. Infra to do this already exists (featurehost + worker + sysexec).

## GPU touchpoints (current)

| Path | Process today | Recovery today |
|---|---|---|
| Fyne main GL window (`ui/ui.go`, `app.go` UI launch) | in daemon (only GL ctx we own) | none; TDR → access-violation in SwapBuffers (fyne #5206) → whole-app crash. **Not recoverable in-proc (C fault, no recover()).** |
| Spout videoshare sender (`videoshare.go`, `sender_spout.go`, tag spout) | in daemon, per-deck LockOSThread + own GL ctx→D3D shared tex | ~none; create-fail logs+idles, send-fail drops frame; no recreate |
| OpenVR overlays (`vroverlay/openvr.go`, tag vr) | in daemon goroutine | supervise handles SteamVR up/down only; NO device-lost/compositor-reset; `SetOverlayRaw` errors swallowed everywhere. **No owned GPU tex (CPU RGBA upload) → recovery cheap once detected** |
| mpv player (`mpvplayer.go`, `mpvipc`) | external proc, own GPU window | isolated; death detected but no restart |
| ffmpeg HW encode (`worker/transcode.go`, `worker/encoders.go`) | worker child | selection-time SW fallback only; no runtime HW→SW retry |
| ffmpeg decode / mediaplayer fallback | child procs from main; frames → Fyne canvas (GL ctx) | ctx-cancel only |
| winshot VR-View capture | in daemon, GDI PrintWindow | returns error, not crash - **no action** |
| raster3d / poster / medialink | CPU/network | none needed |

## Plan (cheap-high-value first)

- **P0 OpenVR in-place device-lost recovery** - extend event pump to treat `VREvent_DriverRequestedQuit`/`ProcessQuit`/compositor display-disconnect as a reconnect trigger; STOP swallowing `SetOverlayRaw` errors - count consecutive fails in `manager.go tick()` + the `_ =` sites (editor/pointer/worldpath), threshold → `Shutdown()`+`resetSession()`+re-`Init` (supervise loop re-enters cleanly). LOW/LOW. Doesn't cover a hard cgo DLL fault (→ P2-vr).
- **P0 ffmpeg HW→SW runtime fallback** - `worker/transcode.go tcRun`: HW encoder that fails after passing probe → retry once SW (reuse `ResolveEncoder` sw map), invalidate cached `working` HW result. LOW/LOW.
- **P1 Move Spout videoshare → featurehost child** (`feat_videoshare.go` + `VideoShareProxy`, tag spout already shipped). RENDER in child (frames too big for stdio) - child subscribes to merged `update` events like `StreamProxy`, runs deckcard/waveform/overlaystyle render + Spout-send; detect SendImage-false/DXGI device-removed → recreate sender; unrecoverable → child exits → Host restarts. MED/MED. Removes 1 of 2 cgo fault vectors.
- **P2 Isolate the Fyne GL window into its own supervised child** - daemon runs headless-resident (already supported: `--service` mode is GL-free); UI child hosts `ui.New`+`u.Run`, talks to daemon over the ctl socket/proxies; UI-child GL crash → Host respawn w/ backoff. HIGH/HIGH but the single highest blast-radius reduction (UI GL is on every run + currently 100% fatal). Interim: externally-supervised process for auto-restart before the full split.
- **P2 Move OpenVR overlays → featurehost `vr` child** (`feat_vr.go`+`VrOverlayProxy`) - closes remaining cgo fault vector; must proxy in-VR editor/IVRInput/motion-OSC/campath providers/keybind dispatch (mostly event-shaped via the bus). HIGH/MED-HIGH. Do P0 first.
- **P3** mpv auto-restart+seek-resume; mediaplayer decode-death robustness (covered by P2-UI); winshot none.

## Reuse
featurehost (resident child, newline-JSON, crash→restart backoff {1,2,5,15,30}s, `AssignToJob` kill-on-close). worker (pooled stateless jobs). sysexec (Hide/AssignToJob/KillTree). Add child = `feat_<name>.go` init/Register + daemon proxy + module.Service. `StreamProxy` = the frame-forwarding precedent.

## Unknowns (need real VR-PC testing)
1. Does a real TDR crash Fyne in SwapBuffers (→ P2 mandatory) or just render black?
2. On compositor reset does `SetOverlayRaw` return error (P0 enough) or hang/fault (→ P2-vr)?
3. Does Spout SendImage return false on DXGI device-removed (in-child recreate) or fault (child-restart only)?
4. Does mpv self-recover a TDR (own device)?

If only one big item funded: **P2 UI-window isolation** (only GPU surface on every run whose failure is currently 100% fatal + unrecoverable in-proc).
