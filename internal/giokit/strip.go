package giokit

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

// ToolStrip renders a ToolStripH (32dp) toolbar: children left-to-right, Gap apart,
// control-height-centered, on a Surface bar with a bottom hairline. Registry offsets
// are pushed per child, so registered widgets get window-correct bounds. Use Sep(th)
// between groups.
func ToolStrip(gtx layout.Context, th *Theme, reg *Registry, children ...layout.Widget) layout.Dimensions {
	w := gtx.Constraints.Max.X
	h := gtx.Dp(th.ToolStripH)
	hair := gtx.Dp(th.Hairline)
	paint.FillShape(gtx.Ops, th.Surface, clip.Rect(image.Rect(0, 0, w, h)).Op())
	paint.FillShape(gtx.Ops, th.Border, clip.Rect(image.Rect(0, h-hair, w, h)).Op())

	x := gtx.Dp(th.PadX)
	yc := (h - gtx.Dp(th.ControlHeight)) / 2
	gap := gtx.Dp(th.Gap)
	for _, c := range children {
		if x >= w {
			break
		}
		cgtx := gtx
		cgtx.Constraints = layout.Constraints{Max: image.Pt(w-x, h)}
		reg.PushOffset(image.Pt(x, yc))
		m := op.Record(gtx.Ops)
		dims := c(cgtx)
		call := m.Stop()
		reg.PopOffset()
		tr := op.Offset(image.Pt(x, yc)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		tr.Pop()
		x += dims.Size.X + gap
	}
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// Sep returns a thin vertical group separator for ToolStrip.
func Sep(th *Theme) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		h := gtx.Dp(th.ControlHeight)
		wl := gtx.Dp(th.Hairline)
		in := gtx.Dp(4)
		paint.FillShape(gtx.Ops, th.Border, clip.Rect(image.Rect(0, in, wl, h-in)).Op())
		return layout.Dimensions{Size: image.Pt(wl, h)}
	}
}

// StatusStrip renders the StatusStripH (24dp) 3-zone bottom bar: left status, centered
// info, right meta, on Surface with a top hairline. Zones are meant to be passive
// labels (registry offsets aren't pushed here - put controls in a ToolStrip). Any zone
// may be nil.
func StatusStrip(gtx layout.Context, th *Theme, left, center, right layout.Widget) layout.Dimensions {
	w := gtx.Constraints.Max.X
	h := gtx.Dp(th.StatusStripH)
	hair := gtx.Dp(th.Hairline)
	pad := gtx.Dp(th.PadX)
	paint.FillShape(gtx.Ops, th.Surface, clip.Rect(image.Rect(0, 0, w, h)).Op())
	paint.FillShape(gtx.Ops, th.Border, clip.Rect(image.Rect(0, 0, w, hair)).Op())

	place := func(c layout.Widget, xFor func(cw int) int) {
		if c == nil {
			return
		}
		cgtx := gtx
		cgtx.Constraints = layout.Constraints{Max: image.Pt(w-2*pad, h)}
		m := op.Record(gtx.Ops)
		dims := c(cgtx)
		call := m.Stop()
		tr := op.Offset(image.Pt(xFor(dims.Size.X), (h-dims.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		tr.Pop()
	}
	place(left, func(int) int { return pad })
	place(center, func(cw int) int { return (w - cw) / 2 })
	place(right, func(cw int) int { return w - cw - pad })
	return layout.Dimensions{Size: image.Pt(w, h)}
}
