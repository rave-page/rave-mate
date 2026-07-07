// Package deckcard renders one deck's now-playing card to an RGBA image: cover art, title/
// artist, BPM•key, elapsed clock, a fader bar and a low/mid/high EQ indicator, on a rounded
// (optionally transparent) card. It is the shared renderer behind the PNG file sink
// (internal/session/sinks/pngsink) and the GPU/IPC video-share sink (internal/videoshare) -
// both produce the same visual; only the output transport differs.
//
// Rendering is pure Go (image/draw + golang.org/x/image/font/opentype on an embedded Orbitron
// face). No cgo, no external state.
package deckcard

import (
	_ "embed"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/draw"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"rave.page/mate/internal/overlaystyle"
	"rave.page/mate/internal/session"
)

// Card geometry (px). Width/Height are the default card size; callers that need a different
// canvas can scale the result.
const (
	Width  = 360
	Height = 120
	pad    = 12
	artSz  = 96
	radius = 14
)

// brand palette (corporate identity - see internal/ui/theme.go).
var (
	colCard  = color.NRGBA{R: 0x12, G: 0x12, B: 0x17, A: 0xff} // #121217 card bg
	colInk   = color.NRGBA{R: 0xe6, G: 0xe8, B: 0xee, A: 0xff} // off-white text
	colMuted = color.NRGBA{R: 0x9a, G: 0x9c, B: 0xa6, A: 0xff} // muted text
	colPink  = color.NRGBA{R: 0xF7, G: 0x08, B: 0x64, A: 0xff} // brand pink
	colMint  = color.NRGBA{R: 0x08, G: 0xF7, B: 0x9B, A: 0xff} // brand mint
	colTrack = color.NRGBA{R: 0x2a, G: 0x2a, B: 0x31, A: 0xff} // bar/EQ track

	colEQArea     = color.NRGBA{R: 0x08, G: 0xF7, B: 0x9B, A: 0x39} // mint @ ~22% (EQ fill)
	colEQGrid     = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x1a} // faint mid grid line
	colFilterHP   = color.NRGBA{R: 0xe2, G: 0x3a, B: 0xd0, A: 0xff} // high-pass sweep (magenta)
	colFilterLP   = color.NRGBA{R: 0x22, G: 0xc9, B: 0xe0, A: 0xff} // low-pass sweep (cyan)
	colCenterTick = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0x59} // filter center tick

	colWaveBg       = color.NRGBA{R: 0x0a, G: 0x0a, B: 0x0e, A: 0xcc} // waveform panel backdrop
	colWavePlayed   = color.NRGBA{R: 0x08, G: 0xF7, B: 0x9B, A: 0xcc} // behind the playhead (already played)
	colWaveUpcoming = color.NRGBA{R: 0xc8, G: 0xca, B: 0xd2, A: 0xc0} // ahead of the playhead (coming up)
	colWavePlaceISH = color.NRGBA{R: 0x9a, G: 0x9c, B: 0xa6, A: 0x80} // flat line while peaks generate
)

// WaveOpts configures the combined scrolling-waveform + EQ-curve + FX-cutoff panel drawn at the
// bottom of the card. Zero value (Enabled=false) → the legacy EQ-box + filter-bar layout.
type WaveOpts struct {
	Enabled      bool    // draw the combined waveform panel instead of the EQ box + filter bar
	Peaks        []byte  // per-track max-abs peak buckets (nil → flat "generating" baseline)
	PeaksKey     string  // stable per-track id (e.g. ArtKey) - caches the smoothed envelope; "" hashes Peaks
	Duration     float64 // seconds the peaks span
	Position     float64 // current playback position (seconds) - caller interpolates (see DeckSnapshot.LivePosition)
	ZoomSeconds  float64 // visible timeframe across the panel (smaller = more zoomed = faster scroll); <=0 → 20
	PlayheadFrac float64 // playhead x as a fraction from the left; <=0 → 3/4

	// Appearance (empty/zero → built-in defaults). Colors are #rrggbb.
	WaveColor   string  // waveform tint (played full, upcoming dimmed)
	WaveOpacity float64 // waveform bar opacity 0..1
	BgColor     string  // canvas background colour
	BgOpacity   float64 // canvas background opacity 0..1

	// Rich styling (shared overlay-style.json; nil/empty → fall back to the flat fields above).
	WaveFill *overlaystyle.Fill     // waveform fill: solid or gradient (overrides WaveColor)
	BgFill   *overlaystyle.Fill     // background fill: solid or gradient (overrides BgColor)
	EQCols   *overlaystyle.EQColors // per-band EQ + per-direction FX curve colours
	Card     *overlaystyle.Card     // deck-card border style/width/colour + corner radius (nil → defaults)

	FaderReact bool // fade the whole card by channel-fader level (only applied when fader data exists)
}

// ApplyStyle overlays the shared overlay-style.json onto these options (style wins over the config
// defaults already set). defWaveColor/defBgColor are the config-resolved solid colours used when the
// style has neither a fill object nor a legacy flat colour. Called per frame by the native sinks.
func (w *WaveOpts) ApplyStyle(st overlaystyle.Style, defWaveColor, defBgColor string) {
	if st.ZoomSeconds > 0 {
		z := st.ZoomSeconds
		if z < 2 {
			z = 2
		} else if z > 600 {
			z = 600
		}
		w.ZoomSeconds = z
	}
	if st.PlayheadPct > 0 && st.PlayheadPct < 1 {
		w.PlayheadFrac = st.PlayheadPct
	}
	if st.WaveOpacity != nil {
		w.WaveOpacity = clamp01(*st.WaveOpacity)
	}
	if st.WaveBgOpacity != nil {
		w.BgOpacity = clamp01(*st.WaveBgOpacity)
	}
	if st.CardFaderReact != nil {
		w.FaderReact = *st.CardFaderReact
	}
	wf := st.WaveFillSpec(defWaveColor)
	bf := st.BgFillSpec(defBgColor)
	w.WaveFill, w.BgFill, w.EQCols, w.Card = &wf, &bf, st.EQColors, st.Card
}

