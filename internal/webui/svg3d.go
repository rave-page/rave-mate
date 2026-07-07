package webui

// Orbit-camera 3D → SVG projection shared by the Motion tab previews (camera paths,
// motion skeleton). Same math as the Fyne raster views (view_campaths/view_motion):
// yaw/pitch orbit around a framed center, perspective divide, screen-space SVG output.

import (
	"fmt"
	"math"
	"strings"
)

type orbitCam struct {
	yaw, pitch, dist float32
	center           [3]float32
	floorY, gridR    float32
	framed           bool
}

func newOrbitCam() orbitCam { return orbitCam{yaw: 0.6, pitch: 0.35, dist: 4, gridR: 2} }

// frame fits the orbit around bounds (mul/add tune the distance like the Fyne views).
func (c *orbitCam) frame(lo, hi [3]float32, mul, add float32) {
	c.center = [3]float32{(lo[0] + hi[0]) / 2, (lo[1] + hi[1]) / 2, (lo[2] + hi[2]) / 2}
	c.floorY = lo[1]
	diag := float32(math.Sqrt(float64(sq3(hi[0]-lo[0]) + sq3(hi[1]-lo[1]) + sq3(hi[2]-lo[2]))))
	c.gridR = float32(math.Max(1, float64((hi[0]-lo[0]+hi[2]-lo[2])/2)))
	c.dist = diag*mul + add
	c.framed = true
}

// project maps a world point to SVG coords for a w×h viewport.
func (c *orbitCam) project(p [3]float32, w, h float32) (float32, float32) {
	dx, dy, dz := p[0]-c.center[0], p[1]-c.center[1], p[2]-c.center[2]
	cy, sy := float32(math.Cos(float64(c.yaw))), float32(math.Sin(float64(c.yaw)))
	x1 := dx*cy + dz*sy
	z1 := -dx*sy + dz*cy
	cp, sp := float32(math.Cos(float64(c.pitch))), float32(math.Sin(float64(c.pitch)))
	y2 := dy*cp - z1*sp
	z2 := dy*sp + z1*cp
	depth := c.dist - z2
	if depth < 0.15 {
		depth = 0.15
	}
	f := h * 0.9
	return w/2 + f*x1/depth, h/2 - f*y2/depth
}

// orbitBy applies drag deltas (fractions of the viewport) + clamps pitch.
func (c *orbitCam) orbitBy(dfx, dfy float32) {
	c.yaw -= dfx * 6
	c.pitch = clampf32(c.pitch+dfy*6, -1.45, 1.45)
}

// zoomBy applies one wheel step (in = closer).
func (c *orbitCam) zoomBy(in bool, lo, hi float32) {
	f := float32(1.12)
	if in {
		f = 1 / f
	}
	c.dist = clampf32(c.dist*f, lo, hi)
}

// gridSVG draws the X-Z floor grid at floorY (spatial reference).
func (c *orbitCam) gridSVG(w, h float32) string {
	var b strings.Builder
	const n = 6
	step := (2 * c.gridR) / n
	for i := 0; i <= n; i++ {
		d := -c.gridR + step*float32(i)
		x1, y1 := c.project([3]float32{c.center[0] - c.gridR, c.floorY, c.center[2] + d}, w, h)
		x2, y2 := c.project([3]float32{c.center[0] + c.gridR, c.floorY, c.center[2] + d}, w, h)
		b.WriteString(svgLine(x1, y1, x2, y2, "var(--rp-faint,#3c414d)", 1))
		x3, y3 := c.project([3]float32{c.center[0] + d, c.floorY, c.center[2] - c.gridR}, w, h)
		x4, y4 := c.project([3]float32{c.center[0] + d, c.floorY, c.center[2] + c.gridR}, w, h)
		b.WriteString(svgLine(x3, y3, x4, y4, "var(--rp-faint,#3c414d)", 1))
	}
	return b.String()
}

func svgLine(x1, y1, x2, y2 float32, stroke string, sw float32) string {
	return fmt.Sprintf(`<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="%.1f"/>`,
		x1, y1, x2, y2, stroke, sw)
}

func svgDisc(x, y, r float32, fill string) string {
	return fmt.Sprintf(`<circle cx="%.1f" cy="%.1f" r="%.1f" fill="%s"/>`, x, y, r, fill)
}

// speedHex maps 0..1 → mint (slow) → pink (fast), mirroring the Fyne speedColor ramp.
func speedHex(f float32) string {
	f = clampf32(f, 0, 1)
	return fmt.Sprintf("#%02x%02x%02x", uint8(8+247*f), uint8(247-100*f), uint8(155-100*f))
}

func clampf32(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func sq3(v float32) float32 { return v * v }
