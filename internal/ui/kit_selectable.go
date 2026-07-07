package ui

// Selectable/copyable text (kit). Two tools, picked by whether the text mutates:
//
//   - kitSelectable / kitSelectableLabel: Fyne ≥2.6 Label.Selectable - true mouse
//     drag-select (I-beam), double-click word / triple-click line, Ctrl+C and
//     right-click → Copy, rendered exactly like a plain Label (no Entry chrome).
//     STATIC TEXT ONLY: Fyne 2.7.4's selectable keeps row/col selection state
//     across SetText, and SelectedText() slices the new string with the old
//     bounds - shrinking text under a live selection panics
//     (slice bounds out of range). Safe for labels whose text is fixed at build
//     time (paths, names, one-shot values) or whose whole widget is rebuilt on
//     refresh; never SetText a selectable label.
//
//   - kitCopyable: invisible right-click → "Copy …" context menu around any
//     content (Labels that tick, canvas.Text LCDs, whole readout panels). No
//     selection state → mutable-safe; the supplier is read at copy time, so it
//     can return the full untruncated value (full path, full node id) even when
//     the display is shortened.
//
// Not for virtualized list rows: the selection overlay is Tappable and would
// swallow row taps (List.OnSelected); the Logs tab keeps its Select/Copy mode.

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

// kitSelectable marks l drag-selectable + copyable and returns it. Static text only (see above).
func kitSelectable(l *widget.Label) *widget.Label {
	l.Selectable = true
	return l
}

// kitSelectableLabel is a plain selectable label. Static text only.
func kitSelectableLabel(text string) *widget.Label { return kitSelectable(widget.NewLabel(text)) }

// kitCopyable wraps content with a right-click → "Copy <what>" menu that copies text().
// Invisible until used; safe on live-updating readouts.
type kitCopyable struct {
	widget.BaseWidget
	content fyne.CanvasObject
	what    string // menu verb object: "Copy <what>"
	text    func() string
}

var _ fyne.SecondaryTappable = (*kitCopyable)(nil)

// newKitCopyable wraps content; text() supplies the copy payload at click time.
func newKitCopyable(what string, content fyne.CanvasObject, text func() string) *kitCopyable {
	c := &kitCopyable{content: content, what: what, text: text}
	c.ExtendBaseWidget(c)
	return c
}

// newKitCopyableLabel wraps a live-updating label, copying its current text.
func newKitCopyableLabel(what string, l *widget.Label) *kitCopyable {
	return newKitCopyable(what, l, func() string { return l.Text })
}

// SetCopyText swaps the copy supplier (e.g. full node id behind a truncated display).
func (c *kitCopyable) SetCopyText(text func() string) { c.text = text }

func (c *kitCopyable) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(c.content)
}

// copyToClipboard puts the supplier text on the clipboard ("" no-ops).
func (c *kitCopyable) copyToClipboard() {
	if c.text == nil {
		return
	}
	if t := c.text(); t != "" {
		fyne.CurrentApp().Clipboard().SetContent(t)
	}
}

// TappedSecondary shows the Copy menu at the cursor.
func (c *kitCopyable) TappedSecondary(ev *fyne.PointEvent) {
	cv := fyne.CurrentApp().Driver().CanvasForObject(c)
	if cv == nil {
		return
	}
	m := fyne.NewMenu("", fyne.NewMenuItem("Copy "+c.what, c.copyToClipboard))
	widget.ShowPopUpMenuAtPosition(m, cv, ev.AbsolutePosition)
}