func (w WaveOpts) zoom() float64 {
	if w.ZoomSeconds <= 0 {
		return 20
	}
	return w.ZoomSeconds
}

func (w WaveOpts) playhead() float64 {
	if w.PlayheadFrac <= 0 || w.PlayheadFrac >= 1 {
		return 0.75
	}
	return w.PlayheadFrac
}

//go:embed assets/Orbitron-Bold.ttf
var orbitronBoldTTF []byte

//go:embed assets/Orbitron-Medium.ttf
var orbitronMediumTTF []byte

// Faces holds the parsed font faces (build once, reuse across renders; not goroutine-safe - a
// font.Face is stateful, so one Faces per render goroutine).
type Faces struct {
	title  font.Face // Orbitron-Bold, title
	body   font.Face // Orbitron-Medium, artist / meta
	small  font.Face // Orbitron-Medium, small labels
	closes []func() error
}

// Close releases the faces.
func (f *Faces) Close() {
	for _, c := range f.closes {
		_ = c()
	}
}

// LoadFaces parses the embedded Orbitron faces at the card's base point sizes (scale 1).
func LoadFaces() (*Faces, error) { return LoadFacesScale(1) }

// LoadFacesScale parses the Orbitron faces at base_pt×scale so RenderScaled draws natively
// crisp glyphs (not raster-upscaled). The same scale must be passed to RenderScaled. scale is
// clamped to [1,8]; non-positive → 1.
func LoadFacesScale(scale float64) (*Faces, error) {
	scale = clampScale(scale)
	f := &Faces{}
	mk := func(ttf []byte, pts float64) (font.Face, error) {
		parsed, err := opentype.Parse(ttf)
		if err != nil {
			return nil, err
		}
		fc, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: pts * scale, DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			return nil, err
		}
		return fc, nil
	}
	var err error
	if f.title, err = mk(orbitronBoldTTF, 13); err != nil {
		return nil, fmt.Errorf("title face: %w", err)
	}
	if f.body, err = mk(orbitronMediumTTF, 10); err != nil {
		f.Close()
		return nil, fmt.Errorf("body face: %w", err)
	}
	if f.small, err = mk(orbitronMediumTTF, 8); err != nil {
		f.Close()
		return nil, fmt.Errorf("small face: %w", err)
	}
	f.closes = []func() error{f.title.Close, f.body.Close, f.small.Close}
	return f, nil
}

// waveStrip is the extra card height (px @1×) added below the standard card when the combined
// waveform panel is enabled - the full-width scrolling waveform + EQ curve + FX cutoff.
const waveStrip = 56

// Render composes one deck's card onto a transparent 360×120 canvas. art is the (already
// decoded) cover image, or nil for the no-art placeholder.
func Render(f *Faces, d session.DeckSnapshot, art image.Image) *image.NRGBA {
	return RenderScaled(f, d, art, WaveOpts{}, 1)
}

// RenderScaled renders the card at round(Width×scale) × round(Height×scale) with every
// geometric value scaled, so a Spout/video-share receiver can display it large and crisp on a
// 4K canvas (native, not raster-upscaled). The caller MUST pass Faces loaded at the SAME scale
// (LoadFacesScale(scale)) so glyphs are drawn natively at size. scale is clamped to [1,8];
// non-positive → 1. scale=1 is pixel-identical to the legacy renderer.
func RenderScaled(f *Faces, d session.DeckSnapshot, art image.Image, wave WaveOpts, scale float64) *image.NRGBA {
	scale = clampScale(scale)
	s := func(v int) int { return int(math.Round(float64(v) * scale)) } // px literal → scaled px

	W, baseH := s(Width), s(Height)
	H := baseH
	if wave.Enabled {
		H += s(waveStrip)
	}
	sPad, sArt, sRad := s(pad), s(artSz), s(radius)
	if wave.Card != nil && wave.Card.Radius != nil { // style-driven corner radius
		rr := *wave.Card.Radius
		if rr < 0 {
			rr = 0
		} else if rr > 60 {
			rr = 60
		}
		sRad = s(int(math.Round(rr)))
	}

	dst := image.NewNRGBA(image.Rect(0, 0, W, H))
	fillRoundRect(dst, image.Rect(0, 0, W, H), sRad, colCard)

	// On-air accent: a pink left edge bar when the deck is audible (runs the full card height).
	if d.OnAir {
		drawRect(dst, 0, sRad, s(4), H-2*sRad, colPink)
	}

	// Cover art (left) or placeholder.
	artRect := image.Rect(sPad, sPad, sPad+sArt, sPad+sArt)
	if art != nil {
		drawCover(dst, artRect, art)
	} else {
		drawPlaceholder(dst, artRect)
	}

	// Text column.
	tx := sPad + sArt + sPad
	tw := W - tx - sPad

	// Deck badge + title.
	badge := "[" + d.Deck + "]"
	drawText(dst, f.small, badge, tx, s(pad+12), colMint)
	bw := textWidth(f.small, badge) + s(6)
	title := d.Title
	if title == "" {
		title = "-"
	}
	drawText(dst, f.title, truncate(f.title, title, tw-bw), tx+bw, s(pad+14), colInk)

	// Artist.
	if d.Artist != "" {
		drawText(dst, f.body, truncate(f.body, d.Artist, tw), tx, s(pad+36), colMuted)
	}

	// BPM • Key (mint accent).
	if meta := metaLine(d); meta != "" {
		drawText(dst, f.body, truncate(f.body, meta, tw), tx, s(pad+54), colMint)
	}

	// Elapsed / length clock.
	drawText(dst, f.small, clock(d.ElapsedTime)+" / "+clock(d.TrackLength), tx, s(pad+72), colMuted)

	// Levels area (bottom of the standard card region).
	faderH, filterH := s(6), s(5)
	levelsBottom := baseH - sPad // 108 @1×
	faderY := levelsBottom - faderH
	if wave.Enabled {
		// Combined mode: the EQ box + filter bar move into the full-width waveform panel below;
		// the standard region keeps only the fader (full width).
		drawFader(dst, tx, faderY, tw, faderH, d)
		panel := image.Rect(sPad, baseH-s(2), W-sPad, H-sPad).Intersect(dst.Bounds())
		// Always draw the curve - neutral (0.5) when no mixer feed, so it's visible as a baseline
		// and animates once EQ/filter data arrives (MIDI map / Traktor channel feed).
		eqLow, eqMid, eqHigh, filt := 0.5, 0.5, 0.5, 0.5
		if d.HasMixer {
			eqLow, eqMid, eqHigh, filt = d.EQLow, d.EQMid, d.EQHigh, d.Filter
		}
		fader := 1.0 // unknown fader → full level (no shift)
		if d.HasFader {
			fader = d.Fader
		}
		curvePys, curveCols := eqFilterCurve(panel, eqLow, eqMid, eqHigh, filt, fader, wave.EQCols)
		drawWaveformPanel(dst, panel, wave, scale, curvePys) // curve = waveform amplitude envelope
		drawCurveLine(dst, panel, curvePys, curveCols, scale)
	} else if d.HasMixer {
		// EQ curve box (left) + meters stack (filter sweep over fader) on the right.
		eqW, gap := s(78), s(10)
		drawEQCurve(dst, image.Rect(tx, levelsBottom-s(20), tx+eqW, levelsBottom), d.EQLow, d.EQMid, d.EQHigh, scale)
		mx := tx + eqW + gap
		mw := tw - eqW - gap
		filterY := faderY - s(4) - filterH
		drawFilter(dst, image.Rect(mx, filterY, mx+mw, filterY+filterH), d.Filter, scale)
		drawFader(dst, mx, faderY, mw, faderH, d)
	} else {
		drawFader(dst, tx, faderY, tw, faderH, d)
	}

	drawCardBorder(dst, image.Rect(0, 0, W, H), sRad, wave.Card, scale)
	if wave.FaderReact && d.HasFader {
		fadeCardAlpha(dst, faderCardOpacity(d.Fader)) // whole card fades with the channel fader
	}
	return dst
}

