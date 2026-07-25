# Zig VR-overlay raster port (`native/zigvr` + `internal/zigvr`, tag `zigvr`)

Port of the vroverlay RENDERING hot path to Zig per the strangler pattern
(ZIG_MIGRATION.md). Go stays the golden reference + untagged fallback.

## What vroverlay renders (survey)

All CPU raster into `*image.NRGBA` (RGBA8 non-premult), uploaded to OpenVR via
`SetOverlayRaw(…, depth 4)` (openvr.go, `vr` featurehost child owns the cgo).
Drawing stack: stdlib `image/draw` + `x/image/font/opentype` (embedded
Orbitron-Bold/Medium) — no gg/Fyne canvas.

Surfaces (render.go / stats.go / worldpath.go / manager.go) — ALL dispatched except
where noted:
- **Content panels** 640×480 (`Renderer.Panel`): Twitch chat, alerts, viewer
  count, chatter list, OBS cockpit — wrapped/balanced text rows, bottom-aligned.
  Rendered on the 100ms tick, gated by a content signature (`linesSig`).
- **Stats panels** 640×480 (`RenderStats`): perf/network/timing — rows + autoscaled
  multi-series graph + footer. Throttled ~2 Hz + signature.
- **Menu** 420×(rows+1)·56 (`RenderMenu`): in-VR editor menu; re-rendered on every
  nav/interaction (editor consumeDirty → ~11ms reconcile).
- **Path orbit** 540×540 (`RenderPathOrbit`, worldpath.go): camera-path preview —
  bg + border + Bresenham floor grid + speed-coloured polyline + discs + HUD text.
  Re-renders on every orbit/zoom/playhead change while the preview is open (~10 Hz),
  the hottest of the small surfaces.
- **Editor textures**: hover row, ghost, tooltip (+ toast), strip, strip hover,
  outline, wrist badge (all-but-the-logo, see Skips), and the edit-mode `Border`
  stamp on finished panels (`Manager.editBorder` → `Renderer.borderInto`).
- **NOT dispatched** (stay pure Go, reasons in Skips): `RenderDot`, the wrist
  badge's CatmullRom logo blit.

## Design: display list, glyphs stay Go-side

Byte-replicating the opentype curve rasterizer (float32 vector rasterizer) in Zig
is not feasible, so per the "pixel-identical" requirement the split is:

- **Go (layout + glyphs):** wrap/truncate/measure (stateful `font.Face`), the
  `font.Drawer` kern/advance walk, and glyph AA-mask rasterization (same faces →
  identical masks). Recorder: `internal/vroverlay/paint.go`.
- **Zig (composition):** executes a bounded display list into the canvas with
  integer math that exactly replicates Go `image/draw`'s RGBA64Image fallback for
  NRGBA dst (the only paths the renderer hits — verified against go1.26 src):
  - `KStore` — exact byte fill (draw.Src round trip is precomputed Go-side;
    also `img.Set` separators).
  - `KOver` — uniform source-over: `out = u16(d·(m−sa)/m) + s`, premult read
    (`NRGBA.RGBA64At`) + unpremult store (`NRGBA.SetRGBA64`), m = 0xffff.
  - `KGlyph` — alpha-mask source-over: `a = m − sa·ma/m`,
    `out = u16((d·a + s·ma)/m)`, `ma = mask8·0x101`; mask rows in an arena.

`paintInto` dispatches: record ops → `rz_vr_render`; ANY failure (caps, foreign
mask type, exec error) falls back to the direct Go draw of the same closure.

Derived primitives (paint.go) — no new op kinds were needed for the wave-2 renders:
- `border(w, col)` = `Border`'s row/col loop as 4 clipped `KStore` bands. The Go loop
  overlaps rows and cols in the corners; every write stores the same bytes, so the
  decomposition (and op order) is pixel-identical.
