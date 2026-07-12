// Package pointcloud samples a posed avatar mesh into an animated point cloud and
// writes it as the compact, web-consumable RMPC stream (see format.go +
// .devnotes/POINTCLOUD_FORMAT.md). It is the anti-extraction-friendly public artifact:
// per-frame surface points (quantized positions + frame-invariant colour) instead of the
// raw FBX/VRM, which never leaves. Pure Go + stdlib; depends only on the vrm data package.
//
// Sampling is area-weighted over the mesh SURFACE (triangles), so the point count is a free
// parameter independent of the model's vertex count - at high density the cloud reads as a
// solid surface, not a scatter (you can't tell it was a point cloud). Each sample is a
// triangle + barycentric weights; per frame its position is the barycentric blend of the
// triangle's 3 CPU-skinned world verts (exactly the GPU's face interpolation), so point
// count AND colour stay frame-invariant (skinning moves positions, not albedo) - frames stay
// fixed-size (O(1) seek) and colour rides once in the header. A triangle-less model (raw
// point mesh) falls back to a density-strided vertex subset.
package pointcloud

import (
	"math"

	"rave.page/mate/internal/vrm"
)

// Bounds is a world-space AABB accumulated across every frame (quantization range).
type Bounds struct {
	Min [3]float32 `json:"min"`
	Max [3]float32 `json:"max"`
}

// NewBounds returns an inverted (empty) AABB ready for Expand.
func NewBounds() Bounds {
	return Bounds{Min: [3]float32{inf, inf, inf}, Max: [3]float32{-inf, -inf, -inf}}
}

const inf = float32(math.MaxFloat32)

// Expand grows b to include p.
func (b *Bounds) Expand(p [3]float32) {
	for i := 0; i < 3; i++ {
		if p[i] < b.Min[i] {
			b.Min[i] = p[i]
		}
		if p[i] > b.Max[i] {
			b.Max[i] = p[i]
		}
	}
}

// Valid reports whether at least one point was seen (Min<=Max on every axis).
func (b Bounds) Valid() bool {
	return b.Min[0] <= b.Max[0] && b.Min[1] <= b.Max[1] && b.Min[2] <= b.Max[2]
}

// Pad grows the AABB by m meters on every side (avoids clamping the extreme points to the
// quant edge; also gives a non-degenerate range on a flat axis).
func (b *Bounds) Pad(m float32) {
	for i := 0; i < 3; i++ {
		b.Min[i] -= m
		b.Max[i] += m
	}
}

// surfSample is one surface point: a triangle (3 vertex indices in mesh `mesh`) + barycentric
// weights. Vertex-fallback samples set i0=i1=i2 and a=1 (b=c=0) → the vertex position.
type surfSample struct {
	mesh       int
	i0, i1, i2 int
	a, b, c    float32
}

// Selection is a fixed set of surface samples, grouped by mesh so Positions poses one mesh at
// a time. Reused for every frame of a take.
type Selection struct {
	samples []surfSample
	Colors  []byte // 3*len(samples) RGB, frame-invariant; nil when colour not requested
}

// Count returns the number of points per frame.
func (s *Selection) Count() int { return len(s.samples) }

// defaultRGB is the fallback point colour (brand-violet, matches the mesh renderer default).
var defaultRGB = [3]byte{0x9A, 0x7A, 0xE0}

// Select picks ~target surface samples across all meshes, area-weighted so coverage is even
// and independent of vertex count. When withColor is set it samples each point's albedo once
// (texture at its interpolated UV, else the mesh diffuse, else the default) into Colors.
// Triangle-less meshes fall back to a deterministic per-mesh vertex stride. target<1 → 1.
func Select(m *vrm.Model, target int, withColor bool) *Selection {
	if target < 1 {
		target = 1
	}
	tris := 0
	for mi := range m.Meshes {
		tris += len(m.Meshes[mi].Indices) / 3
	}
	if tris == 0 {
		return selectVertices(m, target, withColor)
	}
	return selectSurface(m, target, withColor)
}