// faderCardOpacity maps a 0..1 fader level to a card opacity, floored so a fully-down deck stays
// faintly visible rather than vanishing.
func faderCardOpacity(fader float64) float64 {
	const floor = 0.15
	if fader < 0 {
		fader = 0
	} else if fader > 1 {
		fader = 1
	}
	return floor + (1-floor)*fader
}

// fadeCardAlpha scales every pixel's alpha by op (no-op at op>=1).
func fadeCardAlpha(dst *image.NRGBA, op float64) {
	if op >= 1 {
		return
	}
	for i := 3; i < len(dst.Pix); i += 4 {
		dst.Pix[i] = uint8(float64(dst.Pix[i]) * op)
	}
}

// drawCardBorder strokes the card frame per the style (none/solid/glow). nil/none → no border.
func drawCardBorder(dst *image.NRGBA, r image.Rectangle, rad int, c *overlaystyle.Card, scale float64) {
	if c == nil || c.Border == "" || c.Border == "none" {
		return
	}
	col := colPink // default brand-pink frame
	if pc, ok := parseHex(c.BorderColor); ok {
		col = pc
	}
	bw := c.BorderW
	if bw <= 0 {
		bw = 1
	}
	sw := int(math.Round(bw * scale))
	if sw < 1 {
		sw = 1
	}
	if c.Border == "glow" { // faint wide halo under the crisp stroke
		g := col
		g.A = uint8(float64(col.A) * 0.28)
		strokeRoundRect(dst, r, rad, sw*3, g)
	}
	strokeRoundRect(dst, r, rad, sw, col)
}

// metaLine builds the "128 BPM • 8A" accent line (omits empty parts).
func metaLine(d session.DeckSnapshot) string {
	var parts []string
	if d.BPM > 0 {
		parts = append(parts, fmt.Sprintf("%.0f BPM", math.Round(d.BPM)))
	}
	if d.Key != "" {
		parts = append(parts, d.Key)
	}
	return strings.Join(parts, "  •  ")
}

// clock formats seconds as M:SS (negative/NaN → 0:00).
func clock(sec float64) string {
	if sec <= 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
		return "0:00"
	}
	t := int(sec + 0.5)
	return fmt.Sprintf("%d:%02d", t/60, t%60)
}

// drawFader draws a fader bar: dark track with a fill proportional to the level. Unknown fader
// (no HasFader) → empty track. Mint when on-air, pink when cued.
func drawFader(dst *image.NRGBA, x, y, w, h int, d session.DeckSnapshot) {
	drawRoundRect(dst, image.Rect(x, y, x+w, y+h), h/2, colTrack)
	if d.HasFader {
		fw := int(float64(w) * clamp01(d.Fader))
		if fw > 0 {
			c := colMint
			if !d.OnAir {
				c = colPink
			}
			drawRoundRect(dst, image.Rect(x, y, x+fw, y+h), h/2, c)
		}
	}
}

// eqCurveSampler returns f(cx in 0..100) → cy in 0..40 for the low/mid/high EQ S-curve (two cubic
// béziers through cy(v)=38-36·eqLevel(v)). Shared by the legacy box + the merged EQ/filter curve.
func eqCurveSampler(low, mid, high float64) func(float64) float64 {
	yv := func(v float64) float64 { return 38 - 36*eqDisplayLevel(v) }
	y0, y1, y2 := yv(low), yv(mid), yv(high)
	cubic := func(p0, p1, p2, p3, t float64) float64 {
		u := 1 - t
		return u*u*u*p0 + 3*u*u*t*p1 + 3*u*t*t*p2 + t*t*t*p3
	}
	const seg = 48
	type pt struct{ x, y float64 }
	pts := make([]pt, 0, 2*seg+1)
	for i := 0; i <= seg; i++ {
		t := float64(i) / seg
		pts = append(pts, pt{cubic(0, 25, 25, 50, t), cubic(y0, y0, y1, y1, t)})
	}
	for i := 1; i <= seg; i++ {
		t := float64(i) / seg
		pts = append(pts, pt{cubic(50, 75, 75, 100, t), cubic(y1, y1, y2, y2, t)})
	}
	return func(cx float64) float64 { // interpolate y at curve-x (xs monotonic increasing)
		if cx <= pts[0].x {
			return pts[0].y
		}
		last := pts[len(pts)-1]
		if cx >= last.x {
			return last.y
		}
		for i := 1; i < len(pts); i++ {
			if pts[i].x >= cx {
				a, b := pts[i-1], pts[i]
				if b.x == a.x {
					return b.y
				}
				return a.y + (cx-a.x)/(b.x-a.x)*(b.y-a.y)
			}
		}
		return last.y
	}
}

