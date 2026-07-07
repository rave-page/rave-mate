package vroverlay

// geomquad.go - EXACT analytic ray/point ↔ overlay-quad intersection (rewrite core). Every overlay is
// a flat quad whose world pose + size we OWN: center transform (for a hand-attached menu, the current
// device pose × the stored device-relative transform), width in metres, and aspect (uploaded texture
// W:H). So the hit is pure geometry we compute - no ComputeOverlayIntersection, no runtime UV, no
// edge-clamp guard: a miss is simply a point outside the known extents.
//
// Convention (matches the uploaded texture + the runtime's old bottom-origin vUVs, so the (1-v) row
// flip elsewhere is unchanged): local +X = right, +Y = up, +Z = quad normal (facing side). u = 0 at
// the left edge → 1 at the right; v = 0 at the BOTTOM edge → 1 at the top.

// quadHit is one analytic intersection result.
type quadHit struct {
	u, v   float32    // 0..1 within the quad (v bottom-origin); meaningful only when inside
	pt     [3]float32 // world point ON the quad surface (ray hit, or perpendicular foot for a projection)
	dist   float32    // ray param (metres) to the plane, or |perpendicular distance| for a point projection
	inside bool       // hit lies within the quad's real edges (|localX| ≤ halfW AND |localY| ≤ halfH)
	front  bool       // ray/point approached the quad's +Z (facing) side
}

// quadAxes returns a quad center transform's right(+X), up(+Y), normal(+Z) axes and origin (metres).
func quadAxes(m Mat34) (x, y, z, o [3]float32) {
	x = [3]float32{m[0], m[4], m[8]}
	y = [3]float32{m[1], m[5], m[9]}
	z = [3]float32{m[2], m[6], m[10]}
	o = [3]float32{m[3], m[7], m[11]}
	return
}

func dot3(a, b [3]float32) float32 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }

func sub3(a, b [3]float32) [3]float32 { return [3]float32{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }

// hitQuad intersects a ray (origin, unit dir) with the quad (center pose, widthM × heightM). Returns
// the exact UV + whether it lands within the real edges. No dependency on the VR runtime.
func hitQuad(origin, dir [3]float32, center Mat34, widthM, heightM float32) quadHit {
	xAx, yAx, zAx, o := quadAxes(center)
	denom := dot3(dir, zAx)
	if denom > -1e-6 && denom < 1e-6 {
		return quadHit{} // ray parallel to the plane → no hit
	}
	t := dot3(sub3(o, origin), zAx) / denom
	if t < 0 {
		return quadHit{} // plane is behind the ray origin
	}
	p := [3]float32{origin[0] + dir[0]*t, origin[1] + dir[1]*t, origin[2] + dir[2]*t}
	rel := sub3(p, o)
	return quadUV(o, rel, xAx, yAx, widthM, heightM, t, denom < 0)
}

// projectPoint perpendicularly projects a point (a controller tip) onto the quad's plane and returns
// the exact UV + inside test - the near-field "touch" map, stable under wrist-angle tremor because it
// uses tip POSITION, not aim direction. dist is the |perpendicular distance| to the plane.
func projectPoint(tip [3]float32, center Mat34, widthM, heightM float32) quadHit {
	xAx, yAx, zAx, o := quadAxes(center)
	rel := sub3(tip, o)
	d := dot3(rel, zAx) // signed distance along the normal
	h := quadUV(o, rel, xAx, yAx, widthM, heightM, d, d >= 0)
	if h.dist < 0 {
		h.dist = -h.dist
	}
	return h
}

// quadUV maps a center-relative vector onto the quad's local axes → UV, the in-plane world point, and
// the inside/edge test. dist/front are carried from the caller (ray param or signed normal distance).
func quadUV(o, rel, xAx, yAx [3]float32, widthM, heightM, dist float32, front bool) quadHit {
	hw, hh := widthM/2, heightM/2
	if hw <= 0 || hh <= 0 {
		return quadHit{}
	}
	lx := dot3(rel, xAx)
	ly := dot3(rel, yAx)
	return quadHit{
		u: lx/widthM + 0.5,  // -hw→0, +hw→1
		v: ly/heightM + 0.5, // bottom-origin: -hh→0, +hh→1
		pt: [3]float32{ // point projected onto the quad plane (drops any normal component)
			o[0] + xAx[0]*lx + yAx[0]*ly,
			o[1] + xAx[1]*lx + yAx[1]*ly,
			o[2] + xAx[2]*lx + yAx[2]*ly},
		dist:   dist,
		inside: lx >= -hw && lx <= hw && ly >= -hh && ly <= hh,
		front:  front,
	}
}
