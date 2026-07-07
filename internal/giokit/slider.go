package giokit

import (
	"image"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
)

// Slider is a dense horizontal slider over widget.Float (Value in [0,1]). Width comes
// from the max constraint; height is ControlHeight. OnChange fires per drag move,
// OnCommit once on release. Set Value directly for external updates (e.g. playback
// position) - skip while Dragging().
type Slider struct {
	Float    widget.Float
	ID       string // registry label
	OnChange func(v float32)
	OnCommit func(v float32)

	wasDragging bool
}

// Dragging reports whether the user is holding the slider.
func (s *Slider) Dragging() bool { return s.Float.Dragging() }

// Layout renders track + fill + thumb and handles drag input. reg may be nil.
func (s *Slider) Layout(gtx layout.Context, th *Theme, reg *Registry) layout.Dimensions {
	if s.Float.Update(gtx) && s.OnChange != nil {
		s.OnChange(s.Float.Value)
	}
	drag := s.Float.Dragging()
	if s.wasDragging && !drag && s.OnCommit != nil {
		s.OnCommit(s.Float.Value)
	}
	s.wasDragging = drag

	w := gtx.Constraints.Max.X
	h := gtx.Dp(th.ControlHeight)
	size := image.Pt(w, h)

	// Track (3dp) centered vertically; filled part brand pink; thumb 4×12dp.
	trackH := gtx.Dp(3)
	ty := (h - trackH) / 2
	rr := trackH / 2
	paint.FillShape(gtx.Ops, th.Control, clip.UniformRRect(image.Rect(0, ty, w, ty+trackH), rr).Op(gtx.Ops))
	fillW := int(s.Float.Value * float32(w))
	if fillW > 0 {
		paint.FillShape(gtx.Ops, th.BrandBase, clip.UniformRRect(image.Rect(0, ty, fillW, ty+trackH), rr).Op(gtx.Ops))
	}
	thW, thH := gtx.Dp(4), gtx.Dp(12)
	tx := fillW - thW/2
	if tx < 0 {
		tx = 0
	} else if tx > w-thW {
		tx = w - thW
	}
	thumbCol := th.Fg
	if drag {
		thumbCol = th.BrandHot
	}
	paint.FillShape(gtx.Ops, thumbCol, clip.UniformRRect(image.Rect(tx, (h-thH)/2, tx+thW, (h-thH)/2+thH), thW/2).Op(gtx.Ops))

	// Input area spans the whole control (Float maps drag X over Min.X).
	fgtx := gtx
	fgtx.Constraints.Min = size
	s.Float.Layout(fgtx, layout.Horizontal, unit.Dp(8))

	reg.Add("slider", s.ID, size, nil)
	return layout.Dimensions{Size: size}
}