// strokeCurve paints the per-column curve pys as a continuous band of half-width hw, colouring each
// column via colFn (bridges adjacent columns so the line stays connected).
func strokeCurve(dst *image.NRGBA, box image.Rectangle, pys []float64, hw float64, colFn func(col int) color.NRGBA) {
	prev := -1.0
	for col := 0; col < len(pys); col++ {
		py := pys[col]
		lo, hi := py, py
		if prev >= 0 {
			lo, hi = math.Min(lo, prev), math.Max(hi, prev)
		}
		px := box.Min.X + col
		c := colFn(col)
		for y := int(lo - hw + 0.5); y <= int(hi+hw+0.5); y++ {
			if y >= box.Min.Y && y < box.Max.Y {
				dst.SetNRGBA(px, y, blend(dst.NRGBAAt(px, y), c))
			}
		}
		prev = py
	}
}

// eqNeutralLevel is the display height (0=bottom … 1=top) of a neutral (0.5) EQ band. DJs rarely
// boost, so neutral sits high: cuts (0..0.5) get most of the vertical range (more fidelity), boosts
// (0.5..1) are compressed into the small top band.
const eqNeutralLevel = 0.8

// eqDisplayLevel maps an EQ band value (0=full cut … 0.5=neutral … 1=full boost) to a display
// level (0=bottom … 1=top) with the neutral point shifted up + an expanded cut range.
func eqDisplayLevel(v float64) float64 {
	v = clamp01(v)
	if v <= 0.5 {
		return v / 0.5 * eqNeutralLevel
	}
	return eqNeutralLevel + (v-0.5)/0.5*(1-eqNeutralLevel)
}

// smoothstep is the classic 3x²-2x³ ease on [0,1].
func smoothstep(x float64) float64 { return x * x * (3 - 2*x) }

// lerpNRGBA linearly interpolates two colours (t in 0..1).
func lerpNRGBA(a, b color.NRGBA, t float64) color.NRGBA {
	t = clamp01(t)
	li := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*t + 0.5) }
	return color.NRGBA{R: li(a.R, b.R), G: li(a.G, b.G), B: li(a.B, b.B), A: li(a.A, b.A)}
}

// haloCurve paints a soft drop-shadow under a curve: several widening passes with falling alpha so
// it fades smoothly to transparency (a gradient backdrop, not a hard outline). Keeps the line
// legible over a same-colour waveform.
func haloCurve(dst *image.NRGBA, box image.Rectangle, pys []float64, lineHalf, scale float64) {
	const passes = 6
	for i := passes; i >= 1; i-- {
		f := float64(i) / passes // 1 = outermost/faintest … small = inner/strongest
		hw := lineHalf + f*4.0*scale
		a := uint8(0x6e * (1 - f) * (1 - f)) // quadratic falloff to 0 at the outer edge
		if a == 0 {
			continue
		}
		c := color.NRGBA{R: 0x05, G: 0x07, B: 0x09, A: a}
		strokeCurve(dst, box, pys, hw, func(int) color.NRGBA { return c })
	}
}

// drawEQCurve draws the low/mid/high EQ as a filled S-curve (legacy box): mint stroke over a
// ~22% mint area down to the box floor, faint mid grid line.
func drawEQCurve(dst *image.NRGBA, box image.Rectangle, low, mid, high, scale float64) {
	drawEQCurveBox(dst, box, low, mid, high, scale, colEQArea, true, false)
}

// drawEQCurveBox draws the EQ S-curve into box with a configurable area fill colour + optional mid
// grid line + optional dark halo (for legibility over a waveform).
func drawEQCurveBox(dst *image.NRGBA, box image.Rectangle, low, mid, high, scale float64, areaCol color.NRGBA, gridded, halo bool) {
	box = box.Intersect(dst.Bounds())
	bw, bh := box.Dx(), box.Dy()
	if bw <= 0 || bh <= 0 {
		return
	}
	curveY := eqCurveSampler(low, mid, high)
	pixY := func(cy float64) float64 { return float64(box.Min.Y) + cy/40*float64(bh) }

	pys := make([]float64, bw)
	for col := 0; col < bw; col++ {
		py := pixY(curveY((float64(col) + 0.5) / float64(bw) * 100))
		pys[col] = py
		px := box.Min.X + col
		for y := int(py + 0.5); y < box.Max.Y; y++ {
			dst.SetNRGBA(px, y, blend(dst.NRGBAAt(px, y), areaCol))
		}
	}
	if gridded {
		gt := int(math.Round(scale))
		if gt < 1 {
			gt = 1
		}
		gridY := int(pixY(20))
		for dy := 0; dy < gt; dy++ {
			y := gridY + dy
			if y < box.Min.Y || y >= box.Max.Y {
				continue
			}
			for x := box.Min.X; x < box.Max.X; x++ {
				dst.SetNRGBA(x, y, blend(dst.NRGBAAt(x, y), colEQGrid))
			}
		}
	}
	if halo {
		haloCurve(dst, box, pys, 0.8*scale, scale)
	}
	strokeCurve(dst, box, pys, 0.8*scale, func(int) color.NRGBA { return colMint })
}

