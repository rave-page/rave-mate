# CLAUDE.md - rave-mate

Guidance for Claude Code working in this repo. These instructions OVERRIDE default
behavior; follow them exactly.

`rave-mate` is a cross-platform native Go **Live-Suite for DJs, VJs and VR creators**:
tray-resident DJ-set capture, cross-software library sync, live streaming, OBS/Resolume
control, Twitch tooling, and VR/VRChat integration. It integrates with rave.page for the
stream bridge, notifications and publishing. Lightweight, native, minimal deps.
Self-contained Go module (builds standalone with `GOWORK=off`).

## Token-economy style (all code, comments, .md)

Minimize tokens. Drop filler ("simply", "just", "in order to", "make sure that").
1. **Code self-explains.** Comments only where intent isn't obvious - invariants,
   dense logic, security. Exported decls get one terse `// Name does X.` line (Go doc convention).
2. **Terse prose.** Techies only. Drop articles where it stays readable.
3. Same for `.md` files.

## Non-negotiable rules

- **Resource-bearing features run in a supervised subprocess - panic-guard ≠ isolation.**
  Any feature that handles **media/video/audio frames, high-rate network data, spawns
  child processes (ffmpeg), or uses fault-prone cgo (Spout / DirectShow / OpenVR / winmm)**
  MUST run as a `featurehost` child (`rave-mate feature <name>`) with a daemon-side proxy -
  NOT wired straight into `app.go` in-process. A `recover()`/panic-guard catches a Go crash;
  it does **nothing** for a memory or CPU runaway. The **ONLY** in-process carve-out is
  **DB-bound, low-throughput** work (libdb/bbolt single-writer: recorder, automation, merger).
  If it's not that, it goes in a worker. *(Cautionary tale: the medialink/mediaroute/webcam
  media plane was built in-proc; a raw 720p30 cross-PC route churned frames unbounded, ate 75%
  RAM, and killed Parsec on the host. Isolating it is tracked P0.)*
- **Every long-lived buffer/queue on a data path is bounded, with an explicit cap + drop
  policy.** No unbounded slices/channels/maps that grow with traffic. State the cap (frames
  AND bytes) and the policy (drop-oldest / newest-wins / backpressure) in a comment. A
  producer that can outrun its consumer MUST drop or block - never accumulate. The daemon
  carries a Go memory limit (`setMemoryLimitGuard`, `app.go`) + a media RSS watchdog
  (`medialink.memWatchdog`) + a concurrent-route cap - do not remove them.
- **UI is transitioning Fyne → Go-driven HTML/CSS webview (`internal/webui`).** Fyne
  (`fyne.io/fyne/v2`) is still the default renderer; the webview is opt-in via
  `features.ui.renderer="webview"` and coexists behind that flag until it reaches parity,
  then Fyne is retired. Both satisfy the `frontend` seam in `app.go`; only one is
  constructed. The webview reuses the rave.page **design-system sources** (copied CSS +
  Orbitron into `internal/webui/assets/ds`, never a prebuilt web deployment). Go renders
  every view and drives the DOM through the webview binding - there is **no web server** and
  **no JS framework**; the only JS is one small transport/introspection runtime (`shell.go`).
  ctl parity (snapshot/click/tap/type/read/set/screenshot) is preserved against the DOM.
  The Gio player (`internal/giokit`, `player_gio.go`) is untouched by this migration.
  Brand identity for the Fyne path still lives in `internal/ui/theme.go`.
- **Supply chain: 7-day soak.** Never `go get pkg@latest`. Pin exact versions ≥7
  days old. See `SUPPLY_CHAIN.md`. Prefer the **stdlib** - every new direct dep needs
  a justification row in that file.
- **API client is generated.** Run `make generate-api` before touching anything
  API-related, then use `internal/apiclient/` (oapi-codegen, filtered to the ops we
  call). Never hand-edit `apiclient.gen.go`. The spec mis-types some freeform bodies
  (`events`/`metadata` as `[]int`); use the generated `…WithBody` methods with a
  correctly-shaped JSON body via the `internal/api` adapter (see openapi2_codegen_gap).
- **No unchecked `interface{}`/`any`.** Concrete types everywhere; `any` + type
  switch only at real boundaries (wire decode, plugin edges). Mirror the web repo's
  zero-`any` rule.
- **`gofmt` + `go vet` clean.** `golangci-lint` if present. No unhandled errors -
  every `err` is checked or explicitly `_ =`-discarded with a reason.
- **Single source of brand truth.** Colors/spacing/font come from `internal/ui/theme.go`
  + design tokens - never hardcode hex or px in widgets. Mirrors web AGENTS.md §2.
- **Tray app, not a quit-on-close app.** Closing/minimizing the window hides it; the
  process keeps running in the tray. Only the tray "Quit" (or service stop) exits.
