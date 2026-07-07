package visualeditor

import (
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	_ "image/gif"  // decode support for image layers
	_ "image/jpeg" // decode support for image layers
	"image/png"    // decode + PNG export
	"io"
	"math"
	"os"
	"strings"
	"sync"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/math/f64"
	"golang.org/x/image/math/fixed"
)

// ImageLoader resolves an image layer's bytes to an image (path or library ref). Optional;
// nil disables image layers (they render empty).
type ImageLoader func(props ImageProps) (image.Image, bool)

// Compositor renders documents to *image.NRGBA. It caches leaf rasters keyed by a content
// signature so interactive edits that only change transform/opacity/blend/visibility don't
// re-raster (text shaping + image scaling are the costly steps). Safe for single-goroutine
// interactive use; guard with the mutex if shared.
type Compositor struct {
	fonts  *FontRegistry
	loader ImageLoader

	mu    sync.Mutex
	cache map[string]*cachedRaster // layerID → last raster
}

type cachedRaster struct {
	sig string
	img *image.NRGBA
}

// NewCompositor builds a compositor with a font registry and optional image loader.
func NewCompositor(fonts *FontRegistry, loader ImageLoader) *Compositor {
	if fonts == nil {
		fonts = NewFontRegistry()
	}
	return &Compositor{fonts: fonts, loader: loader, cache: map[string]*cachedRaster{}}
}

// Fonts exposes the registry (for UI family lists).
func (c *Compositor) Fonts() *FontRegistry { return c.fonts }

// Render composites the document into a fresh NRGBA. provider resolves {placeholders} in
// text layers (may be nil); the document's static Vars are chained after it.
func (c *Compositor) Render(d *Document, provider Provider) *image.NRGBA {
	c.mu.Lock()
	defer c.mu.Unlock()
	full := ChainProvider{provider, d.varsProvider()}
	dst := image.NewNRGBA(image.Rect(0, 0, d.W, d.H))
	if d.Root != nil {
		c.compositeChildren(dst, d.Root.Children, d, full)
	}
	return dst
}

// compositeChildren blends each visible child onto dst in order.
func (c *Compositor) compositeChildren(dst *image.NRGBA, children []*Layer, d *Document, p Provider) {
	for _, l := range children {
		if l == nil || !l.Visible || l.Opacity <= 0 {
			continue
		}
		raster := c.rasterOf(l, d, p)
		if raster == nil {
			continue
		}
		c.place(dst, raster, l, d)
	}
}

// rasterOf returns the layer's content raster (its own W×H box for leaves, doc-sized for
// groups). Leaf rasters are cached by content signature.
func (c *Compositor) rasterOf(l *Layer, d *Document, p Provider) *image.NRGBA {
	if l.IsGroup() {
		gw, gh := int(l.W), int(l.H)
		if gw <= 0 {
			gw = d.W
		}
		if gh <= 0 {
			gh = d.H
		}
		buf := image.NewNRGBA(image.Rect(0, 0, gw, gh))
		c.compositeChildren(buf, l.Children, d, p)
		return buf // groups aren't cached (cheap blend of already-cached leaves)
	}
	sig := c.contentSig(l, p)
	if cr, ok := c.cache[l.ID]; ok && cr.sig == sig {
		return cr.img
	}
	img := c.rasterLeaf(l, p)
	c.cache[l.ID] = &cachedRaster{sig: sig, img: img}
	return img
}

// contentSig hashes the content-affecting fields (not transform/opacity/blend/visibility).
func (c *Compositor) contentSig(l *Layer, p Provider) string {
	h := fnv.New64a()
	fmt.Fprintf(h, "%s|%g|%g|", l.Kind, l.W, l.H)
	switch l.Kind {
	case KindText:
		if l.Text != nil {
			t := l.Text
			fmt.Fprintf(h, "%q|%s|%g|%v|%g|%g|%s", Substitute(t.Content, p), t.FontFamily,
				t.FontSize, t.Color, t.LetterSpacing, t.LineHeight, t.Align)
		}
	case KindSolid:
		if l.Solid != nil {
			fmt.Fprintf(h, "%v", l.Solid.Color)
		}
	case KindGradient:
		if l.Gradient != nil {
			fmt.Fprintf(h, "%g|%v", l.Gradient.Angle, l.Gradient.Stops)
		}
	case KindImage:
		if l.Image != nil {
			fmt.Fprintf(h, "%s|%s|%s", l.Image.Path, l.Image.LibraryRef, l.Image.Fit)
		}
	}
	return fmt.Sprintf("%x", h.Sum64())
}

