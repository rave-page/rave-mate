package giokit

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

// List renders uniform dense rows (ListRowH, 22dp) with zebra striping. Registry
// offsets are pushed per visible row so registered row widgets get correct bounds.
type List struct {
	L layout.List
}

// Layout renders count rows via row(gtx, i). Row height is fixed; the row widget gets
// exact-height constraints. reg may be nil.
func (l *List) Layout(gtx layout.Context, th *Theme, reg *Registry, count int, row func(gtx layout.Context, i int) layout.Dimensions) layout.Dimensions {
	l.L.Axis = layout.Vertical
	rowPx := gtx.Dp(th.ListRowH)
	return l.L.Layout(gtx, count, func(gtx layout.Context, i int) layout.Dimensions {
		gtx.Constraints.Min.Y, gtx.Constraints.Max.Y = rowPx, rowPx
		w := gtx.Constraints.Max.X
		if i%2 == 1 {
			paint.FillShape(gtx.Ops, WithAlpha(th.Fg, 0x06), clip.Rect(image.Rect(0, 0, w, rowPx)).Op())
		}
		reg.PushOffset(image.Pt(0, RowY(i, l.L.Position.First, l.L.Position.Offset, rowPx)))
		defer reg.PopOffset()
		dims := row(gtx, i)
		dims.Size.Y = rowPx
		return dims
	})
}

// RowY returns row i's y offset within the viewport of a uniform-row list positioned at
// (first, offsetPx).
func RowY(i, first, offsetPx, rowPx int) int { return (i-first)*rowPx - offsetPx }

// VisibleRows returns the max number of uniform rows partially visible in viewportPx
// (worst case: scrolled mid-row).
func VisibleRows(viewportPx, rowPx int) int {
	if rowPx <= 0 || viewportPx <= 0 {
		return 0
	}
	return (viewportPx+rowPx-1)/rowPx + 1
}

// RowsHeight returns the pixel height of n uniform rows.
func RowsHeight(n, rowPx int) int {
	if n < 0 {
		return 0
	}
	return n * rowPx
}