// eqFilterCurve computes the merged EQ+filter curve over box: per-column pixel-y (pys) + per-column
// stroke colour (cols, EQ↔filter cross-faded). The filter rolloff caps the EQ level on the cut
// side; a wide smooth-max rounds the join. HP (filter>0.5) cuts the low end from the left
// (magenta), LP (<0.5) the high end from the right (cyan). The waveform panel uses pys as an
// amplitude envelope; drawCurveLine strokes it.
func eqFilterCurve(box image.Rectangle, low, mid, high, filter, fader float64, ec *overlaystyle.EQColors) ([]float64, []color.NRGBA) {
	bw, bh := box.Dx(), box.Dy()
	if bw <= 0 || bh <= 0 {
		return nil, nil
	}
	loC := eqBandColor(ec, 0, colMint)
	miC := eqBandColor(ec, 1, colMint)
	hiC := eqBandColor(ec, 2, colMint)
	hpC := eqBandColor(ec, 3, colFilterHP)
	lpC := eqBandColor(ec, 4, colFilterLP)
	eqY := eqCurveSampler(low, mid, high)
	pixY := func(cy float64) float64 { return float64(box.Min.Y) + cy/40*float64(bh) }

	// Filter rolloff as a pass-gain 0..1 (1 = unaffected). cy = 38-36·gain matches the EQ's space,
	// so the curves live on the same scale and genuinely intersect.
	dev := clamp01(filter) - 0.5
	active := math.Abs(dev) >= 0.02
	hp := dev > 0
	frac := math.Abs(dev) * 2
	const slope = 0.30
	gainAt := func(fx float64) float64 {
		if !active {
			return 1
		}
		var g float64
		if hp {
			cut := frac * 0.5
			g = (fx - (cut - slope)) / slope
		} else {
			cut := 1 - frac*0.5
			g = ((cut + slope) - fx) / slope
		}
		return smoothstep(clamp01(g)) // ease the ramp → S-shaped rolloff (rounds the 0 + 1 corners)
	}
	edgeCol := hpC
	if !hp {
		edgeCol = lpC
	}

	pys := make([]float64, bw)
	cols := make([]color.NRGBA, bw)
	fd := clamp01(fader)
	for col := 0; col < bw; col++ {
		fx := (float64(col) + 0.5) / float64(bw)
		eqLevel := (38 - eqY(fx*100)) / 36 // EQ band level 0..1 (1 = top)
		g := gainAt(fx)                    // filter pass-gain 0..1
		// Filter + fader MULTIPLY the EQ level, so any movement immediately + proportionally pulls
		// the curve down (responsive) and it smoothly returns to the EQ where the filter passes.
		pys[col] = pixY(38 - 36*eqLevel*g*fd)
		// Base curve colour interpolates across the bands (low→mid left half, mid→high right half),
		// then cross-fades to the filter-cut colour by how much the filter cuts.
		var base color.NRGBA
		if fx < 0.5 {
			base = lerpNRGBA(loC, miC, fx*2)
		} else {
			base = lerpNRGBA(miC, hiC, (fx-0.5)*2)
		}
		w := 0.0
		if active {
			w = smoothstep(clamp01((1 - g) * 1.8)) // tint toward the filter colour by how much it cuts
		}
		cols[col] = lerpNRGBA(base, edgeCol, w)
	}
	return pys, cols
}

// eqBandColor returns the configured per-band/FX curve colour (idx 0..4 = low,mid,high,hp,lp) or def.
func eqBandColor(ec *overlaystyle.EQColors, idx int, def color.NRGBA) color.NRGBA {
	if ec == nil {
		return def
	}
	s := [...]string{ec.Low, ec.Mid, ec.High, ec.HP, ec.LP}[idx]
	if c, ok := parseHex(s); ok {
		return c
	}
	return def
}

// drawCurveLine strokes the merged EQ/filter curve (feathered halo + cross-faded colour) on top.
func drawCurveLine(dst *image.NRGBA, box image.Rectangle, pys []float64, cols []color.NRGBA, scale float64) {
	haloCurve(dst, box, pys, 0.8*scale, scale)
	strokeCurve(dst, box, pys, 0.8*scale, func(col int) color.NRGBA { return cols[col] })
}

// drawFilter draws the center-origin filter sweep: dark track, a center tick, and a fill from
// the centre toward the deviated side - magenta (HP) when filter>0.5, cyan (LP) when <0.5.
func drawFilter(dst *image.NRGBA, bar image.Rectangle, filter, scale float64) {
	h := bar.Dy()
	rad := h / 2
	drawRoundRect(dst, bar, rad, colTrack)
	cx := (bar.Min.X + bar.Max.X) / 2
	dev := clamp01(filter) - 0.5
	w := int(float64(bar.Dx())*math.Abs(dev) + 0.5)
	if w > 0 {
		if dev >= 0 {
			drawRoundRect(dst, image.Rect(cx, bar.Min.Y, cx+w, bar.Max.Y), rad, colFilterHP)
		} else {
			drawRoundRect(dst, image.Rect(cx-w, bar.Min.Y, cx, bar.Max.Y), rad, colFilterLP)
		}
	}
	tw := int(math.Round(scale)) // tick width (1px @1×)
	if tw < 1 {
		tw = 1
	}
	drawRect(dst, cx-tw/2, bar.Min.Y-tw, tw, h+2*tw, colCenterTick) // center tick
}

// drawWaveformPanel draws the scrolling track waveform into box: time maps to x with the playhead
// fixed at wave.playhead() from the left and ZoomSeconds across the box width (smaller = more
// zoomed = faster scroll). A pink playhead line marks "now". nil peaks → a flat baseline (still
// generating). curvePys (per-column EQ/filter curve pixel-y, len == box width, or nil) is used as
// an amplitude envelope: waveform outside it (mirrored around centre) is dimmed, so a cut band /
// filter visibly mutes the waveform there.
// waveEnv caches the smoothed amplitude envelope (0..1 per column at imgPps px/s) per track so the
// scroll samples a STABLE, pre-aggregated curve instead of point-sampling raw peaks every frame -
// the latter shimmers as the playhead moves sub-pixel. Built once per (track, resolution).
var (
	envMu    sync.Mutex
	envCache = map[string][]float64{}
)

