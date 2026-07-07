package vroverlay

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-3 }

func TestEulerRoundTrip(t *testing.T) {
	cases := []struct{ yaw, pitch, roll, x, y, z float64 }{
		{0, 0, 0, 1, 2, 3},
		{30, 0, 0, 0, 1.5, -1},
		{0, 20, 0, 0.2, 0, 0},
		{0, 0, 45, 0, 0, 0},
		{15, -10, 25, 0.1, 1.2, -0.8},
	}
	for _, c := range cases {
		m := EulerToMat(c.yaw, c.pitch, c.roll, c.x, c.y, c.z)
		yaw, pitch, roll, x, y, z := MatToEuler(m)
		if !approx(yaw, c.yaw) || !approx(pitch, c.pitch) || !approx(roll, c.roll) {
			t.Errorf("euler rt: in(%v,%v,%v) out(%v,%v,%v)", c.yaw, c.pitch, c.roll, yaw, pitch, roll)
		}
		if !approx(x, c.x) || !approx(y, c.y) || !approx(z, c.z) {
			t.Errorf("trans rt: in(%v,%v,%v) out(%v,%v,%v)", c.x, c.y, c.z, x, y, z)
		}
	}
}

func TestInvMulIdentity(t *testing.T) {
	m := EulerToMat(20, -15, 33, 0.5, 1.1, -0.7)
	id := MulMat(InvMat(m), m)
	// Should be identity: diagonal 1, translation 0.
	for r := 0; r < 3; r++ {
		for c := 0; c < 4; c++ {
			want := float32(0)
			if r == c {
				want = 1
			}
			if math.Abs(float64(id[r*4+c]-want)) > 1e-3 {
				t.Fatalf("inv*m not identity at (%d,%d)=%v", r, c, id[r*4+c])
			}
		}
	}
}

func TestGrabOffsetRecovers(t *testing.T) {
	// Simulate grab: overlay O, controller C at grab → offset = inv(C)*O. Move controller to C2 →
	// overlay should be C2*offset; if C2==C, overlay==O.
	o := EulerToMat(10, 5, 0, 0.3, 1.4, -1.0)
	c := EulerToMat(40, 0, 0, 0.1, 1.0, -0.5)
	offset := MulMat(InvMat(c), o)
	got := MulMat(c, offset)
	for i := 0; i < 12; i++ {
		if math.Abs(float64(got[i]-o[i])) > 1e-3 {
			t.Fatalf("grab offset lost overlay pose at %d: %v vs %v", i, got[i], o[i])
		}
	}
}
