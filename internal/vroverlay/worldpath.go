package vroverlay

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"time"

	"rave.page/mate/internal/config"
)

// In-VR camera-path preview: a 3D orbit view (perspective + floor grid + a marker flying the path at
// its real speed), rendered into a panel BESIDE the menu - the same idea as the desktop preview. A flat
// VR overlay is monoscopic (one image to both eyes → no stereo depth), so an AR billboard always looks
// 2D; instead we convey depth the way the desktop does - perspective + grid + a slowly auto-orbiting
// viewpoint. The panel never covers the menu, and only re-uploads when the frame changes. Gated to the
// editor being open + a path loaded, so closing the editor always dismisses it.
const (
	worldPathKey   = "rave.page.mate.pathpreview"
	pathPreviewPx  = 540          // orbit preview texture (square)
	pathOrbitSpeed = float32(0.5) // turntable auto-orbit (rad/s)
)

// driveWorldPath renders + positions the orbit preview when active, else hides it. Called each tick.
func (e *editor) driveWorldPath(feat config.VROverlayFeature, hand Hand) {
	if !e.worldPathInit {
		if err := e.m.rt.EnsureOverlay(worldPathKey, "rave-mate path preview"); err != nil {
			return
		}
		e.worldPathInit = true
	}
	if !e.on || !e.worldPathOn || len(e.worldPathGeom.Pts) < 2 {
		if e.worldPathShow.changed(false) {
			_ = e.m.rt.Show(worldPathKey, false)
		}
		if e.worldPathInter {
			e.ed.SetInteractive(worldPathKey, pathPreviewPx, pathPreviewPx, false)
			e.worldPathInter = false
		}
		return
	}
	if e.worldPathZoom == 0 {
		e.worldPathZoom, e.worldPathPitch = 1, 0.38 // first show: defaults
	}
	// Laser-drag to orbit, scroll to zoom (like the desktop preview). Manual drag stops the auto-spin.
	if !e.worldPathInter {
		e.ed.SetInteractive(worldPathKey, pathPreviewPx, pathPreviewPx, true)
		e.worldPathInter = true
	}
	e.pollPreviewInput()
	// Advance the playback head + (until the user drags) a slow auto-orbit, by real elapsed time.
	now := time.Now()
	dt := float32(0)
	if !e.worldPathLast.IsZero() {
		if d := float32(now.Sub(e.worldPathLast).Seconds()); d > 0 && d < 0.2 {
			dt = d
		}
	}
	e.worldPathLast = now
	if !e.worldPathManual {
		e.worldPathYaw += dt * pathOrbitSpeed
	}
	total := pathTotal(e.worldPathGeom.Dur)
	if e.worldPathPlaying && total > 0 {
		e.worldPathT += float64(dt)
		for e.worldPathT > total {
			e.worldPathT -= total
		}
	}
	// Re-render only when the visible frame changes (orbit / zoom / playback head / play state).
	sig := fmt.Sprintf("%.3f|%.3f|%.3f|%.3f|%v|%d", e.worldPathYaw, e.worldPathPitch, e.worldPathZoom, e.worldPathT, e.worldPathPlaying, len(e.worldPathGeom.Pts))
	if e.worldPathSig != sig {
		_ = e.m.rt.SetTexture(worldPathKey, e.m.rend.RenderPathOrbit(e.worldPathGeom, e.worldPathYaw, e.worldPathPitch, e.worldPathZoom, e.worldPathT, e.worldPathPlaying))
		e.worldPathSig = sig
	}
	// Place beside the menu (opposite the help panel), as a normal panel - never covers the menu.
	base := e.menuTransform(feat, hand)
	base.X -= base.WidthM*0.5 + 0.32
	base.WidthM = 0.5
	base.Opacity = 0.97
	if e.worldPathTf.needsApply(base, e.snapTracked(base)) {
		_ = e.m.rt.SetTransform(worldPathKey, base)
	}
	if e.worldPathShow.changed(true) {
		_ = e.m.rt.Show(worldPathKey, true)
	}
}

