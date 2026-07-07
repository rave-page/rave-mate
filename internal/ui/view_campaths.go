package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"rave.page/mate/internal/vrccampaths"
)

// camPathsDialog browses VRChat camera paths (grouped by world / player-relative), previews the
// selected path in 3D (the path, the camera's facing at each keyframe, and an animated marker that
// flies the path at its real speed), and loads a path into VRChat over OSC.
func (u *UI) camPathsDialog() {
	if u.svc.VRCTools == nil {
		dialog.ShowInformation("Camera paths", "VRChat tools are unavailable.", u.win)
		return
	}
	paths := u.svc.VRCTools.CamPaths()
	sortPathsForList(paths) // group by world; player-relative last

	view := newCamPathView()
	info := widget.NewLabel("")
	info.Wrapping = fyne.TextWrapWord

	// List grouped by folder (world / Player-Relative), newest first within each.
	names := camPathListLabels(paths)
	list := widget.NewList(
		func() int { return len(names) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i widget.ListItemID, o fyne.CanvasObject) { o.(*widget.Label).SetText(names[i]) },
	)
	var sel *vrccampaths.Path
	list.OnSelected = func(i widget.ListItemID) {
		if i < 0 || i >= len(paths) {
			return
		}
		p := paths[i]
		sel = &p
		pts, err := vrccampaths.LoadPoints(p.File)
		if err != nil {
			info.SetText("Failed to read path: " + err.Error())
			return
		}
		view.setPath(pts)
		where := p.WorldName
		if p.Local {
			where = "Player-relative"
		} else if where == "" {
			where = "Unknown world"
		}
		info.SetText(fmt.Sprintf("%s\n%s · %d keyframes · %.1fs · saved %s",
			p.Name, where, p.Points, p.DurationSec, p.SavedAt.Format("2006-01-02 15:04")))
	}

	playBtn := widget.NewButton("Play", nil)
	playBtn.OnTapped = func() {
		view.playing = !view.playing
		if view.playing {
			playBtn.SetText("Pause")
		} else {
			playBtn.SetText("Play")
		}
	}
	loadBtn := widget.NewButtonWithIcon("Load into VRChat", theme.MediaPlayIcon(), func() {
		if sel == nil {
			return
		}
		if err := u.svc.VRCTools.LoadCamPath(sel.File); err != nil {
			dialog.ShowError(err, u.win)
			return
		}
		u.Notify("Camera paths", "Sent to VRChat (needs OSC enabled).")
	})
	copyBtn := widget.NewButton("Copy file path", func() {
		if sel != nil {
			u.app.Clipboard().SetContent(sel.File)
			u.Notify("Camera paths", "Path copied - paste in VRChat's camera import.")
		}
	})
	organizeBtn := widget.NewButton("Organize now", func() {
		_, n := u.svc.VRCTools.OrganizeNow()
		u.Notify("Camera paths", fmt.Sprintf("Organized %d path(s).", n))
	})

	// 30fps playback ticker: ease the camera + advance the marker at the path's real speed.
	stop := make(chan struct{})
	go func() {
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
				view.ease()
				if view.playing {
					view.advance(dt)
				}
				fyne.Do(view.Refresh)
			}
		}
	}()

	controls := container.NewVBox(
		container.NewHBox(playBtn, loadBtn, copyBtn, organizeBtn),
		info,
		widget.NewLabelWithStyle("Drag to orbit · scroll to zoom · marker flies the path at its real speed", fyne.TextAlignLeading, fyne.TextStyle{Italic: true}),
	)
	left := container.NewBorder(widget.NewLabel("Camera paths"), nil, nil, nil, list)
	body := container.NewBorder(nil, controls, container.NewGridWrap(fyne.NewSize(220, 360), left), nil, view)

	d := dialog.NewCustom("Camera paths", "Done", body, u.win)
	d.Resize(fyne.NewSize(900, 560))
	d.SetOnClosed(func() { close(stop) })
	if len(paths) > 0 {
		list.Select(0)
	}
	d.Show()
}

