package webui

// Shared interactive camera-path 3D viewer - ONE component for the Motion tab and the
// VRChat tab (replaces the former static isometric duplicate in render_vrchat.go).
// Orbit-drag + wheel-zoom + SMIL flying marker + "Play path" flythrough (the view
// camera rides the path first-person at its real per-segment timing).
//
// Perf contract (task #61 - camera drag was laggy):
//   - The SVG splits into a geometry group (grid/polyline/dots/arrows) and an anim
//     group (the SMIL marker). Drag frames patch ONLY the geometry group - no SMIL
//     rebuild/restart and no full-tab re-render per pointermove.
//   - Drag renders go through renderCoalesce (latest-wins, one in flight), never the
//     serial actWorker, so a fast pointer can't lag the acts lane.
//   - The marker is cleared on drag start and restored once on drag end (a per-move
//     SMIL restart re-parsed the whole animation timeline every frame).

import (
	"fmt"
	"html"
	"math"
	"strings"
	"sync"
	"time"

	"rave.page/mate/internal/governor"
	"rave.page/mate/internal/i18n"
	"rave.page/mate/internal/vrccampaths"
)

const cpvW, cpvH = 640, 400

type cpvSt struct {
	mu   sync.Mutex
	file string
	pts  []vrccampaths.Point
	cam  orbitCam

	dnYaw, dnPit, dnX, dnY float32
	dragging               bool

	playing bool
	playT   float64
	stop    chan struct{} // flythrough goroutine (nil = not running)
}

var (
	cpvMu  sync.Mutex
	cpvMap = map[*UI]map[string]*cpvSt{}
)

func (u *UI) cpv(host string) *cpvSt {
	cpvMu.Lock()
	defer cpvMu.Unlock()
	m := cpvMap[u]
	if m == nil {
		m = map[string]*cpvSt{}
		cpvMap[u] = m
	}
	s := m[host]
	if s == nil {
		s = &cpvSt{cam: newOrbitCam()}
		m[host] = s
	}
	return s
}

func init() {
	onPrefix("cpv-orbit:", func(u *UI, m actMsg) { u.cpvOrbit(m.arg("cpv-orbit:"), m.Val) })
	onPrefix("cpv-zoom:", func(u *UI, m actMsg) { u.cpvZoom(m.arg("cpv-zoom:"), m.Val) })
	onPrefix("cpv-play:", func(u *UI, m actMsg) { u.cpvPlayToggle(m.arg("cpv-play:")) })
}

// cpvEnsure reflects the host's current selection in its viewer. On a selection change it clears the
// old geometry + stops any flythrough synchronously, then loads the keyframes OFF-THREAD (LoadPoints
// = file read + JSON parse; never on the serial render/act thread) and re-renders the pane when they
// land. Same file = keep the user's orbit. Render/cpvView read only cached pts (empty until loaded).
func (u *UI) cpvEnsure(host, file string) {
	s := u.cpv(host)
	s.mu.Lock()
	if s.file == file {
		s.mu.Unlock()
		return
	}
	s.cpvStopLocked()
	s.file, s.pts = file, nil // claim the new file; drop old geometry until the async load lands
	s.mu.Unlock()
	if file == "" {
		return
	}
	u.bg(func() {
		pts, err := vrccampaths.LoadPoints(file)
		s.mu.Lock()
		if s.file != file { // selection moved on while we loaded - drop stale points
			s.mu.Unlock()
			return
		}
		s.pts = pts
		if len(pts) > 0 {
			lo := [3]float32{1e9, 1e9, 1e9}
			hi := [3]float32{-1e9, -1e9, -1e9}
			for _, pt := range pts {
				pos := cpvPos(pt)
				for k := 0; k < 3; k++ {
					lo[k] = float32(math.Min(float64(lo[k]), float64(pos[k])))
					hi[k] = float32(math.Max(float64(hi[k]), float64(pos[k])))
				}
			}
			s.cam.frame(lo, hi, 1.3, 1.5)
		}
		s.mu.Unlock()
		if err != nil {
			u.toast(i18n.T("motion.toast.readPathFailed") + err.Error())
		}
		u.cpvLoadedPatch(host)
	})
}

