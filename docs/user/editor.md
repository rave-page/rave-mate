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
   detection behave exactly like Publish.
3. **Reframe** (video sources): choose a target aspect - 9:16 vertical
   (Reel/Story/TikTok), 4:5 portrait, 1:1 square, 16:9 wide - then drag the
   bright window on the preview frame to choose the visible slice.
   **Keyframes** animate the pan: press *Use playhead frame* to grab a frame at
   the playhead, *+ Keyframe at playhead* to pin the window there, and the pan
   glides between pins in the export (chips jump to / delete each keyframe).
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
  plugin API used by Kdenlive, Shotcut, MLT and many VJ tools. Drop plugin
  `.dll` files into `<config>\vfx\frei0r\`.
- **ISF** (<https://isf.video>) - Interactive Shader Format: GLSL fragment
  shaders with a JSON header, the format used by VDMX, CoGe and Videosync.
  `.fs` files go into `<config>\vfx\isf\`. rave-mate ships a small MIT starter
  set (brightness/contrast/saturation, vignette, posterize, pixelate,
  scanlines, chroma-shift); single-pass shaders are supported, multi-pass and
  audio-reactive inputs are not (yet).

`<config>` is rave-mate's config folder (Settings shows the exact path; on
Windows it is `%AppData%\rave-mate`). The Effects section lists everything
found, *Add effect…* appends to the chain, each entry has enable/reorder/remove
buttons and 0-1 sliders/switches for its parameters. *Preview effects on frame*
renders the current preview frame through the chain - cropped exactly like the
export will be.

Effects run in a separate helper process (`rave-mate-vfx`): a crashing plugin
or shader can never take down rave-mate or a running export queue - the export
just fails with the plugin's error message.

## Caveats

- Reframe/effects need a probed video source - if the aspect select is missing,
  the file is audio-only or still probing (wait a second).
- ISF colors/positions currently apply shader defaults in the UI; float and
  bool parameters are fully editable.
- Keyframe times are source seconds - retrimming does not move keyframes.
