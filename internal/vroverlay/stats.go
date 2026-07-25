package vroverlay

// Live-stats overlay kinds: activatable in-headset panels showing this instance's real-time health.
//   perf    - app+system CPU%, RAM used/free, VR fps/frametime/GPU/reprojection
//   network - peer/API byte rates (graph) + session totals
//   timing  - per-peer RTT trend (graph) + latest ms
// Same lifecycle as the chat/alerts overlays (config entry, add-menu toggle, grab/move/resize,
// layout persistence). Rendered into the SAME fixed panelW×panelH canvas so a resize never changes
// texture dimensions (which would force overlay recreation). The view build + texture re-render is
// throttled to statsThrottle (SetTexture is expensive) and gated by a content signature.

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"
	"time"

	"rave.page/mate/internal/config"
	"rave.page/mate/internal/perfmon"
)

// Stats overlay type keys (config.VROverlay.Type).
const (
	typePerf    = "perf"
	typeNetwork = "network"
	typeTiming  = "timing"
)

// statsThrottle caps the stats view rebuild + texture re-render to ~2 Hz.
const statsThrottle = 500 * time.Millisecond

// statsGraphSpan caps how many trailing samples a stats graph draws (perfmon keeps ~10 min @1 Hz).
const statsGraphSpan = 120

// isStatsType reports whether an overlay type is a live-stats panel (perf/network/timing).
func isStatsType(t string) bool { return t == typePerf || t == typeNetwork || t == typeTiming }

// IsStatsKind is the exported form (featurehost proxy gates stats pushes on it).
func IsStatsKind(t string) bool { return isStatsType(t) }

var (
	colViolet    = color.NRGBA{R: 124, G: 58, B: 237, A: 255}
	colAmber     = color.NRGBA{R: 255, G: 181, B: 71, A: 255}
	statsPalette = []color.NRGBA{colMint, colHot, colViolet, colAmber, colName}
)

// statsRow is one label→value readout (value drawn right-aligned in col).
type statsRow struct {
	label, value string
	col          color.Color
}

// statsSeries is one graph trace: vals oldest→newest (NaN = gap), theme colour, optional area fill.
type statsSeries struct {
	label string
	vals  []float64
	col   color.NRGBA
	fill  bool
}

// statsView is a rendered stats panel: title + readout rows + optional graph traces + a "?" education
// footer explaining the key metric.
type statsView struct {
	title  string
	rows   []statsRow
	graph  []statsSeries
	footer string
}

// sig is a cheap change key: equal sig → identical pixels → skip the GPU texture upload. Graph traces
// contribute len + last (rounded) value, so a flat idle trace never re-uploads while a moving one does.
func (v statsView) sig() string {
	var b strings.Builder
	b.WriteString(v.title)
	b.WriteByte('\n')
	for _, r := range v.rows {
		b.WriteString(r.label)
		b.WriteByte('=')
		b.WriteString(r.value)
		b.WriteByte('\n')
	}
	for _, s := range v.graph {
		fmt.Fprintf(&b, "g:%s:%d:", s.label, len(s.vals))
		if n := len(s.vals); n > 0 {
			if last := s.vals[n-1]; math.IsNaN(last) {
				b.WriteString("nan")
			} else {
				fmt.Fprintf(&b, "%.0f", last)
			}
		}
		b.WriteByte('\n')
	}
	b.WriteString(v.footer)
	return b.String()
}

// renderStatsTexture rebuilds + uploads a stats overlay's texture (throttled to ~2 Hz, gated by the
// content signature). Same edit/grab border as content overlays. Runs on the VR goroutine (tick).
func (m *Manager) renderStatsTexture(o config.VROverlay, key string, rtErr func(error)) {
	now := time.Now()
	if next, ok := m.statsNext[key]; ok && now.Before(next) && m.sig[key] != "" {
		return // still fresh - last texture stands
	}
	m.statsNext[key] = now.Add(statsThrottle)
	sv := m.statsViewFor(o.Type)
	bg := o.ResolvedBgOpacity()
	outline := m.edit != nil && m.edit.editMode
	grabbed := m.edit != nil && m.edit.isGrabbing(key)
	s := sv.sig() + fmt.Sprintf("|bg%.2f|edit%v|grab%v", bg, outline, grabbed)
	if m.sig[key] == s {
		return
	}
	img := m.rend.RenderStats(sv, panelW, panelH, bg)
	m.editBorder(img, key)
	err := m.rt.SetTexture(key, img)
	rtErr(err)
	if err == nil {
		m.sig[key] = s
		m.mu.Lock()
		m.texUploads++
		m.mu.Unlock()
		m.perfC.texTotal.Add(1)
	}
}