func waveEnv(peaks []byte, key string, dur, imgPps float64) []float64 {
	if key == "" {
		h := fnv.New64a()
		_, _ = h.Write(peaks)
		key = strconv.FormatUint(h.Sum64(), 16)
	}
	ck := key + "|" + strconv.Itoa(int(imgPps*10)) + "|" + strconv.FormatFloat(dur, 'f', 1, 64)
	envMu.Lock()
	defer envMu.Unlock()
	if e, ok := envCache[ck]; ok {
		return e
	}
	e := buildEnv(peaks, dur, imgPps)
	if len(envCache) > 24 { // bound: a handful of tracks × zoom levels
		envCache = map[string][]float64{}
	}
	envCache[ck] = e
	return e
}

// buildEnv folds the raw peak buckets into a max-aggregated, heavily-smoothed 0..1 envelope at
// imgPps columns/sec (mirrors the browser's buildWaveImg envelope so all outputs look the same).
func buildEnv(peaks []byte, dur, imgPps float64) []float64 {
	n := len(peaks)
	iw := int(dur*imgPps) + 1
	if iw < 1 || n == 0 || dur <= 0 {
		return []float64{0}
	}
	amp := make([]float64, iw)
	pkPerCol := (float64(n) / dur) / imgPps
	span := 0.5 / imgPps
	for x := 0; x < iw; x++ {
		t := float64(x) / imgPps
		if pkPerCol >= 1 { // zoomed out: max-abs over the column's time span
			ia := int((t - span) / dur * float64(n))
			ib := int(math.Ceil((t + span) / dur * float64(n)))
			if ia < 0 {
				ia = 0
			}
			if ib > n-1 {
				ib = n - 1
			}
			m := 0
			for i := ia; i <= ib; i++ {
				if int(peaks[i]) > m {
					m = int(peaks[i])
				}
			}
			amp[x] = float64(m) / 255
		} else { // zoomed in: interpolate between buckets
			f := t / dur * float64(n-1)
			i := int(f)
			if i < 0 {
				i = 0
			}
			j := i + 1
			if j > n-1 {
				j = n - 1
			}
			amp[x] = (float64(peaks[i])*(1-(f-float64(i))) + float64(peaks[j])*(f-float64(i))) / 255
		}
	}
	for p := 0; p < 3; p++ { // 3 binomial passes → soft, low-fidelity envelope (no shimmer)
		prev := amp[0]
		for x := 0; x < iw; x++ {
			nx := amp[x]
			if x < iw-1 {
				nx = amp[x+1]
			}
			cur := amp[x]
			amp[x] = (prev + 2*cur + nx) * 0.25
			prev = cur
		}
	}
	return amp
}

func drawWaveformPanel(dst *image.NRGBA, box image.Rectangle, wave WaveOpts, scale float64, curvePys []float64) {
	box = box.Intersect(dst.Bounds())
	bw, bh := box.Dx(), box.Dy()
	if bw <= 0 || bh <= 0 {
		return
	}
	bgCol := hexNRGBA(wave.BgColor, wave.BgOpacity, colWaveBg)
	brightCol := hexNRGBA(wave.WaveColor, wave.WaveOpacity, colWavePlayed)
	dimCol := hexNRGBA(wave.WaveColor, wave.WaveOpacity*0.22, colWaveUpcoming)
	// Solid-or-gradient samplers (gradient = overlay-style.json waveFill/waveBg). bright/dim share
	// the waveform fill; dim is the same fill at the lower above-curve opacity.
	bgFn := fillSampler(wave.BgFill, box, wave.BgOpacity, bgCol)
	brightFn := fillSampler(wave.WaveFill, box, wave.WaveOpacity, brightCol)
	dimFn := fillSampler(wave.WaveFill, box, wave.WaveOpacity*0.22, dimCol)
	fillRoundRectFn(dst, box, int(math.Round(4*scale)), bgFn)
	midY := box.Min.Y + bh/2
	half := math.Max(float64(bh)/2-1, 1)
	playX := box.Min.X + int(wave.playhead()*float64(bw))
	pps := float64(bw) / wave.zoom() // pixels per second
	useEnv := len(curvePys) == bw

	n := len(wave.Peaks)
	if n == 0 || wave.Duration <= 0 || pps <= 0 {
		for x := box.Min.X; x < box.Max.X; x++ { // placeholder flat line
			dst.SetNRGBA(x, midY, blend(dst.NRGBAAt(x, midY), colWavePlaceISH))
		}
	} else {
		// Sample a cached, smoothed envelope (built at the display resolution) with sub-pixel linear
		// interpolation as it scrolls - stable, no shimmer (vs. point-sampling raw peaks per pixel).
		env := waveEnv(wave.Peaks, wave.PeaksKey, wave.Duration, pps)
		ew := len(env)
		base := wave.Position*pps - float64(playX-box.Min.X) // env column under panel col 0
		for col := 0; col < bw; col++ {
			x := box.Min.X + col
			src := base + float64(col)
			if src < 0 || src >= float64(ew-1) {
				continue
			}
			i := int(src)
			fr := src - float64(i)
			amp := env[i]*(1-fr) + env[i+1]*fr
			hh := int(amp*half + 0.5)
			curveY := 0.0
			if useEnv {
				curveY = curvePys[col] // the EQ/filter curve = the loudness ceiling
			}
			for y := midY - hh; y <= midY+hh; y++ {
				if y < box.Min.Y || y >= box.Max.Y {
					continue
				}
				c := brightFn(x, y)
				if useEnv && float64(y) < curveY {
					c = dimFn(x, y) // above the curve → quieter, dimmed (below the curve stays bright)
				}
				dst.SetNRGBA(x, y, blend(dst.NRGBAAt(x, y), c))
			}
		}
	}
	// Playhead line ("now").
	pw := int(math.Round(1.5 * scale))
	if pw < 1 {
		pw = 1
	}
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for dx := 0; dx < pw; dx++ {
			if x := playX + dx; x >= box.Min.X && x < box.Max.X {
				dst.SetNRGBA(x, y, colPink)
			}
		}
	}
}

// ── draw primitives ────────────────────────────────────────────────────────────

