/* ravevr — rave-mate VR-overlay raster executor (Zig), C ABI. Mirror of src/root.zig.
 * ABI v1. Go binding: internal/zigvr.
 *
 * Executes a display list of composition ops into an RGBA8 (image.NRGBA layout,
 * non-premultiplied) canvas. Blend math is an exact integer replica of Go
 * image/draw's RGBA64Image fallback (NRGBA dst + Uniform src / *image.Alpha mask),
 * so output is pixel-identical to the Go render path. Layout + glyph rasterization
 * stay Go-side (x/image opentype); glyph alpha masks arrive via the mask arena. */
#ifndef RAVEVR_H
#define RAVEVR_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

uint32_t rz_vr_abi_version(void);

/* One display-list op. Rect is pre-clipped by the caller to [0,w)x[0,h).
 * kind 0 STORE: fill rect with the exact NRGBA bytes in sr..sa (low byte each).
 * kind 1 OVER:  uniform source-over fill; sr..sa = premultiplied 16-bit color.
 * kind 2 GLYPH: alpha-mask source-over (glyph blit); sr..sa premult 16-bit;
 *               mask rows at mask_off in the arena, stride = w (h rows). */
typedef struct {
  int32_t  x, y, w, h;
  uint32_t kind;
  uint16_t sr, sg, sb, sa;
  uint32_t mask_off;
} RzVrOp;

/* Executes ops in order into canvas (w*h*4 bytes, NRGBA, stride = 4*w).
 * Atomic: every op is validated before any pixel is written, so a non-zero return
 * leaves the canvas untouched (the caller redraws via its own raster path).
 * Returns 0 ok; -1 bad args; -2 op rect/kind/mask out of bounds. */
int32_t rz_vr_render(uint8_t *canvas, int32_t w, int32_t h,
                     const RzVrOp *ops, size_t n_ops,
                     const uint8_t *mask, size_t mask_len);

#ifdef __cplusplus
}
#endif

#endif /* RAVEVR_H */
