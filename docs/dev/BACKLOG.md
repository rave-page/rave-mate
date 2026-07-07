# Backlog (user-requested, not yet implemented)

- **VRChat event/instance ops UI** - manage events + multi-instance state: instance list,
  player lists/counts, queue counts, moderation actions, stats/logs. Data sources (VRCX-style):
  authed VRChat API (`/users/{id}/groups`, group instances, `/instances/{worldId:instanceId}`,
  invite/moderation endpoints), the pipeline WS (`internal/vrchat` child) for live
  notifications, and the local VRChat log tail (`internal/vrclog` location timeline) for the
  in-instance player join/leave feed. Needs research pass on instance/moderation endpoint
  shapes + rate etiquette before UI.
- **Puppet dancers** (multi-instance movement sync) - researched, see
  `PUPPET_SYNC_RESEARCH.md`; build order parked there.
- **Motion studio FBX avatars** - DONE: stdlib binary-FBX loader (`internal/vrm/fbx.go`:
  geometry/skeleton/skin/humanoid + UVs/normals/diffuse materials/embedded textures) +
  textured perspective-correct smooth-shaded raster (`internal/motionrender`). Remaining
  gaps: ASCII FBX, blend shapes, alpha/transparency.
- **Peer file transfer integrated into the Library tab** + destination directory browse/manual
  path entry - touches `view_peers*` / `view_studio*` (owned by another active agent at time of
  writing; do there).
- **VR overlay fixes** - replace the rave.page toggle button with the small quick-action menu
  by default; fix UI disappearing after entering+leaving edit mode. `internal/vroverlay/`
  (owned by another active agent at time of writing; do there).
