# Streaming + VR features - master plan

Four features: (1) Twitch integration, (2) VR overlays, (3) inter-instance bus, (4) VR
motion recording + VRChat OSC. Research-verified (Twitch Helix/EventSub, OpenVR, VRChat OSC).

## Dependency order (why this order)

Feature 3 (bus) is the spine - 1, 2, 4 all gain "works across PCs" once it exists, and it's
already ~70% built. Feature 4 reuses feature 2's OpenVR pose binding. So:

```
P0  Bus relay extension (generic event channel + capability routing)   ← foundation, mostly done
P1  Twitch (auth → helix → eventsub → chat → moderation UI)            ← self-contained, usable now
P2  VR overlays (OpenVR IVROverlay, cgo, subprocess)                   ← highest risk
P3  VR motion recording + VRChat OSC + Unity .anim export              ← reuses P2 pose binding
```

Each Pn ships in committable sub-phases.

---

## P0 - Inter-instance bus (foundation)

**Already exists:** `peerlink` (ECDH/Ed25519/SAS authed P2P over WS), `discovery` (mDNS),
`peers` (paired store), `remotectl` (typed RPC over `ChanControl`), `peerbridge` (relays
now-playing + MIDI today via `ChanSession`/`ChanMIDI`). MIDI + now-playing already cross PCs.

**To add:**
- New channel `ChanBus` in `peerlink/protocol.go` - generic pub/sub envelope
  `{topic string, payload json.RawMessage, originNodeID string, ts}`.
- `internal/eventbus/` - local pub/sub (topic→subscribers) that also fans out over `peerlink`
  to peers and re-emits inbound peer events locally. De-dup by `originNodeID`+seq; TTL.
- **Capability routing:** a node advertises which capabilities it *owns* (twitch, vr, midi,
  nowplaying) in its mDNS TXT / a `hello` bus message. A local subsystem that lacks a capability
  transparently subscribes to a peer that has it. e.g. VR-PC subscribes to Stream-PC's
  `twitch.chat` topic; OBS-PC subscribes to DJ-PC's `session`/`midi` (already works).
- Topics: `twitch.chat`, `twitch.event` (follow/sub/bit), `twitch.moderation`, `vr.overlay.cmd`,
  `session`, `midi`, `obs.mic`.
- UI: `view_peers.go` gains a per-peer capability list + "use remote X" indicators.

**Commits:** (a) ChanBus + eventbus local; (b) peer fan-out + capability advertise/route; (c) UI.

---

## P1 - Twitch

Flow verified. **Device Code Flow** (public client, no secret). **Hybrid chat:** EventSub-WS
reads chat + events; Helix POST sends. One EventSub socket. Reuses `coder/websocket` (no new dep).

**Prereq (user):** register a Twitch app at dev.twitch.tv/console/apps as a **Public client** →
Client ID. No secret needed with Device Code Flow. (We ship/accept the Client ID via config.)

**Scopes:** `channel:manage:broadcast user:write:chat user:read:chat moderator:read:followers
channel:read:subscriptions bits:read moderator:manage:banned_users moderator:manage:chat_messages`.

**Packages:**
- `internal/twitch/auth/` - Device Code Flow; token+refresh sealed via `secureseal`; auto-refresh.
- `internal/twitch/helix/` - REST client (Client-Id+Bearer, Ratelimit-* honoring): GetSelf,
  ModifyChannel (title/category), SendChatMessage, BanUser, DeleteChatMessage,
  CreateEventSubSubscription, SearchCategories.
- `internal/twitch/eventsub/` - WS lifecycle (welcome→subscribe→keepalive watchdog→reconnect→
  revocation); typed events: follow/sub/giftsub/resub/cheer/chatmsg.
- `internal/twitch/` Manager - module.Service `Enabled: cfg.Features.Twitch.Enabled`; emits events
  onto eventbus (`twitch.chat`, `twitch.event`).

**Stream-title presets w/ variables (user req):** `config.TwitchFeature.Presets []TitlePreset`
where `TitlePreset{Name, Template, Vars map[string]string}`; Template uses `{var}` placeholders
(e.g. `"{genre} set @ {club} - {event}"`). UI: pick preset, edit vars inline, Apply → ModifyChannel.