// cpvLoadedPatch re-renders the host pane that embeds the viewer after an async point-load, so the
// geometry appears inline (no dependency on the cpv fragments already being in the DOM). Scoped to
// the host's visible surface; a no-op elsewhere.
func (u *UI) cpvLoadedPatch(host string) {
	if u.stopped() {
		return
	}
	switch host {
	case "vrc":
		if u.activeTab() == "vrchat" && u.vrcgSub() == "profile" {
			u.patchCampaths()
		}
	case "mo":
		if u.activeTab() == "motion" {
			u.moPatchBody()
		}
	}
}

// cpvStopLocked ends a running flythrough (caller holds s.mu).
func (s *cpvSt) cpvStopLocked() {
	s.playing, s.playT = false, 0
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
}

// ── render ──

// cpvView renders the interactive viewer for host ("mo" | "vrc"). Hosts call
// cpvEnsure first so the state matches their current selection.
func (u *UI) cpvView(host string) string {
	s := u.cpv(host)
	s.mu.Lock()
	geo, anim := cpvGeoSVGLocked(s), cpvAnimSVGLocked(s)
	s.mu.Unlock()
	return fmt.Sprintf(`<div id="cpv-%s" class=cpv-view data-actpos="cpv-orbit:%s" data-actwheel="cpv-zoom:%s">`+
		`<svg class=cpv-svg viewBox="0 0 %d %d" preserveAspectRatio="xMidYMid meet">`+
		`<rect width="100%%" height="100%%" fill="rgba(0,0,0,.25)"/>`+
		`<g id="cpv-%s-geo">%s</g><g id="cpv-%s-anim">%s</g></svg></div>`,
		host, host, host, cpvW, cpvH, host, geo, host, anim)
}

// cpvPlayBtn renders the action-bound Play-path/Stop button (patched in place on toggle).
func (u *UI) cpvPlayBtn(host string) string {
	s := u.cpv(host)
	s.mu.Lock()
	playing, n := s.playing, len(s.pts)
	s.mu.Unlock()
	inner := btn("▶ "+i18n.T("campath.play"), "go", "cpv-play:"+host, "")
	if playing {
		inner = btn("⏹ "+i18n.T("player.stop"), "outline", "cpv-play:"+host, "")
	}
	if n < 2 {
		inner = ""
	}
	return `<span id="cpv-` + host + `-play">` + inner + `</span>`
}

// cpvGeoSVGLocked renders the geometry group: orbit view (grid, speed polyline,
// keyframe dots, facing arrows) or, mid-flythrough, the first-person frame at playT.
func cpvGeoSVGLocked(s *cpvSt) string {
	const w, h = float32(cpvW), float32(cpvH)
	if len(s.pts) == 0 {
		return `<text x="20" y="200" class=cpv-svgtext>` + html.EscapeString(i18n.T("campath.empty")) + `</text>`
	}
	if s.playing {
		return cpvFlightSVG(s, s.playT)
	}
	cam := s.cam
	var b strings.Builder
	b.WriteString(cam.gridSVG(w, h))
	nodes := make([][2]float32, len(s.pts))
	maxSpd := float32(0.1)
	for i, p := range s.pts {
		pos := cpvPos(p)
		x, y := cam.project(pos, w, h)
		nodes[i] = [2]float32{x, y}
		if sp := float32(p.Speed); sp > maxSpd {
			maxSpd = sp
		}
		fwd := cpvForward(p.Rotation)
		tip := [3]float32{pos[0] + fwd[0]*0.4, pos[1] + fwd[1]*0.4, pos[2] + fwd[2]*0.4}
		tx, ty := cam.project(tip, w, h)
		b.WriteString(svgLine(x, y, tx, ty, "rgba(196,164,255,.8)", 1))
	}
	for i := 1; i < len(nodes); i++ {
		b.WriteString(svgLine(nodes[i-1][0], nodes[i-1][1], nodes[i][0], nodes[i][1],
			speedHex(float32(s.pts[i-1].Speed)/maxSpd), 2))
	}
	for _, n := range nodes {
		b.WriteString(svgDisc(n[0], n[1], 3, "var(--rp-fg,#e6e8ee)"))
	}
	return b.String()
}

