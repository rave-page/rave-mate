package avataratlas

// gltf.go - minimal glTF 2.0 / GLB parser scoped to what avatar scanning needs: GLB container,
// buffers/bufferViews/accessors (VEC3f POSITION, VEC2f TEXCOORD_0, VEC4 ubyte/ushort JOINTS_0,
// VEC4 float/ubyte-norm/ushort-norm WEIGHTS_0, SCALAR ubyte/ushort/uint indices, MAT4f IBMs),
// triangle primitives, nodes tree, skins, pbrMetallicRoughness baseColor, images/textures/
// samplers (image/png + image/jpeg; external .bin/image files relative to a .gltf), VRM 0.x AND
// VRM 1.0 humanoid maps. Everything else rejects with a clear error. Stdlib only.

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg" // texture decode
	_ "image/png"  // texture decode
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// glTF componentType codes.
const (
	compUByte  = 5121
	compUShort = 5123
	compUInt   = 5125
	compFloat  = 5126
)

// glTF sampler wrap modes.
const (
	WrapRepeat = 10497 // default
	WrapClamp  = 33071
	WrapMirror = 33648
)

const modeTriangles = 4

// requiredExtOK are extensionsRequired entries we can safely satisfy while scanning (unlit
// still exposes pbrMetallicRoughness baseColor; VRM0 exporters commonly require it).
var requiredExtOK = map[string]bool{"KHR_materials_unlit": true}

// ── public document model ────────────────────────────────────────────────────

// Node is one scene-graph node (TRS is irrelevant to scanning: skinned vertices live in model
// space and bone-local space comes from the skin's inverseBindMatrices).
type Node struct {
	Name     string
	Children []int
	Parent   int // -1 = root
	Mesh     int // -1 = none
	Skin     int // -1 = none
}

// Skin is joints (node indices) + one column-major inverse bind matrix per joint.
type Skin struct {
	Joints []int
	IBMs   [][16]float64
	// jointIdx maps node index -> position in Joints (built at parse).
	jointIdx map[int]int
}

// JointIndex returns the position of node n in the skin's joint list.
func (s *Skin) JointIndex(n int) (int, bool) { j, ok := s.jointIdx[n]; return j, ok }

// Primitive is one decoded triangle primitive.
type Primitive struct {
	Pos     [][3]float64
	UV      [][2]float64 // nil if no TEXCOORD_0
	Joints  [][4]int     // nil if unskinned
	Weights [][4]float64 // nil if unskinned; normalized to float
	Indices []uint32     // always populated (sequential when the primitive had none)
	Mat     int          // material index, -1 = none
}

// Mesh is a list of primitives.
type Mesh struct{ Primitives []Primitive }

// Texture is a decoded baseColorTexture (bytes stay sRGB; sampled bilinearly in sRGB space,
// factor applied in linear - see sample.go).
type Texture struct {
	Img          image.Image
	WrapS, WrapT int
}

// Material carries only what point colouring needs.
type Material struct {
	BaseColorFactor [4]float64 // linear RGBA, default 1,1,1,1
	Tex             *Texture   // nil = factor-only
}

// Document is the parsed avatar.
type Document struct {
	Nodes     []Node
	Meshes    []Mesh
	Skins     []Skin
	Materials []Material
	// NodeSlot maps node index -> §5 bone slot, from the VRM humanoid map (0.x or 1.0).
	NodeSlot map[int]int
	// VRMVersion is "0" or "1" ("" when the file had no VRM humanoid extension).
	VRMVersion string
	// HumanoidDupNodes counts spec-violating duplicate node references in the humanoid map
	// (two bone names -> same node). Resolution is deterministic (sorted bone-name order,
	// last wins); the anomaly is surfaced here for the CLI report.
	HumanoidDupNodes int
}

// ── raw JSON structs ─────────────────────────────────────────────────────────

type jDoc struct {
	Asset              struct{ Version string }   `json:"asset"`
	ExtensionsRequired []string                   `json:"extensionsRequired"`
	Extensions         map[string]json.RawMessage `json:"extensions"`
	Nodes              []jNode                    `json:"nodes"`
	Meshes             []jMesh                    `json:"meshes"`
	Skins              []jSkin                    `json:"skins"`
	Accessors          []jAccessor                `json:"accessors"`
	BufferViews        []jBufferView              `json:"bufferViews"`
	Buffers            []jBuffer                  `json:"buffers"`
	Materials          []jMaterial                `json:"materials"`
	Textures           []jTexture                 `json:"textures"`
	Images             []jImage                   `json:"images"`
	Samplers           []jSampler                 `json:"samplers"`
}

