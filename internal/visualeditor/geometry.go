package visualeditor

import "math"

// Direct-manipulation geometry: hit-testing, resize/rotate handles and snap
// resolution over the SAME affine model the compositor renders with
// (scale → rotate about the box center; see affine()). All coordinates are doc
// px. The exported entry points dispatch to the zigcore edgeo kernel when
// linked (geometry_zig.go, tag zigdsp); the *Go bodies here are the fallback +
// golden reference the kernel must match bit-for-bit on the flat-box form
// (parity gate: geometry_zig_parity_test.go).

// Handle identifies a manipulation handle on a selected leaf.
type Handle int

// Handle values (order matters - the zig kernel returns these ints).
const (
	HandleNone Handle = iota
	HandleNW
	HandleN
	HandleNE
	HandleE
	HandleSE
	HandleS
	HandleSW
	HandleW
	HandleRotate
)

// FlatBox is a leaf's doc-space placement snapshot (kernel-friendly form).
type FlatBox struct {
	X, Y, W, H float64 // untransformed box (Transform.X/Y + Layer.W/H)
	SX, SY     float64 // effective scale (0 folded to 1)
	Rot        float64 // degrees CW
	Locked     bool
}

// Guide is one active snap line (doc px).
type Guide struct {
	Vert bool // vertical line at X=Pos (else horizontal at Y=Pos)
	Pos  float64
}

func effScale(s float64) float64 {
	if s == 0 {
		return 1
	}
	return s
}

// FlattenLeaves resolves the visible, directly-manipulable leaves in document
// order (bottom→top). Children of transformed groups are excluded - their doc
// placement is a composed affine the box form can't express; identity-transform
// groups recurse. Invisible/zero-opacity layers are skipped like the preview.
func FlattenLeaves(d *Document) (boxes []FlatBox, ids []string) {
	var walk func(ls []*Layer)
	walk = func(ls []*Layer) {
		for _, l := range ls {
			if l == nil || !l.Visible || l.Opacity <= 0 {
				continue
			}
			if l.IsGroup() {
				t := l.Transform
				if t.X == 0 && t.Y == 0 && effScale(t.ScaleX) == 1 && effScale(t.ScaleY) == 1 && t.Rotation == 0 {
					walk(l.Children)
				}
				continue
			}
			boxes = append(boxes, FlatBox{
				X: l.Transform.X, Y: l.Transform.Y, W: l.W, H: l.H,
				SX: effScale(l.Transform.ScaleX), SY: effScale(l.Transform.ScaleY),
				Rot: l.Transform.Rotation, Locked: l.Locked,
			})
			ids = append(ids, l.ID)
		}
	}
	walk(d.Root.Children)
	return boxes, ids
}

// mat returns the content→doc rotation·scale matrix coefficients.
func (b FlatBox) mat() (ma, mb, mc, md float64) {
	rad := b.Rot * math.Pi / 180
	cos, sin := math.Cos(rad), math.Sin(rad)
	return b.SX * cos, -b.SY * sin, b.SX * sin, b.SY * cos
}

// Map transforms a content-space point to doc space.
func (b FlatBox) Map(cx, cy float64) (float64, float64) {
	ma, mb, mc, md := b.mat()
	dx, dy := cx-b.W/2, cy-b.H/2
	return b.X + b.W/2 + ma*dx + mb*dy, b.Y + b.H/2 + mc*dx + md*dy
}

// InvMap transforms a doc-space point to content space (false on degenerate scale).
func (b FlatBox) InvMap(px, py float64) (float64, float64, bool) {
	ma, mb, mc, md := b.mat()
	det := ma*md - mb*mc
	if math.Abs(det) < 1e-9 {
		return 0, 0, false
	}
	dx, dy := px-(b.X+b.W/2), py-(b.Y+b.H/2)
	return b.W/2 + (md*dx-mb*dy)/det, b.H/2 + (-mc*dx+ma*dy)/det, true
}

// Contains reports the doc point lying inside the transformed box.
func (b FlatBox) Contains(px, py float64) bool {
	cx, cy, ok := b.InvMap(px, py)
	return ok && cx >= 0 && cx <= b.W && cy >= 0 && cy <= b.H
}

// Bounds returns the doc-space axis-aligned bounds of the transformed box.
func (b FlatBox) Bounds() (minX, minY, maxX, maxY float64) {
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY = math.Inf(-1), math.Inf(-1)
	for _, p := range [4][2]float64{{0, 0}, {b.W, 0}, {0, b.H}, {b.W, b.H}} {
		x, y := b.Map(p[0], p[1])
		minX, maxX = math.Min(minX, x), math.Max(maxX, x)
		minY, maxY = math.Min(minY, y), math.Max(maxY, y)
	}
	return
}

