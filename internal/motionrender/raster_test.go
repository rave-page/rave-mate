package motionrender

// Tests for the textured/shaded triangle fill: checker sampling orientation +
// perspective-correct attribute interpolation.

import (
	"image"
	"image/color"
	"testing"
)

// checkerTex builds a 2×2 NRGBA: (0,0)=R (1,0)=G (0,1)=B (1,1)=W (image y=0 = top).
func checkerTex() *image.NRGBA {
	tex := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	tex.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	tex.SetNRGBA(1, 0, color.NRGBA{G: 255, A: 255})
	tex.SetNRGBA(0, 1, color.NRGBA{B: 255, A: 255})
	tex.SetNRGBA(1, 1, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	return tex
}

// dominant asserts channel ch of c is >200 and the other two color channels <60.
func dominant(t *testing.T, c color.NRGBA, ch byte, at string) {
	t.Helper()
	chans := map[byte]uint8{'r': c.R, 'g': c.G, 'b': c.B}
	if chans[ch] < 200 {
		t.Errorf("%s: channel %c = %d, want >200 (%v)", at, ch, chans[ch], c)
	}
	for k, v := range chans {
		if k != ch && v > 60 {
			t.Errorf("%s: channel %c = %d, want <60 (%v)", at, k, v, c)
		}
	}
}

// TestFillTriShadedCheckerQuad: a screen-aligned quad at constant depth samples the 2×2
// checker into the expected quadrants (v=1 = texture top, FBX convention).
func TestFillTriShadedCheckerQuad(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	db := newDepthBuffer(64, 64)
	tex := checkerTex()
	light := [3]float32{0, 0, 1}
	n := [3]float32{0, 0, 1} // n·light = 1 → lambert f = 1
	a := sVert{X: 0, Y: 0, Z: 2, U: 0, V: 1, N: n}
	b := sVert{X: 63, Y: 0, Z: 2, U: 1, V: 1, N: n}
	c := sVert{X: 63, Y: 63, Z: 2, U: 1, V: 0, N: n}
	d := sVert{X: 0, Y: 63, Z: 2, U: 0, V: 0, N: n}
	fillTriShaded(img, db, a, b, c, tex, color.NRGBA{}, light)
	fillTriShaded(img, db, a, c, d, tex, color.NRGBA{}, light)
	dominant(t, img.NRGBAAt(16, 16), 'r', "top-left")
	dominant(t, img.NRGBAAt(48, 16), 'g', "top-right")
	dominant(t, img.NRGBAAt(16, 48), 'b', "bottom-left")
	if got := img.NRGBAAt(48, 48); got.R < 200 || got.G < 200 || got.B < 200 {
		t.Errorf("bottom-right = %v, want white", got)
	}
}

// TestFillTriShadedPerspectiveCorrect: with Z varying 1 → 3 across the triangle, the
// screen midpoint's u must be pulled toward the NEAR vertex (u≈0.25, red half of the
// texture), not the affine u=0.5 (texel boundary → purple blend).
func TestFillTriShadedPerspectiveCorrect(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	db := newDepthBuffer(64, 64)
	tex := image.NewNRGBA(image.Rect(0, 0, 2, 1)) // left red, right blue
	tex.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	tex.SetNRGBA(1, 0, color.NRGBA{B: 255, A: 255})
	light := [3]float32{0, 0, 1}
	n := [3]float32{0, 0, 1}
	a := sVert{X: 0, Y: 0, Z: 1, U: 0, V: 0.5, N: n}
	b := sVert{X: 60, Y: 0, Z: 3, U: 1, V: 0.5, N: n}
	c := sVert{X: 0, Y: 60, Z: 1, U: 0, V: 0.5, N: n}
	fillTriShaded(img, db, a, b, c, tex, color.NRGBA{}, light)
	// (30,2) ≈ midpoint of edge a-b: perspective-correct u = (0.5·1/3)/(0.5·1 + 0.5·1/3) = 0.25
	dominant(t, img.NRGBAAt(30, 2), 'r', "perspective midpoint")
}

// TestFillTriShadedDepthTest: a farther shaded triangle must not overwrite a nearer one.
func TestFillTriShadedDepthTest(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	db := newDepthBuffer(8, 8)
	light := [3]float32{0, 0, 1}
	n := [3]float32{0, 0, 1}
	near := color.NRGBA{R: 255, A: 255}
	far := color.NRGBA{G: 255, A: 255}
	tri := func(z float32, col color.NRGBA) {
		fillTriShaded(img, db,
			sVert{X: 0, Y: 0, Z: z, N: n}, sVert{X: 7, Y: 0, Z: z, N: n}, sVert{X: 0, Y: 7, Z: z, N: n},
			nil, col, light)
	}
	tri(1, near)
	tri(5, far)
	if got := img.NRGBAAt(1, 1); got != near {
		t.Errorf("pixel = %v, want near color %v", got, near)
	}
}

// TestFillTriShadedLambert: base color is scaled by ambient+diffuse from the per-pixel normal.
func TestFillTriShadedLambert(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	db := newDepthBuffer(8, 8)
	light := [3]float32{0, 0, 1}
	n := [3]float32{1, 0, 0} // ⊥ light → f = ambient = 0.35
	fillTriShaded(img, db,
		sVert{X: 0, Y: 0, Z: 1, N: n}, sVert{X: 7, Y: 0, Z: 1, N: n}, sVert{X: 0, Y: 7, Z: 1, N: n},
		nil, color.NRGBA{R: 200, A: 255}, light)
	got := img.NRGBAAt(1, 1)
	if got.R < 68 || got.R > 72 { // 200 × 0.35 = 70
		t.Errorf("ambient-lit R = %d, want ≈70", got.R)
	}
}
