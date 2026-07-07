package visualeditor

import (
	"image"
	"image/color"
	"testing"
)

// blend1 blends a single opaque source pixel over an opaque backdrop and returns the result.
func blend1(t *testing.T, backdrop, src color.NRGBA, mode BlendMode, opacity float64) color.NRGBA {
	t.Helper()
	dst := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	dst.SetNRGBA(0, 0, backdrop)
	s := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	s.SetNRGBA(0, 0, src)
	blendRegion(dst, s, 0, 0, opacity, mode)
	return dst.NRGBAAt(0, 0)
}

func eq(t *testing.T, got color.NRGBA, r, g, b, a uint8) {
	t.Helper()
	if got.R != r || got.G != g || got.B != b || got.A != a {
		t.Fatalf("got %v, want {%d %d %d %d}", got, r, g, b, a)
	}
}

func TestBlendModesHandComputed(t *testing.T) {
	cb := color.NRGBA{R: 100, G: 150, B: 200, A: 255}
	// Normal at full opacity → source.
	eq(t, blend1(t, cb, color.NRGBA{50, 100, 150, 255}, BlendNormal, 1), 50, 100, 150, 255)

	// Normal at 0.5 over opaque backdrop → (cb+cs)/2.
	eq(t, blend1(t, color.NRGBA{100, 100, 100, 255}, color.NRGBA{200, 200, 200, 255}, BlendNormal, 0.5),
		150, 150, 150, 255)

	// Multiply by white = source; by black = black.
	eq(t, blend1(t, color.NRGBA{255, 255, 255, 255}, color.NRGBA{128, 64, 32, 255}, BlendMultiply, 1),
		128, 64, 32, 255)
	eq(t, blend1(t, color.NRGBA{0, 0, 0, 255}, color.NRGBA{128, 64, 32, 255}, BlendMultiply, 1),
		0, 0, 0, 255)

	// Screen by black = source; by white = white.
	eq(t, blend1(t, color.NRGBA{0, 0, 0, 255}, color.NRGBA{128, 64, 32, 255}, BlendScreen, 1),
		128, 64, 32, 255)
	eq(t, blend1(t, color.NRGBA{255, 255, 255, 255}, color.NRGBA{128, 64, 32, 255}, BlendScreen, 1),
		255, 255, 255, 255)

	// Darken / Lighten.
	eq(t, blend1(t, color.NRGBA{100, 200, 50, 255}, color.NRGBA{150, 100, 50, 255}, BlendDarken, 1),
		100, 100, 50, 255)
	eq(t, blend1(t, color.NRGBA{100, 200, 50, 255}, color.NRGBA{150, 100, 50, 255}, BlendLighten, 1),
		150, 200, 50, 255)

	// Add (clamped) and Subtract (clamped).
	eq(t, blend1(t, color.NRGBA{100, 200, 0, 255}, color.NRGBA{50, 100, 0, 255}, BlendAdd, 1),
		150, 255, 0, 255)
	eq(t, blend1(t, color.NRGBA{200, 100, 0, 255}, color.NRGBA{50, 150, 0, 255}, BlendSubtract, 1),
		150, 0, 0, 255)

	// Difference is symmetric.
	eq(t, blend1(t, color.NRGBA{200, 50, 0, 255}, color.NRGBA{50, 200, 0, 255}, BlendDifference, 1),
		150, 150, 0, 255)

	// ColorDodge by black = backdrop; ColorBurn by white = backdrop.
	eq(t, blend1(t, cb, color.NRGBA{0, 0, 0, 255}, BlendColorDodge, 1), 100, 150, 200, 255)
	eq(t, blend1(t, cb, color.NRGBA{255, 255, 255, 255}, BlendColorBurn, 1), 100, 150, 200, 255)

	// Overlay/HardLight endpoints: over black backdrop overlay→black; hard-light by mid-black→black.
	eq(t, blend1(t, color.NRGBA{0, 0, 0, 255}, color.NRGBA{128, 128, 128, 255}, BlendOverlay, 1),
		0, 0, 0, 255)
	eq(t, blend1(t, color.NRGBA{200, 200, 200, 255}, color.NRGBA{0, 0, 0, 255}, BlendHardLight, 1),
		0, 0, 0, 255)

	// SoftLight by mid-grey (cs=0.5) is identity on the backdrop.
	got := blend1(t, cb, color.NRGBA{128, 128, 128, 255}, BlendSoftLight, 1)
	// cs=128/255≈0.502 → uses the >0.5 branch with (2cs-1)≈0.0039, near-identity; allow ±2.
	for i, want := range []uint8{100, 150, 200} {
		ch := []uint8{got.R, got.G, got.B}[i]
		if ch < want-2 || ch > want+2 {
			t.Fatalf("soft-light near-identity ch%d: got %d want ~%d", i, ch, want)
		}
	}
}

