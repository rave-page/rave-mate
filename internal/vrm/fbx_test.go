package vrm

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── synthetic binary-FBX builder ─────────────────────────────────────────────

type ftNode struct {
	name  string
	props []any
	kids  []*ftNode
}

// fzlibD marks a float64 array to be written zlib-compressed (encoding 1).
type fzlibD []float64

// fraw marks bytes to be written as a raw 'R' prop (e.g. Video Content).
type fraw []byte

// buildFBX serializes ftNodes into a binary FBX 7400 stream (u32 offsets).
func buildFBX(t *testing.T, nodes ...*ftNode) []byte {
	t.Helper()
	out := append([]byte("Kaydara FBX Binary  "), 0, 0x1a, 0)
	out = binary.LittleEndian.AppendUint32(out, 7400)
	for _, n := range nodes {
		out = append(out, serFBXNode(t, len(out), n)...)
	}
	return append(out, make([]byte, 13)...) // top-level sentinel
}

func serFBXNode(t *testing.T, start int, n *ftNode) []byte {
	t.Helper()
	pb := serFBXProps(t, n.props)
	var kb []byte
	if len(n.kids) > 0 {
		off := start + 13 + len(n.name) + len(pb)
		for _, k := range n.kids {
			b := serFBXNode(t, off, k)
			off += len(b)
			kb = append(kb, b...)
		}
		kb = append(kb, make([]byte, 13)...) // child sentinel
	}
	end := start + 13 + len(n.name) + len(pb) + len(kb)
	rec := binary.LittleEndian.AppendUint32(nil, uint32(end))
	rec = binary.LittleEndian.AppendUint32(rec, uint32(len(n.props)))
	rec = binary.LittleEndian.AppendUint32(rec, uint32(len(pb)))
	rec = append(rec, byte(len(n.name)))
	rec = append(rec, n.name...)
	rec = append(rec, pb...)
	return append(rec, kb...)
}

