package motionrender

// 360°×180° equirectangular rendering for the offline motion→video pipeline (C5):
// rasterize 6 cube faces (90° FOV look-cameras at EqFrame.Eye) with the shared raster
// primitives, then remap face pixels onto the equirect grid via a precomputed table.
// Pure CPU, offline-paced; TriCap bounds per-frame mesh cost.

import (
	"image"
	"image/color"
	"math"

	"rave.page/mate/internal/vrm"
	"rave.page/mate/internal/vrmdyn"
	"rave.page/mate/internal/vrmik"
	"rave.page/mate/internal/vrmotion"
)

// EqFrame is one 360° render request. Eye/Center/Trail are take (OpenVR) space; model
// mode converts them to avatar space internally (mirrors Frame.Trail in Render).
type EqFrame struct {
	Eye    [3]float32 // viewpoint (typically scene center at head height)
	Center [3]float32 // floor-grid center
	FloorY float32
	GridR  float32
	Model  *vrm.Model            // nil = stick-figure mode
	Sample map[int]vrmotion.Pose // pose at the frame's time
	Trail  [][3]float32          // head path, take space
	Name   string                // caption (bottom-left), "" = none
	TriCap int                   // max mesh triangles per frame; 0 = DefaultTriCap
	Dyn    *vrmdyn.State         // secondary-motion sim (hair/tail); nil = rigid
	DT     float64               // seconds since previous frame (0 = re-render, no integration)
	RT     *vrmik.Retarget       // per-take calibration (recenter/scale/roles); nil = raw take
}

// eqNear is the look-camera near plane (m); geometry closer than this is clipped.
const eqNear = 0.05

// eqFaces are the 6 cube-face bases (screen x along right, y along up, depth along fwd).
// Remap + face render share this table, so orientation consistency is automatic.
var eqFaces = [6]struct{ right, up, fwd [3]float32 }{
	{[3]float32{0, 0, -1}, [3]float32{0, 1, 0}, [3]float32{1, 0, 0}},  // +X
	{[3]float32{0, 0, 1}, [3]float32{0, 1, 0}, [3]float32{-1, 0, 0}},  // -X
	{[3]float32{1, 0, 0}, [3]float32{0, 0, -1}, [3]float32{0, 1, 0}},  // +Y (up)
	{[3]float32{1, 0, 0}, [3]float32{0, 0, 1}, [3]float32{0, -1, 0}},  // -Y (down)
	{[3]float32{1, 0, 0}, [3]float32{0, 1, 0}, [3]float32{0, 0, 1}},   // +Z
	{[3]float32{-1, 0, 0}, [3]float32{0, 1, 0}, [3]float32{0, 0, -1}}, // -Z
}

// remapRef points one equirect pixel at a face pixel (byte offset into faces[face].Pix).
type remapRef struct {
	off  int32
	face uint8
}

// EquirectRenderer renders equirect frames of a fixed size, caching face buffers, the
// depth buffer and the pixel remap. NOT safe for concurrent use. The returned image is
// REUSED by the next Render call - encode/copy it before rendering again.
type EquirectRenderer struct {
	w, h, s int // output size + cube-face px (s ≈ w/4 matches equator pixel density)
	faces   [6]*image.NRGBA
	depth   *depthBuffer
	out     *image.NRGBA
	remap   []remapRef
	clipBuf []clipVert
	triBuf  []eqTri
}

// NewEquirectRenderer builds a renderer for a w×h output (w should be 2h for true 360°).
func NewEquirectRenderer(w, h int) *EquirectRenderer {
	w, h = max(w, 8), max(h, 4)
	s := max(32, w/4)
	r := &EquirectRenderer{w: w, h: h, s: s, depth: newDepthBuffer(s, s), out: image.NewNRGBA(image.Rect(0, 0, w, h))}
	for i := range r.faces {
		r.faces[i] = image.NewNRGBA(image.Rect(0, 0, s, s))
	}
	r.remap = make([]remapRef, w*h)
	for py := range h {
		lat := math.Pi/2 - (float64(py)+0.5)/float64(h)*math.Pi
		cl, sl := math.Cos(lat), math.Sin(lat)
		for px := range w {
			lon := (float64(px)+0.5)/float64(w)*2*math.Pi - math.Pi
			d := [3]float32{float32(cl * math.Sin(lon)), float32(sl), float32(-cl * math.Cos(lon))} // lon=0 → -Z
			fi, u, v := faceUV(d)
			fx := clampi(int((u*0.5+0.5)*float32(s)), 0, s-1)
			fy := clampi(int((0.5-v*0.5)*float32(s)), 0, s-1)
			r.remap[py*w+px] = remapRef{off: int32((fy*s + fx) * 4), face: uint8(fi)}
		}
	}
	return r
}

