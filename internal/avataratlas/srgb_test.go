package avataratlas

import "testing"

// TestSRGBSurvivalAllBytes proves the world shader's math spec: every stored sRGB8 byte
// survives hardware EOTF decode (sampler) -> exact piecewise OETF re-encode + round
// (vertex shader) byte-exact. This is the §11 pipeline rule for DATA texels.
func TestSRGBSurvivalAllBytes(t *testing.T) {
	for b := 0; b < 256; b++ {
		lin := SRGBByteToLinear(uint8(b))
		got := LinearToSRGBByte(lin)
		if got != uint8(b) {
			t.Errorf("byte %d: EOTF->%.17g->OETF+round = %d (want identity)", b, lin, got)
		}
	}
}

// TestSRGBPiecewiseAnchors pins the exact spec constants (NOT pow-2.2): linear-segment
// boundary values and endpoints.
func TestSRGBPiecewiseAnchors(t *testing.T) {
	cases := []struct{ s, l float64 }{
		{0, 0},
		{0.04045, 0.04045 / 12.92}, // EOTF linear-segment upper edge
		{1, 1},
	}
	for _, c := range cases {
		if got := SRGBToLinear(c.s); got != c.l {
			t.Errorf("SRGBToLinear(%v) = %v, want %v", c.s, got, c.l)
		}
	}
	if got := LinearToSRGB(0.0031308); got != 0.0031308*12.92 {
		t.Errorf("LinearToSRGB(0.0031308) = %v, want %v", got, 0.0031308*12.92)
	}
	if got := LinearToSRGB(0); got != 0 {
		t.Errorf("LinearToSRGB(0) = %v, want 0", got)
	}
	// Round-trip of the OETF around the knee stays monotonic + inverse-consistent.
	for _, l := range []float64{0.001, 0.0031308, 0.004, 0.5, 0.99} {
		s := LinearToSRGB(l)
		if back := SRGBToLinear(s); back < l-1e-12 || back > l+1e-12 {
			t.Errorf("OETF/EOTF inverse drift at %v: %v", l, back)
		}
	}
}
