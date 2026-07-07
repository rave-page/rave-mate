# UI_WORKFLOW_IA.md - whole-app information architecture (plan of record)

Proposal for regrouping rave-mate's UI **by workflow / persona**, not by feature module.
**Realized: Library + migration phases 1–3** (Live / Overlays / Publish - see "Realized"
at the end). Settings keeps *true configuration only*; control + status live on workflow pages.

Guiding rule: **least clicks together.** Everything one job needs lives on one page. You should
never hunt through Settings to *do* a thing - Settings is for *configuring* a thing once.

## Personas / jobs

1. **LIVE** (DJ/streamer, mid-set) - start/stop stream+record, see now-playing, watch OBS/Twitch/
   timecode/DMX/perf at a glance, fire scene/keybind actions. Zero-hunt, glanceable, big targets.
2. **OVERLAY SETUP** - compose + style overlays (bands/gradients/cards/EQ) end-to-end, preview live.
3. **PUBLISH** - upload/finish a captured set or recording; link tracklist; push playlists to cloud.
4. **LIBRARY** - import/convert/clean/relocate the DJ collection; manage playlists.
5. **LISTEN / WATCH** - browse + play recordings and music files.
6. **PREPARE** - build playlists, set cues, harmonic-plan, sync across DJ apps before a gig.

## Current tabs (feature-shaped)

Dashboard · Session · Recordings · Library · Editor · Automations · Peers · Twitch · VRChat ·
App Groups · Logs · Settings. Settings is a long stack of `featureCard`s that mix a feature
**toggle** (config) with **live control/status** (Traktor bridge, stream, OBS, VRChat, DMX…).

## Proposed top-level structure (workflow-shaped)

| Page | Persona | Absorbs today | Kit components |
|---|---|---|---|
| **Live** | 1 | Dashboard + Session + stream/record/OBS/Twitch/timecode/DMX **status & toggles** | `kitToolStrip` (transport: stream/record/mic), `kitStatusStrip` (OBS/Twitch/tc/DMX/perf), `kitInspector` (now-playing + deck detail), status cards |
| **Overlays** | 2 | overlay-style.json editing now scattered in `obssync`/`unity`/`vrctools`/output settings | `kitSplit` (list ↔ live preview), `kitInspector` (style sections: bands/gradient/EQ/card), `kitSegmented` (output target), `kitDensityGrid` (overlay/preset picker) |
| **Publish** | 3 | Recordings + set-capture linking + playlist cloud sync (from Library) | `kitToolStrip` (publish/link actions), `kitDensityGrid` (recordings), `kitInspector` (tracklist + publish state), `kitStatusStrip` (upload progress) |
| **Library** | 4,5,6 | today's Library (done - see below) | full kit |
| **Automations / App Groups / Peers** | infra | keep as-is (already task-shaped) | adopt `kitStatusStrip`/`kitToolStrip` opportunistically |
| **Settings** | - | **only** true config (see split) | `kitInspector`-style grouped sections |

Rationale for merges:
- **Dashboard + Session → Live**: both are "what's happening now" surfaces. One live cockpit with a
  transport strip on top, deck/now-playing in the middle, and a status strip of every live signal.
- **Overlays as its own page**: overlay composition currently requires editing `overlay-style.json`
  through 3+ settings panels. A DJ styling overlays should stay on one canvas with a live preview.
- **Publish page**: finishing/uploading a set spans Recordings + set-capture + playlist push. Group them.

## What moves OUT of Settings → control pages

Settings today = `featureCard(toggle + inline control/status)`. Split each card:

| Settings panel | Config (stays in Settings) | Control/status (moves to a page) → target |
|---|---|---|
| Traktor / DJ bridge | enable, port, source priorities | live deck/mixer status → **Live** |
| Live stream bridge | enable, target stream | start/stop + live state → **Live** |
| OBS (`obssync`) | enable, ws host/port/password | connection state, record toggle, scene control, overlay push → **Live** + **Overlays** |
| Twitch | enable, auth (device-code) | connected channel, chat/STT state, go-live → **Live** |
| Timecode (`timecode`) | enable, mode/offset | live tc read + lock status → **Live** |
| DMX (`dmx`) | enable, universe/fixtures map | live output/patch status → **Live** |
| Recorder / set-capture (`setcapture`, `audiorec`) | enable, dirs, format, thresholds | active capture + finished-set link/publish → **Publish** |
| STT (`stt`) | enable, model, target | live transcript + push-to-chat → **Live** |
| Unity / VRC tools (`unity`, `vrctools`) | enable, ports, paths | overlay style + live push → **Overlays** |
| VR (`vr`) | enable, bindings config | live binding/summon state → **Live** (VR cockpit strip) |
| Rekordbox / DJ sources (`rekordbox`, `djsources`) | paths, keys, source priority | import/convert actions → **Library** (already there) |
| QML (`qml`) | enable, path | now-playing file status → **Live** |

Stays in Settings (true config): account/sign-in, enable toggles, ports, file paths, model choices,
credentials, run-in-background, update channel, DMX patch map, source-priority weights, API base.

Settings itself should become grouped collapsible sections (a `kitInspector`-style accordion:
CONNECTIONS / CAPTURE / OVERLAYS / VR / ADVANCED) instead of one long scroll of cards.

## Library - realized in this pass

The Library tab now embodies the workflow model:
- **Left rail grouped by job**: `FILES` (Browse/Favorites = listen/watch + browse), `LIBRARY`
  (Collection/Playlists/History = prepare + manage), `JOBS` (Queue/Presets = convert).
- **Inspector surfaces per-item actions in context, never in Settings**: PLAYER (listen), PLAYLISTS
  + HARMONIC + TAGS (prepare a set), ENCODING (convert/publish-prep), ACTIONS (open/reveal). Cross-DJ
  **Sync** + playlist **cloud push/pull** live on the Collection/Playlist surfaces, not in Settings.
- **Two modes merged** into one view; treatment inferred from item type (see view_studio.go).

## Migration phases

1. **Live** page - DONE. Live tab replaces Dashboard + Session: transport `kitToolStrip`
   (stream go-live/end · native audio record · timecode START/STOP + readout), the modular
   card list (now-playing LCD, status, merged **decks** + **sources** cards from the old
   Session tab, streaming cockpit, media sync, DMX, STT - toggle/reorder via Edit dashboard),
   bottom `kitStatusStrip` (OBS/capture/rec · TC/DMX · Twitch/headroom). Settings cards kept
   config only: tc transport, media-sync clock+readout, STT transcript, manual audio record,
   DMX traffic moved to Live; Twitch title presets moved to a Twitch-tab strip.
2. **Overlays** page - DONE. All overlay output cards (style hint, browser, waveform, PNG,
   OBS-direct, video share, now-playing files) moved off Settings onto one page with a style
   `kitToolStrip` + per-output `kitStatusStrip`.
3. **Publish** page - DONE (rename + cockpit): Recordings tab → **Publish** (sets list,
   captures playback, tracklist link/match, export). Playlist cloud sync stays on the
   Library surfaces (already realized there).
4. Regroup **Settings** into a `kitInspector`-style accordion (config only) - REMAINING.
   The sectioned nav (grouped, status-dotted) already keeps config-only cards; converting
   to a single accordion is deferred.

Do not restructure other tabs outside this plan without updating this doc.
