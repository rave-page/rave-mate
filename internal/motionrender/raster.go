package motionrender

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

// ── textured / smooth-shaded fill ────────────────────────────────────────────

// sVert is a projected vertex with shading attributes: screen px, view depth (Z > 0, nearer =
// smaller), texcoords (FBX convention, v=0 bottom) and a world-space normal.
type sVert struct {
	X, Y int
	Z    float32
	U, V float32
	N    [3]float32
}

// fillTriShaded rasterizes a depth-tested triangle with PERSPECTIVE-CORRECT attribute
// interpolation: per-vertex attributes are premultiplied by 1/Z, interpolated linearly in screen
// space, then divided by the interpolated 1/Z. Texels (bilinear; nil tex → base) are modulated by
// a two-sided lambert term from the interpolated normal (ambient 0.35, matching shadeFlat).
func fillTriShaded(img *image.NRGBA, db *depthBuffer, a, b, c sVert, tex *image.NRGBA, base color.NRGBA, light [3]float32) {
	area := edge(a.X, a.Y, b.X, b.Y, c.X, c.Y)
	if area == 0 || a.Z <= 0 || b.Z <= 0 || c.Z <= 0 {
		return
	}
	minX := max(min(a.X, min(b.X, c.X)), 0)
	minY := max(min(a.Y, min(b.Y, c.Y)), 0)
	maxX := min(max(a.X, max(b.X, c.X)), db.w-1)
	maxY := min(max(a.Y, max(b.Y, c.Y)), db.h-1)
	if minX > maxX || minY > maxY {
		return
	}
	invArea := 1.0 / float32(area)
	wA, wB, wC := 1/a.Z, 1/b.Z, 1/c.Z
	uA, vA := a.U*wA, a.V*wA
	uB, vB := b.U*wB, b.V*wB
	uC, vC := c.U*wC, c.V*wC
	nA := [3]float32{a.N[0] * wA, a.N[1] * wA, a.N[2] * wA}
	nB := [3]float32{b.N[0] * wB, b.N[1] * wB, b.N[2] * wB}
	nC := [3]float32{c.N[0] * wC, c.N[1] * wC, c.N[2] * wC}
	const ambient = 0.35
	baseR, baseG, baseB := float32(base.R), float32(base.G), float32(base.B)
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
			b0, b1, b2 := float32(w0)*invArea, float32(w1)*invArea, float32(w2)*invArea
			z := b0*a.Z + b1*b.Z + b2*c.Z
			idx := y*db.w + x
			if z >= db.z[idx] {
				continue
			}
			iw := b0*wA + b1*wB + b2*wC
			if iw <= 0 {
				continue
			}
			// Alpha cutout BEFORE the depth write: VRChat-style avatars stack alpha-masked
			// clothing/accessory shells over the body - the masked-out texels carry garbage
			// color and must neither draw nor occlude (threshold 0.5, standard cutout).
			r, g, bl := baseR, baseG, baseB
			if tex != nil {
				u := (b0*uA + b1*uB + b2*uC) / iw
				v := (b0*vA + b1*vB + b2*vC) / iw
				var al float32
				r, g, bl, al = texSample(tex, u, v)
				if al < 128 {
					continue
				}
			}
			db.z[idx] = z
			// lambert from the interpolated normal (direction only - skip the /iw)
			nx := b0*nA[0] + b1*nB[0] + b2*nC[0]
			ny := b0*nA[1] + b1*nB[1] + b2*nC[1]
			nz := b0*nA[2] + b1*nB[2] + b2*nC[2]
			f := float32(1)
			if nl := float32(math.Sqrt(float64(nx*nx + ny*ny + nz*nz))); nl > 0 {
				d := (nx*light[0] + ny*light[1] + nz*light[2]) / nl
				if d < 0 {
					d = -d
				}
				f = ambient + (1-ambient)*d
				if f > 1 {
					f = 1
				}
			}
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(r * f), G: uint8(g * f), B: uint8(bl * f), A: 255})
		}
	}
}

// texSample bilinearly samples tex at (u,v): repeat wrap, FBX v-flip (v=0 = bottom row).
// Returns RGBA - the alpha drives the raster's cutout test.
func texSample(tex *image.NRGBA, u, v float32) (r, g, b, a float32) {
	w, h := tex.Rect.Dx(), tex.Rect.Dy()
	u -= floor32(u)
	v -= floor32(v)
	fx := u*float32(w) - 0.5
	fy := (1-v)*float32(h) - 0.5
	x0f, y0f := floor32(fx), floor32(fy)
	tx, ty := fx-x0f, fy-y0f
	x0, y0 := int(x0f), int(y0f)
	fetch := func(x, y int) (float32, float32, float32, float32) {
		x, y = clampi(x, 0, w-1), clampi(y, 0, h-1)
		p := tex.Pix[y*tex.Stride+x*4:]
		return float32(p[0]), float32(p[1]), float32(p[2]), float32(p[3])
	}
	r00, g00, b00, a00 := fetch(x0, y0)
	r10, g10, b10, a10 := fetch(x0+1, y0)
	r01, g01, b01, a01 := fetch(x0, y0+1)
	r11, g11, b11, a11 := fetch(x0+1, y0+1)
	rt := r00 + (r10-r00)*tx
	gt := g00 + (g10-g00)*tx
	bt := b00 + (b10-b00)*tx
	at := a00 + (a10-a00)*tx
	rb := r01 + (r11-r01)*tx
	gb := g01 + (g11-g01)*tx
	bb := b01 + (b11-b01)*tx
	ab := a01 + (a11-a01)*tx
	return rt + (rb-rt)*ty, gt + (gb-gt)*ty, bt + (bb-bt)*ty, at + (ab-at)*ty
}

func floor32(v float32) float32 { return float32(math.Floor(float64(v))) }
