package vrm

// Mat4 is a 4x4 column-major matrix (glTF convention): element [col*4+row].
type Mat4 [16]float32

// Identity returns the identity matrix.
func Identity() Mat4 { return Mat4{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1} }

// Mul returns a*b (column-major).
func (a Mat4) Mul(b Mat4) Mat4 {
	var o Mat4
	for c := range 4 {
		for r := range 4 {
			var s float32
			for k := range 4 {
				s += a[k*4+r] * b[c*4+k]
			}
			o[c*4+r] = s
		}
	}
	return o
}

// TRS builds a column-major transform from translation, rotation quat (x,y,z,w), scale.
func TRS(t [3]float32, q [4]float32, s [3]float32) Mat4 {
	x, y, z, w := q[0], q[1], q[2], q[3]
	xx, yy, zz := x*x, y*y, z*z
	xy, xz, yz := x*y, x*z, y*z
	wx, wy, wz := w*x, w*y, w*z
	var m Mat4
	m[0], m[1], m[2], m[3] = (1-2*(yy+zz))*s[0], (2*(xy+wz))*s[0], (2*(xz-wy))*s[0], 0
	m[4], m[5], m[6], m[7] = (2*(xy-wz))*s[1], (1-2*(xx+zz))*s[1], (2*(yz+wx))*s[1], 0
	m[8], m[9], m[10], m[11] = (2*(xz+wy))*s[2], (2*(yz-wx))*s[2], (1-2*(xx+yy))*s[2], 0
	m[12], m[13], m[14], m[15] = t[0], t[1], t[2], 1
	return m
}

// TransformPoint applies m to point p (w=1).
func (m Mat4) TransformPoint(p [3]float32) [3]float32 {
	return [3]float32{
		m[0]*p[0] + m[4]*p[1] + m[8]*p[2] + m[12],
		m[1]*p[0] + m[5]*p[1] + m[9]*p[2] + m[13],
		m[2]*p[0] + m[6]*p[1] + m[10]*p[2] + m[14],
	}
}

// TransformDir applies m's upper-3x3 to direction d (w=0).
func (m Mat4) TransformDir(d [3]float32) [3]float32 {
	return [3]float32{
		m[0]*d[0] + m[4]*d[1] + m[8]*d[2],
		m[1]*d[0] + m[5]*d[1] + m[9]*d[2],
		m[2]*d[0] + m[6]*d[1] + m[10]*d[2],
	}
}

// Translation returns m's translation column.
func (m Mat4) Translation() [3]float32 { return [3]float32{m[12], m[13], m[14]} }

// fromColMajor16 builds a Mat4 from a glTF 16-float column-major slice.
func fromColMajor16(s []float64) Mat4 {
	var m Mat4
	for i := 0; i < 16 && i < len(s); i++ {
		m[i] = float32(s[i])
	}
	return m
}