func drawRect(dst *image.NRGBA, x, y, w, h int, c color.Color) {
	r := image.Rect(x, y, x+w, y+h).Intersect(dst.Bounds())
	draw.Draw(dst, r, image.NewUniform(c), image.Point{}, draw.Over)
}

func fillRoundRect(dst *image.NRGBA, r image.Rectangle, rad int, c color.NRGBA) {
	roundRect(dst, r, rad, c, true)
}

func drawRoundRect(dst *image.NRGBA, r image.Rectangle, rad int, c color.NRGBA) {
	roundRect(dst, r, rad, c, false)
}

// strokeRoundRect draws an anti-aliased rounded-rect outline of width w (inset inward) in c. The
// ring coverage is outer-corner coverage minus the inset inner-corner coverage, so corners stay AA.
func strokeRoundRect(dst *image.NRGBA, r image.Rectangle, rad, w int, c color.NRGBA) {
	r = r.Intersect(dst.Bounds())
	if w <= 0 || r.Empty() {
		return
	}
	if rad*2 > r.Dx() {
		rad = r.Dx() / 2
	}
	if rad*2 > r.Dy() {
		rad = r.Dy() / 2
	}
	inner := image.Rect(r.Min.X+w, r.Min.Y+w, r.Max.X-w, r.Max.Y-w)
	irad := rad - w
	if irad < 0 {
		irad = 0
	}
	fr, ir := float64(rad), float64(irad)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			out := cornerCoverage(x, y, r, fr)
			if out <= 0 {
				continue
			}
			in := 0.0
			if x >= inner.Min.X && x < inner.Max.X && y >= inner.Min.Y && y < inner.Max.Y {
				in = cornerCoverage(x, y, inner, ir)
			}
			cov := out - in
			if cov <= 0 {
				continue
			}
			if cov > 1 {
				cov = 1
			}
			px := c
			px.A = uint8(float64(c.A) * cov)
			dst.SetNRGBA(x, y, blend(dst.NRGBAAt(x, y), px))
		}
	}
}

// roundRect rasterizes a rounded rect with anti-aliased corners. replace=true writes the pixel
// directly (used for the transparent-canvas card body); false blends Over.
func roundRect(dst *image.NRGBA, r image.Rectangle, rad int, c color.NRGBA, replace bool) {
	r = r.Intersect(dst.Bounds())
	if rad*2 > r.Dx() {
		rad = r.Dx() / 2
	}
	if rad*2 > r.Dy() {
		rad = r.Dy() / 2
	}
	fr := float64(rad)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			a := cornerCoverage(x, y, r, fr)
			if a <= 0 {
				continue
			}
			px := c
			px.A = uint8(float64(c.A) * a)
			if replace && a >= 1 {
				dst.SetNRGBA(x, y, c)
			} else {
				dst.SetNRGBA(x, y, blend(dst.NRGBAAt(x, y), px))
			}
		}
	}
}

// cornerCoverage returns 0..1 pixel coverage for the rounded-corner mask at (x,y).
func cornerCoverage(x, y int, r image.Rectangle, rad float64) float64 {
	if rad <= 0 {
		return 1
	}
	fx, fy := float64(x)+0.5, float64(y)+0.5
	cx := math.Max(float64(r.Min.X)+rad, math.Min(fx, float64(r.Max.X)-rad))
	cy := math.Max(float64(r.Min.Y)+rad, math.Min(fy, float64(r.Max.Y)-rad))
	cov := rad - math.Hypot(fx-cx, fy-cy) + 0.5 // 1px AA band
	if cov >= 1 {
		return 1
	}
	if cov <= 0 {
		return 0
	}
	return cov
}

