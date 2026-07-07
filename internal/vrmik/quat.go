package vrmik

import (
	"math"

	"rave.page/mate/internal/vrm"
)

// quat is a unit quaternion (x,y,z,w) - same layout as vrm.TRS's rotation arg.
type quat [4]float32

func qMul(a, b quat) quat {
	return quat{
		a[3]*b[0] + a[0]*b[3] + a[1]*b[2] - a[2]*b[1],
		a[3]*b[1] - a[0]*b[2] + a[1]*b[3] + a[2]*b[0],
		a[3]*b[2] + a[0]*b[1] - a[1]*b[0] + a[2]*b[3],
		a[3]*b[3] - a[0]*b[0] - a[1]*b[1] - a[2]*b[2],
	}
}

func qConj(a quat) quat { return quat{-a[0], -a[1], -a[2], a[3]} }

func qNorm(a quat) quat {
	n := float32(math.Sqrt(float64(a[0]*a[0] + a[1]*a[1] + a[2]*a[2] + a[3]*a[3])))
	if n < eps {
		return quat{0, 0, 0, 1}
	}
	return quat{a[0] / n, a[1] / n, a[2] / n, a[3] / n}
}

// qAxisAngle builds a rotation of ang radians about a unit axis.
func qAxisAngle(axis [3]float32, ang float32) quat {
	h := ang / 2
	s := float32(math.Sin(float64(h)))
	return quat{axis[0] * s, axis[1] * s, axis[2] * s, float32(math.Cos(float64(h)))}
}

// qRotate applies q to vector v.
func qRotate(q quat, v [3]float32) [3]float32 {
	// t = 2 * cross(q.xyz, v); v' = v + q.w*t + cross(q.xyz, t)
	u := [3]float32{q[0], q[1], q[2]}
	t := scale(cross(u, v), 2)
	return add(add(v, scale(t, q[3])), cross(u, t))
}

// qBetween is the shortest-arc rotation mapping unit vector a to unit vector b.
func qBetween(a, b [3]float32) quat {
	d := dot(a, b)
	if d >= 1-eps {
		return quat{0, 0, 0, 1} // already aligned
	}
	if d <= -1+eps { // opposite: rotate 180° about any perpendicular axis
		axis := cross([3]float32{1, 0, 0}, a)
		if length(axis) < eps {
			axis = cross([3]float32{0, 1, 0}, a)
		}
		return qAxisAngle(normalize(axis), math.Pi)
	}
	c := cross(a, b)
	q := quat{c[0], c[1], c[2], 1 + d}
	return qNorm(q)
}

// rotQuat extracts the rotation quaternion from a Mat4's upper-3×3 (columns normalized to drop scale).
func rotQuat(m vrm.Mat4) quat {
	cx := normalize([3]float32{m[0], m[1], m[2]})
	cy := normalize([3]float32{m[4], m[5], m[6]})
	cz := normalize([3]float32{m[8], m[9], m[10]})
	r00, r10, r20 := cx[0], cx[1], cx[2]
	r01, r11, r21 := cy[0], cy[1], cy[2]
	r02, r12, r22 := cz[0], cz[1], cz[2]
	tr := r00 + r11 + r22
	var q quat
	switch {
	case tr > 0:
		s := float32(math.Sqrt(float64(tr+1))) * 2
		q = quat{(r21 - r12) / s, (r02 - r20) / s, (r10 - r01) / s, 0.25 * s}
	case r00 > r11 && r00 > r22:
		s := float32(math.Sqrt(float64(1+r00-r11-r22))) * 2
		q = quat{0.25 * s, (r01 + r10) / s, (r02 + r20) / s, (r21 - r12) / s}
	case r11 > r22:
		s := float32(math.Sqrt(float64(1+r11-r00-r22))) * 2
		q = quat{(r01 + r10) / s, 0.25 * s, (r12 + r21) / s, (r02 - r20) / s}
	default:
		s := float32(math.Sqrt(float64(1+r22-r00-r11))) * 2
		q = quat{(r02 + r20) / s, (r12 + r21) / s, 0.25 * s, (r10 - r01) / s}
	}
	return qNorm(q)
}

// decompose splits a Mat4 into translation, rotation quat, and per-axis scale.
func decompose(m vrm.Mat4) (t [3]float32, q quat, s [3]float32) {
	t = [3]float32{m[12], m[13], m[14]}
	s = [3]float32{
		length([3]float32{m[0], m[1], m[2]}),
		length([3]float32{m[4], m[5], m[6]}),
		length([3]float32{m[8], m[9], m[10]}),
	}
	q = rotQuat(m)
	return
}

