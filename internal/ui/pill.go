package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// pill is a small colored badge (rounded rect + centered text) - key chips, cue
// chips, Camelot wheel cells. Optionally tappable. Restyle via setBadge.
type pill struct {
	widget.BaseWidget
	rect     *canvas.Rectangle
	text     *canvas.Text
	onTapped func()
}

func newPill(label string, bg, fg color.Color, onTapped func()) *pill {
	b := &pill{
		rect:     canvas.NewRectangle(bg),
		text:     canvas.NewText(label, fg),
		onTapped: onTapped,
	}
	b.rect.CornerRadius = theme.InputRadiusSize()
	b.text.TextSize = theme.CaptionTextSize()
	b.text.TextStyle = fyne.TextStyle{Bold: true}
	b.ExtendBaseWidget(b)
	return b
}

// setPill restyles in place (list-row template reuse).
func (b *pill) setPill(label string, bg, fg color.Color) {
	b.text.Text = label
	b.text.Color = fg
	b.rect.FillColor = bg
	b.Refresh()
}

func (b *pill) MinSize() fyne.Size {
	ts := fyne.MeasureText(b.text.Text, b.text.TextSize, b.text.TextStyle)
	return fyne.NewSize(ts.Width+12, ts.Height+6)
}

func (b *pill) Tapped(*fyne.PointEvent) {
	if b.onTapped != nil {
		b.onTapped()
	}
}

func (b *pill) Cursor() desktop.Cursor {
	if b.onTapped != nil {
		return desktop.PointerCursor
	}
	return desktop.DefaultCursor
}

func (b *pill) CreateRenderer() fyne.WidgetRenderer {
	return &pillRenderer{b: b}
}

type pillRenderer struct{ b *pill }

func (r *pillRenderer) Layout(size fyne.Size) {
	r.b.rect.Resize(size)
	ts := fyne.MeasureText(r.b.text.Text, r.b.text.TextSize, r.b.text.TextStyle)
	r.b.text.Move(fyne.NewPos((size.Width-ts.Width)/2, (size.Height-ts.Height)/2))
	r.b.text.Resize(ts)
}

func (r *pillRenderer) MinSize() fyne.Size           { return r.b.MinSize() }
func (r *pillRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.b.rect, r.b.text} }
func (r *pillRenderer) Destroy()                     {}
func (r *pillRenderer) Refresh() {
	r.b.rect.Refresh()
	r.b.text.Refresh()
	r.Layout(r.b.Size())
}