- **Always verify the UI visually.** After any change (UI or not), run
  `rave-mate ctl screenshot-all <dir>` against the running build - it sweeps every tab
  (+ scroll positions), writes PNGs + `report.txt` with ⚠OVERFLOW findings. Eyeball the
  shots; **fix obvious visual issues you find even if your change didn't cause them.**
  Deeper states (settings sections, dialogs) via `ctl click` + `ctl screenshot`.
- **Clean up scratch artefacts.** Delete one-off scripts/dumps; keep the tree clean.
- **Commit after each patch / feature / phase.** Once a logical unit passes `go build ./... && go vet ./... && go test ./...` (and golangci-lint if present), commit it (never push) - don't batch many features into one commit. Pushing and PRs happen only when the user explicitly asks.

## Commands

From `rave-mate/`:

| Task | Command |
|---|---|
| Regenerate API client (run first when API changes) | `make generate-api` |
| Regenerate Windows exe icon resource (only when `icon.png` changes) | `make generate-icon` |
| Build (current OS) | `make build` (OS-aware ldflags) |
| Build (Windows tray, no console) | `go build -tags "spout vr" -ldflags "-s -w -H windowsgui -extldflags=-static" -o dist/rave-mate.exe ./cmd/rave-mate` (build ALL feature tags so the exe ships every feature - CI does the same. `-extldflags=-static` statically links the MinGW C/C++ runtime so the exe runs on a clean PC; without it you get `libgcc_s_seh-1.dll` / `libstdc++-6.dll` missing errors. `openvr_api.dll` / `SpoutLibrary.dll` are runtime-loaded - ship them beside the exe so VR/Spout work, but their absence only disables that feature, never blocks launch.) |
| Run | `go run ./cmd/rave-mate` |
| Run as background service (headless) | `go run ./cmd/rave-mate --service` |
| Install / remove OS service | `rave-mate install` / `uninstall` / `status` (Windows install needs admin) |
| Run a worker subprocess (spawned by the daemon) | `rave-mate worker <type>` (e.g. `probe`) |
| Vet | `go vet ./...` |
| Format | `gofmt -w .` |
| Test | `go test ./...` |
| Tidy modules | `go mod tidy` |
| Supply-chain soak gate | `bash scripts/check-release-age.sh` |
| Vuln scan | `govulncheck ./...` |
| Package (Fyne, win) | `fyne package -os windows --release` |

"Tests pass" = `go build ./... && go vet ./... && go test ./...` clean.

## Architecture

Standard Go layout. `cmd/` = entrypoints, `internal/` = everything else (unimportable
outside the module).

