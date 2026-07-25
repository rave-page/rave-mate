package vroverlay

import (
	"bytes"
	_ "embed"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"sync/atomic"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"rave.page/mate/internal/zigvr"
)

//go:embed assets/Orbitron-Bold.ttf
var orbBoldTTF []byte

//go:embed assets/Orbitron-Medium.ttf
var orbMedTTF []byte

//go:embed assets/icon.png
var iconPNG []byte

// logo is the decoded rave-mate app icon (nil if decode fails).
var logo *image.NRGBA

func init() {
	if img, err := png.Decode(bytes.NewReader(iconPNG)); err == nil {
		b := img.Bounds()
		n := image.NewNRGBA(b)
		draw.Draw(n, b, img, b.Min, draw.Src)
		logo = n
	}
}

// drawScaled blits src into dst's x,y,w,h rect (high-quality scale, alpha-blended).
func drawScaled(dst, src *image.NRGBA, x, y, w, h int) {
	xdraw.CatmullRom.Scale(dst, image.Rect(x, y, x+w, y+h), src, src.Bounds(), xdraw.Over, nil)
}

// brand colours (mirrors internal/ui/theme.go; duplicated to avoid a UI import cycle).
var (
	colPanelBG = color.NRGBA{R: 10, G: 10, B: 14, A: 210}
	colText    = color.NRGBA{R: 250, G: 250, B: 250, A: 255}
	colName    = color.NRGBA{R: 0xF7, G: 0x08, B: 0x64, A: 255}
)

// Line is one renderable line: a chat message (Name set) or an alert (Name empty, Color = line).
type Line struct {
	Name  string
	Text  string
	Color color.Color
}

// Renderer holds the parsed faces; build one per Manager (font.Face is stateful).
type Renderer struct {
	name font.Face // Orbitron-Bold (names / alerts)
	body font.Face // Orbitron-Medium (message text)
	lh   int       // line height px

	// canvas recycles the hot-path raster buffers (menu ~1MB, panels 1.2MB) - a fresh
	// alloc per content change was pure GC churn. Only RenderMenu/Panel use it: their
	// output is uploaded synchronously (SetOverlayRaw copies) and never retained, and
	// every render fully overwrites the buffer (bg fill first). Cap: canvasCacheMax.
	canvas map[[2]int]*image.NRGBA

	// zig dispatches the renders to the ravevr Zig raster lib when linked (-tags zigvr):
	// Panel/RenderMenu/RenderStats plus the small editor textures (hover row, ghost,
	// tooltip, wrist, strip(+hover), outline, path orbit). The direct Go path stays the
	// golden reference + fallback. dl is the reused per-frame display list (VR goroutine only).
	zig bool
	dl  *zigvr.List

	// Dispatch tally (atomic - PerfProbe reads it off the VR goroutine). A zigvr build stuck
	// at zigOK=0 / a climbing zigFB means the display list is being rejected and every render
	// silently costs the Go path: the zigui lesson (a whole tab rendered from Go for weeks).
	zigOK, zigFB atomic.Uint64
}

// canvasCacheMax bounds the canvas cache (menu + posmenu + panel sizes; a handful live).
const canvasCacheMax = 8