// cpvAnimSVGLocked renders the SMIL marker flying the path at real per-segment speed.
// Empty while dragging (cleared on pointerdown, restored on pointerup) and while the
// first-person flythrough owns the view.
func cpvAnimSVGLocked(s *cpvSt) string {
	const w, h = float32(cpvW), float32(cpvH)
	if len(s.pts) < 2 || s.dragging || s.playing {
		return ""
	}
	cam := s.cam
	var path, kt, kp strings.Builder
	total, arc := 0.0, 0.0
	arcs := make([]float64, len(s.pts))
	var px, py float32
	for i, p := range s.pts {
		x, y := cam.project(cpvPos(p), w, h)
		if i == 0 {
			fmt.Fprintf(&path, "M %.1f %.1f", x, y)
		} else {
			fmt.Fprintf(&path, " L %.1f %.1f", x, y)
			arc += math.Hypot(float64(x-px), float64(y-py))
			arcs[i] = arc
			total += cpvSegDur(s.pts, i-1)
		}
		px, py = x, y
	}
	if total <= 0 || arc <= 0 {
		return ""
	}
	acc := 0.0
	for i := range s.pts {
		if i > 0 {
			kt.WriteString(";")
			kp.WriteString(";")
		}
		fmt.Fprintf(&kt, "%.4f", acc/total)
		fmt.Fprintf(&kp, "%.4f", arcs[i]/arc)
		if i < len(s.pts)-1 {
			acc += cpvSegDur(s.pts, i)
		}
	}
	return fmt.Sprintf(`<circle r="6" fill="var(--rp-amber,#FFB547)"><animateMotion dur="%.2fs" repeatCount="indefinite" calcMode="linear" keyTimes="%s" keyPoints="%s" path="%s"/></circle>`,
		total, kt.String(), kp.String(), path.String())
}

// ── flythrough (Play path) ──

// cpvFlightSVG renders the first-person frame at time t: eye/look interpolated
// between keyframes at honest per-segment timing; grid + path + dots projected from
// the moving camera. Points behind the eye clamp to the near plane (preview-grade).
func cpvFlightSVG(s *cpvSt, t float64) string {
	const w, h = float32(cpvW), float32(cpvH)
	pts := s.pts
	eye, fwd, total := cpvPoseAt(pts, t)
	right := cpvCross([3]float32{0, 1, 0}, fwd)
	if l := cpvLen(right); l < 1e-4 {
		right = [3]float32{1, 0, 0}
	} else {
		right = cpvScale(right, 1/l)
	}
	up := cpvCross(fwd, right)
	f := h * 0.9
	proj := func(p [3]float32) (float32, float32) {
		d := [3]float32{p[0] - eye[0], p[1] - eye[1], p[2] - eye[2]}
		depth := cpvDot(d, fwd)
		if depth < 0.15 {
			depth = 0.15
		}
		return w/2 + f*cpvDot(d, right)/depth, h/2 - f*cpvDot(d, up)/depth
	}
	var b strings.Builder
	b.WriteString(gridLinesSVG(proj, s.cam.center, s.cam.floorY, s.cam.gridR))
	maxSpd := float32(0.1)
	for _, p := range pts {
		if sp := float32(p.Speed); sp > maxSpd {
			maxSpd = sp
		}
	}
	var px, py float32
	for i, p := range pts {
		x, y := proj(cpvPos(p))
		if i > 0 {
			b.WriteString(svgLine(px, py, x, y, speedHex(float32(pts[i-1].Speed)/maxSpd), 2))
		}
		b.WriteString(svgDisc(x, y, 3, "var(--rp-fg,#e6e8ee)"))
		px, py = x, y
	}
	b.WriteString(fmt.Sprintf(`<text x="12" y="%d" class=cpv-svgtext>%.1f / %.1f s</text>`, cpvH-12, t, total))
	return b.String()
}

