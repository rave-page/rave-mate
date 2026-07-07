# VR

Needs SteamVR (OpenVR) and a build with the `vr` feature (`openvr_api.dll` beside the exe -
release builds ship it).

## Headset overlays

In-headset panels rendered by rave-mate: Twitch chat, alerts, now-playing, wrist quick-action
buttons, OBS status. Content can come from THIS instance or a paired one (DJ PC data shown in
the VR PC's headset). Layouts are editable in VR (summon the editor by holding A/X - default
binding) and can bind per VRChat world (auto-apply on world join). Overlays can optionally run
in an isolated subprocess so a GPU/driver fault never kills the app.

## Keybinds

SteamVR input bindings (proper IVRInput actions - rebindable in the SteamVR binding UI) + MIDI
keybinds dispatch app actions: STT push-to-talk, OBS mic toggle, overlay summon, app-group
launch, etc. "Learn MIDI" captures your controller's next press.

## Motion studio

Record VR motion takes (HMD/controllers/trackers), preview on a skeleton, export as Unity
`.anim` clips straight into your avatar project (the Unity plugin lists takes and previews them
on your real model). Avatar preview + video render support **VRM and binary FBX** (textured,
smooth-shaded; embedded or sibling-file textures). ASCII FBX: re-export as binary.

## VTuber / VMC

Live VMC output (UDP 39539) carries device transforms to VTuber apps. VRM avatars can be synced
between paired instances.

## VRChat camera OSC

Dolly/camera path presets + control via VRChat's OSC interface, camera-path files managed by
VRC tools (backup/restore per world).

## Perf telemetry

VR frame-timing telemetry is collected locally and shared over the peer link - watch the
headset PC's reprojection from the stream PC (`ctl vrperf` too).

## Caveats

- OpenVR only (not OpenXR-native); SteamVR must be running.
- The VR surface moves fast - expect alpha-channel rough edges; report issues with `ctl logs`.