// HitTest returns the topmost box index containing the point, or -1.
func HitTest(boxes []FlatBox, px, py float64) int {
	if zigGeo() {
		return zigHitTest(boxes, px, py)
	}
	return hitTestGo(boxes, px, py)
}

func hitTestGo(boxes []FlatBox, px, py float64) int {
	for i := len(boxes) - 1; i >= 0; i-- {
		if boxes[i].Contains(px, py) {
			return i
		}
	}
	return -1
}

// handleContentPts lists the 8 resize handles' content-space anchors in Handle order.
func (b FlatBox) handleContentPts() [8][2]float64 {
	return [8][2]float64{
		{0, 0}, {b.W / 2, 0}, {b.W, 0}, {b.W, b.H / 2},
		{b.W, b.H}, {b.W / 2, b.H}, {0, b.H}, {0, b.H / 2},
	}
}

// HandleAt returns the handle within tol (doc px) of the point; the rotate
// handle floats rotOff doc px above the top-edge midpoint. Handles win over
// body containment - call before HitTest.
func HandleAt(b FlatBox, px, py, tol, rotOff float64) Handle {
	if zigGeo() {
		return zigHandleAt(b, px, py, tol, rotOff)
	}
	return handleAtGo(b, px, py, tol, rotOff)
}

func handleAtGo(b FlatBox, px, py, tol, rotOff float64) Handle {
	best, bestD := HandleNone, tol*tol
	for i, p := range b.handleContentPts() {
		x, y := b.Map(p[0], p[1])
		if d := (px-x)*(px-x) + (py-y)*(py-y); d <= bestD {
			best, bestD = Handle(i+1), d
		}
	}
	// rotate anchor: rotOff above the top edge in CONTENT units of the y-scale
	if sy := math.Abs(b.SY); sy > 1e-9 {
		x, y := b.Map(b.W/2, -rotOff/sy)
		if d := (px-x)*(px-x) + (py-y)*(py-y); d <= bestD {
			best = HandleRotate
		}
	}
	return best
}

// snapAxis snaps the moving edge triple (lo, mid, hi) against candidates;
// returns the correcting delta + the candidate line hit (ok=false: no snap).
func snapAxis(lo, mid, hi float64, cands []float64, thresh float64) (adj, line float64, ok bool) {
	best := thresh
	for _, c := range cands {
		for _, v := range [3]float64{lo, mid, hi} {
			if d := math.Abs(v - c); d < best {
				best, adj, line, ok = d, c-v, c, true
			}
		}
	}
	return adj, line, ok
}

// SnapMove adjusts a proposed move delta so the moving box's edges/center snap
// to the canvas thirds-free lines (edges + center) and to other unlocked boxes'
// edges/centers within thresh. Returns the adjusted delta + active guides.
func SnapMove(boxes []FlatBox, moveIdx int, dx, dy, thresh float64, docW, docH float64) (float64, float64, []Guide) {
	if zigGeo() {
		return zigSnapMove(boxes, moveIdx, dx, dy, thresh, docW, docH)
	}
	return snapMoveGo(boxes, moveIdx, dx, dy, thresh, docW, docH)
}

func snapMoveGo(boxes []FlatBox, moveIdx int, dx, dy, thresh float64, docW, docH float64) (float64, float64, []Guide) {
	if moveIdx < 0 || moveIdx >= len(boxes) {
		return dx, dy, nil
	}
	m := boxes[moveIdx]
	m.X += dx
	m.Y += dy
	minX, minY, maxX, maxY := m.Bounds()
	vc := []float64{0, docW / 2, docW}
	hc := []float64{0, docH / 2, docH}
	for i, b := range boxes {
		if i == moveIdx {
			continue
		}
		bMinX, bMinY, bMaxX, bMaxY := b.Bounds()
		vc = append(vc, bMinX, (bMinX+bMaxX)/2, bMaxX)
		hc = append(hc, bMinY, (bMinY+bMaxY)/2, bMaxY)
	}
	var guides []Guide
	if adj, line, ok := snapAxis(minX, (minX+maxX)/2, maxX, vc, thresh); ok {
		dx += adj
		guides = append(guides, Guide{Vert: true, Pos: line})
	}
	if adj, line, ok := snapAxis(minY, (minY+maxY)/2, maxY, hc, thresh); ok {
		dy += adj
		guides = append(guides, Guide{Vert: false, Pos: line})
	}
	return dx, dy, guides
}

