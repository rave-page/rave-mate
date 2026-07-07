package vroverlay

import "math"

const deg2rad = math.Pi / 180

// Mat34 is a row-major 3x4 rigid transform (3x3 rotation in cols 0-2, translation in col 3),
// matching OpenVR's HmdMatrix34_t layout. Index (row r, col c) = r*4+c.
type Mat34 [12]float32

func at(m Mat34, r, c int) float32 { return m[r*4+c] }

// EulerToMat builds a transform from euler degrees (R = Ry*Rx*Rz, the C fill_matrix convention) +
// translation in metres.
func EulerToMat(yawDeg, pitchDeg, rollDeg float64, x, y, z float64) Mat34 {
	yaw, pitch, roll := yawDeg*deg2rad, pitchDeg*deg2rad, rollDeg*deg2rad
	cy, sy := math.Cos(yaw), math.Sin(yaw)
	cp, sp := math.Cos(pitch), math.Sin(pitch)
	cr, sr := math.Cos(roll), math.Sin(roll)
	var m Mat34
	m[0], m[1], m[2], m[3] = f(cy*cr+sy*sp*sr), f(cr*sy*sp-cy*sr), f(cp*sy), f(x)
	m[4], m[5], m[6], m[7] = f(cp*sr), f(cp*cr), f(-sp), f(y)
	m[8], m[9], m[10], m[11] = f(cy*sp*sr-sy*cr), f(sy*sr+cy*cr*sp), f(cy*cp), f(z)
	return m
}

// MatToEuler extracts euler degrees (same Ry*Rx*Rz convention) + translation from a transform.
func MatToEuler(m Mat34) (yawDeg, pitchDeg, rollDeg, x, y, z float64) {
	sp := -float64(at(m, 1, 2))
	if sp > 1 {
		sp = 1
	} else if sp < -1 {
		sp = -1
	}
	pitch := math.Asin(sp)
	cp := math.Cos(pitch)
	var yaw, roll float64
	if math.Abs(cp) > 1e-6 {
		roll = math.Atan2(float64(at(m, 1, 0)), float64(at(m, 1, 1)))
		yaw = math.Atan2(float64(at(m, 0, 2)), float64(at(m, 2, 2)))
	} else { // gimbal lock: fold roll into yaw
		roll = 0
		yaw = math.Atan2(-float64(at(m, 2, 0)), float64(at(m, 0, 0)))
	}
	return yaw / deg2rad, pitch / deg2rad, roll / deg2rad,
		float64(at(m, 0, 3)), float64(at(m, 1, 3)), float64(at(m, 2, 3))
}

// MulMat composes two rigid transforms (a then b applied as a*b, 4x4 with implicit 0,0,0,1 row).
func MulMat(a, b Mat34) Mat34 {
	var r Mat34
	for i := 0; i < 3; i++ {
		for j := 0; j < 4; j++ {
			var s float32
			for k := 0; k < 3; k++ {
				s += at(a, i, k) * at(b, k, j)
			}
			if j == 3 {
				s += at(a, i, 3) // translation column adds a's translation
			}
			r[i*4+j] = s
		}
	}
	return r
}

// InvMat inverts a rigid transform (R^T, -R^T t).
func InvMat(m Mat34) Mat34 {
	var r Mat34
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			r[i*4+j] = at(m, j, i) // transpose rotation
		}
	}
	tx, ty, tz := at(m, 0, 3), at(m, 1, 3), at(m, 2, 3)
	r[3] = -(r[0]*tx + r[1]*ty + r[2]*tz)
	r[7] = -(r[4]*tx + r[5]*ty + r[6]*tz)
	r[11] = -(r[8]*tx + r[9]*ty + r[10]*tz)
	return r
}

func f(v float64) float32 { return float32(v) }

// MatPos returns a transform's translation (metres).
func MatPos(m Mat34) (x, y, z float64) {
	return float64(m[3]), float64(m[7]), float64(m[11])
}

// MatForward returns a transform's forward axis (−Z column; the direction an HMD/overlay faces).
func MatForward(m Mat34) (x, y, z float64) {
	return -float64(m[2]), -float64(m[6]), -float64(m[10])
}

// MatToQuat extracts the rotation quaternion (x,y,z,w) from a rigid transform (motion capture).
func MatToQuat(m Mat34) [4]float32 {
	r00, r01, r02 := float64(at(m, 0, 0)), float64(at(m, 0, 1)), float64(at(m, 0, 2))
	r10, r11, r12 := float64(at(m, 1, 0)), float64(at(m, 1, 1)), float64(at(m, 1, 2))
	r20, r21, r22 := float64(at(m, 2, 0)), float64(at(m, 2, 1)), float64(at(m, 2, 2))
	tr := r00 + r11 + r22
	var x, y, z, w float64
	switch {
	case tr > 0:
		s := math.Sqrt(tr+1) * 2
		w, x, y, z = 0.25*s, (r21-r12)/s, (r02-r20)/s, (r10-r01)/s
	case r00 > r11 && r00 > r22:
		s := math.Sqrt(1+r00-r11-r22) * 2
		w, x, y, z = (r21-r12)/s, 0.25*s, (r01+r10)/s, (r02+r20)/s
	case r11 > r22:
		s := math.Sqrt(1+r11-r00-r22) * 2
		w, x, y, z = (r02-r20)/s, (r01+r10)/s, 0.25*s, (r12+r21)/s
	default:
		s := math.Sqrt(1+r22-r00-r11) * 2
		w, x, y, z = (r10-r01)/s, (r02+r20)/s, (r12+r21)/s, 0.25*s
	}
	return [4]float32{float32(x), float32(y), float32(z), float32(w)}
}