// canvasFor returns a fully-reusable w×h canvas. The caller must overwrite every pixel
// and upload before the next canvasFor call with the same dims.
func (r *Renderer) canvasFor(w, h int) *image.NRGBA {
	if img, ok := r.canvas[[2]int{w, h}]; ok {
		return img
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	if len(r.canvas) >= canvasCacheMax {
		for k := range r.canvas { // drop an arbitrary entry - sizes churn only on config changes
			delete(r.canvas, k)
			break
		}
	}
	r.canvas[[2]int{w, h}] = img
	return img
}

// NewRenderer parses the embedded faces at the given scale (1 = default).
func NewRenderer(scale float64) (*Renderer, error) {
	if scale <= 0 {
		scale = 1
	}
	mk := func(ttf []byte, pt float64) (font.Face, error) {
		p, err := opentype.Parse(ttf)
		if err != nil {
			return nil, err
		}
		return opentype.NewFace(p, &opentype.FaceOptions{Size: pt * scale, DPI: 72, Hinting: font.HintingFull})
	}
	nm, err := mk(orbBoldTTF, 16)
	if err != nil {
		return nil, err
	}
	bd, err := mk(orbMedTTF, 15)
	if err != nil {
		return nil, err
	}
	return &Renderer{name: nm, body: bd, lh: int(22 * scale), canvas: map[[2]int]*image.NRGBA{},
		zig: zigvr.Available()}, nil
}

// scaleA returns c with its alpha scaled by f (0..1) - lets the panel/menu background fade
// independently of the (opaque) text drawn on top.
func scaleA(c color.NRGBA, f float64) color.NRGBA {
	if f < 0 {
		f = 0
	} else if f > 1 {
		f = 1
	}
	c.A = uint8(float64(c.A) * f)
	return c
}

// Panel renders lines (oldest first) bottom-aligned into a w×h RGBA panel. bgAlpha (0..1) scales the
// translucent background only - text stays fully opaque.
func (r *Renderer) Panel(lines []Line, w, h int, bgAlpha float64) *image.NRGBA {
	img := r.canvasFor(w, h)
	r.paintInto(img, func(p *paint) { r.panelPaint(p, lines, w, h, bgAlpha) })
	return img
}

// panelPaint draws the panel into a paint target (direct Go or zigvr display list).
func (r *Renderer) panelPaint(p *paint, lines []Line, w, h int, bgAlpha float64) {
	p.fillSrc(0, 0, w, h, scaleA(colPanelBG, bgAlpha))

	pad := 12
	maxW := w - 2*pad
	// Wrap into visual rows from the newest backwards until the panel fills.
	type row struct {
		text string
		col  color.Color
		face font.Face
		ind  int // indentation px (wrapped continuation)
	}
	var rows []row
	for _, ln := range lines {
		if ln.Name != "" {
			prefix := ln.Name + ": "
			col := ln.Color
			if col == nil {
				col = colName
			}
			pw := textWidth(r.name, prefix)
			// first visual row carries the name; wrap the message after it.
			wrapped := wrapText(r.body, ln.Text, maxW-pw)
			for i, seg := range wrapped {
				if i == 0 {
					rows = append(rows, row{text: prefix + seg, col: col, face: r.name, ind: 0})
				} else {
					rows = append(rows, row{text: seg, col: colText, face: r.body, ind: pw})
				}
			}
			if len(wrapped) == 0 {
				rows = append(rows, row{text: prefix, col: col, face: r.name})
			}
		} else {
			for _, seg := range wrapText(r.name, ln.Text, maxW) {
				col := ln.Color
				if col == nil {
					col = colText
				}
				rows = append(rows, row{text: seg, col: col, face: r.name})
			}
		}
	}
	// Bottom-align: draw only the rows that fit, newest at the bottom.
	maxRows := max((h-2*pad)/r.lh, 1)
	if len(rows) > maxRows {
		rows = rows[len(rows)-maxRows:]
	}
	y := h - pad - (len(rows)-1)*r.lh
	for _, rw := range rows {
		p.text(rw.face, rw.text, pad+rw.ind, y, rw.col)
		y += r.lh
	}
}

// Menu layout (pixels). Rows are full-width, drawn top-down; row 0 is the title.
const (
	MenuW    = 420
	MenuRowH = 56
)

// MenuItemKind distinguishes a non-interactive header from a clickable action or a drag/click slider.
type MenuItemKind int

const (
	MIHeader MenuItemKind = iota
	MIAction
	MISlider
)

// MenuItem is one menu row. Headers are labels only; actions fire OnClick; sliders show Frac (0..1)
// + Value text and fire OnSet(frac) when the laser clicks along the track.
type MenuItem struct {
	Kind    MenuItemKind
	Label   string
	Value   string
	Frac    float64
	OnClick func()
	OnSet   func(frac float64)
}

var (
	colMenuBG  = color.NRGBA{R: 12, G: 12, B: 16, A: 240}
	colHeadBG  = color.NRGBA{R: 24, G: 18, B: 28, A: 255}
	colRow     = color.NRGBA{R: 20, G: 20, B: 26, A: 255}
	colTrack   = color.NRGBA{R: 44, G: 44, B: 54, A: 255}
	colSep     = color.NRGBA{R: 40, G: 40, B: 48, A: 255}
	colMuteTxt = color.NRGBA{R: 150, G: 150, B: 160, A: 255}
	colRowHi   = color.NRGBA{R: 0x3a, G: 0x0e, B: 0x24, A: 255} // ray-pointer hovered row tint (brand)
)

// RenderMenu draws a titled menu of items. bgAlpha (0..1) scales the panel/row backgrounds only -
// text + slider fills stay opaque. Item i occupies top-left y in [(i+1)*MenuRowH, (i+2)*MenuRowH).
// Hover is NOT baked here - it's a separate tiny overlay (RenderHoverRow + editor.driveHover), so a
// hover move never re-uploads this whole texture (~2.5MB GPU churn + visible flicker per row change).
// padRows fixes the canvas height at max(len(items), padRows) rows: a constant texture size across
// pages means SetTexture never destroy+recreates the overlay on navigation (the menu blink). The pad
// zone below the rows is menu background - inert (row math bounds-checks past len(items)).
func (r *Renderer) RenderMenu(title string, items []MenuItem, bgAlpha float64, padRows int) *image.NRGBA {
	rows := max(len(items), padRows)
	h := MenuRowH * (rows + 1)
	img := r.canvasFor(MenuW, h)
	r.paintInto(img, func(p *paint) { r.menuPaint(p, title, items, bgAlpha, h) })
	return img
}

// menuPaint draws the menu into a paint target (direct Go or zigvr display list).
func (r *Renderer) menuPaint(p *paint, title string, items []MenuItem, bgAlpha float64, h int) {
	headBG, rowBG := scaleA(colHeadBG, bgAlpha), scaleA(colRow, bgAlpha)
	p.fillSrc(0, 0, MenuW, h, scaleA(colMenuBG, bgAlpha))
	// Title bar.
	p.fillOver(0, 0, MenuW, MenuRowH, headBG)
	p.text(r.name, truncText(r.name, title, MenuW-32), 16, 34, colName)
	for i, it := range items {
		top := (i + 1) * MenuRowH
		rb := rowBG
		switch it.Kind {
		case MIHeader:
			p.fillOver(0, top, MenuW, MenuRowH, headBG)
			p.text(r.name, truncText(r.name, it.Label, MenuW-32), 16, top+34, colMuteTxt)
		case MIAction:
			p.fillOver(0, top+1, MenuW, MenuRowH-1, rb)
			vw := 0
			if it.Value != "" {
				vw = textWidth(r.body, truncText(r.body, it.Value, MenuW/2))
			}
			p.text(r.body, truncText(r.body, it.Label, MenuW-44-vw), 16, top+34, colText)
			if it.Value != "" {
				v := truncText(r.body, it.Value, MenuW/2)
				p.text(r.body, v, MenuW-textWidth(r.body, v)-16, top+34, colName)
			} else {
				p.text(r.body, "›", MenuW-26, top+34, colMuteTxt)
			}
		case MISlider:
			p.fillOver(0, top+1, MenuW, MenuRowH-1, rb)
			vw := textWidth(r.body, truncText(r.body, it.Value, MenuW/2))
			p.text(r.body, truncText(r.body, it.Label, MenuW-44-vw), 16, top+24, colText)
			vs := truncText(r.body, it.Value, MenuW/2)
			p.text(r.body, vs, MenuW-textWidth(r.body, vs)-16, top+24, colName)
			// track + fill
			tx0, tx1, ty := 16, MenuW-16, top+40
			p.fillOver(tx0, ty, tx1-tx0, 8, colTrack)
			fw := int(float64(tx1-tx0) * clampFrac(it.Frac))
			p.fillOver(tx0, ty, fw, 8, colName)
		}
		p.set(0, top, MenuW, 1, colSep) // separator
	}
}

// RenderHoverRow draws the ray-pointer row highlight as its OWN small texture (MenuW×MenuRowH):
// translucent brand tint + opaque left accent bar. Rendered once; editor.driveHover floats it over
// the hovered row, so hover moves cost a transform update instead of a full menu re-upload.
func (r *Renderer) RenderHoverRow() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, MenuW, MenuRowH))
	tint := colRowHi
	tint.A = 96 // translucent - the row's text/slider stays readable underneath
	r.paintInto(img, func(p *paint) {
		p.fillOver(0, 1, MenuW, MenuRowH-1, tint)
		p.fillOver(0, 1, 4, MenuRowH-1, colName) // left accent bar
	})
	return img
}

