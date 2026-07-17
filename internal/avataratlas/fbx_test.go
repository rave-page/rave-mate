package avataratlas

// fbx_test.go - synthetic binary-FBX writer (we control both sides) + parser/pipeline tests.
// The rig mirrors the golden 2-bone quad pair (hips quad y 0..30 raw cm, spine quad y 30..60,
// UnitScaleFactor 1 -> metres = raw/100) plus an empty-cluster Head bone so MapHumanoid's
// core check passes. Knobs cover: record format 7400 vs 7500, zlib arrays, UV mapping/
// reference variants, ngon fan-triangulation, embedded vs external texture, unit scaling,
// Blender-style cluster matrices, malformed-input crafting.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ── writer ───────────────────────────────────────────────────────────────────

type tnode struct {
	name  string
	props []any // int64->'L', int32->'I', float64->'D', string->'S', []byte->'R', []int32->'i', []float64->'d', rawProp verbatim
	kids  []*tnode
}

// rawProp injects crafted property bytes verbatim (malformed-input tests).
type rawProp []byte

func tn(name string, props ...any) *tnode { return &tnode{name: name, props: props} }

func (n *tnode) add(kids ...*tnode) *tnode {
	n.kids = append(n.kids, kids...)
	return n
}

func serTestProps(t *testing.T, props []any, zl bool) []byte {
	t.Helper()
	le := binary.LittleEndian
	var b bytes.Buffer
	w32 := func(v uint32) {
		var x [4]byte
		le.PutUint32(x[:], v)
		b.Write(x[:])
	}
	array := func(code byte, payload []byte, count int) {
		b.WriteByte(code)
		if zl {
			var comp bytes.Buffer
			zw := zlib.NewWriter(&comp)
			zw.Write(payload)
			zw.Close()
			w32(uint32(count))
			w32(1)
			w32(uint32(comp.Len()))
			b.Write(comp.Bytes())
			return
		}
		w32(uint32(count))
		w32(0)
		w32(uint32(len(payload)))
		b.Write(payload)
	}
	for _, p := range props {
		switch v := p.(type) {
		case int64:
			b.WriteByte('L')
			var x [8]byte
			le.PutUint64(x[:], uint64(v))
			b.Write(x[:])
		case int32:
			b.WriteByte('I')
			w32(uint32(v))
		case float64:
			b.WriteByte('D')
			var x [8]byte
			le.PutUint64(x[:], math.Float64bits(v))
			b.Write(x[:])
		case string:
			b.WriteByte('S')
			w32(uint32(len(v)))
			b.WriteString(v)
		case []byte:
			b.WriteByte('R')
			w32(uint32(len(v)))
			b.Write(v)
		case []int32:
			payload := make([]byte, 4*len(v))
			for i, e := range v {
				le.PutUint32(payload[i*4:], uint32(e))
			}
			array('i', payload, len(v))
		case []float64:
			payload := make([]byte, 8*len(v))
			for i, e := range v {
				le.PutUint64(payload[i*8:], math.Float64bits(e))
			}
			array('d', payload, len(v))
		case rawProp:
			b.Write(v)
		default:
			t.Fatalf("unsupported test prop %T", p)
		}
	}
	return b.Bytes()
}

func serTestNode(t *testing.T, n *tnode, off int, big, zl bool) []byte {
	t.Helper()
	hdr := nullRecLen(big)
	props := serTestProps(t, n.props, zl)
	inner := off + hdr + len(n.name) + len(props)
	var kids []byte
	if len(n.kids) > 0 {
		for _, k := range n.kids {
			kids = append(kids, serTestNode(t, k, inner+len(kids), big, zl)...)
		}
		kids = append(kids, make([]byte, hdr)...) // null terminator
	}
	end := inner + len(kids)
	le := binary.LittleEndian
	var b bytes.Buffer
	if big {
		var x [8]byte
		for _, v := range []uint64{uint64(end), uint64(len(n.props)), uint64(len(props))} {
			le.PutUint64(x[:], v)
			b.Write(x[:])
		}
	} else {
		var x [4]byte
		for _, v := range []uint32{uint32(end), uint32(len(n.props)), uint32(len(props))} {
			le.PutUint32(x[:], v)
			b.Write(x[:])
		}
	}
	b.WriteByte(byte(len(n.name)))
	b.WriteString(n.name)
	b.Write(props)
	b.Write(kids)
	return b.Bytes()
}

