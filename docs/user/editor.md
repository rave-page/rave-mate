# Editor

The **Editor** tab has two modes, switchable at the top:

- **Image** - compose thumbnails, posters and overlay stills from layers.
- **Video** - cut a capture, reframe it to vertical/square, add effects, export
  platform-ready clips (Instagram Reels/Stories, TikTok, Shorts).

## Image mode

A layer compositor in the spirit of Photoshop/GIMP, sized for "I need a YouTube
thumbnail in two minutes":

- **Canvas presets** (canvas dialog): YouTube thumbnail 1280×720, Instagram post
  1080×1080 / portrait 1080×1350, Story/Reel 1080×1920, X header 1500×500,
  A4/A3 poster (300 dpi), stream overlay 1920×1080 - or any custom size.
- **Insert template**: ready-made presets - lower third, centered title, corner
  caption, ticker bar, plus two full-canvas starters sized like their canvas
  presets: *Thumbnail base* (1280×720) and *Story teaser* (1080×1920). Your own
  saved components appear alongside.
- **Direct manipulation**: click a layer to select it, drag to move, corner/edge
  handles to resize (Shift keeps the aspect), the mint handle above the box
  rotates (Shift snaps to 15°). While dragging, the canvas snaps to the canvas
  edges/center and to other layers' edges/centers - mint guide lines show the
  active snap.
- **Multi-select**: Shift-click (stage or layer rows) adds/removes layers; the
  newest is the primary (solid outline + handles), others show a dashed
  outline. Dragging moves the whole selection; Delete / Duplicate / arrow-nudge
  / Group apply to all of it. With 2+ selected, the align row aligns the layers
  to each other (their common bounding box); with one, to the canvas.
- **Align row**: one-click align left/center/right/top/middle/bottom against the
  canvas.
- **Keyboard**: arrows nudge 1 px (Shift = 10 px), `Del` deletes,
  `Ctrl+Z` undoes, **⧉ Duplicate** clones the selected layer.
- Layers support text, images (Browse… picks a file), fills, opacity, blend
  modes and grouping; everything autosaves.

## Video mode

Built for "wide capture in, phone-format clip out", laid out like an NLE: a
big **Preview** viewer, an **Inspector** column on the right (source, aspect,
layout, effects, export), and the trim **timeline** across the bottom - the cut
workflow is the same as the Publish tab, so nothing new to learn:

1. **Source**: *Change source…* opens a picker with your recent recordings
   (video/audio) plus Browse for any file.
2. **Cut**: trim IN/OUT on the embedded player - waveform, zoom and silence
   detection behave exactly like Publish. The player's video shows the
   **selected reframe live**: crop layout plays the chosen aspect/zoom/pan
   slice, fit layout plays the original inside the target frame.
   **Transport**: playback stops at the OUT marker (press play there to loop
   from IN), *Stop* parks the playhead at IN, and *⇤ IN* moves the playhead
   to IN without touching play/pause.
   **Tracklist navigation**: when the source is one of your set captures, the
   player carries the set's track markers - ⏮/⏭ step between tracks, the
   *Jump to track* menu seeks straight to any track, the waveform shows a tick
   per track start, and *Auto-trim → Snap IN/OUT to track boundary* lands the
   cut exactly on the track change. Hunting the best part of a set for a Reel:
   jump to the track, snap IN, snap OUT, export.
3. **Reframe** (video sources): choose a target aspect - 9:16 vertical
   (Reel/Story/TikTok), 4:5 portrait, 1:1 square, 16:9 wide - then drag the
   bright window on the preview frame to choose the visible slice.
   **Zoom** punches in tighter than the aspect window: scroll the mouse wheel
   over the preview frame or use the *Zoom* slider (1-4×) in the inspector.
   Zoomed, the window pans on BOTH axes (it also works with *keep aspect* -
   a straight punch-in without reframing).
   **Keyframes** animate the pan: press *Use playhead frame* to grab a frame at
   the playhead, *+ Keyframe at playhead* to pin the window there, and the pan
   (both axes) glides between pins in the export (chips jump to / delete each
   keyframe).
4. **Layout**: *Zoom-fill* crops to the frame; *Original inside + filled
   background* keeps the whole picture at its native aspect, centered, and
   fills the rest with a zoomed copy (the pan window picks the slice) - the
   classic Reel look. A **Background blur** slider softens the fill, and the
   effect chain styles only the background; the foreground stays clean.
