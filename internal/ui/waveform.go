package ui

import (
	"image"
	"image/color"
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/musiclib"
)

// waveformView is the interactive player waveform: peak buckets rendered via a
// canvas.Raster with cue/loop/beatgrid overlays and a playhead. Navigation:
//
//	tap = seek · drag = pan (zoomed) · wheel = zoom at cursor · double-tap = fit.
//
// All state mutations happen on the Fyne thread (callers use fyne.Do).
type waveformView struct {
	widget.BaseWidget

	peaks  []byte  // uint8 max-abs per bucket (probe.peaks)
	durSec float64 // exact decoded duration
	cues   []musiclib.CuePoint
	grid   []musiclib.GridMarker

	viewStart float64 // window left edge (sec)
	viewDur   float64 // window width (sec); == durSec → fit
	cur       float64 // playhead (sec); <0 = hidden

	onSeek func(sec float64)

	raster   *canvas.Raster
	dragging bool
}

const minViewDur = 2.0 // max zoom: 2 s across the widget

func newWaveformView(onSeek func(sec float64)) *waveformView {
	w := &waveformView{onSeek: onSeek, cur: -1}
	w.raster = canvas.NewRaster(w.draw)
	w.ExtendBaseWidget(w)
	return w
}

func (w *waveformView) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(w.raster)
}

func (w *waveformView) MinSize() fyne.Size { return fyne.NewSize(120, 110) }

// setData loads a track's peaks + overlays and resets the view to fit.
func (w *waveformView) setData(peaks []byte, durSec float64, cues []musiclib.CuePoint, grid []musiclib.GridMarker) {
	w.peaks, w.durSec, w.cues, w.grid = peaks, durSec, cues, grid
	w.viewStart, w.viewDur = 0, durSec
	w.Refresh()
}

// setPlayhead moves the playhead; when playing past the right edge of a zoomed
// view, the window follows (playhead pinned at 25%).
func (w *waveformView) setPlayhead(sec float64) {
	w.cur = sec
	if w.zoomed() && sec >= 0 {
		if sec < w.viewStart || sec > w.viewStart+w.viewDur {
			w.viewStart = clampF(sec-w.viewDur*0.25, 0, w.durSec-w.viewDur)
		}
	}
	w.Refresh()
}

func (w *waveformView) zoomed() bool { return w.durSec > 0 && w.viewDur < w.durSec-1e-9 }

// fit resets zoom to the whole track.
func (w *waveformView) fit() {
	w.viewStart, w.viewDur = 0, w.durSec
	w.Refresh()
}

// zoomAt scales the window by factor (<1 = in) keeping the time at fraction fx
// of the width stationary.
func (w *waveformView) zoomAt(factor, fx float64) {
	if w.durSec <= 0 {
		return
	}
	anchor := w.viewStart + fx*w.viewDur
	nd := clampF(w.viewDur*factor, minViewDur, w.durSec)
	w.viewStart = clampF(anchor-fx*nd, 0, w.durSec-nd)
	w.viewDur = nd
	w.Refresh()
}

// ── interaction ───────────────────────────────────────────────────────────────

func (w *waveformView) Tapped(e *fyne.PointEvent) {
	if w.durSec <= 0 || w.onSeek == nil {
		return
	}
	width := w.Size().Width
	if width <= 0 {
		return
	}
	w.onSeek(w.viewStart + float64(e.Position.X)/float64(width)*w.viewDur)
}

func (w *waveformView) DoubleTapped(*fyne.PointEvent) { w.fit() }

func (w *waveformView) Dragged(e *fyne.DragEvent) {
	if !w.zoomed() {
		return
	}
	width := w.Size().Width
	if width <= 0 {
		return
	}
	w.dragging = true
	w.viewStart = clampF(w.viewStart-float64(e.Dragged.DX)/float64(width)*w.viewDur, 0, w.durSec-w.viewDur)
	w.Refresh()
}

func (w *waveformView) DragEnd() { w.dragging = false }

func (w *waveformView) Scrolled(e *fyne.ScrollEvent) {
	if w.durSec <= 0 {
		return
	}
	width := w.Size().Width
	if width <= 0 {
		return
	}
	factor := 0.8 // wheel up = zoom in
	if e.Scrolled.DY < 0 {
		factor = 1.25
	}
	w.zoomAt(factor, float64(e.Position.X)/float64(width))
}

func (w *waveformView) Cursor() desktop.Cursor { return desktop.PointerCursor }

// ── rendering ─────────────────────────────────────────────────────────────────

// cueKindColor maps portable cue kinds to brand accents (legend mirrors this).
func cueKindColor(k musiclib.CueKind) color.NRGBA {
	switch k {
	case musiclib.CueHot:
		return toNRGBA(colBrandBase)
	case musiclib.CueLoop:
		return toNRGBA(colBrandViol)
	case musiclib.CueLoad:
		return toNRGBA(colBrandMint)
	case musiclib.CueFade:
		return toNRGBA(colBrandAmber)
	case musiclib.CueGrid:
		return toNRGBA(colMuted)
	default: // CuePlain
		return toNRGBA(colInfo)
	}
}