func TestBlendOntoTransparent(t *testing.T) {
	// Opaque source over fully transparent backdrop → source unchanged.
	got := blend1(t, color.NRGBA{0, 0, 0, 0}, color.NRGBA{40, 80, 120, 255}, BlendMultiply, 1)
	eq(t, got, 40, 80, 120, 255)
}

func TestBlendAlphaOut(t *testing.T) {
	// 50% source over 50% backdrop → ao = 0.5 + 0.5*0.5 = 0.75 → 191.
	got := blend1(t, color.NRGBA{0, 0, 0, 128}, color.NRGBA{255, 255, 255, 255}, BlendNormal, 0.5)
	if got.A < 190 || got.A > 192 {
		t.Fatalf("alpha out: got %d want ~191", got.A)
	}
}

func TestRenderSolidLayers(t *testing.T) {
	d := NewDocument(4, 4)
	d.Root.Children = append(d.Root.Children,
		NewSolid("bg", 0, 0, 4, 4, color.NRGBA{10, 20, 30, 255}),
		NewSolid("fg", 1, 1, 2, 2, color.NRGBA{200, 100, 50, 255}),
	)
	c := NewCompositor(nil, nil)
	img := c.Render(d, nil)
	eq(t, img.NRGBAAt(0, 0), 10, 20, 30, 255)   // background only
	eq(t, img.NRGBAAt(1, 1), 200, 100, 50, 255) // fg on top
	eq(t, img.NRGBAAt(3, 3), 10, 20, 30, 255)   // outside fg box
}

func TestVisibilityAndOpacityGate(t *testing.T) {
	d := NewDocument(2, 2)
	fg := NewSolid("fg", 0, 0, 2, 2, color.NRGBA{255, 0, 0, 255})
	fg.Visible = false
	bg := NewSolid("bg", 0, 0, 2, 2, color.NRGBA{0, 0, 255, 255})
	d.Root.Children = append(d.Root.Children, bg, fg)
	c := NewCompositor(nil, nil)
	img := c.Render(d, nil)
	eq(t, img.NRGBAAt(0, 0), 0, 0, 255, 255) // invisible fg skipped
}

func TestGroupOpacity(t *testing.T) {
	// Group at 50% opacity over an opaque backdrop halves a fully-opaque child contribution.
	d := NewDocument(1, 1)
	bg := NewSolid("bg", 0, 0, 1, 1, color.NRGBA{0, 0, 0, 255})
	g := NewGroup("g")
	g.Opacity = 0.5
	g.Children = append(g.Children, NewSolid("c", 0, 0, 1, 1, color.NRGBA{255, 255, 255, 255}))
	d.Root.Children = append(d.Root.Children, bg, g)
	c := NewCompositor(nil, nil)
	img := c.Render(d, nil)
	got := img.NRGBAAt(0, 0)
	if got.R < 126 || got.R > 129 {
		t.Fatalf("group opacity: got R=%d want ~127", got.R)
	}
}

func TestCacheReusesRaster(t *testing.T) {
	d := NewDocument(4, 4)
	s := NewSolid("s", 0, 0, 4, 4, color.NRGBA{1, 2, 3, 255})
	d.Root.Children = append(d.Root.Children, s)
	c := NewCompositor(nil, nil)
	_ = c.Render(d, nil)
	r1 := c.cache[s.ID].img
	// Move layer (transform only) → raster must be reused (same pointer).
	s.Transform.X = 0 // still simple; re-render
	_ = c.Render(d, nil)
	if c.cache[s.ID].img != r1 {
		t.Fatal("transform-only change should reuse cached raster")
	}
	// Change content (color) → raster re-rendered (new pointer).
	s.Solid.Color = RGBA{9, 9, 9, 255}
	_ = c.Render(d, nil)
	if c.cache[s.ID].img == r1 {
		t.Fatal("content change should invalidate cached raster")
	}
}