type jNode struct {
	Name     string `json:"name"`
	Children []int  `json:"children"`
	Mesh     *int   `json:"mesh"`
	Skin     *int   `json:"skin"`
}

type jMesh struct {
	Primitives []jPrimitive `json:"primitives"`
}

type jPrimitive struct {
	Attributes map[string]int `json:"attributes"`
	Indices    *int           `json:"indices"`
	Material   *int           `json:"material"`
	Mode       *int           `json:"mode"`
}

type jSkin struct {
	InverseBindMatrices *int  `json:"inverseBindMatrices"`
	Joints              []int `json:"joints"`
}

type jAccessor struct {
	BufferView    *int            `json:"bufferView"`
	ByteOffset    int             `json:"byteOffset"`
	ComponentType int             `json:"componentType"`
	Normalized    bool            `json:"normalized"`
	Count         int             `json:"count"`
	Type          string          `json:"type"`
	Sparse        json.RawMessage `json:"sparse"`
}

type jBufferView struct {
	Buffer     int  `json:"buffer"`
	ByteOffset int  `json:"byteOffset"`
	ByteLength int  `json:"byteLength"`
	ByteStride *int `json:"byteStride"`
}

type jBuffer struct {
	URI        string `json:"uri"`
	ByteLength int    `json:"byteLength"`
}

type jMaterial struct {
	PBR *struct {
		BaseColorFactor  []float64 `json:"baseColorFactor"`
		BaseColorTexture *struct {
			Index    int `json:"index"`
			TexCoord int `json:"texCoord"`
		} `json:"baseColorTexture"`
	} `json:"pbrMetallicRoughness"`
}

type jTexture struct {
	Sampler *int `json:"sampler"`
	Source  *int `json:"source"`
}

type jImage struct {
	URI        string `json:"uri"`
	MimeType   string `json:"mimeType"`
	BufferView *int   `json:"bufferView"`
}

type jSampler struct {
	WrapS *int `json:"wrapS"`
	WrapT *int `json:"wrapT"`
}

// VRM 0.x: extensions.VRM.humanoid.humanBones = [{bone, node}].
type jVRM0 struct {
	Humanoid struct {
		HumanBones []struct {
			Bone string `json:"bone"`
			Node int    `json:"node"`
		} `json:"humanBones"`
	} `json:"humanoid"`
}

// VRM 1.0: extensions.VRMC_vrm.humanoid.humanBones.{name}.node.
type jVRM1 struct {
	Humanoid struct {
		HumanBones map[string]struct {
			Node int `json:"node"`
		} `json:"humanBones"`
	} `json:"humanoid"`
}

// ── entry points ─────────────────────────────────────────────────────────────

var glbMagic = []byte{'g', 'l', 'T', 'F'}

// Load parses a .vrm/.glb (GLB container) or .gltf (JSON + external/data-URI resources) file.
func Load(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if bytes.HasPrefix(data, glbMagic) {
		return ParseGLB(data, dir)
	}
	return ParseGLTF(data, nil, dir)
}

// ParseGLB splits a GLB container (magic/version/length header, JSON chunk, optional BIN chunk)
// and parses it. baseDir resolves any external URIs the JSON still references.
func ParseGLB(data []byte, baseDir string) (*Document, error) {
	if len(data) < 12 || !bytes.HasPrefix(data, glbMagic) {
		return nil, fmt.Errorf("glb: not a GLB container (bad magic)")
	}
	if v := binary.LittleEndian.Uint32(data[4:]); v != 2 {
		return nil, fmt.Errorf("glb: container version %d unsupported (want 2)", v)
	}
	total := binary.LittleEndian.Uint32(data[8:])
	if int(total) > len(data) {
		return nil, fmt.Errorf("glb: truncated: header says %d bytes, have %d", total, len(data))
	}
	var jsonChunk, binChunk []byte
	for off := 12; off+8 <= int(total); {
		clen := int(binary.LittleEndian.Uint32(data[off:]))
		ctype := binary.LittleEndian.Uint32(data[off+4:])
		off += 8
		if off+clen > int(total) {
			return nil, fmt.Errorf("glb: chunk overruns container")
		}
		switch ctype {
		case 0x4E4F534A: // "JSON"
			jsonChunk = data[off : off+clen]
		case 0x004E4942: // "BIN\0"
			binChunk = data[off : off+clen]
		}
		off += clen
	}
	if jsonChunk == nil {
		return nil, fmt.Errorf("glb: no JSON chunk")
	}
	return ParseGLTF(jsonChunk, binChunk, baseDir)
}