// rasterLeaf renders a leaf into a fresh W×H NRGBA.
func (c *Compositor) rasterLeaf(l *Layer, p Provider) *image.NRGBA {
	w, hh := int(math.Round(l.W)), int(math.Round(l.H))
	if w < 1 || hh < 1 {
		return image.NewNRGBA(image.Rect(0, 0, 1, 1))
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, hh))
	switch l.Kind {
	case KindSolid:
		if l.Solid != nil {
			fillNRGBA(img, l.Solid.Color.NRGBA())
		}
	case KindGradient:
		if l.Gradient != nil {
			c.rasterGradient(img, *l.Gradient)
		}
	case KindImage:
		if l.Image != nil && c.loader != nil {
			if src, ok := c.loader(*l.Image); ok && src != nil {
				drawFit(img, src, l.Image.Fit)
			}
		}
	case KindText:
		if l.Text != nil {
			c.rasterText(img, *l.Text, p)
		}
	}
	return img
}

// place transforms raster (content space) into doc space and blends onto dst.
func (c *Compositor) place(dst, raster *image.NRGBA, l *Layer, d *Document) {
	t := l.Transform
	simple := t.Rotation == 0 && t.ScaleX == 1 && t.ScaleY == 1 &&
		t.X == math.Trunc(t.X) && t.Y == math.Trunc(t.Y)
	if simple {
		blendRegion(dst, raster, int(t.X), int(t.Y), l.Opacity, l.Blend)
		return
	}

	cw, ch := float64(raster.Bounds().Dx()), float64(raster.Bounds().Dy())
	s2d := affine(t, cw, ch)
	// transformed bbox from the 4 content corners
	minX, minY, maxX, maxY := transformedBounds(s2d, cw, ch)
	x0, y0 := int(math.Floor(minX)), int(math.Floor(minY))
	x1, y1 := int(math.Ceil(maxX)), int(math.Ceil(maxY))
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > d.W {
		x1 = d.W
	}
	if y1 > d.H {
		y1 = d.H
	}
	if x1 <= x0 || y1 <= y0 {
		return
	}
	tmp := image.NewNRGBA(image.Rect(0, 0, x1-x0, y1-y0))
	// shift dst coords into tmp-local space
	local := s2d
	local[2] -= float64(x0)
	local[5] -= float64(y0)
	xdraw.BiLinear.Transform(tmp, local, raster, raster.Bounds(), xdraw.Src, nil)
	blendRegion(dst, tmp, x0, y0, l.Opacity, l.Blend)
}

// affine builds the content→doc matrix: translate(-w/2,-h/2) → scale → rotate → translate(X+w/2,Y+h/2).
func affine(t Transform, w, h float64) f64.Aff3 {
	rad := t.Rotation * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	a := t.ScaleX * cos
	b := -t.ScaleY * sin
	cc := t.ScaleX * sin
	dd := t.ScaleY * cos
	tx := -(w/2)*a - (h/2)*b + t.X + w/2
	ty := -(w/2)*cc - (h/2)*dd + t.Y + h/2
	return f64.Aff3{a, b, tx, cc, dd, ty}
}

// transformedBounds returns the doc-space bounding box of the w×h content box under m.
func transformedBounds(m f64.Aff3, w, h float64) (minX, minY, maxX, maxY float64) {
	pts := [4][2]float64{{0, 0}, {w, 0}, {0, h}, {w, h}}
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY = math.Inf(-1), math.Inf(-1)
	for _, pt := range pts {
		dx := m[0]*pt[0] + m[1]*pt[1] + m[2]
		dy := m[3]*pt[0] + m[4]*pt[1] + m[5]
		minX, maxX = math.Min(minX, dx), math.Max(maxX, dx)
		minY, maxY = math.Min(minY, dy), math.Max(maxY, dy)
	}
	return
}

// ── rasterizers ───────────────────────────────────────────────────────────────

