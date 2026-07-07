package ui

// Dashboard NETWORK + TIMING cards: LCD-style time graphs over the 1 Hz netstats sampler
// (peer/API byte rates, per-peer RTT). Same chassis language as the now-playing LCD -
// recessed beveled well, mint-on-dark mono readouts.

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// graphSeries is one trace: vals oldest→newest (≤ span; NaN = gap), theme color, area fill.
type graphSeries struct {
	vals []float64
	col  color.NRGBA
	fill bool
}

// netGraph is an LCD-style multi-series time graph: one sample column per tick,
// right-aligned (newest at the right edge), rastered onto the dark LCD well.
type netGraph struct {
	widget.BaseWidget
	span int

	mu     sync.Mutex
	series []graphSeries

	raster *canvas.Raster
}

func newNetGraph(span int) *netGraph {
	g := &netGraph{span: span}
	g.raster = canvas.NewRaster(g.draw)
	g.ExtendBaseWidget(g)
	return g
}

func (g *netGraph) CreateRenderer() fyne.WidgetRenderer { return widget.NewSimpleRenderer(g.raster) }

func (g *netGraph) MinSize() fyne.Size { return fyne.NewSize(160, 54) }

// SetSeries swaps the traces and repaints. Call on the Fyne thread.
func (g *netGraph) SetSeries(s []graphSeries) {
	g.mu.Lock()
	g.series = s
	g.mu.Unlock()
	g.Refresh()
}

func (g *netGraph) draw(px, py int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, px, py))
	fillRect(img, 0, 0, px, py, toNRGBA(colLCD))
	g.mu.Lock()
	series := append([]graphSeries(nil), g.series...)
	span := g.span
	g.mu.Unlock()
	if px == 0 || py == 0 || span == 0 {
		return img
	}

	// faint horizontal ruling
	grid := toNRGBA(colBorder)
	grid.A = 0x46
	fillRect(img, 0, py/3, px, py/3+1, grid)
	fillRect(img, 0, 2*py/3, px, 2*py/3+1, grid)

	// autoscale to the hottest sample across all traces (min 1 so a flat zero stays at the floor)
	maxV := 1.0
	for _, s := range series {
		for _, v := range s.vals {
			if !math.IsNaN(v) && v > maxV {
				maxV = v
			}
		}
	}
	usable := float64(py) * 0.9
	colW := float64(px) / float64(span)

	for _, s := range series {
		n := len(s.vals)
		if n == 0 {
			continue
		}
		x0 := px - int(float64(n)*colW) // right-aligned: newest sample at the right edge
		prevY := -1
		for x := maxI(x0, 0); x < px; x++ {
			idx := minI(int(float64(x-x0)/colW), n-1)
			v := s.vals[idx]
			if math.IsNaN(v) {
				prevY = -1
				continue
			}
			y := py - 1 - int(v/maxV*usable)
			if y < 0 {
				y = 0
			}
			if s.fill {
				fc := s.col
				fc.A = 0x2e
				vline(img, x, y+1, py, fc)
			}
			top, bot := y, y
			if prevY >= 0 { // connect columns → continuous line
				top, bot = minI(prevY, y), maxI(prevY, y)
			}
			vline(img, x, top, bot+2, s.col)
			prevY = y
		}
	}
	return img
}

// lcdText is a small mono LCD-style readout in c.
func lcdText(c color.Color) *canvas.Text {
	t := canvas.NewText("", c)
	t.TextStyle = fyne.TextStyle{Monospace: true}
	t.TextSize = 11
	return t
}

func lastVal(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	if v := vals[len(vals)-1]; !math.IsNaN(v) {
		return v
	}
	return 0
}

func rateStr(vals []float64) string { return humanBytes(int64(lastVal(vals))) + "/s" }

