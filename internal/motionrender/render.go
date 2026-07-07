// Package motionrender renders VR motion scenes (floor grid, head trail, stick-figure
// skeleton or a posed VRM avatar mesh) to images - pure CPU, stdlib image only.
// Shared core for the webview motion-studio preview and the offline video render
// pipeline (C5). Extracted from the Fyne motionView (internal/ui/view_motion.go +
// raster3d.go), which retires with the webview cutover.
package motionrender

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"rave.page/mate/internal/vrm"
	"rave.page/mate/internal/vrmdyn"
	"rave.page/mate/internal/vrmik"
	"rave.page/mate/internal/vrmotion"
)

// Camera is an orbit camera: yaw/pitch around Center at Dist, floor grid at FloorY.
type Camera struct {
	Yaw, Pitch, Dist float32
	Center           [3]float32
	FloorY, GridR    float32
}

// FrameModel points the camera at a model's rest bounds (Fyne frameModel port).
func (c *Camera) FrameModel(m *vrm.Model) {
	lo, hi := m.Bounds()
	c.Center = [3]float32{(lo[0] + hi[0]) / 2, (lo[1] + hi[1]) / 2, (lo[2] + hi[2]) / 2}
	c.FloorY = lo[1]
	diag := float32(math.Sqrt(float64(sq(hi[0]-lo[0]) + sq(hi[1]-lo[1]) + sq(hi[2]-lo[2]))))
	c.GridR = float32(math.Max(1, float64((hi[0]-lo[0]+hi[2]-lo[2])/2)))
	c.Dist = diag*1.6 + 1.0
}

// project maps a world point to screen px + depth (nearer = smaller).
func (c Camera) project(p [3]float32, w, h int) (int, int, float32) {
	dx, dy, dz := p[0]-c.Center[0], p[1]-c.Center[1], p[2]-c.Center[2]
	cy, sy := float32(math.Cos(float64(c.Yaw))), float32(math.Sin(float64(c.Yaw)))
	x1 := dx*cy + dz*sy
	z1 := -dx*sy + dz*cy
	cp, sp := float32(math.Cos(float64(c.Pitch))), float32(math.Sin(float64(c.Pitch)))
	y2 := dy*cp - z1*sp
	z2 := dy*sp + z1*cp
	depth := c.Dist - z2
	if depth < 0.15 {
		depth = 0.15
	}
	f := float32(h) * 0.9
	return int(float32(w)/2 + f*x1/depth), int(float32(h)/2 - f*y2/depth), depth
}

// Frame is one render request.
type Frame struct {
	W, H   int
	Cam    Camera
	Model  *vrm.Model            // nil = stick-figure mode
	Sample map[int]vrmotion.Pose // pose at the frame's time (nil = rest pose / no skeleton)
	Trail  [][3]float32          // head path, world space
	Name   string                // caption (bottom-left), "" = none
	TriCap int                   // max mesh triangles per frame; 0 = 40000
	Dyn    *vrmdyn.State         // secondary-motion sim (hair/tail); nil = rigid
	DT     float64               // seconds since previous frame (0 = re-render, no integration)
	RT     *vrmik.Retarget       // per-take calibration (recenter/scale/roles); nil = raw take
	Marks  map[int]vrmotion.Pose // take-space tracker points drawn OVER the mesh (mesh mode only) -
	// lets the user compare the posed mesh against the raw stick-point take. nil = none.
}

// DefaultTriCap bounds mesh rasterization so a heavy avatar can't stall a frame. Skipping
// triangles punches visible holes in solid surfaces (whatever is behind shows through), so the
// cap sits above typical VRChat avatars (~100-150k tris ≈ 19ms at 640×400); it's a runaway
// guard, not a quality dial.
const DefaultTriCap = 200000

var modelLight = [3]float32{0.40, 0.82, 0.41} // fixed world light (unit)

