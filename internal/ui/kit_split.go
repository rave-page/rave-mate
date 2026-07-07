package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
)

// kitHSplit wraps container.NewHSplit with a preset divider offset (0..1, leading share).
// Thin draggable handle; both panes scroll internally. Use when both panes should stay
// visible and the user may rebalance them; when a pane must COLLAPSE to a single column on a
// narrow window, use newAdaptiveSplit (layout_adaptive.go) instead.
func kitHSplit(leading, trailing fyne.CanvasObject, offset float64) *container.Split {
	s := container.NewHSplit(leading, trailing)
	s.SetOffset(offset)
	return s
}

// kitVSplit is kitHSplit's vertical sibling (top/bottom panes).
func kitVSplit(top, bottom fyne.CanvasObject, offset float64) *container.Split {
	s := container.NewVSplit(top, bottom)
	s.SetOffset(offset)
	return s
}
