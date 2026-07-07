package giokit

import (
	"image"
	"image/color"
	"strings"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
)

// DrawText lays out txt in f at size/col. maxLines 0 = unlimited.
func DrawText(gtx layout.Context, th *Theme, f font.Font, size unit.Sp, col color.NRGBA, maxLines int, txt string) layout.Dimensions {
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: col}.Add(gtx.Ops)
	mat := m.Stop()
	return widget.Label{MaxLines: maxLines}.Layout(gtx, th.Shaper, f, size, txt, mat)
}

// Label variants - layout.Widget factories. Passive (not registry-registered).

// Body returns a body-text label (13sp, fg).
func Body(th *Theme, txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return DrawText(gtx, th, th.Sans, th.TextSize, th.Fg, 1, txt)
	}
}

// Muted returns a body-size secondary label.
func Muted(th *Theme, txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return DrawText(gtx, th, th.Sans, th.TextSize, th.Muted, 1, txt)
	}
}

// Caption returns a 12sp muted caption label.
func Caption(th *Theme, txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return DrawText(gtx, th, th.Sans, th.CaptionSize, th.Muted, 1, txt)
	}
}

// SmallCaps returns an uppercased Orbitron caption (section headers / strip titles).
func SmallCaps(th *Theme, txt string) layout.Widget {
	up := strings.ToUpper(txt)
	return func(gtx layout.Context) layout.Dimensions {
		return DrawText(gtx, th, th.Display, th.CaptionSize, th.Muted, 1, up)
	}
}

// Display returns an Orbitron display-text label (brand chrome / headings).
func Display(th *Theme, txt string) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		return DrawText(gtx, th, th.Display, th.DisplaySize, th.Fg, 1, txt)
	}
}

// Button is a dense (ControlHeight) push button. Label text or, when Label is empty,
// the Icon glyph (square). Primary fills brand pink. ID overrides the registry label.
type Button struct {
	Click    widget.Clickable
	Label    string
	Icon     rune // glyph variant when Label == ""
	ID       string
	Primary  bool
	Disabled bool
	OnClick  func()
}

// regLabel returns the registry activation label.
func (b *Button) regLabel() string {
	if b.ID != "" {
		return b.ID
	}
	if b.Label != "" {
		return b.Label
	}
	if b.Icon != 0 {
		return string(b.Icon)
	}
	return ""
}

// Layout renders the button and fires OnClick on click. reg may be nil.
func (b *Button) Layout(gtx layout.Context, th *Theme, reg *Registry) layout.Dimensions {
	if b.Disabled {
		gtx = gtx.Disabled()
	}
	for b.Click.Clicked(gtx) {
		if b.OnClick != nil && !b.Disabled {
			b.OnClick()
		}
	}
	txt := b.Label
	if txt == "" && b.Icon != 0 {
		txt = string(b.Icon)
	}
	fill, txtCol := th.Control, th.Fg
	if b.Primary {
		fill = th.BrandBase
	}
	if b.Disabled {
		txtCol = th.Muted
	}

	// Measure the label into a macro, then size the button around it.
	m := op.Record(gtx.Ops)
	tgtx := gtx
	tgtx.Constraints = layout.Constraints{Max: gtx.Constraints.Max}
	tdims := DrawText(tgtx, th, th.Sans, th.TextSize, txtCol, 1, txt)
	call := m.Stop()

	h := gtx.Dp(th.ControlHeight)
	w := tdims.Size.X + 2*gtx.Dp(th.PadX)
	if b.Label == "" { // icon glyph → square
		w = h
	}
	if w < h {
		w = h
	}
	size := image.Pt(w, h)

	dims := b.Click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		defer clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(th.Radius)).Push(gtx.Ops).Pop()
		paint.Fill(gtx.Ops, fill)
		if !b.Disabled && b.Click.Hovered() {
			paint.Fill(gtx.Ops, WithAlpha(th.BrandHot, 0x30))
		}
		if !b.Disabled && b.Click.Pressed() {
			paint.Fill(gtx.Ops, WithAlpha(th.BrandHot, 0x50))
		}
		tr := op.Offset(image.Pt((size.X-tdims.Size.X)/2, (size.Y-tdims.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		tr.Pop()
		return layout.Dimensions{Size: size}
	})
	reg.Add("button", b.regLabel(), dims.Size, b.Click.Click)
	return dims
}

// Toggle is a dense checkbox: 12dp box + caption label. ID overrides the registry label.
type Toggle struct {
	Bool     widget.Bool
	Label    string
	ID       string
	OnChange func(v bool)
}

// Layout renders the toggle; a click flips Bool.Value and fires OnChange. reg may be nil.
func (t *Toggle) Layout(gtx layout.Context, th *Theme, reg *Registry) layout.Dimensions {
	if t.Bool.Update(gtx) && t.OnChange != nil {
		t.OnChange(t.Bool.Value)
	}
	label := t.ID
	if label == "" {
		label = t.Label
	}
	dims := t.Bool.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		box := gtx.Dp(12)
		h := gtx.Dp(th.ControlHeight)
		gap := gtx.Dp(th.Gap)

		by := (h - box) / 2
		r := image.Rect(0, by, box, by+box)
		rr := gtx.Dp(3)
		if t.Bool.Value {
			paint.FillShape(gtx.Ops, th.BrandBase, clip.UniformRRect(r, rr).Op(gtx.Ops))
			in := gtx.Dp(3)
			paint.FillShape(gtx.Ops, th.Fg, clip.Rect(r.Inset(in)).Op())
		} else {
			paint.FillShape(gtx.Ops, th.Control, clip.UniformRRect(r, rr).Op(gtx.Ops))
			paint.FillShape(gtx.Ops, th.Border, clip.Stroke{Path: clip.UniformRRect(r, rr).Path(gtx.Ops), Width: float32(gtx.Dp(th.Hairline))}.Op())
		}

		m := op.Record(gtx.Ops)
		tgtx := gtx
		tgtx.Constraints = layout.Constraints{Max: gtx.Constraints.Max}
		tdims := DrawText(tgtx, th, th.Sans, th.CaptionSize, th.Fg, 1, t.Label)
		call := m.Stop()
		tr := op.Offset(image.Pt(box+gap, (h-tdims.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		tr.Pop()
		return layout.Dimensions{Size: image.Pt(box+gap+tdims.Size.X, h)}
	})
	reg.Add("toggle", label, dims.Size, func() {
		t.Bool.Value = !t.Bool.Value
		if t.OnChange != nil {
			t.OnChange(t.Bool.Value)
		}
	})
	return dims
}