// pollPreviewInput handles laser input on the preview panel: hold + drag orbits (yaw/pitch), scroll
// zooms - the same gestures as the desktop preview. The first drag stops the auto-spin.
func (e *editor) pollPreviewInput() {
	for _, ev := range e.ed.PollEvents(worldPathKey) {
		switch ev.Type {
		case EvMouseDown:
			e.worldPathDrag, e.worldPathDragX, e.worldPathDragY, e.worldPathManual = true, ev.X, ev.Y, true
		case EvMouseUp:
			e.worldPathDrag = false
		case EvMouseMove:
			if e.worldPathDrag {
				e.worldPathYaw -= (ev.X - e.worldPathDragX) * 0.012
				e.worldPathPitch = clampF32(e.worldPathPitch+(ev.Y-e.worldPathDragY)*0.012, -1.4, 1.4)
				e.worldPathDragX, e.worldPathDragY = ev.X, ev.Y
			}
		case EvScroll:
			e.worldPathZoom = clampF32(e.worldPathZoom*f32pow(0.9, ev.Scroll), 0.3, 4)
			e.worldPathManual = true
		}
	}
}

// RenderPathOrbit draws the camera path in a 3D orbit view (floor grid, speed-coloured polyline,
// keyframe dots, and a marker at the current playback head) at orbit angle yaw/pitch and zoom (1 =
// auto-fit). Perspective gives the depth a flat overlay can't. Mirrors the desktop preview.
func (r *Renderer) RenderPathOrbit(g CamPathGeom, yaw, pitch, zoom float32, t float64, playing bool) *image.NRGBA {
	const px = pathPreviewPx
	img := image.NewNRGBA(image.Rect(0, 0, px, px))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.NRGBA{R: 10, G: 10, B: 14, A: 235}), image.Point{}, draw.Src)
	Border(img, colName, 3)
	if len(g.Pts) < 2 {
		return img
	}
	lo, hi := g.Pts[0], g.Pts[0]
	for _, p := range g.Pts {
		for i := 0; i < 3; i++ {
			if p[i] < lo[i] {
				lo[i] = p[i]
			}
			if p[i] > hi[i] {
				hi[i] = p[i]
			}
		}
	}
	center := [3]float32{(lo[0] + hi[0]) / 2, (lo[1] + hi[1]) / 2, (lo[2] + hi[2]) / 2}
	floorY := lo[1]
	diag := f32sqrt(sqf(hi[0]-lo[0]) + sqf(hi[1]-lo[1]) + sqf(hi[2]-lo[2]))
	if zoom < 0.3 {
		zoom = 0.3
	} else if zoom > 4 {
		zoom = 4
	}
	dist := (diag*1.3 + 1.5) * zoom
	gridR := float32(math.Max(1, float64((hi[0]-lo[0]+hi[2]-lo[2])/2)))
	cy, sy := f32cos(yaw), f32sin(yaw)
	cp, sp := f32cos(pitch), f32sin(pitch)
	project := func(p [3]float32) (int, int) {
		dx, dy, dz := p[0]-center[0], p[1]-center[1], p[2]-center[2]
		x1 := dx*cy + dz*sy
		z1 := -dx*sy + dz*cy
		y2 := dy*cp - z1*sp
		z2 := dy*sp + z1*cp
		depth := dist - z2
		if depth < 0.15 {
			depth = 0.15
		}
		f := float32(px) * 0.85
		return int(float32(px)/2 + f*x1/depth), int(float32(px)/2 - f*y2/depth)
	}
	// Floor grid for spatial reference.
	const n = 6
	step := (2 * gridR) / n
	for i := 0; i <= n; i++ {
		d := -gridR + step*float32(i)
		a1, b1 := project([3]float32{center[0] - gridR, floorY, center[2] + d})
		a2, b2 := project([3]float32{center[0] + gridR, floorY, center[2] + d})
		wpLine(img, a1, b1, a2, b2, wpGrid)
		c1, e1 := project([3]float32{center[0] + d, floorY, center[2] - gridR})
		c2, e2 := project([3]float32{center[0] + d, floorY, center[2] + gridR})
		wpLine(img, c1, e1, c2, e2, wpGrid)
	}
	// Path polyline, speed-coloured (slow → mint, fast → pink), with keyframe dots.
	maxSpd := float32(0.1)
	for _, s := range g.Spd {
		if s > maxSpd {
			maxSpd = s
		}
	}
	var lx, ly int
	for i, p := range g.Pts {
		x, y := project(p)
		if i > 0 {
			spd := float32(0)
			if i-1 < len(g.Spd) {
				spd = g.Spd[i-1]
			}
			wpLine(img, lx, ly, x, y, wpSpeedColor(spd/maxSpd))
		}
		lx, ly = x, y
		wpDot(img, x, y, 3, wpNode)
	}
	// Marker flying the path at the playback head.
	mx, my := project(samplePath(g, t))
	wpDot(img, mx, my, 6, colName)
	// HUD: time + play state.
	total := pathTotal(g.Dur)
	state := "paused"
	if playing {
		state = "playing"
	}
	drawText(img, r.body, fmt.Sprintf("%.1f / %.1fs  %s", t, total, state), 14, px-16, colText)
	return img
}

