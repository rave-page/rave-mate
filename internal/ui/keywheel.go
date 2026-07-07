package ui

import (
	"image"
	"image/color"
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/musiclib"
)

// keyWheel is the circular Camelot wheel: a per-pixel "fragment shader"
// (canvas.Raster - Fyne exposes no GLSL hook, so the shader runs in software,
// same authoring model: f(x,y) → color) with Fyne UI composited on top
// (segment labels, live center readout, tap + hover).
//
// Geometry: 12 clock segments (12 at the top), outer ring = B (major),
// inner ring = A (minor). Colors follow the harmonic relation to Ref:
// mint=same · violet=relative · hot=+1 · amber=−1 · muted=dissonant.
// Selected segments render saturated with a radial glow; segments with
// tracks are tinted; empty ones stay dark and ignore taps.
type keyWheel struct {
	widget.BaseWidget

	ref      *musiclib.Key        // harmonic reference (selected track); nil = neutral brand coloring
	selected map[string]bool      // keyLabel-keyed filter set; nil = pick mode (no toggle state)
	present  map[musiclib.Key]int // tracks per key; nil = treat all keys as present
	onPick   func(musiclib.Key)   // tap callback (after toggle in filter mode)

	hover  *musiclib.Key // segment under the cursor
	raster *canvas.Raster
	labels [24]*canvas.Text // segment captions, polar-positioned by the renderer
	center *canvas.Text     // hub readout: hovered key (+track count) or the reference
	size   float32          // requested diameter (dip)
}

// Ring radii in units of the wheel radius; angular gap between segments (rad at r=1).
const (
	whOuterHi = 0.995
	whOuterLo = 0.78
	whInnerHi = 0.755
	whInnerLo = 0.54
	whHub     = 0.50
	whGapRad  = 0.022
	whSeg     = math.Pi / 6 // 30°
)

func newKeyWheel(size float32, ref *musiclib.Key, selected map[string]bool, present map[musiclib.Key]int, onPick func(musiclib.Key)) *keyWheel {
	w := &keyWheel{ref: ref, selected: selected, present: present, onPick: onPick, size: size}
	w.raster = canvas.NewRaster(w.shade)
	for i := range w.labels {
		t := canvas.NewText(wheelKeyAt(i).Camelot(), colForeground)
		t.TextSize = theme.CaptionTextSize()
		t.TextStyle = fyne.TextStyle{Bold: true}
		w.labels[i] = t
	}
	w.center = canvas.NewText("", colMuted)
	w.center.TextSize = theme.CaptionTextSize()
	w.center.Alignment = fyne.TextAlignCenter
	w.refreshCenter()
	w.ExtendBaseWidget(w)
	return w
}

// wheelKeyAt maps label index 0-23 → key (0-11 outer/major ring, 12-23 inner/minor).
func wheelKeyAt(i int) musiclib.Key {
	num := i%12 + 1
	return musiclib.Key{Num: num, Minor: i >= 12}
}

func (w *keyWheel) MinSize() fyne.Size { return fyne.NewSize(w.size, w.size) }

// keyAt polar-hit-tests a widget-local point; ok=false in gaps/hub/outside.
func (w *keyWheel) keyAt(p fyne.Position) (musiclib.Key, bool) {
	s := w.Size()
	radius := float64(fyne.Min(s.Width, s.Height)) / 2
	if radius <= 0 {
		return musiclib.Key{}, false
	}
	dx := float64(p.X) - float64(s.Width)/2
	dy := float64(p.Y) - float64(s.Height)/2
	r := math.Hypot(dx, dy) / radius
	var minor bool
	switch {
	case r >= whOuterLo && r <= whOuterHi:
		minor = false
	case r >= whInnerLo && r <= whInnerHi:
		minor = true
	default:
		return musiclib.Key{}, false
	}
	theta := math.Atan2(dx, -dy) // clockwise from 12 o'clock
	if theta < 0 {
		theta += 2 * math.Pi
	}
	idx := int(math.Round(theta/whSeg)) % 12
	num := idx
	if num == 0 {
		num = 12
	}
	return musiclib.Key{Num: num, Minor: minor}, true
}

func (w *keyWheel) isPresent(k musiclib.Key) bool {
	return w.present == nil || w.present[k] > 0
}

