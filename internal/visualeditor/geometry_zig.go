package visualeditor

// Zig edgeo kernel dispatch (tag zigdsp): the exported geometry entry points
// route here when the lib is linked; the *Go bodies in geometry.go stay as
// fallback + golden reference. Parity gate: geometry_zig_parity_test.go.

import "rave.page/mate/internal/zignative"

// zigGeo reports the edgeo kernel is linked + ABI-compatible.
func zigGeo() bool { return zignative.Available() }

// flat7 is the kernel's box form: {x,y,w,h,sx,sy,rot} (Locked stays Go-side).
func (b FlatBox) flat7() [7]float64 {
	return [7]float64{b.X, b.Y, b.W, b.H, b.SX, b.SY, b.Rot}
}

func flat7s(boxes []FlatBox) []float64 {
	out := make([]float64, 0, len(boxes)*7)
	for _, b := range boxes {
		out = append(out, b.X, b.Y, b.W, b.H, b.SX, b.SY, b.Rot)
	}
	return out
}

func zigHitTest(boxes []FlatBox, px, py float64) int {
	if len(boxes) == 0 {
		return -1
	}
	return zignative.EdHitTest(flat7s(boxes), px, py)
}

func zigHandleAt(b FlatBox, px, py, tol, rotOff float64) Handle {
	f := b.flat7()
	return Handle(zignative.EdHandleAt(f[:], px, py, tol, rotOff))
}

func zigSnapMove(boxes []FlatBox, moveIdx int, dx, dy, thresh, docW, docH float64) (float64, float64, []Guide) {
	if moveIdx < 0 || moveIdx >= len(boxes) {
		return dx, dy, nil
	}
	ndx, ndy, g, n := zignative.EdSnapMove(flat7s(boxes), moveIdx, dx, dy, thresh, docW, docH)
	var guides []Guide
	for i := 0; i < n; i++ {
		guides = append(guides, Guide{Vert: g[i*2] != 0, Pos: g[i*2+1]})
	}
	return ndx, ndy, guides
}

func zigResizeBox(b FlatBox, hd Handle, px, py float64, uniform bool) (nw, nh, nx, ny float64) {
	f := b.flat7()
	return zignative.EdResizeBox(f[:], int(hd), px, py, uniform)
}

func zigAngleAt(b FlatBox, px, py float64) float64 {
	f := b.flat7()
	return zignative.EdAngleAt(f[:], px, py)
}

func zigRotateFrom(origRot, downAngle, nowAngle float64, snap bool) float64 {
	return zignative.EdRotateFrom(origRot, downAngle, nowAngle, snap)
}
