package vrslgrid

// zigfill.go - batched cell painting via the rz_fill_cells kernel (tag zigdsp).
// The Go SetRGBA fill loops stay as fallback + golden reference; parity gate:
// zigfill_parity_test.go.

import (
	"image"
	"image/color"

	"rave.page/mate/internal/zignative"
)

// zigFill reports the batched fill kernel is linked + ABI-compatible.
func zigFill() bool { return zignative.Available() }

// cellBatch accumulates square cell fills for one rz_fill_cells call.
// nil batch = callers use the Go fill loops directly.
type cellBatch struct{ cells []int32 }

// newCellBatch returns a batch when zig is set (capacity hint n cells).
func newCellBatch(zig bool, n int) *cellBatch {
	if !zig {
		return nil
	}
	return &cellBatch{cells: make([]int32, 0, 4*n)}
}

// add queues a size×size fill at pixel (x0,y0).
func (b *cellBatch) add(x0, y0, size int, c color.RGBA) {
	b.cells = append(b.cells, int32(x0), int32(y0), int32(size),
		int32(uint32(c.R)|uint32(c.G)<<8|uint32(c.B)<<16|uint32(c.A)<<24))
}

// flush paints every queued cell into img (zero-origin) and resets the batch.
func (b *cellBatch) flush(img *image.RGBA) {
	if b == nil || len(b.cells) == 0 {
		return
	}
	zignative.FillCells(img.Pix, img.Stride, img.Rect.Dx(), img.Rect.Dy(), b.cells)
	b.cells = b.cells[:0]
}

// cellPainterAt returns the 16px-cell paint fn for cell coords offset xOff pixels:
// batch-add when b != nil, else the Go fill.
func cellPainterAt(img *image.RGBA, b *cellBatch, xOff int) func(cx, cy int, col color.RGBA) {
	if b != nil {
		return func(cx, cy int, col color.RGBA) { b.add(xOff+cx*CellPx, cy*CellPx, CellPx, col) }
	}
	return func(cx, cy int, col color.RGBA) { fillCellAt(img, xOff, cx, cy, col) }
}
