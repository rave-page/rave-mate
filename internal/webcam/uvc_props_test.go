package webcam

import "testing"

func TestClampProp(t *testing.T) {
	cases := []struct {
		v, min, max, step, want int32
	}{
		{50, 0, 100, 1, 50},   // in range, unit step
		{-10, 0, 100, 1, 0},   // below min
		{200, 0, 100, 1, 100}, // above max
		{7, 0, 100, 5, 5},     // snap down
		{8, 0, 100, 5, 10},    // snap up
		{98, 0, 100, 5, 100},  // snap to max exactly
		{99, 0, 100, 25, 100}, // snap onto max grid point
		{-3, -10, 10, 4, -2},  // grid anchored at min: -10,-6,-2,…
		{9, -10, 10, 4, 10},   // top of anchored grid (−10+20)
		{5, 0, 9, 4, 4},       // snapped value may not exceed max → step back
		{3, 0, 100, 0, 3},     // degenerate step
		{3, 10, 5, 1, 3},      // degenerate range passes through
	}
	for _, c := range cases {
		if got := clampProp(c.v, c.min, c.max, c.step); got != c.want {
			t.Errorf("clampProp(%d,[%d..%d]/%d) = %d, want %d", c.v, c.min, c.max, c.step, got, c.want)
		}
	}
}

func TestPropByID(t *testing.T) {
	p, ok := propByID("zoom")
	if !ok || p.Iface != ifaceCamCtl || p.Index != 3 {
		t.Fatalf("zoom: %+v ok=%t", p, ok)
	}
	p, ok = propByID("whiteBalance")
	if !ok || p.Iface != ifaceProcAmp || p.Index != 7 {
		t.Fatalf("whiteBalance: %+v ok=%t", p, ok)
	}
	if _, ok := propByID("nope"); ok {
		t.Fatal("unknown id resolved")
	}
}
