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

A cross-platform native **Live-Suite for DJs, VJs and VR creators** - one small Go binary
(no Electron) that sits in your tray next to your DJ software and handles everything
*around* the set. It works with **Traktor, Serato, VirtualDJ and Rekordbox** (plus Pioneer
CDJ/XDJ rigs over Pro DJ Link and generic MIDI controllers): live deck data becomes
automatic tracklists, overlays and stream titles; sets get captured and fingerprinted; your
library stays in sync across DJ programs; and OBS, Twitch, VRChat, VR headsets and DMX
lighting hang off the same session. Integrates with [rave.page](https://rave.page) for
stream publishing, profiles and events.

**Why it's cool**: everything is local-first and transparent (a live log of every request that
leaves your machine), features are independent toggles with zero footprint when off, crashes are
isolated into supervised subprocesses, and the whole thing installs as one static exe.

![The Live tab mid-set: go-live strip, record and timecode controls, merged deck cards and connection status](https://raw.githubusercontent.com/wiki/rave-page/rave-mate/img/streaming-live.png)

## What it does for you

- **Record every set, hands-free.** Live deck data from your DJ software becomes a
  confirmed-play tracklist (a track counts after ~30s as the audible deck); when your DJ
  software later writes its own history file, rave-mate auto-reconciles the tracklist
  against that ground truth. Optional audio capture time-links the recording to the
  tracklist. → [Recording & Tracklists](https://github.com/rave-page/rave-mate/wiki/Recording-and-Tracklists)
- **Show what's playing.** Browser/PNG/OBS overlays, scrolling waveform + EQ panel, and a
  now-playing text file any streaming tool can read - all fed from the merged session, so
  they work the same whichever DJ software you run. →
  [Overlays & Visuals](https://github.com/rave-page/rave-mate/wiki/Overlays-and-Visuals)
- **One library, every DJ program.** Merge collections and sync ratings, cues and playlists
  between Traktor, Rekordbox and VirtualDJ. →
  [Library](https://github.com/rave-page/rave-mate/wiki/Library)
- **Go live on rave.page**, run your **Twitch** channel, perform in **VR/VRChat**, pair
  **multiple PCs**, drive **DMX lights** - see the
  [wiki](https://github.com/rave-page/rave-mate/wiki) for a guide per feature group.

Traktor is the most field-tested integration; the others are implemented and tested but have
fewer field hours (see the status table below).
[DJ Sources](https://github.com/rave-page/rave-mate/wiki/DJ-Sources) spells out exactly what
each software can deliver and the setup trade-offs.

## Features (grouped)

### DJ sources → one live session
- **Traktor, Serato, VirtualDJ, Rekordbox**, **Pioneer Pro DJ Link** (CDJ/XDJ LAN
  now-playing), generic **MIDI-in** - each an opt-in Settings card that states its trade-off
- **Traktor live deck state comes via controller mappings** (the primary path): the built-in
  mapping manager one-click installs the shipped **RavePage Generic-MIDI map** (play, fader,
  EQ, filter, cue for decks A-D) and the stock **Denon DN-HC4500** map (A/B titles) into
  Traktor's `Settings.tsi` (auto-backup, refuses while Traktor runs); rave-mate decodes them
  over a virtual MIDI port. **MIDI learn** binds controller presses to rave-mate actions
- Advanced Traktor opt-in: the **QML feed** - patches a controller QML surface so Traktor
  POSTs full 4-deck title/artist/BPM/position to a local listener (richest feed; needs admin
  rights, re-apply after each Traktor update)
- **Collection enrichment**: album/genre/key/BPM looked up from your DJ software's collection
  files - e.g. Traktor's NML, a **snapshot Traktor writes on save/close**, not live in-app
  state (fine for tags, which rarely change mid-set)
- **Icecast set capture**: point your DJ software's broadcast (e.g. Traktor Broadcasting) at
  rave-mate's local receiver - set audio lands on disk time-linked to the tracklist, in-band
  now-playing included
- All sources fuse per-field by priority + TTL in a **session hub** → one merged
  "what's playing" state
- **Tracklist recorder** (confirmed-play, all supported software), post-set
  **history auto-reconcile**, set ↔ recording linking, per-track fingerprinting

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
  **cross-DJ-software library sync** (Traktor/Rekordbox/VirtualDJ hub-merge), play-count
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
  rebuilds (see `.devnotes/WORLD_INTEGRATIONS_RESEARCH.md`)
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
| Traktor sources (controller-mapping MIDI, QML feed, NML), session hub, recorder, overlays (web/PNG/OBS), library, transcode | **Battle-tested** - used in production sets by the authors |
| Twitch, OBS control, set capture, audio recorder, peer link (LAN) | **Used regularly**, stable |
| VRChat account/status/emoji/photos/cam-paths, VR overlays/keybinds | **Used in real events**; VR surface still gets frequent fixes |
| World Sync (gist perms/posters/events/now-playing) | **New** - Go side unit-tested; needs live soak |
| Unity plugin C# (motion window, world-sync window, UdonSharp readers) | **Unverified in Unity** - written to compile, not yet exercised in-editor; treat as beta |
| Rekordbox integration (live source, XML writeback) | **WIP / experimental** - tested against local installs; XML grid/cue writeback is fresh, back up + verify imports |
| VirtualDJ integration (live source, database writeback) | **WIP / untested** - implemented, not yet field-tested; back up your VirtualDJ database first |
| Serato integration (library import, grid writeback) | **Unfinished** - read-side works, write-side is new byte-splicing code; treat as preview and keep backups |
| ravemidi kernel driver (one-way virtual MIDI ports) | **Developer preview** - self-signed/test-signed builds only; attestation-signed release pending (see MIDI Mixer tab) |
| macOS/Linux builds | Compile + basic runs; Windows is the primary tested platform |
| Motion studio FBX avatars | Binary FBX loads + renders textured/smooth-shaded (ASCII FBX + blend shapes not supported) |

## Quick start

**Users**: grab a build from [Releases](https://github.com/rave-page/rave-mate/releases),
run it (it lives in the tray - closing the window hides it), open **Settings** and switch on
the card for your DJ software. Full walkthrough:
[Getting Started](https://github.com/rave-page/rave-mate/wiki/Getting-Started).

**From source**:
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

## Windows SmartScreen & the self-signed driver — don't be scared

Downloading a rave-mate build can trigger **Microsoft Defender SmartScreen**
("Windows protected your PC") or a browser warning. That's expected, and it is **not** a
malware verdict: SmartScreen flags executables that are new and not signed with an expensive
EV code-signing certificate — the default state of most young open-source projects. Every
release binary is built by public GitHub Actions CI from the source in this repo, and the
self-updater verifies a signed release feed. To proceed: **More info → Run anyway** (or
right-click the file → Properties → Unblock). If in doubt, build from source — that's the
point of open source.

The optional **ravemidi virtual MIDI kernel driver** (one-way ports so DJ software can't
echo its own LED feedback back into itself) is currently distributed **self-signed
(test-signed)**. Windows only loads kernel drivers signed via Microsoft attestation, so the
preview requires developer mode steps: trust our test certificate, enable test-signing boot
mode (Secure Boot off), reboot, install the INF — the **MIDI Mixer tab** in the app shows
the exact commands, and [`driver/ravemidi/README.md`](driver/ravemidi/README.md) has the
full walkthrough. A Microsoft-attestation-signed release (loads normally, Secure Boot on,
no warnings) is in the pipeline. You never need the driver for rave-mate to work — without
it, one-way ports fall back to teVirtualMIDI, or use loopMIDI two-way ports.

## Docs

- **[Wiki](https://github.com/rave-page/rave-mate/wiki)** - workflow-first guides:
  [Getting Started](https://github.com/rave-page/rave-mate/wiki/Getting-Started),
  [User Guide index](https://github.com/rave-page/rave-mate/wiki/User-Guide),
  [DJ Sources](https://github.com/rave-page/rave-mate/wiki/DJ-Sources),
  developer + reference pages.
- **Users (in-repo)**: [`docs/user/`](docs/user/) - one guide per feature group: what it does,
  how to use it, how it works, caveats.
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

**Protocols we ported:**
- [chrisle/serato-connect](https://github.com/chrisle/serato-connect) (MIT) - the Serato
  Remote real-time OSC-over-TCP protocol (`internal/seratoremote`) and the Serato History
  `adat` field ids are derived from its `docs/protocol.md` spec + implementation; the framing
  and OSC parity tests are ports of its test vectors. Full license in
  [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).

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
