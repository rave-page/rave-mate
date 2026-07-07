package ui

import (
	"image"
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
)

// veCanvas is the visual-editor preview surface: a checkerboard behind the composited
// document image, with a selection outline. It reports layer-move drags, zoom scrolls and
// taps (for hit-test selection) back to the editor via callbacks. Sized to doc×zoom and
// hosted inside a Scroll for panning.
type veCanvas struct {
	widget.BaseWidget
	scaled fyne.Size // doc size × zoom

	img     *canvas.Image
	selRect *canvas.Rectangle

	onDrag    func(dx, dy float32) // drag delta in widget (scaled) px
	onDragEnd func()
	onTap     func(pos fyne.Position) // tap in widget (scaled) px
	onScroll  func(dy float32)
}

func newVECanvas() *veCanvas {
	c := &veCanvas{scaled: fyne.NewSize(320, 240)}
	c.img = canvas.NewImageFromImage(image.NewNRGBA(image.Rect(0, 0, 1, 1)))
	c.img.FillMode = canvas.ImageFillStretch
	c.img.ScaleMode = canvas.ImageScaleFastest
	c.selRect = canvas.NewRectangle(color.Transparent)
	c.selRect.StrokeColor = colBrandBase
	c.selRect.StrokeWidth = 2
	c.selRect.Hidden = true
	c.ExtendBaseWidget(c)
	return c
}

// setImage swaps the composited image and target scaled size.
func (c *veCanvas) setImage(img image.Image, scaled fyne.Size) {
	c.img.Image = img
	c.scaled = scaled
	c.img.Refresh()
	c.Refresh()
}

// setSelection positions the selection outline (widget/scaled coords); hide with w<=0.
func (c *veCanvas) setSelection(x, y, w, h float32) {
	if w <= 0 || h <= 0 {
		c.selRect.Hidden = true
	} else {
		c.selRect.Hidden = false
		c.selRect.Move(fyne.NewPos(x, y))
		c.selRect.Resize(fyne.NewSize(w, h))
	}
	c.selRect.Refresh()
}

func (c *veCanvas) MinSize() fyne.Size { return c.scaled }

func (c *veCanvas) CreateRenderer() fyne.WidgetRenderer {
	checker := canvas.NewRasterWithPixels(checkerPixel)
	return &veCanvasRenderer{c: c, checker: checker,
		objects: []fyne.CanvasObject{checker, c.img, c.selRect}}
}

// ── interaction ───────────────────────────────────────────────────────────────

func (c *veCanvas) Dragged(ev *fyne.DragEvent) {
	if c.onDrag != nil {
		c.onDrag(ev.Dragged.DX, ev.Dragged.DY)
	}
}

func (c *veCanvas) DragEnd() {
	if c.onDragEnd != nil {
		c.onDragEnd()
	}
}

func (c *veCanvas) Tapped(ev *fyne.PointEvent) {
	if c.onTap != nil {
		c.onTap(ev.Position)
	}
}

func (c *veCanvas) Scrolled(ev *fyne.ScrollEvent) {
	if c.onScroll != nil {
		c.onScroll(ev.Scrolled.DY)
	}
}

var (
	_ fyne.Draggable  = (*veCanvas)(nil)
	_ fyne.Tappable   = (*veCanvas)(nil)
	_ fyne.Scrollable = (*veCanvas)(nil)
)

// checkerPixel paints an 8px checkerboard so transparency reads clearly.
func checkerPixel(x, y, _, _ int) color.Color {
	if ((x/8)+(y/8))%2 == 0 {
		return color.NRGBA{R: 0x2a, G: 0x2d, B: 0x35, A: 0xff}
	}
	return color.NRGBA{R: 0x20, G: 0x23, B: 0x2a, A: 0xff}
}

type veCanvasRenderer struct {
	c       *veCanvas
	checker *canvas.Raster
	objects []fyne.CanvasObject
}

func (r *veCanvasRenderer) Layout(_ fyne.Size) {
	s := r.c.scaled
	r.checker.Resize(s)
	r.checker.Move(fyne.NewPos(0, 0))
	r.c.img.Resize(s)
	r.c.img.Move(fyne.NewPos(0, 0))
}

func (r *veCanvasRenderer) MinSize() fyne.Size           { return r.c.scaled }
func (r *veCanvasRenderer) Objects() []fyne.CanvasObject { return r.objects }
func (r *veCanvasRenderer) Refresh()                     { r.checker.Refresh(); canvas.Refresh(r.c) }
func (r *veCanvasRenderer) Destroy()                     {}
