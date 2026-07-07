package motionrender

import (
	"image/color"
	"testing"

	"rave.page/mate/internal/vrmotion"
)

// TestEquirectRemapCoversAllPixels: the face→equirect remap is total - every output
// pixel maps to exactly one face (no unmapped/black seams) and all 6 faces are sampled.
func TestEquirectRemapCoversAllPixels(t *testing.T) {
	r := NewEquirectRenderer(256, 128)
	cols := [6]color.NRGBA{
		{R: 10, A: 255}, {R: 20, A: 255}, {R: 30, A: 255},
		{R: 40, A: 255}, {R: 50, A: 255}, {R: 60, A: 255},
	}
	for i := range r.faces {
		fillImg(r.faces[i], cols[i])
	}
	r.composite()
	seen := map[uint8]int{}
	for y := range r.h {
		for x := range r.w {
			c := r.out.NRGBAAt(x, y)
			ok := false
			for _, fc := range cols {
				if c == fc {
					ok = true
					break
				}
			}
			if !ok {
				t.Fatalf("unmapped pixel (%d,%d) = %v", x, y, c)
			}
			seen[c.R]++
		}
	}
	if len(seen) != 6 {
		t.Fatalf("expected all 6 faces sampled, got %d: %v", len(seen), seen)
	}
}

// TestEquirectTriangleLandsOnExpectedSide: a single triangle 1m along +X of the eye
// must land near lon=+90° (x≈0.75W) at the horizon (y≈0.5H).
func TestEquirectTriangleLandsOnExpectedSide(t *testing.T) {
	r := NewEquirectRenderer(256, 128)
	red := color.NRGBA{R: 255, A: 255}
	// normals = light dir → lambert f=1 → exact base color out
	tri := eqTri{v: [3][3]float32{{1, -0.3, -0.3}, {1, 0.3, -0.3}, {1, 0, 0.3}}, base: red,
		n: [3][3]float32{modelLight, modelLight, modelLight}}
	for fi := range r.faces {
		fillImg(r.faces[fi], colBG)
		r.depth.reset()
		cam := lookCam{right: eqFaces[fi].right, up: eqFaces[fi].up, fwd: eqFaces[fi].fwd, s: r.s}
		r.clipBuf = drawTriFace(r.faces[fi], r.depth, cam, tri, r.clipBuf)
	}
	r.composite()
	sx, sy, n := 0, 0, 0
	for y := range r.h {
		for x := range r.w {
			if r.out.NRGBAAt(x, y) == red {
				sx, sy, n = sx+x, sy+y, n+1
			}
		}
	}
	if n == 0 {
		t.Fatal("triangle rendered no pixels")
	}
	cx, cy := float64(sx)/float64(n)/float64(r.w), float64(sy)/float64(n)/float64(r.h)
	if cx < 0.70 || cx > 0.80 {
		t.Fatalf("centroid x = %.3f, want ≈0.75 (+X side)", cx)
	}
	if cy < 0.42 || cy > 0.58 {
		t.Fatalf("centroid y = %.3f, want ≈0.5 (horizon)", cy)
	}
}

// TestEquirectSkeletonHeadSide: full Render path - a head pose 1m at -X of the eye
// paints head-colored pixels near lon=-90° (x≈0.25W).
func TestEquirectSkeletonHeadSide(t *testing.T) {
	f := EqFrame{
		Eye: [3]float32{0, 1.6, 0}, Center: [3]float32{0, 0, 0}, FloorY: 0, GridR: 2,
		Sample: map[int]vrmotion.Pose{0: {Pos: [3]float32{-1, 1.6, 0}}},
	}
	img := NewEquirectRenderer(256, 128).Render(f)
	sx, n := 0, 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if img.NRGBAAt(x, y) == colHead {
				sx, n = sx+x, n+1
			}
		}
	}
	if n == 0 {
		t.Fatal("head disc rendered no pixels")
	}
	cx := float64(sx) / float64(n) / 256
	if cx < 0.20 || cx > 0.30 {
		t.Fatalf("head centroid x = %.3f, want ≈0.25 (-X side)", cx)
	}
}

// TestOrbitRenderSkeleton: the perspective Render path draws the head disc within bounds.
func TestOrbitRenderSkeleton(t *testing.T) {
	cam := Camera{Yaw: 0.6, Pitch: 0.35, Dist: 3, Center: [3]float32{0, 1, 0}, FloorY: 0, GridR: 2}
	img := Render(Frame{
		W: 160, H: 120, Cam: cam,
		Sample: map[int]vrmotion.Pose{0: {Pos: [3]float32{0, 1.6, 0}}, 1: {Pos: [3]float32{0.3, 1.0, 0.1}}},
	})
	if img.Bounds().Dx() != 160 || img.Bounds().Dy() != 120 {
		t.Fatalf("bounds = %v", img.Bounds())
	}
	n := 0
	for y := range 120 {
		for x := range 160 {
			if img.NRGBAAt(x, y) == colHead {
				n++
			}
		}
	}
	if n == 0 {
		t.Fatal("head disc not rendered")
	}
}
