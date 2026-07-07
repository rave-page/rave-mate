package ui

// Software triangle rasterizer with a per-pixel z-buffer, for the in-app avatar-mesh preview.
// Reusable + pure (image + math only): the caller projects world triangles to screen (px + depth,
// smaller depth = nearer, matching motionView.project) and flat-shades faces via shadeFlat.

import (
	"image"
	"image/color"
	"math"
)

// projVert is a projected vertex: integer screen px + interpolated depth (nearer = smaller).
type projVert struct {
	X, Y int
	Z    float32
}

// depthBuffer is a nearest-depth buffer sized w×h (row-major). Initialized to +Inf (nothing drawn).
type depthBuffer struct {
	w, h int
	z    []float32
}

func newDepthBuffer(w, h int) *depthBuffer {
	z := make([]float32, w*h)
	for i := range z {
		z[i] = math.MaxFloat32
	}
	return &depthBuffer{w: w, h: h, z: z}
}

// edge is 2× the signed area of triangle (a,b,c) in screen space (>0 = CCW).
func edge(ax, ay, bx, by, cx, cy int) int {
	return (bx-ax)*(cy-ay) - (by-ay)*(cx-ax)
}

// fillTriangle rasterizes a flat-shaded, depth-tested triangle into img. Depth is barycentric-
// interpolated per pixel; a fragment is written only when nearer than the depth buffer. Winding-
// agnostic (front + back faces both fill).
func fillTriangle(img *image.NRGBA, db *depthBuffer, a, b, c projVert, col color.NRGBA) {
	area := edge(a.X, a.Y, b.X, b.Y, c.X, c.Y)
	if area == 0 {
		return // degenerate (zero screen area)
	}
	minX := max(min(a.X, min(b.X, c.X)), 0)
	minY := max(min(a.Y, min(b.Y, c.Y)), 0)
	maxX := min(max(a.X, max(b.X, c.X)), db.w-1)
	maxY := min(max(a.Y, max(b.Y, c.Y)), db.h-1)
	if minX > maxX || minY > maxY {
		return
	}
	invArea := 1.0 / float32(area)
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			w0 := edge(b.X, b.Y, c.X, c.Y, x, y)
			w1 := edge(c.X, c.Y, a.X, a.Y, x, y)
			w2 := edge(a.X, a.Y, b.X, b.Y, x, y)
			if area > 0 {
				if w0 < 0 || w1 < 0 || w2 < 0 {
					continue
				}
			} else if w0 > 0 || w1 > 0 || w2 > 0 {
				continue
			}
			z := (float32(w0)*a.Z + float32(w1)*b.Z + float32(w2)*c.Z) * invArea
			idx := y*db.w + x
			if z < db.z[idx] {
				db.z[idx] = z
				img.SetNRGBA(x, y, col)
			}
		}
	}
}

// faceNormal returns the normalized world-space normal of triangle (p0,p1,p2).
func faceNormal(p0, p1, p2 [3]float32) [3]float32 {
	ux, uy, uz := p1[0]-p0[0], p1[1]-p0[1], p1[2]-p0[2]
	vx, vy, vz := p2[0]-p0[0], p2[1]-p0[1], p2[2]-p0[2]
	n := [3]float32{uy*vz - uz*vy, uz*vx - ux*vz, ux*vy - uy*vx}
	l := float32(math.Sqrt(float64(n[0]*n[0] + n[1]*n[1] + n[2]*n[2])))
	if l == 0 {
		return [3]float32{0, 0, 1}
	}
	return [3]float32{n[0] / l, n[1] / l, n[2] / l}
}

// shadeFlat scales base by an ambient+diffuse lambert term from |normal·light| (two-sided, so
// back-faces aren't black - winding after skinning isn't guaranteed). light must be unit length.
func shadeFlat(base color.NRGBA, normal, light [3]float32) color.NRGBA {
	d := normal[0]*light[0] + normal[1]*light[1] + normal[2]*light[2]
	if d < 0 {
		d = -d
	}
	const ambient = 0.35
	f := ambient + (1-ambient)*d
	if f > 1 {
		f = 1
	}
	return color.NRGBA{R: uint8(float32(base.R) * f), G: uint8(float32(base.G) * f), B: uint8(float32(base.B) * f), A: base.A}
}
