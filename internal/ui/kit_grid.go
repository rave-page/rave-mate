package ui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// kitGridItem is one card in a kitDensityGrid.
type kitGridItem struct {
	ID        string        // stable id echoed back to OnActivate / OnAction
	Title     string        // primary line (truncated)
	Subtitle  string        // secondary muted line (truncated)
	Icon      fyne.Resource // fallback glyph when there is no thumbnail
	ThumbPath string        // optional image file rendered as the card thumbnail
	Selected  bool          // draws a brand selection tint
}

// kitGridAction is a hover-overlay action icon on a card.
type kitGridAction struct {
	ID   string // echoed to OnAction
	Icon fyne.Resource
	Tip  string
}

// gridColumns returns how many cellW-wide cells fit in width, given inter-cell padding (>=1).
func gridColumns(width, cellW, pad float32) int {
	if cellW <= 0 {
		return 1
	}
	n := int((width + pad) / (cellW + pad))
	if n < 1 {
		n = 1
	}
	return n
}

// gridRows returns the row count for n items across cols columns.
func gridRows(n, cols int) int {
	if cols < 1 {
		cols = 1
	}
	if n <= 0 {
		return 0
	}
	return (n + cols - 1) / cols
}

// kitDensityGrid is a virtualized card grid (built on the virtualized widget.GridWrap) with
// per-card hover-overlay action icons and selection highlighting - a dense media browser à la
// Resolume / VRCX. Cells are a fixed cellW×cellH so GridWrap can recycle them. Wire OnActivate
// (card body click → navigate/open) and OnAction (hover-overlay icon). Reusable by any
// thumbnail/card surface.
type kitDensityGrid struct {
	widget.BaseWidget
	cellW, cellH float32
	items        []kitGridItem
	actions      []kitGridAction
	grid         *widget.GridWrap
	OnActivate   func(id string)
	OnAction     func(id, action string)
	OnSecondary  func(id string, ev *fyne.PointEvent) // right-click on a card (context menu)
}

// newKitDensityGrid builds a grid with the given fixed cell size.
func newKitDensityGrid(cellW, cellH float32) *kitDensityGrid {
	g := &kitDensityGrid{cellW: cellW, cellH: cellH}
	g.grid = widget.NewGridWrap(
		func() int { return len(g.items) },
		func() fyne.CanvasObject { return newKitGridCell(g) },
		func(id widget.GridWrapItemID, o fyne.CanvasObject) {
			if id < 0 || int(id) >= len(g.items) {
				return
			}
			o.(*kitGridCell).bind(g.items[id])
		},
	)
	g.ExtendBaseWidget(g)
	return g
}

// SetActions sets the hover-overlay action icons (call before/after SetItems; cells rebuild).
func (g *kitDensityGrid) SetActions(actions ...kitGridAction) { g.actions = actions }

// SetItems replaces the grid contents and repaints from the top.
func (g *kitDensityGrid) SetItems(items []kitGridItem) {
	g.items = items
	g.grid.Refresh()
	if g.grid.Length() > 0 {
		g.grid.ScrollToTop()
	}
}

// MinSize reports a couple of rows tall so the grid always has usable height in a border.
func (g *kitDensityGrid) MinSize() fyne.Size {
	cols := gridColumns(g.cellW*3, g.cellW, theme.Padding())
	rows := gridRows(len(g.items), cols)
	if rows > 2 {
		rows = 2
	}
	if rows < 1 {
		rows = 1
	}
	h := float32(rows)*(g.cellH+theme.Padding()) + theme.Padding()
	return fyne.NewSize(g.cellW+theme.Padding()*2, h)
}

func (g *kitDensityGrid) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(g.grid)
}

// ── card cell ────────────────────────────────────────────────────────────────

// kitGridCell is one recyclable card: thumbnail (image or glyph) + title/subtitle, a
// selection/hover background, and a top-right overlay of action icons shown on hover.
type kitGridCell struct {
	widget.BaseWidget
	g        *kitDensityGrid
	id       string
	selected bool
	hovered  bool

	bg      *canvas.Rectangle
	img     *canvas.Image
	icon    *widget.Icon
	thumb   *fyne.Container // stack(icon, img) - one shown
	title   *widget.Label
	sub     *widget.Label
	overlay *fyne.Container
	root    *fyne.Container
}

