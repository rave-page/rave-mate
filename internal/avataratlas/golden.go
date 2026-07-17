package avataratlas

// golden.go - the FROZEN golden test vector (contract §11): a synthetic 2-bone rig built in
// code (bone0 hips at origin, bone1 spine at +0.3m, two textured quads), sampled with seed 1
// into 64 points on performer slot 0. Emitted as golden_atlas_slot0.png + .json sidecar,
// checked into testdata/ here AND Packages/page.rave.puppets/Tests~/golden/ in the world repo.
// Go decoder and world reader must reproduce it field-exact. Nothing here may change without a
// contract version bump; the rig is deterministic end to end (fixed geometry, fixed 4x4
// texture, seeded PRNG, no time source).

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
)

// Golden parameters (frozen).
const (
	GoldenSeed   = 1
	GoldenPoints = 64
	GoldenSlot   = 0
	GoldenPNG    = "golden_atlas_slot0.png"
	GoldenJSON   = "golden_atlas_slot0.json"
)

// goldenTexture is the fixed 4x4 sRGB base colour texture: px(x,y) = (x*64+32, y*64+32,
// (x+y)*32+16, 255).
func goldenTexture() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			o := img.PixOffset(x, y)
			img.Pix[o] = uint8(x*64 + 32)
			img.Pix[o+1] = uint8(y*64 + 32)
			img.Pix[o+2] = uint8((x+y)*32 + 16)
			img.Pix[o+3] = 255
		}
	}
	return img
}

