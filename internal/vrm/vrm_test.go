package vrm

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"testing"
)

// buildGLB assembles a minimal valid .glb from a JSON doc + binary buffer (chunks padded to 4).
func buildGLB(t *testing.T, doc map[string]any, bin []byte) []byte {
	t.Helper()
	js, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	for len(js)%4 != 0 {
		js = append(js, ' ')
	}
	for len(bin)%4 != 0 {
		bin = append(bin, 0)
	}
	total := 12 + 8 + len(js) + 8 + len(bin)
	out := make([]byte, 0, total)
	hdr := make([]byte, 12)
	binary.LittleEndian.PutUint32(hdr[0:], 0x46546C67)
	binary.LittleEndian.PutUint32(hdr[4:], 2)
	binary.LittleEndian.PutUint32(hdr[8:], uint32(total))
	out = append(out, hdr...)
	ch := make([]byte, 8)
	binary.LittleEndian.PutUint32(ch[0:], uint32(len(js)))
	binary.LittleEndian.PutUint32(ch[4:], 0x4E4F534A) // JSON
	out = append(out, ch...)
	out = append(out, js...)
	binary.LittleEndian.PutUint32(ch[0:], uint32(len(bin)))
	binary.LittleEndian.PutUint32(ch[4:], 0x004E4942) // BIN
	out = append(out, ch...)
	out = append(out, bin...)
	return out
}

func f32le(vals ...float32) []byte {
	b := make([]byte, 4*len(vals))
	for i, v := range vals {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(v))
	}
	return b
}

func TestParseTriangleGLB(t *testing.T) {
	// 3 positions + 3 ushort indices.
	pos := f32le(0, 0, 0, 1, 0, 0, 0, 1, 0)
	idx := make([]byte, 6)
	binary.LittleEndian.PutUint16(idx[0:], 0)
	binary.LittleEndian.PutUint16(idx[2:], 1)
	binary.LittleEndian.PutUint16(idx[4:], 2)
	bin := append(append([]byte{}, pos...), idx...) // idx at offset 36

	doc := map[string]any{
		"asset":   map[string]any{"version": "2.0"},
		"buffers": []any{map[string]any{"byteLength": len(bin)}},
		"bufferViews": []any{
			map[string]any{"buffer": 0, "byteOffset": 0, "byteLength": 36},
			map[string]any{"buffer": 0, "byteOffset": 36, "byteLength": 6},
		},
		"accessors": []any{
			map[string]any{"bufferView": 0, "componentType": cFloat, "count": 3, "type": "VEC3"},
			map[string]any{"bufferView": 1, "componentType": cUShort, "count": 3, "type": "SCALAR"},
		},
		"meshes": []any{map[string]any{"primitives": []any{
			map[string]any{"attributes": map[string]any{"POSITION": 0}, "indices": 1, "mode": 4},
		}}},
		"nodes":  []any{map[string]any{"mesh": 0, "name": "tri"}},
		"scenes": []any{map[string]any{"nodes": []any{0}}},
	}

	m, err := Parse(buildGLB(t, doc, bin))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Meshes) != 1 {
		t.Fatalf("meshes = %d, want 1", len(m.Meshes))
	}
	if len(m.Meshes[0].Verts) != 3 || len(m.Meshes[0].Indices) != 3 {
		t.Fatalf("verts %d idx %d", len(m.Meshes[0].Verts), len(m.Meshes[0].Indices))
	}
	if m.Meshes[0].Verts[1].Pos != [3]float32{1, 0, 0} {
		t.Errorf("vert1 pos = %v", m.Meshes[0].Verts[1].Pos)
	}
	if m.Meshes[0].Skinned {
		t.Error("unskinned mesh marked skinned")
	}
	// Posed positions at rest = identity world (node has no transform).
	world := m.RestWorld()
	skin := m.SkinMatrices(world)
	got := m.PosedPositions(0, world, skin)
	if got[2] != [3]float32{0, 1, 0} {
		t.Errorf("posed vert2 = %v", got[2])
	}
}

func TestParseHumanoidVRM0(t *testing.T) {
	doc := map[string]any{
		"asset": map[string]any{"version": "2.0"},
		"nodes": []any{map[string]any{"name": "hips"}, map[string]any{"name": "head"}},
		"extensions": map[string]any{
			"VRM": map[string]any{"humanoid": map[string]any{"humanBones": []any{
				map[string]any{"bone": "hips", "node": 0},
				map[string]any{"bone": "head", "node": 1},
			}}},
		},
	}
	m, err := Parse(buildGLB(t, doc, nil))
	if err != nil {
		t.Fatal(err)
	}
	if m.HumanoidNode("head") != 1 || m.HumanoidNode("hips") != 0 {
		t.Errorf("humanoid map = %v", m.Humanoid)
	}
	if m.HumanoidNode("leftHand") != -1 {
		t.Error("absent bone should return -1")
	}
}

func TestMatTRSIdentity(t *testing.T) {
	m := TRS([3]float32{1, 2, 3}, [4]float32{0, 0, 0, 1}, [3]float32{1, 1, 1})
	if p := m.TransformPoint([3]float32{0, 0, 0}); p != [3]float32{1, 2, 3} {
		t.Errorf("translation TRS = %v", p)
	}
	id := Identity()
	if got := id.Mul(m); got != m {
		t.Errorf("I*m != m")
	}
}