func newKitGridCell(g *kitDensityGrid) *kitGridCell {
	c := &kitGridCell{g: g}
	c.bg = canvas.NewRectangle(colSurface)
	c.bg.CornerRadius = theme.InputRadiusSize()
	c.bg.StrokeColor = colBorder
	c.bg.StrokeWidth = 1
	c.img = canvas.NewImageFromResource(nil)
	c.img.FillMode = canvas.ImageFillContain
	c.img.Hide()
	c.icon = widget.NewIcon(theme.FileIcon())
	c.thumb = container.NewStack(container.NewCenter(c.icon), c.img)
	c.title = widget.NewLabel("")
	c.title.Truncation = fyne.TextTruncateEllipsis
	c.title.TextStyle = fyne.TextStyle{Bold: true}
	c.sub = widget.NewLabel("")
	c.sub.Truncation = fyne.TextTruncateEllipsis
	c.sub.Importance = widget.LowImportance

	// overlay: top-right cluster of action icons, hidden until hover
	btns := make([]fyne.CanvasObject, 0, len(g.actions))
	for _, a := range g.actions {
		aID := a.ID
		btns = append(btns, newKitIconButton(a.Icon, a.Tip, func() {
			if g.OnAction != nil {
				g.OnAction(c.id, aID)
			}
		}))
	}
	c.overlay = container.NewVBox(container.NewHBox(layout.NewSpacer(), container.NewHBox(btns...)))
	c.overlay.Hide()

	content := container.NewBorder(nil, container.NewVBox(c.title, c.sub), nil, nil, c.thumb)
	c.root = container.NewStack(c.bg, content, c.overlay)
	c.ExtendBaseWidget(c)
	return c
}

func (c *kitGridCell) bind(it kitGridItem) {
	c.id = it.ID
	c.selected = it.Selected
	c.title.SetText(it.Title)
	c.sub.SetText(it.Subtitle)
	if it.ThumbPath != "" {
		c.img.File = it.ThumbPath
		c.img.Resource = nil
		c.img.Show()
		c.icon.Hide()
		c.img.Refresh()
	} else {
		c.img.Hide()
		c.icon.Show()
		if it.Icon != nil {
			c.icon.SetResource(it.Icon)
		}
	}
	c.applyBg()
}

func (c *kitGridCell) applyBg() {
	switch {
	case c.selected:
		c.bg.FillColor = withAlpha(colBrandBase, 0x40)
		c.bg.StrokeColor = colBrandBase
	case c.hovered:
		c.bg.FillColor = withAlpha(colForeground, 0x12)
		c.bg.StrokeColor = colBorder
	default:
		c.bg.FillColor = colSurface
		c.bg.StrokeColor = colBorder
	}
	c.bg.Refresh()
}

func (c *kitGridCell) MinSize() fyne.Size { return fyne.NewSize(c.g.cellW, c.g.cellH) }

func (c *kitGridCell) Tapped(*fyne.PointEvent) {
	if c.g.OnActivate != nil {
		c.g.OnActivate(c.id)
	}
}

// TappedSecondary forwards right-clicks to the grid's context-menu hook.
func (c *kitGridCell) TappedSecondary(ev *fyne.PointEvent) {
	if c.g.OnSecondary != nil {
		c.g.OnSecondary(c.id, ev)
	}
}

func (c *kitGridCell) MouseIn(*desktop.MouseEvent) {
	c.hovered = true
	c.applyBg()
	if len(c.g.actions) > 0 {
		c.overlay.Show()
	}
}
func (c *kitGridCell) MouseMoved(*desktop.MouseEvent) {}
func (c *kitGridCell) MouseOut() {
	c.hovered = false
	c.applyBg()
	c.overlay.Hide()
}

func (c *kitGridCell) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (c *kitGridCell) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(c.root)
}
