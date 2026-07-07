# Player + Harmonic Keys

Interactive waveform player and harmonic-key (Camelot) features across the Studio library.

## Webui unified player/editor (2026-07 - supersedes the Fyne/webui per-surface players)

ONE component (`internal/webui/player.go` render + `player_actions.go` state/engine) drives every
playback/edit surface in the webview UI: Publish set playback, the trim editor (old modal deleted)
and the Library inspector player. Host-keyed instances ("publish"/"library"), acts `mp-<verb>:<host>`.

- **Transport**: audio → featurehost PlayerProxy; video → embedded `<video>` served by a loopback
  Range HTTP endpoint (`mediahttp.go`, token-gated, 127.0.0.1 only), element events mirrored to Go
  via `mp-vtick` acts. No mpv window. Undecodable codec → honest degrade panel + export hint.
- **Waveform** = nav surface: multi-band SVG on a shared wall-clock axis, click-seek / wheel-zoom /
  drag-pan lanes, track/fader/cue markers, enc + LUFS chips (hover/click-pin cards), momentary-LUFS
  readout. Axis extends to engine total when decodable audio is shorter (truncated files stay fully
  seekable).
- **Edit mode** (off by default; `Trim / edit` button or loose-capture edit): IN/OUT drag lanes +
  fields, Auto-trim smart-select (tracklist bounds / silence / last fader / snap), compact export
  row (preset + outpath + target), transcode via Hub/worker (`transcode.run` TrimStart/End).
- **Dual sets** (audio + video capture): both waveforms one timeline via `internal/setalign`
  (envelope NCC; envelopes from worker `probe.envelope`; wall-clock prior; result cached
  `store.KindAlign`), muted video preview slaved to the aligned audio playhead, one IN/OUT cuts
  both ("Both (aligned)" export). Manual nudge/offset field.
- Track nav folded into the transport: prev/next + current-track smart-select. Verbose help lives
  in tooltips (`wave-nav`, `trim-editor`, `embedded-video`, `dual-alignment`).

## Waveform player (track detail → PLAYER)

- `internal/worker/probe.go` `probe.peaks` - ffmpeg decodes audio to mono s16le 8 kHz,
  folds into N uint8 max-abs buckets (default 8192). Cached per path+mtime
  (`store.KindPeaks`, JSON `{d: durSec, p: peaks}`); batch "Waveforms" prerender uses it.
- `internal/ui/waveform.go` `waveformView` - canvas.Raster widget:
  - peaks mirrored around center; played part brand-pink, playhead line follows ticks
  - **cue overlays** colored by `CueKind`: hot=pink, loop=violet (region shaded by LenMs),
    load=mint, fade=amber, plain=info, grid=muted
  - **beatgrid ticks** from `GridMarker` BPM once a beat ≥ 4 px (every 4th = bar, brighter)
  - **navigation**: tap = seek · wheel = zoom at cursor · drag = pan · double-tap = fit ·
    zoom buttons; max zoom 2 s across; zoomed view follows the playhead (pinned 25 %)
- `view_studio_player.go` - pills row (key brand-colored + BPM + ★rating), cue pill list
  (kind-colored, "1 · Name · 0:32", tap = seek there), transport. Collection tracks
  contribute cues/grid/key via `sv.byPath`; loose files get a plain waveform.

## Harmonic keys (musiclib/key.go)

`ParseKey` normalizes musical ("Ebm", "F♯ minor"), Camelot ("8A"), Open Key ("8m"/"8d")
→ `Key{Num 1-12, Minor}`; NML `MUSICAL_KEY VALUE` (0-23) is the fallback when INFO KEY
is absent. `KeyRelation`: Same / Relative (ring switch) / Up / Down (±1) / None.

Colors (`ui/keys.go`): same=mint · relative=violet · energy+1=hot · energy−1=amber ·
dissonant=uncolored.

## In the UI

- **Row key pills** (Browse / Collection / History / playlist panes): "8A · Am",
  recolored live by relation to the **selected track** (`sv.keyRef`); dissonant stays
  neutral. Off-collection rows show their raw key text.
- **Circular Camelot wheel** (`ui/keywheel.go`) - a per-pixel software "fragment
  shader" on canvas.Raster (Fyne exposes no GLSL hook; same authoring model,
  f(x,y)→color): polar mapping, outer ring = B/major, inner = A/minor, 12 at top,
  AA ring edges + constant-width angular gaps, radial glow on saturated segments,
  hover lighten + live hub readout (hovered key + track count), dark hub. UI
  composited on top: 24 polar-positioned labels, center text, tap hit-test
  (polar), Hoverable. Empty keys render dark and ignore taps.
  - **Pick mode** (HARMONIC KEYS panel in track detail): harmonic segments
    saturated vs the ref; tap a key → Collection filtered to it; "Show harmonic
    tracks" selects all four compatible keys; legend underneath.
  - **Filter mode** (Key chips on Collection / Browse / open playlist): wheel
    popover; in-filter keys saturated + glow, harmonic-but-unselected tinted;
    Harmonic + Clear shortcuts; chip label tracks the count. Playlist reorder
    (↑↓✕) is disabled while its key filter is active (view indices ≠ stored order).

## ctl fix

`ctl tap` (snapshot.go) now passes widget-LOCAL `e.Position` (plus AbsolutePosition)
to the generic Tappable fallback - previously absolute canvas coords, which skewed
position-sensitive widgets (waveform seek-by-tap verified coordinate-exact after).

Verified live via ctl against the 22,960-track collection: peaks render + cache, play,
tap-seek (2:21 at 50 % of 4:41), 4× zoom w/ beatgrid bars, cue pill, harmonic wheel
colors for 2A ref, "Show harmonic tracks" → Key (4) → exact subset, playlist key
filter 30 → 6 rows all 8A, selecting an 8A track recolors them mint.

## Gio player window (DEFAULT pop-out, 2026-07)

`openPlayerWindow` (Library pop-out, modal "Pop out", recordings) opens
`internal/giokit/playerwin` by default. Same mpv `--wid` embed, but into a Gio
window's native HWND with a dense giokit transport (seek + time, ±10s, volume, trim
IN/OUT + export via the existing Fyne preset dialog / transcode pipeline) and a
collapsible waveform strip ("Wave" toggle: shared peaks source, scrub-to-seek,
PLL-smoothed playhead). Tri-state `features.player.gioWindow`: unset/true = Gio,
explicit false = legacy Fyne pop-out (Settings → Library & media); darwin or missing
mpv falls back to legacy automatically. ctl-drivable via `gio-snapshot` / `gio-tap`.
First surface of the Fyne→Gio migration - see `GIO_MIGRATION.md`.
