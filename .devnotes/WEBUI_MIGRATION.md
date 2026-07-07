# Fyne → HTML/CSS webview migration (`internal/webui`)

Replace the Fyne UI with a **Go-driven HTML/CSS renderer** - better looks, responsiveness,
ergonomics. Same features, no regression; a pure UI swap. Coexists behind a config flag until
parity, then Fyne is retired. The Gio player (`internal/giokit`, `player_gio.go`) is untouched.

## Principles (from the design brief)

- **Go drives everything; HTML+CSS render it; JS is a dumb pipe.** Every view is rendered in Go
  (`render.go`) and the DOM is patched via the webview binding - like JS would, but from Go.
- **Not a web app.** No loopback HTTP server, no SSE, no routes, no framework. The document loads
  once via `SetHtml`; Go pushes fragment updates with `eval("__patch(id,html)")`.
- **Reuse rave.page SOURCES, not deployments.** The design system CSS + Orbitron are copied from
  `rave-page-design-system/` into `assets/ds/` (tokens + `.rp-*` kit). No prebuilt bundle, no
  dependency on the web repo, no build step.
- **Minimal JS.** One ~90-line runtime (`shell.go` `runtimeJS`): forward clicks/submits to the
  bound Go `rave()` fn + expose `__patch` and the ctl introspection helpers. Zero business logic.

## Architecture

```
config features.ui.renderer="webview"  →  app.go picks webui.New(svc) over ui.New(svc)
                                          (both satisfy the `frontend` interface; one window)
internal/webui/
  shell.go            shell iface + the single runtime JS + eval round-trip registry
  shell_cgo.go        WebView2/WebKit host (bind `rave`, Init(runtimeJS), SetHtml, Eval, watchdog)
  shell_nocgo.go      stub → Available()=false → app.go falls back to Fyne
  render.go           ALL HTML built in Go (design-system CSS inlined; tabs; Live cockpit)
  ui.go               UI type (frontend seam): lifecycle, action dispatch, live DOM push
  control.go          ctl parity over the DOM (snapshot/click/tap/type/read/set/screenshot*)
  screenshot_windows.go  PrintWindow → PNG (OS window capture for ctl screenshot[-all])
  pickers.go          studio.Picker stub (native dialogs TODO)
  assets/ds/          copied rave.page design-system CSS + Orbitron TTF
  assets/app.css      layout on top of the .rp-* kit
```

Data flows through the identical `ui.Services` struct the Fyne UI consumes (Fyne-free seam).

## Enable / test

`features.ui.renderer="webview"` in `config.json` (edit while the app is stopped - it re-saves on
quit; a stale exe strips unknown fields, so rebuild first). Needs the WebView2 runtime (Win11
default) + a cgo build. `ctl` works identically (`snapshot`/`tab`/`click`/`screenshot`).

## Parity status - 13/13 tabs at parity, live-verified ✅

All tabs are ported to **full feature/UI parity** and live-verified in the WebView2 build against
real data (paired-peer video sources, 85 recorded sets, signed-in VRChat account, running overlay
server). `ctl screenshot-all` = 12 tabs, 0 errors (the 4 `⚠OVERFLOW` flags are benign horizontal
scroll inside intended containers - track lists, `.md-list`, hint-chip rows).

**Foundation (shared, unlocks every tab):** action registry (`dispatch.go` `onExact`/`onPrefix` - tabs
self-register handlers in their own files) + live-tick registry (`live_ticks.go` `onLiveTick`) + the
primitive kit in `components.go` (`selectBox`, `slider`, `progressBar`, `statusRow`, `subTabs`,
`masterDetail`, `itemRow`, `hint` chip, `modal`+`openModal`/`closeModal`, `card`) + per-tab CSS split
(`assets/tabs/*.css`). `btn`/`selectBox`/`subTabs` emit `data-act`/`-val` via `html.EscapeString` (not
`%q`) so compound args (0x1f separators, paths) round-trip through the DOM.

**Delivered per tab (fan-out, commit 3a01173):**
- **Settings** - ~40 card bodies, ~90 `set:` keys, per-card live status dots + section nav, install/
  progress bars (ffmpeg/fpcalc/mpv/openvr/whisper), all sub-dialogs (OBS remotes, OBS-sync, presets,
  timecode sinks, worldsync PAT, unity), Twitch/GitHub/VRChat auth flows.
