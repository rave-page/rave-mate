# Visual Editor

Layered image composition in the **Editor → Visual** tab (Photoshop/GIMP-style: layering,
blending, text, templates). Engine is UI-independent (`internal/visualeditor`, pure stdlib +
`golang.org/x/image`, fully unit-tested); Fyne front-end in `internal/ui/ve_*.go`.

## Engine (`internal/visualeditor`)

- **Document**: `W,H` + root `Group`. Layer tree = nested `Group`s + leaves (`Image`, `Text`,
  `Solid`, linear `Gradient`). Per-layer: name, visible, locked, opacity 0–1, blend mode,
  transform (X,Y,scaleX,scaleY,rotation°), content box W,H. Versioned JSON (schema 1);
  `Unmarshal` normalizes defaults + rejects newer schemas.
- **Compositor**: renders the tree → `*image.NRGBA`. Group = composite children to a buffer,
  then blend the buffer. Bilinear affine transforms (`x/image/draw`). Per-leaf raster cache
  keyed by content signature - transform/opacity/blend/visibility edits skip re-raster.
- **Blend modes** (13, W3C separable): `normal multiply screen overlay darken lighten add
  subtract difference soft-light hard-light color-dodge color-burn`.
- **Placeholders**: `Substitute` replaces `{key}` via a `Provider`. Built-ins:
  `{track.title} {track.artist} {track.bpm} {track.key} {time} {date}` + document static
  `Vars`. Unknown keys stay literal. `ChainProvider`/`MapProvider` compose sources.
- **Templates**: `Template` = named `Group` on a W×H canvas; `Instantiate()` deep-copies with
  fresh IDs. `TemplateStore` saves/loads user templates as JSON in `<config>/visualeditor/
  templates/`; `All()` = built-ins + user. Built-in presets (double as text-layout presets):
  **lower-third, centered title, corner caption, ticker bar**.
- **Fonts**: `FontRegistry` - embedded Orbitron families + host-registered TTF/OTF
  (loaded from `<config>/fonts/`).

## UI (`internal/ui/ve_editor.go`, `ve_panels.go`, `ve_canvas.go`)

- Center: zoomable/pannable checkerboard canvas (scroll = zoom, drag = move selected layer,
  tap = hit-select). Right: layers tree (eye/lock/select/reorder/group/ungroup/delete +
  opacity slider + blend dropdown) and inspector (transform + type props, native color picker).
  Top: dense toolbar (add layers, insert template, save-as-template, undo/redo, export PNG,
  zoom, canvas size) - every tool has a hover-help `?`.
- Live provider pulls now-playing from `svc.Session.Snapshot().BuildOverlay(...)`; a 1s loop
  re-renders when values change. Working document autosaves to `<config>/visualeditor/
  document.json` and reloads on open.
- Legacy poster composer preserved under **Editor → Poster**.

## Deferred (v1 scope-out)

- **"Use as overlay" bridge**: TODO. The overlay renderers (pngsink → `<config>/overlay-png`,
  browser/obs sinks) are now-playing-driven and session-keyed; feeding a static editor
  composition needs a new overlay source/sink + config toggle. Export writes PNG to a chosen
  file for now; a user can point OBS at it manually.
- Out of scope: filters/effects, masks, brushes, bezier shapes, multi-select marquee.