func serFBXProps(t *testing.T, props []any) []byte {
	t.Helper()
	var b []byte
	for _, p := range props {
		switch v := p.(type) {
		case int32:
			b = append(b, 'I')
			b = binary.LittleEndian.AppendUint32(b, uint32(v))
		case int64:
			b = append(b, 'L')
			b = binary.LittleEndian.AppendUint64(b, uint64(v))
		case float64:
			b = append(b, 'D')
			b = binary.LittleEndian.AppendUint64(b, math.Float64bits(v))
		case string:
			b = append(b, 'S')
			b = binary.LittleEndian.AppendUint32(b, uint32(len(v)))
			b = append(b, v...)
		case []float64:
			b = append(b, 'd')
			b = binary.LittleEndian.AppendUint32(b, uint32(len(v)))
			b = binary.LittleEndian.AppendUint32(b, 0)
			b = binary.LittleEndian.AppendUint32(b, uint32(len(v)*8))
			for _, f := range v {
				b = binary.LittleEndian.AppendUint64(b, math.Float64bits(f))
			}
		case []int32:
			b = append(b, 'i')
			b = binary.LittleEndian.AppendUint32(b, uint32(len(v)))
			b = binary.LittleEndian.AppendUint32(b, 0)
			b = binary.LittleEndian.AppendUint32(b, uint32(len(v)*4))
			for _, x := range v {
				b = binary.LittleEndian.AppendUint32(b, uint32(x))
			}
		case fraw:
			b = append(b, 'R')
			b = binary.LittleEndian.AppendUint32(b, uint32(len(v)))
			b = append(b, v...)
		case fzlibD:
			var raw []byte
			for _, f := range v {
				raw = binary.LittleEndian.AppendUint64(raw, math.Float64bits(f))
			}
			var zb bytes.Buffer
			zw := zlib.NewWriter(&zb)
			if _, err := zw.Write(raw); err != nil {
				t.Fatal(err)
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			b = append(b, 'd')
			b = binary.LittleEndian.AppendUint32(b, uint32(len(v)))
			b = binary.LittleEndian.AppendUint32(b, 1)
			b = binary.LittleEndian.AppendUint32(b, uint32(zb.Len()))
			b = append(b, zb.Bytes()...)
		default:
			t.Fatalf("serFBXProps: unsupported %T", p)
		}
	}
	return b
}

func ftP(name, typ string, vals ...any) *ftNode {
	return &ftNode{name: "P", props: append([]any{name, typ, "", "A"}, vals...)}
}

func ftProps70(ps ...*ftNode) *ftNode { return &ftNode{name: "Properties70", kids: ps} }

func ftModel(id int64, name, class string, kids ...*ftNode) *ftNode {
	return &ftNode{name: "Model", props: []any{id, name + "\x00\x01Model", class}, kids: kids}
}

func ftGeom(id int64, verts any, poly []int32) *ftNode {
	return &ftNode{name: "Geometry", props: []any{id, "geo\x00\x01Geometry", "Mesh"}, kids: []*ftNode{
		{name: "Vertices", props: []any{verts}},
		{name: "PolygonVertexIndex", props: []any{poly}},
	}}
}

func ftConn(child, parent int64) *ftNode {
	return &ftNode{name: "C", props: []any{"OO", child, parent}}
}

func ftConnOP(child, parent int64, prop string) *ftNode {
	return &ftNode{name: "C", props: []any{"OP", child, parent, prop}}
}

// ftGeomUV wraps ftGeom with a LayerElementUV (mapping/reference + data + optional index).
func ftGeomUV(id int64, verts []float64, poly []int32, mapping, ref string, uv []float64, uvIdx []int32) *ftNode {
	g := ftGeom(id, verts, poly)
	l := &ftNode{name: "LayerElementUV", props: []any{int32(0)}, kids: []*ftNode{
		{name: "MappingInformationType", props: []any{mapping}},
		{name: "ReferenceInformationType", props: []any{ref}},
		{name: "UV", props: []any{uv}},
	}}
	if uvIdx != nil {
		l.kids = append(l.kids, &ftNode{name: "UVIndex", props: []any{uvIdx}})
	}
	g.kids = append(g.kids, l)
	return g
}

func ftMaterial(id int64, name string, r, g, b float64) *ftNode {
	return &ftNode{name: "Material", props: []any{id, name + "\x00\x01Material", ""},
		kids: []*ftNode{ftProps70(ftP("DiffuseColor", "Color", r, g, b))}}
}

// checkerPNG encodes a 2×2 PNG: (0,0)=R (1,0)=G (0,1)=B (1,1)=W.
func checkerPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	img.SetNRGBA(1, 0, color.NRGBA{G: 255, A: 255})
	img.SetNRGBA(0, 1, color.NRGBA{B: 255, A: 255})
	img.SetNRGBA(1, 1, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func ftGlobal(unitScale float64) *ftNode {
	return &ftNode{name: "GlobalSettings", kids: []*ftNode{ftProps70(ftP("UnitScaleFactor", "double", unitScale))}}
}

var fbxIdentity16 = []float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}

// ── tests ────────────────────────────────────────────────────────────────────

func TestParseFBXScene(t *testing.T) {
	// UnitScaleFactor 100 → already meters. Quad 1.7 tall, fan → 2 triangles.
	verts := []float64{-0.5, 0, 0, 0.5, 0, 0, 0.5, 1.7, 0, -0.5, 1.7, 0}
	data := buildFBX(t,
		ftGlobal(100),
		&ftNode{name: "Objects", kids: []*ftNode{
			ftModel(100, "mixamorig:Hips", "LimbNode", ftProps70(ftP("Lcl Translation", "Lcl Translation", 0.0, 1.0, 0.0))),
			ftModel(101, "mixamorig:Head", "LimbNode", ftProps70(ftP("Lcl Translation", "Lcl Translation", 0.0, 0.6, 0.0))),
			ftModel(102, "Body", "Mesh"),
			ftGeom(200, verts, []int32{0, 1, 2, -4}),
		}},
		&ftNode{name: "Connections", kids: []*ftNode{
			ftConn(100, 0), ftConn(101, 100), ftConn(102, 0), ftConn(200, 102),
		}},
	)
	m, err := ParseFBX(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(m.Nodes))
	}
	if m.Nodes[1].Parent != 0 || m.Nodes[0].Parent != -1 {
		t.Errorf("hierarchy: head parent %d, hips parent %d", m.Nodes[1].Parent, m.Nodes[0].Parent)
	}
	if len(m.Roots) != 2 {
		t.Errorf("roots = %v", m.Roots)
	}
	if m.Nodes[1].Local.Translation() != [3]float32{0, 0.6, 0} {
		t.Errorf("head local = %v", m.Nodes[1].Local.Translation())
	}
	if m.HumanoidNode("hips") != 0 || m.HumanoidNode("head") != 1 {
		t.Errorf("humanoid = %v", m.Humanoid)
	}
	if len(m.Meshes) != 1 {
		t.Fatalf("meshes = %d", len(m.Meshes))
	}
	ms := m.Meshes[0]
	if len(ms.Verts) != 4 || ms.NodeIdx != 2 || ms.Skinned {
		t.Errorf("mesh verts=%d node=%d skinned=%v", len(ms.Verts), ms.NodeIdx, ms.Skinned)
	}
	want := []uint32{0, 1, 2, 0, 2, 3}
	for i, v := range want {
		if ms.Indices[i] != v {
			t.Fatalf("indices = %v, want %v", ms.Indices, want)
		}
	}
	lo, hi := m.Bounds()
	if h := hi[1] - lo[1]; math.Abs(float64(h)-1.7) > 1e-5 {
		t.Errorf("height = %v, want 1.7", h)
	}
}