// blend composites fg over bg (straight-alpha Over).
func blend(bg, fg color.NRGBA) color.NRGBA {
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

// drawCover scales src to cover dstRect (centre-crop), clipped to a rounded mask.
func drawCover(dst *image.NRGBA, dstRect image.Rectangle, src image.Image) {
	sb := src.Bounds()
	sw, sh := float64(sb.Dx()), float64(sb.Dy())
	if sw <= 0 || sh <= 0 {
		drawPlaceholder(dst, dstRect)
		return
	}
	dw, dh := float64(dstRect.Dx()), float64(dstRect.Dy())
	scale := math.Max(dw/sw, dh/sh)
	ox := (sw - dw/scale) / 2
	oy := (sh - dh/scale) / 2
	const rad = 8
	for y := 0; y < dstRect.Dy(); y++ {
		for x := 0; x < dstRect.Dx(); x++ {
			a := cornerCoverage(dstRect.Min.X+x, dstRect.Min.Y+y, dstRect, rad)
			if a <= 0 {
				continue
			}
			sx := sb.Min.X + int(ox+float64(x)/scale)
			sy := sb.Min.Y + int(oy+float64(y)/scale)
			cr, cg, cb, _ := src.At(sx, sy).RGBA()
			px := color.NRGBA{R: uint8(cr >> 8), G: uint8(cg >> 8), B: uint8(cb >> 8), A: uint8(255 * a)}
			dx, dy := dstRect.Min.X+x, dstRect.Min.Y+y
			dst.SetNRGBA(dx, dy, blend(dst.NRGBAAt(dx, dy), px))
		}
	}
}

// drawPlaceholder draws a rounded dark tile with a pink diamond accent (no-art stand-in).
func drawPlaceholder(dst *image.NRGBA, r image.Rectangle) {
	drawRoundRect(dst, r, 8, colTrack)
	cx, cy := (r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2
	sz := r.Dx() / 6
	for y := -sz; y <= sz; y++ {
		for x := -sz; x <= sz; x++ {
			if abs(x)+abs(y) <= sz {
				dst.SetNRGBA(cx+x, cy+y, blend(dst.NRGBAAt(cx+x, cy+y), colPink))
			}
		}
	}
}

// ── text ───────────────────────────────────────────────────────────────────────

func drawText(dst *image.NRGBA, f font.Face, s string, x, baseline int, c color.Color) {
	if s == "" {
		return
	}
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: f}
	d.Dot = fixed.P(x, baseline)
	d.DrawString(s)
}

func textWidth(f font.Face, s string) int { return font.MeasureString(f, s).Ceil() }

// truncate clips s to maxW px, appending "…" when it overflows.
func truncate(f font.Face, s string, maxW int) string {
	if maxW <= 0 || textWidth(f, s) <= maxW {
		return s
	}
	const ell = "…"
	ew := textWidth(f, ell)
	rs := []rune(s)
	for len(rs) > 0 {
		rs = rs[:len(rs)-1]
		if textWidth(f, string(rs))+ew <= maxW {
			return strings.TrimRight(string(rs), " ") + ell
		}
	}
	return ell
}

// ── misc ───────────────────────────────────────────────────────────────────────

// clampScale bounds a render scale to [1,8]; non-positive/NaN → 1.
func clampScale(s float64) float64 {
	if s <= 0 || math.IsNaN(s) {
		return 1
	}
	if s < 1 {
		return 1
	}
	if s > 8 {
		return 8
	}
	return s
}

// hexNRGBA parses a #rgb/#rrggbb colour and sets its alpha to opacity (0..1). Invalid hex → the
// fallback (with its own alpha), so a renderer with an unset colour keeps the built-in look.
// fillSampler returns a per-pixel colour for a solid OR gradient fill over box at the given opacity,
// matching the browser's gradient geometry (0°=left→right, 90°=top→bottom; radial from centre).
func fillSampler(fill *overlaystyle.Fill, box image.Rectangle, op float64, def color.NRGBA) func(x, y int) color.NRGBA {
	if fill == nil || !fill.IsGradient() {
		c := def
		if fill != nil && fill.Color != "" {
			c = hexNRGBA(fill.Color, op, def)
		}
		return func(int, int) color.NRGBA { return c }
	}
	g := fill.Grad
	stops := append([]overlaystyle.Stop(nil), g.Stops...)
	sort.Slice(stops, func(i, j int) bool { return stops[i].P < stops[j].P })
	var ramp [256]color.NRGBA
	for i := range ramp {
		ramp[i] = sampleStops(stops, float64(i)/255, op)
	}
	w, h := float64(box.Dx()), float64(box.Dy())
	cx, cy := float64(box.Min.X)+w/2, float64(box.Min.Y)+h/2
	if g.Kind == "radial" {
		r := math.Max(w, h) / 2
		if r <= 0 {
			r = 1
		}
		return func(x, y int) color.NRGBA {
			t := math.Hypot(float64(x)-cx, float64(y)-cy) / r
			if t > 1 {
				t = 1
			}
			return ramp[int(t*255+0.5)]
		}
	}
	a := g.Angle * math.Pi / 180
	dx, dy := math.Cos(a)*w/2, math.Sin(a)*h/2
	ax, ay := cx-dx, cy-dy // gradient start
	bx, by := 2*dx, 2*dy   // B − A
	den := bx*bx + by*by
	return func(x, y int) color.NRGBA {
		if den == 0 {
			return ramp[0]
		}
		t := ((float64(x)-ax)*bx + (float64(y)-ay)*by) / den
		if t < 0 {
			t = 0
		} else if t > 1 {
			t = 1
		}
		return ramp[int(t*255+0.5)]
	}
}

// sampleStops linearly interpolates sorted gradient stops at t (0..1), applying opacity.
func sampleStops(stops []overlaystyle.Stop, t, op float64) color.NRGBA {
	if len(stops) == 0 {
		return color.NRGBA{}
	}
	a8 := uint8(clamp01(op)*255 + 0.5)
	if t <= stops[0].P {
		c, _ := parseHex(stops[0].C)
		c.A = a8
		return c
	}
	if last := stops[len(stops)-1]; t >= last.P {
		c, _ := parseHex(last.C)
		c.A = a8
		return c
	}
	for i := 1; i < len(stops); i++ {
		if stops[i].P >= t {
			a, b := stops[i-1], stops[i]
			f := 0.0
			if b.P != a.P {
				f = (t - a.P) / (b.P - a.P)
			}
			ca, _ := parseHex(a.C)
			cb, _ := parseHex(b.C)
			lerp := func(x, y uint8) uint8 { return uint8(float64(x) + (float64(y)-float64(x))*f) }
			return color.NRGBA{R: lerp(ca.R, cb.R), G: lerp(ca.G, cb.G), B: lerp(ca.B, cb.B), A: a8}
		}
	}
	c, _ := parseHex(stops[len(stops)-1].C)
	c.A = a8
	return c
}

// fillRoundRectFn fills a rounded rect, colouring each pixel via fn (for gradient backgrounds),
// with anti-aliased corners (reuses cornerCoverage).
func fillRoundRectFn(dst *image.NRGBA, r image.Rectangle, rad int, fn func(x, y int) color.NRGBA) {
	r = r.Intersect(dst.Bounds())
	if rad*2 > r.Dx() {
		rad = r.Dx() / 2
	}
	if rad*2 > r.Dy() {
		rad = r.Dy() / 2
	}
	fr := float64(rad)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			cov := cornerCoverage(x, y, r, fr)
			if cov <= 0 {
				continue
			}
			px := fn(x, y)
			px.A = uint8(float64(px.A) * cov)
			dst.SetNRGBA(x, y, blend(dst.NRGBAAt(x, y), px))
		}
	}
}

func hexNRGBA(hexStr string, opacity float64, fallback color.NRGBA) color.NRGBA {
	c, ok := parseHex(hexStr)
	if !ok {
		return fallback
	}
	c.A = uint8(clamp01(opacity)*255 + 0.5)
	return c
}

func parseHex(s string) (color.NRGBA, bool) {
	s = strings.TrimSpace(s)
	if len(s) == 0 || s[0] != '#' {
		return color.NRGBA{}, false
	}
	s = s[1:]
	if len(s) == 3 {
		s = string([]byte{s[0], s[0], s[1], s[1], s[2], s[2]})
	}
	if len(s) != 6 {
		return color.NRGBA{}, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return color.NRGBA{}, false
	}
	return color.NRGBA{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v), A: 255}, true
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