// RenderGhost draws a plain translucent placeholder (brand outline + "PREVIEW" label) - used for the
// menu-placement preview so it reads as a ghost, NOT a second usable menu.
// RenderGhost draws the menu-placement preview at the SAME pixel dimensions as the real menu (rows =
// the live menu's item count) so, shown at the same WidthM, it occupies exactly the menu's footprint -
// but as a translucent ghost (no UI rows) so it reads as a placeholder, not a second menu.
func (r *Renderer) RenderGhost(rows int) *image.NRGBA {
	if rows < 3 {
		rows = 3
	}
	w, h := MenuW, MenuRowH*(rows+1)
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	r.paintInto(img, func(p *paint) {
		p.fillSrc(0, 0, w, h, color.NRGBA{R: 0x2a, G: 0x06, B: 0x18, A: 90})
		p.border(4, colName)
		p.text(r.name, "PREVIEW", 16, h/2-6, colName)
		p.text(r.body, "release here -> Apply", 16, h/2+22, colMuteTxt)
	})
	return img
}

// TooltipW is the hover-tooltip panel width (px); height grows with the wrapped text.
const TooltipW = 380

// RenderTooltip draws a truncated menu row's full text, word-wrapped, in a small brand-bordered
// panel (shown beside the menu on laser hover).
func (r *Renderer) RenderTooltip(text string) *image.NRGBA {
	const pad = 14
	lines := wrapText(r.body, text, TooltipW-2*pad)
	if len(lines) == 0 {
		lines = []string{text}
	}
	h := 2*pad + len(lines)*r.lh
	img := image.NewNRGBA(image.Rect(0, 0, TooltipW, h))
	r.paintInto(img, func(p *paint) {
		p.fillSrc(0, 0, TooltipW, h, color.NRGBA{R: 10, G: 10, B: 14, A: 245})
		p.border(3, colName)
		y := pad + r.lh - 6
		for _, ln := range lines {
			p.text(r.body, ln, pad, y, colText)
			y += r.lh
		}
	})
	return img
}

