# rave.page Motion

Unity editor plugin (UPM package `page.rave.mate`) installed into a VRChat / Unity
avatar project by rave-mate.

## What it does

- Reads the `.anim` AnimationClips rave-mate exports to `Assets/rave.page/Motion`
  (one per recorded VR motion take).
- **Avatar-model preview lives here**: rave-mate's own preview is an abstract 2D/3D
  skeleton because it has no avatar mesh. Unity has the real rigged avatar, so the
  faithful preview - the recorded take playing on *your* model - is rendered in this
  editor window via `UnityEditor.AnimationMode` (samples the clip onto the scene
  avatar without entering Play mode).
- "Add to avatar" builds or extends an `AnimatorController` next to the avatar with
  the chosen clips as states. For VRChat menu toggles, VRCFury is the recommended
  alternative (noted in-window).
- Avatar field auto-picks from the active scene (VRCAvatarDescriptor via reflection >
  humanoid Animator; active > inactive; EditorOnly skipped) on window open + hierarchy
  change. A manual pick is never overridden while its object is alive.
- "Export avatar as VRM" selects the avatar, then opens UniVRM's exporter: known
  window types via reflection (VRM 0.x wizards + VRM-1.0 dialog, all loaded
  assemblies), else the export menu items by path (`VRM0/…`, `VRM1/…`, historical
  `VRM/…`). Detected UniVRM assemblies/version are logged; if every route fails the
  dialog lists what was tried. Not installed → prompts to install UniVRM.

## Motion sync

Takes flow **file-based**: Export a take in rave-mate → it writes a `.anim` to
`Assets/rave.page/Motion` → this window auto-detects it (~1s poll) and lists it, no
manual Refresh needed (Refresh button still forces a rescan).

There is **no live pose stream yet**. rave-mate emits VMC over UDP `127.0.0.1:39539`
for external VTuber apps, but that carries raw device transforms (HMD/controllers/
trackers), not humanoid bones - so the plugin has no VMC/OSC receiver. A real-time
avatar link would need humanoid-bone poses on both ends (see repo notes).

## World Sync (gist feeds)

rave-mate writes `Assets/rave.page/WorldSync/sources.json` (Worlds tab → Unity
projects → Write source URLs): the published gist URLs for permission lists +
poster/events/now-playing channels. **Tools → rave.page → World Sync** lists
them, copies URLs, and wires them into scene components:
- our UdonSharp readers (`Runtime/`): `RaveMatePosterBoard`, `RaveMateEventsBoard`,
  `RaveMateNowPlayingCard` - `sourceUrl` field. Runtime scripts compile only with
  UdonSharp + Worlds SDK present (`UDONSHARP` define gate).
- VideoTXL Remote Whitelist (best-effort: type name contains `RemoteWhitelist`,
  URL-ish serialized property) - perm list `allow.txt` (newline) or `allow.json`
  (JSON mode, array path `users`).

Text content is fully gist-dynamic; **images are build-time `VRCUrl` slots**
(Udon can't construct URLs at runtime) on VRC image-allowlisted hosts.

## Control socket

`RaveMateControl` listens on `127.0.0.1:47625` (line requests, one JSON line per
reply) so rave-mate - or a test agent - can drive the editor: `PING`, `LIST-TAKES`,
`PERM-SOURCES` (returns sources.json), `SCREENSHOT <path>`, `QUIT`. All Unity API
work is marshalled to the main thread.

## Verification

The C# in this package has **not** been compiled/run inside Unity by the generator -
it needs in-Unity verification (open the project, `Tools/rave.page/Motion`).