// faceUV picks the face whose forward axis best matches direction d (max dot > 0 always)
// and returns the in-face plane coords (both in [-1,1] on the chosen face).
func faceUV(d [3]float32) (int, float32, float32) {
	best, bd := 0, float32(-2)
	for i := range eqFaces {
		if w := dot3(d, eqFaces[i].fwd); w > bd {
			best, bd = i, w
		}
	}
	f := eqFaces[best]
	return best, dot3(d, f.right) / bd, dot3(d, f.up) / bd
}

// Render draws one 360° frame. See EquirectRenderer for buffer-reuse semantics.
func (r *EquirectRenderer) Render(f EqFrame) *image.NRGBA {
	eye, center, trail := f.Eye, f.Center, f.Trail
	if f.Model != nil { // avatar space (mirrors Render's trail handling)
		eye, center = f.RT.Conv(eye), f.RT.Conv(center)
		conv := make([][3]float32, len(f.Trail))
		for i, p := range f.Trail {
			conv[i] = f.RT.Conv(p)
		}
		trail = conv
	}
	var tris []eqTri
	if f.Model != nil {
		r.triBuf = meshTris(f, r.triBuf[:0])
		tris = r.triBuf
	}
	for fi := range r.faces {
		r.renderFace(fi, f, eye, center, trail, tris)
	}
	r.composite()
	if f.Name != "" {
		drawText(r.out, f.Name, 12, r.h-14, colText)
	}
	return r.out
}

// eqTri is one world-space (avatar-space) triangle with shading attributes (world normals,
// FBX-convention texcoords, diffuse texture/color).
type eqTri struct {
	v    [3][3]float32
	uv   [3][2]float32
	n    [3][3]float32
	tex  *image.NRGBA
	base color.NRGBA
}

// meshTris poses the mesh ONCE per frame (shared by all 6 faces) and collects attributed
// triangles, downsampled to TriCap like renderModel.
func meshTris(f EqFrame, out []eqTri) []eqTri {
	m := f.Model
	local := vrmik.PoseRT(m, f.Sample, f.RT)
	if f.Dyn != nil {
		f.Dyn.Step(m, local, f.DT)
	}
	world := m.WorldFrom(local)
	skin := m.SkinMatrices(world)
	tcap := f.TriCap
	if tcap <= 0 {
		tcap = DefaultTriCap
	}
	total := 0
	for mi := range m.Meshes {
		total += len(m.Meshes[mi].Indices) / 3
	}
	tstep := 1
	if total > tcap {
		tstep = total/tcap + 1
	}
	for mi := range m.Meshes {
		mesh := &m.Meshes[mi]
		pts := m.PosedPositions(mi, world, skin)
		nrm := m.PosedNormals(mi, world, skin)
		base := mesh.Diffuse
		if base.A == 0 {
			base = colAvatar
		}
		idx := mesh.Indices
		for i := 0; i+2 < len(idx); i += 3 * tstep {
			i0, i1, i2 := idx[i], idx[i+1], idx[i+2]
			p0, p1, p2 := pts[i0], pts[i1], pts[i2]
			t := eqTri{v: [3][3]float32{p0, p1, p2}, tex: mesh.Tex, base: base}
			if mesh.UV != nil {
				t.uv = [3][2]float32{mesh.UV[i0], mesh.UV[i1], mesh.UV[i2]}
			}
			if nrm != nil {
				t.n = [3][3]float32{nrm[i0], nrm[i1], nrm[i2]}
			} else {
				fn := faceNormal(p0, p1, p2)
				t.n = [3][3]float32{fn, fn, fn}
			}
			out = append(out, t)
		}
	}
	return out
}