func fillNRGBA(img *image.NRGBA, c color.NRGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

// rasterGradient fills img with a linear gradient along Angle (0=L→R, 90=T→B).
func (c *Compositor) rasterGradient(img *image.NRGBA, g GradientProps) {
	stops := g.Stops
	if len(stops) == 0 {
		return
	}
	b := img.Bounds()
	w, h := float64(b.Dx()), float64(b.Dy())
	rad := g.Angle * math.Pi / 180
	dx, dy := math.Cos(rad), math.Sin(rad)
	// projection range over the box so t spans [0,1] corner-to-corner along the axis
	var lo, hi float64 = math.Inf(1), math.Inf(-1)
	for _, cx := range []float64{0, w} {
		for _, cy := range []float64{0, h} {
			proj := cx*dx + cy*dy
			lo, hi = math.Min(lo, proj), math.Max(hi, proj)
		}
	}
	span := hi - lo
	if span == 0 {
		span = 1
	}
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			t := ((float64(x)*dx + float64(y)*dy) - lo) / span
			img.SetNRGBA(b.Min.X+x, b.Min.Y+y, gradientAt(stops, t))
		}
	}
}

// gradientAt samples the stop list at t∈[0,1] (linear interp between straddling stops).
func gradientAt(stops []GradientStop, t float64) color.NRGBA {
	if t <= stops[0].Pos {
		return stops[0].Color.NRGBA()
	}
	last := stops[len(stops)-1]
	if t >= last.Pos {
		return last.Color.NRGBA()
	}
	for i := 1; i < len(stops); i++ {
		if t <= stops[i].Pos {
			a, bb := stops[i-1], stops[i]
			span := bb.Pos - a.Pos
			f := 0.0
			if span > 0 {
				f = (t - a.Pos) / span
			}
			return lerpNRGBA(a.Color.NRGBA(), bb.Color.NRGBA(), f)
		}
	}
	return last.Color.NRGBA()
}

func lerpNRGBA(a, b color.NRGBA, f float64) color.NRGBA {
	return color.NRGBA{
		R: uint8(float64(a.R) + (float64(b.R)-float64(a.R))*f),
		G: uint8(float64(a.G) + (float64(b.G)-float64(a.G))*f),
		B: uint8(float64(a.B) + (float64(b.B)-float64(a.B))*f),
		A: uint8(float64(a.A) + (float64(b.A)-float64(a.A))*f),
	}
}

// drawFit scales src into dst's box per fit mode (bilinear).
func drawFit(dst *image.NRGBA, src image.Image, fit ImageFit) {
	db := dst.Bounds()
	sb := src.Bounds()
	dw, dh := float64(db.Dx()), float64(db.Dy())
	sw, sh := float64(sb.Dx()), float64(sb.Dy())
	if sw <= 0 || sh <= 0 {
		return
	}
	var target image.Rectangle
	switch fit {
	case FitStretch:
		target = db
	case FitContain:
		s := math.Min(dw/sw, dh/sh)
		nw, nh := sw*s, sh*s
		ox, oy := (dw-nw)/2, (dh-nh)/2
		target = image.Rect(db.Min.X+int(ox), db.Min.Y+int(oy), db.Min.X+int(ox+nw), db.Min.Y+int(oy+nh))
	default: // FitCover
		s := math.Max(dw/sw, dh/sh)
		nw, nh := sw*s, sh*s
		ox, oy := (dw-nw)/2, (dh-nh)/2
		target = image.Rect(db.Min.X+int(ox), db.Min.Y+int(oy), db.Min.X+int(ox+nw), db.Min.Y+int(oy+nh))
	}
	xdraw.BiLinear.Scale(dst, target, src, sb, xdraw.Over, nil)
}

// rasterText shapes multi-line, wrapped, aligned text into img (top-anchored).
func (c *Compositor) rasterText(img *image.NRGBA, t TextProps, p Provider) {
	content := Substitute(t.Content, p)
	if strings.TrimSpace(content) == "" {
		return
	}
	size := t.FontSize
	if size < 1 {
		size = 24
	}
	face := c.fonts.Face(t.FontFamily, size)
	if face == nil {
		return
	}
	boxW := img.Bounds().Dx()
	lines := wrapRunes(face, content, t.LetterSpacing, boxW)
	m := face.Metrics()
	ascent := float64(m.Ascent) / 64
	lh := t.LineHeight
	if lh <= 0 {
		lh = 1.2
	}
	step := size * lh
	col := image.NewUniform(t.Color.NRGBA())
	y := ascent
	for _, line := range lines {
		lw := lineWidth(face, line, t.LetterSpacing)
		var x float64
		switch t.Align {
		case AlignCenter:
			x = (float64(boxW) - lw) / 2
		case AlignRight:
			x = float64(boxW) - lw
		default:
			x = 0
		}
		drawLine(img, face, col, line, x, y, t.LetterSpacing)
		y += step
	}
}

