package vroverlay

import "testing"

// rowForV mirrors applyHover's click-where-you-point-now row mapping: bottom-origin v → top-Y → row.
func rowForV(v float32, mh int) int { return menuRowAt((1-v)*float32(mh), mh) }

// The click path now maps the 1€-smoothed hit v DIRECTLY to a row (hysteresis removed) so dot =
// highlight = click. Lock the mapping: the bottom-origin→top-Y flip and the row bands must be exact,
// or the pointer clicks the wrong row (the whole point of the fix).
func TestRowMappingDirect(t *testing.T) {
	const rows = 5
	mh := MenuRowH * (rows + 1) // 336: how applyHover sizes the hit area

	// Bottom-origin v: v≈1 is the TOP of the quad (row 0 area after the title band), v≈0 the bottom.
	// Mid of row 0's band (topY [56,112)) → row 0.
	if got := rowForV(vForTopY(MenuRowH*1+MenuRowH/2, mh), mh); got != 0 {
		t.Fatalf("row0 = %d, want 0", got)
	}
	// Mid of row 1's band (topY [112,168)) → row 1. A point a hair past the boundary switches
	// immediately now (no ¼-row hold) - that is the click-where-you-point behavior.
	if got := rowForV(vForTopY(MenuRowH*2+2, mh), mh); got != 1 {
		t.Fatalf("just-past-boundary = %d, want 1 (no hysteresis hold)", got)
	}
	// Last row.
	if got := rowForV(vForTopY(MenuRowH*(rows+1)-MenuRowH/2, mh), mh); got != rows-1 {
		t.Fatalf("last row = %d, want %d", got, rows-1)
	}
	// Title-bar band [0,56) → -1 (no clickable row).
	if got := rowForV(vForTopY(MenuRowH/2, mh), mh); got != -1 {
		t.Fatalf("title bar = %d, want -1", got)
	}
}

// vForTopY converts a top-left pixel Y to the bottom-origin v applyHover receives (inverse of its 1-v).
func vForTopY(topY, mh int) float32 { return 1 - float32(topY)/float32(mh) }

// oneEuro must converge to a held value (kills rest jitter) and reset cleanly (no lerp across a jump).
func TestOneEuroConvergeReset(t *testing.T) {
	var f oneEuro
	const dt = 1.0 / 90
	var y float64
	for i := 0; i < 400; i++ {
		y = f.filter(1.0, dt)
	}
	if d := y - 1.0; d > 0.01 || d < -0.01 {
		t.Fatalf("held-value convergence = %.4f, want ≈1.0", y)
	}
	f.reset()
	if got := f.filter(5.0, dt); got != 5.0 {
		t.Fatalf("post-reset first sample = %.4f, want 5.0 (no lerp from old state)", got)
	}
}