// WristW is the wrist toggle button's pixel size (square logo).
const WristW = 160

// RenderWrist draws the rave-mate logo as the wrist edit-toggle button (brand ring when edit on;
// mint ring when the ray pointer is hovering it, so it reads as an aim target even with editor closed).
func (r *Renderer) RenderWrist(on, hover bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, WristW, WristW))
	bg := color.NRGBA{R: 10, G: 10, B: 14, A: 200}
	if on {
		bg = color.NRGBA{R: 0x2a, G: 0x06, B: 0x18, A: 230}
	}
	// Two dispatched stages around the logo blit: drawScaled is x/image's float64 CatmullRom
	// resampler fused with the Over blend (NOT display-list expressible — see ZIG_VR_OVERLAY.md),
	// so it stays Go-side between them. Each stage flushes before the next → same draw order.
	r.paintInto(img, func(p *paint) {
		p.fillSrc(0, 0, WristW, WristW, bg)
		if logo == nil {
			p.text(r.name, "RM", 40, WristW/2+10, colText)
		}
	})
	if logo != nil {
		drawScaled(img, logo, 16, 16, WristW-32, WristW-32) // inset logo
	}
	r.paintInto(img, func(p *paint) {
		if on {
			p.border(6, colName)
		}
		if hover {
			p.border(4, colMint) // pointer-hover ring (drawn inside the on-ring)
		}
	})
	return img
}

