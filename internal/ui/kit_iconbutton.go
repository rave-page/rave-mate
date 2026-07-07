package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// kitIconBtnSize is the compact square target for an icon button - smaller than Fyne's
// text-button height, big enough to stay clickable in a dense strip.
const kitIconBtnSize float32 = 30

// kitIconButton is an icon-only, compact, square button with a hover/tap tooltip. It
// replaces widget.NewButtonWithIcon("", …) in dense toolbars (kitToolStrip), grid
// hover-overlays (kitDensityGrid) and the Library nav rail, where a full text button
// wastes width and carries no tooltip. Visual state, all from theme.go tokens:
//   - hover  → subtle brand-hot tint (error tint when danger)
//   - active → brand fill (selected/toggled)
type kitIconButton struct {
	widget.BaseWidget
	icon     fyne.Resource
	onTapped func()
	active   bool
	hovered  bool
	danger   bool // destructive action → error-tinted hover
	tt       kitTooltip
	rend     *kitIconButtonRenderer
}

// newKitIconButton builds an icon button with a tooltip. tip=="" disables the tooltip.
func newKitIconButton(icon fyne.Resource, tip string, onTapped func()) *kitIconButton {
	b := &kitIconButton{icon: icon, onTapped: onTapped}
	b.tt.text = tip
	b.ExtendBaseWidget(b)
	return b
}

// SetActive toggles the selected (brand-filled) state.
func (b *kitIconButton) SetActive(on bool) {
	if b.active == on {
		return
	}
	b.active = on
	b.Refresh()
}

// SetDanger marks the button destructive (error-tinted hover).
func (b *kitIconButton) SetDanger(on bool) { b.danger = on }

// SetIcon swaps the glyph (e.g. play↔pause).
func (b *kitIconButton) SetIcon(r fyne.Resource) {
	b.icon = r
	b.Refresh()
}

// SetTip updates the tooltip text.
func (b *kitIconButton) SetTip(s string) { b.tt.text = s }

func (b *kitIconButton) MinSize() fyne.Size { return fyne.NewSize(kitIconBtnSize, kitIconBtnSize) }

func (b *kitIconButton) Tapped(*fyne.PointEvent) {
	b.tt.hide()
	if b.onTapped != nil {
		b.onTapped()
	}
}

func (b *kitIconButton) MouseIn(*desktop.MouseEvent) {
	b.hovered = true
	b.Refresh()
	b.tt.show(b)
}
func (b *kitIconButton) MouseMoved(*desktop.MouseEvent) {}
func (b *kitIconButton) MouseOut() {
	b.hovered = false
	b.Refresh()
	b.tt.hide()
}

func (b *kitIconButton) Cursor() desktop.Cursor {
	if b.onTapped != nil {
		return desktop.PointerCursor
	}
	return desktop.DefaultCursor
}

func (b *kitIconButton) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(color.Transparent)
	bg.CornerRadius = theme.InputRadiusSize()
	ic := widget.NewIcon(b.icon)
	r := &kitIconButtonRenderer{b: b, bg: bg, ic: ic, objs: []fyne.CanvasObject{bg, ic}}
	b.rend = r
	r.applyState()
	return r
}

type kitIconButtonRenderer struct {
	b    *kitIconButton
	bg   *canvas.Rectangle
	ic   *widget.Icon
	objs []fyne.CanvasObject
}

func (r *kitIconButtonRenderer) applyState() {
	switch {
	case r.b.active:
		r.bg.FillColor = colBrandBase
	case r.b.hovered && r.b.danger:
		r.bg.FillColor = withAlpha(colError, 0x33)
	case r.b.hovered:
		r.bg.FillColor = withAlpha(colBrandHot, 0x2e)
	default:
		r.bg.FillColor = color.Transparent
	}
	r.ic.SetResource(r.b.icon)
	r.bg.Refresh()
}

func (r *kitIconButtonRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	m := r.ic.MinSize()
	r.ic.Resize(m)
	r.ic.Move(fyne.NewPos((size.Width-m.Width)/2, (size.Height-m.Height)/2))
}

func (r *kitIconButtonRenderer) MinSize() fyne.Size           { return r.b.MinSize() }
func (r *kitIconButtonRenderer) Objects() []fyne.CanvasObject { return r.objs }
func (r *kitIconButtonRenderer) Destroy()                     {}
func (r *kitIconButtonRenderer) Refresh() {
	r.applyState()
	r.Layout(r.b.Size())
	canvas.Refresh(r.b)
}
