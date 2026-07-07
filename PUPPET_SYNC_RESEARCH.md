# Puppet dancers - multi-instance live movement sync (research)

Goal: VR events with spillover instances show "puppet dancers" - world objects (ideally the
dancer's own FBX) mirroring a live dancer who runs rave-mate. Verified 2026-07. rave-mate already
owns the capture side: `vrmotion` (OpenVR device poses, takes), VMC out (UDP 39539), OSC, VRSL
video grid (`vrslgrid`/`dmx`), Spout/OBS/RTSP outputs (`videoshare`, `rtspserve`), peerlink.

## Platform primitives (verified)

- **Udon has no sockets.** Worlds receive external data only via: string/image download
  (1 per 5 s + gist CDN ~5 min - slideshow, not motion), video players, local MIDI (`--midi`),
  and per-player state VRChat itself syncs.
- **Video → data works**: AVPro/video → shader → small RenderTexture →
  [`VRCAsyncGPUReadback`](https://creators.vrchat.com/worlds/udon/vrc-graphics/asyncgpureadback/)
  gives Udon the pixels (~1 frame late). This is how VRSL ships DMX and how the gatekept
  motion systems work (data-over-video). Known issue: readback [broken on Quest](https://feedback.vrchat.com/udon/p/vrcasyncgpureadback-doesnt-work-on-quest)
  - Quest needs the pure-shader decode path (GPU-skinned puppet, no Udon readback).
- **`VRCPlayerApi.GetBonePosition/Rotation` works for REMOTE players** (read in `PostLateUpdate`)
  ([player positions](https://creators.vrchat.com/worlds/udon/players/player-positions/)) - a world
  can retarget any in-instance player's pose onto a puppet.
- **OSC drives only the LOCAL client**: avatar parameters + [OSC Trackers](https://docs.vrchat.com/docs/osc-trackers)
  (head/hands alignment + up to 8 FBT points → full IK). VRChat then network-syncs that player's
  pose natively (~6–20 Hz IK, interpolated - "fine for most things, not dancing" per community).
- **Synced avatar parameters**: 256-bit budget/avatar, playable sync ~10 Hz (floats 8-bit);
  puppet-menu params use faster interpolated IK sync ([animator parameters](https://creators.vrchat.com/avatars/animator-parameters/)).
- **World MIDI** (`--midi`): local client only; re-broadcast needs Udon manual sync (~limited;
  rave-mate already hit the ~128 events/frame crash ceiling in the DMX→MIDI bridge). Not a motion
  transport.

## Options (multiple ways, per request)

### A. Motion-over-video (RECOMMENDED - no rave-mate user needed in any instance)
Dancer's rave-mate encodes skeleton pose (≈25 joints × pos/rot, float16 → 2 px/channel,
VRSL-style pixel grid with a sync/CRC row) into a strip of the event's video stream (or its own
VRCDN stream). Every instance's video player receives it; decode:
1. **Udon path (PC)**: shader condenses the strip → tiny RT → `VRCAsyncGPUReadback` → Udon sets
   puppet bone transforms on the included FBX. ~30–60 Hz at video fps.
2. **Shader path (Quest-safe)**: vertex-shader skinning reads the strip directly - puppet is a
   GPU-skinned mesh, zero Udon cost, works where readback is broken.
- Resolution: best of all options (per-joint, video-fps). Latency = stream latency (VRCDN ~2–10 s,
  LAN `rtspt` <1 s - acceptable for dance vibes, not for sync-critical choreo cues).
- Multi-dancer: one grid row per dancer; slot→dancer mapping published via the existing World
  Sync gist channel (`puppets.json`).
- Cost: the world must play the stream anyway at a video event - data rides along free.

### B. Repeater avatar via OSC Trackers (already ~80% in rave-mate)
A helper in the spillover instance runs rave-mate; dancer's pose streams rave-mate→rave-mate
(peerlink today = LAN; internet needs the planned relay), then out locally as OSC
`/tracking/trackers/1..8` + head/hands → the helper's avatar performs the dance; VRChat's native
IK sync shows it to everyone in that instance. World optionally retargets
`GetBonePosition(helper)` → puppet FBX so it looks like a puppet, not a possessed guest.
- Pros: no world video infra; native interpolation. Cons: needs a human+PC per instance (the
  user's stated pain), IK-sync ceiling ~6–20 Hz, 8-point IK (not per-finger), helper's avatar is
  occupied.

### C. Avatar-parameter puppetry (coarse fallback)
Dancer's (or repeater's) avatar exposes joint params inside the 256-bit synced budget (IK-synced
via puppet controls). Handful of joints max → head/arms sway, not full-body dance. Only useful as
a zero-infra fallback.

### D. String-download polling - unusable for motion (≥5 s + CDN cache). Stays what it is:
permissions/setlists/now-playing (shipped in World Sync).

## Avatar (FBX) inclusion + account linking
Dancer hands their avatar FBX to the world creator (or creator uses a generic puppet). World
ships N puppet slots (prefab per slot: FBX + decoder). Mapping slot→dancer published by rave-mate
via a World Sync `puppets.json` gist channel: `{slot, vrchatUserId?, displayName, ravePage,
gridRow}` - same publisher/refresher machinery as perms. rave-mate side: puppet manager UI links
VRChat account (already linked) + rave.page profile + calibration (bone map/scale from the
dancer's own `vrmotion` skeleton).

## Honest limits
- rave-mate captures **device poses** (HMD/controllers/trackers), not a full solved humanoid -
  full-body puppet quality needs FBT trackers on the dancer or an IK solver in rave-mate
  (vrmik exists; quality TBD). 3-point dancers ⇒ upper-body puppets.
- Latency: video route is seconds-scale; puppets dance "with the music" only if the same stream
  carries the audio (it does at video events - stream-internal sync is perfect).
- Quest: Udon decode path broken (readback bug) - shader path required.

## Build order (when scheduled)
1. `puppetgrid`: pose→pixel-grid encoder + Go decoder (round-trip tested; mirrors `vrslgrid`).
2. Wire into outputs: Spout/OBS overlay strip + `rtspserve`/VRCDN.
3. Unity plugin: puppet slot prefab - AVPro→RT→readback→bone driver (C#, in-Unity verify) +
   reference decode shader (Quest path).
4. `puppets.json` World Sync channel + puppet manager UI (slots, calibration, link).
5. Repeater mode: peer pose stream → local OSC trackers out (Option B; smallest new code).
