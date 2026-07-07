package vrmdyn

// Small quat/vec3 helpers, duplicated from internal/vrmik (kept unexported there;
// duplication keeps vrmik untouched). Quat layout (x,y,z,w) matches vrm.TRS.

import (
	"math"

	"rave.page/mate/internal/vrm"
)

const eps = 1e-6

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

func qAxisAngle(axis [3]float32, ang float32) quat {
	h := ang / 2
	s := float32(math.Sin(float64(h)))
	return quat{axis[0] * s, axis[1] * s, axis[2] * s, float32(math.Cos(float64(h)))}
}

// qRotate applies q to vector v.
func qRotate(q quat, v [3]float32) [3]float32 {
	u := [3]float32{q[0], q[1], q[2]}
	t := scale(cross(u, v), 2)
	return add(add(v, scale(t, q[3])), cross(u, t))
}

// qBetween is the shortest-arc rotation mapping unit vector a to unit vector b.
func qBetween(a, b [3]float32) quat {
	d := dot(a, b)
	if d >= 1-eps {
		return quat{0, 0, 0, 1}
	}
	if d <= -1+eps { // opposite: 180° about any perpendicular axis
		axis := cross([3]float32{1, 0, 0}, a)
		if length(axis) < eps {
			axis = cross([3]float32{0, 1, 0}, a)
		}
		return qAxisAngle(normalize(axis), math.Pi)
	}
	c := cross(a, b)
	return qNorm(quat{c[0], c[1], c[2], 1 + d})
}

// rotQuat extracts the rotation quaternion from a Mat4's upper-3×3 (scale dropped).
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

// matTS extracts translation + per-axis scale from a column-major Mat4.
func matTS(m vrm.Mat4) (t, s [3]float32) {
	t = [3]float32{m[12], m[13], m[14]}
	s = [3]float32{
		length([3]float32{m[0], m[1], m[2]}),
		length([3]float32{m[4], m[5], m[6]}),
		length([3]float32{m[8], m[9], m[10]}),
	}
	return
}

func mpos(m vrm.Mat4) [3]float32 { return [3]float32{m[12], m[13], m[14]} }

func sub(a, b [3]float32) [3]float32 { return [3]float32{a[0] - b[0], a[1] - b[1], a[2] - b[2]} }
func add(a, b [3]float32) [3]float32 { return [3]float32{a[0] + b[0], a[1] + b[1], a[2] + b[2]} }
func scale(a [3]float32, s float32) [3]float32 {
	return [3]float32{a[0] * s, a[1] * s, a[2] * s}
}
func dot(a, b [3]float32) float32 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func cross(a, b [3]float32) [3]float32 {
	return [3]float32{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}
func length(a [3]float32) float32  { return float32(math.Sqrt(float64(dot(a, a)))) }
func dist(a, b [3]float32) float32 { return length(sub(a, b)) }
func normalize(a [3]float32) [3]float32 {
	l := length(a)
	if l < eps {
		return [3]float32{0, 0, 1}
	}
	return scale(a, 1/l)
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func powf(b, e float32) float32 { return float32(math.Pow(float64(b), float64(e))) }
