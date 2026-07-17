package avataratlas

// bonemap_test.go - v1.3.1 name-heuristic tests: Mixamo / VRoid / Mecanim / Cats token
// tables, the bare-"leg" convention resolution, -bonemap overrides, core-missing rejects
// listing names, and the plain-glTF fallback (Mixamo-named GLB maps instead of rejecting).

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestGuessSlotTable: per-name heuristics across the naming families.
func TestGuessSlotTable(t *testing.T) {
	cases := []struct {
		name string
		slot int // -1 = unmapped
	}{
		// Mixamo
		{"mixamorig:Hips", 0}, {"mixamorig:Spine", 1}, {"mixamorig:Spine1", 2},
		{"mixamorig:Spine2", 3}, {"mixamorig:Neck", 4}, {"mixamorig:Head", 5},
		{"mixamorig:LeftShoulder", 6}, {"mixamorig:LeftArm", 7}, {"mixamorig:LeftForeArm", 8},
		{"mixamorig:LeftHand", 9}, {"mixamorig:RightArm", 11}, {"mixamorig:RightForeArm", 12},
		{"mixamorig:LeftUpLeg", 14}, {"mixamorig:LeftLeg", 15}, // bare leg defaults Mixamo shin
		{"mixamorig:LeftFoot", 16}, {"mixamorig:LeftToeBase", 17}, {"mixamorig:RightToeBase", 21},
		// VRoid J_Bip
		{"J_Bip_C_Hips", 0}, {"J_Bip_C_Spine", 1}, {"J_Bip_C_Chest", 2},
		{"J_Bip_C_UpperChest", 3}, {"J_Bip_C_Neck", 4}, {"J_Bip_C_Head", 5},
		{"J_Bip_L_Shoulder", 6}, {"J_Bip_L_UpperArm", 7}, {"J_Bip_R_LowerArm", 12},
		{"J_Bip_L_Hand", 9}, {"J_Bip_L_UpperLeg", 14}, {"J_Bip_R_LowerLeg", 19},
		{"J_Bip_R_Foot", 20}, {"J_Bip_L_ToeBase", 17},
		// Mecanim
		{"Hips", 0}, {"Spine", 1}, {"Chest", 2}, {"UpperChest", 3}, {"Neck", 4}, {"Head", 5},
		{"LeftShoulder", 6}, {"LeftUpperArm", 7}, {"LeftLowerArm", 8}, {"LeftHand", 9},
		{"RightUpperLeg", 18}, {"RightLowerLeg", 19}, {"LeftFoot", 16}, {"LeftToes", 17},
		// Cats/Blender ("Upper Chest", elbow/wrist/knee/ankle vocabulary)
		{"Upper Chest", 3}, {"Left shoulder", 6}, {"Left arm", 7}, {"Left elbow", 8},
		{"Left wrist", 9}, {"Left knee", 15}, {"Left ankle", 16}, {"Right toe", 21},
		// affix sides + alternate vocabulary
		{"Thigh_L", 14}, {"Calf_R", 19}, {"L_Hand", 9}, {"Clavicle_R", 10}, {"Pelvis", 0},
		// unmapped: secondary/decoration/end bones
		{"J_Sec_Hair1_01", -1}, {"J_Sec_L_Bust2", -1}, {"HeadTop_End", -1},
		{"LeftHandIndex1", -1}, {"Eye_L", -1}, {"Breast_R", -1}, {"Armature", -1},
		{"Body", -1}, {"IndexFinger1_L", -1}, {"Thumb0_R", -1}, {"ASS_L", -1},
	}
	for _, tc := range cases {
		slot, ok := GuessSlot(tc.name)
		if tc.slot < 0 {
			if ok {
				t.Errorf("%q mapped to slot %d, want unmapped", tc.name, slot)
			}
			continue
		}
		if !ok || slot != tc.slot {
			t.Errorf("%q -> (%d,%v), want slot %d", tc.name, slot, ok, tc.slot)
		}
	}
}

// mapDoc builds a minimal Document whose skin joints carry the given names.
func mapDoc(names ...string) *Document {
	doc := &Document{NodeSlot: map[int]int{}}
	joints := make([]int, len(names))
	for i, n := range names {
		doc.Nodes = append(doc.Nodes, Node{Name: n, Parent: i - 1, Mesh: -1, Skin: -1})
		joints[i] = i
	}
	doc.Skins = []Skin{{Joints: joints}}
	return doc
}

// TestMapHumanoidLegConvention: bare "leg" resolves per rig - thigh when a knee-family
// joint claims lowerLeg (Cats), shin otherwise (Mixamo).
func TestMapHumanoidLegConvention(t *testing.T) {
	cats := mapDoc("Hips", "Spine", "Head", "Left leg", "Left knee", "Right leg", "Right knee")
	if _, err := MapHumanoid(cats, nil); err != nil {
		t.Fatal(err)
	}
	want := map[int]int{0: 0, 1: 1, 2: 5, 3: 14, 4: 15, 5: 18, 6: 19}
	if !reflect.DeepEqual(cats.NodeSlot, want) {
		t.Fatalf("cats NodeSlot %v, want %v (leg=thigh)", cats.NodeSlot, want)
	}

	mixamo := mapDoc("mixamorig:Hips", "mixamorig:Spine", "mixamorig:Head",
		"mixamorig:LeftUpLeg", "mixamorig:LeftLeg", "mixamorig:RightUpLeg", "mixamorig:RightLeg")
	if _, err := MapHumanoid(mixamo, nil); err != nil {
		t.Fatal(err)
	}
	want = map[int]int{0: 0, 1: 1, 2: 5, 3: 14, 4: 15, 5: 18, 6: 19}
	if !reflect.DeepEqual(mixamo.NodeSlot, want) {
		t.Fatalf("mixamo NodeSlot %v, want %v (leg=shin)", mixamo.NodeSlot, want)
	}
}