// buildNetworkContent is the Network module: peer + API up/down rate traces,
// color-keyed legend + session totals, refreshed on a 500 ms tick (the sampler
// advances 1 column/sec). nil when the sampler is absent.
func (u *UI) buildNetworkContent() fyne.CanvasObject {
	if u.svc.NetStats == nil {
		return nil
	}
	span := u.svc.NetStats.Snapshot().Span
	netG := newNetGraph(span)
	peerDown := lcdText(colBrandMint)
	peerUp := lcdText(colBrandHot)
	apiDown := lcdText(colBrandViol)
	apiUp := lcdText(colBrandAmber)
	totals := lcdText(withAlpha(colBrandMint, 0xcc))
	netLegend := container.NewHBox(
		peerDown, peerUp,
		helpIcon("PEER ↓/↑ - bytes per second over the LAN link to paired rave-mates: bridged DJ data, remote control, library/file sync. Includes the link's own framing overhead."),
		apiDown, apiUp,
		helpIcon("API ↓/↑ - bytes per second to rave.page: live-stream ingest, library sync, auth, cover art."),
	)
	totalsRow := container.NewHBox(totals,
		helpIcon("Everything moved since app start - peer + API, download (↓) and upload (↑) combined per direction."))
	well := newBeveledPanel(container.NewVBox(netLegend, netG, totalsRow), colLCD, false, 6)

	update := func() {
		snap := u.svc.NetStats.Snapshot()
		peerDown.Text = "PEER↓ " + rateStr(snap.PeerIn)
		peerUp.Text = "↑ " + rateStr(snap.PeerOut)
		apiDown.Text = "API↓ " + rateStr(snap.APIIn)
		apiUp.Text = "↑ " + rateStr(snap.APIOut)
		totals.Text = fmt.Sprintf("SESSION ↓ %s · ↑ %s",
			humanBytes(int64(snap.SessPeerIn+snap.SessAPIIn)),
			humanBytes(int64(snap.SessPeerOut+snap.SessAPIOut)))
		for _, t := range []*canvas.Text{peerDown, peerUp, apiDown, apiUp, totals} {
			canvas.Refresh(t)
		}
		netG.SetSeries([]graphSeries{
			{vals: snap.PeerIn, col: toNRGBA(colBrandMint), fill: true},
			{vals: snap.PeerOut, col: toNRGBA(colBrandHot), fill: true},
			{vals: snap.APIIn, col: toNRGBA(colBrandViol)},
			{vals: snap.APIOut, col: toNRGBA(colBrandAmber)},
		})
	}
	update()
	tick := time.NewTicker(500 * time.Millisecond)
	u.closers = append(u.closers, tick.Stop)
	goUI("dashboard-net", func() {
		for range tick.C {
			fyne.Do(update)
		}
	})
	return well
}

// buildTimingContent is the Timing module: per-peer RTT traces, latest ms labeled
// per peer. nil when the sampler is absent.
func (u *UI) buildTimingContent() fyne.CanvasObject {
	if u.svc.NetStats == nil {
		return nil
	}
	span := u.svc.NetStats.Snapshot().Span
	timG := newNetGraph(span)
	timLegend := container.NewHBox() // rebuilt per tick - the help icon lives outside it
	legendRow := container.NewBorder(nil, nil, nil,
		helpIcon("RTT = round-trip time: how long a ping to that peer takes there and back (travel time, not clock offset - offset is how far two clocks disagree). Wired LAN <~5 ms; spikes or a climbing trace = Wi-Fi trouble or a saturated link."),
		timLegend)
	hint := mutedLabel("no peers connected")
	well := newBeveledPanel(container.NewVBox(legendRow, timG, hint), colLCD, false, 6)

	palette := []color.Color{colBrandMint, colBrandHot, colBrandViol, colBrandAmber, colInfo}
	flatZero := make([]float64, span)

	update := func() {
		snap := u.svc.NetStats.Snapshot()
		series := make([]graphSeries, 0, len(snap.RTT))
		legend := make([]fyne.CanvasObject, 0, len(snap.RTT))
		for i, r := range snap.RTT {
			c := palette[i%len(palette)]
			series = append(series, graphSeries{vals: r.Ms, col: toNRGBA(c)})
			ms := "-"
			if r.Has {
				ms = fmt.Sprintf("%.1fms", r.LatestMs)
			}
			t := lcdText(c)
			t.Text = strings.ToUpper(r.Label) + " " + ms
			legend = append(legend, t)
		}
		if len(series) == 0 {
			series = []graphSeries{{vals: flatZero, col: toNRGBA(colMuted)}}
			hint.Show()
		} else {
			hint.Hide()
		}
		timG.SetSeries(series)
		timLegend.Objects = legend
		timLegend.Refresh()
	}
	update()
	tick := time.NewTicker(500 * time.Millisecond)
	u.closers = append(u.closers, tick.Stop)
	goUI("dashboard-tim", func() {
		for range tick.C {
			fyne.Do(update)
		}
	})
	return well
}