// cpvPoseAt interpolates eye position + forward direction along the path at time t.
func cpvPoseAt(pts []vrccampaths.Point, t float64) (eye, fwd [3]float32, total float64) {
	for i := 0; i < len(pts)-1; i++ {
		total += cpvSegDur(pts, i)
	}
	if t < 0 {
		t = 0
	}
	if t > total {
		t = total
	}
	acc := 0.0
	k := 0
	fr := float32(0)
	for i := 0; i < len(pts)-1; i++ {
		d := cpvSegDur(pts, i)
		if t <= acc+d || i == len(pts)-2 {
			k = i
			fr = float32((t - acc) / d)
			break
		}
		acc += d
	}
	fr = clampf32(fr, 0, 1)
	a, b := cpvPos(pts[k]), cpvPos(pts[k+1])
	eye = [3]float32{a[0] + (b[0]-a[0])*fr, a[1] + (b[1]-a[1])*fr, a[2] + (b[2]-a[2])*fr}
	fa, fb := cpvForward(pts[k].Rotation), cpvForward(pts[k+1].Rotation)
	fwd = [3]float32{fa[0] + (fb[0]-fa[0])*fr, fa[1] + (fb[1]-fa[1])*fr, fa[2] + (fb[2]-fa[2])*fr}
	if l := cpvLen(fwd); l < 1e-4 {
		fwd = fa // opposed rotations cancel - hold the segment-start look
	} else {
		fwd = cpvScale(fwd, 1/l)
	}
	return eye, fwd, total
}

// cpvSegDur is the honest duration of segment i→i+1 (the keyframe's Duration field,
// floored like the SMIL marker so a zero-length segment can't stall the clock).
func cpvSegDur(pts []vrccampaths.Point, i int) float64 {
	d := pts[i].Duration
	if d < 0.05 {
		d = 0.05
	}
	return d
}

func (u *UI) cpvPlayToggle(host string) {
	s := u.cpv(host)
	s.mu.Lock()
	if s.playing {
		s.cpvStopLocked()
		s.mu.Unlock()
		u.cpvPatchAll(host)
		return
	}
	if len(s.pts) < 2 {
		s.mu.Unlock()
		return
	}
	s.playing, s.playT = true, 0
	s.stop = make(chan struct{})
	stop := s.stop
	s.mu.Unlock()
	u.cpvPatchAll(host)
	u.bg(func() { u.cpvRunFlight(host, stop) })
}

// cpvRunFlight advances the flythrough at ~30fps and patches the geometry group.
// Time always advances; renders skip while the window is mid size-move or the
// governor gates UI animation (a live stream owns the CPU). One goroutine, no queue -
// the eval queue coalesces by fragment id (newest wins).
func (u *UI) cpvRunFlight(host string, stop chan struct{}) {
	tk := time.NewTicker(33 * time.Millisecond)
	defer tk.Stop()
	last := time.Now()
	for {
		select {
		case <-stop:
			return
		case now := <-tk.C:
			dt := now.Sub(last).Seconds()
			last = now
			s := u.cpv(host)
			s.mu.Lock()
			if !s.playing {
				s.mu.Unlock()
				return
			}
			s.playT += dt
			_, _, total := cpvPoseAt(s.pts, 0)
			done := s.playT >= total
			if done {
				s.playT = total
			}
			geo := cpvGeoSVGLocked(s)
			if done {
				s.cpvStopLocked()
			}
			s.mu.Unlock()
			if done {
				u.cpvPatchAll(host)
				return
			}
			if inSizeMove() || !governor.UIAnimAllowed() {
				continue
			}
			u.eval("window.__patch('cpv-" + host + "-geo'," + jsQuote(geo) + ")")
		}
	}
}

// ── orbit / zoom (pointer transport) ──