**UI:**
- `view_twitch.go` tab: live chat (scrollback + send box), event feed (follows/subs/bits),
  moderation (ban/timeout/delete from a chat line's context menu).
- `view_settings`: Twitch card (enable, Client ID, sign-in via device code, channel, title presets).
- Bus-aware: if local node has no Twitch but a peer does, the chat view subscribes to the peer's
  `twitch.chat`/`twitch.event` and sends/moderates via remotectl RPC to that peer.

**Commits:** auth → helix → eventsub → manager+eventbus → title presets → chat/event UI →
moderation → cross-PC (bus).

---

## P2 - VR overlays (OpenVR)

**Reality (verified):** OpenXR **cannot** render persistent overlays over other apps in 2026
(`XR_EXTX_overlay` unsupported by SteamVR; only Monado/Linux). Every Windows overlay app
(XSOverlay, OVR Toolkit) uses **OpenVR `IVROverlay`**. → We use OpenVR. **Requires SteamVR
running** (documented prerequisite). Despite the "OpenXR" ask, OpenVR is the only path that works.

**Native dep:** vendor `openvr_capi.h` (flat C API, BSD-3) + `openvr_api.dll`, pinned SDK tag.
No Go-module dep (only maintained binding is archived/2017). Hand-write a thin cgo wrapper.
Build-tag `vr` (like `spout`); subprocess isolation via **featurehost** (SteamVR lib can crash).

**Packages:**
- `internal/openvr/` (cgo, `//go:build vr`) - FnTable binding: overlay.go (CreateOverlay,
  SetOverlayRaw[feeds image.NRGBA.Pix directly], transforms, alpha, show), input.go (IVRInput
  action manifest → digital toggle), poses.go (GetDeviceToAbsoluteTrackingPose, device enum/class).
- `internal/featurehost/feat_vr.go` - child process hosting the binding; daemon sends overlay
  textures via `Call("setOverlay",…)`, child emits poses/input events.
- `internal/vroverlay/` - overlay manager: maps app overlay-cards (NRGBA) → overlay handles,
  dirty-flag uploads, controller-relative anchoring, hotkey toggle.

**Configurable overlays (user req):** position, size (m), opacity, display style, message count,
display duration → `config.VROverlayFeature` per-overlay specs. Overlay types: Twitch chat,
follow/sub/bit notifications. Snap-to-controller (offset + resize around snap point). Hotkey
binds via IVRInput action manifest (rebindable in SteamVR); also MIDI binds (reuse midisrc) and
"toggle OBS mic" action (reuse obs-websocket SetInputMute - source selectable).

**Bus-aware:** chat/notification content comes from eventbus topics - so VR-PC overlays show the
Stream-PC's Twitch chat with zero local Twitch setup.

**Commits:** cgo binding (overlay) → featurehost child → overlay mgr + config → chat overlay →
notification overlay → controller snap → input/hotkey/MIDI/OBS-mic binds → bus-fed content.

---

## P3 - VR motion recording + VRChat OSC

Reuses P2's `openvr/poses.go` (GetDeviceToAbsoluteTrackingPose over all devices incl. Vive/Tundra
trackers). OSC implemented in-package (OSC 1.0 is trivial; zero dep).

**Packages:**
- `internal/vrchat/osc/` - OSC 1.0 encode/decode (encoding/binary), UDP send 9000 / recv 9001.
- `internal/vrchat/trackers.go` - `/tracking/trackers/{1-8}/position|rotation` (+head); quat→ZXY
  euler degrees, Unity left-handed +Y-up meters. `params.go` - `/avatar/parameters/*`, `/input/*`.
- `internal/motion/` - record.go (sample poses at fixed rate → internal binary+JSON header),
  replay.go (frame walker w/ selectable start/end/duration → OSC tracker stream), animexport.go
  (→ Unity `.anim` YAML AnimationClip: m_PositionCurves + m_EulerCurves, m_RotationOrder 4=ZXY),
  preview.go (2D orthographic skeleton on Fyne canvas - reuses NRGBA render path, timeline slider).

**UI:** `view_vr_motion.go` - record/stop, clip list, trim (start/end), preview playback,
replay-to-VRChat, export .anim.

**Commits:** osc core → trackers/params → record → replay → preview → .anim export → UI.

---

## Supply chain

- No new Go modules for Twitch (stdlib + existing coder/websocket), bus (existing), OSC (in-pkg).
- OpenVR: vendored C header + DLL (BSD-3), build-tag `vr`, justified in SUPPLY_CHAIN.md. Not go.mod.
- All within stdlib-first + 7-day-soak rules.

## Cross-PC scenario (user's example) - how it lands

DJ-PC (MIDI+now-playing) → OBS-PC overlays already work via peerbridge; extend so OBS-PC also
toggles its OBS mic from DJ MIDI. Stream-PC (Twitch) publishes `twitch.*` to the bus → VR-PC VR
overlays render that chat with no local Twitch. VR-PC's controller/MIDI hotkeys toggle Stream-PC's
OBS mic over the bus. Every capability is consumed wherever the user is, regardless of which
instance owns it.