// camPathListLabels builds display rows: "World - name (12 keyframes, 24s)".
func camPathListLabels(paths []vrccampaths.Path) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		where := p.WorldName
		if p.Local {
			where = "Player-relative"
		} else if where == "" {
			where = "Unknown"
		}
		out[i] = fmt.Sprintf("%s - %s (%dkf, %.0fs)", where, p.Name, p.Points, p.DurationSec)
	}
	return out
}

// ── 3D camera-path preview widget ────────────────────────────────────────────

type camNode struct {
	pos [3]float32
	fwd [3]float32 // camera facing (unit), from euler
	spd float32    // segment speed (for colouring)
	dur float64    // segment duration (seconds to the next node)
}

type camPathView struct {
	widget.BaseWidget
	raster *canvas.Raster

	yaw, pitch, dist    float32
	tyaw, tpitch, tdist float32
	center              [3]float32
	floorY, gridR       float32

	nodes   []camNode
	total   float64 // total path duration
	t       float64 // playback head (seconds)
	playing bool
	hasPath bool
}

func newCamPathView() *camPathView {
	v := &camPathView{yaw: 0.6, pitch: 0.35, dist: 4, tyaw: 0.6, tpitch: 0.35, tdist: 4, gridR: 2}
	v.raster = canvas.NewRaster(v.render)
	v.ExtendBaseWidget(v)
	return v
}

func (v *camPathView) MinSize() fyne.Size                  { return fyne.NewSize(560, 360) }
func (v *camPathView) CreateRenderer() fyne.WidgetRenderer { return &camPathRenderer{v: v} }
func (v *camPathView) Refresh()                            { v.raster.Refresh() }

// setPath loads keyframes: positions, per-keyframe facing (from euler), speed + duration, and
// frames the orbit camera around the path bounds.
func (v *camPathView) setPath(pts []vrccampaths.Point) {
	v.nodes = v.nodes[:0]
	v.total, v.t = 0, 0
	lo := [3]float32{1e9, 1e9, 1e9}
	hi := [3]float32{-1e9, -1e9, -1e9}
	for _, p := range pts {
		pos := [3]float32{float32(p.Position.X), float32(p.Position.Y), float32(p.Position.Z)}
		for i := 0; i < 3; i++ {
			if pos[i] < lo[i] {
				lo[i] = pos[i]
			}
			if pos[i] > hi[i] {
				hi[i] = pos[i]
			}
		}
		v.nodes = append(v.nodes, camNode{pos: pos, fwd: eulerForward(p.Rotation), spd: float32(p.Speed), dur: maxf(p.Duration, 0.05)})
		v.total += maxf(p.Duration, 0.05)
	}
	if len(v.nodes) == 0 {
		v.hasPath = false
		return
	}
	v.center = [3]float32{(lo[0] + hi[0]) / 2, (lo[1] + hi[1]) / 2, (lo[2] + hi[2]) / 2}
	v.floorY = lo[1]
	diag := float32(math.Sqrt(float64(sq(hi[0]-lo[0]) + sq(hi[1]-lo[1]) + sq(hi[2]-lo[2]))))
	v.gridR = float32(math.Max(1, float64((hi[0]-lo[0]+hi[2]-lo[2])/2)))
	v.tdist = diag*1.3 + 1.5
	v.dist = v.tdist
	v.hasPath = true
}

// advance moves the playback head, looping.
func (v *camPathView) advance(dt float64) {
	if v.total <= 0 {
		return
	}
	v.t += dt
	for v.t > v.total {
		v.t -= v.total
	}
}

