package vrmik

import (
	"math"
	"testing"

	"rave.page/mate/internal/vrmotion"
)

// syntheticTake builds a take: head at (2, 1.8, 1) facing OpenVR forward (-Z, identity rot),
// hands on keys 5 (right, +X side) / 7 (left), a hip tracker on key 2, feet on keys 4 (right) / 6 (left).
// Keys deliberately scrambled vs the legacy 1..5 layout to prove geometric classification.
func syntheticTake() *vrmotion.Recording {
	head := [3]float32{2, 1.8, 1}
	off := func(dx, dy, dz float32) vrmotion.Pose {
		return vrmotion.Pose{Pos: [3]float32{head[0] + dx, head[1] + dy, head[2] + dz}, Rot: [4]float32{0, 0, 0, 1}}
	}
	var frames []vrmotion.Frame
	for i := range 30 {
		w := float32(i) * 0.02 // slight wiggle so stats aren't degenerate
		frames = append(frames, vrmotion.Frame{T: float64(i) / 30, Poses: map[int]vrmotion.Pose{
			0: {Pos: head, Rot: [4]float32{0, 0, 0, 1}},
			5: off(0.4+w, -0.6, -0.2),  // right hand (user right = +X when facing -Z)
			7: off(-0.4-w, -0.6, -0.2), // left hand
			2: off(0.02, -0.7, 0.02),   // hips: hugs the head axis
			4: off(0.15, -1.7, 0),      // right foot (~0.1 abs height)
			6: off(-0.15, -1.7, 0),     // left foot
		}})
	}
	return &vrmotion.Recording{Name: "synth", Hz: 30, Frames: frames, Duration: 1}
}

func TestCalibrateClassifiesRolesAndRecenters(t *testing.T) {
	m := armModel(t, true) // head rest world y = 1.5
	rt := Calibrate(m, syntheticTake())

	want := map[int]int{0: RoleHead, 5: RoleRightHand, 7: RoleLeftHand, 2: RoleHips, 4: RoleRightFoot, 6: RoleLeftFoot}
	for k, role := range want {
		if got := rt.Roles[k]; got != role {
			t.Errorf("key %d → role %d, want %d (roles=%v)", k, got, role, rt.Roles)
		}
	}
	if math.Abs(float64(rt.Origin[0]-2)) > 1e-3 || rt.Origin[1] != 0 || math.Abs(float64(rt.Origin[2]-1)) > 1e-3 {
		t.Errorf("origin=%v want (2,0,1)", rt.Origin)
	}
	if math.Abs(float64(rt.Scale-1.5/1.8)) > 1e-3 {
		t.Errorf("scale=%f want %f", rt.Scale, 1.5/1.8)
	}
}

func TestNormalizeRemapsAndTransforms(t *testing.T) {
	m := armModel(t, true)
	rec := syntheticTake()
	rt := Calibrate(m, rec)
	out := rt.Normalize(rec.Frames[0].Poses)
	hp, ok := out[RoleHead]
	if !ok {
		t.Fatal("head missing after Normalize")
	}
	// head (2,1.8,1) − origin (2,0,1) = (0,1.8,0), × scale → (0,1.5,0)
	if math.Abs(float64(hp.Pos[0])) > 1e-3 || math.Abs(float64(hp.Pos[1]-1.5)) > 1e-3 || math.Abs(float64(hp.Pos[2])) > 1e-3 {
		t.Errorf("normalized head=%v want (0,1.5,0)", hp.Pos)
	}
	if _, ok := out[RoleRightHand]; !ok {
		t.Error("right hand missing after Normalize")
	}
}

func TestConvRotationNotReflection(t *testing.T) {
	// convP must be a proper rotation: rotating then converting == converting then rotating
	// with the converted quat, and chirality is preserved (det=+1: right stays right).
	q := quat{0, float32(math.Sin(0.4)), 0, float32(math.Cos(0.4))} // some yaw
	v := [3]float32{0.3, 1.1, -0.7}
	lhs := convP(qRotate(q, v))
	rhs := qRotate(convQ([4]float32(q)), convP(v))
	if dist(lhs, rhs) > 1e-5 {
		t.Errorf("conv not rotation-consistent: %v vs %v", lhs, rhs)
	}
	// 180° yaw: x and z negate, y unchanged.
	if got := convP([3]float32{1, 2, 3}); got != [3]float32{-1, 2, -3} {
		t.Errorf("convP = %v want (-1,2,-3)", got)
	}
}

func TestTwoHandsOnlyClassifiesHands(t *testing.T) {
	// The common HMD + 2 controllers case: both non-head keys become hands, never hips/feet.
	head := [3]float32{0, 1.7, 0}
	var frames []vrmotion.Frame
	for i := range 10 {
		frames = append(frames, vrmotion.Frame{T: float64(i) / 30, Poses: map[int]vrmotion.Pose{
			0: {Pos: head, Rot: [4]float32{0, 0, 0, 1}},
			1: {Pos: [3]float32{0.35, 1.1, -0.2}, Rot: [4]float32{0, 0, 0, 1}},  // +X = user right
			2: {Pos: [3]float32{-0.35, 1.1, -0.2}, Rot: [4]float32{0, 0, 0, 1}}, // -X = user left
		}})
	}
	m := armModel(t, true)
	rt := Calibrate(m, &vrmotion.Recording{Name: "hands", Hz: 30, Frames: frames, Duration: 1})
	if rt.Roles[1] != RoleRightHand || rt.Roles[2] != RoleLeftHand {
		t.Errorf("roles=%v want key1=right hand, key2=left hand", rt.Roles)
	}
}
