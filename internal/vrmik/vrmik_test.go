package vrmik

import (
	"encoding/json"
	"math"
	"testing"

	"rave.page/mate/internal/vrm"
	"rave.page/mate/internal/vrmotion"
)

// handFrom reconstructs the end-effector world position from an elbow: elbow + l2 toward target.
func handFrom(s, elbow, target [3]float32, l2 float32) [3]float32 {
	return add(elbow, scale(normalize(sub(target, elbow)), l2))
}

func TestSolveElbowReach(t *testing.T) {
	s := [3]float32{0, 0, 0}
	pole := [3]float32{0, -1, 0}
	const l1, l2 float32 = 1, 1

	// Within reach: hand reaches target exactly, elbow at bone length l1.
	within := [3]float32{1, 0, 0}
	e := solveElbow(s, within, add(s, pole), l1, l2)
	if d := dist(e, s); math.Abs(float64(d-l1)) > 1e-4 {
		t.Errorf("within: |elbow-s|=%f want %f", d, l1)
	}
	if d := dist(handFrom(s, e, within, l2), within); d > 1e-4 {
		t.Errorf("within: hand miss = %f", d)
	}
	if e[1] >= 0 { // elbow should bend toward the (downward) pole
		t.Errorf("within: elbow y=%f, expected below shoulder", e[1])
	}

	// At reach: straight arm, hand exactly on target.
	at := [3]float32{2, 0, 0}
	e = solveElbow(s, at, add(s, pole), l1, l2)
	if d := dist(handFrom(s, e, at, l2), at); d > 1e-3 {
		t.Errorf("at-reach: hand miss = %f", d)
	}

	// Beyond reach: arm fully extends toward target (elbow on s→target line, |hand-s| == l1+l2).
	beyond := [3]float32{3, 0, 0}
	e = solveElbow(s, beyond, add(s, pole), l1, l2)
	hand := handFrom(s, e, beyond, l2)
	if d := dist(hand, s); math.Abs(float64(d-(l1+l2))) > 1e-3 {
		t.Errorf("beyond: |hand-s|=%f want %f (fully extended)", d, l1+l2)
	}
	if math.Abs(float64(hand[1])) > 1e-3 || math.Abs(float64(hand[2])) > 1e-3 {
		t.Errorf("beyond: hand off the s→target axis: %v", hand)
	}
}

// armModel builds a minimal VRM with a left-arm chain (hips→leftUpperArm→leftLowerArm→leftHand)
// plus a head, bone lengths 0.3 each. withHumanoid toggles the VRM humanoid extension.
func armModel(t *testing.T, withHumanoid bool) *vrm.Model {
	t.Helper()
	doc := map[string]any{
		"asset": map[string]any{"version": "2.0"},
		"nodes": []any{
			map[string]any{"name": "hips", "translation": []float64{0, 1, 0}, "children": []int{1, 4}},
			map[string]any{"name": "lUA", "translation": []float64{0.2, 0.3, 0}, "children": []int{2}},
			map[string]any{"name": "lLA", "translation": []float64{0.3, 0, 0}, "children": []int{3}},
			map[string]any{"name": "lH", "translation": []float64{0.3, 0, 0}},
			map[string]any{"name": "head", "translation": []float64{0, 0.5, 0}},
		},
	}
	if withHumanoid {
		doc["extensions"] = map[string]any{
			"VRM": map[string]any{"humanoid": map[string]any{"humanBones": []any{
				map[string]any{"bone": "hips", "node": 0},
				map[string]any{"bone": "leftUpperArm", "node": 1},
				map[string]any{"bone": "leftLowerArm", "node": 2},
				map[string]any{"bone": "leftHand", "node": 3},
				map[string]any{"bone": "head", "node": 4},
			}}},
		}
	}
	js, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	m, err := vrm.Parse(js)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPoseArmReaches(t *testing.T) {
	m := armModel(t, true)
	// OpenVR→avatar is a 180° yaw ({-x,y,-z}): feed the take-space point whose avatar-space
	// image (0.4,1.6,0) is within the 0.6 m reach of the shoulder at (0.2,1.3,0).
	sample := [3]float32{-0.4, 1.6, 0}
	target := convP(sample)
	local := Pose(m, map[int]vrmotion.Pose{1: {Pos: sample}})
	if len(local) != len(m.Nodes) {
		t.Fatalf("len(local)=%d want %d", len(local), len(m.Nodes))
	}
	world := m.WorldFrom(local)
	hand := pos(world[3])
	if d := dist(hand, target); d > 1e-3 {
		t.Errorf("hand=%v target=%v miss=%f", hand, target, d)
	}
}

func TestPoseNoHumanoidFallsBackToRest(t *testing.T) {
	m := armModel(t, false)
	rest := m.RestLocal()
	got := Pose(m, map[int]vrmotion.Pose{1: {Pos: [3]float32{5, 5, 5}}})
	if len(got) != len(m.Nodes) {
		t.Fatalf("len=%d want %d", len(got), len(m.Nodes))
	}
	for i := range got {
		if got[i] != rest[i] {
			t.Errorf("node %d = %v, want rest %v", i, got[i], rest[i])
		}
	}
}

func TestPoseNilSampleReturnsRest(t *testing.T) {
	m := armModel(t, true)
	got := Pose(m, nil)
	if len(got) != len(m.Nodes) {
		t.Fatalf("len=%d want %d", len(got), len(m.Nodes))
	}
}
