package vrmdyn

import (
	"encoding/json"
	"math"
	"testing"

	"rave.page/mate/internal/vrm"
)

// Node indices in testModel.
const (
	nArmature = iota
	nHips
	nHead
	nHairRoot
	nHair1
	nHair2
	nFinger1
	nFinger2
	nTailRoot
	nTail1
)

// testModel: humanoid hips+head, a 3-joint horizontal hair chain off the head, a 2-joint
// tail off the hips, and a keyword-less finger chain (must stay rigid).
func testModel(t *testing.T) *vrm.Model {
	t.Helper()
	doc := map[string]any{
		"asset": map[string]any{"version": "2.0"},
		"nodes": []any{
			map[string]any{"name": "Armature", "children": []int{1}},
			map[string]any{"name": "Hips", "translation": []float64{0, 1, 0}, "children": []int{2, 6, 8}},
			map[string]any{"name": "Head", "translation": []float64{0, 0.5, 0}, "children": []int{3}},
			map[string]any{"name": "HairRoot", "translation": []float64{0, 0.1, 0}, "children": []int{4}},
			map[string]any{"name": "Hair1", "translation": []float64{0.3, 0, 0}, "children": []int{5}},
			map[string]any{"name": "Hair2", "translation": []float64{0.3, 0, 0}},
			map[string]any{"name": "IndexFinger1", "translation": []float64{0.2, 0, 0}, "children": []int{7}},
			map[string]any{"name": "IndexFinger2", "translation": []float64{0.1, 0, 0}},
			map[string]any{"name": "TailRoot", "translation": []float64{0, 0, -0.2}, "children": []int{9}},
			map[string]any{"name": "Tail1", "translation": []float64{0, 0, -0.3}},
		},
		"extensions": map[string]any{
			"VRM": map[string]any{"humanoid": map[string]any{"humanBones": []any{
				map[string]any{"bone": "hips", "node": 1},
				map[string]any{"bone": "head", "node": 2},
			}}},
		},
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

// worldPos poses + returns a node's world position after one WorldFrom pass.
func worldPos(m *vrm.Model, local []vrm.Mat4, node int) [3]float32 {
	return mpos(m.WorldFrom(local)[node])
}

// shiftHips returns rest locals with the hips translated by d.
func shiftHips(m *vrm.Model, d [3]float32) []vrm.Mat4 {
	local := m.RestLocal()
	local[nHips][12] += d[0]
	local[nHips][13] += d[1]
	local[nHips][14] += d[2]
	return local
}

func TestDetectChains(t *testing.T) {
	m := testModel(t)
	st := NewState(m)
	infos := st.Chains()
	byRoot := map[string]int{}
	for _, ci := range infos {
		byRoot[ci.Root] = ci.Joints
	}
	if len(infos) != 2 {
		t.Fatalf("chains = %v, want HairRoot + TailRoot", infos)
	}
	if byRoot["HairRoot"] != 3 {
		t.Errorf("HairRoot joints = %d, want 3", byRoot["HairRoot"])
	}
	if byRoot["TailRoot"] != 2 {
		t.Errorf("TailRoot joints = %d, want 2", byRoot["TailRoot"])
	}
	for _, p := range st.parts { // fingers must not dangle
		if p.node == nFinger1 || p.node == nFinger2 {
			t.Fatalf("finger node %d simulated", p.node)
		}
	}
}

// fleshModel: humanoid hips, a thigh-jiggle helper bone under it, plus a parentless
// keyword-named mesh-container node ("Hair" owning a mesh).
func fleshModel(t *testing.T) *vrm.Model {
	t.Helper()
	doc := map[string]any{
		"asset": map[string]any{"version": "2.0"},
		"nodes": []any{
			map[string]any{"name": "Hips", "translation": []float64{0, 1, 0}, "children": []int{1}},
			map[string]any{"name": "Zthigh_jiggle_L", "translation": []float64{0.1, -0.2, 0}},
			map[string]any{"name": "Hair"}, // parentless mesh container
		},
		"extensions": map[string]any{
			"VRM": map[string]any{"humanoid": map[string]any{"humanBones": []any{
				map[string]any{"bone": "hips", "node": 0},
			}}},
		},
	}
	js, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	m, err := vrm.Parse(js)
	if err != nil {
		t.Fatal(err)
	}
	m.Meshes = append(m.Meshes, vrm.Mesh{NodeIdx: 2})
	return m
}

func TestFleshBonesGetStiffParamsAndContainersSkipped(t *testing.T) {
	m := fleshModel(t)
	st := NewState(m)
	if len(st.chains) != 1 {
		t.Fatalf("chains = %+v, want only the thigh jiggle (mesh container skipped)", st.Chains())
	}
	ch := st.chains[0]
	if ch.name != "Zthigh_jiggle_L" {
		t.Fatalf("chain root = %q", ch.name)
	}
	if ch.prm != fleshParams {
		t.Errorf("thigh jiggle params = %+v, want fleshParams (stiff, gravity-free)", ch.prm)
	}
	if ch.prm.gravity != 0 {
		t.Errorf("flesh gravity = %f, want 0", ch.prm.gravity)
	}
}

func TestIsFleshName(t *testing.T) {
	for name, want := range map[string]bool{
		"Zthigh_jiggle_L": true, "zbreast_dynamic": true, "Zass_jiggle_R": true,
		"zbelly_jiggle_root": true, "HairRoot": false, "Chain Base": false, "TailRoot": false,
	} {
		if got := isFleshName(name); got != want {
			t.Errorf("isFleshName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestMatchDynamicBoundaries(t *testing.T) {
	for name, want := range map[string]bool{
		"HairRoot": true, "L_ear": true, "LeftEar": true, "tail_01": true,
		"Forearm": false, "forearm": false, "IndexFinger1": false, "spine": false,
	} {
		if _, got := matchDynamic(name); got != want {
			t.Errorf("matchDynamic(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestPendulumSettlesUnderGravity(t *testing.T) {
	m := testModel(t)
	st := NewStateWithConfig(m, &Sidecar{Version: 1, Chains: []SidecarChain{
		{Root: "HairRoot", Pull: 0.02, Spring: 0.9, Gravity: 1.0},
	}})
	if len(st.Chains()) != 1 {
		t.Fatalf("chains = %v", st.Chains())
	}
	for range 240 { // 4s static parent
		local := m.RestLocal()
		st.Step(m, local, 1.0/60)
	}
	local := m.RestLocal()
	st.Step(m, local, 0) // apply settled state
	tip := worldPos(m, local, nHair2)
	if tip[1] > 1.6-0.05 {
		t.Errorf("hair tip y = %v, want sag below 1.55 (rest 1.6)", tip[1])
	}
	// settled: negligible velocity
	for pi := st.chains[0].start; pi < st.chains[0].end; pi++ {
		p := st.parts[pi]
		if v := dist(p.pos, p.prev); v > 1e-3 {
			t.Errorf("particle %d still moving: |vel| = %v", pi, v)
		}
	}
}

func TestFollowsMovingParentWithLag(t *testing.T) {
	m := testModel(t)
	st := NewState(m)
	st.Step(m, m.RestLocal(), 1.0/60) // seed at rest

	local := shiftHips(m, [3]float32{0.3, 0, 0})
	st.Step(m, local, 1.0/60)
	tip := worldPos(m, local, nHair2)
	if tip[0] > 0.9-0.01 {
		t.Errorf("tip x = %v after 1 frame, want lag behind target 0.9", tip[0])
	}
	for range 300 { // converge onto the moved pose
		local = shiftHips(m, [3]float32{0.3, 0, 0})
		st.Step(m, local, 1.0/60)
	}
	tip = worldPos(m, local, nHair2)
	if math.Abs(float64(tip[0]-0.9)) > 0.02 {
		t.Errorf("tip x = %v after settling, want ≈0.9", tip[0])
	}
}

func TestLengthConstraintHolds(t *testing.T) {
	m := testModel(t)
	st := NewState(m)
	for i := range 60 { // violent alternating hips motion (below teleport threshold)
		dx := float32(0.4)
		if i%2 == 1 {
			dx = -0.4
		}
		st.Step(m, shiftHips(m, [3]float32{dx, 0, 0}), 1.0/60)
	}
	for pi, p := range st.parts {
		if p.kinematic || p.restLen <= eps {
			continue
		}
		if d := dist(p.pos, st.parts[p.parent].pos); math.Abs(float64(d-p.restLen)) > 1e-3 {
			t.Errorf("particle %d (node %d): |pos-parent| = %v, restLen = %v", pi, p.node, d, p.restLen)
		}
	}
}

func TestDtZeroDoesNotIntegrate(t *testing.T) {
	m := testModel(t)
	st := NewState(m)
	st.Step(m, m.RestLocal(), 1.0/60)
	before := make([][3]float32, len(st.parts))
	for i, p := range st.parts {
		before[i] = p.pos
	}
	st.Step(m, shiftHips(m, [3]float32{0.2, 0, 0}), 0) // paused re-render at a new pose
	for i, p := range st.parts {
		if p.pos != before[i] {
			t.Fatalf("particle %d moved on dt=0: %v -> %v", i, before[i], p.pos)
		}
	}
}

func TestTeleportResets(t *testing.T) {
	m := testModel(t)
	st := NewState(m)
	st.Step(m, m.RestLocal(), 1.0/60)

	local := shiftHips(m, [3]float32{1, 0, 0}) // >0.5m anchor jump
	st.Step(m, local, 1.0/60)
	worldAnim := m.WorldFrom(shiftHips(m, [3]float32{1, 0, 0}))
	for pi, p := range st.parts {
		if p.tip {
			continue
		}
		if d := dist(p.pos, mpos(worldAnim[p.node])); d > 0.005 {
			t.Errorf("particle %d lags %vm after teleport, want re-seat", pi, d)
		}
	}
}

func TestResetReseats(t *testing.T) {
	m := testModel(t)
	st := NewState(m)
	for range 30 {
		st.Step(m, shiftHips(m, [3]float32{0.3, 0, 0}), 1.0/60)
	}
	st.Reset()
	st.Step(m, m.RestLocal(), 1.0/60)
	restW := m.RestWorld()
	for pi, p := range st.parts {
		if p.tip {
			continue
		}
		if d := dist(p.pos, mpos(restW[p.node])); d > 0.005 {
			t.Errorf("particle %d not re-seated after Reset: off by %v", pi, d)
		}
	}
}