// ParseGLTF parses glTF JSON with an optional GLB BIN chunk; external buffer/image URIs
// resolve relative to baseDir.
func ParseGLTF(jsonBytes, bin []byte, baseDir string) (*Document, error) {
	var jd jDoc
	if err := json.Unmarshal(jsonBytes, &jd); err != nil {
		return nil, fmt.Errorf("gltf: bad JSON: %w", err)
	}
	if !strings.HasPrefix(jd.Asset.Version, "2.") {
		return nil, fmt.Errorf("gltf: asset version %q unsupported (want 2.x)", jd.Asset.Version)
	}
	for _, e := range jd.ExtensionsRequired {
		if !requiredExtOK[e] {
			return nil, fmt.Errorf("gltf: required extension %q unsupported", e)
		}
	}
	p := &parser{jd: &jd, bin: bin, baseDir: baseDir, buffers: make([][]byte, len(jd.Buffers))}
	return p.parse()
}

// ── parser ───────────────────────────────────────────────────────────────────

type parser struct {
	jd      *jDoc
	bin     []byte
	baseDir string
	buffers [][]byte // lazy
}

func (p *parser) parse() (*Document, error) {
	doc := &Document{NodeSlot: map[int]int{}}

	// Nodes tree (+ parents derived from children).
	doc.Nodes = make([]Node, len(p.jd.Nodes))
	for i, jn := range p.jd.Nodes {
		n := Node{Name: jn.Name, Children: jn.Children, Parent: -1, Mesh: -1, Skin: -1}
		if jn.Mesh != nil {
			n.Mesh = *jn.Mesh
		}
		if jn.Skin != nil {
			n.Skin = *jn.Skin
		}
		doc.Nodes[i] = n
	}
	for i := range doc.Nodes {
		for _, c := range doc.Nodes[i].Children {
			if c < 0 || c >= len(doc.Nodes) {
				return nil, fmt.Errorf("gltf: node %d child %d out of range", i, c)
			}
			doc.Nodes[c].Parent = i
		}
	}

	// Skins.
	doc.Skins = make([]Skin, len(p.jd.Skins))
	for i, js := range p.jd.Skins {
		s := Skin{Joints: js.Joints, jointIdx: make(map[int]int, len(js.Joints))}
		for j, n := range js.Joints {
			if n < 0 || n >= len(doc.Nodes) {
				return nil, fmt.Errorf("gltf: skin %d joint node %d out of range", i, n)
			}
			s.jointIdx[n] = j
		}
		if js.InverseBindMatrices != nil {
			ms, err := p.mat4s(*js.InverseBindMatrices)
			if err != nil {
				return nil, fmt.Errorf("gltf: skin %d IBMs: %w", i, err)
			}
			if len(ms) < len(js.Joints) {
				return nil, fmt.Errorf("gltf: skin %d has %d IBMs for %d joints", i, len(ms), len(js.Joints))
			}
			s.IBMs = ms[:len(js.Joints)]
		} else {
			// spec default: identity
			s.IBMs = make([][16]float64, len(js.Joints))
			for j := range s.IBMs {
				s.IBMs[j] = [16]float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}
			}
		}
		doc.Skins[i] = s
	}

	// Meshes.
	doc.Meshes = make([]Mesh, len(p.jd.Meshes))
	for mi, jm := range p.jd.Meshes {
		for pi, jp := range jm.Primitives {
			prim, err := p.primitive(jp)
			if err != nil {
				return nil, fmt.Errorf("gltf: mesh %d primitive %d: %w", mi, pi, err)
			}
			doc.Meshes[mi].Primitives = append(doc.Meshes[mi].Primitives, prim)
		}
	}

	// Materials (decode only images actually referenced as baseColorTexture).
	doc.Materials = make([]Material, len(p.jd.Materials))
	for i, jm := range p.jd.Materials {
		m := Material{BaseColorFactor: [4]float64{1, 1, 1, 1}}
		if jm.PBR != nil {
			for k := 0; k < 4 && k < len(jm.PBR.BaseColorFactor); k++ {
				m.BaseColorFactor[k] = jm.PBR.BaseColorFactor[k]
			}
			if bct := jm.PBR.BaseColorTexture; bct != nil {
				if bct.TexCoord != 0 {
					return nil, fmt.Errorf("gltf: material %d baseColorTexture texCoord %d unsupported (only TEXCOORD_0)", i, bct.TexCoord)
				}
				tex, err := p.texture(bct.Index)
				if err != nil {
					return nil, fmt.Errorf("gltf: material %d: %w", i, err)
				}
				m.Tex = tex
			}
		}
		doc.Materials[i] = m
	}

	if err := p.humanoid(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// humanoid fills NodeSlot from VRM 1.0 (VRMC_vrm) or VRM 0.x (VRM) humanoid maps; 1.0 wins if
// both are present. Names not in the §5 table (fingers/eyes/jaw...) stay unmapped - their
// nodes remap via ancestor walk at sampling.
func (p *parser) humanoid(doc *Document) error {
	if raw, ok := p.jd.Extensions["VRMC_vrm"]; ok {
		var v jVRM1
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("gltf: VRMC_vrm extension: %w", err)
		}
		// Go map iteration is randomized; iterate bone names sorted so spec-violating
		// duplicate node references resolve deterministically (last sorted name wins).
		names := make([]string, 0, len(v.Humanoid.HumanBones))
		for name := range v.Humanoid.HumanBones {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			hb := v.Humanoid.HumanBones[name]
			if slot, ok := SlotByVRMName(name); ok && hb.Node >= 0 && hb.Node < len(doc.Nodes) {
				if _, dup := doc.NodeSlot[hb.Node]; dup {
					doc.HumanoidDupNodes++
				}
				doc.NodeSlot[hb.Node] = slot
			}
		}
		doc.VRMVersion = "1"
		return nil
	}
	if raw, ok := p.jd.Extensions["VRM"]; ok {
		var v jVRM0
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("gltf: VRM extension: %w", err)
		}
		for _, hb := range v.Humanoid.HumanBones {
			if slot, ok := SlotByVRMName(hb.Bone); ok && hb.Node >= 0 && hb.Node < len(doc.Nodes) {
				if _, dup := doc.NodeSlot[hb.Node]; dup {
					doc.HumanoidDupNodes++
				}
				doc.NodeSlot[hb.Node] = slot
			}
		}
		doc.VRMVersion = "0"
		return nil
	}
	return nil // no humanoid map; Sample rejects with a clear error
}

