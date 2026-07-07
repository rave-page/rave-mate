package ui

// Winamp-style chassis widgets: a beveled metallic panel (raised or recessed), a small
// spectrum-analyzer strip, and an LCD now-playing readout. These give the classic-Winamp
// look (brushed-metal bevels + green-LCD, here brand mint) on top of the Fyne theme.

import (
	"image/color"
	"math/rand"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/widget"
)

var (
	bevelLight = hexRGB(0x4a, 0x50, 0x5c) // raised top-left highlight (brushed metal)
	bevelDark  = hexRGB(0x0c, 0x0e, 0x12) // raised bottom-right shadow
)

// beveledPanel wraps content in a 2px metallic bevel. raised=true is a button/panel that
// pops out (light top-left, dark bottom-right); raised=false is a recessed well (e.g. the
// LCD), with the bevel inverted. fill is the panel surface colour.
type beveledPanel struct {
	widget.BaseWidget
	content fyne.CanvasObject
	fill    color.Color
	raised  bool
	pad     float32
}

func newBeveledPanel(content fyne.CanvasObject, fill color.Color, raised bool, pad float32) *beveledPanel {
	p := &beveledPanel{content: content, fill: fill, raised: raised, pad: pad}
	p.ExtendBaseWidget(p)
	return p
}

func (p *beveledPanel) CreateRenderer() fyne.WidgetRenderer {
	bg := canvas.NewRectangle(p.fill)
	top := canvas.NewRectangle(color.Black)
	left := canvas.NewRectangle(color.Black)
	bottom := canvas.NewRectangle(color.Black)
	right := canvas.NewRectangle(color.Black)
	r := &beveledRenderer{p: p, bg: bg, top: top, left: left, bottom: bottom, right: right}
	r.recolor()
	return r
}

type beveledRenderer struct {
	p                        *beveledPanel
	bg                       *canvas.Rectangle
	top, left, bottom, right *canvas.Rectangle
}

func (r *beveledRenderer) recolor() {
	lt, dk := bevelLight, bevelDark
	if !r.p.raised { // recessed well - invert
		lt, dk = bevelDark, bevelLight
	}
	r.top.FillColor, r.left.FillColor = lt, lt
	r.bottom.FillColor, r.right.FillColor = dk, dk
}

func (r *beveledRenderer) Layout(s fyne.Size) {
	const b = 2
	r.bg.Resize(s)
	r.bg.Move(fyne.NewPos(0, 0))
	r.top.Resize(fyne.NewSize(s.Width, b))
	r.top.Move(fyne.NewPos(0, 0))
	r.left.Resize(fyne.NewSize(b, s.Height))
	r.left.Move(fyne.NewPos(0, 0))
	r.bottom.Resize(fyne.NewSize(s.Width, b))
	r.bottom.Move(fyne.NewPos(0, s.Height-b))
	r.right.Resize(fyne.NewSize(b, s.Height))
	r.right.Move(fyne.NewPos(s.Width-b, 0))
	if r.p.content != nil {
		pad := r.p.pad
		r.p.content.Resize(fyne.NewSize(s.Width-2*pad, s.Height-2*pad))
		r.p.content.Move(fyne.NewPos(pad, pad))
	}
}

func (r *beveledRenderer) MinSize() fyne.Size {
	pad := r.p.pad
	if r.p.content == nil {
		return fyne.NewSize(2*pad, 2*pad)
	}
	m := r.p.content.MinSize()
	return fyne.NewSize(m.Width+2*pad, m.Height+2*pad)
}

func (r *beveledRenderer) Refresh() {
	r.bg.FillColor = r.p.fill
	r.recolor()
	canvas.Refresh(r.p)
}

func (r *beveledRenderer) Objects() []fyne.CanvasObject {
	objs := []fyne.CanvasObject{r.bg, r.top, r.left, r.bottom, r.right}
	if r.p.content != nil {
		objs = append(objs, r.p.content)
	}
	return objs
}
func (r *beveledRenderer) Destroy() {}

// ── spectrum analyzer ────────────────────────────────────────────────────────

// spectrum is a small animated bar-graph (Winamp's visualizer). It self-animates while
// shown; SetActive(false) decays the bars to rest. Not real FFT - a lively idle visual.
type spectrum struct {
	widget.BaseWidget
	bars   int
	mu     sync.Mutex
	vals   []float64
	active bool
}

func newSpectrum(bars int) *spectrum {
	s := &spectrum{bars: bars, vals: make([]float64, bars)}
	s.ExtendBaseWidget(s)
	return s
}

// SetActive toggles the lively animation (playing) vs decay-to-floor (idle).
func (s *spectrum) SetActive(a bool) {
	s.mu.Lock()
	s.active = a
	s.mu.Unlock()
}

func (s *spectrum) CreateRenderer() fyne.WidgetRenderer {
	rects := make([]*canvas.Rectangle, s.bars)
	objs := make([]fyne.CanvasObject, s.bars)
	for i := range rects {
		rects[i] = canvas.NewRectangle(colBrandMint)
		objs[i] = rects[i]
	}
	r := &spectrumRenderer{s: s, rects: rects, objs: objs, stop: make(chan struct{})}
	go r.animate()
	return r
}

type spectrumRenderer struct {
	s     *spectrum
	rects []*canvas.Rectangle
	objs  []fyne.CanvasObject
	stop  chan struct{}
}

func (r *spectrumRenderer) animate() {
	t := time.NewTicker(60 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			r.s.mu.Lock()
			active := r.s.active
			for i := range r.s.vals {
				if active {
					target := 0.15 + rand.Float64()*0.85
					r.s.vals[i] += (target - r.s.vals[i]) * 0.5
				} else {
					r.s.vals[i] *= 0.8
				}
			}
			r.s.mu.Unlock()
			fyne.Do(func() { r.Layout(r.s.Size()); canvas.Refresh(r.s) })
		}
	}
}

func (r *spectrumRenderer) Layout(sz fyne.Size) {
	n := len(r.rects)
	if n == 0 {
		return
	}
	gap := float32(2)
	bw := (sz.Width - gap*float32(n-1)) / float32(n)
	r.s.mu.Lock()
	vals := append([]float64(nil), r.s.vals...)
	r.s.mu.Unlock()
	for i, rc := range r.rects {
		h := sz.Height * float32(vals[i])
		if h < 1 {
			h = 1
		}
		// brighter (pink) near the top, mint below - VU feel
		if vals[i] > 0.8 {
			rc.FillColor = colBrandBase
		} else {
			rc.FillColor = colBrandMint
		}
		rc.Resize(fyne.NewSize(bw, h))
		rc.Move(fyne.NewPos(float32(i)*(bw+gap), sz.Height-h))
	}
}
func (r *spectrumRenderer) MinSize() fyne.Size           { return fyne.NewSize(60, 22) }
func (r *spectrumRenderer) Refresh()                     { r.Layout(r.s.Size()) }
func (r *spectrumRenderer) Objects() []fyne.CanvasObject { return r.objs }
func (r *spectrumRenderer) Destroy()                     { close(r.stop) }