// samplePath returns the interpolated position at time t along the path (using per-segment durations).
func samplePath(g CamPathGeom, t float64) [3]float32 {
	if len(g.Pts) == 1 {
		return g.Pts[0]
	}
	acc := 0.0
	for i := 0; i < len(g.Pts)-1; i++ {
		seg := segDur(g.Dur, i)
		if t <= acc+seg || i == len(g.Pts)-2 {
			u := float32(0)
			if seg > 0 {
				u = clampF32(float32((t-acc)/seg), 0, 1)
			}
			a, b := g.Pts[i], g.Pts[i+1]
			return [3]float32{a[0] + (b[0]-a[0])*u, a[1] + (b[1]-a[1])*u, a[2] + (b[2]-a[2])*u}
		}
		acc += seg
	}
	return g.Pts[len(g.Pts)-1]
}

// pathTotal sums the per-segment durations (min 0.05s each so a zero-duration path still plays).
func pathTotal(dur []float32) float64 {
	total := 0.0
	for i := 0; i < len(dur); i++ {
		total += segDur(dur, i)
	}
	return total
}

func segDur(dur []float32, i int) float64 {
	if i < len(dur) && dur[i] > 0.05 {
		return float64(dur[i])
	}
	return 0.05
}

func sqf(v float32) float32       { return v * v }
func f32sqrt(v float32) float32   { return float32(math.Sqrt(float64(v))) }
func f32cos(v float32) float32    { return float32(math.Cos(float64(v))) }
func f32sin(v float32) float32    { return float32(math.Sin(float64(v))) }
func f32pow(b, e float32) float32 { return float32(math.Pow(float64(b), float64(e))) }

var (
	wpGrid = color.NRGBA{R: 40, G: 40, B: 52, A: 180}
	wpNode = color.NRGBA{R: 0xFF, G: 0xB5, B: 0x47, A: 255}
)

func wpSpeedColor(f float32) color.NRGBA {
	if f < 0 {
		f = 0
	} else if f > 1 {
		f = 1
	}
	return color.NRGBA{R: uint8(8 + 247*f), G: uint8(247 - 100*f), B: uint8(155 - 100*f), A: 255}
}

// wpLine draws a Bresenham line (bounded) into the overlay, slightly thickened.
func wpLine(img *image.NRGBA, x0, y0, x1, y1 int, col color.NRGBA) {
	dx := abs(x1 - x0)
	dy := -abs(y1 - y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	for i := 0; i < 8000; i++ {
		if x0 >= 0 && x0 < img.Rect.Dx() && y0 >= 0 && y0 < img.Rect.Dy() {
			img.SetNRGBA(x0, y0, col)
			if x0+1 < img.Rect.Dx() {
				img.SetNRGBA(x0+1, y0, col)
			}
		}
		if x0 == x1 && y0 == y1 {
			return
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func wpDot(img *image.NRGBA, cx, cy, r int, col color.NRGBA) {
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if dx*dx+dy*dy > r*r {
				continue
			}
			x, y := cx+dx, cy+dy
			if x >= 0 && x < img.Rect.Dx() && y >= 0 && y < img.Rect.Dy() {
				img.SetNRGBA(x, y, col)
			}
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