func (p *parser) primitive(jp jPrimitive) (Primitive, error) {
	if jp.Mode != nil && *jp.Mode != modeTriangles {
		return Primitive{}, fmt.Errorf("mode %d unsupported (triangles only)", *jp.Mode)
	}
	prim := Primitive{Mat: -1}
	if jp.Material != nil {
		prim.Mat = *jp.Material
	}
	posIdx, ok := jp.Attributes["POSITION"]
	if !ok {
		return Primitive{}, fmt.Errorf("no POSITION attribute")
	}
	var err error
	if prim.Pos, err = p.vec3f(posIdx); err != nil {
		return Primitive{}, fmt.Errorf("POSITION: %w", err)
	}
	if uvIdx, ok := jp.Attributes["TEXCOORD_0"]; ok {
		if prim.UV, err = p.vec2f(uvIdx); err != nil {
			return Primitive{}, fmt.Errorf("TEXCOORD_0: %w", err)
		}
	}
	if jIdx, ok := jp.Attributes["JOINTS_0"]; ok {
		if prim.Joints, err = p.vec4Joints(jIdx); err != nil {
			return Primitive{}, fmt.Errorf("JOINTS_0: %w", err)
		}
	}
	if wIdx, ok := jp.Attributes["WEIGHTS_0"]; ok {
		if prim.Weights, err = p.vec4Weights(wIdx); err != nil {
			return Primitive{}, fmt.Errorf("WEIGHTS_0: %w", err)
		}
	}
	// spec: all attribute accessors of a primitive have the same count. Indices are only
	// range-checked against POSITION; a shorter attribute would panic at sampling.
	if prim.UV != nil && len(prim.UV) != len(prim.Pos) {
		return Primitive{}, fmt.Errorf("TEXCOORD_0 count %d != POSITION count %d", len(prim.UV), len(prim.Pos))
	}
	if prim.Joints != nil && len(prim.Joints) != len(prim.Pos) {
		return Primitive{}, fmt.Errorf("JOINTS_0 count %d != POSITION count %d", len(prim.Joints), len(prim.Pos))
	}
	if prim.Weights != nil && len(prim.Weights) != len(prim.Pos) {
		return Primitive{}, fmt.Errorf("WEIGHTS_0 count %d != POSITION count %d", len(prim.Weights), len(prim.Pos))
	}
	if jp.Indices != nil {
		if prim.Indices, err = p.scalarIndices(*jp.Indices); err != nil {
			return Primitive{}, fmt.Errorf("indices: %w", err)
		}
	} else {
		prim.Indices = make([]uint32, len(prim.Pos))
		for i := range prim.Indices {
			prim.Indices[i] = uint32(i)
		}
	}
	if len(prim.Indices)%3 != 0 {
		return Primitive{}, fmt.Errorf("index count %d not a multiple of 3", len(prim.Indices))
	}
	for _, ix := range prim.Indices {
		if int(ix) >= len(prim.Pos) {
			return Primitive{}, fmt.Errorf("index %d out of range (%d vertices)", ix, len(prim.Pos))
		}
	}
	return prim, nil
}

