package vrm

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Minimal glTF 2.0 / GLB reader scoped to what VRM exporters emit: a single binary buffer (GLB
// BIN chunk) or base64 data-URI buffer, non-sparse float/int accessors, triangle primitives. No
// external-file buffers, no sparse accessors, no draco - those return an error (a preview model
// should be self-contained). Pure stdlib.

// glTF componentType codes.
const (
	cByte   = 5120
	cUByte  = 5121
	cShort  = 5122
	cUShort = 5123
	cUInt   = 5125
	cFloat  = 5126
)

type gltfDoc struct {
	Asset       struct{ Version string }   `json:"asset"`
	Nodes       []gltfNode                 `json:"nodes"`
	Meshes      []gltfMesh                 `json:"meshes"`
	Skins       []gltfSkin                 `json:"skins"`
	Accessors   []gltfAccessor             `json:"accessors"`
	BufferViews []gltfBufferView           `json:"bufferViews"`
	Buffers     []gltfBuffer               `json:"buffers"`
	Extensions  map[string]json.RawMessage `json:"extensions"`
}

type gltfNode struct {
	Name        string    `json:"name"`
	Children    []int     `json:"children"`
	Matrix      []float64 `json:"matrix"`
	Translation []float64 `json:"translation"`
	Rotation    []float64 `json:"rotation"`
	Scale       []float64 `json:"scale"`
	Mesh        *int      `json:"mesh"`
	Skin        *int      `json:"skin"`
}

type gltfMesh struct {
	Name       string          `json:"name"`
	Primitives []gltfPrimitive `json:"primitives"`
}

type gltfPrimitive struct {
	Attributes map[string]int `json:"attributes"`
	Indices    *int           `json:"indices"`
	Mode       *int           `json:"mode"`
}

type gltfSkin struct {
	InverseBindMatrices *int  `json:"inverseBindMatrices"`
	Joints              []int `json:"joints"`
	Skeleton            *int  `json:"skeleton"`
}

type gltfAccessor struct {
	BufferView    *int            `json:"bufferView"`
	ByteOffset    int             `json:"byteOffset"`
	ComponentType int             `json:"componentType"`
	Count         int             `json:"count"`
	Type          string          `json:"type"`
	Normalized    bool            `json:"normalized"`
	Sparse        json.RawMessage `json:"sparse"`
}

type gltfBufferView struct {
	Buffer     int  `json:"buffer"`
	ByteOffset int  `json:"byteOffset"`
	ByteLength int  `json:"byteLength"`
	ByteStride *int `json:"byteStride"`
}

type gltfBuffer struct {
	URI        string `json:"uri"`
	ByteLength int    `json:"byteLength"`
}

// parseGLB splits a .glb container into its JSON doc + binary buffer. Falls back to a plain .gltf
// JSON (with an embedded base64 buffer) when data isn't a GLB.
func parseContainer(data []byte) (*gltfDoc, []byte, error) {
	if len(data) >= 12 && binary.LittleEndian.Uint32(data) == 0x46546C67 { // "glTF"
		return parseGLB(data)
	}
	var doc gltfDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("not GLB and not glTF JSON: %w", err)
	}
	bin, err := embeddedBuffer(doc)
	return &doc, bin, err
}

func parseGLB(data []byte) (*gltfDoc, []byte, error) {
	if len(data) < 12 {
		return nil, nil, fmt.Errorf("glb too short")
	}
	if binary.LittleEndian.Uint32(data[4:]) != 2 {
		return nil, nil, fmt.Errorf("glb version != 2")
	}
	var jsonChunk, binChunk []byte
	off := 12
	for off+8 <= len(data) {
		clen := int(binary.LittleEndian.Uint32(data[off:]))
		ctype := binary.LittleEndian.Uint32(data[off+4:])
		start := off + 8
		if start+clen > len(data) {
			return nil, nil, fmt.Errorf("glb chunk overruns file")
		}
		switch ctype {
		case 0x4E4F534A: // "JSON"
			jsonChunk = data[start : start+clen]
		case 0x004E4942: // "BIN\0"
			binChunk = data[start : start+clen]
		}
		off = start + clen
	}
	if jsonChunk == nil {
		return nil, nil, fmt.Errorf("glb missing JSON chunk")
	}
	var doc gltfDoc
	if err := json.Unmarshal(jsonChunk, &doc); err != nil {
		return nil, nil, fmt.Errorf("glb json: %w", err)
	}
	return &doc, binChunk, nil
}

// embeddedBuffer returns the first buffer's bytes when it's a base64 data URI.
func embeddedBuffer(doc gltfDoc) ([]byte, error) {
	if len(doc.Buffers) == 0 {
		return nil, nil
	}
	uri := doc.Buffers[0].URI
	if i := strings.Index(uri, ";base64,"); i >= 0 {
		return base64.StdEncoding.DecodeString(uri[i+len(";base64,"):])
	}
	if uri == "" {
		return nil, nil // GLB-style buffer with no URI but no BIN chunk
	}
	return nil, fmt.Errorf("external buffer URIs not supported (self-contained VRM/GLB only)")
}

