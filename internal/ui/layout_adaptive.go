package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
)

// adaptiveSplit lays out two panes side-by-side when the viewport is wide enough for both at
// their min widths, and stacks them vertically (leading on top) when it isn't - so a desktop
// split view collapses to a single mobile column instead of pinning the window wide. Unlike
// container.Split (MinSize width = sum of panes, undraggable below that), it reports a
// SHRINKABLE min width = the wider single pane, letting the window go narrow and reflow.
type adaptiveSplit struct {
	gap      float32
	leadFrac float32 // leading pane's width fraction when side-by-side (0..1)
}

func newAdaptiveSplit(leadFrac float32) *adaptiveSplit {
	if leadFrac <= 0 || leadFrac >= 1 {
		leadFrac = 0.5
	}
	return &adaptiveSplit{gap: 12, leadFrac: leadFrac}
}

// sideBySide reports whether both panes fit horizontally at their min widths.
func (a *adaptiveSplit) sideBySide(objs []fyne.CanvasObject, w float32) bool {
	if len(objs) < 2 {
		return true
	}
	return w >= objs[0].MinSize().Width+a.gap+objs[1].MinSize().Width
}

func (a *adaptiveSplit) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	objs = visibleObjects(objs)
	if len(objs) == 0 {
		return
	}
	if len(objs) == 1 {
		objs[0].Resize(size)
		objs[0].Move(fyne.NewPos(0, 0))
		return
	}
	l, r := objs[0], objs[1]
	if a.sideBySide(objs, size.Width) {
		lw := (size.Width - a.gap) * a.leadFrac
		if lw < l.MinSize().Width {
			lw = l.MinSize().Width
		}
		rw := size.Width - a.gap - lw
		if rw < r.MinSize().Width { // give the trailing pane its min, shrink the leading
			rw = r.MinSize().Width
			lw = size.Width - a.gap - rw
		}
		l.Resize(fyne.NewSize(lw, size.Height))
		l.Move(fyne.NewPos(0, 0))
		r.Resize(fyne.NewSize(rw, size.Height))
		r.Move(fyne.NewPos(lw+a.gap, 0))
		return
	}
	// Stacked: split height by leadFrac (mirrors the side-by-side width logic), clamped to
	// each pane's min - so the leading content pane (e.g. the file list) keeps a usable
	// height instead of collapsing to its one-row MinSize while the trailing detail pane
	// hogs the whole column.
	lh := (size.Height - a.gap) * a.leadFrac
	if lh < l.MinSize().Height {
		lh = l.MinSize().Height
	}
	rh := size.Height - a.gap - lh
	if rh < r.MinSize().Height { // give the trailing pane its min, shrink the leading
		rh = r.MinSize().Height
		lh = size.Height - a.gap - rh
	}
	if lh < 0 {
		lh = 0
	}
	l.Resize(fyne.NewSize(size.Width, lh))
	l.Move(fyne.NewPos(0, 0))
	r.Resize(fyne.NewSize(size.Width, rh))
	r.Move(fyne.NewPos(0, lh+a.gap))
}

func (a *adaptiveSplit) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var maxW, maxH float32
	for _, o := range visibleObjects(objs) {
		m := o.MinSize()
		if m.Width > maxW {
			maxW = m.Width
		}
		if m.Height > maxH {
			maxH = m.Height
		}
	}
	return fyne.NewSize(maxW, maxH)
}

func visibleObjects(objs []fyne.CanvasObject) []fyne.CanvasObject {
	out := make([]fyne.CanvasObject, 0, len(objs))
	for _, o := range objs {
		if o != nil && o.Visible() {
			out = append(out, o)
		}
	}
	return out
}

func shrinkWidth(width float32, obj fyne.CanvasObject) *fyne.Container {
	return container.New(&shrinkWidthLayout{width: width}, obj)
}

type shrinkWidthLayout struct {
	width float32
}

func (s *shrinkWidthLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	for _, o := range visibleObjects(objs) {
		o.Move(fyne.NewPos(0, 0))
		o.Resize(size)
	}
}

func (s *shrinkWidthLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	var h float32
	for _, o := range visibleObjects(objs) {
		if m := o.MinSize(); m.Height > h {
			h = m.Height
		}
	}
	return fyne.NewSize(s.width, h)
}