- **Library** - 8 sub-sections, inspector (Go-SVG waveform + Camelot key-wheel, player, tags, details),
  encode-preset builder with **electron local-studio dynamic media hints** (upscale/bitrate/codec-match
  chips), browse grid+batch+context-menu, collection import/export/filters, cloud sync.
- **Peers** - control banner + per-conn Control toggle + bridged now-playing, media-plane clock/sync/
  TC-master + per-route telemetry, webcam PTZ panel, file-xfer settings + progress bars.
- **Publish** - REC/CAP/OBS hero badges, now-playing + countdown, master/detail captures↔tracklist,
  transport (seek + per-track jump), export/delete/match-history dialogs.
- **Overlays** - per-output card bodies, per-card status dots, Spout install progress, summary strip.
- **Twitch** - OBS control bar + viewer count, moderation (delete/timeout/ban), title presets, sub
  badges + name colours, kind-specific alerts.
- **VRChat** - account seed + char counters, status/bio presets + variables + event refresh, emote
  flipbook, camera-path inline-SVG preview, photos browser (bounded data-URI thumbs).
- **Worlds** - link hint + GitHub device/PAT flow, per-list entries + friend/group-role pickers,
  per-target gist status (URL/copy/open), posters/events/now-playing cards, unity card.
- **Editor** - layer compositor on the real `internal/visualeditor` engine (shares `document.json`
  with Fyne), CSS-blend WYSIWYG preview, blend/opacity/transform inspectors, templates, undo/redo,
  export PNG.

`go build`/`go vet`/`golangci-lint` clean on no-tags, `spout`, and `spout vr`.

## Round 2 (fan-out #2) - all prior follow-ups CLOSED, live-verified ✅

- **Native pickers** - `studio.Picker` implemented on Windows (stdlib syscall only): files via
  `GetOpen/GetSaveFileNameW`, folders via `IFileOpenDialog` (FOS_PICKFOLDERS, `SHBrowseForFolderW`
  fallback). Generic browse contract `pick-dir:`/`pick-file:`/`pick-save:<container>:` + `<target>`
  re-dispatch; Browse… buttons across Settings/Library/Publish. Gotcha fixed live: Go can schedule
  the picker onto a thread another component CoInitialized as **MTA** (WASAPI) where `Show()`
  silently "cancels" - `pickers_windows.go` detects RPC_E_CHANGED_MODE, kills the poisoned thread,
  retries on a fresh STA one (manual smoke test: `go test -tags manualpick -run TestPickFolderManual`).
- **Publish trim editor** - SVG waveform w/ draggable IN/OUT lanes, zoom-at-cursor + pan
  (`data-actpos`/`data-actwheel` runtime primitives), auto-trim (tracklist bounds / silence /
  last-fader), numeric fields, transport on `PlayerProxy`, export = Fyne's `transcode.run` params
  w/ live progress; mpv popout for video. Embedded `--wid` video stays Fyne/Gio-only.
- **Library depth** - relocate writeback (BuildIndex/Relocate → `collection.fixed.nml`), full
  smart-rules editor (Fyne's fixed AND-of-fields schema + live match count - NOT a new rules
  engine, keeps rules-JSON compat), waveform pan/zoom, cosmetic fixes (name/size separator,
  pin-folder stray char).
- **VRChat Groups sub-tab** - UI over the studio-channel group-management surface
  (`groups_mgmt.go`): my-groups picker, overview + enriched roles + my-permissions, members
  (role add/remove, kick, ban - permission-gated + confirm modals), requests/invites/bans,
  announcement create, paged audit log. No current-announcement getter / post create-delete
  (backend surface lacks the client ops). Verified live against the signed-in account.
- **Typography** - Orbitron only for headings + menu chrome; body/inputs/buttons/data use a
  crisp monospace (`--font-body` override in `app.css`; same conclusion as the Fyne theme).

Remaining (small): Settings API card + Overlays path fields could adopt more Browse buttons as
they grow; embedded in-window video (`mpv --wid` into the webview HWND) if ever wanted.
Flip the `renderer` default to `webview` and retire Fyne when ready - no known parity gaps.