// renderFace draws the scene (grid, trail, skeleton or mesh) onto cube face fi.
func (r *EquirectRenderer) renderFace(fi int, f EqFrame, eye, center [3]float32, trail [][3]float32, tris []eqTri) {
	img := r.faces[fi]
	fillImg(img, colBG)
	r.depth.reset()
	cam := lookCam{eye: eye, right: eqFaces[fi].right, up: eqFaces[fi].up, fwd: eqFaces[fi].fwd, s: r.s}

	// floor grid (X-Z at FloorY, centered on the grid center)
	gr := f.GridR
	if gr <= 0 {
		gr = 2
	}
	const n = 6
	step := (2 * gr) / n
	for i := 0; i <= n; i++ {
		d := -gr + step*float32(i)
		seg3(img, cam, [3]float32{center[0] - gr, f.FloorY, center[2] + d}, [3]float32{center[0] + gr, f.FloorY, center[2] + d}, colGrid)
		seg3(img, cam, [3]float32{center[0] + d, f.FloorY, center[2] - gr}, [3]float32{center[0] + d, f.FloorY, center[2] + gr}, colGrid)
	}

	for i := 1; i < len(trail); i++ {
		seg3(img, cam, trail[i-1], trail[i], colTrail)
	}

	if f.Model == nil && f.Sample != nil {
		head, hasHead := f.Sample[0]
		for key, p := range f.Sample {
			if key != 0 && hasHead {
				seg3(img, cam, head.Pos, p.Pos, colTrk)
			}
			sx, sy, depth, ok := cam.project(p.Pos)
			if !ok {
				continue
			}
			col, base := colTrk, 5
			if key == 0 {
				col, base = colHead, 8
			}
			// 1.5m reference distance sizes discs like the orbit view at typical framing
			drawDisc(img, image.Pt(sx, sy), clampi(int(float32(base)*1.5/depth), 2, 18), col)
		}
	}
	for i := range tris {
		r.clipBuf = drawTriFace(img, r.depth, cam, tris[i], r.clipBuf)
	}
}

// composite remaps face pixels onto the equirect output (total mapping: every output
// pixel reads exactly one face pixel - no seams).
func (r *EquirectRenderer) composite() {
	for i, m := range r.remap {
		copy(r.out.Pix[i*4:i*4+4], r.faces[m.face].Pix[m.off:m.off+4])
	}
}

// ── look camera (position + orthonormal basis, 90° FOV square face) ──────────

type lookCam struct {
	eye, right, up, fwd [3]float32
	s                   int
}

// toCam transforms a world point into camera space (x right, y up, z forward).
func (c lookCam) toCam(p [3]float32) (x, y, z float32) {
	d := [3]float32{p[0] - c.eye[0], p[1] - c.eye[1], p[2] - c.eye[2]}
	return dot3(d, c.right), dot3(d, c.up), dot3(d, c.fwd)
}

// projCamF maps camera-space (z>0) to face px (float, 90° FOV).
func (c lookCam) projCamF(x, y, z float32) (float32, float32) {
	h := float32(c.s) / 2
	return h + h*x/z, h - h*y/z
}

// project maps a world point to face px + depth; ok=false when behind the near plane.
func (c lookCam) project(p [3]float32) (int, int, float32, bool) {
	x, y, z := c.toCam(p)
	if z < eqNear {
		return 0, 0, 0, false
	}
	fx, fy := c.projCamF(x, y, z)
	return int(fx), int(fy), z, true
}

// seg3 draws a world-space segment: near-clip in camera space, project, 2D-clip to the
// face (Liang-Barsky) so drawLine's bounded walk always covers the visible span.
func seg3(img *image.NRGBA, cam lookCam, a, b [3]float32, col color.NRGBA) {
	ax, ay, az := cam.toCam(a)
	bx, by, bz := cam.toCam(b)
	if az < eqNear && bz < eqNear {
		return
	}
	if az < eqNear {
		t := (eqNear - az) / (bz - az)
		ax, ay, az = ax+(bx-ax)*t, ay+(by-ay)*t, eqNear
	} else if bz < eqNear {
		t := (eqNear - bz) / (az - bz)
		bx, by, bz = bx+(ax-bx)*t, by+(ay-by)*t, eqNear
	}
	x0, y0 := cam.projCamF(ax, ay, az)
	x1, y1 := cam.projCamF(bx, by, bz)
	if p0, p1, ok := clipSeg2D(x0, y0, x1, y1, cam.s); ok {
		drawLine(img, p0, p1, col)
	}
}