// invert returns the inverse of an affine Mat4 (general 4×4 inverse via cofactors); Identity if singular.
func invert(m vrm.Mat4) vrm.Mat4 {
	var inv vrm.Mat4
	inv[0] = m[5]*m[10]*m[15] - m[5]*m[11]*m[14] - m[9]*m[6]*m[15] + m[9]*m[7]*m[14] + m[13]*m[6]*m[11] - m[13]*m[7]*m[10]
	inv[4] = -m[4]*m[10]*m[15] + m[4]*m[11]*m[14] + m[8]*m[6]*m[15] - m[8]*m[7]*m[14] - m[12]*m[6]*m[11] + m[12]*m[7]*m[10]
	inv[8] = m[4]*m[9]*m[15] - m[4]*m[11]*m[13] - m[8]*m[5]*m[15] + m[8]*m[7]*m[13] + m[12]*m[5]*m[11] - m[12]*m[7]*m[9]
	inv[12] = -m[4]*m[9]*m[14] + m[4]*m[10]*m[13] + m[8]*m[5]*m[14] - m[8]*m[6]*m[13] - m[12]*m[5]*m[10] + m[12]*m[6]*m[9]
	inv[1] = -m[1]*m[10]*m[15] + m[1]*m[11]*m[14] + m[9]*m[2]*m[15] - m[9]*m[3]*m[14] - m[13]*m[2]*m[11] + m[13]*m[3]*m[10]
	inv[5] = m[0]*m[10]*m[15] - m[0]*m[11]*m[14] - m[8]*m[2]*m[15] + m[8]*m[3]*m[14] + m[12]*m[2]*m[11] - m[12]*m[3]*m[10]
	inv[9] = -m[0]*m[9]*m[15] + m[0]*m[11]*m[13] + m[8]*m[1]*m[15] - m[8]*m[3]*m[13] - m[12]*m[1]*m[11] + m[12]*m[3]*m[9]
	inv[13] = m[0]*m[9]*m[14] - m[0]*m[10]*m[13] - m[8]*m[1]*m[14] + m[8]*m[2]*m[13] + m[12]*m[1]*m[10] - m[12]*m[2]*m[9]
	inv[2] = m[1]*m[6]*m[15] - m[1]*m[7]*m[14] - m[5]*m[2]*m[15] + m[5]*m[3]*m[14] + m[13]*m[2]*m[7] - m[13]*m[3]*m[6]
	inv[6] = -m[0]*m[6]*m[15] + m[0]*m[7]*m[14] + m[4]*m[2]*m[15] - m[4]*m[3]*m[14] - m[12]*m[2]*m[7] + m[12]*m[3]*m[6]
	inv[10] = m[0]*m[5]*m[15] - m[0]*m[7]*m[13] - m[4]*m[1]*m[15] + m[4]*m[3]*m[13] + m[12]*m[1]*m[7] - m[12]*m[3]*m[5]
	inv[14] = -m[0]*m[5]*m[14] + m[0]*m[6]*m[13] + m[4]*m[1]*m[14] - m[4]*m[2]*m[13] - m[12]*m[1]*m[6] + m[12]*m[2]*m[5]
	inv[3] = -m[1]*m[6]*m[11] + m[1]*m[7]*m[10] + m[5]*m[2]*m[11] - m[5]*m[3]*m[10] - m[9]*m[2]*m[7] + m[9]*m[3]*m[6]
	inv[7] = m[0]*m[6]*m[11] - m[0]*m[7]*m[10] - m[4]*m[2]*m[11] + m[4]*m[3]*m[10] + m[8]*m[2]*m[7] - m[8]*m[3]*m[6]
	inv[11] = -m[0]*m[5]*m[11] + m[0]*m[7]*m[9] + m[4]*m[1]*m[11] - m[4]*m[3]*m[9] - m[8]*m[1]*m[7] + m[8]*m[3]*m[5]
	inv[15] = m[0]*m[5]*m[10] - m[0]*m[6]*m[9] - m[4]*m[1]*m[10] + m[4]*m[2]*m[9] + m[8]*m[1]*m[6] - m[8]*m[2]*m[5]
	det := m[0]*inv[0] + m[1]*inv[4] + m[2]*inv[8] + m[3]*inv[12]
	if det > -eps && det < eps {
		return vrm.Identity()
	}
	id := 1 / det
	for i := range inv {
		inv[i] *= id
	}
	return inv
}
