package avataratlas

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestGoldenStability: re-running the golden pipeline reproduces the committed fixture bytes
// EXACTLY (testdata/ mirrors Packages/page.rave.puppets/Tests~/golden/ in the world repo).
// A diff here means the frozen pipeline drifted - that is a contract version bump, never a
// fixture refresh.
func TestGoldenStability(t *testing.T) {
	atlas, res, err := GoldenAtlas()
	if err != nil {
		t.Fatal(err)
	}
	var pngBuf bytes.Buffer
	if err := atlas.EncodePNG(&pngBuf); err != nil {
		t.Fatal(err)
	}
	wantPNG, err := os.ReadFile(filepath.Join("testdata", GoldenPNG))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pngBuf.Bytes(), wantPNG) {
		t.Errorf("golden PNG drifted (%d vs %d bytes)", pngBuf.Len(), len(wantPNG))
	}

	sidecar, err := GoldenSidecar(atlas, res)
	if err != nil {
		t.Fatal(err)
	}
	sidecar = append(sidecar, '\n')
	wantJSON, err := os.ReadFile(filepath.Join("testdata", GoldenJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sidecar, wantJSON) {
		t.Errorf("golden JSON sidecar drifted")
	}
}

// TestGoldenStructure pins the frozen structural facts and proves the committed PNG decodes
// field-exact back to the freshly generated atlas.
func TestGoldenStructure(t *testing.T) {
	atlas, res, err := GoldenAtlas()
	if err != nil {
		t.Fatal(err)
	}
	if len(atlas.Points) != GoldenPoints || res.Dropped != 0 {
		t.Fatalf("golden emitted %d points, dropped %d (want %d/0)", len(atlas.Points), res.Dropped, GoldenPoints)
	}
	if atlas.SlotIndex != GoldenSlot || atlas.BoneCount != 2 || atlas.Version != Version {
		t.Fatalf("golden header %+v", atlas)
	}
	if !atlas.Boxes[0].Used() || !atlas.Boxes[1].Used() {
		t.Fatal("golden must use slots 0 (hips) and 1 (spine)")
	}
	if h := AtlasHeight(GoldenPoints); h != 3 {
		t.Fatalf("golden height %d, want 3", h)
	}
	for _, p := range atlas.Points {
		if p.Weight != WeightV1 {
			t.Fatalf("golden point weight %d, want 255 (v1 rigid)", p.Weight)
		}
	}

	f, err := os.Open(filepath.Join("testdata", GoldenPNG))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	decoded, err := DecodePNG(f)
	if err != nil {
		t.Fatalf("committed golden PNG rejected: %v", err)
	}
	if !reflect.DeepEqual(atlas, decoded) {
		t.Fatal("committed golden PNG decodes differently from regenerated atlas")
	}
}