// editBorder outlines a content/stats texture in edit mode (mint = grabbed, brand = editable) so the
// user sees edit mode is live. Shared by the panel + stats render paths.
func (m *Manager) editBorder(img *image.NRGBA, key string) {
	switch {
	case m.edit != nil && m.edit.isGrabbing(key):
		m.rend.borderInto(img, colMint, 10)
	case m.edit != nil && m.edit.editMode:
		m.rend.borderInto(img, colName, 6)
	}
}

// statsViewFor builds the view for a stats overlay type.
func (m *Manager) statsViewFor(typ string) statsView {
	switch typ {
	case typeNetwork:
		return m.networkView()
	case typeTiming:
		return m.timingView()
	default:
		return m.perfView()
	}
}

// perfView: app+system CPU/RAM (perfmon ring) + local VR compositor frame timing (runtime).
func (m *Manager) perfView() statsView {
	v := statsView{title: "PERFORMANCE",
		footer: "reproj = frames re-shown to hold your headset's refresh rate; a sustained count above 0 means the GPU can't keep up."}
	var appCPU, sysCPU []float64
	if m.statsPerf != nil {
		if s := m.statsPerf(); len(s) > 0 {
			s = lastSamples(s, statsGraphSpan)
			last := s[len(s)-1]
			v.rows = append(v.rows,
				statsRow{"App CPU", fmt.Sprintf("%.0f%%", last.CPUPct), pctColor(last.CPUPct, 100)},
				statsRow{"App RAM", fmt.Sprintf("%.0f MB", last.RSSMB), colText},
			)
			appCPU = mapSamples(s, func(x perfmon.Sample) float64 { return x.CPUPct })
			if last.SysOK {
				free := (last.SysMemTotalMB - last.SysMemUsedMB) / 1024
				v.rows = append(v.rows,
					statsRow{"System CPU", fmt.Sprintf("%.0f%%", last.SysCPUPct), pctColor(last.SysCPUPct, 100)},
					statsRow{"System RAM", fmt.Sprintf("%.1f / %.1f GB (free %.1f GB)",
						last.SysMemUsedMB/1024, last.SysMemTotalMB/1024, free),
						memColor(last.SysMemUsedMB, last.SysMemTotalMB)},
				)
				sysCPU = mapSamples(s, func(x perfmon.Sample) float64 {
					if x.SysOK {
						return x.SysCPUPct
					}
					return math.NaN()
				})
			}
		}
	}
	if len(v.rows) == 0 {
		v.rows = append(v.rows, statsRow{"CPU / RAM", "waiting for data", colMuted})
	}
	if ps, ok := m.rt.PerfStats(); ok && ps.Connected {
		v.rows = append(v.rows,
			statsRow{"VR frame rate", fmt.Sprintf("%.0f / %.0f fps", ps.FPS, ps.DisplayHz), fpsColor(ps.FPS, ps.DisplayHz)},
			statsRow{"Frame time", fmt.Sprintf("%.1f ms", ps.FrameMs), colText},
			statsRow{"GPU time", fmt.Sprintf("%.1f ms", ps.GpuMs), colText},
			statsRow{"Reprojection", fmt.Sprintf("%d reproj / %d dropped", ps.Reprojected, ps.Dropped), reprojColor(ps.Reprojected, ps.Dropped)},
		)
	} else {
		v.rows = append(v.rows, statsRow{"VR compositor", "not connected", colMuted})
	}
	if len(appCPU) > 0 {
		v.graph = append(v.graph, statsSeries{label: "app cpu%", vals: appCPU, col: colMint, fill: true})
	}
	if len(sysCPU) > 0 {
		v.graph = append(v.graph, statsSeries{label: "sys cpu%", vals: sysCPU, col: colViolet})
	}
	return v
}