var (
	colBG     = color.NRGBA{R: 10, G: 10, B: 14, A: 255}
	colGrid   = color.NRGBA{R: 34, G: 34, B: 46, A: 255}
	colTrail  = color.NRGBA{R: 0x4a, G: 0x2a, B: 0x48, A: 255}
	colHead   = color.NRGBA{R: 0xF7, G: 0x08, B: 0x64, A: 255} // brand pink
	colTrk    = color.NRGBA{R: 0x08, G: 0xF7, B: 0x9B, A: 255} // brand mint
	colText   = color.NRGBA{R: 0xc8, G: 0xc8, B: 0xd0, A: 255}
	colAvatar = color.NRGBA{R: 0x9A, G: 0x7A, B: 0xE0, A: 255} // brand-violet-ish
)

// Render draws one frame. Model mode poses the mesh from Sample via vrmik (rest pose
// when Sample is nil); otherwise draws the skeleton (head disc + head→tracker bones).
func Render(f Frame) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, f.W, f.H))
	fillImg(img, colBG)
	c := f.Cam

	// floor grid (X-Z at FloorY)
	const n = 6
	step := (2 * c.GridR) / n
	for i := 0; i <= n; i++ {
		d := -c.GridR + step*float32(i)
		a1, b1, _ := c.project([3]float32{c.Center[0] - c.GridR, c.FloorY, c.Center[2] + d}, f.W, f.H)
		a2, b2, _ := c.project([3]float32{c.Center[0] + c.GridR, c.FloorY, c.Center[2] + d}, f.W, f.H)
		drawLine(img, image.Pt(a1, b1), image.Pt(a2, b2), colGrid)
		c1, e1, _ := c.project([3]float32{c.Center[0] + d, c.FloorY, c.Center[2] - c.GridR}, f.W, f.H)
		c2, e2, _ := c.project([3]float32{c.Center[0] + d, c.FloorY, c.Center[2] + c.GridR}, f.W, f.H)
		drawLine(img, image.Pt(c1, e1), image.Pt(c2, e2), colGrid)
	}

	// head trail (avatar space in model mode so it aligns with the posed mesh)
	var prev image.Point
	for i, p := range f.Trail {
		if f.Model != nil {
			p = f.RT.Conv(p)
		}
		sx, sy, _ := c.project(p, f.W, f.H)
		cur := image.Pt(sx, sy)
		if i > 0 {
			drawLine(img, prev, cur, colTrail)
		}
		prev = cur
	}

	if f.Model != nil {
		renderModel(img, f)
		drawMarks(img, f)
	} else if f.Sample != nil {
		head, hasHead := f.Sample[0]
		var hpt image.Point
		if hasHead {
			hx, hy, _ := c.project(head.Pos, f.W, f.H)
			hpt = image.Pt(hx, hy)
		}
		for key, p := range f.Sample {
			sx, sy, depth := c.project(p.Pos, f.W, f.H)
			pt := image.Pt(sx, sy)
			col, base := colTrk, 5
			if key == 0 {
				col, base = colHead, 8
			} else if hasHead {
				drawLine(img, hpt, pt, colTrk)
			}
			r := min(max(int(float32(base)*(c.Dist/depth)), 2), 18)
			drawDisc(img, pt, r, col)
		}
	}
	if f.Name != "" {
		drawText(img, f.Name, 12, f.H-14, colText)
	}
	return img
}