// GoldenGLB assembles the frozen 2-bone rig as a VRM 0.x GLB, exercising the same parse path
// as a real avatar: hips quad (y 0..0.3, joint 0) + spine quad (y 0.3..0.6, joint 1), IBM1 =
// translate(0,-0.3,0), clamp-wrapped 4x4 baseColorTexture, default baseColorFactor.
func GoldenGLB() ([]byte, error) {
	var bin bytes.Buffer
	le := binary.LittleEndian
	putF32 := func(vs ...float64) {
		for _, v := range vs {
			var b [4]byte
			le.PutUint32(b[:], math.Float32bits(float32(v)))
			bin.Write(b[:])
		}
	}
	align := func() {
		for bin.Len()%4 != 0 {
			bin.WriteByte(0)
		}
	}
	type bv struct{ off, len int }
	var views []bv
	view := func(start int) {
		views = append(views, bv{start, bin.Len() - start})
	}

	// positions (VEC3 float x8)
	start := bin.Len()
	quad := func(yLo, yHi float64) {
		putF32(-0.25, yLo, 0, 0.25, yLo, 0, 0.25, yHi, 0, -0.25, yHi, 0)
	}
	quad(0, 0.3)
	quad(0.3, 0.6)
	view(start)

	// uvs (VEC2 float x8): hips quad V 1..0.5, spine quad V 0.5..0
	start = bin.Len()
	putF32(0, 1, 1, 1, 1, 0.5, 0, 0.5)
	putF32(0, 0.5, 1, 0.5, 1, 0, 0, 0)
	view(start)

	// joints (VEC4 ubyte x8)
	start = bin.Len()
	for v := 0; v < 8; v++ {
		j := byte(0)
		if v >= 4 {
			j = 1
		}
		bin.Write([]byte{j, 0, 0, 0})
	}
	view(start)

	// weights (VEC4 float x8)
	start = bin.Len()
	for v := 0; v < 8; v++ {
		putF32(1, 0, 0, 0)
	}
	view(start)

	// indices (SCALAR ushort x12)
	start = bin.Len()
	for _, ix := range []uint16{0, 1, 2, 0, 2, 3, 4, 5, 6, 4, 6, 7} {
		var b [2]byte
		le.PutUint16(b[:], ix)
		bin.Write(b[:])
	}
	align()
	view(start)

	// IBMs (MAT4 float x2): identity; translate(0,-0.3,0)
	start = bin.Len()
	putF32(1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1)
	putF32(1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, -0.3, 0, 1)
	view(start)

	// texture PNG
	start = bin.Len()
	if err := png.Encode(&bin, goldenTexture()); err != nil {
		return nil, err
	}
	align()
	view(start)

	jviews := make([]map[string]any, len(views))
	for i, v := range views {
		jviews[i] = map[string]any{"buffer": 0, "byteOffset": v.off, "byteLength": v.len}
	}
	doc := map[string]any{
		"asset": map[string]any{"version": "2.0", "generator": "avataratlas golden"},
		"nodes": []map[string]any{
			{"name": "hips", "children": []int{1}},
			{"name": "spine"},
			{"name": "body", "mesh": 0, "skin": 0},
		},
		"skins": []map[string]any{{"joints": []int{0, 1}, "inverseBindMatrices": 5}},
		"meshes": []map[string]any{{"primitives": []map[string]any{{
			"attributes": map[string]int{"POSITION": 0, "TEXCOORD_0": 1, "JOINTS_0": 2, "WEIGHTS_0": 3},
			"indices":    4, "material": 0, "mode": 4,
		}}}},
		"materials": []map[string]any{{"pbrMetallicRoughness": map[string]any{
			"baseColorTexture": map[string]any{"index": 0},
		}}},
		"textures": []map[string]any{{"sampler": 0, "source": 0}},
		"samplers": []map[string]any{{"wrapS": WrapClamp, "wrapT": WrapClamp}},
		"images":   []map[string]any{{"bufferView": 6, "mimeType": "image/png"}},
		"accessors": []map[string]any{
			{"bufferView": 0, "componentType": compFloat, "count": 8, "type": "VEC3"},
			{"bufferView": 1, "componentType": compFloat, "count": 8, "type": "VEC2"},
			{"bufferView": 2, "componentType": compUByte, "count": 8, "type": "VEC4"},
			{"bufferView": 3, "componentType": compFloat, "count": 8, "type": "VEC4"},
			{"bufferView": 4, "componentType": compUShort, "count": 12, "type": "SCALAR"},
			{"bufferView": 5, "componentType": compFloat, "count": 2, "type": "MAT4"},
		},
		"bufferViews": jviews,
		"buffers":     []map[string]any{{"byteLength": bin.Len()}},
		"extensions": map[string]any{"VRM": map[string]any{
			"exporterVersion": "avataratlas-golden",
			"humanoid": map[string]any{"humanBones": []map[string]any{
				{"bone": "hips", "node": 0},
				{"bone": "spine", "node": 1},
			}},
		}},
	}
	jsonBytes, err := json.Marshal(doc)
	if err != nil {
		return nil, err
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
	w32(0x4E4F534A) // "JSON"
	glb.Write(jsonBytes)
	w32(uint32(bin.Len()))
	w32(0x004E4942) // "BIN\0"
	glb.Write(bin.Bytes())
	return glb.Bytes(), nil
}

// GoldenAtlas runs the full frozen pipeline: GLB -> parse -> sample(64, seed 1) -> atlas slot 0.
func GoldenAtlas() (*Atlas, *SampleResult, error) {
	glb, err := GoldenGLB()
	if err != nil {
		return nil, nil, err
	}
	doc, err := ParseGLB(glb, "")
	if err != nil {
		return nil, nil, err
	}
	res, err := Sample(doc, GoldenPoints, GoldenSeed)
	if err != nil {
		return nil, nil, err
	}
	if res.Dropped != 0 {
		return nil, nil, fmt.Errorf("golden: %d dropped points (want 0)", res.Dropped)
	}
	atlas, err := BuildAtlas(res.Points, GoldenSlot)
	if err != nil {
		return nil, nil, err
	}
	return atlas, res, nil
}

// Sidecar mirrors the golden JSON layout: header fields + all points pre-quantization
// (bone-local metres) + expected post-quantization wire bytes.
type Sidecar struct {
	Contract string         `json:"contract"`
	Seed     int64          `json:"seed"`
	Points   int            `json:"points"`
	Dropped  int            `json:"dropped"`
	Header   SidecarHeader  `json:"header"`
	Boxes    []SidecarBox   `json:"boxes"`
	Samples  []SidecarPoint `json:"samples"`
}

type SidecarHeader struct {
	Version    int `json:"version"`
	Flags      int `json:"flags"`
	SlotIndex  int `json:"slotIndex"`
	BoneCount  int `json:"boneCount"`
	PointCount int `json:"pointCount"`
	Width      int `json:"width"`
	Height     int `json:"height"`
}

type SidecarBox struct {
	Slot   int    `json:"slot"`
	Name   string `json:"name"`
	MinMm  [3]int `json:"minMm"`
	SizeMm [3]int `json:"sizeMm"`
}

type SidecarPoint struct {
	Slot   int        `json:"slot"`
	Pos    [3]float64 `json:"pos"` // pre-quantization bone-local metres
	Q      [3]uint16  `json:"q"`
	RGB    [3]uint8   `json:"rgb"`
	Weight uint8      `json:"weight"`
	Px     string     `json:"px"` // the point's 3 wire pixels, 12 bytes hex
}

// GoldenSidecar renders the sidecar JSON for an atlas + its pre-quantization samples.
func GoldenSidecar(a *Atlas, res *SampleResult) ([]byte, error) {
	sc := Sidecar{
		Contract: "MOCAP_PANEL_CONTRACT v1.3 §11 RPA1",
		Seed:     GoldenSeed,
		Points:   res.Requested,
		Dropped:  res.Dropped,
		Header: SidecarHeader{
			Version: a.Version, Flags: a.Flags, SlotIndex: a.SlotIndex, BoneCount: a.BoneCount,
			PointCount: len(a.Points), Width: Width, Height: AtlasHeight(len(a.Points)),
		},
	}
	for slot := 0; slot < BoneSlots; slot++ {
		if !a.Boxes[slot].Used() {
			continue
		}
		b := SidecarBox{Slot: slot, Name: SlotName(slot)}
		for ax := 0; ax < 3; ax++ {
			b.MinMm[ax] = int(a.Boxes[slot].Min[ax])
			b.SizeMm[ax] = int(a.Boxes[slot].Size[ax])
		}
		sc.Boxes = append(sc.Boxes, b)
	}
	if len(res.Points) != len(a.Points) {
		return nil, fmt.Errorf("golden: sample/atlas point count mismatch %d/%d", len(res.Points), len(a.Points))
	}
	for i, s := range res.Points {
		p := a.Points[i]
		wire := [12]uint8{
			uint8(p.Q[0] >> 8), uint8(p.Q[0]), uint8(p.Q[1] >> 8), uint8(p.Q[1]),
			uint8(p.Q[2] >> 8), uint8(p.Q[2]), p.Slot, p.Weight,
			p.RGB[0], p.RGB[1], p.RGB[2], 255,
		}
		sc.Samples = append(sc.Samples, SidecarPoint{
			Slot: s.Slot, Pos: s.Local, Q: p.Q, RGB: p.RGB, Weight: p.Weight,
			Px: fmt.Sprintf("%x", wire),
		})
	}
	return json.MarshalIndent(sc, "", " ")
}

// WriteGolden regenerates the frozen golden pair into dir.
func WriteGolden(dir string) error {
	atlas, res, err := GoldenAtlas()
	if err != nil {
		return err
	}
	var pngBuf bytes.Buffer
	if err := atlas.EncodePNG(&pngBuf); err != nil {
		return err
	}
	sidecar, err := GoldenSidecar(atlas, res)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, GoldenPNG), pngBuf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, GoldenJSON), append(sidecar, '\n'), 0o644)
}