// sampleAt returns the interpolated camera position + facing at time t along the path.
func (v *camPathView) sampleAt(t float64) (pos, fwd [3]float32) {
	if len(v.nodes) == 0 {
		return
	}
	if len(v.nodes) == 1 {
		return v.nodes[0].pos, v.nodes[0].fwd
	}
	acc := 0.0
	for i := 0; i < len(v.nodes)-1; i++ {
		seg := v.nodes[i].dur
		if t <= acc+seg || i == len(v.nodes)-2 {
			u := float32(0)
			if seg > 0 {
				u = clamp32(float32((t-acc)/seg), 0, 1)
			}
			a, b := v.nodes[i], v.nodes[i+1]
			return lerp3(a.pos, b.pos, u), nlerp3(a.fwd, b.fwd, u)
		}
		acc += seg
	}
	last := v.nodes[len(v.nodes)-1]
	return last.pos, last.fwd
}

func (v *camPathView) Dragged(e *fyne.DragEvent) {
	v.tyaw -= e.Dragged.DX * 0.012
	v.tpitch = clamp32(v.tpitch+e.Dragged.DY*0.012, -1.45, 1.45)
}
func (v *camPathView) DragEnd() {}
func (v *camPathView) Scrolled(e *fyne.ScrollEvent) {
	v.tdist = clamp32(v.tdist*float32(math.Pow(0.9, float64(e.Scrolled.DY/40))), 0.8, 30)
}
func (v *camPathView) ease() {
	v.yaw += (v.tyaw - v.yaw) * 0.3
	v.pitch += (v.tpitch - v.pitch) * 0.3
	v.dist += (v.tdist - v.dist) * 0.3
}

func (v *camPathView) project(p [3]float32, w, h int) (int, int, float32) {
	dx, dy, dz := p[0]-v.center[0], p[1]-v.center[1], p[2]-v.center[2]
	cy, sy := float32(math.Cos(float64(v.yaw))), float32(math.Sin(float64(v.yaw)))
	x1 := dx*cy + dz*sy
	z1 := -dx*sy + dz*cy
	cp, sp := float32(math.Cos(float64(v.pitch))), float32(math.Sin(float64(v.pitch)))
	y2 := dy*cp - z1*sp
	z2 := dy*sp + z1*cp
	depth := v.dist - z2
	if depth < 0.15 {
		depth = 0.15
	}
	f := float32(h) * 0.9
	return int(float32(w)/2 + f*x1/depth), int(float32(h)/2 - f*y2/depth), depth
}

func (v *camPathView) render(w, h int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	fillImg(img, mpBG)
	if !v.hasPath {
		drawText5(img, "Select a camera path to preview", 16, h/2, mpText)
		return img
	}
	// Floor grid for spatial reference.
	const n = 6
	step := (2 * v.gridR) / n
	for i := 0; i <= n; i++ {
		d := -v.gridR + step*float32(i)
		a1, b1, _ := v.project([3]float32{v.center[0] - v.gridR, v.floorY, v.center[2] + d}, w, h)
		a2, b2, _ := v.project([3]float32{v.center[0] + v.gridR, v.floorY, v.center[2] + d}, w, h)
		drawLine(img, image.Pt(a1, b1), image.Pt(a2, b2), mpGrid)
		c1, e1, _ := v.project([3]float32{v.center[0] + d, v.floorY, v.center[2] - v.gridR}, w, h)
		c2, e2, _ := v.project([3]float32{v.center[0] + d, v.floorY, v.center[2] + v.gridR}, w, h)
		drawLine(img, image.Pt(c1, e1), image.Pt(c2, e2), mpGrid)
	}
	// Path polyline, coloured by per-segment speed (slow=mint → fast=pink).
	maxSpd := float32(0.1)
	for _, nd := range v.nodes {
		if nd.spd > maxSpd {
			maxSpd = nd.spd
		}
	}
	var prev image.Point
	for i, nd := range v.nodes {
		sx, sy, _ := v.project(nd.pos, w, h)
		cur := image.Pt(sx, sy)
		if i > 0 {
			drawLine(img, prev, cur, speedColor(v.nodes[i-1].spd/maxSpd))
		}
		prev = cur
		// keyframe dot + facing arrow
		drawDisc(img, cur, 3, mpHead)
		drawFacing(img, v, nd.pos, nd.fwd, w, h, mpTrk)
	}
	// Animated camera marker flying the path.
	pos, fwd := v.sampleAt(v.t)
	mx, my, _ := v.project(pos, w, h)
	drawDisc(img, image.Pt(mx, my), 6, cpMarker)
	drawFacing(img, v, pos, fwd, w, h, cpMarker)
	// HUD: time + current speed.
	cur := v.currentSpeed()
	drawText5(img, fmt.Sprintf("t=%.1f/%.1fs  speed=%.1f", v.t, v.total, cur), 12, h-14, mpText)
	return img
}