// networkView: peer/API byte-rate traces + session totals (netstats sampler).
func (m *Manager) networkView() statsView {
	v := statsView{title: "NETWORK",
		footer: "rates are bytes/sec. 'peer' = the LAN link to a paired instance; 'api' = rave.page over the internet."}
	if m.statsNet == nil {
		v.rows = append(v.rows, statsRow{"Network", "waiting for data", colMuted})
		return v
	}
	snap := m.statsNet()
	v.rows = append(v.rows,
		statsRow{"Peer down", byteRate(lastF(snap.PeerIn)), colMint},
		statsRow{"Peer up", byteRate(lastF(snap.PeerOut)), colHot},
		statsRow{"API down", byteRate(lastF(snap.APIIn)), colViolet},
		statsRow{"API up", byteRate(lastF(snap.APIOut)), colAmber},
		statsRow{"Session", fmt.Sprintf("down %s / up %s",
			humanBytes(snap.SessPeerIn+snap.SessAPIIn), humanBytes(snap.SessPeerOut+snap.SessAPIOut)), colText},
	)
	v.graph = []statsSeries{
		{label: "peer dn", vals: snap.PeerIn, col: colMint, fill: true},
		{label: "peer up", vals: snap.PeerOut, col: colHot, fill: true},
		{label: "api dn", vals: snap.APIIn, col: colViolet},
		{label: "api up", vals: snap.APIOut, col: colAmber},
	}
	return v
}

// timingView: per-peer RTT trend (netstats sampler). RTT = ping travel time to a paired instance, not
// clock offset (offset isn't collected - the link is round-trip-timed only).
func (m *Manager) timingView() statsView {
	v := statsView{title: "TIMING",
		footer: "RTT = round-trip ping time to a paired instance; spikes or a climbing trend mean Wi-Fi trouble or a saturated link."}
	if m.statsNet == nil {
		v.rows = append(v.rows, statsRow{"Timing", "waiting for data", colMuted})
		return v
	}
	snap := m.statsNet()
	if len(snap.RTT) == 0 {
		v.rows = append(v.rows, statsRow{"Peers", "no paired instance connected", colMuted})
		return v
	}
	for i, r := range snap.RTT {
		c := statsPalette[i%len(statsPalette)]
		val := "n/a"
		if r.Has {
			val = fmt.Sprintf("%.1f ms", r.LatestMs)
		}
		label := r.Label
		if label == "" {
			label = r.NodeID
		}
		v.rows = append(v.rows, statsRow{strings.ToUpper(label), val, c})
		v.graph = append(v.graph, statsSeries{label: label, vals: r.Ms, col: c})
	}
	return v
}

// RenderStats draws a stats panel into a fixed w×h canvas: title, readout rows, an optional autoscaled
// time graph (with legend), and a wrapped "?" education footer. bgAlpha scales the panel background
// only. Reuses the Renderer's Orbitron faces + brand palette (corporate identity).
func (r *Renderer) RenderStats(v statsView, w, h int, bgAlpha float64) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	r.paintInto(img, func(p *paint) { r.statsPaint(p, v, w, h, bgAlpha) })
	return img
}

// statsPaint draws the stats panel into a paint target (direct Go or zigvr display list).
func (r *Renderer) statsPaint(p *paint, v statsView, w, h int, bgAlpha float64) {
	p.fillSrc(0, 0, w, h, scaleA(colPanelBG, bgAlpha))
	pad := 16

	p.text(r.name, v.title, pad, pad+r.lh-4, colName)
	p.fillOver(pad, pad+r.lh+2, w-2*pad, 2, colName) // brand underline
	y := pad + r.lh + 8 + r.lh

	for _, row := range v.rows {
		col := row.col
		if col == nil {
			col = colText
		}
		p.text(r.body, truncText(r.body, row.label, w-2*pad-textWidth(r.body, row.value)-16), pad, y, colText)
		p.text(r.body, row.value, w-pad-textWidth(r.body, row.value), y, col)
		y += r.lh
	}

	// Footer "?" education line (bottom, wrapped).
	var foot []string
	if v.footer != "" {
		foot = wrapText(r.body, "? "+v.footer, w-2*pad)
	}
	footTop := h - pad - len(foot)*r.lh

	// Graph fills the gap between the rows and the footer (when there's room + data).
	gTop, gBot := y+6, footTop-12
	if len(v.graph) > 0 && gBot-gTop > 48 {
		drawStatsLegend(p, r, v.graph, pad, gTop+r.lh-6)
		gTop += r.lh
		drawStatsGraph(p, pad, gTop, w-2*pad, gBot-gTop, v.graph)
	}

	fy := footTop + r.lh - 6
	for _, ln := range foot {
		p.text(r.body, ln, pad, fy, colMuted)
		fy += r.lh
	}
}