// ── buffers / accessors ──────────────────────────────────────────────────────

func (p *parser) buffer(i int) ([]byte, error) {
	if i < 0 || i >= len(p.jd.Buffers) {
		return nil, fmt.Errorf("buffer %d out of range", i)
	}
	if p.buffers[i] != nil {
		return p.buffers[i], nil
	}
	jb := p.jd.Buffers[i]
	var data []byte
	switch {
	case jb.URI == "":
		if p.bin == nil {
			return nil, fmt.Errorf("buffer %d has no URI and no GLB BIN chunk", i)
		}
		data = p.bin
	case strings.HasPrefix(jb.URI, "data:"):
		comma := strings.IndexByte(jb.URI, ',')
		if comma < 0 || !strings.Contains(jb.URI[:comma], "base64") {
			return nil, fmt.Errorf("buffer %d: unsupported data URI", i)
		}
		var err error
		if data, err = base64.StdEncoding.DecodeString(jb.URI[comma+1:]); err != nil {
			return nil, fmt.Errorf("buffer %d: data URI decode: %w", i, err)
		}
	default:
		u, err := url.PathUnescape(jb.URI)
		if err != nil {
			u = jb.URI
		}
		if data, err = os.ReadFile(filepath.Join(p.baseDir, filepath.FromSlash(u))); err != nil {
			return nil, fmt.Errorf("buffer %d: %w", i, err)
		}
	}
	if len(data) < jb.ByteLength {
		return nil, fmt.Errorf("buffer %d: %d bytes, declared %d", i, len(data), jb.ByteLength)
	}
	p.buffers[i] = data
	return data, nil
}

// view returns bufferView bytes + its byteStride (0 = tight).
func (p *parser) view(i int) ([]byte, int, error) {
	if i < 0 || i >= len(p.jd.BufferViews) {
		return nil, 0, fmt.Errorf("bufferView %d out of range", i)
	}
	jv := p.jd.BufferViews[i]
	buf, err := p.buffer(jv.Buffer)
	if err != nil {
		return nil, 0, err
	}
	if jv.ByteOffset < 0 || jv.ByteLength < 0 {
		return nil, 0, fmt.Errorf("bufferView %d: negative byteOffset/byteLength (%d/%d)", i, jv.ByteOffset, jv.ByteLength)
	}
	if jv.ByteOffset+jv.ByteLength > len(buf) {
		return nil, 0, fmt.Errorf("bufferView %d overruns buffer", i)
	}
	stride := 0
	if jv.ByteStride != nil {
		stride = *jv.ByteStride
	}
	return buf[jv.ByteOffset : jv.ByteOffset+jv.ByteLength], stride, nil
}

var typeComps = map[string]int{"SCALAR": 1, "VEC2": 2, "VEC3": 3, "VEC4": 4, "MAT4": 16}

