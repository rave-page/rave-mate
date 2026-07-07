// Package mediaeditor provides a pure-Go poster/thumbnail composer for rave.page events.
// Image composition runs entirely in stdlib + golang.org/x/image; no Fyne dependency here.
package mediaeditor

import (
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

//go:embed assets/Orbitron-Bold.ttf
var orbitronBoldTTF []byte

//go:embed assets/Orbitron-Regular.ttf
var orbitronRegularTTF []byte

// Poster describes a poster/thumbnail composition.
type Poster struct {
	Width, Height int

	// Background: solid fill (used even when BackgroundImagePath is set, as underlay).
	Background color.Color

	// BackgroundImagePath: optional PNG/JPEG scaled to cover.
	BackgroundImagePath string

	// Title: large bold (Orbitron-Bold), centered.
	Title string
	// Subtitle: medium weight, centered, below Title.
	Subtitle string
	// Lines: e.g. DJ names, one per line, below Subtitle.
	Lines []string

	// LogoPath: optional PNG/JPEG placed bottom-right at watermark opacity.
	LogoPath string
}

// Render composites and returns the final image.
func (p Poster) Render() (image.Image, error) {
	dst := image.NewNRGBA(image.Rect(0, 0, p.Width, p.Height))

	// ── background ──────────────────────────────────────────────
	bg := p.Background
	if bg == nil {
		bg = color.NRGBA{R: 0x0a, G: 0x0a, B: 0x0a, A: 0xff} // brand default
	}
	fill(dst, bg)

	if p.BackgroundImagePath != "" {
		bgImg, err := loadImage(p.BackgroundImagePath)
		if err == nil {
			drawCoverScaled(dst, bgImg)
		}
		// silently skip a missing/corrupt background image
	}

	// ── brand overlay: semi-transparent gradient-like dark tint ─
	drawTint(dst, color.NRGBA{A: 0xaa})

	// ── logo watermark ───────────────────────────────────────────
	if p.LogoPath != "" {
		logo, err := loadImage(p.LogoPath)
		if err == nil {
			drawLogoWatermark(dst, logo)
		}
	}

	// ── fonts ────────────────────────────────────────────────────
	boldFace, err := loadFace(orbitronBoldTTF, float64(p.Width)/12)
	if err != nil {
		return nil, fmt.Errorf("bold face: %w", err)
	}
	defer boldFace.Close()

	subFace, err := loadFace(orbitronRegularTTF, float64(p.Width)/18)
	if err != nil {
		return nil, fmt.Errorf("sub face: %w", err)
	}
	defer subFace.Close()

	lineFace, err := loadFace(orbitronRegularTTF, float64(p.Width)/24)
	if err != nil {
		return nil, fmt.Errorf("line face: %w", err)
	}
	defer lineFace.Close()

	// ── text layout ──────────────────────────────────────────────
	white := color.NRGBA{R: 0xfa, G: 0xfa, B: 0xfa, A: 0xff}
	muted := color.NRGBA{R: 0xa1, G: 0xa1, B: 0xaa, A: 0xff}
	brand := color.NRGBA{R: 0xF7, G: 0x08, B: 0x64, A: 0xff}

	// Vertical stack: center-align in upper 70% of canvas.
	padH := p.Width / 16 // horizontal padding for text
	maxW := p.Width - 2*padH

	margin := p.Height / 8
	y := margin

	// Title (bold, brand-pink accent line above it)
	if p.Title != "" {
		accentH := max(3, p.Height/200)
		titleLines := wrapText(boldFace, p.Title, maxW)
		titleH := lineHeight(boldFace) * len(titleLines)
		// accent above title
		accentY := y - accentH - lineHeight(boldFace)/4
		if accentY < margin/2 {
			accentY = margin / 2
		}
		accentW := min(p.Width/4, 120)
		accentX := (p.Width - accentW) / 2
		drawRect(dst, accentX, accentY, accentW, accentH, brand)
		drawTextLines(dst, boldFace, titleLines, p.Width, padH, y, white, maxW)
		y += titleH + lineHeight(boldFace)/2
	}

	if p.Subtitle != "" {
		subLines := wrapText(subFace, p.Subtitle, maxW)
		drawTextLines(dst, subFace, subLines, p.Width, padH, y, muted, maxW)
		y += lineHeight(subFace)*len(subLines) + lineHeight(subFace)/2
	}

	if len(p.Lines) > 0 {
		// separator
		sepY := y + lineHeight(lineFace)/4
		drawRect(dst, padH, sepY, maxW, max(1, p.Height/300), color.NRGBA{R: 0x26, G: 0x26, B: 0x2b, A: 0xff})
		y = sepY + lineHeight(lineFace)/2

		for _, line := range p.Lines {
			if line == "" {
				y += lineHeight(lineFace) / 2
				continue
			}
			wrapped := wrapText(lineFace, line, maxW)
			drawTextLines(dst, lineFace, wrapped, p.Width, padH, y, white, maxW)
			y += lineHeight(lineFace) * len(wrapped)
		}
	}

	return dst, nil
}

// Encode writes img as PNG to w.
func Encode(img image.Image, w io.Writer) error {
	return png.Encode(w, img)
}

// ── helpers ──────────────────────────────────────────────────────────────────

// fill paints the entire image with c.
func fill(dst *image.NRGBA, c color.Color) {
	draw.Draw(dst, dst.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
}

// drawTint draws a uniform transparent overlay to darken the background.
func drawTint(dst *image.NRGBA, c color.Color) {
	draw.Draw(dst, dst.Bounds(), image.NewUniform(c), image.Point{}, draw.Over)
}

// drawRect fills a solid rectangle.
func drawRect(dst *image.NRGBA, x, y, w, h int, c color.Color) {
	r := image.Rect(x, y, x+w, y+h).Intersect(dst.Bounds())
	draw.Draw(dst, r, image.NewUniform(c), image.Point{}, draw.Over)
}

// drawCoverScaled scales src to cover dst (may crop), centered.
func drawCoverScaled(dst *image.NRGBA, src image.Image) {
	db := dst.Bounds()
	sb := src.Bounds()
	dw, dh := float64(db.Dx()), float64(db.Dy())
	sw, sh := float64(sb.Dx()), float64(sb.Dy())

	scale := math.Max(dw/sw, dh/sh)
	nw := int(math.Round(sw * scale))
	nh := int(math.Round(sh * scale))
	ox := (db.Dx() - nw) / 2
	oy := (db.Dy() - nh) / 2

	scaledDst := image.Rect(db.Min.X+ox, db.Min.Y+oy, db.Min.X+ox+nw, db.Min.Y+oy+nh)
	drawScaled(dst, scaledDst, src)
}

// drawScaled nearest-neighbor scales src into the dstRect of dst.
func drawScaled(dst *image.NRGBA, dstRect image.Rectangle, src image.Image) {
	sb := src.Bounds()
	dstRect = dstRect.Intersect(dst.Bounds())
	if dstRect.Empty() {
		return
	}
	dw := dstRect.Dx()
	dh := dstRect.Dy()
	sw := float64(sb.Dx())
	sh := float64(sb.Dy())
	for dy := 0; dy < dh; dy++ {
		sy := int(float64(dy) / float64(dh) * sh)
		for dx := 0; dx < dw; dx++ {
			sx := int(float64(dx) / float64(dw) * sw)
			dst.Set(dstRect.Min.X+dx, dstRect.Min.Y+dy, src.At(sb.Min.X+sx, sb.Min.Y+sy))
		}
	}
}

// drawLogoWatermark places logo at 30% opacity in the bottom-right corner.
func drawLogoWatermark(dst *image.NRGBA, logo image.Image) {
	db := dst.Bounds()
	lb := logo.Bounds()

	maxSide := db.Dx() / 6
	lw, lh := lb.Dx(), lb.Dy()
	if lw > maxSide || lh > maxSide {
		scale := math.Min(float64(maxSide)/float64(lw), float64(maxSide)/float64(lh))
		lw = int(math.Round(float64(lw) * scale))
		lh = int(math.Round(float64(lh) * scale))
	}

	pad := db.Dx() / 32
	ox := db.Max.X - lw - pad
	oy := db.Max.Y - lh - pad
	scaledRect := image.Rect(ox, oy, ox+lw, oy+lh)

	// Draw logo at 30% opacity via per-pixel alpha blend.
	dstCopy := image.NewNRGBA(image.Rect(0, 0, lw, lh))
	drawScaled(dstCopy, image.Rect(0, 0, lw, lh), logo)

	const alpha = 76 // ~30% of 255
	for y := 0; y < lh; y++ {
		for x := 0; x < lw; x++ {
			sc := dstCopy.NRGBAAt(x, y)
			sc.A = uint8(int(sc.A) * alpha / 255)
			dst.SetNRGBA(scaledRect.Min.X+x, scaledRect.Min.Y+y,
				blendNRGBA(dst.NRGBAAt(scaledRect.Min.X+x, scaledRect.Min.Y+y), sc))
		}
	}
}

// blendNRGBA performs a standard Over blend of src onto dst (pre-multiplied alpha path).
func blendNRGBA(bg, fg color.NRGBA) color.NRGBA {
	if fg.A == 0 {
		return bg
	}
	if fg.A == 255 {
		return fg
	}
	fa := uint32(fg.A)
	ba := uint32(255 - fg.A)
	r := (uint32(fg.R)*fa + uint32(bg.R)*ba) / 255
	g := (uint32(fg.G)*fa + uint32(bg.G)*ba) / 255
	b := (uint32(fg.B)*fa + uint32(bg.B)*ba) / 255
	a := uint32(bg.A) + fa*(255-uint32(bg.A))/255
	return color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: uint8(a)}
}

// loadImage decodes a PNG or JPEG from path.
func loadImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	img, _, err := image.Decode(f)
	return img, err
}