// accessorData returns the raw bytes for accessor i (respecting bufferView offset/length).
func (d *gltfDoc) accessorBytes(bin []byte, a gltfAccessor) ([]byte, int, error) {
	if a.Sparse != nil {
		return nil, 0, fmt.Errorf("sparse accessors unsupported")
	}
	if a.BufferView == nil {
		return nil, 0, fmt.Errorf("accessor without bufferView unsupported")
	}
	bv := d.BufferViews[*a.BufferView]
	stride := componentSize(a.ComponentType) * typeComponents(a.Type)
	if bv.ByteStride != nil && *bv.ByteStride != 0 {
		stride = *bv.ByteStride
	}
	start := bv.ByteOffset + a.ByteOffset
	if start < 0 || start > len(bin) {
		return nil, 0, fmt.Errorf("accessor offset out of range")
	}
	return bin[start:], stride, nil
}

// readFloats decodes accessor a as []float32 with comp components each (e.g. VEC3 → comp=3).
func (d *gltfDoc) readFloats(bin []byte, ai, comp int) ([]float32, error) {
	a := d.Accessors[ai]
	if typeComponents(a.Type) != comp {
		return nil, fmt.Errorf("accessor %d type %s != %d comps", ai, a.Type, comp)
	}
	raw, stride, err := d.accessorBytes(bin, a)
	if err != nil {
		return nil, err
	}
	out := make([]float32, a.Count*comp)
	cs := componentSize(a.ComponentType)
	for e := range a.Count {
		base := e * stride
		for c := range comp {
			p := base + c*cs
			if p+cs > len(raw) {
				return nil, fmt.Errorf("accessor %d read overrun", ai)
			}
			out[e*comp+c] = readComponentFloat(raw[p:], a.ComponentType, a.Normalized)
		}
	}
	return out, nil
}

// readInts decodes accessor a as []uint32 with comp components (JOINTS_0 / indices).
func (d *gltfDoc) readInts(bin []byte, ai, comp int) ([]uint32, error) {
	a := d.Accessors[ai]
	if typeComponents(a.Type) != comp {
		return nil, fmt.Errorf("accessor %d type %s != %d comps", ai, a.Type, comp)
	}
	raw, stride, err := d.accessorBytes(bin, a)
	if err != nil {
		return nil, err
	}
	out := make([]uint32, a.Count*comp)
	cs := componentSize(a.ComponentType)
	for e := range a.Count {
		base := e * stride
		for c := range comp {
			p := base + c*cs
			if p+cs > len(raw) {
				return nil, fmt.Errorf("accessor %d read overrun", ai)
			}
			out[e*comp+c] = readComponentUint(raw[p:], a.ComponentType)
		}
	}
	return out, nil
}

// readMat4s decodes a MAT4 accessor (inverse bind matrices).
func (d *gltfDoc) readMat4s(bin []byte, ai int) ([]Mat4, error) {
	f, err := d.readFloats(bin, ai, 16)
	if err != nil {
		return nil, err
	}
	out := make([]Mat4, len(f)/16)
	for i := range out {
		copy(out[i][:], f[i*16:i*16+16])
	}
	return out, nil
}

func readComponentFloat(b []byte, ct int, normalized bool) float32 {
	switch ct {
	case cFloat:
		return math.Float32frombits(binary.LittleEndian.Uint32(b))
	case cUByte:
		v := float32(b[0])
		if normalized {
			return v / 255
		}
		return v
	case cUShort:
		v := float32(binary.LittleEndian.Uint16(b))
		if normalized {
			return v / 65535
		}
		return v
	case cByte:
		v := float32(int8(b[0]))
		if normalized {
			return float32(math.Max(float64(v)/127, -1))
		}
		return v
	case cShort:
		v := float32(int16(binary.LittleEndian.Uint16(b)))
		if normalized {
			return float32(math.Max(float64(v)/32767, -1))
		}
		return v
	}
	return 0
}

func readComponentUint(b []byte, ct int) uint32 {
	switch ct {
	case cUByte, cByte:
		return uint32(b[0])
	case cUShort, cShort:
		return uint32(binary.LittleEndian.Uint16(b))
	case cUInt:
		return binary.LittleEndian.Uint32(b)
	}
	return 0
}

func componentSize(ct int) int {
	switch ct {
	case cByte, cUByte:
		return 1
	case cShort, cUShort:
		return 2
	case cUInt, cFloat:
		return 4
	}
	return 0
}

func typeComponents(t string) int {
	switch t {
	case "SCALAR":
		return 1
	case "VEC2":
		return 2
	case "VEC3":
		return 3
	case "VEC4", "MAT2":
		return 4
	case "MAT3":
		return 9
	case "MAT4":
		return 16
	}
	return 0
}