func TestParseFBXZlibArray(t *testing.T) {
	data := buildFBX(t,
		ftGlobal(100),
		&ftNode{name: "Objects", kids: []*ftNode{
			ftModel(102, "Body", "Mesh"),
			ftGeom(200, fzlibD{0, 0, 0, 1, 0, 0, 0, 1, 0}, []int32{0, 1, -3}),
		}},
		&ftNode{name: "Connections", kids: []*ftNode{ftConn(102, 0), ftConn(200, 102)}},
	)
	m, err := ParseFBX(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Meshes) != 1 || len(m.Meshes[0].Verts) != 3 {
		t.Fatalf("meshes = %+v", m.Meshes)
	}
	if m.Meshes[0].Verts[1].Pos != [3]float32{1, 0, 0} {
		t.Errorf("vert1 = %v", m.Meshes[0].Verts[1].Pos)
	}
	if len(m.Meshes[0].Indices) != 3 {
		t.Errorf("indices = %v", m.Meshes[0].Indices)
	}
}

func TestParseFBXClusterWeights(t *testing.T) {
	// 5 clusters on vertex 0 → top-4 kept, renormalized. Bone1 bind translated (0,2,0).
	objs := []*ftNode{ftModel(110, "Body", "Mesh")}
	conns := []*ftNode{ftConn(110, 0), ftConn(200, 110), ftConn(300, 200)}
	weights := []float64{0.5, 0.4, 0.3, 0.2, 0.1}
	for i := range 5 {
		boneID, clID := int64(100+i), int64(301+i)
		objs = append(objs, ftModel(boneID, "bone", "LimbNode"))
		link := append([]float64(nil), fbxIdentity16...)
		if i == 1 {
			link[13] = 2
		}
		objs = append(objs, &ftNode{name: "Deformer", props: []any{clID, "cl\x00\x01SubDeformer", "Cluster"}, kids: []*ftNode{
			{name: "Indexes", props: []any{[]int32{0}}},
			{name: "Weights", props: []any{[]float64{weights[i]}}},
			{name: "TransformLink", props: []any{link}},
		}})
		conns = append(conns, ftConn(clID, 300), ftConn(boneID, clID))
	}
	objs = append(objs,
		ftGeom(200, []float64{0, 0, 0, 1, 0, 0, 0, 1, 0}, []int32{0, 1, -3}),
		&ftNode{name: "Deformer", props: []any{int64(300), "sk\x00\x01Deformer", "Skin"}},
	)
	data := buildFBX(t,
		ftGlobal(100),
		&ftNode{name: "Objects", kids: objs},
		&ftNode{name: "Connections", kids: conns},
	)
	m, err := ParseFBX(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.SkinJoints) != 5 || len(m.InverseBind) != 5 {
		t.Fatalf("joints = %v", m.SkinJoints)
	}
	// Bones are models 1..5 (Body is node 0).
	for slot, node := range m.SkinJoints {
		if node != slot+1 {
			t.Fatalf("skinJoints = %v", m.SkinJoints)
		}
	}
	if got := m.InverseBind[1].Translation(); got != [3]float32{0, -2, 0} {
		t.Errorf("inverseBind1 t = %v, want (0,-2,0)", got)
	}
	ms := m.Meshes[0]
	if !ms.Skinned {
		t.Fatal("mesh not skinned")
	}
	v := ms.Verts[0]
	if v.Joints != [4]uint16{0, 1, 2, 3} {
		t.Errorf("joints = %v", v.Joints)
	}
	var sum float32
	for k, want := range []float32{0.5 / 1.4, 0.4 / 1.4, 0.3 / 1.4, 0.2 / 1.4} {
		if math.Abs(float64(v.Weights[k]-want)) > 1e-5 {
			t.Errorf("weight%d = %v, want %v", k, v.Weights[k], want)
		}
		sum += v.Weights[k]
	}
	if math.Abs(float64(sum)-1) > 1e-5 {
		t.Errorf("weight sum = %v", sum)
	}
}