// StripCellPx is the wrist-strip icon-button cell size (square, px).
const StripCellPx = 96

// RenderStrip draws the wrist icon strip: one square glyph cell per button, XSOverlay-style.
// Active buttons get a brand-tinted bg + accent border. Hover is NOT baked (separate overlay,
// RenderStripHover + driveStripHover) so hover moves never re-upload this texture.
func (r *Renderer) RenderStrip(btns []StripButton) *image.NRGBA {
	n := max(len(btns), 1)
	img := image.NewNRGBA(image.Rect(0, 0, StripCellPx*n, StripCellPx))
	r.paintInto(img, func(p *paint) {
		p.fillSrc(0, 0, StripCellPx*n, StripCellPx, colMenuBG)
		for i, b := range btns {
			x := i * StripCellPx
			bg := colRow
			if b.Active {
				bg = colRowHi
			}
			p.fillOver(x+3, 3, StripCellPx-6, StripCellPx-6, bg)
			if b.Active { // accent frame on the cell inset
				p.fillOver(x+3, 3, StripCellPx-6, 3, colName)
				p.fillOver(x+3, StripCellPx-6, StripCellPx-6, 3, colName)
				p.fillOver(x+3, 3, 3, StripCellPx-6, colName)
				p.fillOver(x+StripCellPx-6, 3, 3, StripCellPx-6, colName)
			}
			g := truncText(r.name, b.Glyph, StripCellPx-12)
			p.text(r.name, g, x+(StripCellPx-textWidth(r.name, g))/2, StripCellPx/2+6, colText)
		}
	})
	return img
}

// RenderStripHover is the wrist-strip hover highlight (one cell): translucent brand tint +
// mint ring, floated over the pointed cell by driveStripHover (transform-only hover updates).
func (r *Renderer) RenderStripHover() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, StripCellPx, StripCellPx))
	tint := colRowHi
	tint.A = 96
	r.paintInto(img, func(p *paint) {
		p.fillOver(0, 0, StripCellPx, StripCellPx, tint)
		p.border(4, colMint)
	})
	return img
}

// DotW is the ray-cursor dot texture size (square, transparent bg).
const DotW = 64

// RenderDot draws a soft filled brand-pink circle on transparent - the XSOverlay-style ray cursor
// placed at the ray→overlay hit point (billboarded to the HMD).
// NOT zigvr-dispatched: every pixel has its own alpha (soft edge) so there are no runs to record -
// a display list would be 1 op/px with the float math still Go-side (no win). 64×64, uploaded once
// per session. See ZIG_VR_OVERLAY.md "Skips".
func (r *Renderer) RenderDot() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, DotW, DotW))
	c := float64(DotW) / 2
	rad := c - 4
	for y := 0; y < DotW; y++ {
		for x := 0; x < DotW; x++ {
			dx, dy := float64(x)+0.5-c, float64(y)+0.5-c
			d := math.Sqrt(dx*dx + dy*dy)
			if d > rad {
				continue
			}
			a := 1.0
			if e := rad - d; e < 6 { // soft edge over the outer 6px
				a = float64(e) / 6
			}
			img.SetNRGBA(x, y, scaleA(colName, a))
		}
	}
	return img
}