- `setN` = an `img.SetNRGBA` run (raw store, no colour-model conversion) — the
  worldpath Bresenham/disc rasters. Lines emit 1 op per step (1-2 px, same in-bounds
  guard as the per-pixel version); discs emit one run per row (`dx² ≤ r²−dy²` is
  contiguous).
- **Staged dispatch**: a render may call `paintInto` more than once over one canvas
  (each stage flushes before the next → same draw order), which keeps a
  non-recordable step in Go without losing the rest. `RenderWrist` uses it:
  ops(bg) → Go `drawScaled(logo)` → ops(rings).

`rz_vr_render` is **atomic**: all ops are validated before any pixel is written, so a
rejected list leaves the canvas untouched. Required now that ops-only renders (border
stamps, hover tints) composite onto existing pixels instead of overwriting the canvas —
a half-executed list plus the Go fallback redraw would double-blend.

Dispatch tally: `Renderer.zigOK/zigFB` (atomic) surface in `Manager.PerfProbe` as
`zig raster: ok=N fallback=N`. `ok=0` on a zigvr build = the Zig path never runs;
climbing `fallback` = lists being rejected (op cap / foreign glyph mask). The parity
gate asserts the Zig run dispatched, so "parity" can't silently be Go-vs-Go (the zigui
lesson: a whole tab rendered from Go for weeks).

## ABI (v1, include/ravevr.h)

```c
uint32_t rz_vr_abi_version(void);
typedef struct { int32_t x,y,w,h; uint32_t kind;
                 uint16_t sr,sg,sb,sa; uint32_t mask_off; } RzVrOp; // 32 B
int32_t rz_vr_render(uint8_t *canvas, int32_t w, int32_t h,
                     const RzVrOp *ops, size_t n_ops,
                     const uint8_t *mask, size_t mask_len);
```

Rects are pre-clipped Go-side (image.Rect canon + bounds ∩, DrawMask-equivalent
mask clip); Zig re-validates every rect/kind/mask offset and errors instead of
writing OOB. Go owns all buffers (no native allocation).

## Bounds (per CLAUDE.md)

`internal/zigvr/list.go`: ops cap 65536 (~2 MiB), glyph-mask arena cap 4 MiB —
a panel/menu render is ≤ ~3k ops / ~1 MiB; the path orbit is the op-heaviest
(~1 op per Bresenham step → ~8-15k ops for a 14-keyframe path). Drop policy:
exceeding a cap fails the record and that frame renders via the Go path — so a
pathological path (hundreds of keyframes) degrades to Go instead of growing.
The `List` is recycled per frame (VR goroutine only), so steady-state allocs
don't grow.

## Go-runtime workarounds (ZIG_MIGRATION.md "Why Zig")

- `Renderer.canvas` (canvasFor) is a GC-dodging buffer recycler. Kept as-is for
  Panel/Menu — it's Go-side and load-bearing for the current path. NOT extended to
  the newly-dispatched renders: `RenderPathOrbit` still allocates a fresh 1.1 MiB
  canvas per frame (~11 MiB/s of garbage at 10 Hz). Flagged, not "fixed" — widening
  a GC workaround is the wrong direction; the canvas should follow the raster into
  Zig-owned memory in a later pass.
- Nothing else here is runtime-shaped: the throttles/signature gates
  (`statsThrottle`, `linesSig`, hover-as-its-own-overlay) exist to avoid GPU texture
  UPLOADS, not GC/scheduler pressure — they stay.

## Skips (per-pixel work deliberately left in Go)

- **`RenderDot`** (64×64 ray cursor): every pixel carries its own alpha (soft edge) —
  no runs to record, so a display list would be 1 op/px with the float math still
  Go-side (zero win). Uploaded ONCE per session. A dedicated circle op kind earns
  nothing at this size/frequency.