```
cmd/rave-mate/main.go      Entrypoint: SCM detection, `worker`/`install`/`uninstall`/
                           `status` subcommands, flag parse, single-instance, app.Run
cmd/rave-mate/rsrc_windows_amd64.syso  Committed Windows icon resource (RT_ICON/RT_GROUP_ICON).
                           Auto-linked by `go build` → exe shows the brand icon in
                           taskbar/launcher/Explorer. Regen with `make generate-icon`.
internal/
  config/     Versioned typed config. Every capability is an independent Feature
              (Traktor, StreamBridge, Transcode, StudioChannel, NML, MIDI, Recorder,
              NowPlayingFile, VRChat, VR, Notifications) with its own sub-config.
              Disabled = zero footprint.
  module/     The app is the ONE daemon. Manager starts only enabled feature modules
              and supports live SetEnabled() start/stop. Traktor + Studio are modules.
  worker/     Subprocess job system. The daemon spawns `rave-mate worker <type>`
              children, newline-JSON over stdio, pools + idle-reaps + restarts on
              crash, capped per-type. Heavy/isolated work (ffmpeg) runs out-of-process.
              First worker: `probe` (ffprobe-backed). Add a type → registry in runtime.go.
  featurehost/ Resident feature subprocesses (`rave-mate feature <name>`): traktor, midi,
              icecast, stream, obs, vrchat, vr run as supervised children - crash/cgo fault
              kills only the child; the Host logs + toasts + restarts with capped backoff.
              Duplex newline-JSON stdio; daemon-side proxies (TraktorProxy etc.) keep the old
              in-proc surfaces. The vr child owns ALL OpenVR (default on vr builds; state is
              re-pushed declaratively on every respawn; `inProc` config key opts out). See
              FEATURE_ISOLATION_SUMMARY.md. DB-bound features stay in-proc, panic-guarded
              (debuglog.Go everywhere; UI uses goUI).
  sysexec/    Shared child-process plumbing: Hide, LowPriority, AssignToJob (Windows
              kill-on-close job object), KillTree. Used by worker + featurehost.
  service/    install/uninstall/status as an OS service (Windows SCM, Linux systemd
              --user, macOS launchd). Must NOT import app (cycle): the SCM run body is
              injected from main.
  app/        Lifecycle orchestrator: wires config → modules + worker supervisor → tray
              → window, graceful shutdown. Run (signals) + RunCtx (external ctx, for SCM)
  ui/         Fyne: theme.go (corporate identity), fonts (embedded Orbitron),
              tray, window, per-tab views (dashboard, traktor, logs, settings)
  logbus/     In-mem ring buffer + subscriber fan-out (mirrors web failedMediaLogger /
              sseDebugLogger). Every service logs here; the Logs tab renders it live.
  config/     Typed config + OS-correct paths (os.UserConfigDir), JSON-persisted
  apiclient/  GENERATED (oapi-codegen, make generate-api). Filtered Go client over
              net/http for the few ops we call. Never hand-edit.
  api/        Thin adapter over apiclient: redacted-logging Doer (never logs tokens),
              correctly-shaped bodies where the spec mis-types freeform fields.
  auth/       Browser-deeplink login: registers ravepage://+rave:// schemes, opens
              {website}/desktop/bridge, exchanges the grant code (POST /auth/exchange),
              token store (DPAPI-sealed at rest). Legacy dymattic:// still accepted.
  traktor/    HTTP listener :8080 - Traktor Pro 4 deck/channel/master ingest + snapshot.
              One Source feeding the merger.
  session/    DJ-data aggregation hub (see docs/DJ_SOURCES.md). Sources (sources/: traktorsrc,
              nmlsrc, midisrc, icecastsrc + planned nowplaying/qml stubs) emit normalized
              Observations; the Merger fuses per-field by priority+TTL; Sinks (sinks/:
              filesink, recorder) consume the UnifiedState. aggregator/ = the "session"
              module wiring it all + live Reconcile(). Canonical field names = Traktor's
              wire keys (zero ingest wire-break). Recorder = confirmed-play tracklist.
  icecast/    Local Icecast2 source receiver: Traktor broadcasts a live set here; streams the
              encoded body to setsDir + parses now-playing (in-band Ogg Vorbis comments +
              /admin/metadata). Feeds icecastsrc + the set→tracklist linker. See SET_CAPTURE_SUMMARY.md.
  setfp/      Per-track fingerprinting of a captured set: ffmpeg slices each track's span
              (offset = track.StartedAt − capture.StartedAt) → fpcalc → change_log (track_hash+fp).
  obs/        obs-websocket v5 client (handshake/auth, request correlator, op:5 event
              fan-out, GetRecordStatus). The featurehost "obs" child watches
              RecordStateChanged → finished recordings link to the tracklist like Icecast
              captures (kind=obs in set_recordings). See RECORDING_COCKPIT_SUMMARY.md.
  vrchat/     Client-side VRChat link (port of app/src/vrchat): stdlib http client vs
              api.vrchat.cloud (Basic-auth login + totp/otp/emailOtp 2FA + auth/twoFactorAuth
              cookie session), sealed session at rest (vrchat.bin; creds never stored),
              account state machine (Manager), + pipeline WS (wss://pipeline.vrchat.cloud).
              The pipeline runs as the featurehost "vrchat" child; opt-in uplink vaults the
              session on rave.page (/auth/vrchat/token). See VRCHAT_SUMMARY.md.
  localmedia/ Shared local-fs browse (ListDirectory + Defaults), web-byte-exact. Backs BOTH the
              studio WS server and remotectl, so remote file-browse streams the controlled box's
              fs (no native dialog). studio/dispatch.go + remotectl both call it.
  remotectl/  LAN peer-control RPC over peerlink ChanControl - a paired instance drives this one's
              automations + library + file-browse. Mirrors the studio method system
              ({t,id,method,params} + localMedia.*/automations.*/library.*). Endpoint + typed
              Client + handlers over automation.Manager / *libdb.DB. See REMOTE_CONTROL_SUMMARY.md.
  midi/       MIDI input driver. Windows = winmm.dll via stdlib syscall (NO new dep);
              !windows = stub. Consumed by session/sources/midisrc (Denon + custom CC map).
  sysactivity/ OS idle + running-process detection for schedule gates. Windows = user32
              GetLastInputInfo + kernel32 Toolhelp snapshot (stdlib syscall, no dep); !windows =
              no-op (gating fails open). Used by automation/scheduler. See SCHEDULE_CONDITIONS_SUMMARY.md.
  stream/     Live stream publisher: subscribes to the session Merger (the hub), batches
              merged updates → /streams/{id}/ingest + heartbeat (port of streamPublisher.ts).
  studio/     Local Studio control channel: loopback WS server (127.0.0.1:47615-47619)
              the web app connects to. Handshake is byte-exact with the web studio client.
              Same security: ECDH P-256 + HKDF session key, mutual
              /auth/me identity match, per-frame HMAC (jti-bound), monotonic seq, origin
              allowlist + PNA preflight. Crypto is all stdlib (crypto/ecdh, crypto/hkdf).
  sysnotify/  User-facing notifications go through the frontend.Notify seam: the Fyne renderer
              uses app.SendNotification; the webview renderer shows an in-page toast + a native OS
              notification via internal/sysnotify (tray balloon on Windows, osascript/notify-send
              on macOS/Linux).
  wirecrypto/ Shared stdlib crypto + canonical-JSON (ECDH-P256/HKDF/HMAC, byte-exact TS
              parity) used by studio AND peerlink. studio keeps unexported aliases.
  shared/     Folded-in formerly-external packages (secureseal, auth, logbus, selfupdate,
              branding) - this module is self-contained. secureseal = OS at-rest sealing
              (Windows DPAPI; no-op elsewhere), used by auth + identity.
  identity/   Stable long-term Ed25519 node identity + NodeID, persisted in the bbolt store
              (sealed via shared/secureseal where available). Authenticates the LAN peer link.
  discovery/  Pure-stdlib mDNS/DNS-SD (_ravemate._tcp) for LAN peer discovery - own minimal
              DNS wire codec; x/net/ipv4 for per-iface multicast joins + same-host loopback.
  peerlink/   Secure peer-to-peer link between two rave-mate instances (NO rave.page API).
              Ephemeral ECDH authed by Ed25519-signed transcript + 6-digit SAS pairing;
              trust-on-pair, silent reconnect, MAC'd control frames. coder/websocket on a
              LAN listener (47631-47635). Manager ties discovery + peers + identity.
  peers/      Remembered (SAS-paired) peers over the bbolt store; feeds peerlink reconnect.
tools/genapi/ Build-time only (own go.mod): fetches /openapi.json, generates apiclient.
tools/winicon/ Build-time only (own go.mod, pure stdlib): icon.png → cmd/rave-mate .syso
              (area-average resize → 7 PNG-in-ICO sizes → COFF .rsrc). No external dep.
```