func TestParseFBXUnitScale(t *testing.T) {
	// No GlobalSettings → cm default ×0.01: 170-unit figure → 1.7 m.
	data := buildFBX(t,
		&ftNode{name: "Objects", kids: []*ftNode{
			ftModel(102, "Body", "Mesh"),
			ftGeom(200, []float64{0, 0, 0, 50, 0, 0, 0, 170, 0}, []int32{0, 1, -3}),
		}},
		&ftNode{name: "Connections", kids: []*ftNode{ftConn(102, 0), ftConn(200, 102)}},
	)
	m, err := ParseFBX(data)
	if err != nil {
		t.Fatal(err)
	}
	got := m.Meshes[0].Verts[2].Pos
	if math.Abs(float64(got[1])-1.7) > 1e-5 || got[0] != 0 {
		t.Errorf("scaled vert = %v", got)
	}
	lo, hi := m.Bounds()
	if h := hi[1] - lo[1]; math.Abs(float64(h)-1.7) > 1e-5 {
		t.Errorf("height = %v", h)
	}
}

func TestParseFBXScaleFallback(t *testing.T) {
	// Bogus UnitScaleFactor 10000 (×100) on a 1.7-unit figure → 170 m, implausible → ×1 fallback.
	data := buildFBX(t,
		ftGlobal(10000),
		&ftNode{name: "Objects", kids: []*ftNode{
			ftModel(102, "Body", "Mesh"),
			ftGeom(200, []float64{0, 0, 0, 0.5, 0, 0, 0, 1.7, 0}, []int32{0, 1, -3}),
		}},
		&ftNode{name: "Connections", kids: []*ftNode{ftConn(102, 0), ftConn(200, 102)}},
	)
	m, err := ParseFBX(data)
	if err != nil {
		t.Fatal(err)
	}
	lo, hi := m.Bounds()
	if h := hi[1] - lo[1]; math.Abs(float64(h)-1.7) > 1e-5 {
		t.Errorf("height = %v, want 1.7 (×1 fallback)", h)
	}
}

