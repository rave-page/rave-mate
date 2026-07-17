package avataratlas

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestSampleDeterminism: same doc + seed + count = identical points; different seed differs.
func TestSampleDeterminism(t *testing.T) {
	doc := parseRig(t, rigOpts{})
	a, err := Sample(doc, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Sample(doc, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Points, b.Points) {
		t.Fatal("same seed produced different points")
	}
	c, err := Sample(doc, 64, 2)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(a.Points, c.Points) {
		t.Fatal("different seed produced identical points")
	}
}

// TestSampleSlots: equal-area quads split points across both bones; bone-local positions land
// inside each bone's expected bind-space range (spine points shifted by its IBM).
func TestSampleSlots(t *testing.T) {
	doc := parseRig(t, rigOpts{})
	res, err := Sample(doc, 400, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped != 0 || len(res.Points) != 400 {
		t.Fatalf("dropped %d, emitted %d, want 0/400", res.Dropped, len(res.Points))
	}
	if res.PerSlot[0] == 0 || res.PerSlot[1] == 0 {
		t.Fatalf("histogram %v: both slots must receive points", res.PerSlot[:2])
	}
	if res.PerSlot[0]+res.PerSlot[1] != 400 {
		t.Fatalf("histogram sums to %d, want 400", res.PerSlot[0]+res.PerSlot[1])
	}
	// Equal areas: no worse than a 3:1 split at n=400 (loose - deterministic given the seed).
	if res.PerSlot[0] < 100 || res.PerSlot[1] < 100 {
		t.Fatalf("area weighting badly skewed: %v", res.PerSlot[:2])
	}
	for _, p := range res.Points {
		if p.Local[1] < -1e-9 || p.Local[1] > 0.3+1e-9 {
			t.Fatalf("slot %d local y %v outside bind range [0,0.3]", p.Slot, p.Local[1])
		}
	}
}

// TestAncestorWalkRemap: quad B weighted to an unmapped "finger" joint (child of spine) must
// sample EXACTLY like quad B weighted to spine directly - same slot, same IBM (spine's, not
// finger's), same bytes.
func TestAncestorWalkRemap(t *testing.T) {
	direct, err := Sample(parseRig(t, rigOpts{}), 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	walked, err := Sample(parseRig(t, rigOpts{fingerJoint: true}), 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	if walked.Dropped != 0 {
		t.Fatalf("walked rig dropped %d", walked.Dropped)
	}
	if !reflect.DeepEqual(direct.Points, walked.Points) {
		t.Fatal("ancestor-walk remap differs from direct spine weighting (wrong slot or wrong IBM)")
	}
}

// TestDroppedCounting: quad B weighted to an orphan root joint with no mapped ancestor ->
// those draws drop and are counted; emitted + dropped == requested; survivors are all hips.
func TestDroppedCounting(t *testing.T) {
	res, err := Sample(parseRig(t, rigOpts{orphanJoint: true}), 200, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped == 0 {
		t.Fatal("orphan joint produced no drops")
	}
	if len(res.Points)+res.Dropped != 200 {
		t.Fatalf("emitted %d + dropped %d != requested 200", len(res.Points), res.Dropped)
	}
	for _, p := range res.Points {
		if p.Slot != 0 {
			t.Fatalf("survivor on slot %d, want only hips", p.Slot)
		}
	}
	if res.PerSlot[0] != len(res.Points) {
		t.Fatalf("histogram %d != emitted %d", res.PerSlot[0], len(res.Points))
	}
}

// TestSkippedPrimitives: a primitive without JOINTS_0/WEIGHTS_0 is skipped and counted, and
// contributes no points.
func TestSkippedPrimitives(t *testing.T) {
	res, err := Sample(parseRig(t, rigOpts{unskinnedB: true}), 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.SkippedPrims != 1 {
		t.Fatalf("SkippedPrims %d, want 1", res.SkippedPrims)
	}
	if len(res.Points) != 64 {
		t.Fatalf("emitted %d, want 64", len(res.Points))
	}
}

// TestNoHumanoidRejected: a bare glTF without a VRM humanoid map rejects with a clear error.
func TestNoHumanoidRejected(t *testing.T) {
	doc := parseRig(t, rigOpts{noVRM: true})
	if _, err := Sample(doc, 16, 1); err == nil {
		t.Fatal("no-humanoid doc sampled, want reject")
	}
}

// TestBaseColorFactorLinear: factor-only material (no texture) applies in linear and
// re-encodes with the exact OETF: byte = LinearToSRGBByte(factor).
func TestBaseColorFactorLinear(t *testing.T) {
	glb := buildRig(t, rigOpts{}, "")
	jsonChunk, bin := splitGLB(t, glb)
	var raw map[string]any
	if err := json.Unmarshal(jsonChunk, &raw); err != nil {
		t.Fatal(err)
	}
	raw["materials"].([]any)[0].(map[string]any)["pbrMetallicRoughness"] = map[string]any{
		"baseColorFactor": []float64{0.25, 0.5, 1.0, 1.0},
	}
	mut, _ := json.Marshal(raw)
	doc, err := ParseGLTF(mut, bin, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Sample(doc, 16, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := [3]uint8{LinearToSRGBByte(0.25), LinearToSRGBByte(0.5), 255}
	for _, p := range res.Points {
		if p.RGB != want {
			t.Fatalf("factor-only colour %v, want %v", p.RGB, want)
		}
	}
}