func compSize(ct int) int {
	switch ct {
	case compUByte:
		return 1
	case compUShort:
		return 2
	case compUInt, compFloat:
		return 4
	}
	return 0
}

// accessor resolves accessor i into raw element access: base slice, per-element stride.
func (p *parser) accessor(i, wantComps int, wantTypes ...int) (acc jAccessor, data []byte, stride int, err error) {
	if i < 0 || i >= len(p.jd.Accessors) {
		return acc, nil, 0, fmt.Errorf("accessor %d out of range", i)
	}
	acc = p.jd.Accessors[i]
	if acc.Sparse != nil {
		return acc, nil, 0, fmt.Errorf("accessor %d: sparse unsupported", i)
	}
	if typeComps[acc.Type] != wantComps {
		return acc, nil, 0, fmt.Errorf("accessor %d: type %s (want %d components)", i, acc.Type, wantComps)
	}
	okType := false
	for _, t := range wantTypes {
		if acc.ComponentType == t {
			okType = true
		}
	}
	if !okType {
		return acc, nil, 0, fmt.Errorf("accessor %d: componentType %d unsupported", i, acc.ComponentType)
	}
	if acc.BufferView == nil {
		return acc, nil, 0, fmt.Errorf("accessor %d: no bufferView (zero-filled accessors unsupported)", i)
	}
	view, vstride, err := p.view(*acc.BufferView)
	if err != nil {
		return acc, nil, 0, err
	}
	elem := compSize(acc.ComponentType) * wantComps
	// spec: byteStride is 4..252 and >= element size; 0/absent = tightly packed. A stride
	// below elem (incl. negative) would defeat the overrun check and panic element reads.
	if vstride != 0 && vstride < elem {
		return acc, nil, 0, fmt.Errorf("accessor %d: bufferView byteStride %d < element size %d", i, vstride, elem)
	}
	stride = vstride
	if stride == 0 {
		stride = elem
	}
	if acc.ByteOffset < 0 || acc.Count < 0 {
		return acc, nil, 0, fmt.Errorf("accessor %d: negative offset/count", i)
	}
	need := acc.ByteOffset + (acc.Count-1)*stride + elem
	if acc.Count == 0 {
		need = acc.ByteOffset
	}
	if need > len(view) {
		return acc, nil, 0, fmt.Errorf("accessor %d overruns bufferView (%d > %d)", i, need, len(view))
	}
	return acc, view[acc.ByteOffset:], stride, nil
}

func f32At(b []byte) float64 { return float64(math.Float32frombits(binary.LittleEndian.Uint32(b))) }

func (p *parser) vec3f(i int) ([][3]float64, error) {
	acc, data, stride, err := p.accessor(i, 3, compFloat)
	if err != nil {
		return nil, err
	}
	out := make([][3]float64, acc.Count)
	for e := 0; e < acc.Count; e++ {
		b := data[e*stride:]
		out[e] = [3]float64{f32At(b), f32At(b[4:]), f32At(b[8:])}
	}
	return out, nil
}

func (p *parser) vec2f(i int) ([][2]float64, error) {
	acc, data, stride, err := p.accessor(i, 2, compFloat)
	if err != nil {
		return nil, err
	}
	out := make([][2]float64, acc.Count)
	for e := 0; e < acc.Count; e++ {
		b := data[e*stride:]
		out[e] = [2]float64{f32At(b), f32At(b[4:])}
	}
	return out, nil
}

func (p *parser) vec4Joints(i int) ([][4]int, error) {
	acc, data, stride, err := p.accessor(i, 4, compUByte, compUShort)
	if err != nil {
		return nil, err
	}
	out := make([][4]int, acc.Count)
	for e := 0; e < acc.Count; e++ {
		b := data[e*stride:]
		for k := 0; k < 4; k++ {
			if acc.ComponentType == compUByte {
				out[e][k] = int(b[k])
			} else {
				out[e][k] = int(binary.LittleEndian.Uint16(b[k*2:]))
			}
		}
	}
	return out, nil
}