// wrapRunes word-wraps s to fit maxW px (accounting for letter spacing). Explicit newlines
// force breaks. A single word wider than the box gets its own (overflowing) line.
func wrapRunes(face font.Face, s string, spacing float64, maxW int) []string {
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		cur := ""
		for _, w := range words {
			cand := w
			if cur != "" {
				cand = cur + " " + w
			}
			if lineWidth(face, cand, spacing) <= float64(maxW) || cur == "" {
				cur = cand
			} else {
				out = append(out, cur)
				cur = w
			}
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	return out
}

// lineWidth measures a line's advance width in px including letter spacing.
func lineWidth(face font.Face, s string, spacing float64) float64 {
	var w float64
	rs := []rune(s)
	for i, r := range rs {
		adv, ok := face.GlyphAdvance(r)
		if !ok {
			adv, _ = face.GlyphAdvance(' ')
		}
		w += float64(adv) / 64
		if i < len(rs)-1 {
			w += spacing
		}
	}
	return w
}

// drawLine draws one line rune-by-rune from baseline (x,y) with letter spacing.
func drawLine(img *image.NRGBA, face font.Face, src image.Image, s string, x, y, spacing float64) {
	d := &font.Drawer{Dst: img, Src: src, Face: face}
	for _, r := range s {
		d.Dot = fixed.Point26_6{X: fixed.Int26_6(x * 64), Y: fixed.Int26_6(y * 64)}
		d.DrawString(string(r))
		adv, ok := face.GlyphAdvance(r)
		if !ok {
			adv, _ = face.GlyphAdvance(' ')
		}
		x += float64(adv)/64 + spacing
	}
}

// ── compositing ─────────────────────────────────────────────────────────────

// blendRegion blends src (straight NRGBA) onto dst at (ox,oy) with opacity + mode
// (W3C separable compositing over premultiplied-safe straight-alpha pixels).
func blendRegion(dst, src *image.NRGBA, ox, oy int, opacity float64, mode BlendMode) {
	sb := src.Bounds()
	for y := sb.Min.Y; y < sb.Max.Y; y++ {
		dy := oy + (y - sb.Min.Y)
		if dy < dst.Bounds().Min.Y || dy >= dst.Bounds().Max.Y {
			continue
		}
		for x := sb.Min.X; x < sb.Max.X; x++ {
			dx := ox + (x - sb.Min.X)
			if dx < dst.Bounds().Min.X || dx >= dst.Bounds().Max.X {
				continue
			}
			sc := src.NRGBAAt(x, y)
			as := float64(sc.A) / 255 * opacity
			if as <= 0 {
				continue
			}
			dc := dst.NRGBAAt(dx, dy)
			ab := float64(dc.A) / 255
			ao := as + ab*(1-as)
			if ao <= 0 {
				continue
			}
			r := blendChannel(mode, float64(dc.R)/255, float64(sc.R)/255, as, ab, ao)
			g := blendChannel(mode, float64(dc.G)/255, float64(sc.G)/255, as, ab, ao)
			b := blendChannel(mode, float64(dc.B)/255, float64(sc.B)/255, as, ab, ao)
			dst.SetNRGBA(dx, dy, color.NRGBA{
				R: clamp8(r), G: clamp8(g), B: clamp8(b), A: clamp8(ao),
			})
		}
	}
}

// blendChannel computes the output straight-alpha channel value in [0,1].
func blendChannel(mode BlendMode, cb, cs, as, ab, ao float64) float64 {
	b := blendFn(mode, cb, cs)
	num := (1-ab)*as*cs + as*ab*b + (1-as)*ab*cb
	return num / ao
}

func clamp8(v float64) uint8 {
	n := int(math.Round(v * 255))
	if n < 0 {
		n = 0
	}
	if n > 255 {
		n = 255
	}
	return uint8(n)
}

// EncodePNG writes img as PNG to w.
func EncodePNG(img image.Image, w io.Writer) error { return png.Encode(w, img) }

// LoadImageFile is a convenience ImageLoader factory reading Path from disk (PNG/JPEG/GIF).
func LoadImageFile(props ImageProps) (image.Image, bool) {
	if props.Path == "" {
		return nil, false
	}
	f, err := os.Open(props.Path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, false
	}
	return img, true
}
