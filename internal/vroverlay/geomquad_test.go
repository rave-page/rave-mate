package vroverlay

import (
	"math"
	"testing"
)

func approx32(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-4 }

// A 1.0 m × 0.5 m quad facing +Z at a known center: rays down −Z must map to exact, center-consistent
// UVs - no scale factor, no runtime round-trip. This is the property the old UVWorld path violated
// (drift growing from center); locking it proves cursor-placement and selection can't disagree.
func TestHitQuadExact(t *testing.T) {
	center := EulerToMat(0, 0, 0, 0, 0, 0) // identity rotation at origin
	const w, h = float32(1.0), float32(0.5)
	down := [3]float32{0, 0, -1}

	cases := []struct {
		name         string
		origin       [3]float32
		wantU, wantV float32
		wantInside   bool
	}{
		{"center", [3]float32{0, 0, 1}, 0.5, 0.5, true},
		{"right-edge", [3]float32{0.5, 0, 1}, 1.0, 0.5, true},
		{"left-edge", [3]float32{-0.5, 0, 1}, 0.0, 0.5, true},
		{"top-edge", [3]float32{0, 0.25, 1}, 0.5, 1.0, true}, // +Y up → v=1 (bottom-origin)
		{"bottom-edge", [3]float32{0, -0.25, 1}, 0.5, 0.0, true},
		{"quarter", [3]float32{0.25, -0.125, 1}, 0.75, 0.25, true},
		{"miss-right", [3]float32{0.6, 0, 1}, 1.1, 0.5, false}, // outside the real edge → clean miss
	}
	for _, c := range cases {
		got := hitQuad(c.origin, down, center, w, h)
		if got.inside != c.wantInside || !approx32(got.u, c.wantU) || !approx32(got.v, c.wantV) {
			t.Errorf("%s: u=%.4f v=%.4f inside=%v, want u=%.4f v=%.4f inside=%v",
				c.name, got.u, got.v, got.inside, c.wantU, c.wantV, c.wantInside)
		}
		if c.wantInside && !approx32(got.dist, 1.0) {
			t.Errorf("%s: dist=%.4f, want 1.0", c.name, got.dist)
		}
	}

	// Ray pointing away from the plane → no hit (t<0).
	if got := hitQuad([3]float32{0, 0, 1}, [3]float32{0, 0, 1}, center, w, h); got.inside {
		t.Error("ray facing away must not hit")
	}
}

// Same exactness for a TRANSLATED + hand-attached-style offset center (world quad pose is composed
// exactly; the analytic map is invariant to where the quad sits).
func TestHitQuadTranslated(t *testing.T) {
	center := EulerToMat(0, 0, 0, 2, 1, -3) // moved off origin
	got := hitQuad([3]float32{2, 1, -2}, [3]float32{0, 0, -1}, center, 1.0, 0.5)
	if !got.inside || !approx32(got.u, 0.5) || !approx32(got.v, 0.5) {
		t.Fatalf("translated center hit u=%.4f v=%.4f inside=%v, want 0.5/0.5/true", got.u, got.v, got.inside)
	}
}

// projectPoint maps a tip perpendicularly onto the plane (near-field touch): UV from tip POSITION,
// dist = |perpendicular distance|, stable regardless of aim angle.
func TestProjectPoint(t *testing.T) {
	center := EulerToMat(0, 0, 0, 0, 0, 0)
	got := projectPoint([3]float32{0.25, 0, 0.1}, center, 1.0, 0.5)
	if !got.inside || !approx32(got.u, 0.75) || !approx32(got.v, 0.5) || !approx32(got.dist, 0.1) {
		t.Fatalf("project u=%.4f v=%.4f dist=%.4f inside=%v, want 0.75/0.5/0.1/true",
			got.u, got.v, got.dist, got.inside)
	}
	// A tip off the edge projects outside → not inside (clean, no clamp).
	if off := projectPoint([3]float32{0.9, 0, 0.1}, center, 1.0, 0.5); off.inside {
		t.Error("off-edge tip must project outside the quad")
	}
}
