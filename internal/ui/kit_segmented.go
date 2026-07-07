package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// Compact segmented control: a single joined pill split into mutually-exclusive cells.
// Denser and flatter than widget.RadioGroup (the old Segmented) - one row, no radio dots,
// caption-sized text. Drop-in for the mode/kind/sort selectors. Single-select only; the
// active cell is brand-filled, hover tints the cell under the cursor.

const (
	kitSegH    float32 = 26 // control height - compact, matches a dense toolbar row
	kitSegPadX float32 = 10 // horizontal padding inside each cell
)

// segIndexOf returns the index of label in options, or -1. Pure helper (unit-tested).
func segIndexOf(options []string, label string) int {
	for i, o := range options {
		if o == label {
			return i
		}
	}
	return -1
}

// kitSegmented is a compact single-select segmented control.
type kitSegmented struct {
	widget.BaseWidget
	options  []string
	selected int
	hovered  int // -1 = none
	onChange func(string)
	rend     *kitSegRenderer
}

// newKitSegmented builds a segmented control. selected picks the initial cell (falls back
// to the first). onChange fires with the chosen label whenever the selection changes.
func newKitSegmented(options []string, selected string, onChange func(string)) *kitSegmented {
	s := &kitSegmented{options: options, hovered: -1, onChange: onChange}
	if i := segIndexOf(options, selected); i >= 0 {
		s.selected = i
	}
	s.ExtendBaseWidget(s)
	return s
}

// Selected returns the current option label ("" when empty).
func (s *kitSegmented) Selected() string {
	if s.selected >= 0 && s.selected < len(s.options) {
		return s.options[s.selected]
	}
	return ""
}

// Select sets the active option by label and fires onChange if it changed.
func (s *kitSegmented) Select(label string) {
	s.selectIndex(segIndexOf(s.options, label), true)
}

// selectIndex sets the active cell; fires onChange only when notify and the value changed.
func (s *kitSegmented) selectIndex(i int, notify bool) {
	if i < 0 || i >= len(s.options) || i == s.selected {
		return
	}
	s.selected = i
	s.Refresh()
	if notify && s.onChange != nil {
		s.onChange(s.options[i])
	}
}

func (s *kitSegmented) MinSize() fyne.Size {
	var w float32
	for _, o := range s.options {
		ts := fyne.MeasureText(o, theme.CaptionTextSize(), fyne.TextStyle{Bold: true})
		w += ts.Width + kitSegPadX*2
	}
	return fyne.NewSize(w, kitSegH)
}

func (s *kitSegmented) Tapped(ev *fyne.PointEvent) {
	if s.rend == nil {
		return
	}
	s.selectIndex(s.rend.cellAt(ev.Position.X), true)
}

func (s *kitSegmented) MouseIn(ev *desktop.MouseEvent)    { s.setHover(ev.Position.X) }
func (s *kitSegmented) MouseMoved(ev *desktop.MouseEvent) { s.setHover(ev.Position.X) }
func (s *kitSegmented) MouseOut()                         { s.setHover(-1) }

func (s *kitSegmented) setHover(x float32) {
	i := -1
	if x >= 0 && s.rend != nil {
		i = s.rend.cellAt(x)
	}
	if i != s.hovered {
		s.hovered = i
		s.Refresh()
	}
}

func (s *kitSegmented) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (s *kitSegmented) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(colSecondary)
	bg.CornerRadius = theme.InputRadiusSize()
	bg.StrokeColor = colBorder
	bg.StrokeWidth = 1
	r := &kitSegRenderer{s: s, bg: bg}
	for range s.options {
		cell := canvas.NewRectangle(color.Transparent)
		cell.CornerRadius = theme.InputRadiusSize()
		txt := canvas.NewText("", colForeground)
		txt.TextSize = theme.CaptionTextSize()
		txt.TextStyle = fyne.TextStyle{Bold: true}
		r.cells = append(r.cells, cell)
		r.texts = append(r.texts, txt)
	}
	s.rend = r
	r.objs = []fyne.CanvasObject{bg}
	for i := range s.options {
		r.objs = append(r.objs, r.cells[i], r.texts[i])
	}
	r.applyState()
	return r
}

type kitSegRenderer struct {
	s      *kitSegmented
	bg     *canvas.Rectangle
	cells  []*canvas.Rectangle
	texts  []*canvas.Text
	objs   []fyne.CanvasObject
	widths []float32 // per-cell width from the last Layout (for hit-testing)
}

// cellAt maps an x offset to a cell index, clamped to the last cell.
func (r *kitSegRenderer) cellAt(x float32) int {
	acc := float32(0)
	for i, w := range r.widths {
		acc += w
		if x <= acc {
			return i
		}
	}
	if n := len(r.s.options); n > 0 {
		return n - 1
	}
	return -1
}

func (r *kitSegRenderer) applyState() {
	for i, txt := range r.texts {
		txt.Text = r.s.options[i]
		switch {
		case i == r.s.selected:
			r.cells[i].FillColor = colBrandBase
			txt.Color = colBackground
		case i == r.s.hovered:
			r.cells[i].FillColor = withAlpha(colBrandHot, 0x2e)
			txt.Color = colForeground
		default:
			r.cells[i].FillColor = color.Transparent
			txt.Color = colMuted
		}
		r.cells[i].Refresh()
		txt.Refresh()
	}
}

func (r *kitSegRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	r.widths = r.widths[:0]
	// Cell width = text width + padding; the last cell absorbs any rounding remainder so
	// the joined pill fills the full control width with no seam.
	total := float32(0)
	raw := make([]float32, len(r.s.options))
	for i, o := range r.s.options {
		ts := fyne.MeasureText(o, theme.CaptionTextSize(), fyne.TextStyle{Bold: true})
		raw[i] = ts.Width + kitSegPadX*2
		total += raw[i]
	}
	scale := float32(1)
	if total > 0 {
		scale = size.Width / total
	}
	x := float32(0)
	for i := range r.s.options {
		w := raw[i] * scale
		if i == len(r.s.options)-1 {
			w = size.Width - x // exact fill
		}
		r.widths = append(r.widths, w)
		r.cells[i].Resize(fyne.NewSize(w, size.Height))
		r.cells[i].Move(fyne.NewPos(x, 0))
		ts := fyne.MeasureText(r.texts[i].Text, r.texts[i].TextSize, r.texts[i].TextStyle)
		r.texts[i].Move(fyne.NewPos(x+(w-ts.Width)/2, (size.Height-ts.Height)/2))
		r.texts[i].Resize(ts)
		x += w
	}
}

func (r *kitSegRenderer) MinSize() fyne.Size           { return r.s.MinSize() }
func (r *kitSegRenderer) Objects() []fyne.CanvasObject { return r.objs }
func (r *kitSegRenderer) Destroy()                     {}
func (r *kitSegRenderer) Refresh() {
	r.bg.Refresh()
	r.applyState()
	r.Layout(r.s.Size())
	canvas.Refresh(r.s)
}