// renderModel poses + rasterizes the skinned mesh: textured + smooth-shaded when the mesh
// carries UVs/normals (FBX), flat-shaded otherwise; depth-buffered, downsampled to TriCap.
func renderModel(img *image.NRGBA, f Frame) {
	m := f.Model
	local := vrmik.PoseRT(m, f.Sample, f.RT)
	if f.Dyn != nil {
		f.Dyn.Step(m, local, f.DT)
	}
	world := m.WorldFrom(local)
	skin := m.SkinMatrices(world)
	db := newDepthBuffer(f.W, f.H)

	cap := f.TriCap
	if cap <= 0 {
		cap = DefaultTriCap
	}
	total := 0
	for mi := range m.Meshes {
		total += len(m.Meshes[mi].Indices) / 3
	}
	tstep := 1
	if total > cap {
		tstep = total/cap + 1
	}
	for mi := range m.Meshes {
		mesh := &m.Meshes[mi]
		pts := m.PosedPositions(mi, world, skin)
		nrm := m.PosedNormals(mi, world, skin)
		base := mesh.Diffuse
		if base.A == 0 {
			base = colAvatar
		}
		shaded := nrm != nil || mesh.Tex != nil
		idx := mesh.Indices
		for i := 0; i+2 < len(idx); i += 3 * tstep {
			i0, i1, i2 := idx[i], idx[i+1], idx[i+2]
			p0, p1, p2 := pts[i0], pts[i1], pts[i2]
			x0, y0, z0 := f.Cam.project(p0, f.W, f.H)
			x1, y1, z1 := f.Cam.project(p1, f.W, f.H)
			x2, y2, z2 := f.Cam.project(p2, f.W, f.H)
			if !shaded {
				col := shadeFlat(base, faceNormal(p0, p1, p2), modelLight)
				fillTriangle(img, db, projVert{x0, y0, z0}, projVert{x1, y1, z1}, projVert{x2, y2, z2}, col)
				continue
			}
			a := sVert{X: x0, Y: y0, Z: z0}
			b := sVert{X: x1, Y: y1, Z: z1}
			c := sVert{X: x2, Y: y2, Z: z2}
			if mesh.UV != nil {
				a.U, a.V = mesh.UV[i0][0], mesh.UV[i0][1]
				b.U, b.V = mesh.UV[i1][0], mesh.UV[i1][1]
				c.U, c.V = mesh.UV[i2][0], mesh.UV[i2][1]
			}
			if nrm != nil {
				a.N, b.N, c.N = nrm[i0], nrm[i1], nrm[i2]
			} else {
				fn := faceNormal(p0, p1, p2)
				a.N, b.N, c.N = fn, fn, fn
			}
			fillTriShaded(img, db, a, b, c, mesh.Tex, base, modelLight)
		}
	}
}

// drawMarks overlays the take's tracker points (converted through the same retarget as the
// mesh) on a model render - head pink, trackers mint, matching the stick-figure palette.
// Drawn depth-free on top so occluded points stay visible for comparison.
func drawMarks(img *image.NRGBA, f Frame) {
	if f.Marks == nil {
		return
	}
	c := f.Cam
	for key, p := range f.Marks {
		sx, sy, depth := c.project(f.RT.Conv(p.Pos), f.W, f.H)
		col, base := colTrk, 4
		if key == 0 {
			col, base = colHead, 6
		}
		r := min(max(int(float32(base)*(c.Dist/depth)), 2), 14)
		drawDisc(img, image.Pt(sx, sy), r, col)
	}
}

// PNGBase64 renders a frame and returns it as a base64 PNG (data-URI payload).
func PNGBase64(f Frame) string {
	var buf bytes.Buffer
	_ = png.Encode(&buf, Render(f))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func drawText(img *image.NRGBA, s string, x, y int, col color.NRGBA) {
	d := &font.Drawer{Dst: img, Src: image.NewUniform(col), Face: basicfont.Face7x13, Dot: fixed.P(x, y+11)}
	d.DrawString(s)
}

func fillImg(img *image.NRGBA, c color.NRGBA) {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}

func drawDisc(img *image.NRGBA, c image.Point, r int, col color.NRGBA) {
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			if dx*dx+dy*dy <= r*r {
				img.SetNRGBA(c.X+dx, c.Y+dy, col)
			}
		}
	}
}

func drawLine(img *image.NRGBA, a, b image.Point, col color.NRGBA) {
	dx, dy := absi(b.X-a.X), -absi(b.Y-a.Y)
	sx, sy := 1, 1
	if a.X > b.X {
		sx = -1
	}
	if a.Y > b.Y {
		sy = -1
	}
	err := dx + dy
	x, y := a.X, a.Y
	for range 5000 { // bound the walk (offscreen-safe)
		img.SetNRGBA(x, y, col)
		if x == b.X && y == b.Y {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x += sx
		}
		if e2 <= dx {
			err += dx
			y += sy
		}
	}
}

func absi(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
func sq(v float32) float32 { return v * v }
