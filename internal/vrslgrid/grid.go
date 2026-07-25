// Package vrslgrid rasterizes a DMX universe store into the VRSL (VR Stage Lighting) DMX-in-video
// grid - the pixel encoding VRChat club worlds decode back into light values from a video stream.
//
// Layout (per DMX_TIMECODE_RESEARCH.md, from the MIT VRSLDMX.cginc shader source):
//   - 1 DMX channel = 1 cell = 16×16 px; the decoder point-samples each cell's centre.
//   - Per-universe addressing: x = ch%13, y = ch/13 (ch 0..511) → a 13-wide × 40-tall cell block.
//   - Universe stride = 520 cells = 512 channels + 8 dead padding cells (the tail of row 39).
//   - Mono: luminance 0..255 = DMX 0..255 written to R=G=B.
//   - Extended 9-universe RGB: universes 1-3 → R, 4-6 → G, 7-9 → B, folded onto the u1-3 cell
//     positions (3 blocks, coloured).
//
// COLOUR SPACE CAVEAT: VRSL 2.7.0+ decodes in LINEAR space, so we write raw DMX bytes with NO gamma
// applied. If the capture/encode path (OBS/Spout/stream) applies an sRGB↔linear conversion, every
// value skews - keep the whole chain linear. Surfaced as a UI tooltip too.
//
// Exact on-canvas placement/orientation for a given world version needs validation against a live
// VRSL world with a lighting console (not verifiable without hardware); the addressing math here is
// locked by tests.
package vrslgrid

import (
	"image"
	"image/color"
	"image/draw"
	"strings"
)

// Grid geometry (16×16 cells; a universe = 13×40 cells = 520 stride, 512 ch + 8 dead).
const (
	CellPx      = 16
	ColsPerUni  = 13
	RowsPerUni  = 40
	ChPerUni    = 512
	CellsPerUni = ColsPerUni * RowsPerUni // 520
	DeadCells   = CellsPerUni - ChPerUni  // 8
	GridWidthPx = ColsPerUni * CellPx     // 208
)

// Mode selects the pixel packing.
type Mode string

const (
	ModeMono Mode = "mono" // one 13×40 grey block per universe
	ModeRGB9 Mode = "rgb9" // 3 blocks: u[0:3]→R, u[3:6]→G, u[6:9]→B
)

// ParseMode maps a config string to a Mode (default ModeMono).
func ParseMode(s string) Mode {
	if strings.EqualFold(strings.TrimSpace(s), string(ModeRGB9)) {
		return ModeRGB9
	}
	return ModeMono
}

// Reader is the universe read surface the renderer consumes (satisfied by *artnet.Store, and by a
// future peer-bus DMX source).
type Reader interface {
	Get(u uint16) ([512]byte, bool)
}

// cellForChannel maps a DMX channel (0..511) to its local cell (x,y) in a universe block.
func cellForChannel(ch int) (x, y int) { return ch % ColsPerUni, ch / ColsPerUni }

// Render rasterizes universes into a VRSL grid image. Mono stacks one grey block per universe;
// RGB9 packs the first up-to-9 universes into 3 colour blocks. Dead padding cells stay opaque black.
func Render(r Reader, universes []int, mode Mode) *image.RGBA {
	return render(r, universes, mode, zigFill())
}

// render is Render with an explicit cell-fill backend (zig = batched rz_fill_cells;
// false = the Go loops). Parity gate: zigfill_parity_test.go.
func render(r Reader, universes []int, mode Mode, zig bool) *image.RGBA {
	if mode == ModeRGB9 {
		return renderRGB9(r, universes, zig)
	}
	return renderMono(r, universes, zig)
}

func renderMono(r Reader, universes []int, zig bool) *image.RGBA {
	blocks := len(universes)
	if blocks < 1 {
		blocks = 1
	}
	img := newBlack(blocks)
	fb := newCellBatch(zig, len(universes)*ChPerUni)
	paint := cellPainterAt(img, fb, 0)
	for i, uni := range universes {
		if uni < 0 {
			continue
		}
		data, _ := r.Get(uint16(uni))
		for ch := 0; ch < ChPerUni; ch++ {
			v := data[ch]
			cx, cy := cellForChannel(ch)
			paint(cx, i*RowsPerUni+cy, color.RGBA{v, v, v, 255})
		}
	}
	fb.flush(img)
	return img
}

func renderRGB9(r Reader, universes []int, zig bool) *image.RGBA {
	img := newBlack(3)
	fb := newCellBatch(zig, 3*ChPerUni)
	paint := cellPainterAt(img, fb, 0)
	get := func(idx int) [512]byte {
		if idx < len(universes) && universes[idx] >= 0 {
			d, _ := r.Get(uint16(universes[idx]))
			return d
		}
		return [512]byte{}
	}
	for j := 0; j < 3; j++ {
		dr, dg, db := get(j), get(j+3), get(j+6)
		for ch := 0; ch < ChPerUni; ch++ {
			cx, cy := cellForChannel(ch)
			paint(cx, j*RowsPerUni+cy, color.RGBA{dr[ch], dg[ch], db[ch], 255})
		}
	}
	fb.flush(img)
	return img
}

// newBlack allocates an opaque-black grid tall enough for n universe blocks.
func newBlack(blocks int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, GridWidthPx, blocks*RowsPerUni*CellPx))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{0, 0, 0, 255}), image.Point{}, draw.Src)
	return img
}