// clipSeg2D clips a segment to the face rect (+1px pad), Liang-Barsky.
func clipSeg2D(x0, y0, x1, y1 float32, s int) (image.Point, image.Point, bool) {
	t0, t1 := float32(0), float32(1)
	dx, dy := x1-x0, y1-y0
	clip := func(p, q float32) bool {
		if p == 0 {
			return q >= 0
		}
		r := q / p
		if p < 0 {
			if r > t1 {
				return false
			}
			if r > t0 {
				t0 = r
			}
		} else {
			if r < t0 {
				return false
			}
			if r < t1 {
				t1 = r
			}
		}
		return true
	}
	lo, hi := float32(-1), float32(s)+1
	if !(clip(-dx, x0-lo) && clip(dx, hi-x0) && clip(-dy, y0-lo) && clip(dy, hi-y0)) {
		return image.Point{}, image.Point{}, false
	}
	return image.Pt(int(x0+t0*dx), int(y0+t0*dy)), image.Pt(int(x0+t1*dx), int(y0+t1*dy)), true
}

// clipVert is a camera-space vertex + interpolable shading attributes for near-clipping.
type clipVert struct {
	p  [3]float32
	uv [2]float32
	n  [3]float32
}

// drawTriFace near-clips a world triangle in camera space (0..4 verts), fan-triangulates
// and rasterizes depth-tested with perspective-correct texture/normal shading. buf is the
// caller's clip scratch (returned for reuse).
func drawTriFace(img *image.NRGBA, db *depthBuffer, cam lookCam, t eqTri, buf []clipVert) []clipVert {
	var cv [3]clipVert
	for i := range 3 {
		cv[i].p[0], cv[i].p[1], cv[i].p[2] = cam.toCam(t.v[i])
		cv[i].uv, cv[i].n = t.uv[i], t.n[i]
	}
	poly := clipTriNear(cv, buf[:0])
	if len(poly) < 3 {
		return poly
	}
	var pv [4]sVert
	for i, p := range poly {
		fx, fy := cam.projCamF(p.p[0], p.p[1], p.p[2])
		pv[i] = sVert{X: int(fx), Y: int(fy), Z: p.p[2], U: p.uv[0], V: p.uv[1], N: p.n}
	}
	for i := 2; i < len(poly); i++ {
		fillTriShaded(img, db, pv[0], pv[i-1], pv[i], t.tex, t.base, modelLight)
	}
	return poly
}

// clipTriNear clips a camera-space triangle against z=eqNear (Sutherland-Hodgman on one
// plane), lerping attributes; appends ≤4 verts to buf.
func clipTriNear(v [3]clipVert, buf []clipVert) []clipVert {
	for i := range 3 {
		a, b := v[i], v[(i+1)%3]
		ain, bin := a.p[2] >= eqNear, b.p[2] >= eqNear
		if ain {
			buf = append(buf, a)
		}
		if ain != bin {
			t := (eqNear - a.p[2]) / (b.p[2] - a.p[2])
			lerp3 := func(x, y [3]float32) [3]float32 {
				return [3]float32{x[0] + (y[0]-x[0])*t, x[1] + (y[1]-x[1])*t, x[2] + (y[2]-x[2])*t}
			}
			cv := clipVert{p: lerp3(a.p, b.p), n: lerp3(a.n, b.n)}
			cv.p[2] = eqNear
			cv.uv = [2]float32{a.uv[0] + (b.uv[0]-a.uv[0])*t, a.uv[1] + (b.uv[1]-a.uv[1])*t}
			buf = append(buf, cv)
		}
	}
	return buf
}

// reset re-initializes the depth buffer for the next face.
func (db *depthBuffer) reset() {
	for i := range db.z {
		db.z[i] = math.MaxFloat32
	}
}

func dot3(a, b [3]float32) float32 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }

func clampi(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