func (u *UI) cpvOrbit(host, val string) {
	kind, fx, fy, ok := moPosParse(val)
	if !ok {
		return
	}
	s := u.cpv(host)
	s.mu.Lock()
	if s.playing { // the flythrough owns the camera
		s.mu.Unlock()
		return
	}
	switch kind {
	case "down":
		s.dnYaw, s.dnPit, s.dnX, s.dnY = s.cam.yaw, s.cam.pitch, fx, fy
		s.dragging = true
		s.mu.Unlock()
		// clear the SMIL marker once - per-move restarts re-parsed its whole timeline
		u.eval("window.__patch('cpv-" + host + "-anim','')")
		return
	case "move":
		s.cam.yaw = s.dnYaw - (fx-s.dnX)*6
		s.cam.pitch = clampf32(s.dnPit+(fy-s.dnY)*6, -1.45, 1.45)
		s.mu.Unlock()
		u.renderCoalesce("cpv-"+host, func() { u.cpvPatchGeo(host) })
		return
	case "up":
		s.cam.yaw = s.dnYaw - (fx-s.dnX)*6
		s.cam.pitch = clampf32(s.dnPit+(fy-s.dnY)*6, -1.45, 1.45)
		s.dragging = false
		s.mu.Unlock()
		u.renderCoalesce("cpv-"+host, func() { u.cpvPatchGeoAnim(host) })
		return
	}
	s.mu.Unlock()
}

func (u *UI) cpvZoom(host, val string) {
	in := strings.HasPrefix(val, "in:")
	s := u.cpv(host)
	s.mu.Lock()
	if s.playing {
		s.mu.Unlock()
		return
	}
	s.cam.zoomBy(in, 0.8, 30)
	s.mu.Unlock()
	u.renderCoalesce("cpv-"+host, func() { u.cpvPatchGeoAnim(host) })
}

// ── patches ──

func (u *UI) cpvPatchGeo(host string) {
	s := u.cpv(host)
	s.mu.Lock()
	geo := cpvGeoSVGLocked(s)
	s.mu.Unlock()
	u.eval("window.__patch('cpv-" + host + "-geo'," + jsQuote(geo) + ")")
}

func (u *UI) cpvPatchGeoAnim(host string) {
	s := u.cpv(host)
	s.mu.Lock()
	geo, anim := cpvGeoSVGLocked(s), cpvAnimSVGLocked(s)
	s.mu.Unlock()
	u.eval("window.__patch('cpv-" + host + "-geo'," + jsQuote(geo) + ");" +
		"window.__patch('cpv-" + host + "-anim'," + jsQuote(anim) + ")")
}

// cpvPatchAll refreshes geometry, marker and the play button (play/stop flips).
func (u *UI) cpvPatchAll(host string) {
	s := u.cpv(host)
	s.mu.Lock()
	geo, anim := cpvGeoSVGLocked(s), cpvAnimSVGLocked(s)
	s.mu.Unlock()
	u.eval("window.__patch('cpv-" + host + "-geo'," + jsQuote(geo) + ");" +
		"window.__patch('cpv-" + host + "-anim'," + jsQuote(anim) + ");" +
		"window.__patch('cpv-" + host + "-play'," + jsQuote(u.cpvPlayBtnInner(host)) + ")")
}

// cpvPlayBtnInner is cpvPlayBtn without the wrapper (for patching the wrapper).
func (u *UI) cpvPlayBtnInner(host string) string {
	full := u.cpvPlayBtn(host)
	open := strings.Index(full, ">")
	return strings.TrimSuffix(full[open+1:], "</span>")
}

// ── math helpers ──

func cpvPos(p vrccampaths.Point) [3]float32 {
	return [3]float32{float32(p.Position.X), float32(p.Position.Y), float32(p.Position.Z)}
}

// cpvForward converts a VRChat euler (degrees) into a unit world-forward vector.
func cpvForward(r vrccampaths.Vec3) [3]float32 {
	const d2r = math.Pi / 180
	yaw, pitch := r.Y*d2r, r.X*d2r
	return [3]float32{
		float32(math.Sin(yaw) * math.Cos(pitch)),
		float32(-math.Sin(pitch)),
		float32(math.Cos(yaw) * math.Cos(pitch)),
	}
}

func cpvCross(a, b [3]float32) [3]float32 {
	return [3]float32{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}
func cpvDot(a, b [3]float32) float32 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func cpvLen(a [3]float32) float32 {
	return float32(math.Sqrt(float64(a[0]*a[0] + a[1]*a[1] + a[2]*a[2])))
}
func cpvScale(a [3]float32, f float32) [3]float32 { return [3]float32{a[0] * f, a[1] * f, a[2] * f} }