func (v *camPathView) currentSpeed() float32 {
	acc := 0.0
	for i := 0; i < len(v.nodes); i++ {
		acc += v.nodes[i].dur
		if v.t <= acc {
			return v.nodes[i].spd
		}
	}
	if len(v.nodes) > 0 {
		return v.nodes[len(v.nodes)-1].spd
	}
	return 0
}

// drawFacing draws a short arrow from a world point along a world-space forward vector.
func drawFacing(img *image.NRGBA, v *camPathView, p, fwd [3]float32, w, h int, col color.NRGBA) {
	tip := [3]float32{p[0] + fwd[0]*0.4, p[1] + fwd[1]*0.4, p[2] + fwd[2]*0.4}
	ax, ay, _ := v.project(p, w, h)
	bx, by, _ := v.project(tip, w, h)
	drawLine(img, image.Pt(ax, ay), image.Pt(bx, by), col)
}

// speedColor maps 0..1 → mint (slow) → amber → pink (fast).
func speedColor(f float32) color.NRGBA {
	f = clamp32(f, 0, 1)
	return color.NRGBA{R: uint8(8 + 247*f), G: uint8(247 - 100*f), B: uint8(155 - 100*f), A: 255}
}

var cpMarker = color.NRGBA{R: 0xFF, G: 0xB5, B: 0x47, A: 255} // amber marker

func eulerForward(r vrccampaths.Vec3) [3]float32 {
	const d2r = math.Pi / 180
	yaw, pitch := float64(r.Y)*d2r, float64(r.X)*d2r
	return [3]float32{
		float32(math.Sin(yaw) * math.Cos(pitch)),
		float32(-math.Sin(pitch)),
		float32(math.Cos(yaw) * math.Cos(pitch)),
	}
}

func lerp3(a, b [3]float32, u float32) [3]float32 {
	return [3]float32{a[0] + (b[0]-a[0])*u, a[1] + (b[1]-a[1])*u, a[2] + (b[2]-a[2])*u}
}

// nlerp3 interpolates + renormalizes two direction vectors.
func nlerp3(a, b [3]float32, u float32) [3]float32 {
	q := lerp3(a, b, u)
	n := float32(math.Sqrt(float64(sq(q[0]) + sq(q[1]) + sq(q[2]))))
	if n == 0 {
		return [3]float32{0, 0, 1}
	}
	return [3]float32{q[0] / n, q[1] / n, q[2] / n}
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

type camPathRenderer struct{ v *camPathView }

func (r *camPathRenderer) Layout(s fyne.Size)           { r.v.raster.Resize(s) }
func (r *camPathRenderer) MinSize() fyne.Size           { return r.v.MinSize() }
func (r *camPathRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.v.raster} }
func (r *camPathRenderer) Refresh()                     { r.v.raster.Refresh() }
func (r *camPathRenderer) Destroy()                     {}

// sortPathsForList is a stable display order: world groups A→Z, player-relative last, newest first.
func sortPathsForList(paths []vrccampaths.Path) {
	sort.SliceStable(paths, func(i, j int) bool {
		fi, fj := paths[i].Folder(), paths[j].Folder()
		if fi != fj {
			if fi == vrccampaths.PlayerRelativeFolder {
				return false
			}
			if fj == vrccampaths.PlayerRelativeFolder {
				return true
			}
			return fi < fj
		}
		return paths[i].SavedAt.After(paths[j].SavedAt)
	})
}
