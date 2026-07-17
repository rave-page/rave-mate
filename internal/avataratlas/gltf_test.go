package avataratlas

// gltf_test.go - synthetic-rig builder + parser tests. The builder mirrors the golden rig
// (two stacked quads, hips + spine) with knobs: VRM 0.x vs 1.0 humanoid map, an extra
// unmapped joint (child "finger" or orphan root "prop") that quad B weights to, alternative
// JOINTS_0/WEIGHTS_0 component types, GLB vs .gltf-with-external-files output.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type rigOpts struct {
	vrm1         bool // VRMC_vrm (1.0) instead of VRM (0.x)
	noVRM        bool // no humanoid extension at all
	fingerJoint  bool // add unmapped joint node "finger" (child of spine); quad B -> finger
	orphanJoint  bool // add unmapped ROOT joint node "prop"; quad B -> prop
	ushortJoints bool // JOINTS_0 as ushort
	ubyteWeights bool // WEIGHTS_0 as normalized ubyte
	unskinnedB   bool // strip JOINTS_0/WEIGHTS_0 from a second primitive (skip counting)
}

// buildRig returns (glbBytes) or, when dir != "", writes rig.gltf + rig.bin + tex.png into
// dir and returns nil (load via Load(dir/rig.gltf)).
func buildRig(t *testing.T, o rigOpts, dir string) []byte {
	t.Helper()
	le := binary.LittleEndian
	var bin bytes.Buffer
	putF32 := func(vs ...float64) {
		for _, v := range vs {
			var b [4]byte
			le.PutUint32(b[:], math.Float32bits(float32(v)))
			bin.Write(b[:])
		}
	}
	type bv struct{ off, len int }
	var views []bv
	mark := func(start int) int {
		views = append(views, bv{start, bin.Len() - start})
		return len(views) - 1
	}

	// Quad B's joint: spine (1) by default, or the extra unmapped joint (2).
	quadBJoint := 1
	if o.fingerJoint || o.orphanJoint {
		quadBJoint = 2
	}

	start := bin.Len()
	putF32(-0.25, 0, 0, 0.25, 0, 0, 0.25, 0.3, 0, -0.25, 0.3, 0)
	putF32(-0.25, 0.3, 0, 0.25, 0.3, 0, 0.25, 0.6, 0, -0.25, 0.6, 0)
	bvPos := mark(start)

	start = bin.Len()
	putF32(0, 1, 1, 1, 1, 0.5, 0, 0.5)
	putF32(0, 0.5, 1, 0.5, 1, 0, 0, 0)
	bvUV := mark(start)

	start = bin.Len()
	for v := 0; v < 8; v++ {
		j := 0
		if v >= 4 {
			j = quadBJoint
		}
		if o.ushortJoints {
			var b [8]byte
			le.PutUint16(b[:], uint16(j))
			bin.Write(b[:])
		} else {
			bin.Write([]byte{byte(j), 0, 0, 0})
		}
	}
	bvJoints := mark(start)

	start = bin.Len()
	for v := 0; v < 8; v++ {
		if o.ubyteWeights {
			bin.Write([]byte{255, 0, 0, 0})
		} else {
			putF32(1, 0, 0, 0)
		}
	}
	bvWeights := mark(start)

	start = bin.Len()
	for _, ix := range []uint16{0, 1, 2, 0, 2, 3, 4, 5, 6, 4, 6, 7} {
		var b [2]byte
		le.PutUint16(b[:], ix)
		bin.Write(b[:])
	}
	bvIdx := mark(start)

	joints := []int{0, 1}
	ibms := [][]float64{
		{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1},
		{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, -0.3, 0, 1},
	}
	nodes := []map[string]any{
		{"name": "hips", "children": []int{1}},
		{"name": "spine"},
	}
	if o.fingerJoint {
		nodes[1]["children"] = []int{2}
		nodes = append(nodes, map[string]any{"name": "finger"})
		joints = append(joints, 2)
		// finger IBM differs from spine's on purpose: remap must use SPINE's, not this.
		ibms = append(ibms, []float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, -0.45, 0, 1})
	}
	if o.orphanJoint {
		nodes = append(nodes, map[string]any{"name": "prop"})
		joints = append(joints, 2)
		ibms = append(ibms, []float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1})
	}
	nodes = append(nodes, map[string]any{"name": "body", "mesh": 0, "skin": 0})

	start = bin.Len()
	for _, m := range ibms {
		putF32(m...)
	}
	bvIBM := mark(start)

	texPNG := &bytes.Buffer{}
	if err := png.Encode(texPNG, goldenTexture()); err != nil {
		t.Fatal(err)
	}
	imgEntry := map[string]any{"mimeType": "image/png"}
	if dir == "" {
		start = bin.Len()
		bin.Write(texPNG.Bytes())
		for bin.Len()%4 != 0 {
			bin.WriteByte(0)
		}
		imgEntry["bufferView"] = mark(start)
	} else {
		imgEntry["uri"] = "tex.png"
	}

	jointsCT, weightsCT := compUByte, compFloat
	if o.ushortJoints {
		jointsCT = compUShort
	}
	weightsAcc := map[string]any{"bufferView": bvWeights, "componentType": weightsCT, "count": 8, "type": "VEC4"}
	if o.ubyteWeights {
		weightsCT = compUByte
		weightsAcc["componentType"] = weightsCT
		weightsAcc["normalized"] = true
	}

	prims := []map[string]any{{
		"attributes": map[string]int{"POSITION": bvPos, "TEXCOORD_0": 1, "JOINTS_0": 2, "WEIGHTS_0": 3},
		"indices":    4, "material": 0,
	}}
	if o.unskinnedB {
		prims = append(prims, map[string]any{
			"attributes": map[string]int{"POSITION": 0, "TEXCOORD_0": 1},
			"indices":    4, "material": 0,
		})
	}

	jviews := make([]map[string]any, len(views))
	for i, v := range views {
		jviews[i] = map[string]any{"buffer": 0, "byteOffset": v.off, "byteLength": v.len}
	}
	doc := map[string]any{
		"asset":     map[string]any{"version": "2.0"},
		"nodes":     nodes,
		"skins":     []map[string]any{{"joints": joints, "inverseBindMatrices": 5}},
		"meshes":    []map[string]any{{"primitives": prims}},
		"materials": []map[string]any{{"pbrMetallicRoughness": map[string]any{"baseColorTexture": map[string]any{"index": 0}}}},
		"textures":  []map[string]any{{"sampler": 0, "source": 0}},
		"samplers":  []map[string]any{{"wrapS": WrapClamp, "wrapT": WrapClamp}},
		"images":    []map[string]any{imgEntry},
		"accessors": []map[string]any{
			{"bufferView": bvPos, "componentType": compFloat, "count": 8, "type": "VEC3"},
			{"bufferView": bvUV, "componentType": compFloat, "count": 8, "type": "VEC2"},
			{"bufferView": bvJoints, "componentType": jointsCT, "count": 8, "type": "VEC4"},
			weightsAcc,
			{"bufferView": bvIdx, "componentType": compUShort, "count": 12, "type": "SCALAR"},
			{"bufferView": bvIBM, "componentType": compFloat, "count": len(ibms), "type": "MAT4"},
		},
		"bufferViews": jviews,
	}
	switch {
	case o.noVRM:
	case o.vrm1:
		hb := map[string]any{
			"hips":  map[string]any{"node": 0},
			"spine": map[string]any{"node": 1},
		}
		doc["extensions"] = map[string]any{"VRMC_vrm": map[string]any{
			"specVersion": "1.0",
			"humanoid":    map[string]any{"humanBones": hb},
		}}
	default:
		doc["extensions"] = map[string]any{"VRM": map[string]any{
			"humanoid": map[string]any{"humanBones": []map[string]any{
				{"bone": "hips", "node": 0},
				{"bone": "spine", "node": 1},
			}},
		}}
	}

	if dir != "" {
		doc["buffers"] = []map[string]any{{"uri": "rig.bin", "byteLength": bin.Len()}}
		jsonBytes, err := json.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string][]byte{"rig.gltf": jsonBytes, "rig.bin": bin.Bytes(), "tex.png": texPNG.Bytes()} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}

	doc["buffers"] = []map[string]any{{"byteLength": bin.Len()}}
	jsonBytes, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for len(jsonBytes)%4 != 0 {
		jsonBytes = append(jsonBytes, ' ')
	}
	var glb bytes.Buffer
	w32 := func(v uint32) {
		var b [4]byte
		le.PutUint32(b[:], v)
		glb.Write(b[:])
	}
	glb.Write([]byte("glTF"))
	w32(2)
	w32(uint32(12 + 8 + len(jsonBytes) + 8 + bin.Len()))
	w32(uint32(len(jsonBytes)))
	w32(0x4E4F534A)
	glb.Write(jsonBytes)
	w32(uint32(bin.Len()))
	w32(0x004E4942)
	glb.Write(bin.Bytes())
	return glb.Bytes()
}

