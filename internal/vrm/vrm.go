// Package vrm loads a VRM / glTF 2.0 avatar (pure stdlib, no cgo) for an in-app preview: node
// hierarchy, skinned triangle meshes, the skin's joints + inverse-bind matrices, and the VRM
// humanoid bone→node map (VRM 0.x "VRM" and 1.0 "VRMC_vrm"). The FBX path also carries per-vertex
// UVs/normals + diffuse material color/texture; the glTF path leaves those nil (flat-shaded).
// The caller computes world matrices (rest or posed) and CPU-skins via the helpers here, then
// rasterizes. Single-skin assumption (true for VRChat/VRM avatars).
package vrm

import (
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// Vertex is a skinned mesh vertex: bind-pose position + up to 4 joint influences.
type Vertex struct {
	Pos     [3]float32
	Joints  [4]uint16
	Weights [4]float32
}

// Mesh is one triangle primitive.
type Mesh struct {
	Verts   []Vertex
	Indices []uint32
	NodeIdx int  // node referencing this mesh (placement for unskinned meshes)
	Skinned bool // primitive has JOINTS_0 + WEIGHTS_0 and its node has a skin

	UV      [][2]float32 // per-vertex texcoords (FBX convention, v=0 bottom); nil = untextured
	Normals [][3]float32 // per-vertex bind-pose unit normals; nil = flat-shade
	Diffuse color.NRGBA  // material diffuse; zero value = renderer default
	Tex     *image.NRGBA // diffuse texture; nil = none (use Diffuse)
}

// Node is one scene-graph node with its local bind transform.
type Node struct {
	Name     string
	Parent   int // -1 for roots
	Children []int
	Local    Mat4
}

// Model is a loaded avatar.
type Model struct {
	Nodes       []Node
	Roots       []int
	Meshes      []Mesh
	SkinJoints  []int          // node index per skin-joint slot
	InverseBind []Mat4         // per skin-joint
	Humanoid    map[string]int // lower bone name → node index
	restLocal   []Mat4
}

// Load reads + parses a .vrm / .glb / .gltf / binary .fbx avatar file.
func Load(path string) (*Model, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if isBinaryFBX(data) {
		return parseFBX(data, filepath.Dir(path))
	}
	if strings.EqualFold(filepath.Ext(path), ".fbx") {
		return nil, errors.New("ASCII FBX not supported - re-export as binary FBX")
	}
	return Parse(data)
}

// Parse builds a Model from raw VRM/glTF bytes.
func Parse(data []byte) (*Model, error) {
	doc, bin, err := parseContainer(data)
	if err != nil {
		return nil, err
	}
	m := &Model{Humanoid: map[string]int{}}

	m.Nodes = make([]Node, len(doc.Nodes))
	for i, n := range doc.Nodes {
		nd := Node{Name: n.Name, Parent: -1, Children: append([]int(nil), n.Children...)}
		if len(n.Matrix) == 16 {
			nd.Local = fromColMajor16(n.Matrix)
		} else {
			nd.Local = TRS(vec3or(n.Translation, [3]float32{0, 0, 0}),
				vec4or(n.Rotation, [4]float32{0, 0, 0, 1}),
				vec3or(n.Scale, [3]float32{1, 1, 1}))
		}
		m.Nodes[i] = nd
	}
	childOf := make([]bool, len(m.Nodes))
	for i := range m.Nodes {
		for _, c := range m.Nodes[i].Children {
			if c >= 0 && c < len(m.Nodes) {
				m.Nodes[c].Parent = i
				childOf[c] = true
			}
		}
	}
	for i := range m.Nodes {
		if !childOf[i] {
			m.Roots = append(m.Roots, i)
		}
	}
	m.restLocal = make([]Mat4, len(m.Nodes))
	for i := range m.Nodes {
		m.restLocal[i] = m.Nodes[i].Local
	}

	if len(doc.Skins) > 0 {
		sk := doc.Skins[0]
		m.SkinJoints = append([]int(nil), sk.Joints...)
		if sk.InverseBindMatrices != nil {
			if m.InverseBind, err = doc.readMat4s(bin, *sk.InverseBindMatrices); err != nil {
				return nil, err
			}
		} else {
			m.InverseBind = make([]Mat4, len(sk.Joints))
			for i := range m.InverseBind {
				m.InverseBind[i] = Identity()
			}
		}
	}

	for ni, n := range doc.Nodes {
		if n.Mesh == nil {
			continue
		}
		gm := doc.Meshes[*n.Mesh]
		for _, prim := range gm.Primitives {
			if prim.Mode != nil && *prim.Mode != 4 { // triangles only
				continue
			}
			mesh, err := buildMesh(doc, bin, prim, ni, n.Skin != nil)
			if err != nil {
				return nil, err
			}
			if mesh != nil {
				m.Meshes = append(m.Meshes, *mesh)
			}
		}
	}

	parseHumanoid(doc, m.Humanoid)
	return m, nil
}

func buildMesh(doc *gltfDoc, bin []byte, prim gltfPrimitive, nodeIdx int, nodeSkinned bool) (*Mesh, error) {
	posAcc, ok := prim.Attributes["POSITION"]
	if !ok {
		return nil, nil
	}
	pos, err := doc.readFloats(bin, posAcc, 3)
	if err != nil {
		return nil, err
	}
	n := len(pos) / 3
	verts := make([]Vertex, n)
	for i := range n {
		verts[i].Pos = [3]float32{pos[i*3], pos[i*3+1], pos[i*3+2]}
	}

	skinned := false
	if jAcc, ok := prim.Attributes["JOINTS_0"]; ok && nodeSkinned {
		if wAcc, ok := prim.Attributes["WEIGHTS_0"]; ok {
			joints, err := doc.readInts(bin, jAcc, 4)
			if err != nil {
				return nil, err
			}
			weights, err := doc.readFloats(bin, wAcc, 4)
			if err != nil {
				return nil, err
			}
			for i := range n {
				for k := range 4 {
					verts[i].Joints[k] = uint16(joints[i*4+k])
					verts[i].Weights[k] = weights[i*4+k]
				}
			}
			skinned = true
		}
	}

	var indices []uint32
	if prim.Indices != nil {
		if indices, err = doc.readInts(bin, *prim.Indices, 1); err != nil {
			return nil, err
		}
	} else {
		indices = make([]uint32, n)
		for i := range n {
			indices[i] = uint32(i)
		}
	}
	return &Mesh{Verts: verts, Indices: indices, NodeIdx: nodeIdx, Skinned: skinned}, nil
}

// RestWorld returns world matrices at the bind pose.
func (m *Model) RestWorld() []Mat4 { return m.WorldFrom(m.restLocal) }

// RestLocal returns a copy of the bind-pose local transforms (caller poses by overriding entries).
func (m *Model) RestLocal() []Mat4 { return append([]Mat4(nil), m.restLocal...) }

// WorldFrom computes world matrices from per-node local transforms (len == len(Nodes)).
func (m *Model) WorldFrom(local []Mat4) []Mat4 {
	world := make([]Mat4, len(m.Nodes))
	var visit func(i int, parent Mat4)
	visit = func(i int, parent Mat4) {
		w := parent.Mul(local[i])
		world[i] = w
		for _, c := range m.Nodes[i].Children {
			if c >= 0 && c < len(m.Nodes) {
				visit(c, w)
			}
		}
	}
	for _, r := range m.Roots {
		visit(r, Identity())
	}
	return world
}

// SkinMatrices returns world[jointNode]*inverseBind per skin-joint, for CPU skinning.
func (m *Model) SkinMatrices(world []Mat4) []Mat4 {
	out := make([]Mat4, len(m.SkinJoints))
	for j, node := range m.SkinJoints {
		ibm := Identity()
		if j < len(m.InverseBind) {
			ibm = m.InverseBind[j]
		}
		if node >= 0 && node < len(world) {
			out[j] = world[node].Mul(ibm)
		} else {
			out[j] = ibm
		}
	}
	return out
}

// PosedPositions returns world-space positions for mesh mi: linear-blend skinning when skinned,
// else the mesh node's world transform applied to bind positions.
func (m *Model) PosedPositions(mi int, world, skin []Mat4) [][3]float32 {
	mesh := m.Meshes[mi]
	out := make([][3]float32, len(mesh.Verts))
	if mesh.Skinned && len(skin) > 0 {
		for i, v := range mesh.Verts {
			var p [3]float32
			var wsum float32
			for k := range 4 {
				w := v.Weights[k]
				if w == 0 {
					continue
				}
				j := int(v.Joints[k])
				if j >= len(skin) {
					continue
				}
				tp := skin[j].TransformPoint(v.Pos)
				p[0] += tp[0] * w
				p[1] += tp[1] * w
				p[2] += tp[2] * w
				wsum += w
			}
			if wsum == 0 {
				p = world[mesh.NodeIdx].TransformPoint(v.Pos)
			}
			out[i] = p
		}
		return out
	}
	wm := world[mesh.NodeIdx]
	for i, v := range mesh.Verts {
		out[i] = wm.TransformPoint(v.Pos)
	}
	return out
}

// PosedNormals returns world-space unit normals for mesh mi (nil when the mesh has none):
// weight-blended 3x3 skin transform when skinned, else the mesh node's rotation. Renormalized,
// so uniform scale is safe (non-uniform scale skips the inverse-transpose - fine for avatars).
func (m *Model) PosedNormals(mi int, world, skin []Mat4) [][3]float32 {
	mesh := m.Meshes[mi]
	if mesh.Normals == nil {
		return nil
	}
	out := make([][3]float32, len(mesh.Normals))
	if mesh.Skinned && len(skin) > 0 {
		for i, v := range mesh.Verts {
			n := mesh.Normals[i]
			var d [3]float32
			var wsum float32
			for k := range 4 {
				w := v.Weights[k]
				if w == 0 {
					continue
				}
				j := int(v.Joints[k])
				if j >= len(skin) {
					continue
				}
				td := skin[j].TransformDir(n)
				d[0] += td[0] * w
				d[1] += td[1] * w
				d[2] += td[2] * w
				wsum += w
			}
			if wsum == 0 {
				d = world[mesh.NodeIdx].TransformDir(n)
			}
			out[i] = norm3(d)
		}
		return out
	}
	wm := world[mesh.NodeIdx]
	for i, n := range mesh.Normals {
		out[i] = norm3(wm.TransformDir(n))
	}
	return out
}

// norm3 normalizes v; zero vector → +Z.
func norm3(v [3]float32) [3]float32 {
	l := float32(math.Sqrt(float64(v[0]*v[0] + v[1]*v[1] + v[2]*v[2])))
	if l == 0 {
		return [3]float32{0, 0, 1}
	}
	return [3]float32{v[0] / l, v[1] / l, v[2] / l}
}

// Bounds returns the rest-pose world AABB across all meshes (lo, hi).
func (m *Model) Bounds() (lo, hi [3]float32) {
	lo = [3]float32{1e9, 1e9, 1e9}
	hi = [3]float32{-1e9, -1e9, -1e9}
	world := m.RestWorld()
	skin := m.SkinMatrices(world)
	any := false
	for mi := range m.Meshes {
		for _, p := range m.PosedPositions(mi, world, skin) {
			any = true
			for i := range 3 {
				if p[i] < lo[i] {
					lo[i] = p[i]
				}
				if p[i] > hi[i] {
					hi[i] = p[i]
				}
			}
		}
	}
	if !any {
		return [3]float32{-0.5, 0, -0.5}, [3]float32{0.5, 1.7, 0.5}
	}
	return lo, hi
}

func vec3or(s []float64, def [3]float32) [3]float32 {
	if len(s) < 3 {
		return def
	}
	return [3]float32{float32(s[0]), float32(s[1]), float32(s[2])}
}

func vec4or(s []float64, def [4]float32) [4]float32 {
	if len(s) < 4 {
		return def
	}
	return [4]float32{float32(s[0]), float32(s[1]), float32(s[2]), float32(s[3])}
}

// ── VRM humanoid bone map ────────────────────────────────────────────────────

// parseHumanoid fills out with lower-cased humanoid bone name → node index, supporting VRM 1.0
// (VRMC_vrm: humanBones map) and VRM 0.x (VRM: humanBones array).
func parseHumanoid(doc *gltfDoc, out map[string]int) {
	if raw, ok := doc.Extensions["VRMC_vrm"]; ok {
		var ext struct {
			Humanoid struct {
				HumanBones map[string]struct {
					Node int `json:"node"`
				} `json:"humanBones"`
			} `json:"humanoid"`
		}
		if json.Unmarshal(raw, &ext) == nil {
			for bone, b := range ext.Humanoid.HumanBones {
				out[lower(bone)] = b.Node
			}
			if len(out) > 0 {
				return
			}
		}
	}
	if raw, ok := doc.Extensions["VRM"]; ok {
		var ext struct {
			Humanoid struct {
				HumanBones []struct {
					Bone string `json:"bone"`
					Node int    `json:"node"`
				} `json:"humanBones"`
			} `json:"humanoid"`
		}
		if json.Unmarshal(raw, &ext) == nil {
			for _, b := range ext.Humanoid.HumanBones {
				out[lower(b.Bone)] = b.Node
			}
		}
	}
}

func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// HumanoidNode returns the node index for a humanoid bone (e.g. "head","leftHand"); -1 if absent.
func (m *Model) HumanoidNode(bone string) int {
	if n, ok := m.Humanoid[lower(bone)]; ok {
		return n
	}
	return -1
}
