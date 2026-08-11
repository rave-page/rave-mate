# Editor two-mode redesign (2026-08-11)

User ask: split Editor tab into **Image** (thumbnails/posters; PS/GIMP-style layer editing,
presets, visual move/scale/align) and **Video** (cut + export vertical "Hochformat" clips from
wide captures, linked to Publish features; open-standard effects: ISF/GLSL + frei0r).

**Zig-first (user directive):** new features implemented in Zig wherever the architecture has
a Zig seam. UI renders in `native/zigui` (Go renderer = mandated golden reference + untagged
fallback, per the parity system); geometry kernels (hit-test/handles/snap) in `native/zigcore`
via the zigdsp seam (Go stub untagged, parity-tested); the effects engine is a no-cgo Zig
child exe `native/zigvfx` (zigenc pattern). Go remains only in the daemon spine (act registry,
config, worker supervision) that features plug into.

## Mode axis

`edState.mode` ∈ `image` (default) | `video`. Switcher = subTabs under the tab panel.
`renderEditor()` branches: image → `editorHTML` (zig-ported, goldens updated), video →
`edvHTML` (Go renderer only for now; zigui falls back — new surface, allowed).

## Image mode (P1)

Engine already has layers/transform/blend/undo (`internal/visualeditor`). Added:

- **Direct manipulation** on `.ed-stage` via `data-actpos=ed-stage` (fractional down/move/up,
  mods on down). Go owns everything: `visualeditor.HitTest` (inverse-transform point, topmost
  leaf), handle detection (8 resize + 1 rotate above top-center, tolerance in stage-frac),
  drag state machine on `edState` (`snapshot(true)` at down, mutate per move, autosave at up).
  Move patches `#ed-preview` only; full `patchMain` at up.
  - pointerdown preventDefault suppresses click ⇒ layer `data-act=ed-select` stays for
    ctl `__click`/a11y only; real selection happens in the down hit-test.
- **Snapping**: canvas edges/center + other leaves' edges/centers, threshold ~0.7% doc-W.
  Active snap renders guide lines (`edPreviewState.Guides []edGuide`).
- **Resize** mutates W/H (leaf box, scale untouched); shift = uniform aspect. **Rotate**
  handle; shift snaps 15°.
- **Align bar** (Row3 toolbar): L/C/R/T/M/B against canvas, uses layer's effective box.
- **Duplicate** (`ed-dup`), arrow-key nudge 1px/10px (keyscope `ed`), Del delete, ctrl+z undo.
- **Canvas presets** in the canvas modal (`ed-canvas-preset:WxH`): YT thumb 1280×720, IG
  1080×1080 / 4:5 1080×1350 / 9:16 1080×1920, X header 1500×500, A4/A3 poster 300dpi,
  overlay 1920×1080. Modal = not golden-gated.
- **Image picker**: browse button (`pick-file:ed-img:path`) beside the path field.
- New builtin templates sized for thumbnail + story.

Golden cost: `editorHTML` bytes change (stage attr, handles, guides, Row3, browse btn) →
port to `native/zigui/src/editor.zig`, extend fixtures.

## Video mode (P2)

New pkg `internal/videoedit`: `Project{Source, InSec/OutSec, Aspect(orig|9:16|4:5|1:1|16:9),
PanKF []PanKey{T,X}, Effects []EffectInst, OutPath, PresetID}` + persistence
(`visualeditor/videoproject.json`) + pure filter builders (`CropExpr` lerps pan keyframes →
ffmpeg `crop=w:h:x:y` expr; unit-tested).

UI reuses the **mp component** with host `"editor"` (same trim/waveform/silence/export spine
as Publish — same-renderer reuse ⇒ no new zig surface). The surrounding video-mode view gets
its own wire message + `native/zigui/src/editor_video.zig` renderer + goldens from day one.
Around the mp component:
- Source row: Browse + recent-captures select (same libdb query as Publish).
- Reframe panel: aspect select; crop window overlay on the video (`data-actpos=edv-pan`,
  4 shade divs + border rect, drag pans the free axis); keyframe add-at-playhead + list.
- Export: inline `transcode.Preset` per platform preset (Reel/Story 1080×1920 …) +
  `transcode.Job.VF` (new: raw vf prefix) carrying the crop expr. No effects → existing
  hub/worker path untouched, progress/cancel free.

## Effects (P3 frei0r, P4 ISF) — Zig child exe

`native/zigvfx` → `rave-mate-vfx.exe` (no cgo, zigenc pattern). Modes:
- `--list <dirs>`: discover plugins → JSON on stdout.
- `--frame <chain.json> <in.raw> <out.raw>`: one frame through the chain (preview).
- `--pipe <chain.json> WxH`: rawvideo RGBA stdin → chain → stdout (export; single frame in
  flight, bounded).
Inside:
- **frei0r** host: `std.DynLib` + f0r_init/get_plugin_info/get_param_info/construct/update.
  RGBA8 frame-in/out, params float/bool/color/position. Discovery: `<configDir>/vfx/frei0r/`.
- **ISF** host: parse `/*{json}*/` header (INPUTS float/bool/color/point2D + inputImage,
  single-pass subset), GLSL prelude (IMG_NORM_PIXEL etc.), offscreen WGL context + FBO,
  readback. `<configDir>/vfx/isf/` + bundled self-written starter set (MIT). OpenFX non-goal.
Go side: worker type `vfx` (own pool — GL/plugin crash can't hit transcode) is a thin
orchestrator: `vfx.list`, `vfx.preview` (frame at t → chain → PNG), `vfx.run` (spawn ffmpeg
decode → zigvfx --pipe → ffmpeg encode, `-ss` audio from source, progress by frame count).
Export routing: chain empty → plain transcode; else vfx.run.

## Shipping

Per-phase commits; i18n 7 locales per phase; docs + wiki at end; ctl screenshot sweep;
push development, watch CI, deploy local install.
