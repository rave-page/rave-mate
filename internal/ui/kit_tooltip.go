package ui

// Dense-UI component kit (kit_*.go): reusable, brand-styled Fyne primitives that back the
// re-laid-out Library tab and any future dense control page. Everything styles from
// theme.go tokens only (no hardcoded hex/px beyond compact sizing constants documented
// per widget). The kit owns the `kit`-prefixed namespace in package ui.

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

// kitTooltip is a lazily-created hover/tap tooltip bubble shared by the kit widgets. It
// anchors wrapped text under (or above, when clamped) an anchor object - the same
// behaviour as help.go's education popup, factored out so every compact control can offer
// a tooltip without re-implementing the popup lifecycle.
type kitTooltip struct {
	text string
	pop  *widget.PopUp
}

const kitTipCols = 42 // max chars/line in a kit tooltip bubble

// show displays the tooltip anchored to obj. No-op when the text is empty, already shown,
// or there is no window yet.
func (t *kitTooltip) show(obj fyne.CanvasObject) {
	if t.text == "" || (t.pop != nil && t.pop.Visible()) {
		return
	}
	win := currentWindow()
	if win == nil {
		return
	}
	lbl := widget.NewLabel(wrapHelp(t.text, kitTipCols))
	t.pop = widget.NewPopUp(lbl, win.Canvas())
	sz := t.pop.MinSize()
	cs := win.Canvas().Size()
	pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(obj)
	x := pos.X
	if x+sz.Width > cs.Width-4 {
		x = cs.Width - sz.Width - 4
	}
	if x < 4 {
		x = 4
	}
	y := pos.Y + obj.Size().Height + 2
	if y+sz.Height > cs.Height-4 { // no room below → flip above the anchor
		y = pos.Y - sz.Height - 2
	}
	t.pop.ShowAtPosition(fyne.NewPos(x, y))
}

// hide dismisses the tooltip if shown.
func (t *kitTooltip) hide() {
	if t.pop != nil {
		t.pop.Hide()
		t.pop = nil
	}
}