func parseRig(t *testing.T, o rigOpts) *Document {
	t.Helper()
	doc, err := ParseGLB(buildRig(t, o, ""), "")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

// TestParseVRM0Mapping: VRM 0.x extensions.VRM.humanoid.humanBones[] -> NodeSlot.
func TestParseVRM0Mapping(t *testing.T) {
	doc := parseRig(t, rigOpts{})
	if doc.VRMVersion != "0" {
		t.Fatalf("VRMVersion %q, want 0", doc.VRMVersion)
	}
	if want := map[int]int{0: 0, 1: 1}; !reflect.DeepEqual(doc.NodeSlot, want) {
		t.Fatalf("NodeSlot %v, want %v", doc.NodeSlot, want)
	}
	if doc.Nodes[1].Parent != 0 || doc.Nodes[0].Parent != -1 {
		t.Fatalf("parents wrong: %+v", doc.Nodes)
	}
}

// TestParseVRM1Mapping: VRM 1.0 extensions.VRMC_vrm.humanoid.humanBones.{name}.node.
func TestParseVRM1Mapping(t *testing.T) {
	doc := parseRig(t, rigOpts{vrm1: true})
	if doc.VRMVersion != "1" {
		t.Fatalf("VRMVersion %q, want 1", doc.VRMVersion)
	}
	if want := map[int]int{0: 0, 1: 1}; !reflect.DeepEqual(doc.NodeSlot, want) {
		t.Fatalf("NodeSlot %v, want %v", doc.NodeSlot, want)
	}
}

// TestParseAltComponentTypes: ushort JOINTS_0 + normalized-ubyte WEIGHTS_0 sample identically
// to the ubyte/float rig (same geometry, same seed).
func TestParseAltComponentTypes(t *testing.T) {
	base, err := Sample(parseRig(t, rigOpts{}), 32, 1)
	if err != nil {
		t.Fatal(err)
	}
	alt, err := Sample(parseRig(t, rigOpts{ushortJoints: true, ubyteWeights: true}), 32, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(base.Points, alt.Points) {
		t.Fatal("ushort/ubyte-norm rig sampled differently from ubyte/float rig")
	}
}

// TestParseExternalGLTF: .gltf + external .bin + external image parse via Load and sample
// identically to the same rig as GLB.
func TestParseExternalGLTF(t *testing.T) {
	dir := t.TempDir()
	buildRig(t, rigOpts{}, dir)
	ext, err := Load(filepath.Join(dir, "rig.gltf"))
	if err != nil {
		t.Fatalf("load external: %v", err)
	}
	glbDoc := parseRig(t, rigOpts{})

	a, err := Sample(ext, 32, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Sample(glbDoc, 32, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Points, b.Points) {
		t.Fatal("external .gltf sampled differently from GLB")
	}
}

// TestParseRejects: clear errors on unsupported topologies.
func TestParseRejects(t *testing.T) {
	// Non-triangle mode.
	glb := buildRig(t, rigOpts{}, "")
	var raw map[string]any
	glbJSON, bin := splitGLB(t, glb)
	if err := json.Unmarshal(glbJSON, &raw); err != nil {
		t.Fatal(err)
	}
	raw["meshes"].([]any)[0].(map[string]any)["primitives"].([]any)[0].(map[string]any)["mode"] = 1
	mut, _ := json.Marshal(raw)
	if _, err := ParseGLTF(mut, bin, ""); err == nil {
		t.Error("line-mode primitive accepted, want reject")
	}

	// Unknown required extension.
	if err := json.Unmarshal(glbJSON, &raw); err != nil {
		t.Fatal(err)
	}
	raw["extensionsRequired"] = []string{"KHR_draco_mesh_compression"}
	mut, _ = json.Marshal(raw)
	if _, err := ParseGLTF(mut, bin, ""); err == nil {
		t.Error("required draco accepted, want reject")
	}

	// Not a GLB.
	if _, err := ParseGLB([]byte("not a glb at all"), ""); err == nil {
		t.Error("garbage accepted as GLB")
	}
}

// splitGLB extracts JSON + BIN chunks for mutation tests.
func splitGLB(t *testing.T, glb []byte) (jsonChunk, binChunk []byte) {
	t.Helper()
	le := binary.LittleEndian
	total := int(le.Uint32(glb[8:]))
	for off := 12; off+8 <= total; {
		clen := int(le.Uint32(glb[off:]))
		ctype := le.Uint32(glb[off+4:])
		off += 8
		switch ctype {
		case 0x4E4F534A:
			jsonChunk = glb[off : off+clen]
		case 0x004E4942:
			binChunk = glb[off : off+clen]
		}
		off += clen
	}
	if jsonChunk == nil {
		t.Fatal("no JSON chunk")
	}
	return
}