func (p *parser) vec4Weights(i int) ([][4]float64, error) {
	acc, data, stride, err := p.accessor(i, 4, compFloat, compUByte, compUShort)
	if err != nil {
		return nil, err
	}
	if acc.ComponentType != compFloat && !acc.Normalized {
		return nil, fmt.Errorf("accessor %d: integer WEIGHTS_0 must be normalized", i)
	}
	out := make([][4]float64, acc.Count)
	for e := 0; e < acc.Count; e++ {
		b := data[e*stride:]
		for k := 0; k < 4; k++ {
			switch acc.ComponentType {
			case compFloat:
				out[e][k] = f32At(b[k*4:])
			case compUByte:
				out[e][k] = float64(b[k]) / 255
			case compUShort:
				out[e][k] = float64(binary.LittleEndian.Uint16(b[k*2:])) / 65535
			}
		}
	}
	return out, nil
}

func (p *parser) scalarIndices(i int) ([]uint32, error) {
	acc, data, stride, err := p.accessor(i, 1, compUByte, compUShort, compUInt)
	if err != nil {
		return nil, err
	}
	out := make([]uint32, acc.Count)
	for e := 0; e < acc.Count; e++ {
		b := data[e*stride:]
		switch acc.ComponentType {
		case compUByte:
			out[e] = uint32(b[0])
		case compUShort:
			out[e] = uint32(binary.LittleEndian.Uint16(b))
		case compUInt:
			out[e] = binary.LittleEndian.Uint32(b)
		}
	}
	return out, nil
}

func (p *parser) mat4s(i int) ([][16]float64, error) {
	acc, data, stride, err := p.accessor(i, 16, compFloat)
	if err != nil {
		return nil, err
	}
	out := make([][16]float64, acc.Count)
	for e := 0; e < acc.Count; e++ {
		b := data[e*stride:]
		for k := 0; k < 16; k++ {
			out[e][k] = f32At(b[k*4:])
		}
	}
	return out, nil
}

// ── images / textures ────────────────────────────────────────────────────────

func (p *parser) texture(i int) (*Texture, error) {
	if i < 0 || i >= len(p.jd.Textures) {
		return nil, fmt.Errorf("texture %d out of range", i)
	}
	jt := p.jd.Textures[i]
	if jt.Source == nil {
		return nil, fmt.Errorf("texture %d has no source image", i)
	}
	img, err := p.image(*jt.Source)
	if err != nil {
		return nil, err
	}
	tex := &Texture{Img: img, WrapS: WrapRepeat, WrapT: WrapRepeat}
	if jt.Sampler != nil && *jt.Sampler >= 0 && *jt.Sampler < len(p.jd.Samplers) {
		js := p.jd.Samplers[*jt.Sampler]
		if js.WrapS != nil {
			tex.WrapS = *js.WrapS
		}
		if js.WrapT != nil {
			tex.WrapT = *js.WrapT
		}
	}
	return tex, nil
}

func (p *parser) image(i int) (image.Image, error) {
	if i < 0 || i >= len(p.jd.Images) {
		return nil, fmt.Errorf("image %d out of range", i)
	}
	ji := p.jd.Images[i]
	var raw []byte
	switch {
	case ji.BufferView != nil:
		view, stride, err := p.view(*ji.BufferView)
		if err != nil {
			return nil, fmt.Errorf("image %d: %w", i, err)
		}
		if stride != 0 {
			return nil, fmt.Errorf("image %d: strided bufferView unsupported", i)
		}
		raw = view
	case strings.HasPrefix(ji.URI, "data:"):
		comma := strings.IndexByte(ji.URI, ',')
		if comma < 0 || !strings.Contains(ji.URI[:comma], "base64") {
			return nil, fmt.Errorf("image %d: unsupported data URI", i)
		}
		var err error
		if raw, err = base64.StdEncoding.DecodeString(ji.URI[comma+1:]); err != nil {
			return nil, fmt.Errorf("image %d: data URI decode: %w", i, err)
		}
	case ji.URI != "":
		u, err := url.PathUnescape(ji.URI)
		if err != nil {
			u = ji.URI
		}
		if raw, err = os.ReadFile(filepath.Join(p.baseDir, filepath.FromSlash(u))); err != nil {
			return nil, fmt.Errorf("image %d: %w", i, err)
		}
	default:
		return nil, fmt.Errorf("image %d has neither bufferView nor URI", i)
	}
	img, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("image %d (%s): decode: %w (png/jpeg only)", i, ji.MimeType, err)
	}
	_ = format
	return img, nil
}