// draw renders the whole view into an RGBA frame (raster regenerates on Refresh
// and on resize; px dimensions are device pixels).
func (w *waveformView) draw(px, py int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, px, py))
	bg := toNRGBA(colSurface)
	fillRect(img, 0, 0, px, py, bg)
	if len(w.peaks) == 0 || w.durSec <= 0 || px == 0 {
		return img
	}

	secPerPx := w.viewDur / float64(px)
	toX := func(sec float64) int { return int((sec - w.viewStart) / secPerPx) }
	mid := py / 2

	// beatgrid ticks first (under the wave): only when a beat is ≥ 4 device px wide
	if len(w.grid) > 0 && w.grid[0].BPM > 0 {
		beat := 60.0 / w.grid[0].BPM
		if beat/secPerPx >= 4 {
			tick := toNRGBA(colBorder)
			bar := toNRGBA(colMuted)
			anchor := w.grid[0].PositionMs / 1000
			first := math.Ceil((w.viewStart-anchor)/beat - 1e-9)
			for b := first; ; b++ {
				sec := anchor + b*beat
				if sec > w.viewStart+w.viewDur {
					break
				}
				x := toX(sec)
				if x < 0 || x >= px {
					continue
				}
				c := tick
				if int64(b)%4 == 0 {
					c = bar
				}
				vline(img, x, 0, py, c)
			}
		}
	}

	// loop regions (shaded) under the bars
	for _, c := range w.cues {
		if c.LenMs <= 0 {
			continue
		}
		x0, x1 := toX(c.StartMs/1000), toX((c.StartMs+c.LenMs)/1000)
		if x1 < 0 || x0 >= px {
			continue
		}
		col := cueKindColor(c.Kind)
		col.A = 0x30
		fillRect(img, maxI(x0, 0), 0, minI(x1, px), py, col)
	}

	// waveform bars: per device-px column, max of covered buckets; played part hot
	played := toNRGBA(colBrandBase)
	rest := toNRGBA(colForeground)
	rest.A = 0x52
	bucketDur := w.durSec / float64(len(w.peaks))
	curX := -1
	if w.cur >= 0 {
		curX = toX(w.cur)
	}
	for x := range px {
		t0 := w.viewStart + float64(x)*secPerPx
		b0 := int(t0 / bucketDur)
		b1 := int((t0 + secPerPx) / bucketDur)
		if b0 < 0 {
			b0 = 0
		}
		if b1 < b0 {
			b1 = b0
		}
		if b0 >= len(w.peaks) {
			break
		}
		if b1 >= len(w.peaks) {
			b1 = len(w.peaks) - 1
		}
		peak := byte(0)
		for b := b0; b <= b1; b++ {
			if w.peaks[b] > peak {
				peak = w.peaks[b]
			}
		}
		h := int(float64(peak) / 255 * float64(mid-2))
		if peak > 0 && h == 0 {
			h = 1
		}
		col := rest
		if curX >= 0 && x <= curX {
			col = played
		}
		vline(img, x, mid-h, mid+h+1, col)
	}

	// center line
	cl := toNRGBA(colMuted)
	cl.A = 0x40
	for x := range px {
		img.SetNRGBA(x, mid, cl)
	}

	// cue markers (over the bars): 2px line + a 6px head flag, kind-colored
	scale := float64(px) / math.Max(float64(w.Size().Width), 1) // device px per dip
	for _, c := range w.cues {
		if c.Kind == musiclib.CueGrid && w.viewDur > 120 {
			continue // grid anchors only matter zoomed in
		}
		x := toX(c.StartMs / 1000)
		if x < 0 || x >= px {
			continue
		}
		col := cueKindColor(c.Kind)
		lw := maxI(int(scale), 1)
		for dx := 0; dx < 2*lw && x+dx < px; dx++ {
			vline(img, x+dx, 0, py, col)
		}
		head := minI(6*lw, px-x)
		fillRect(img, x, 0, x+head, minI(6*lw, py), col)
	}

	// playhead
	if curX >= 0 && curX < px {
		ph := toNRGBA(colForeground)
		lw := maxI(int(scale), 1)
		for dx := 0; dx < lw && curX+dx < px; dx++ {
			vline(img, curX+dx, 0, py, ph)
		}
	}
	return img
}

// ── tiny draw helpers ─────────────────────────────────────────────────────────

func fillRect(img *image.NRGBA, x0, y0, x1, y1 int, c color.NRGBA) {
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	b := img.Bounds()
	if x1 > b.Max.X {
		x1 = b.Max.X
	}
	if y1 > b.Max.Y {
		y1 = b.Max.Y
	}
	if c.A == 0xff {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				img.SetNRGBA(x, y, c)
			}
		}
		return
	}
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetNRGBA(x, y, blend(img.NRGBAAt(x, y), c))
		}
	}
}

func vline(img *image.NRGBA, x, y0, y1 int, c color.NRGBA) {
	b := img.Bounds()
	if x < 0 || x >= b.Max.X {
		return
	}
	if y0 < 0 {
		y0 = 0
	}
	if y1 > b.Max.Y {
		y1 = b.Max.Y
	}
	if c.A == 0xff {
		for y := y0; y < y1; y++ {
			img.SetNRGBA(x, y, c)
		}
		return
	}
	for y := y0; y < y1; y++ {
		img.SetNRGBA(x, y, blend(img.NRGBAAt(x, y), c))
	}
}

// blend does src-over alpha compositing of fg onto bg.
func blend(bg, fg color.NRGBA) color.NRGBA {
	a := int(fg.A)
	ia := 255 - a
	return color.NRGBA{
		R: uint8((int(fg.R)*a + int(bg.R)*ia) / 255),
		G: uint8((int(fg.G)*a + int(bg.G)*ia) / 255),
		B: uint8((int(fg.B)*a + int(bg.B)*ia) / 255),
		A: 0xff,
	}
}

func toNRGBA(c color.Color) color.NRGBA {
	r, g, b, a := c.RGBA()
	return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}

func clampF(v, lo, hi float64) float64 {
	if hi < lo {
		hi = lo
	}
	return math.Min(math.Max(v, lo), hi)
}

func maxI(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}