func TestFBXBoneMapping(t *testing.T) {
	cases := map[string]string{
		"mixamorig:Hips":        "hips",
		"mixamorig1:LeftArm":    "leftupperarm",
		"mixamorig:LeftForeArm": "leftlowerarm",
		"Upperarm_L":            "leftupperarm",
		"R_Hand":                "righthand",
		"mixamorig:RightUpLeg":  "rightupperleg",
		"Left leg":              "leftlowerleg",
		"right_foot":            "rightfoot",
		"Head":                  "head",
		"Chest.001":             "",
	}
	for name, want := range cases {
		if got := fbxBoneAlias[fbxNormName(name)]; got != want {
			t.Errorf("map(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestParseFBXUVByPolygonVertexSplit(t *testing.T) {
	// Two triangles sharing ctrl 0 with DIFFERENT UVs → vertex split; skin weights follow.
	// Corners: face0 = ctrl 0,1,2 (uvIdx 0,1,2), face1 = ctrl 0,2,3 (uvIdx 3,2,4).
	verts := []float64{0, 0, 0, 1, 0, 0, 1, 1, 0, 0, 1, 0}
	uv := []float64{0, 0, 1, 0, 1, 1, 0.5, 0.5, 0, 1}
	geom := ftGeomUV(200, verts, []int32{0, 1, -3, 0, 2, -4}, "ByPolygonVertex", "IndexToDirect", uv, []int32{0, 1, 2, 3, 2, 4})
	boneLink := append([]float64(nil), fbxIdentity16...)
	data := buildFBX(t,
		ftGlobal(100),
		&ftNode{name: "Objects", kids: []*ftNode{
			ftModel(110, "Body", "Mesh"),
			ftModel(100, "bone", "LimbNode"),
			geom,
			&ftNode{name: "Deformer", props: []any{int64(300), "sk\x00\x01Deformer", "Skin"}},
			&ftNode{name: "Deformer", props: []any{int64(301), "cl\x00\x01SubDeformer", "Cluster"}, kids: []*ftNode{
				{name: "Indexes", props: []any{[]int32{0}}},
				{name: "Weights", props: []any{[]float64{0.8}}},
				{name: "TransformLink", props: []any{boneLink}},
			}},
		}},
		&ftNode{name: "Connections", kids: []*ftNode{
			ftConn(110, 0), ftConn(200, 110), ftConn(300, 200), ftConn(301, 300), ftConn(100, 301),
		}},
	)
	m, err := ParseFBX(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Meshes) != 1 {
		t.Fatalf("meshes = %d", len(m.Meshes))
	}
	ms := m.Meshes[0]
	// ctrl 0 splits (two UVs) → 5 verts: [c0a c1 c2 c0b c3]
	if len(ms.Verts) != 5 || len(ms.UV) != 5 {
		t.Fatalf("verts=%d uv=%d, want 5/5", len(ms.Verts), len(ms.UV))
	}
	want := []uint32{0, 1, 2, 3, 2, 4}
	for i, v := range want {
		if ms.Indices[i] != v {
			t.Fatalf("indices = %v, want %v", ms.Indices, want)
		}
	}
	if ms.UV[0] != [2]float32{0, 0} || ms.UV[3] != [2]float32{0.5, 0.5} {
		t.Errorf("split UVs = %v / %v", ms.UV[0], ms.UV[3])
	}
	if ms.Verts[0].Pos != ms.Verts[3].Pos {
		t.Errorf("split verts should share pos: %v %v", ms.Verts[0].Pos, ms.Verts[3].Pos)
	}
	if !ms.Skinned || ms.Verts[0].Weights != ms.Verts[3].Weights || ms.Verts[0].Weights[0] != 1 {
		t.Errorf("skin weights didn't follow split: %v %v", ms.Verts[0].Weights, ms.Verts[3].Weights)
	}
	// no normal layer → smooth normals, planar quad → ±Z
	if len(ms.Normals) != 5 {
		t.Fatalf("normals = %d", len(ms.Normals))
	}
	for i, n := range ms.Normals {
		if math.Abs(math.Abs(float64(n[2]))-1) > 1e-5 {
			t.Errorf("normal[%d] = %v, want ±Z", i, n)
		}
	}
}

func TestParseFBXUVByControlPointDirect(t *testing.T) {
	verts := []float64{0, 0, 0, 1, 0, 0, 1, 1, 0, 0, 1, 0}
	uv := []float64{0, 0, 1, 0, 1, 1, 0, 1} // per control point
	data := buildFBX(t,
		ftGlobal(100),
		&ftNode{name: "Objects", kids: []*ftNode{
			ftModel(110, "Body", "Mesh"),
			ftGeomUV(200, verts, []int32{0, 1, 2, -4}, "ByControlPoint", "Direct", uv, nil),
		}},
		&ftNode{name: "Connections", kids: []*ftNode{ftConn(110, 0), ftConn(200, 110)}},
	)
	m, err := ParseFBX(data)
	if err != nil {
		t.Fatal(err)
	}
	ms := m.Meshes[0]
	if len(ms.Verts) != 4 { // no splits needed
		t.Fatalf("verts = %d, want 4", len(ms.Verts))
	}
	for i, w := range [][2]float32{{0, 0}, {1, 0}, {1, 1}, {0, 1}} {
		if ms.UV[i] != w {
			t.Errorf("uv[%d] = %v, want %v", i, ms.UV[i], w)
		}
	}
}

func TestParseFBXMaterialSplit(t *testing.T) {
	// 2 faces, ByPolygon materials [0,1] → 2 sub-meshes with red / green diffuse.
	verts := []float64{0, 0, 0, 1, 0, 0, 1, 1, 0, 0, 1, 0}
	geom := ftGeom(200, verts, []int32{0, 1, -3, 0, 2, -4})
	geom.kids = append(geom.kids, &ftNode{name: "LayerElementMaterial", props: []any{int32(0)}, kids: []*ftNode{
		{name: "MappingInformationType", props: []any{"ByPolygon"}},
		{name: "ReferenceInformationType", props: []any{"IndexToDirect"}},
		{name: "Materials", props: []any{[]int32{0, 1}}},
	}})
	data := buildFBX(t,
		ftGlobal(100),
		&ftNode{name: "Objects", kids: []*ftNode{
			ftModel(110, "Body", "Mesh"),
			geom,
			ftMaterial(500, "red", 1, 0, 0),
			ftMaterial(501, "green", 0, 1, 0),
		}},
		&ftNode{name: "Connections", kids: []*ftNode{
			ftConn(110, 0), ftConn(200, 110), ftConn(500, 110), ftConn(501, 110),
		}},
	)
	m, err := ParseFBX(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Meshes) != 2 {
		t.Fatalf("meshes = %d, want 2 (per material)", len(m.Meshes))
	}
	if m.Meshes[0].Diffuse != (color.NRGBA{R: 255, A: 255}) || m.Meshes[1].Diffuse != (color.NRGBA{G: 255, A: 255}) {
		t.Errorf("diffuse = %v / %v", m.Meshes[0].Diffuse, m.Meshes[1].Diffuse)
	}
	if len(m.Meshes[0].Indices) != 3 || len(m.Meshes[1].Indices) != 3 {
		t.Errorf("indices = %d / %d", len(m.Meshes[0].Indices), len(m.Meshes[1].Indices))
	}
}

func TestParseFBXEmbeddedTexture(t *testing.T) {
	verts := []float64{0, 0, 0, 1, 0, 0, 1, 1, 0}
	data := buildFBX(t,
		ftGlobal(100),
		&ftNode{name: "Objects", kids: []*ftNode{
			ftModel(110, "Body", "Mesh"),
			ftGeomUV(200, verts, []int32{0, 1, -3}, "ByPolygonVertex", "Direct", []float64{0, 0, 1, 0, 1, 1}, nil),
			ftMaterial(500, "skin", 0.8, 0.8, 0.8),
			{name: "Texture", props: []any{int64(600), "tex\x00\x01Texture", ""}, kids: []*ftNode{
				{name: "FileName", props: []any{"C:\\gone\\tex.png"}},
			}},
			{name: "Video", props: []any{int64(700), "vid\x00\x01Video", "Clip"}, kids: []*ftNode{
				{name: "Filename", props: []any{"C:\\gone\\tex.png"}},
				{name: "Content", props: []any{fraw(checkerPNG(t))}},
			}},
		}},
		&ftNode{name: "Connections", kids: []*ftNode{
			ftConn(110, 0), ftConn(200, 110), ftConn(500, 110),
			ftConn(700, 600),                   // video → texture
			ftConnOP(600, 500, "DiffuseColor"), // texture → material
		}},
	)
	m, err := ParseFBX(data)
	if err != nil {
		t.Fatal(err)
	}
	ms := m.Meshes[0]
	if ms.Tex == nil {
		t.Fatal("embedded texture not decoded")
	}
	if ms.Tex.Rect.Dx() != 2 || ms.Tex.Rect.Dy() != 2 {
		t.Fatalf("tex dims = %v", ms.Tex.Rect)
	}
	if got := ms.Tex.NRGBAAt(0, 0); got != (color.NRGBA{R: 255, A: 255}) {
		t.Errorf("texel(0,0) = %v, want red", got)
	}
	if ms.Diffuse == (color.NRGBA{}) {
		t.Error("diffuse not set")
	}
}

func TestParseFBXExternalTextureFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tex.png"), checkerPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	verts := []float64{0, 0, 0, 1, 0, 0, 1, 1, 0}
	data := buildFBX(t,
		ftGlobal(100),
		&ftNode{name: "Objects", kids: []*ftNode{
			ftModel(110, "Body", "Mesh"),
			ftGeomUV(200, verts, []int32{0, 1, -3}, "ByPolygonVertex", "Direct", []float64{0, 0, 1, 0, 1, 1}, nil),
			ftMaterial(500, "skin", 0.8, 0.8, 0.8),
			{name: "Texture", props: []any{int64(600), "tex\x00\x01Texture", ""}, kids: []*ftNode{
				{name: "FileName", props: []any{"B:\\stale\\path\\tex.png"}}, // stale abs → base-name fallback
				{name: "RelativeFilename", props: []any{"tex.png"}},
			}},
		}},
		&ftNode{name: "Connections", kids: []*ftNode{
			ftConn(110, 0), ftConn(200, 110), ftConn(500, 110),
			ftConnOP(600, 500, "DiffuseColor"),
		}},
	)
	fbxPath := filepath.Join(dir, "a.fbx")
	if err := os.WriteFile(fbxPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load(fbxPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.Meshes[0].Tex == nil {
		t.Fatal("external texture not resolved from fbx dir")
	}
	// ParseFBX without a dir must fall back to nil texture, not error.
	m2, err := ParseFBX(data)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Meshes[0].Tex != nil {
		t.Error("dirless parse should have no texture")
	}
}

func TestLoadASCIIFBXError(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.fbx")
	if err := os.WriteFile(p, []byte("; FBX 7.4.0 project file\nFBXHeaderExtension: {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "ASCII FBX") {
		t.Fatalf("err = %v, want ASCII FBX error", err)
	}
}

func TestLoadRealFBX(t *testing.T) {
	path := filepath.Join(os.Getenv("APPDATA"), "rave-mate", "vr_avatars", "LAMB.fbx")
	if _, err := os.Stat(path); err != nil {
		t.Skip("LAMB.fbx not present")
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Meshes) < 1 {
		t.Fatal("no meshes")
	}
	total := 0
	for _, ms := range m.Meshes {
		total += len(ms.Verts)
	}
	if total < 1000 {
		t.Fatalf("total verts = %d, want >= 1000", total)
	}
	if m.HumanoidNode("hips") < 0 || m.HumanoidNode("head") < 0 {
		t.Fatalf("humanoid missing hips/head: %v", m.Humanoid)
	}
	lo, hi := m.Bounds()
	h := hi[1] - lo[1]
	if h < 0.5 || h > 3 {
		t.Fatalf("height = %v m, want [0.5,3]", h)
	}
	// vrmik-compatible surface: rest world → skin matrices → posed positions+normals, no panic.
	world := m.RestWorld()
	skin := m.SkinMatrices(world)
	for mi := range m.Meshes {
		if got := m.PosedPositions(mi, world, skin); len(got) != len(m.Meshes[mi].Verts) {
			t.Fatalf("posed len mismatch mesh %d", mi)
		}
		if n := m.PosedNormals(mi, world, skin); n != nil && len(n) != len(m.Meshes[mi].Verts) {
			t.Fatalf("posed normals len mismatch mesh %d", mi)
		}
	}
	textured, withUV, withN := 0, 0, 0
	for mi := range m.Meshes {
		ms := &m.Meshes[mi]
		texDim := "-"
		if ms.Tex != nil {
			textured++
			texDim = ms.Tex.Rect.Size().String()
		}
		if ms.UV != nil {
			withUV++
		}
		if ms.Normals != nil {
			withN++
		}
		t.Logf("mesh %d: verts=%d tris=%d diffuse=%v tex=%s", mi, len(ms.Verts), len(ms.Indices)/3, ms.Diffuse, texDim)
	}
	if textured == 0 {
		t.Error("no textured mesh (LAMB embeds Video Content - expected ≥1)")
	}
	if withUV == 0 || withN != len(m.Meshes) {
		t.Errorf("uv=%d normals=%d of %d meshes", withUV, withN, len(m.Meshes))
	}
	t.Logf("meshes=%d (textured=%d uv=%d) verts=%d joints=%d nodes=%d humanoid=%d height=%.2fm",
		len(m.Meshes), textured, withUV, total, len(m.SkinJoints), len(m.Nodes), len(m.Humanoid), h)
}
