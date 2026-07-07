# WEBCAM_SUMMARY.md - webcam/UVC source (medialink P5, first slice)

Camera on any instance → local Spout sender + PTZ/exposure control, driveable from a paired
instance. `internal/webcam`, module `webcam`, config v24 `features.webcam`. Design + decisions:
MEDIALINK_DESIGN.md §5 + §13. Off = zero footprint (no ffmpeg child, no COM).

## Pipeline

ffmpeg dshow child (`-f dshow -video_size WxH [-framerate F] -i video="<name>" -pix_fmt rgba
-f rawvideo -`, supervised, capped-backoff restart) → stride-framed RGBA frames
(`medialink.Source`, cap-1 newest-wins + drop counter) → `medialink.Pump` → Spout sink over
`videoshare.FrameSender`. Sender name: `rave-mate cam <device>` (Spout2 Capture in OBS/Resolume).
RGBA not BGRA: same ffmpeg cost, zero swizzle into the GL_RGBA spout shim (§13 P5b). Non-spout
build / missing DLL → clear reason string, no capture. Network route = P4 (capture already speaks
`medialink.Source`).

## Enumeration

`ffmpeg -list_devices` + per-device `-list_options` stderr parse (tagged + legacy sectioned
formats; modes deduped by size, max fps wins). Windows-only.

## UVC control

Stateless DirectShow COM shim, stdlib syscall (no cgo/deps): FriendlyName match →
`IAMCameraControl` (pan/tilt/zoom/focus/exposure) + `IAMVideoProcAmp` (brightness/contrast/
saturation/whiteBalance/gain). Range/step/default/auto-cap read; sets clamped + step-snapped;
auto toggle; unsupported props omitted. Independent of the capture stream.

## Bus surface (`media.cam.*`)

- `media.cam.status` broadcast (~2 s + on change): enabled/running/device+modes/sender/props/err.
- `media.cam.cmd` directed (Target = node id): `cam.start|stop|set|refresh`.
- Capability `media.cam` while enabled. Camera executes on its owning instance; enable the
  feature on both ends for remote view/control.

## UI

Peers tab ▸ "Webcam": per-instance panel (this instance + each paired instance) - device/mode
pick, Start/Stop, copyable Spout sender name, prop sliders + auto checks, ?-help. Panels persist
across the tab's 2 s rebuilds. Settings ▸ Streaming & remote ▸ Webcam: toggle + auto-start.

## Files

`internal/webcam/{ctl,manager,devices,capture,framepipe,spoutsink,uvc_props,uvc_windows,
uvc_other}.go` + tests; UI `internal/ui/view_peers_webcam.go`, `view_settings_webcam.go`;
wiring `internal/app/app.go`; config `internal/config/config.go` (v24).

## Tests

Device/option parser fixtures, frame-pipe framing (half-reads, torn tail, fresh buffers),
clamp/step-snap, bus ctl round-trip across two linked eventbuses, config gating, capability
advertise/retract, UI formatters.

## Remaining (needs hardware / P4)

Live verify: real webcam capture → OBS Spout pickup, PTZ from a paired instance, unplug/replug
recovery. Network route (webcam on PC1 → PC2's OBS ≤ 100 ms) lands with P4 encode path.
