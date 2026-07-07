package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
)

// kitToolStrip is a dense (~32px) horizontal toolbar on a raised surface, grouping compact
// controls (kitIconButton, kitSearchField, kitSegmented, labels) separated by thin vertical
// rules (kitToolSep). It uses RowWrapLayout, so on a window too narrow to hold one row it
// wraps to a second row instead of clipping a control off-screen - never a horizontal
// scrollbar. Replaces the old hero + wrapping button rows.
func kitToolStrip(items ...fyne.CanvasObject) fyne.CanvasObject {
	bg := canvas.NewRectangle(colSurface)
	row := container.New(layout.NewRowWrapLayout(), items...)
	return container.NewStack(bg, container.NewPadded(row))
}

// kitToolSepH is the height of a tool-strip separator rule (matches a compact control).
const kitToolSepH float32 = 20

// kitToolSep is a thin vertical rule for grouping items inside a kitToolStrip.
func kitToolSep() fyne.CanvasObject {
	r := canvas.NewRectangle(colBorder)
	return container.NewGridWrap(fyne.NewSize(1, kitToolSepH), r)
}
