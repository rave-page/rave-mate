package ui

import (
	"image"
	"image/color"
	"testing"
)

func TestFillTriangleCoversAndZTests(t *testing.T) {
	const w, h = 8, 8
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	db := newDepthBuffer(w, h)
	red := color.NRGBA{R: 255, A: 255}
	// A triangle covering the lower-left half of the 8×8 grid, depth 5.
	fillTriangle(img, db, projVert{0, 0, 5}, projVert{7, 0, 5}, projVert{0, 7, 5}, red)

	// A point well inside the triangle is filled.
	if got := img.NRGBAAt(1, 1); got != red {
		t.Errorf("inside pixel = %v, want red", got)
	}
	// A point outside (upper-right) is untouched.
	if got := img.NRGBAAt(7, 7); got != (color.NRGBA{}) {
		t.Errorf("outside pixel = %v, want zero", got)
	}

	// A nearer green triangle (depth 1) overwrites the same region.
	green := color.NRGBA{G: 255, A: 255}
	fillTriangle(img, db, projVert{0, 0, 1}, projVert{7, 0, 1}, projVert{0, 7, 1}, green)
	if got := img.NRGBAAt(1, 1); got != green {
		t.Errorf("nearer pixel = %v, want green", got)
	}

	// A farther blue triangle (depth 9) is z-rejected (green stays).
	blue := color.NRGBA{B: 255, A: 255}
	fillTriangle(img, db, projVert{0, 0, 9}, projVert{7, 0, 9}, projVert{0, 7, 9}, blue)
	if got := img.NRGBAAt(1, 1); got != green {
		t.Errorf("z-rejected pixel = %v, want green (unchanged)", got)
	}
}

func TestFillTriangleWindingAgnostic(t *testing.T) {
	const w, h = 8, 8
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	db := newDepthBuffer(w, h)
	red := color.NRGBA{R: 255, A: 255}
	// Reverse winding (CW) of the previous triangle still fills.
	fillTriangle(img, db, projVert{0, 0, 5}, projVert{0, 7, 5}, projVert{7, 0, 5}, red)
	if got := img.NRGBAAt(1, 1); got != red {
		t.Errorf("CW inside pixel = %v, want red", got)
	}
}

func TestFillTriangleDegenerateNoop(t *testing.T) {
	const w, h = 4, 4
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	db := newDepthBuffer(w, h)
	// Collinear points → zero area → nothing drawn, no panic.
	fillTriangle(img, db, projVert{0, 0, 1}, projVert{1, 1, 1}, projVert{2, 2, 1}, color.NRGBA{R: 255, A: 255})
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if img.NRGBAAt(x, y) != (color.NRGBA{}) {
				t.Fatalf("degenerate triangle drew at %d,%d", x, y)
			}
		}
	}
}

func TestFaceNormalUnit(t *testing.T) {
	n := faceNormal([3]float32{0, 0, 0}, [3]float32{1, 0, 0}, [3]float32{0, 1, 0})
	if n != ([3]float32{0, 0, 1}) {
		t.Errorf("normal = %v, want +Z", n)
	}
}
