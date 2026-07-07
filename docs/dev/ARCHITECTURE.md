# Architecture

One Go daemon; standard layout (`cmd/` entrypoints, `internal/` everything else). The UI is
native Fyne. Features are config-gated modules with live start/stop.

## Big picture

```
DJ software ──► session sources ──► Merger (priority+TTL fusion) ──► sinks
(Traktor/CDJ/Serato/…)                    │                        (file/PNG/web overlay,
                                          │                         recorder, stream publisher)
OS / devices ──► feature modules ◄────────┘
(MIDI, OpenVR, audio, Art-Net)
        │
        ├─ featurehost: crashy/cgo features run as supervised child processes
        ├─ worker: job subprocesses (ffmpeg probe/transcode), pooled + reaped
        └─ ui: Fyne tabs bound to Services handles (all may be nil)
```

## Key packages (internal/)

- `config` - versioned typed config; every capability = independent `Feature` struct;
  `configVersion` migrations keep old files loading.
- `module` - the module manager: starts only enabled features, live `SetEnabled`.
- `featurehost` / `worker` / `sysexec` - subprocess isolation (newline-JSON stdio, supervised
  restart, kill-on-close job objects on Windows).
- `session` (+ `session/sources/*`, `session/sinks/*`, `session/aggregator`) - the DJ-data hub:
  sources emit normalized Observations; the Merger fuses per-field by priority+TTL; sinks
  consume the unified state. Canonical field names = Traktor wire keys.
- `ui` - Fyne views; `theme.go` = design tokens (single brand truth), `kit_*.go` = component
  kit, `help.go` = ? tooltips.
- `logbus` - in-memory ring + fan-out; everything logs here; Logs tab renders live.
- `api`/`apiclient` - rave.page API: generated client (never hand-edit) + redacted-logging
  adapter.
- `auth` - browser-deeplink sign-in (OS URL scheme → one-time grant → token exchange); tokens
  sealed at rest.
- `vrchat` - client-side VRChat API (login/2FA cookies sealed; friends/groups endpoints);
  pipeline WS runs as a featurehost child.
- `github` + `vrcperm` - World Sync: GitHub device-flow/PAT link + gist publisher for world
  permission lists and poster/events/now-playing channels (see WORLD_INTEGRATIONS_RESEARCH.md).
- `twitch` - device-code OAuth, Helix, EventSub WS; events fan out on `eventbus`.
- `identity` / `discovery` / `peerlink` / `peers` / `peerbridge` / `remotectl` / `medialink` -
  LAN multi-instance plane: Ed25519 identity, mDNS discovery, SAS-paired encrypted links,
  remote control, media routes.
- `studio` / `wirecrypto` - loopback WS channel for the rave.page web app (ECDH P-256 + HKDF +
  per-frame HMAC).
- `vroverlay` / `vrbind` / `vrmotion` / `vrm` / `vmc` - OpenVR overlays, SteamVR input
  bindings, motion capture/export, VRM avatars, VMC output.
- `libdb` / `library` / `libsync` / `playsync` / `musiclib` - DJ library store + cross-software
  sync with append-only change log.
- `dmx` / `vrslgrid` / `vrcmidi` / `timecode` / `artnet` - lighting + house-clock plane.
- `unityproj` - Unity project discovery + embedded `page.rave.mate` UPM plugin (motion import,
  world-sync wiring; C# needs in-Unity verification).

## Concurrency rules

Services own their goroutines, stopped via context cancel. UI updates from non-UI goroutines go
through `fyne.Do`; UI-side goroutines spawn via `goUI` (panic-guarded to logbus). No widget
touches off the main thread.

## Security model (short)

Secrets sealed at rest (`shared/secureseal`, DPAPI); never logged (redacted Doers). Loopback
control surfaces bound to 127.0.0.1; LAN peer link authenticated by Ed25519-signed ECDH + SAS.
Self-updates Ed25519-verified when provisioned. See `SECURITY.md`.
