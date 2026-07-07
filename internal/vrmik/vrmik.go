// Package vrmik poses a VRM humanoid from a recorded VR motion sample via simple analytic IK.
// Pure + testable (no UI): given the loaded vrm.Model and one vrmotion sample (head + controllers/
// trackers), it returns per-node LOCAL transform overrides (len == len(Nodes)) that place the
// avatar. The caller feeds these to vrm.WorldFrom → SkinMatrices → PosedPositions to skin + render.
//
// Coordinate assumption: the sample is OpenVR right-handed standing-universe (metres, +Y up,
// -Z forward). The VRM/glTF avatar is right-handed +Y up with the model facing +Z. We convert
// OpenVR → avatar space with a 180° yaw ({-x,y,-z}; quats {-x,y,-z,w}) - a proper rotation,
// so chirality is preserved: the user's right hand drives the avatar's right arm. (The
// earlier Z-only flip was a reflection and mirrored every limb.) Isolated in convP/convQ.
package vrmik

import (
	"math"

	"rave.page/mate/internal/vrm"
	"rave.page/mate/internal/vrmotion"
)

const eps = 1e-6

// Pose returns per-node LOCAL overrides posing m's humanoid from sample (raw take keys, no
// calibration). Prefer PoseRT with a Calibrate()d Retarget for recorded takes.
func Pose(m *vrm.Model, sample map[int]vrmotion.Pose) []vrm.Mat4 {
	return PoseRT(m, sample, nil)
}

// PoseRT poses m's humanoid from sample after applying rt (recenter/scale/role remap; nil =
// passthrough). Falls back to the rest pose when the model has no humanoid map or sample is
// nil. Bones absent from the humanoid map degrade gracefully (that limb stays at rest).
func PoseRT(m *vrm.Model, sample map[int]vrmotion.Pose, rt *Retarget) []vrm.Mat4 {
	local := m.RestLocal()
	if len(m.Humanoid) == 0 || sample == nil {
		return local
	}
	sample = rt.Normalize(sample)
	restW := m.RestWorld()

	hips := m.HumanoidNode("hips")
	head := m.HumanoidNode("head")

	// Body yaw from the HMD's Y-twist (stable under pitch, so looking down doesn't spin the
	// body). Applied to the hips AND used to rotate the elbow/knee pole hints with the body.
	bodyYaw := quat{0, 0, 0, 1}
	if hp, ok := sample[RoleHead]; ok {
		cq := convQ(hp.Rot)
		yaw := 2 * float32(math.Atan2(float64(cq[1]), float64(cq[3])))
		bodyYaw = qAxisAngle([3]float32{0, 1, 0}, yaw)
	}

	// Hips: hips tracker if present, else drop straight down from the head by the rest torso length.
	if hips >= 0 {
		var target [3]float32
		have := false
		if p, ok := sample[RoleHips]; ok {
			target, have = convP(p.Pos), true
		} else if hp, ok := sample[RoleHead]; ok && head >= 0 {
			torso := dist(pos(restW[head]), pos(restW[hips]))
			ht := convP(hp.Pos)
			target, have = [3]float32{ht[0], ht[1] - torso, ht[2]}, true
		}
		if have {
			placeRoot(m, local, restW, hips, target)
		}
		// Turn the whole body with the head. Arms/legs IK below re-anchors off the rotated
		// shoulders/hips, so turns replicate instead of leaving the body at rest.
		if _, ok := sample[RoleHead]; ok {
			setWorldRot(m, local, m.WorldFrom(local), hips, qMul(bodyYaw, rotQuat(restW[hips])))
		}
	}

	// Head: HMD rotation as a delta on the bone's REST world rotation (FBX rigs have
	// non-identity rest orientations - writing the HMD quat absolutely twists the skull).
	if head >= 0 {
		if hp, ok := sample[RoleHead]; ok {
			setWorldRot(m, local, m.WorldFrom(local), head, qMul(convQ(hp.Rot), rotQuat(restW[head])))
		}
	}

	// Arms/legs: 2-bone IK. Pole hints are body-relative (rotated by bodyYaw) and per-side -
	// elbows point down + OUT + slightly back (a lateral component keeps the bend plane stable
	// when the hand hangs straight down, where a pure-down pole degenerates and twists the arm).
	pole := func(p poleDir) poleDir { return poleDir(qRotate(bodyYaw, [3]float32(p))) }
	if p, ok := sample[RoleLeftHand]; ok {
		twoBone(m, local, restW, "leftUpperArm", "leftLowerArm", "leftHand", convP(p.Pos), pole(leftElbowPole))
	}
	if p, ok := sample[RoleRightHand]; ok {
		twoBone(m, local, restW, "rightUpperArm", "rightLowerArm", "rightHand", convP(p.Pos), pole(rightElbowPole))
	}
	// Legs: 2-bone IK to foot trackers if present, knees biased forward.
	if p, ok := sample[RoleLeftFoot]; ok {
		twoBone(m, local, restW, "leftUpperLeg", "leftLowerLeg", "leftFoot", convP(p.Pos), pole(kneePole))
	}
	if p, ok := sample[RoleRightFoot]; ok {
		twoBone(m, local, restW, "rightUpperLeg", "rightLowerLeg", "rightFoot", convP(p.Pos), pole(kneePole))
	}
	return local
}