func (w *keyWheel) Tapped(e *fyne.PointEvent) {
	k, ok := w.keyAt(e.Position)
	if !ok || !w.isPresent(k) {
		return
	}
	if w.selected != nil {
		w.selected[keyLabel(k)] = !w.selected[keyLabel(k)]
		w.Refresh()
	}
	if w.onPick != nil {
		w.onPick(k)
	}
}

func (w *keyWheel) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (w *keyWheel) MouseIn(e *desktop.MouseEvent) { w.MouseMoved(e) }

func (w *keyWheel) MouseMoved(e *desktop.MouseEvent) {
	k, ok := w.keyAt(e.Position)
	switch {
	case !ok:
		if w.hover != nil {
			w.hover = nil
			w.refreshCenter()
			w.Refresh()
		}
	case w.hover == nil || *w.hover != k:
		w.hover = &k
		w.refreshCenter()
		w.Refresh()
	}
}

func (w *keyWheel) MouseOut() {
	if w.hover != nil {
		w.hover = nil
		w.refreshCenter()
		w.Refresh()
	}
}

// refreshCenter updates the hub readout: hovered key + its track count, else the ref.
func (w *keyWheel) refreshCenter() {
	switch {
	case w.hover != nil:
		txt := keyLabel(*w.hover)
		if w.present != nil {
			txt += " · " + itoa(w.present[*w.hover])
		}
		w.center.Text = txt
		w.center.Color = colForeground
	case w.ref != nil:
		w.center.Text = keyLabel(*w.ref)
		w.center.Color = colMuted
	default:
		w.center.Text = ""
	}
	w.center.Refresh()
}

// ── the "fragment shader" ─────────────────────────────────────────────────────

// segColor resolves a segment's flat color (before glow/AA). present dim, tint,
// saturated-selected and hover-lightened states mirror keyCellColors.
// isSaturated reports whether k renders at full color: in the filter (filter
// mode), or harmonic vs the ref (pick mode - there's no filter state to show).
func (w *keyWheel) isSaturated(k musiclib.Key) bool {
	if w.selected != nil {
		return w.selected[keyLabel(k)]
	}
	return w.ref != nil && w.isPresent(k) && musiclib.KeyRelation(*w.ref, k) != musiclib.RelNone
}

func (w *keyWheel) segColor(k musiclib.Key) color.NRGBA {
	sel := w.isSaturated(k)
	bg, _ := keyCellColors(k, w.ref, sel, w.isPresent(k))
	c := toNRGBA(bg)
	if w.hover != nil && *w.hover == k && w.isPresent(k) {
		c = blend(c, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x2e}) // hover lighten
	}
	return c
}

// shade evaluates one frame: for every device pixel, polar-map to (ring, segment),
// pick the segment color, apply a radial glow on selected segments and anti-alias
// ring edges + angular gaps against the surface color.
func (w *keyWheel) shade(px, py int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, px, py))
	bgc := toNRGBA(colSurface)
	if px == 0 || py == 0 {
		return img
	}
	radius := float64(minI(px, py)) / 2
	cx, cy := float64(px)/2, float64(py)/2
	aa := 1.4 / radius // edge feather in r-units (~1.4 device px)

	// flat colors per segment, precomputed (24 lookups instead of per-pixel maps)
	var cols [24]color.NRGBA
	var selFlag [24]bool
	for i := range cols {
		k := wheelKeyAt(i)
		cols[i] = w.segColor(k)
		selFlag[i] = w.isSaturated(k)
	}
	hub := blend(bgc, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x0a})

	for y := range py {
		dy := float64(y) - cy
		for x := range px {
			dx := float64(x) - cx
			r := math.Hypot(dx, dy) / radius
			if r > whOuterHi+aa {
				img.SetNRGBA(x, y, bgc)
				continue
			}
			if r < whHub {
				img.SetNRGBA(x, y, hub)
				continue
			}
			// ring + coverage (feathered at the four radial edges)
			var lo, hi float64
			var minor bool
			switch {
			case r >= whOuterLo-aa:
				lo, hi, minor = whOuterLo, whOuterHi, false
			case r >= whInnerLo-aa && r <= whInnerHi+aa:
				lo, hi, minor = whInnerLo, whInnerHi, true
			default:
				img.SetNRGBA(x, y, bgc)
				continue
			}
			cov := smoothCov(r, lo, hi, aa)
			if cov <= 0 {
				img.SetNRGBA(x, y, bgc)
				continue
			}
			theta := math.Atan2(dx, -dy)
			if theta < 0 {
				theta += 2 * math.Pi
			}
			idx := int(math.Round(theta/whSeg)) % 12
			// angular distance to the segment boundary → constant-width gap + AA
			d := math.Abs(theta - float64(idx)*whSeg)
			if d > math.Pi {
				d = 2*math.Pi - d
			}
			edge := whSeg/2 - whGapRad/r // gap stays ~constant width across radii
			acov := clampF((edge-d)/(aa/r)+0.5, 0, 1)
			cov *= acov

			num := idx
			if num == 0 {
				num = 12
			}
			i := num - 1 // label/color index (0-11 major, +12 minor)
			if minor {
				i += 12
			}
			c := cols[i]
			if selFlag[i] {
				// radial glow: brightest mid-ring, eases to the edges
				mid := (lo + hi) / 2
				t := 1 - math.Abs(r-mid)/((hi-lo)/2)
				if t > 0 {
					c = blend(c, color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: uint8(36 * t)})
				}
			}
			c.A = 0xff
			if cov >= 1 {
				img.SetNRGBA(x, y, c)
			} else {
				c.A = uint8(255 * cov)
				img.SetNRGBA(x, y, blend(bgc, c))
			}
		}
	}
	return img
}