// TestMapHumanoidDeterministic: repeated mapping of the same doc yields identical reports.
func TestMapHumanoidDeterministic(t *testing.T) {
	var want *BoneMapping
	for run := 0; run < 10; run++ {
		doc := mapDoc("Hips", "Spine", "Head", "Left arm", "Right arm", "J_Sec_Hair1", "Eye_L")
		rep, err := MapHumanoid(doc, nil)
		if err != nil {
			t.Fatal(err)
		}
		if run == 0 {
			want = rep
		} else if !reflect.DeepEqual(want, rep) {
			t.Fatalf("run %d: mapping drifted: %+v vs %+v", run, rep, want)
		}
	}
	if got := want.Unmapped; !reflect.DeepEqual(got, []string{"Eye_L", "J_Sec_Hair1"}) {
		t.Fatalf("unmapped %v, want sorted [Eye_L J_Sec_Hair1]", got)
	}
}

// TestMapHumanoidOverrides: -bonemap entries win over heuristics, own their slot
// exclusively, force-unmap with "", and reject unknown slot names / unmatched nodes.
func TestMapHumanoidOverrides(t *testing.T) {
	doc := mapDoc("Hips", "Spine", "Head", "MysteryBone")
	rep, err := MapHumanoid(doc, map[string]string{"MysteryBone": "head"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(rep.Source, "+override") {
		t.Fatalf("source %q, want +override suffix", rep.Source)
	}
	if doc.NodeSlot[3] != 5 {
		t.Fatalf("MysteryBone slot %v, want 5", doc.NodeSlot[3])
	}
	if _, still := doc.NodeSlot[2]; still {
		t.Fatal("heuristic Head kept slot 5 despite override claiming it")
	}

	// Force-unmap: "" removes the node; core check then fails and lists names.
	doc = mapDoc("Hips", "Spine", "Head")
	_, err = MapHumanoid(doc, map[string]string{"Head": ""})
	if err == nil || !strings.Contains(err.Error(), "head") || !strings.Contains(err.Error(), "Hips->hips") {
		t.Fatalf("error %v, want core-missing listing matched pairs", err)
	}

	if _, err = MapHumanoid(mapDoc("Hips", "Spine", "Head"), map[string]string{"Hips": "noSuchSlot"}); err == nil || !strings.Contains(err.Error(), "unknown slot") {
		t.Fatalf("error %v, want unknown-slot reject", err)
	}
	if _, err = MapHumanoid(mapDoc("Hips", "Spine", "Head"), map[string]string{"Ghost": "head"}); err == nil || !strings.Contains(err.Error(), "matches no node") {
		t.Fatalf("error %v, want unmatched-override reject", err)
	}
}

// TestMapHumanoidCoreMissingListsNames: the reject names missing cores, matched pairs AND
// unmapped joints - the actionable output the operator needs.
func TestMapHumanoidCoreMissingListsNames(t *testing.T) {
	doc := mapDoc("Left arm", "Foo", "Hips")
	_, err := MapHumanoid(doc, nil)
	if err == nil {
		t.Fatal("core-missing doc accepted")
	}
	for _, want := range []string{"spine", "head", "Left arm->leftUpperArm", "Hips->hips", "Foo", "-bonemap"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

// TestGLTFFallbackHeuristics: a plain (non-VRM) GLB with Mixamo-named joints maps via
// heuristics and samples, instead of the pre-v1.3.1 no-humanoid reject.
func TestGLTFFallbackHeuristics(t *testing.T) {
	glbJSON, bin := splitGLB(t, buildRig(t, rigOpts{noVRM: true, fingerJoint: true}, ""))
	var raw map[string]any
	if err := json.Unmarshal(glbJSON, &raw); err != nil {
		t.Fatal(err)
	}
	nodes := raw["nodes"].([]any)
	nodes[0].(map[string]any)["name"] = "mixamorig:Hips"
	nodes[1].(map[string]any)["name"] = "mixamorig:Spine"
	nodes[2].(map[string]any)["name"] = "mixamorig:Head"
	mut, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := ParseGLTF(mut, bin, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.NodeSlot) != 0 {
		t.Fatalf("non-VRM doc pre-mapped: %v", doc.NodeSlot)
	}
	rep, err := MapHumanoid(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Source != "heuristic" {
		t.Fatalf("source %q, want heuristic", rep.Source)
	}
	if want := map[int]int{0: 0, 1: 1, 2: 5}; !reflect.DeepEqual(doc.NodeSlot, want) {
		t.Fatalf("NodeSlot %v, want %v", doc.NodeSlot, want)
	}
	res, err := Sample(doc, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped != 0 || len(res.Points) != 64 {
		t.Fatalf("dropped %d emitted %d, want 0/64", res.Dropped, len(res.Points))
	}
	for _, p := range res.Points {
		if p.Slot != 0 && p.Slot != 5 {
			t.Fatalf("point on slot %d, want hips/head only", p.Slot)
		}
	}
}
