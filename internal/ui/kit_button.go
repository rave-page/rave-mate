package ui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// kitButtonVariant selects the kitButton visual role (maps from widget.ButtonImportance).
type kitButtonVariant int

const (
	kitBtnOutline kitButtonVariant = iota // neutral: metal fill + bevel rim (Low/Medium)
	kitBtnBrand                           // primary CTA: brand fill (High)
	kitBtnDanger                          // destructive: error rim + text (Danger)
)

// Note: the target was ~26px, but the dense theme metrics already bring a stock
// widget.Button to 24px - 22px is the point where kitButton actually beats it
// (icon 16 + 3px margins; guarded by TestKitButton).
const (
	kitBtnHeight float32 = 22 // fixed compact height (themed stock button = 24, raw stock ≈ 36)
	kitBtnHPad   float32 = 8  // horizontal content inset
	kitBtnGap    float32 = 4  // icon ↔ label gap
)

// kitButton is the dense drop-in for widget.Button on compact surfaces: fixed 26px
// height, caption-size Orbitron-bold label, optional inline icon, hover tint. All
// colors from theme.go tokens. API mirrors widget.Button (OnTapped field, SetText/
// SetIcon, Enable/Disable) so sweeps stay mechanical.
type kitButton struct {
	widget.DisableableWidget
	OnTapped func()

	text    string
	icon    fyne.Resource
	variant kitButtonVariant
	hovered bool
}

// newKitButton builds a compact outline (neutral) text button.
func newKitButton(text string, tapped func()) *kitButton {
	return newKitButtonWithIcon(text, nil, tapped)
}

// newKitButtonWithIcon builds a compact outline button with an inline icon.
func newKitButtonWithIcon(text string, icon fyne.Resource, tapped func()) *kitButton {
	b := &kitButton{OnTapped: tapped, text: text, icon: icon}
	b.ExtendBaseWidget(b)
	return b
}

// SetVariant switches the visual role (outline/brand/danger).
func (b *kitButton) SetVariant(v kitButtonVariant) {
	if b.variant == v {
		return
	}
	b.variant = v
	b.Refresh()
}

// SetText swaps the label.
func (b *kitButton) SetText(s string) {
	if b.text == s {
		return
	}
	b.text = s
	b.Refresh()
}

// SetIcon swaps the glyph (nil hides it).
func (b *kitButton) SetIcon(r fyne.Resource) {
	b.icon = r
	b.Refresh()
}

func (b *kitButton) MinSize() fyne.Size {
	iconSz := theme.Size(theme.SizeNameInlineIcon)
	if b.text == "" { // icon-only → compact near-square
		w := iconSz + 2*kitBtnGap
		if w < kitBtnHeight {
			w = kitBtnHeight
		}
		return fyne.NewSize(w, kitBtnHeight)
	}
	w := 2*kitBtnHPad + fyne.MeasureText(b.text, theme.CaptionTextSize(), fyne.TextStyle{Bold: true}).Width
	if b.icon != nil {
		w += iconSz + kitBtnGap
	}
	return fyne.NewSize(w, kitBtnHeight)
}

func (b *kitButton) Tapped(*fyne.PointEvent) {
	if b.Disabled() || b.OnTapped == nil {
		return
	}
	b.OnTapped()
}

func (b *kitButton) MouseIn(*desktop.MouseEvent) {
	b.hovered = true
	b.Refresh()
}
func (b *kitButton) MouseMoved(*desktop.MouseEvent) {}
func (b *kitButton) MouseOut() {
	b.hovered = false
	b.Refresh()
}

func (b *kitButton) Cursor() desktop.Cursor {
	if b.Disabled() {
		return desktop.DefaultCursor
	}
	return desktop.PointerCursor
}

func (b *kitButton) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(colSecondary)
	bg.CornerRadius = theme.InputRadiusSize()
	hov := canvas.NewRectangle(color.Transparent)
	hov.CornerRadius = bg.CornerRadius
	ic := widget.NewIcon(theme.CancelIcon()) // placeholder; applyState sets/hides
	lbl := canvas.NewText(b.text, colForeground)
	lbl.TextStyle = fyne.TextStyle{Bold: true}
	lbl.TextSize = theme.CaptionTextSize()
	r := &kitButtonRenderer{b: b, bg: bg, hov: hov, ic: ic, lbl: lbl,
		objs: []fyne.CanvasObject{bg, hov, ic, lbl}}
	r.applyState()
	return r
}

type kitButtonRenderer struct {
	b    *kitButton
	bg   *canvas.Rectangle
	hov  *canvas.Rectangle
	ic   *widget.Icon
	lbl  *canvas.Text
	objs []fyne.CanvasObject
}

func (r *kitButtonRenderer) applyState() {
	b := r.b
	fill, rim, fg := colSecondary, colBorder, colForeground
	switch b.variant {
	case kitBtnBrand:
		fill, rim = colBrandBase, color.Transparent
	case kitBtnDanger:
		rim, fg = withAlpha(colError, 0x88), colError
	}
	hover := withAlpha(colBrandHot, 0x22)
	switch b.variant {
	case kitBtnBrand:
		hover = withAlpha(colBrandHot, 0x40)
	case kitBtnDanger:
		hover = withAlpha(colError, 0x33)
	}
	if b.Disabled() {
		fill, rim, fg = withAlpha(colSecondary, 0x80), withAlpha(colBorder, 0x80), colMuted
	}
	r.bg.FillColor = fill
	r.bg.StrokeColor = rim
	r.bg.StrokeWidth = 1
	if b.hovered && !b.Disabled() {
		r.hov.FillColor = hover
	} else {
		r.hov.FillColor = color.Transparent
	}
	if b.icon != nil {
		r.ic.SetResource(b.icon)
		r.ic.Show()
	} else {
		r.ic.Hide()
	}
	r.lbl.Text = b.text
	r.lbl.Color = fg
	r.lbl.TextSize = theme.CaptionTextSize()
	r.bg.Refresh()
	r.hov.Refresh()
	r.lbl.Refresh()
}

// Layout centers icon+label as one group, like widget.Button.
func (r *kitButtonRenderer) Layout(size fyne.Size) {
	r.bg.Resize(size)
	r.hov.Resize(size)
	iconSz := theme.Size(theme.SizeNameInlineIcon)
	txt := r.lbl.MinSize()
	content := txt.Width
	if r.b.icon != nil {
		content += iconSz
		if r.b.text != "" {
			content += kitBtnGap
		}
	}
	x := (size.Width - content) / 2
	if x < kitBtnHPad/2 {
		x = kitBtnHPad / 2
	}
	if r.b.icon != nil {
		r.ic.Resize(fyne.NewSize(iconSz, iconSz))
		r.ic.Move(fyne.NewPos(x, (size.Height-iconSz)/2))
		x += iconSz + kitBtnGap
	}
	r.lbl.Move(fyne.NewPos(x, (size.Height-txt.Height)/2))
}

func (r *kitButtonRenderer) MinSize() fyne.Size           { return r.b.MinSize() }
func (r *kitButtonRenderer) Objects() []fyne.CanvasObject { return r.objs }
func (r *kitButtonRenderer) Destroy()                     {}
func (r *kitButtonRenderer) Refresh() {
	r.applyState()
	r.Layout(r.b.Size())
	canvas.Refresh(r.b)
}
