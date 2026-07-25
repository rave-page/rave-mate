# Zig VR-overlay raster port (`native/zigvr` + `internal/zigvr`, tag `zigvr`)

Port of the vroverlay RENDERING hot path to Zig per the strangler pattern
(ZIG_MIGRATION.md). Go stays the golden reference + untagged fallback.

## What vroverlay renders (survey)

All CPU raster into `*image.NRGBA` (RGBA8 non-premult), uploaded to OpenVR via
`SetOverlayRaw(…, depth 4)` (openvr.go, `vr` featurehost child owns the cgo).
Drawing stack: stdlib `image/draw` + `x/image/font/opentype` (embedded
Orbitron-Bold/Medium) — no gg/Fyne canvas.

Surfaces (render.go / stats.go / manager.go):
- **Content panels** 640×480 (`Renderer.Panel`): Twitch chat, alerts, viewer
  count, chatter list, OBS cockpit — wrapped/balanced text rows, bottom-aligned.
  Rendered on the 100ms tick, gated by a content signature (`linesSig`).
- **Stats panels** 640×480 (`RenderStats`): perf/network/timing — rows + autoscaled
  multi-series graph + footer. Throttled ~2 Hz + signature.
- **Menu** 420×(rows+1)·56 (`RenderMenu`): in-VR editor menu; re-rendered on every
  nav/interaction (editor consumeDirty → ~11ms reconcile).
- Rare/small (NOT dispatched, stay pure Go): hover row, ghost, tooltip, wrist
  button (CatmullRom logo scale), strip, dot, outline, `Border`.

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
worst-case real render is ≤ ~3k ops / ~1 MiB. Drop policy: exceeding a cap fails
the record and that frame renders via the Go path. The `List` is recycled per
frame (VR goroutine only), so steady-state allocs don't grow.

## Parity + bench (gate)

`internal/vroverlay/zigparity_test.go` (tag `zigvr`) renders each dispatched
surface through both paths and asserts **byte-equal pixels** — no diff bound
needed; identical masks + identical blend math ⇒ identical output. Cases: chat
(wrap/colors/empty/overflow, bgAlpha 0/0.82/1), menu (header/action/slider,
truncation, padRows, missing-glyph emoji), stats (graph fill+line+NaN gaps,
waiting view). ALL PASS pixel-identical. Zig-side `zig build test` covers blend
vectors + bounds rejection.

Bench (Ryzen 9 5950X, go1.26.5 / zig 0.16.0 ReleaseFast):

| render | Go | Zig | speedup |
|---|---|---|---|
| Panel 640×480 | 3.08 ms | 0.86 ms | 3.6x |
| Menu 420×448 | 4.78 ms | 1.56 ms | 3.1x |
| Stats 640×480 | 7.76 ms | 2.34 ms | 3.3x |

(Go's cost is the per-pixel interface-call fallback in image/draw; Zig executes
the same math in tight loops.)

## Build

- `make zig` (scripts/build-zig.{sh,ps1}) now also builds
  `native/zigvr/zig-out/lib/libravevr.a`.
- Link: `-tags zigvr` (`make build-zig-all` = zigdsp + zigvr). Untagged builds
  use `internal/zigvr/zigvr_stub.go` → `Available()=false` → pure-Go raster.

## Verification status

- `zig build test`, tagged + untagged `go build/vet/test`, gofmt: clean.
- Parity tests = the gate. **Live headset verification NOT done — no HMD
  available in this environment**; first in-VR session should eyeball chat +
  menu + stats overlays once.
