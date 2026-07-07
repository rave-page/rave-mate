package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// kitStatusStrip is the slim bottom status bar (IDE-style): a left zone (counts/context), a
// center zone (hints), and a right zone (state) plus an optional inline progress readout for
// the active background job. Surface-tinted, caption-sized. Update zones from the UI thread.
type kitStatusStrip struct {
	left   *widget.Label
	center *widget.Label
	right  *widget.Label
	rightC *kitCopyable // right zone's copy wrapper (SetRightCopyText overrides supplier)
	prog   *widget.ProgressBar
	progW  fyne.CanvasObject
	root   fyne.CanvasObject
}

// kitStatusProgW is the fixed width of the inline progress bar in the status strip.
const kitStatusProgW float32 = 140

// newKitStatusStrip builds an empty status strip. Every zone offers right-click → Copy of
// its current text (status values are the things users paste into bug reports).
func newKitStatusStrip() *kitStatusStrip {
	s := &kitStatusStrip{
		left:   mutedInline(""),
		center: mutedInline(""),
		right:  mutedInline(""),
		prog:   widget.NewProgressBar(),
	}
	s.rightC = newKitCopyableLabel("value", s.right)
	// Fixed width, native min height - forcing 12px clipped the bar (MinSize 24) on every tab.
	s.progW = container.NewGridWrap(fyne.NewSize(kitStatusProgW, s.prog.MinSize().Height), s.prog)
	s.progW.Hide()
	bg := canvas.NewRectangle(colSurface)
	inner := container.NewBorder(nil, nil,
		newKitCopyableLabel("value", s.left),
		container.NewHBox(s.progW, s.rightC),
		container.NewCenter(newKitCopyableLabel("value", s.center)))
	s.root = container.NewStack(bg, container.NewPadded(inner))
	return s
}

// SetRightCopyText overrides what the right zone's right-click Copy yields (e.g. the full
// node id behind a truncated display); what renames the menu item to "Copy <what>".
func (s *kitStatusStrip) SetRightCopyText(what string, text func() string) {
	s.rightC.what = what
	s.rightC.SetCopyText(text)
}

// Object returns the strip's canvas object.
func (s *kitStatusStrip) Object() fyne.CanvasObject { return s.root }

// SetLeft sets the left (counts/context) zone.
func (s *kitStatusStrip) SetLeft(text string) { s.left.SetText(text) }

// SetCenter sets the center (hint) zone.
func (s *kitStatusStrip) SetCenter(text string) { s.center.SetText(text) }

// SetRight sets the right (state) zone.
func (s *kitStatusStrip) SetRight(text string) { s.right.SetText(text) }

// SetProgress shows the inline job readout: a label in the right zone + a bar at frac (0..1).
func (s *kitStatusStrip) SetProgress(label string, frac float64) {
	s.right.SetText(label)
	s.prog.SetValue(frac)
	s.progW.Show()
}

// ClearProgress hides the inline job readout.
func (s *kitStatusStrip) ClearProgress() {
	s.progW.Hide()
	s.right.SetText("")
}