func writeTestFBX(t *testing.T, version uint32, zl bool, roots []*tnode) []byte {
	t.Helper()
	big := version >= 7500
	var b bytes.Buffer
	b.WriteString(fbxMagic)
	b.Write([]byte{0x1A, 0x00})
	var x [4]byte
	binary.LittleEndian.PutUint32(x[:], version)
	b.Write(x[:])
	for _, r := range roots {
		b.Write(serTestNode(t, r, b.Len(), big, zl))
	}
	b.Write(make([]byte, nullRecLen(big))) // top-level terminator
	return b.Bytes()
}

// ── rig builder ──────────────────────────────────────────────────────────────

type fbxRigOpts struct {
	version      uint32 // 0 = 7400
	zlib         bool
	uvByControl  bool // ByControlPoint/Direct (default ByPolygonVertex/IndexToDirect)
	uvDirect     bool // ByPolygonVertex/Direct
	uvConst      bool // constant UV per quad (texel-exact colour assertions)
	ngon         bool // quads as 4-gons with negative terminator (default pre-triangulated)
	extTexture   bool // external tex.png (default embedded Video Content)
	noTexture    bool
	unitFactor   float64 // 0 = 1 (raw cm)
	blenderStyle bool    // cluster Transform = TransformLink^-1 * meshGlobal
	badCluster   bool    // hips cluster bone connection -> nonexistent id
	connCycle    bool    // hips <-> spine parent cycle
}

// object ids
const (
	idMesh      = int64(100)
	idGeom      = int64(200)
	idMat       = int64(300)
	idTex       = int64(400)
	idVideo     = int64(500)
	idSkin      = int64(600)
	idClusterH  = int64(610)
	idClusterS  = int64(620)
	idClusterHd = int64(630)
	idHips      = int64(700)
	idSpine     = int64(710)
	idHead      = int64(720)
)

func translate16(x, y, z float64) []float64 {
	return []float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, x, y, z, 1}
}