// poleDir biases the elbow/knee bend: a world offset added to the joint anchor to define the bend plane.
type poleDir [3]float32

// Body-relative pole hints (body facing +Z, avatar's left = +X); rotated by body yaw at pose time.
var (
	leftElbowPole  = poleDir{0.5, -0.8, -0.4}  // down, out to the left, slightly back
	rightElbowPole = poleDir{-0.5, -0.8, -0.4} // down, out to the right, slightly back
	kneePole       = poleDir{0, -0.2, 1}       // knees point forward
)

// twoBone solves an analytic 2-bone IK for chain root→mid→end so end reaches target (avatar space),
// writing LOCAL rotation overrides into local. No-op if any bone is missing.
func twoBone(m *vrm.Model, local, restW []vrm.Mat4, rootB, midB, endB string, target [3]float32, pole poleDir) {
	root, mid, end := m.HumanoidNode(rootB), m.HumanoidNode(midB), m.HumanoidNode(endB)
	if root < 0 || mid < 0 || end < 0 {
		return
	}
	l1 := dist(pos(restW[root]), pos(restW[mid]))
	l2 := dist(pos(restW[mid]), pos(restW[end]))
	if l1 <= eps || l2 <= eps {
		return
	}
	world := m.WorldFrom(local)
	s := pos(world[root])
	elbow := solveElbow(s, target, add(s, [3]float32(pole)), l1, l2)

	// Aim upper bone (root) at the elbow.
	fromRoot := sub(pos(world[mid]), s)
	aimBone(m, local, world, root, fromRoot, sub(elbow, s))

	// Aim lower bone (mid) from the moved elbow toward the target.
	world = m.WorldFrom(local)
	e := pos(world[mid])
	fromMid := sub(pos(world[end]), e)
	aimBone(m, local, world, mid, fromMid, sub(target, e))
}

// solveElbow returns the elbow world position for a 2-bone chain anchored at s with segment lengths
// l1,l2 reaching toward target, bending toward poleP. Beyond reach the chain extends straight (elbow
// on the s→target line).
func solveElbow(s, target, poleP [3]float32, l1, l2 float32) [3]float32 {
	toT := sub(target, s)
	d := length(toT)
	if d < eps {
		return add(s, scale([3]float32{0, -1, 0}, l1)) // degenerate: drop straight down
	}
	dir := scale(toT, 1/d)
	dc := d
	if dc > l1+l2 {
		dc = l1 + l2
	}
	cosA := clamp((l1*l1+dc*dc-l2*l2)/(2*l1*dc), -1, 1)
	a := float32(math.Acos(float64(cosA)))
	axis := cross(dir, sub(poleP, s))
	if length(axis) < eps {
		axis = cross(dir, [3]float32{0, 1, 0})
		if length(axis) < eps {
			axis = cross(dir, [3]float32{1, 0, 0})
		}
	}
	axis = normalize(axis)
	upperDir := qRotate(qAxisAngle(axis, a), dir)
	return add(s, scale(upperDir, l1))
}

// aimBone sets joint's LOCAL rotation so its bone direction rotates from fromDir to toDir (world),
// preserving the joint's rest local translation + scale. world = current world matrices.
func aimBone(m *vrm.Model, local, world []vrm.Mat4, joint int, fromDir, toDir [3]float32) {
	dR := qBetween(normalize(fromDir), normalize(toDir))
	setWorldRot(m, local, world, joint, qMul(dR, rotQuat(world[joint])))
}

// setWorldRot writes joint's LOCAL rotation so its world rotation becomes qw, keeping rest local
// translation + scale. world = current world matrices (for the parent's world rotation).
func setWorldRot(m *vrm.Model, local, world []vrm.Mat4, joint int, qw quat) {
	rp := quat{0, 0, 0, 1}
	if p := m.Nodes[joint].Parent; p >= 0 {
		rp = rotQuat(world[p])
	}
	localQ := qMul(qConj(rp), qw)
	t, _, s := decompose(local[joint])
	local[joint] = vrm.TRS(t, [4]float32(localQ), s)
}

// placeRoot sets joint's LOCAL translation so its world position becomes target, keeping rest
// rotation + scale. Uses the parent's REST world (ancestors above the root aren't posed).
func placeRoot(m *vrm.Model, local, restW []vrm.Mat4, joint int, target [3]float32) {
	pw := vrm.Identity()
	if p := m.Nodes[joint].Parent; p >= 0 {
		pw = restW[p]
	}
	lt := invert(pw).TransformPoint(target)
	_, q, s := decompose(local[joint])
	local[joint] = vrm.TRS(lt, [4]float32(q), s)
}

// ── coordinate conversion (OpenVR → avatar space) ────────────────────────────

// ConvPos maps an OpenVR position into avatar space (same mapping the IK uses), so callers can
// draw take-space points (e.g. a head trail) aligned with the posed avatar.
func ConvPos(p [3]float32) [3]float32 { return convP(p) }

func convP(p [3]float32) [3]float32 { return [3]float32{-p[0], p[1], -p[2]} }
func convQ(q [4]float32) quat       { return quat{-q[0], q[1], -q[2], q[3]} } // 180° yaw conjugation
func pos(m vrm.Mat4) [3]float32     { return [3]float32{m[12], m[13], m[14]} }

// ── vec3 helpers ─────────────────────────────────────────────────────────────

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
func clamp(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
