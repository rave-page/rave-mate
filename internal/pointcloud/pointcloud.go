// Package pointcloud samples a posed avatar mesh into an animated point cloud and
// writes it as the compact, web-consumable RMPC stream (see format.go +
// .devnotes/POINTCLOUD_FORMAT.md). It is the anti-extraction-friendly public artifact:
// per-frame surface points (quantized positions + frame-invariant colour) instead of the
// raw FBX/VRM, which never leaves. Pure Go + stdlib; depends only on the vrm data package.
//
// Per-frame positions come from vrm.Model.PosedPositions (the same CPU-skinning primitive
// the C5 video renderer uses). A Selection is a fixed density-strided vertex subset chosen
// once and reused every frame, so point count AND colour stay frame-invariant (skinning
// moves positions, not albedo) - that keeps frames fixed-size (O(1) seek) and lets colour
// ride once in the header.
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

// sampleRef identifies one selected mesh vertex.
type sampleRef struct{ mesh, vert int }

// Selection is a fixed subset of mesh vertices, ordered by mesh so Positions can pose one
// mesh at a time. Reused for every frame of a take.
type Selection struct {
	refs   []sampleRef
	Colors []byte // 3*len(refs) RGB, frame-invariant; nil when colour not requested
}

// Count returns the number of points per frame.
func (s *Selection) Count() int { return len(s.refs) }

// defaultRGB is the fallback point colour (brand-violet, matches the mesh renderer default).
var defaultRGB = [3]byte{0x9A, 0x7A, 0xE0}

// Select picks ~target vertices across all meshes at a deterministic per-mesh stride. When
// withColor is set it samples each point's albedo once (texture at its UV, else the mesh
// diffuse, else the default) into Colors. target<1 clamps to 1.
func Select(m *vrm.Model, target int, withColor bool) *Selection {
	total := 0
	for mi := range m.Meshes {
		total += len(m.Meshes[mi].Verts)
	}
	if total == 0 {
		return &Selection{}
	}
	if target < 1 {
		target = 1
	}
	stride := total / target
	if stride < 1 {
		stride = 1
	}
	sel := &Selection{refs: make([]sampleRef, 0, total/stride+1)}
	if withColor {
		sel.Colors = make([]byte, 0, 3*(total/stride+1))
	}
	for mi := range m.Meshes {
		mesh := &m.Meshes[mi]
		for vi := 0; vi < len(mesh.Verts); vi += stride {
			sel.refs = append(sel.refs, sampleRef{mi, vi})
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
// when it has room, else a new slice is allocated - the returned slice is valid until the
// next call that reuses it. One mesh is posed at a time (refs are mesh-ordered).
func (s *Selection) Positions(m *vrm.Model, world, skin []vrm.Mat4, dst [][3]float32) [][3]float32 {
	n := len(s.refs)
	if cap(dst) < n {
		dst = make([][3]float32, n)
	}
	dst = dst[:n]
	curMesh := -1
	var posed [][3]float32
	for i, r := range s.refs {
		if r.mesh != curMesh {
			posed = m.PosedPositions(r.mesh, world, skin)
			curMesh = r.mesh
		}
		dst[i] = posed[r.vert]
	}
	return dst
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