`libdb` also has an append-only `change_log` (every play_count/last_played/rating/metadata
mutation + recorder play events) - the backbone for the future cross-machine library merge
(Phase 3) and rollback. See `PEER_LINK_SUMMARY.md`.

### Auth (browser-deeplink, no embedded login)

The browser holds the Zitadel session; rave-mate never sees a password. Sign in → opens
`{website}/desktop/bridge` → user authenticates there → the bridge mints a one-time grant
and redirects to `rave://auth/callback?code=…` → the OS hands that deeplink to rave-mate
(single-instance forwards it to the running app) → we POST `/auth/exchange` for tokens.
Tokens are sealed at rest via Windows DPAPI (in-memory only where no OS secret store
exists). Registering the URL scheme mutates `HKCU\Software\Classes`. `rave://` is the
product scheme; `ravepage://` is the canonical grant scheme; legacy `dymattic://` is still
accepted for older links but no longer registered on new installs.

### Provenance (upstream web client)

Several modules were ported from the (private) rave.page web client - the Go here is the
canonical source of truth; the TS originals are NOT part of this repo, so re-implement in
idiomatic Go rather than reaching for a parent tree:
- Traktor ingest → `internal/traktor`; stream publisher → `internal/stream`
- Studio control channel → `internal/studio` (the handshake is byte-exact with the web
  client - `canonicalJSON` is cross-checked in `encoding_test.go`; don't change the
  MAC/transcript inputs without re-pinning both ends)
- Web `@theme` design tokens → `internal/ui/theme.go` (palette, Orbitron)

Formerly-shared code (secureseal, auth, logbus, selfupdate, branding) lives in
`internal/shared` - this module is self-contained, with no external shared module.

### Concurrency

Services own their goroutines + are stopped via `context.Context` cancel. UI updates
from non-UI goroutines go through `fyne.Do` / the app's event channel - never touch
widgets off the main thread.

### Design tokens (corporate identity)

Dark-first. From the web `@theme`: bg `#0a0a0a`, fg `#fafafa`, brand-base `#F70864`,
brand-hot `#FF3E8A`, brand-mint `#08F79B` (success/live), brand-violet `#7C3AED`
(navigate/info), brand-amber `#FFB547` (warning), radius `8px`. Orbitron = display/brand
chrome; default sans = body; default mono = logs.

## Run-as-service

`--service` runs headless (no window/tray attempt) for OS service managers
(systemd / launchd / Windows Service). Same core services, no UI.

## Environment

- Cross-platform (win/mac/linux). A C toolchain + OpenGL are needed for desktop builds.
- API URL resolution: defaults to the rave.page development API; override with
  `RAVE_API_BASE_URL`. Production only on explicit opt-in.
- API test credentials are personal + development-only. Supply your own in a git-ignored
  local file; never commit them, never log them, never point them at production.
