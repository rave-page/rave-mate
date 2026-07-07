# rave-mate

[![CI](https://github.com/rave-page/rave-mate/actions/workflows/ci.yml/badge.svg)](https://github.com/rave-page/rave-mate/actions/workflows/ci.yml)
[![Security](https://github.com/rave-page/rave-mate/actions/workflows/security.yml/badge.svg)](https://github.com/rave-page/rave-mate/actions/workflows/security.yml)
[![Release](https://github.com/rave-page/rave-mate/actions/workflows/release.yml/badge.svg)](https://github.com/rave-page/rave-mate/actions/workflows/release.yml)
[![License: AGPL v3](https://img.shields.io/badge/license-AGPL--3.0--or--later-blue)](LICENSE)
[![Status: alpha](https://img.shields.io/badge/status-alpha-orange)](#release-channels)

> ⚠️ **Alpha software.** rave-mate is in active alpha - **every build we publish right now is
> an alpha release** (there is no stable/production channel yet). Expect rough edges and
> breaking changes, keep backups of your media/library/config, and don't rely on it for
> unattended production. Want the bleeding edge? Grab a **nightly** build (see below).

A cross-platform native **Live-Suite for DJs, VJs and VR creators**: DJ-set capture,
cross-software library sync, live streaming, OBS/Resolume control, and VR/VRChat tooling -
one small Go binary that sits in your tray and fuses your DJ software, OBS, Twitch, VRChat and
VR rig into a single control surface (one Go binary, no Electron). It integrates with
[rave.page](https://rave.page) for stream publishing, profiles and events.

**Why it's cool**: everything is local-first and transparent (a live log of every request that
leaves your machine), features are independent toggles with zero footprint when off, crashes are
isolated into supervised subprocesses, and the whole thing installs as one static exe.

## Features (grouped)

### DJ sources → one live session
- **Traktor Pro 4** HTTP bridge (deck/channel/master, controller mapping manager)
- **Pioneer Pro DJ Link** (CDJ/XDJ LAN now-playing), **Serato**, **VirtualDJ**, **Rekordbox**
- **MIDI-in** source (Denon stock map + custom mappings, MIDI learn)
- Traktor **NML** collection/history; **Icecast set capture** (record the broadcast + in-band
  now-playing)
- All sources fuse in a priority **session hub** → one merged "what's playing" state
- **Tracklist recorder** (confirmed-play), set ↔ recording linking, per-track fingerprinting

### Streaming, recording & publishing
- Live set publisher → rave.page streams (ingest + heartbeat)
- **OBS** integration: obs-websocket control, record-status linking, stream/record cockpit
  across multiple PCs, media-source house-clock chase
- Native **audio recorder** (FLAC, OBS-synced), local **RTSP** performer chain (VRChat AVPro)

### Overlays & visuals
- Browser overlay (OBS Browser source), per-deck **PNG cards**, direct **obs-websocket
  renderer**, scrolling **waveform + EQ/FX** panel, cover-art resolver
- **Video share** (Spout), **visual editor** (layers/blend/text), media/poster editor
- **VRSL DMX video grid**, Art-Net ingest/emit, **DMX→MIDI** bridge for VRChat `--midi` worlds
- House **timecode**: LTC audio / MTC / Art-Net TimeCode

### Library
- Local DJ library (SQLite), browse/search/playlists, transcode workers, tag write-back,
  **cross-DJ-software library sync** (Traktor/Serato/VirtualDJ/Rekordbox hub-merge), play-count
  sync, append-only change log

### Multi-PC & remote
- **Peer link**: LAN discovery (mDNS) + SAS-paired encrypted connections between rave-mate
  instances; remote control (automations/library/file browse), live DJ-data bridge, media routes
- **Local Studio** WebSocket channel for the rave.page web app (ECDH + HMAC, origin-pinned)
- **App groups**: relaunch your whole DJ-rig app set after a crash

### Twitch
- Device-code sign-in (no password/secret), live chat, follow/sub/bit alerts, stream-title
  presets with variables, moderation, **speech-to-text → chat** (local Whisper)

### VRChat
- Client-side account link (2FA, session sealed at rest, never your password on our servers)
- Status/bio editor with presets + event variables, animated-**emoji sprite-sheet** generator
- Screenshot organizer (by world/event), **camera-path backup/restore**, location timeline,
  per-world overlay layouts
- **World Sync**: publish permission lists (VideoTXL-compatible), poster billboards, upcoming
  events + a live now-playing card as GitHub gists your world polls - update worlds without
  rebuilds (see `WORLD_INTEGRATIONS_RESEARCH.md`)
- **Unity plugin** (`page.rave.mate`): motion-take import + avatar preview, world-sync URL
  wiring, UdonSharp reader prefabs

### VR
- OpenVR **overlays** in-headset (chat, alerts, wrist quick actions, per-world layouts)
- VR **keybinds** (SteamVR input + MIDI dispatch), **motion capture** studio (record takes,
  export Unity `.anim`), **VMC** output for VTuber apps, VRChat camera/dolly OSC presets
- VR perf telemetry (local or from a paired headset PC)

### Platform
- Tray app or headless OS service, **signed self-updater** with release channels, crash
  guardian + GPU watchdog, subprocess feature isolation, versioned config with migrations,
  `rave-mate ctl` control socket for scripting/automation

## Honest status: tested vs untested

| Area | Status |
|---|---|
| Traktor bridge, session hub, recorder, overlays (web/PNG/OBS), library, transcode | **Battle-tested** - used in production sets by the authors |
| Twitch, OBS control, set capture, audio recorder, peer link (LAN) | **Used regularly**, stable |
| VRChat account/status/emoji/photos/cam-paths, VR overlays/keybinds | **Used in real events**; VR surface still gets frequent fixes |
| World Sync (gist perms/posters/events/now-playing) | **New** - Go side unit-tested; needs live soak |
| Unity plugin C# (motion window, world-sync window, UdonSharp readers) | **Unverified in Unity** - written to compile, not yet exercised in-editor; treat as beta |
| Serato/VirtualDJ/Rekordbox live sources | Implemented + tested against local installs; fewer field hours than Traktor |
| macOS/Linux builds | Compile + basic runs; Windows is the primary tested platform |
| Motion studio FBX avatars | Binary FBX loads + renders textured/smooth-shaded (ASCII FBX + blend shapes not supported) |

## Quick start

```
make build                        # current OS
go run ./cmd/rave-mate            # tray + window
go run ./cmd/rave-mate --service  # headless
rave-mate ctl status              # drive a running instance
```

Windows release-style build (all features, static runtime):
```
go build -tags "spout vr" -ldflags "-s -w -H windowsgui -extldflags=-static" -o dist/rave-mate.exe ./cmd/rave-mate
```
`openvr_api.dll` / `SpoutLibrary.dll` ship beside the exe (runtime-loaded; absence only disables
VR/Spout). See `docs/dev/BUILDING.md`.

## Release channels

Builds are stamped `nightly` / `alpha` / `beta` / `production` (see `docs/dev/RELEASING.md`).
**We are in alpha - only `nightly` and `alpha` builds exist right now** (no `beta`/`production`
channel yet). All of them are prereleases and **show a warning on launch**: they are development
releases - always keep backups of your media/library/config; we are not liable for damage to
files or systems (see LICENSE §15–16).

- **[Nightly](https://github.com/rave-page/rave-mate/releases/tag/nightly)** - a single rolling
  prerelease rebuilt from the tip of `development` on every push (and daily). Bleeding edge;
  most likely to break. Download the `.exe` / Linux binary from the release assets.
- **Alpha** - tagged snapshots (`vX.Y.Z-alpha.N`) on the
  [Releases](https://github.com/rave-page/rave-mate/releases) page; a little more settled than
  nightly, still alpha.

## Docs

- **Users**: [`docs/user/`](docs/user/) - one guide per feature group: what it does, how to use
  it, how it works, caveats.
- **Developers**: [`docs/dev/`](docs/dev/) - architecture, building, releasing;
  [`CONTRIBUTING.md`](CONTRIBUTING.md); `CLAUDE.md` (agent/dev workflow rules);
  [`SUPPLY_CHAIN.md`](SUPPLY_CHAIN.md) (7-day soak policy, dependency justification).

## License

**AGPL-3.0-or-later** - an OSI-approved copyleft open-source license. Free to use, modify,
redistribute, fork, and build commercial products or services on. The catch: any version you
redistribute **or run as a network service** for others must also be licensed
AGPL-3.0-or-later, with complete corresponding source made available to its users. You may not
relicense it as proprietary or ship it as a closed product. See [LICENSE](LICENSE) +
[NOTICE](NOTICE).

## Acknowledgements

rave-mate stands on open-source work. Thanks to the projects that made it possible - and to
those we learned from.

**Runtime / libraries we build on:**
- [Valve OpenVR SDK](https://github.com/ValveSoftware/openvr) (BSD-3) - the `IVROverlay` /
  `IVRInput` APIs our VR overlays and SteamVR bindings use.
- [Fyne](https://fyne.io) (BSD-3) - native cross-platform UI.
- [mpv](https://mpv.io) - the in-app video player (own GPU window, JSON-IPC driven).
- [FFmpeg](https://ffmpeg.org) - media probe / encode / decode across the media plane.
- Plus the Go modules justified in [`SUPPLY_CHAIN.md`](SUPPLY_CHAIN.md).

**VR overlay UX - studied, not copied:** the correct approaches for laser↔overlay intersection
(controller *tip*-pose ray + `ComputeOverlayIntersection`), overlay lifecycle, and grab/edit
manipulation were learned from these excellent projects. They are **GPL-3.0**; rave-mate does not
copy their source - the standard maths were reimplemented from scratch for our
AGPL-3.0-or-later tree:
- [Desktop+](https://github.com/elvissteinjr/DesktopPlus) - the reference for the tip-offset ray
  and front-facing intersection test.
- [wlx-overlay-s](https://github.com/galister/wlx-overlay-s) - the reference for ray-plane
  hit-testing and the grab-offset move/rotate/scale model.

Any project we missed but should credit: open an issue - attribution matters to us.
