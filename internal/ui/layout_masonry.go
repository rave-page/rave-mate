package ui

import "fyne.io/fyne/v2"

// masonryLayout arranges children into a responsive number of equal-width columns, placing
// each next child in the currently-shortest column (balanced, variable-height - no row-locked
// gaps like GridWithColumns). Column count scales with width (minColWidth..maxCols), so a
// fullscreen window uses the horizontal space instead of one tall column. The last laid-out
// width is cached so MinSize - which Fyne calls without a width - can report the height for
// the current column count (a Refresh cycle converges on first show).
type masonryLayout struct {
	minColWidth float32
	gap         float32
	maxCols     int
	lastWidth   float32
}

func newMasonry() *masonryLayout { return &masonryLayout{minColWidth: 420, gap: 12, maxCols: 3} }

func (m *masonryLayout) columns(w float32) int {
	if w <= 0 {
		w = m.lastWidth
	}
	n := 1
	if m.minColWidth > 0 && w > 0 {
		n = int((w + m.gap) / (m.minColWidth + m.gap))
	}
	if n < 1 {
		n = 1
	}
	if m.maxCols > 0 && n > m.maxCols {
		n = m.maxCols
	}
	return n
}

// arrange places (or just measures) the children; returns the occupied size.
func (m *masonryLayout) arrange(objs []fyne.CanvasObject, width float32, place bool) fyne.Size {
	cols := m.columns(width)
	colW := (width - float32(cols-1)*m.gap) / float32(cols)
	if colW <= 0 {
		colW = m.minColWidth
	}
	heights := make([]float32, cols)
	for _, o := range objs {
		if o == nil || !o.Visible() {
			continue
		}
		shortest := 0
		for i := 1; i < cols; i++ {
			if heights[i] < heights[shortest] {
				shortest = i
			}
		}
		h := o.MinSize().Height
		if place {
			o.Resize(fyne.NewSize(colW, h))
			o.Move(fyne.NewPos(float32(shortest)*(colW+m.gap), heights[shortest]))
		}
		heights[shortest] += h + m.gap
	}
	var maxH float32
	for _, h := range heights {
		if h > maxH {
			maxH = h
		}
	}
	return fyne.NewSize(width, maxH)
}

func (m *masonryLayout) Layout(objs []fyne.CanvasObject, size fyne.Size) {
	m.lastWidth = size.Width
	m.arrange(objs, size.Width, true)
}

func (m *masonryLayout) MinSize(objs []fyne.CanvasObject) fyne.Size {
	// Height for the current/last width's column count, so the enclosing scroll knows how tall
	// the content is.
	w := m.lastWidth
	if w <= 0 {
		w = m.minColWidth
	}
	h := m.arrange(objs, w, false).Height

	// Report a SHRINKABLE min WIDTH = the widest single child (one column), NOT lastWidth.
	// Returning lastWidth here pinned the window to its widest-ever layout, so it could never be
	// resized narrow to reflow. Bounding by the widest child keeps any one card from clipping -
	// which is why the card descriptions must word-wrap (small child min width → narrow window).
	var maxChildW float32
	for _, o := range objs {
		if o == nil || !o.Visible() {
			continue
		}
		if cw := o.MinSize().Width; cw > maxChildW {
			maxChildW = cw
		}
	}
	if maxChildW <= 0 {
		maxChildW = m.minColWidth
	}
	return fyne.NewSize(maxChildW, h)
}