// buildFBXRig serializes the synthetic rig; when o.extTexture, dir receives tex.png.
func buildFBXRig(t *testing.T, o fbxRigOpts, dir string) []byte {
	t.Helper()
	version := o.version
	if version == 0 {
		version = 7400
	}
	factor := o.unitFactor
	if factor == 0 {
		factor = 1
	}

	// Geometry: two stacked quads in raw cm (metres = raw*factor/100).
	verts := []float64{
		-25, 0, 0, 25, 0, 0, 25, 30, 0, -25, 30, 0,
		-25, 30, 0, 25, 30, 0, 25, 60, 0, -25, 60, 0,
	}
	var pvi []int32
	if o.ngon {
		pvi = []int32{0, 1, 2, ^int32(3), 4, 5, 6, ^int32(7)}
	} else {
		pvi = []int32{0, 1, ^int32(2), 0, 2, ^int32(3), 4, 5, ^int32(6), 4, 6, ^int32(7)}
	}

	// Per-control-point UVs, FBX bottom-left origin (the parser flips V).
	cpUV := [][2]float64{
		{0, 0}, {1, 0}, {1, 0.5}, {0, 0.5},
		{0, 0.5}, {1, 0.5}, {1, 1}, {0, 1},
	}
	if o.uvConst {
		for i := 0; i < 4; i++ {
			cpUV[i] = [2]float64{0.125, 0.875}   // flip -> v 0.125 -> texel (0,0)
			cpUV[i+4] = [2]float64{0.625, 0.375} // flip -> v 0.625 -> texel (2,2)
		}
	}

	uvLayer := tn("LayerElementUV", int32(0))
	uvLayer.add(tn("Version", int32(101)), tn("Name", "UVMap"))
	switch {
	case o.uvByControl:
		flat := make([]float64, 0, 16)
		for _, uv := range cpUV {
			flat = append(flat, uv[0], uv[1])
		}
		uvLayer.add(
			tn("MappingInformationType", "ByControlPoint"),
			tn("ReferenceInformationType", "Direct"),
			tn("UV", flat))
	case o.uvDirect:
		var flat []float64
		for _, ix := range pvi {
			cp := ix
			if ix < 0 {
				cp = ^ix
			}
			flat = append(flat, cpUV[cp][0], cpUV[cp][1])
		}
		uvLayer.add(
			tn("MappingInformationType", "ByPolygonVertex"),
			tn("ReferenceInformationType", "Direct"),
			tn("UV", flat))
	default: // ByPolygonVertex + IndexToDirect
		flat := make([]float64, 0, 16)
		for _, uv := range cpUV {
			flat = append(flat, uv[0], uv[1])
		}
		idx := make([]int32, len(pvi))
		for i, ix := range pvi {
			cp := ix
			if ix < 0 {
				cp = ^ix
			}
			idx[i] = cp
		}
		uvLayer.add(
			tn("MappingInformationType", "ByPolygonVertex"),
			tn("ReferenceInformationType", "IndexToDirect"),
			tn("UV", flat),
			tn("UVIndex", idx))
	}

	geom := tn("Geometry", idGeom, "quads\x00\x01Geometry", "Mesh").add(
		tn("Vertices", verts),
		tn("PolygonVertexIndex", pvi),
		uvLayer,
		tn("LayerElementMaterial", int32(0)).add(
			tn("MappingInformationType", "AllSame"),
			tn("ReferenceInformationType", "IndexToDirect"),
			tn("Materials", []int32{0})))

	// Cluster matrices: SDK-style (Transform = mesh global = I) or Blender-style
	// (Transform = TransformLink^-1 * meshGlobal, already bone-relative).
	tlHips := translate16(0, 0, 0)
	tlSpine := translate16(0, 30, 0)
	tlHead := translate16(0, 60, 0)
	tHips, tSpine, tHead := translate16(0, 0, 0), translate16(0, 0, 0), translate16(0, 0, 0)
	if o.blenderStyle {
		tSpine = translate16(0, -30, 0)
		tHead = translate16(0, -60, 0)
	}

	cluster := func(id int64, name string, tf, tl []float64, idxs []int32, ws []float64) *tnode {
		c := tn("Deformer", id, name+"\x00\x01SubDeformer", "Cluster").add(
			tn("Version", int32(100)),
			tn("Transform", tf),
			tn("TransformLink", tl))
		if idxs != nil {
			c.add(tn("Indexes", idxs), tn("Weights", ws))
		}
		return c
	}

	objects := tn("Objects").add(
		geom,
		tn("Model", idMesh, "body\x00\x01Model", "Mesh"),
		tn("Model", idHips, "Hips\x00\x01Model", "LimbNode"),
		tn("Model", idSpine, "Spine\x00\x01Model", "LimbNode"),
		tn("Model", idHead, "Head\x00\x01Model", "LimbNode"),
		tn("Material", idMat, "skin\x00\x01Material", "").add(
			tn("Properties70").add(
				tn("P", "DiffuseColor", "Color", "", "A", 1.0, 1.0, 1.0))),
		tn("Deformer", idSkin, "body\x00\x01Deformer", "Skin").add(tn("Version", int32(101))),
		cluster(idClusterH, "Hips", tHips, tlHips, []int32{0, 1, 2, 3}, []float64{1, 1, 1, 1}),
		cluster(idClusterS, "Spine", tSpine, tlSpine, []int32{4, 5, 6, 7}, []float64{1, 1, 1, 1}),
		cluster(idClusterHd, "Head", tHead, tlHead, nil, nil), // no influences - slot exists, zero points
	)

	texPNG := &bytes.Buffer{}
	if err := png.Encode(texPNG, goldenTexture()); err != nil {
		t.Fatal(err)
	}
	if !o.noTexture {
		tex := tn("Texture", idTex, "skin\x00\x01Texture", "").add(
			tn("Properties70").add(
				tn("P", "WrapModeU", "enum", "", "", int32(1)),
				tn("P", "WrapModeV", "enum", "", "", int32(1))))
		video := tn("Video", idVideo, "skin\x00\x01Video", "Clip")
		if o.extTexture {
			if err := os.WriteFile(filepath.Join(dir, "tex.png"), texPNG.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			tex.add(tn("RelativeFilename", "tex.png"))
		} else {
			video.add(tn("Content", texPNG.Bytes())) // []byte -> 'R' property
		}
		objects.add(tex, video)
	}

	conn := func(kind string, props ...any) *tnode {
		return tn("C", append([]any{kind}, props...)...)
	}
	hipsBone := idHips
	if o.badCluster {
		hipsBone = int64(999999)
	}
	connections := tn("Connections").add(
		conn("OO", idGeom, idMesh),
		conn("OO", idMesh, int64(0)),
		conn("OO", idHips, int64(0)),
		conn("OO", idSpine, idHips),
		conn("OO", idHead, idSpine),
		conn("OO", idSkin, idGeom),
		conn("OO", idClusterH, idSkin),
		conn("OO", idClusterS, idSkin),
		conn("OO", idClusterHd, idSkin),
		conn("OO", hipsBone, idClusterH),
		conn("OO", idSpine, idClusterS),
		conn("OO", idHead, idClusterHd),
		conn("OO", idMat, idMesh),
	)
	if o.connCycle {
		connections.add(conn("OO", idHips, idSpine)) // hips->spine on top of spine->hips
	}
	if !o.noTexture {
		connections.add(
			conn("OP", idTex, idMat, "DiffuseColor"),
			conn("OO", idVideo, idTex))
	}

	roots := []*tnode{
		tn("FBXHeaderExtension").add(tn("FBXVersion", int32(version))),
		tn("GlobalSettings").add(
			tn("Version", int32(1000)),
			tn("Properties70").add(
				tn("P", "UnitScaleFactor", "double", "Number", "", factor))),
		objects,
		connections,
	}
	return writeTestFBX(t, version, o.zlib, roots)
}

func parseFBXRig(t *testing.T, o fbxRigOpts) *Document {
	t.Helper()
	dir := t.TempDir()
	doc, err := ParseFBX(buildFBXRig(t, o, dir), dir)
	if err != nil {
		t.Fatalf("parse fbx rig: %v", err)
	}
	return doc
}

func sampleFBXRig(t *testing.T, o fbxRigOpts, n int) *SampleResult {
	t.Helper()
	doc := parseFBXRig(t, o)
	if _, err := MapHumanoid(doc, nil); err != nil {
		t.Fatalf("map humanoid: %v", err)
	}
	res, err := Sample(doc, n, 1)
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	return res
}

// ── tests ────────────────────────────────────────────────────────────────────

// TestFBXPipelineRoundTrip: 2-bone skinned quad through the FULL pipeline: parse ->
// heuristic bone map -> sample -> atlas -> PNG encode -> decode; decoded atlas is
// field-exact vs the built one, locals land in the bind-space ranges.
func TestFBXPipelineRoundTrip(t *testing.T) {
	doc := parseFBXRig(t, fbxRigOpts{})
	if doc.InputKind != "fbx" || doc.FBXUnitScaleFactor != 1 {
		t.Fatalf("InputKind %q factor %v, want fbx/1", doc.InputKind, doc.FBXUnitScaleFactor)
	}
	mapping, err := MapHumanoid(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mapping.Source != "heuristic" || len(mapping.Pairs) != 3 {
		t.Fatalf("mapping %+v, want heuristic hips/spine/head", mapping)
	}
	res, err := Sample(doc, 64, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped != 0 || len(res.Points) != 64 {
		t.Fatalf("dropped %d emitted %d, want 0/64", res.Dropped, len(res.Points))
	}
	for _, p := range res.Points {
		if p.Slot != 0 && p.Slot != 1 {
			t.Fatalf("point on slot %d, want hips/spine only", p.Slot)
		}
		if p.Local[1] < -1e-9 || p.Local[1] > 0.3+1e-9 || math.Abs(p.Local[0]) > 0.25+1e-9 {
			t.Fatalf("slot %d local %v outside bind range", p.Slot, p.Local)
		}
	}
	atlas, err := BuildAtlas(res.Points, 3)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := atlas.EncodePNG(&buf); err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePNG(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, atlas) {
		t.Fatal("decoded atlas differs from built atlas")
	}
}

// TestFBXVariantsSampleIdentically: zlib arrays, version 7500 records, ByControlPoint and
// ByPolygonVertex/Direct UVs, ngon fan-triangulation and Blender-style cluster matrices all
// encode the SAME rig - every variant must sample bit-identically to the baseline.
func TestFBXVariantsSampleIdentically(t *testing.T) {
	base := sampleFBXRig(t, fbxRigOpts{}, 64)
	variants := map[string]fbxRigOpts{
		"zlib":             {zlib: true},
		"v7500":            {version: 7500},
		"v7500+zlib":       {version: 7500, zlib: true},
		"uvByControlPoint": {uvByControl: true},
		"uvDirect":         {uvDirect: true},
		"ngon":             {ngon: true},
		"blenderClusters":  {blenderStyle: true},
	}
	for name, o := range variants {
		t.Run(name, func(t *testing.T) {
			got := sampleFBXRig(t, o, 64)
			if !reflect.DeepEqual(base.Points, got.Points) {
				t.Fatalf("%s variant sampled differently from baseline", name)
			}
		})
	}
}

// TestFBXBlenderConventionDetected: Blender-style rigs surface the convention warning; the
// SDK-style baseline does not.
func TestFBXBlenderConventionDetected(t *testing.T) {
	if ws := parseFBXRig(t, fbxRigOpts{}).Warnings; len(ws) != 0 {
		t.Fatalf("baseline warnings %v, want none", ws)
	}
	ws := parseFBXRig(t, fbxRigOpts{blenderStyle: true}).Warnings
	if len(ws) != 1 || !strings.Contains(ws[0], "Blender-style") {
		t.Fatalf("warnings %v, want Blender-style detection", ws)
	}
}

// TestFBXTextureColours: constant-UV quads assert texel-exact colours through the embedded
// texture, proving the V-flip (FBX bottom-left -> top-left) and the sRGB pipeline; the
// external-file variant must match bit-exactly.
func TestFBXTextureColours(t *testing.T) {
	want := map[int][3]uint8{}
	for slot, texel := range map[int][3]float64{
		0: {32, 32, 16},    // uv (0.125, flip 0.125) -> texel (0,0)
		1: {160, 160, 144}, // uv (0.625, flip 0.625) -> texel (2,2)
	} {
		var rgb [3]uint8
		for ch := 0; ch < 3; ch++ {
			rgb[ch] = LinearToSRGBByte(SRGBToLinear(texel[ch] / 255))
		}
		want[slot] = rgb
	}
	embedded := sampleFBXRig(t, fbxRigOpts{uvConst: true}, 64)
	for _, p := range embedded.Points {
		if p.RGB != want[p.Slot] {
			t.Fatalf("slot %d colour %v, want %v (V-flip broken?)", p.Slot, p.RGB, want[p.Slot])
		}
	}
	external := sampleFBXRig(t, fbxRigOpts{uvConst: true, extTexture: true}, 64)
	if !reflect.DeepEqual(embedded.Points, external.Points) {
		t.Fatal("external-file texture sampled differently from embedded Content")
	}
}

// TestFBXMissingTextureDegrades: an external texture whose file does not exist scans
// factor-only (white) with a warning instead of failing.
func TestFBXMissingTextureDegrades(t *testing.T) {
	data := buildFBXRig(t, fbxRigOpts{extTexture: true}, t.TempDir())
	doc, err := ParseFBX(data, t.TempDir()) // fresh dir: tex.png absent
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range doc.Warnings {
		if strings.Contains(w, "factor-only") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings %v, want missing-texture warning", doc.Warnings)
	}
	if _, err := MapHumanoid(doc, nil); err != nil {
		t.Fatal(err)
	}
	res, err := Sample(doc, 16, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res.Points {
		if p.RGB != [3]uint8{255, 255, 255} {
			t.Fatalf("colour %v, want factor-only white", p.RGB)
		}
	}
}

// TestFBXUnitScaleFactor: UnitScaleFactor 2.54 (raw inches) scales bind-space extents by
// 2.54 vs the raw-cm rig: quad span 0..30 raw -> 0..0.762 m.
func TestFBXUnitScaleFactor(t *testing.T) {
	res := sampleFBXRig(t, fbxRigOpts{unitFactor: 2.54}, 200)
	maxY, maxAbsX := 0.0, 0.0
	for _, p := range res.Points {
		maxY = math.Max(maxY, p.Local[1])
		maxAbsX = math.Max(maxAbsX, math.Abs(p.Local[0]))
	}
	if maxY > 0.762+1e-9 || maxY < 0.6 {
		t.Fatalf("max local y %v, want ~0..0.762 (30 raw in * 2.54/100)", maxY)
	}
	if maxAbsX > 0.635+1e-9 {
		t.Fatalf("max |x| %v exceeds 25*2.54/100", maxAbsX)
	}
}

// TestFBXDeterminism: full pipeline repeated from fresh parses yields identical PNG bytes.
func TestFBXDeterminism(t *testing.T) {
	var want []byte
	for run := 0; run < 5; run++ {
		res := sampleFBXRig(t, fbxRigOpts{}, 64)
		atlas, err := BuildAtlas(res.Points, 0)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := atlas.EncodePNG(&buf); err != nil {
			t.Fatal(err)
		}
		if run == 0 {
			want = buf.Bytes()
		} else if !bytes.Equal(want, buf.Bytes()) {
			t.Fatalf("run %d: atlas bytes drifted", run)
		}
	}
}

// TestFBXLoadDispatch: Load sniffs binary FBX by magic; .fbx without the magic reports the
// ASCII-unsupported error; ParseFBX rejects ASCII content directly.
func TestFBXLoadDispatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rig.fbx")
	if err := os.WriteFile(path, buildFBXRig(t, fbxRigOpts{}, dir), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.InputKind != "fbx" {
		t.Fatalf("InputKind %q, want fbx", doc.InputKind)
	}

	ascii := filepath.Join(dir, "ascii.fbx")
	if err := os.WriteFile(ascii, []byte("; FBX 7.3.0 project file\nFBXHeaderExtension: {\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(ascii); err == nil || !strings.Contains(err.Error(), "ASCII") {
		t.Fatalf("ascii .fbx error %v, want ASCII-unsupported", err)
	}
	if _, err := ParseFBX([]byte("; FBX 7.3.0 project file\n"), ""); err == nil || !strings.Contains(err.Error(), "ASCII") {
		t.Fatalf("ParseFBX ascii error %v, want ASCII-unsupported", err)
	}
}

// TestFBXTruncationNeverPanics: every prefix of a valid rig must reject (or, complete, parse)
// without panicking - the container hardening sweep.
func TestFBXTruncationNeverPanics(t *testing.T) {
	data := buildFBXRig(t, fbxRigOpts{}, t.TempDir())
	for n := 0; n <= len(data); n++ {
		_, err := ParseFBX(data[:n], "")
		if n == len(data) && err != nil {
			t.Fatalf("full rig rejected: %v", err)
		}
	}
	// zlib variant sweep too (compressed-array truncation paths)
	data = buildFBXRig(t, fbxRigOpts{zlib: true}, t.TempDir())
	for n := 0; n <= len(data); n++ {
		ParseFBX(data[:n], "")
	}
}

// TestFBXMalformedRejects: crafted malformations reject with clean errors, never panic.
func TestFBXMalformedRejects(t *testing.T) {
	le := binary.LittleEndian
	craftArray := func(code byte, count, encoding, compLen uint32, payload []byte) rawProp {
		b := []byte{code, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		le.PutUint32(b[1:], count)
		le.PutUint32(b[5:], encoding)
		le.PutUint32(b[9:], compLen)
		return append(b, payload...)
	}
	cases := []struct {
		name    string
		roots   []*tnode
		wantSub string
	}{
		{
			name: "oversized array count",
			roots: []*tnode{tn("Objects").add(
				tn("Geometry", idGeom, "g\x00\x01Geometry", "Mesh").add(
					&tnode{name: "Vertices", props: []any{craftArray('d', 0xFFFFFFF0, 0, 4, []byte{1, 2, 3, 4})}}))},
			wantSub: "exceeds",
		},
		{
			name: "array payload overrun",
			roots: []*tnode{tn("Objects").add(
				tn("Geometry", idGeom, "g\x00\x01Geometry", "Mesh").add(
					&tnode{name: "Vertices", props: []any{craftArray('d', 4, 0, 64, []byte{1, 2, 3, 4})}}))},
			wantSub: "overruns",
		},
		{
			name: "bad zlib payload",
			roots: []*tnode{tn("Objects").add(
				tn("Geometry", idGeom, "g\x00\x01Geometry", "Mesh").add(
					&tnode{name: "Vertices", props: []any{craftArray('d', 2, 1, 4, []byte{0xDE, 0xAD, 0xBE, 0xEF})}}))},
			wantSub: "zlib",
		},
		{
			name: "string length overrun",
			roots: []*tnode{tn("Objects").add(
				&tnode{name: "Model", props: []any{int64(1), rawProp{'S', 0xFF, 0xFF, 0xFF, 0x7F, 'x'}}})},
			wantSub: "overruns",
		},
		{
			name: "unknown property typecode",
			roots: []*tnode{tn("Objects").add(
				&tnode{name: "Model", props: []any{rawProp{'Q', 0, 0, 0, 0}}})},
			wantSub: "typecode",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := writeTestFBX(t, 7400, false, tc.roots)
			_, err := ParseFBX(data, "")
			if err == nil {
				t.Fatal("malformed input accepted, want reject")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q, want substring %q", err, tc.wantSub)
			}
		})
	}
}

// TestFBXClusterMissingModel: a cluster whose bone connection points at a nonexistent id
// rejects with a clean error naming the cluster.
func TestFBXClusterMissingModel(t *testing.T) {
	data := buildFBXRig(t, fbxRigOpts{badCluster: true}, t.TempDir())
	_, err := ParseFBX(data, "")
	if err == nil || !strings.Contains(err.Error(), "bone model") {
		t.Fatalf("error %v, want missing-bone-model reject", err)
	}
}

// TestFBXConnectionCycle: a parent cycle in the model graph rejects cleanly (no hang).
func TestFBXConnectionCycle(t *testing.T) {
	data := buildFBXRig(t, fbxRigOpts{connCycle: true}, t.TempDir())
	_, err := ParseFBX(data, "")
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("error %v, want hierarchy-cycle reject", err)
	}
}

// TestFBXUnskinnedGeometryCounted: a mesh node without a skin is skipped and its primitives
// counted (contract v1.3.1 "unskinned skipped + counted"); the skinned mesh still samples.
func TestFBXUnskinnedGeometryCounted(t *testing.T) {
	doc := parseFBXRig(t, fbxRigOpts{})
	if _, err := MapHumanoid(doc, nil); err != nil {
		t.Fatal(err)
	}
	doc.Nodes = append(doc.Nodes, Node{Name: "prop", Parent: -1, Mesh: 0, Skin: -1})
	res, err := Sample(doc, 16, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.SkippedPrims != 1 {
		t.Fatalf("SkippedPrims %d, want 1 (the unskinned node's primitive)", res.SkippedPrims)
	}
	if len(res.Points) != 16 {
		t.Fatalf("emitted %d, want 16", len(res.Points))
	}
}