// minSizePx is the smallest box dimension a handle drag can reach.
const minSizePx = 8

// ResizeBox computes the new W/H + X/Y for dragging handle hd to the doc point
// (px,py), keeping the opposite edge/corner anchored in doc space. uniform
// locks the aspect ratio (corner handles). Scale/rotation are unchanged.
func ResizeBox(b FlatBox, hd Handle, px, py float64, uniform bool) (nw, nh, nx, ny float64) {
	if zigGeo() {
		return zigResizeBox(b, hd, px, py, uniform)
	}
	return resizeBoxGo(b, hd, px, py, uniform)
}

func resizeBoxGo(b FlatBox, hd Handle, px, py float64, uniform bool) (nw, nh, nx, ny float64) {
	cx, cy, ok := b.InvMap(px, py)
	if !ok {
		return b.W, b.H, b.X, b.Y
	}
	nw, nh = b.W, b.H
	var q [2]float64 // anchor in orig content space
	switch hd {
	case HandleNW:
		nw, nh, q = b.W-cx, b.H-cy, [2]float64{b.W, b.H}
	case HandleN:
		nh, q = b.H-cy, [2]float64{b.W / 2, b.H}
	case HandleNE:
		nw, nh, q = cx, b.H-cy, [2]float64{0, b.H}
	case HandleE:
		nw, q = cx, [2]float64{0, b.H / 2}
	case HandleSE:
		nw, nh, q = cx, cy, [2]float64{0, 0}
	case HandleS:
		nh, q = cy, [2]float64{b.W / 2, 0}
	case HandleSW:
		nw, nh, q = b.W-cx, cy, [2]float64{b.W, 0}
	case HandleW:
		nw, q = b.W-cx, [2]float64{b.W, b.H / 2}
	default:
		return b.W, b.H, b.X, b.Y
	}
	if uniform && b.W > 0 && b.H > 0 {
		kw, kh := nw/b.W, nh/b.H
		k := kw
		if math.Abs(kh-1) > math.Abs(kw-1) {
			k = kh
		}
		if k < minSizePx/math.Max(b.W, b.H) {
			k = minSizePx / math.Max(b.W, b.H)
		}
		nw, nh = b.W*k, b.H*k
	}
	nw, nh = math.Max(nw, minSizePx), math.Max(nh, minSizePx)

	// re-anchor: the anchor's doc position must survive the size change
	ax, ay := b.Map(q[0], q[1])
	qx, qy := q[0], q[1] // anchor in NEW content space (same relative corner/edge)
	switch hd {
	case HandleNW:
		qx, qy = nw, nh
	case HandleN:
		qx, qy = nw/2, nh
	case HandleNE:
		qx, qy = 0, nh
	case HandleE:
		qx, qy = 0, nh/2
	case HandleSE:
		qx, qy = 0, 0
	case HandleS:
		qx, qy = nw/2, 0
	case HandleSW:
		qx, qy = nw, 0
	case HandleW:
		qx, qy = nw, nh/2
	}
	ma, mb, mc, md := b.mat()
	dxq, dyq := qx-nw/2, qy-nh/2
	nx = ax - (ma*dxq + mb*dyq) - nw/2
	ny = ay - (mc*dxq + md*dyq) - nh/2
	return nw, nh, nx, ny
}

// AngleAt returns the doc-space angle (deg) from the box center to the point.
func AngleAt(b FlatBox, px, py float64) float64 {
	if zigGeo() {
		return zigAngleAt(b, px, py)
	}
	return angleAtGo(b, px, py)
}

func angleAtGo(b FlatBox, px, py float64) float64 {
	return math.Atan2(py-(b.Y+b.H/2), px-(b.X+b.W/2)) * 180 / math.Pi
}

// RotateFrom returns the rotation for a rotate-drag: orig rotation plus the
// angular delta since the drag started; snap rounds to 15° steps.
func RotateFrom(origRot, downAngle, nowAngle float64, snap bool) float64 {
	if zigGeo() {
		return zigRotateFrom(origRot, downAngle, nowAngle, snap)
	}
	return rotateFromGo(origRot, downAngle, nowAngle, snap)
}

func rotateFromGo(origRot, downAngle, nowAngle float64, snap bool) float64 {
	r := origRot + nowAngle - downAngle
	if snap {
		r = math.Round(r/15) * 15
	}
	// normalize to (-180,180] for sane inspector numbers
	for r > 180 {
		r -= 360
	}
	for r <= -180 {
		r += 360
	}
	return r
}