// selectSurface area-weight samples triangles. Per-mesh allocation (∝ mesh surface area)
// keeps samples mesh-grouped so Positions poses each mesh once per frame.
func selectSurface(m *vrm.Model, target int, withColor bool) *Selection {
	// Per-mesh triangle table + cumulative area.
	type meshTris struct {
		i0, i1, i2 []int
		cum        []float32
		area       float32
	}
	mts := make([]meshTris, len(m.Meshes))
	var totalArea float32
	for mi := range m.Meshes {
		mesh := &m.Meshes[mi]
		idx := mesh.Indices
		mt := &mts[mi]
		for t := 0; t+2 < len(idx); t += 3 {
			a, b, c := int(idx[t]), int(idx[t+1]), int(idx[t+2])
			ar := triArea(mesh.Verts[a].Pos, mesh.Verts[b].Pos, mesh.Verts[c].Pos)
			if ar <= 0 {
				continue
			}
			mt.area += ar
			totalArea += ar
			mt.i0 = append(mt.i0, a)
			mt.i1 = append(mt.i1, b)
			mt.i2 = append(mt.i2, c)
			mt.cum = append(mt.cum, mt.area)
		}
	}
	if totalArea <= 0 {
		return selectVertices(m, target, withColor)
	}

	sel := &Selection{samples: make([]surfSample, 0, target)}
	if withColor {
		sel.Colors = make([]byte, 0, 3*target)
	}
	var rng uint64 = 0x9E3779B97F4A7C15 // fixed seed → reproducible exports
	for mi := range mts {
		mt := &mts[mi]
		if len(mt.cum) == 0 {
			continue
		}
		n := int(math.Round(float64(target) * float64(mt.area/totalArea)))
		if n < 1 {
			n = 1
		}
		mesh := &m.Meshes[mi]
		for k := 0; k < n; k++ {
			tr := pickTri(mt.cum, mt.area, &rng)
			i0, i1, i2 := mt.i0[tr], mt.i1[tr], mt.i2[tr]
			wu, wv := nextFloat(&rng), nextFloat(&rng)
			if wu+wv > 1 {
				wu, wv = 1-wu, 1-wv
			}
			ww := 1 - wu - wv
			sel.samples = append(sel.samples, surfSample{mi, i0, i1, i2, ww, wu, wv})
			if withColor {
				c0, c1, c2 := vertColorF(mesh, i0), vertColorF(mesh, i1), vertColorF(mesh, i2)
				sel.Colors = append(sel.Colors,
					clampByte(c0[0]*ww+c1[0]*wu+c2[0]*wv),
					clampByte(c0[1]*ww+c1[1]*wu+c2[1]*wv),
					clampByte(c0[2]*ww+c1[2]*wu+c2[2]*wv))
			}
		}
	}
	return sel
}

// selectVertices: fallback for triangle-less geometry - a deterministic per-mesh vertex
// stride (degenerate samples with a=1 → the vertex position verbatim).
func selectVertices(m *vrm.Model, target int, withColor bool) *Selection {
	total := 0
	for mi := range m.Meshes {
		total += len(m.Meshes[mi].Verts)
	}
	if total == 0 {
		return &Selection{}
	}
	stride := total / target
	if stride < 1 {
		stride = 1
	}
	sel := &Selection{samples: make([]surfSample, 0, total/stride+1)}
	if withColor {
		sel.Colors = make([]byte, 0, 3*(total/stride+1))
	}
	for mi := range m.Meshes {
		mesh := &m.Meshes[mi]
		for vi := 0; vi < len(mesh.Verts); vi += stride {
			sel.samples = append(sel.samples, surfSample{mesh: mi, i0: vi, i1: vi, i2: vi, a: 1})
			if withColor {
				c := vertColor(mesh, vi)
				sel.Colors = append(sel.Colors, c[0], c[1], c[2])
			}
		}
	}
	return sel
}