// loadFace parses a TrueType font and returns a face at the given point size.
func loadFace(ttf []byte, pts float64) (font.Face, error) {
	parsed, err := opentype.Parse(ttf)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(parsed, &opentype.FaceOptions{
		Size: pts,
		DPI:  96,
	})
}

// lineHeight returns the integer advance height of a face.
func lineHeight(f font.Face) int {
	m := f.Metrics()
	return (m.Ascent + m.Descent).Ceil()
}

// wrapText splits text into lines no wider than maxW pixels (word-wrap).
func wrapText(f font.Face, text string, maxW int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	cur := ""
	for _, w := range words {
		candidate := w
		if cur != "" {
			candidate = cur + " " + w
		}
		if textWidth(f, candidate) <= maxW {
			cur = candidate
		} else {
			if cur != "" {
				lines = append(lines, cur)
			}
			cur = w
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// textWidth returns the advance width in pixels for s rendered with f.
func textWidth(f font.Face, s string) int {
	w := font.MeasureString(f, s)
	return w.Ceil()
}

// drawTextLines draws lines centered horizontally. y is the top of the first line.
func drawTextLines(dst *image.NRGBA, f font.Face, lines []string, canvasW, _ int, y int, c color.Color, _ int) {
	m := f.Metrics()
	ascent := m.Ascent.Ceil()
	lh := (m.Ascent + m.Descent).Ceil()

	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(c),
		Face: f,
	}
	for _, line := range lines {
		w := textWidth(f, line)
		x := (canvasW - w) / 2
		d.Dot = fixed.P(x, y+ascent)
		d.DrawString(line)
		y += lh
	}
}

// min/max for int (Go 1.21 has these builtins, but use compat helpers for older toolchains).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// loadImageDecoder registers decoders (called by init).
func init() {
	// ensure jpeg decoder is available (image.Decode needs registered formats)
	_ = jpeg.DefaultQuality
}