// drawStatsLegend draws colour swatch + label chips left→right at the given text baseline.
func drawStatsLegend(p *paint, r *Renderer, series []statsSeries, x, baseline int) {
	cx := x
	for _, s := range series {
		if s.label == "" {
			continue
		}
		p.fillOver(cx, baseline-10, 12, 10, s.col)
		cx += 16
		p.text(r.body, s.label, cx, baseline, colText)
		cx += textWidth(r.body, s.label) + 18
	}
}

// drawStatsGraph rasters multi-series traces into x,y,w,h: recessed dark well, faint thirds grid,
// right-aligned (newest at the right edge), autoscaled to the hottest sample. Mirrors the dashboard
// netGraph so the in-headset graph reads identically to the desktop one.
func drawStatsGraph(p *paint, x, y, w, h int, series []statsSeries) {
	p.fillOver(x, y, w, h, color.NRGBA{R: 6, G: 6, B: 10, A: 220})
	grid := color.NRGBA{R: 60, G: 60, B: 72, A: 70}
	p.fillOver(x, y+h/3, w, 1, grid)
	p.fillOver(x, y+2*h/3, w, 1, grid)

	span := 0
	for _, s := range series {
		if len(s.vals) > span {
			span = len(s.vals)
		}
	}
	if span == 0 || w <= 0 || h <= 0 {
		return
	}
	maxV := 1.0
	for _, s := range series {
		for _, val := range s.vals {
			if !math.IsNaN(val) && val > maxV {
				maxV = val
			}
		}
	}
	usable := float64(h) * 0.9
	colW := float64(w) / float64(span)
	for _, s := range series {
		n := len(s.vals)
		if n == 0 {
			continue
		}
		x0 := x + w - int(float64(n)*colW) // right-aligned: newest at the right edge
		prevY := -1
		for px := max(x0, x); px < x+w; px++ {
			idx := min(int(float64(px-x0)/colW), n-1)
			if idx < 0 {
				continue
			}
			val := s.vals[idx]
			if math.IsNaN(val) {
				prevY = -1
				continue
			}
			gy := y + h - 1 - int(val/maxV*usable)
			if gy < y {
				gy = y
			}
			if s.fill {
				fc := s.col
				fc.A = 0x2e
				p.fillOver(px, gy+1, 1, y+h-(gy+1), fc)
			}
			top, bot := gy, gy
			if prevY >= 0 { // connect columns → continuous line
				top, bot = min(prevY, gy), max(prevY, gy)
			}
			p.fillOver(px, top, 1, bot+2-top, s.col)
			prevY = gy
		}
	}
}

// --- small helpers ---

func lastSamples(s []perfmon.Sample, n int) []perfmon.Sample {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

func mapSamples(s []perfmon.Sample, f func(perfmon.Sample) float64) []float64 {
	out := make([]float64, len(s))
	for i := range s {
		out[i] = f(s[i])
	}
	return out
}

func lastF(vals []float64) float64 {
	if n := len(vals); n > 0 {
		if v := vals[n-1]; !math.IsNaN(v) {
			return v
		}
	}
	return 0
}

func byteRate(bps float64) string {
	if bps < 0 {
		bps = 0
	}
	return humanBytes(uint64(bps)) + "/s"
}

// humanBytes formats a byte count with a binary-prefix unit (own copy - vroverlay can't import ui).
func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// pctColor greens low load, ambers mid, reds high (v against full = the "100%" reference).
func pctColor(v, full float64) color.Color {
	switch {
	case full > 0 && v >= full*0.9:
		return colHot
	case full > 0 && v >= full*0.6:
		return colAmber
	default:
		return colMint
	}
}

func memColor(used, total float64) color.Color {
	if total <= 0 {
		return colText
	}
	return pctColor(used/total*100, 100)
}

// fpsColor greens at/near the refresh target, ambers a dip, reds a big shortfall.
func fpsColor(fps, target float64) color.Color {
	if target <= 0 {
		target = 90
	}
	switch {
	case fps >= target*0.95:
		return colMint
	case fps >= target*0.8:
		return colAmber
	default:
		return colHot
	}
}

func reprojColor(reproj, dropped int) color.Color {
	switch {
	case dropped > 0 || reproj > 5:
		return colHot
	case reproj > 0:
		return colAmber
	default:
		return colMint
	}
}
