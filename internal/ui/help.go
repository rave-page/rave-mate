package ui

// Education-first help primitive: a subtle "?" glyph next to any label/card title.
// Hover or tap opens an anchored rich tooltip (widget.PopUp) with a short wrapped
// explanation. Use helpIcon / labelWithHelp / cardWithHelp everywhere a newcomer
// could ask "what is this?".

import (
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

const helpWrapCols = 46 // max chars/line in the tooltip body

// helpIcon returns a small themed "?" that opens explain in a popup on hover/tap.
func helpIcon(explain string) fyne.CanvasObject {
	h := &helpIconWidget{explain: explain}
	h.ExtendBaseWidget(h)
	// Center keeps the glyph at MinSize inside stretching HBox/Border rows
	// (an unwrapped widget would be stretched to row height → ellipse ring).
	return container.NewCenter(h)
}

// labelWithHelp is a plain label with a trailing help "?".
func labelWithHelp(text, explain string) fyne.CanvasObject {
	return container.NewHBox(widget.NewLabel(text), helpIcon(explain))
}

// cardWithHelp is featureCard's help-enabled sibling: bold title + "?" popup,
// optional muted subtitle, body.
func cardWithHelp(title, subtitle, explain string, content ...fyne.CanvasObject) *widget.Card {
	titleLbl := widget.NewLabelWithStyle(title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	head := container.NewHBox(titleLbl)
	if explain != "" {
		head.Add(helpIcon(explain))
	}
	items := []fyne.CanvasObject{head}
	if subtitle != "" {
		items = append(items, mutedLabel(subtitle))
	}
	items = append(items, content...)
	return widget.NewCard("", "", container.NewVBox(items...))
}

// helpIconWidget renders the ring+"?" glyph and owns its popup lifecycle.
type helpIconWidget struct {
	widget.BaseWidget
	explain string
	pop     *widget.PopUp
}

var (
	_ fyne.Tappable     = (*helpIconWidget)(nil)
	_ desktop.Hoverable = (*helpIconWidget)(nil)
)

func (h *helpIconWidget) CreateRenderer() fyne.WidgetRenderer {
	txt := canvas.NewText("?", withAlpha(colMuted, 0xdd))
	txt.TextSize = 9
	txt.TextStyle = fyne.TextStyle{Bold: true}
	ring := canvas.NewCircle(color.Transparent)
	ring.StrokeColor = withAlpha(colMuted, 0x88)
	ring.StrokeWidth = 1
	return widget.NewSimpleRenderer(container.NewStack(ring, container.NewCenter(txt)))
}

func (h *helpIconWidget) MinSize() fyne.Size { return fyne.NewSize(15, 15) }

func (h *helpIconWidget) Tapped(*fyne.PointEvent) {
	if h.pop != nil && h.pop.Visible() { // second tap (or after outside-tap dismiss) closes
		h.hidePop()
		return
	}
	h.pop = nil
	h.showPop()
}

func (h *helpIconWidget) MouseIn(*desktop.MouseEvent)    { h.showPop() }
func (h *helpIconWidget) MouseMoved(*desktop.MouseEvent) {}
func (h *helpIconWidget) MouseOut()                      { h.hidePop() }

// showPop anchors the tooltip under the icon, clamped to the canvas (flips above
// when there is no room below) - stays readable on narrow windows.
func (h *helpIconWidget) showPop() {
	if h.pop != nil && h.pop.Visible() {
		return
	}
	win := currentWindow()
	if win == nil {
		return
	}
	body := widget.NewLabel(wrapHelp(h.explain, helpWrapCols))
	h.pop = widget.NewPopUp(body, win.Canvas())
	sz := h.pop.MinSize()
	cs := win.Canvas().Size()
	pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(h)
	x := pos.X
	if x+sz.Width > cs.Width-4 {
		x = cs.Width - sz.Width - 4
	}
	if x < 4 {
		x = 4
	}
	y := pos.Y + h.Size().Height + 2
	if y+sz.Height > cs.Height-4 {
		y = pos.Y - sz.Height - 2
	}
	h.pop.ShowAtPosition(fyne.NewPos(x, y))
}

func (h *helpIconWidget) hidePop() {
	if h.pop != nil {
		h.pop.Hide()
		h.pop = nil
	}
}

// wrapHelp word-wraps s to at most cols chars/line, BALANCED: greedy wrap fixes the
// line count, then the width is tightened to the smallest that still fits that count -
// even line lengths, no orphaned trailing word/chars (the recurring "2 chars on their
// own line" eyesore). Existing newlines = paragraph breaks.
func wrapHelp(s string, cols int) string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		lines := greedyWrap(words, cols)
		// Tighten: the narrowest width producing the SAME line count balances the block.
		for w := cols - 1; w > cols/2; w-- {
			if cand := greedyWrap(words, w); len(cand) == len(lines) {
				lines = cand
			} else {
				break
			}
		}
		out = append(out, lines...)
	}
	return strings.Join(out, "\n")
}

// greedyWrap breaks words into lines of at most cols chars (an overlong word gets its
// own line - never split mid-word).
func greedyWrap(words []string, cols int) []string {
	var lines []string
	line := words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) > cols {
			lines = append(lines, line)
			line = w
			continue
		}
		line += " " + w
	}
	return append(lines, line)
}