- **Wrist logo blit** (`drawScaled` = `xdraw.CatmullRom.Scale`, Over): x/image's
  two-pass float64 kernel resampler FUSED with the blend. Splitting it (scale into a
  temp, then composite) double-rounds ⇒ not pixel-identical, and replicating the
  resampler is a large float port for one 128×128 blit. Staged dispatch ports
  everything around it. NOTE: this blit is 5.5 ms — ~94% of a wrist repaint. It's a
  CONSTANT per (on-state) and belongs in a Go-side cache; that's an independent
  optimization, not a Zig port. Follow-up, not done here.
- `editor.go` / `strip.go` / `worldlayout.go` / `motion.go` / `health.go`: enumerated,
  **no raster at all** — they only call `Renderer` methods (layout/state/transform
  logic). Nothing to port.

## Parity + bench (gate)

`internal/vroverlay/zigparity_test.go` (tag `zigvr`) renders each dispatched
surface through both paths and asserts **byte-equal pixels** — no diff bound
needed; identical masks + identical blend math ⇒ identical output. It also asserts
each Zig run actually dispatched (zero fallbacks). Cases: chat
(wrap/colors/empty/overflow, bgAlpha 0/0.82/1), menu (header/action/slider,
truncation, padRows, missing-glyph emoji), stats (graph fill+line+NaN gaps,
waiting view), small renders (hover row, strip hover, ghost min/padded, tooltip
short/wrapped/empty, outline brand/mint, strip empty/cells incl. truncation +
missing glyph, wrist × on/hover matrix + the no-logo branch), edit-border stamp
over a Go-rendered panel, path orbit (default/zoom clamp/2-point/degenerate).
ALL PASS pixel-identical. Zig-side `zig build test` covers blend vectors, bounds
rejection + atomicity (rejected list = no partial write).

Bench (Ryzen 9 5950X, go1.26.5 / zig 0.16.0 ReleaseFast, `-benchtime 300x`):

| render | Go | Zig | speedup |
|---|---|---|---|
| Panel 640×480 | 3393 µs | 947 µs | 3.6x |
| Menu 420×448 | 5039 µs | 1609 µs | 3.1x |
| Stats 640×480 | 6198 µs | 2079 µs | 3.0x |
| Path orbit 540×540 | 2463 µs | 337 µs | 7.3x |
| Ghost 420×560 | 2182 µs | 235 µs | 9.3x |
| Strip 480×96 | 1045 µs | 91 µs | 11.5x |
| Tooltip 380×94 | 838 µs | 493 µs | 1.7x |
| Hover row 420×56 | 340 µs | 135 µs | 2.5x |
| Outline 320×240 (border only) | 67 µs | 33 µs | 2.0x |
| Wrist 160×160 | 5833 µs | 5490 µs | 1.06x |

(Go's cost is the per-pixel interface-call fallback in image/draw; Zig executes
the same math in tight loops. Glyph-dominated renders — tooltip — win least, since
rasterizing the masks stays Go-side. Wrist is the `drawScaled` skip above.)

## Build

- `make zig` (scripts/build-zig.{sh,ps1}) now also builds
  `native/zigvr/zig-out/lib/libravevr.a`.
- Link: `-tags zigvr` (`make build-zig-all` = zigdsp + zigvr). Untagged builds
  use `internal/zigvr/zigvr_stub.go` → `Available()=false` → pure-Go raster.

## Verification status

- `zig build test`, tagged + untagged `go build/vet/test`, combined
  `-tags "zigdsp zigui zigvr"` link, gofmt + `zig fmt`: clean.
- Parity tests = the gate; mutation-checked (a 1-px band change in `border` fails
  outline + edit-border immediately).
- **Live headset verification NOT done — no HMD available in this environment**;
  first in-VR session should eyeball chat + menu + stats, then the editor chrome
  (wrist badge, strip + hover, menu hover row, tooltip, placement ghost,
  selection outline) and the camera-path orbit preview, and check
  `PerfProbe` shows `zig raster: ok>0 fallback=0`.
