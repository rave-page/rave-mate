package giokit

import (
	"image"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

// tooltipDelay is the hover dwell before the bubble appears.
const tooltipDelay = 450 * time.Millisecond

// Tooltip shows Text in a caption bubble below a widget after hovering. The caller
// supplies the hover state (e.g. btn.Click.Hovered()) - giokit widgets expose it.
type Tooltip struct {
	Text  string
	since time.Time
}

// Layout renders w and, once hovered ≥ tooltipDelay, defers the bubble on top.
func (t *Tooltip) Layout(gtx layout.Context, th *Theme, hovered bool, w layout.Widget) layout.Dimensions {
	dims := w(gtx)
	if !hovered || t.Text == "" {
		t.since = time.Time{}
		return dims
	}
	if t.since.IsZero() {
		t.since = gtx.Now
	}
	if gtx.Now.Sub(t.since) < tooltipDelay {
		gtx.Execute(op.InvalidateCmd{At: t.since.Add(tooltipDelay)})
		return dims
	}

	// Bubble: caption text on Surface with a hairline border, deferred above siblings.
	m := op.Record(gtx.Ops)
	tm := op.Record(gtx.Ops)
	tgtx := gtx
	tgtx.Constraints = layout.Constraints{Max: image.Pt(gtx.Dp(320), gtx.Constraints.Max.Y)}
	tdims := DrawText(tgtx, th, th.Sans, th.CaptionSize, th.Fg, 0, t.Text)
	tcall := tm.Stop()
	px, py := gtx.Dp(th.PadX), gtx.Dp(th.PadY)
	bub := image.Rect(0, 0, tdims.Size.X+2*px, tdims.Size.Y+2*py)
	off := op.Offset(image.Pt(0, dims.Size.Y+gtx.Dp(2))).Push(gtx.Ops)
	rr := gtx.Dp(4)
	paint.FillShape(gtx.Ops, th.Border, clip.UniformRRect(bub.Inset(-gtx.Dp(th.Hairline)), rr).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(bub, rr).Op(gtx.Ops))
	ti := op.Offset(image.Pt(px, py)).Push(gtx.Ops)
	tcall.Add(gtx.Ops)
	ti.Pop()
	off.Pop()
	op.Defer(gtx.Ops, m.Stop())
	return dims
}