// Positions extracts the selection's world-space positions for one posed frame. world/skin
// come from the caller's pose chain (vrmik.PoseRT → WorldFrom → SkinMatrices). dst is reused
// when it has room, else a new slice is allocated - valid until the next call that reuses it.
// One mesh is posed at a time (samples are mesh-grouped); each point = barycentric blend of
// its triangle's 3 posed verts.
func (s *Selection) Positions(m *vrm.Model, world, skin []vrm.Mat4, dst [][3]float32) [][3]float32 {
	n := len(s.samples)
	if cap(dst) < n {
		dst = make([][3]float32, n)
	}
	dst = dst[:n]
	curMesh := -1
	var posed [][3]float32
	for i, sp := range s.samples {
		if sp.mesh != curMesh {
			posed = m.PosedPositions(sp.mesh, world, skin)
			curMesh = sp.mesh
		}
		p0, p1, p2 := posed[sp.i0], posed[sp.i1], posed[sp.i2]
		dst[i] = [3]float32{
			p0[0]*sp.a + p1[0]*sp.b + p2[0]*sp.c,
			p0[1]*sp.a + p1[1]*sp.b + p2[1]*sp.c,
			p0[2]*sp.a + p1[2]*sp.b + p2[2]*sp.c,
		}
	}
	return dst
}

// pickTri binary-searches the cumulative-area table for a uniform [0,area) draw.
func pickTri(cum []float32, area float32, rng *uint64) int {
	r := nextFloat(rng) * area
	lo, hi := 0, len(cum)-1
	for lo < hi {
		mid := (lo + hi) / 2
		if cum[mid] < r {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// triArea is half the cross-product magnitude of the triangle edges.
func triArea(a, b, c [3]float32) float32 {
	e1 := [3]float32{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
	e2 := [3]float32{c[0] - a[0], c[1] - a[1], c[2] - a[2]}
	cx := e1[1]*e2[2] - e1[2]*e2[1]
	cy := e1[2]*e2[0] - e1[0]*e2[2]
	cz := e1[0]*e2[1] - e1[1]*e2[0]
	return 0.5 * float32(math.Sqrt(float64(cx*cx+cy*cy+cz*cz)))
}

// nextFloat is a deterministic splitmix64 draw in [0,1) (24-bit mantissa). Fixed-seeded per
// export → reproducible clouds without depending on math/rand global state.
func nextFloat(s *uint64) float32 {
	*s += 0x9E3779B97F4A7C15
	z := *s
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	z ^= z >> 31
	return float32(z>>40) / float32(1<<24)
}

// vertColor samples a vertex's albedo: texture at its UV (nearest), else mesh diffuse, else
// the default. UV v is bottom-origin (FBX), so the texture row is flipped.
func vertColor(mesh *vrm.Mesh, vi int) [3]byte {
	if mesh.Tex != nil && vi < len(mesh.UV) {
		uv := mesh.UV[vi]
		tb := mesh.Tex.Bounds()
		px := tb.Min.X + int(fracf(uv[0])*float32(tb.Dx()))
		py := tb.Min.Y + int((1-fracf(uv[1]))*float32(tb.Dy()))
		px = clampi(px, tb.Min.X, tb.Max.X-1)
		py = clampi(py, tb.Min.Y, tb.Max.Y-1)
		c := mesh.Tex.NRGBAAt(px, py)
		return [3]byte{c.R, c.G, c.B}
	}
	if mesh.Diffuse.A != 0 {
		return [3]byte{mesh.Diffuse.R, mesh.Diffuse.G, mesh.Diffuse.B}
	}
	return defaultRGB
}

// vertColorF is vertColor as float32 RGB (0-255) for barycentric colour blending.
func vertColorF(mesh *vrm.Mesh, vi int) [3]float32 {
	c := vertColor(mesh, vi)
	return [3]float32{float32(c[0]), float32(c[1]), float32(c[2])}
}

func clampByte(f float32) byte {
	if f <= 0 {
		return 0
	}
	if f >= 255 {
		return 255
	}
	return byte(f + 0.5)
}

// fracf returns the fractional part of x in [0,1) (UV wrap).
func fracf(x float32) float32 {
	x -= float32(math.Floor(float64(x)))
	if x < 0 {
		x += 1
	}
	return x
}

func clampi(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