// smoothCov is the feathered coverage of r inside [lo,hi].
func smoothCov(r, lo, hi, aa float64) float64 {
	in := clampF((r-lo)/aa+0.5, 0, 1)
	out := clampF((hi-r)/aa+0.5, 0, 1)
	return in * out
}

// ── renderer: shader below, labels on top ────────────────────────────────────

func (w *keyWheel) CreateRenderer() fyne.WidgetRenderer {
	objs := make([]fyne.CanvasObject, 0, 26)
	objs = append(objs, w.raster)
	for _, t := range w.labels {
		objs = append(objs, t)
	}
	objs = append(objs, w.center)
	return &keyWheelRenderer{w: w, objs: objs}
}

type keyWheelRenderer struct {
	w    *keyWheel
	objs []fyne.CanvasObject
}

func (r *keyWheelRenderer) Layout(size fyne.Size) {
	r.w.raster.Resize(size)
	radius := float64(fyne.Min(size.Width, size.Height)) / 2
	cx, cy := float64(size.Width)/2, float64(size.Height)/2
	for i, t := range r.w.labels {
		k := wheelKeyAt(i)
		rr := radius * (whOuterLo + whOuterHi) / 2
		if k.Minor {
			rr = radius * (whInnerLo + whInnerHi) / 2
		}
		theta := float64(k.Num%12) * whSeg
		ts := fyne.MeasureText(t.Text, t.TextSize, t.TextStyle)
		x := cx + rr*math.Sin(theta) - float64(ts.Width)/2
		y := cy - rr*math.Cos(theta) - float64(ts.Height)/2
		t.Move(fyne.NewPos(float32(x), float32(y)))
		t.Resize(ts)
	}
	cs := fyne.MeasureText(r.w.center.Text, r.w.center.TextSize, r.w.center.TextStyle)
	r.w.center.Move(fyne.NewPos(float32(cx)-cs.Width/2, float32(cy)-cs.Height/2))
	r.w.center.Resize(cs)
}

func (r *keyWheelRenderer) MinSize() fyne.Size           { return r.w.MinSize() }
func (r *keyWheelRenderer) Objects() []fyne.CanvasObject { return r.objs }
func (r *keyWheelRenderer) Destroy()                     {}

func (r *keyWheelRenderer) Refresh() {
	// label legibility follows the segment state (bright on colored, muted on dark)
	for i, t := range r.w.labels {
		k := wheelKeyAt(i)
		sel := r.w.isSaturated(k)
		switch {
		case sel:
			t.Color = colBackground
		case r.w.isPresent(k):
			t.Color = colForeground
		default:
			t.Color = colMuted
		}
		t.Refresh()
	}
	r.w.raster.Refresh()
	r.Layout(r.w.Size())
}