5. **Effects**: an open-standard effect chain, applied between reframe and
   encode (see below).
6. **Export**: pick a target (Reel/Story/TikTok 1080×1920, Instagram 4:5,
   square, landscape 1080p, keep source size), optionally set the output file
   (auto: next to the source), press *Export video*. Progress and cancel work
   like every other export; effect chains and the filled-background layout
   route through the effects engine automatically.

## Effects (frei0r + ISF)

Effects use two open standards, so packs made for other VJ/video tools work here:

- **frei0r** (<https://frei0r.dyne.org>) - the minimalist cross-app video
  plugin API used by Kdenlive, Shotcut, MLT and many VJ tools. **The Windows
  install ships ~155 frei0r plugins out of the box** (built from the pinned
  official source, GPL-2+, license + source pointer beside the DLLs in the
  install folder). All three plugin types run in the chain: filters,
  sources (badged `generator` - they replace the frame with their own
  content, e.g. Plasma, Ising0r, test patterns) and mixers (badged `mixer`).
  Extra plugins drop into `<config>\vfx\frei0r\`.
- **ISF** (<https://isf.video>) - Interactive Shader Format: GLSL fragment
  shaders with a JSON header, the format used by VDMX, CoGe and Videosync.
  `.fs` files go into `<config>\vfx\isf\`. rave-mate ships a small MIT starter
  set (brightness/contrast/saturation, vignette, posterize, pixelate,
  scanlines, chroma-shift, tint, spotlight, bloom, trails). Single- and
  multi-pass shaders are supported - `PASSES` with named targets,
  `PERSISTENT` feedback buffers (motion trails accumulate across frames
  during export/preview playback), `WIDTH`/`HEIGHT` expressions like
  `$WIDTH/2.0`, `FLOAT` buffers, and the `PASSINDEX`/`FRAMEINDEX` uniforms.
  Audio-reactive and `IMPORTED` inputs are not supported (yet).

`<config>` is rave-mate's config folder (Settings shows the exact path; on
Windows it is `%AppData%\rave-mate`). The Effects section lists everything
found. *Add effect…* is a searchable picker: type to filter by name,
description, category, standard (`frei0r` / `ISF`), `generator` or `mixer`;
rows are grouped by standard and show each effect's categories plus a badge
(`frei0r · generator`, `frei0r · mixer`, `ISF · generator`). Generators
replace the frame with their own content. **Mixers blend the untouched
source frame (base) with the chain output so far (top)**: a generator or a
blur before `screen` / `multiply` / `overlay` composites over the original
video; a mixer first in the chain blends the frame with itself (`multiply`
darkens, `screen` lifts). Each chain entry has enable/reorder/remove
buttons and 0-1 sliders/switches for its parameters. The preview frame
re-renders **automatically** (debounced) whenever you add/remove/toggle an
effect, move a parameter, or change aspect/zoom/layout/pan - *Preview effects
on frame* still forces a render on demand. Both are cropped exactly like the
export will be.

**Getting more effects** - the buttons under the Effects section:

- *ISF library ↗* opens <https://editor.isf.video/shaders> - thousands of
  community ISF shaders; download a `.fs` and drop it in the ISF folder.
- *Get Vidvox pack (MIT)* downloads the official
  [Vidvox ISF-Files](https://github.com/Vidvox/ISF-Files) collection (200+
  MIT-licensed generators and filters, license file included) into
  `<config>\vfx\isf\vidvox\` and rescans - one click, no manual steps.
  Audio-reactive shaders from the pack are skipped (no audio input here).
- *ISF folder* / *frei0r folder* open the plugin folders in your file manager
  for drop-in installs (the standard frei0r set is already bundled with the
  Windows install; these folders are for extras).

Effects run in a separate helper process (`rave-mate-vfx`): a crashing plugin
or shader can never take down rave-mate or a running export queue - the export
just fails with the plugin's error message.

## Caveats

- Reframe/effects need a probed video source - if the aspect select is missing,
  the file is audio-only or still probing (wait a second).
- Color parameters edit as a swatch + hex field, positions as normalized X/Y
  sliders (an ISF color's alpha keeps its shader default).
- Keyframe times are source seconds - retrimming does not move keyframes.