// OutlineW/H are the edit-mode selection-frame texture dims. Aspect matches the content-overlay quads
// (panelW×panelH = 4:3), so at the overlay's own WidthM the frame lands exactly on its edges.
const (
	OutlineW = 320
	OutlineH = 240
)

// RenderOutline draws a hollow selection frame on transparent, placed on a content overlay's quad in
// edit mode: brand (colName) = the SELECTED overlay, mint (colMint) = the pointer-hovered "selectable".
func (r *Renderer) RenderOutline(col color.Color) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, OutlineW, OutlineH))
	r.borderInto(img, col, 6)
	return img
}

// borderInto draws a w-px border via the zigvr path when linked. Ops-only render: every other
// pixel of img is untouched (used to stamp the edit-mode frame onto an already-rendered panel).
func (r *Renderer) borderInto(img *image.NRGBA, col color.Color, w int) {
	r.paintInto(img, func(p *paint) { p.border(w, col) })
}

func fillRect(img *image.NRGBA, x, y, w, h int, c color.Color) {
	draw.Draw(img, image.Rect(x, y, x+w, y+h), image.NewUniform(c), image.Point{}, draw.Over)
}

func clampFrac(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// Border draws a w-px border around the image in col (selection highlight).
func Border(img *image.NRGBA, col color.Color, w int) {
	b := img.Bounds()
	for i := 0; i < w; i++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			img.Set(x, b.Min.Y+i, col)
			img.Set(x, b.Max.Y-1-i, col)
		}
		for y := b.Min.Y; y < b.Max.Y; y++ {
			img.Set(b.Min.X+i, y, col)
			img.Set(b.Max.X-1-i, y, col)
		}
	}
}

// SelColor is the selection-border colour (brand base).
var SelColor = colName

// Close releases the faces.
func (r *Renderer) Close() {
	if c, ok := r.name.(interface{ Close() error }); ok {
		_ = c.Close()
	}
	if c, ok := r.body.(interface{ Close() error }); ok {
		_ = c.Close()
	}
}

func drawText(dst *image.NRGBA, f font.Face, s string, x, baseline int, c color.Color) {
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: f, Dot: fixed.P(x, baseline)}
	d.DrawString(s)
}

func textWidth(f font.Face, s string) int { return font.MeasureString(f, s).Ceil() }

// truncText trims s with a ".." suffix so it fits maxW px (prevents menu rows overflowing the panel).
func truncText(f font.Face, s string, maxW int) string {
	if maxW <= 0 || textWidth(f, s) <= maxW {
		return s
	}
	const ell = ".."
	ew := textWidth(f, ell)
	r := []rune(s)
	for len(r) > 0 {
		r = r[:len(r)-1]
		if textWidth(f, string(r))+ew <= maxW {
			return string(r) + ell
		}
	}
	return ell
}

// wrapText wraps s to maxW px using f, BALANCED: greedy wrap fixes the line count, then
// the width is tightened (12 × ~4% steps) to the narrowest that keeps that count - even
// line lengths, no orphaned short last line. Returns visual segments (never empty for
// non-empty s).
func wrapText(f font.Face, s string, maxW int) []string {
	if s == "" {
		return nil
	}
	if maxW < 1 {
		maxW = 1
	}
	lines := greedyWrapPx(f, s, maxW)
	if len(lines) < 2 {
		return lines
	}
	for w := maxW; w > maxW/2; w = w * 24 / 25 {
		cand := greedyWrapPx(f, s, w)
		if len(cand) != len(lines) {
			break
		}
		lines = cand
	}
	return lines
}

// greedyWrapPx is the plain first-fit wrap (an overlong word gets its own line).
func greedyWrapPx(f font.Face, s string, maxW int) []string {
	var out []string
	var cur string
	for _, word := range splitWords(s) {
		try := word
		if cur != "" {
			try = cur + " " + word
		}
		if textWidth(f, try) <= maxW || cur == "" {
			cur = try
		} else {
			out = append(out, cur)
			cur = word
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func splitWords(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
