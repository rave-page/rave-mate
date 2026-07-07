// Binary FBX 7.x avatar loader: node records → scene graph + skinned meshes + humanoid-name
// heuristic, mirroring the glTF path in vrm.go. Stdlib only (encoding/binary, compress/zlib,
// image/png, image/jpeg). Geometry, skeleton, skin clusters, per-vertex UVs/normals and diffuse
// materials/textures (embedded Video Content or sibling files) - no blend shapes or animation.
package vrm

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	_ "image/jpeg" // texture decode
	_ "image/png"  // texture decode
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var fbxMagic = []byte("Kaydara FBX Binary")

// isBinaryFBX reports whether data starts with the Kaydara binary-FBX magic.
func isBinaryFBX(data []byte) bool { return bytes.HasPrefix(data, fbxMagic) }

// ── low-level record parser ──────────────────────────────────────────────────

type fbxProp struct {
	typ byte
	i   int64     // Y C I L
	f   float64   // F D
	s   string    // S R
	d   []float64 // 'd' + 'f' arrays
	l   []int64   // 'i' 'l' 'b' arrays
}

type fbxNode struct {
	name  string
	props []fbxProp
	kids  []*fbxNode
}

func (n *fbxNode) child(name string) *fbxNode {
	for _, k := range n.kids {
		if k.name == name {
			return k
		}
	}
	return nil
}

// arrD returns the named child's first float-array prop.
func (n *fbxNode) arrD(name string) []float64 {
	if c := n.child(name); c != nil && len(c.props) > 0 {
		return c.props[0].d
	}
	return nil
}

// arrL returns the named child's first int-array prop.
func (n *fbxNode) arrL(name string) []int64 {
	if c := n.child(name); c != nil && len(c.props) > 0 {
		return c.props[0].l
	}
	return nil
}

// strS returns the named child's first string/raw prop ("" when absent).
func (n *fbxNode) strS(name string) string {
	if c := n.child(name); c != nil && len(c.props) > 0 {
		return c.props[0].s
	}
	return ""
}

// fbxCur is a slice-based cursor; first failure sticks in err and no-ops the rest.
type fbxCur struct {
	b   []byte
	pos int
	big bool // version >= 7500: u64 record offsets
	err error
}

func (c *fbxCur) fail(msg string) {
	if c.err == nil {
		c.err = errors.New(msg)
	}
}

func (c *fbxCur) take(n int) []byte {
	if c.err != nil {
		return nil
	}
	if n < 0 || c.pos+n > len(c.b) {
		c.fail("fbx: truncated data")
		return nil
	}
	b := c.b[c.pos : c.pos+n]
	c.pos += n
	return b
}

func (c *fbxCur) u8() byte {
	if b := c.take(1); b != nil {
		return b[0]
	}
	return 0
}

func (c *fbxCur) u16() uint16 {
	if b := c.take(2); b != nil {
		return binary.LittleEndian.Uint16(b)
	}
	return 0
}

func (c *fbxCur) u32() uint32 {
	if b := c.take(4); b != nil {
		return binary.LittleEndian.Uint32(b)
	}
	return 0
}

func (c *fbxCur) u64() uint64 {
	if b := c.take(8); b != nil {
		return binary.LittleEndian.Uint64(b)
	}
	return 0
}

// off reads a record-offset-sized uint (u32 <7500, u64 >=7500).
func (c *fbxCur) off() int {
	if c.big {
		return int(c.u64())
	}
	return int(c.u32())
}

// array reads an FBX array payload (count, encoding, byteLen), inflating zlib (encoding 1).
func (c *fbxCur) array(elem int) []byte {
	n := int(c.u32())
	enc := c.u32()
	blen := int(c.u32())
	raw := c.take(blen)
	if c.err != nil {
		return nil
	}
	if n < 0 || n > 1<<27 { // cap vs corrupt counts / zlib bombs
		c.fail("fbx: array too large")
		return nil
	}
	want := n * elem
	if enc == 1 {
		zr, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			c.fail("fbx: bad zlib array")
			return nil
		}
		out := make([]byte, want)
		if _, err := io.ReadFull(zr, out); err != nil {
			c.fail("fbx: short zlib array")
			return nil
		}
		_ = zr.Close()
		return out
	}
	if len(raw) < want {
		c.fail("fbx: short array")
		return nil
	}
	return raw[:want]
}

func (c *fbxCur) prop() fbxProp {
	t := c.u8()
	p := fbxProp{typ: t}
	switch t {
	case 'Y':
		p.i = int64(int16(c.u16()))
	case 'C':
		p.i = int64(c.u8())
	case 'I':
		p.i = int64(int32(c.u32()))
	case 'L':
		p.i = int64(c.u64())
	case 'F':
		p.f = float64(math.Float32frombits(c.u32()))
	case 'D':
		p.f = math.Float64frombits(c.u64())
	case 'S', 'R':
		p.s = string(c.take(int(c.u32())))
	case 'f':
		raw := c.array(4)
		p.d = make([]float64, len(raw)/4)
		for i := range p.d {
			p.d[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:])))
		}
	case 'd':
		raw := c.array(8)
		p.d = make([]float64, len(raw)/8)
		for i := range p.d {
			p.d[i] = math.Float64frombits(binary.LittleEndian.Uint64(raw[i*8:]))
		}
	case 'i':
		raw := c.array(4)
		p.l = make([]int64, len(raw)/4)
		for i := range p.l {
			p.l[i] = int64(int32(binary.LittleEndian.Uint32(raw[i*4:])))
		}
	case 'l':
		raw := c.array(8)
		p.l = make([]int64, len(raw)/8)
		for i := range p.l {
			p.l[i] = int64(binary.LittleEndian.Uint64(raw[i*8:]))
		}
	case 'b':
		raw := c.array(1)
		p.l = make([]int64, len(raw))
		for i := range p.l {
			p.l[i] = int64(raw[i])
		}
	default:
		c.fail("fbx: unknown property type")
	}
	return p
}

// fbxWant filters the parse to scene-relevant subtrees; everything else is seek-skipped.
func fbxWant(parent, name string) bool {
	switch parent {
	case "":
		return name == "GlobalSettings" || name == "Objects" || name == "Connections"
	case "Objects":
		return name == "Geometry" || name == "Model" || name == "Deformer" ||
			name == "Material" || name == "Texture" || name == "Video"
	case "GlobalSettings", "Model", "Material":
		return name == "Properties70"
	case "Properties70":
		return name == "P"
	case "Connections":
		return name == "C"
	case "Geometry":
		return name == "Vertices" || name == "PolygonVertexIndex" ||
			name == "LayerElementUV" || name == "LayerElementNormal" || name == "LayerElementMaterial"
	case "LayerElementUV":
		return name == "UV" || name == "UVIndex" || name == "MappingInformationType" || name == "ReferenceInformationType"
	case "LayerElementNormal":
		return name == "Normals" || name == "NormalsIndex" || name == "MappingInformationType" || name == "ReferenceInformationType"
	case "LayerElementMaterial":
		return name == "Materials" || name == "MappingInformationType" || name == "ReferenceInformationType"
	case "Texture", "Video":
		// Texture writes "FileName", Video writes "Filename" - accept both on either.
		return name == "FileName" || name == "Filename" || name == "RelativeFilename" || name == "Content"
	case "Deformer":
		return name == "Indexes" || name == "Weights" || name == "TransformLink"
	}
	return false
}

// parseKids reads sibling records until the NULL sentinel or end.
func (c *fbxCur) parseKids(parent string, end int) []*fbxNode {
	hdr := 13
	if c.big {
		hdr = 25
	}
	var kids []*fbxNode
	for c.err == nil && c.pos+hdr <= end {
		recStart := c.pos
		endOff := c.off()
		nProps := c.off()
		propLen := c.off()
		nameLen := int(c.u8())
		if c.err != nil {
			break
		}
		if endOff == 0 && nProps == 0 && propLen == 0 && nameLen == 0 {
			break // NULL sentinel = end of sibling list
		}
		name := string(c.take(nameLen))
		if c.err != nil {
			break
		}
		if endOff <= recStart || endOff > len(c.b) {
			c.fail("fbx: bad record offset")
			break
		}
		if !fbxWant(parent, name) {
			c.pos = endOff // skip whole subtree
			continue
		}
		propEnd := c.pos + propLen
		if propLen < 0 || propEnd > endOff {
			c.fail("fbx: bad property block")
			break
		}
		n := &fbxNode{name: name}
		for i := 0; i < nProps && c.err == nil; i++ {
			n.props = append(n.props, c.prop())
		}
		if c.err != nil {
			break
		}
		c.pos = propEnd
		if c.pos < endOff {
			n.kids = c.parseKids(name, endOff)
		}
		c.pos = endOff
		kids = append(kids, n)
	}
	return kids
}

// ── Properties70 helpers ─────────────────────────────────────────────────────

// p70 finds the Properties70 ▸ P entry named key (nil-safe).
func p70(n *fbxNode, key string) *fbxNode {
	if n == nil {
		return nil
	}
	ps := n.child("Properties70")
	if ps == nil {
		return nil
	}
	for _, p := range ps.kids {
		if p.name == "P" && len(p.props) > 0 && p.props[0].s == key {
			return p
		}
	}
	return nil
}

// p70Nums returns a P entry's numeric values (props after name/type/label/flags).
func p70Nums(p *fbxNode) []float64 {
	if p == nil || len(p.props) < 5 {
		return nil
	}
	var out []float64
	for _, pr := range p.props[4:] {
		switch pr.typ {
		case 'D', 'F':
			out = append(out, pr.f)
		case 'I', 'L', 'Y', 'C':
			out = append(out, float64(pr.i))
		}
	}
	return out
}

func p70Float(n *fbxNode, key string) (float64, bool) {
	if v := p70Nums(p70(n, key)); len(v) > 0 {
		return v[0], true
	}
	return 0, false
}

func p70Int(n *fbxNode, key string) (int64, bool) {
	if v, ok := p70Float(n, key); ok {
		return int64(v), true
	}
	return 0, false
}

func p70Vec3(n *fbxNode, key string) ([3]float64, bool) {
	v := p70Nums(p70(n, key))
	if len(v) < 3 {
		return [3]float64{}, false
	}
	return [3]float64{v[0], v[1], v[2]}, true
}

// ── object helpers ───────────────────────────────────────────────────────────

// fbxObjName extracts the object name ("Name\x00\x01Class" binary form, "Class::Name" ASCII form).
func fbxObjName(n *fbxNode) string {
	if len(n.props) < 2 {
		return ""
	}
	s := n.props[1].s
	if i := strings.Index(s, "\x00\x01"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "::"); i >= 0 {
		s = s[i+2:]
	}
	return s
}

func fbxObjClass(n *fbxNode) string {
	if len(n.props) < 3 {
		return ""
	}
	return n.props[2].s
}

// ── transforms ───────────────────────────────────────────────────────────────

func fbxTranslate(t [3]float64) Mat4 {
	m := Identity()
	m[12], m[13], m[14] = float32(t[0]), float32(t[1]), float32(t[2])
	return m
}

func fbxScaleM(s [3]float64) Mat4 {
	m := Identity()
	m[0], m[5], m[10] = float32(s[0]), float32(s[1]), float32(s[2])
	return m
}

func fbxCosSin(deg float64) (float32, float32) {
	r := deg * math.Pi / 180
	return float32(math.Cos(r)), float32(math.Sin(r))
}

func fbxRotX(deg float64) Mat4 {
	c, s := fbxCosSin(deg)
	m := Identity()
	m[5], m[6], m[9], m[10] = c, s, -s, c
	return m
}

func fbxRotY(deg float64) Mat4 {
	c, s := fbxCosSin(deg)
	m := Identity()
	m[0], m[2], m[8], m[10] = c, -s, s, c
	return m
}

func fbxRotZ(deg float64) Mat4 {
	c, s := fbxCosSin(deg)
	m := Identity()
	m[0], m[1], m[4], m[5] = c, s, -s, c
	return m
}

// fbxEuler converts Euler degrees + FBX rotation order (0=XYZ … 5=ZYX, first axis applied first).
func fbxEuler(deg [3]float64, order int64) Mat4 {
	x, y, z := fbxRotX(deg[0]), fbxRotY(deg[1]), fbxRotZ(deg[2])
	switch order {
	case 1: // XZY
		return y.Mul(z).Mul(x)
	case 2: // YZX
		return x.Mul(z).Mul(y)
	case 3: // YXZ
		return z.Mul(x).Mul(y)
	case 4: // ZXY
		return y.Mul(x).Mul(z)
	case 5: // ZYX
		return x.Mul(y).Mul(z)
	default: // 0 = XYZ (6 = SphericXYZ treated as XYZ)
		return z.Mul(y).Mul(x)
	}
}

// fbxLocal builds a model node's local transform: T · Rpre · R · S (Euler degrees).
func fbxLocal(n *fbxNode) Mat4 {
	t, _ := p70Vec3(n, "Lcl Translation")
	lm := fbxTranslate(t)
	if pre, ok := p70Vec3(n, "PreRotation"); ok {
		lm = lm.Mul(fbxEuler(pre, 0)) // PreRotation is always XYZ
	}
	if r, ok := p70Vec3(n, "Lcl Rotation"); ok {
		order := int64(0)
		if o, ok := p70Int(n, "RotationOrder"); ok {
			order = o
		}
		lm = lm.Mul(fbxEuler(r, order))
	}
	if sc, ok := p70Vec3(n, "Lcl Scaling"); ok {
		lm = lm.Mul(fbxScaleM(sc))
	}
	return lm
}

// invAffine inverts a column-major affine 4x4 (3x3 adjugate + translation), float64 in.
func invAffine(m [16]float64) Mat4 {
	a00, a10, a20 := m[0], m[1], m[2]
	a01, a11, a21 := m[4], m[5], m[6]
	a02, a12, a22 := m[8], m[9], m[10]
	det := a00*(a11*a22-a12*a21) - a01*(a10*a22-a12*a20) + a02*(a10*a21-a11*a20)
	if math.Abs(det) < 1e-12 {
		return Identity()
	}
	id := 1 / det
	b00, b01, b02 := (a11*a22-a12*a21)*id, (a02*a21-a01*a22)*id, (a01*a12-a02*a11)*id
	b10, b11, b12 := (a12*a20-a10*a22)*id, (a00*a22-a02*a20)*id, (a02*a10-a00*a12)*id
	b20, b21, b22 := (a10*a21-a11*a20)*id, (a01*a20-a00*a21)*id, (a00*a11-a01*a10)*id
	tx, ty, tz := m[12], m[13], m[14]
	var o Mat4
	o[0], o[1], o[2] = float32(b00), float32(b10), float32(b20)
	o[4], o[5], o[6] = float32(b01), float32(b11), float32(b21)
	o[8], o[9], o[10] = float32(b02), float32(b12), float32(b22)
	o[12] = float32(-(b00*tx + b01*ty + b02*tz))
	o[13] = float32(-(b10*tx + b11*ty + b12*tz))
	o[14] = float32(-(b20*tx + b21*ty + b22*tz))
	o[15] = 1
	return o
}

// fbxTriangulateCorners fan-triangulates PolygonVertexIndex (negative value = ^v, marks polygon
// end) into corner ordinals (positions in poly, the "ByPolygonVertex" index space) + a face index
// per triangle (the "ByPolygon" index space).
func fbxTriangulateCorners(poly []int64) (tris []int, triFace []int) {
	start, face := 0, 0
	for k, v := range poly {
		if v >= 0 {
			continue
		}
		for t := start + 2; t <= k; t++ {
			tris = append(tris, start, t-1, t)
			triFace = append(triFace, face)
		}
		face++
		start = k + 1
	}
	return tris, triFace
}

// fbxCtrl decodes poly[k] to a control-point index (end-of-polygon values are ^v).
func fbxCtrl(poly []int64, k int) int {
	v := poly[k]
	if v < 0 {
		v = ^v
	}
	return int(v)
}

// ── layer elements (UV / normals) ────────────────────────────────────────────

// fbxLayer resolves a LayerElement* to per-corner values via its mapping + reference modes.
type fbxLayer struct {
	data    []float64
	idx     []int64
	dim     int
	byCtrl  bool // ByControlPoint / ByVertex / ByVertice
	allSame bool
}

// newFBXLayer builds a resolver from a LayerElement node; nil when absent/empty.
func newFBXLayer(n *fbxNode, dataName, idxName string, dim int) *fbxLayer {
	if n == nil {
		return nil
	}
	d := n.arrD(dataName)
	if len(d) < dim {
		return nil
	}
	l := &fbxLayer{data: d, dim: dim}
	switch n.strS("MappingInformationType") {
	case "ByControlPoint", "ByVertex", "ByVertice":
		l.byCtrl = true
	case "AllSame":
		l.allSame = true
	}
	if strings.HasPrefix(n.strS("ReferenceInformationType"), "Index") { // IndexToDirect / Index
		l.idx = n.arrL(idxName)
	}
	return l
}

// at returns the element for polygon-vertex ordinal pv / control point ctrl (nil out of range).
func (l *fbxLayer) at(pv, ctrl int) []float64 {
	i := pv
	switch {
	case l.allSame:
		i = 0
	case l.byCtrl:
		i = ctrl
	}
	if l.idx != nil {
		if i < 0 || i >= len(l.idx) {
			return nil
		}
		i = int(l.idx[i])
	}
	if i < 0 || (i+1)*l.dim > len(l.data) {
		return nil
	}
	return l.data[i*l.dim : i*l.dim+l.dim]
}

// ── materials + textures ─────────────────────────────────────────────────────

// fbxTexMax caps decoded texture dimensions (point-sampled downscale) - bounds render cost + RAM.
const fbxTexMax = 1024

// fbxDecodeTex decodes PNG/JPEG bytes into NRGBA, downscaling so max dim ≤ fbxTexMax.
// Unsupported/corrupt/absurd input → nil (caller falls back to diffuse color), never an error.
func fbxDecodeTex(b []byte) *image.NRGBA {
	if len(b) == 0 {
		return nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > 1<<14 || cfg.Height > 1<<14 {
		return nil
	}
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil
	}
	step := 1
	for (cfg.Width+step-1)/step > fbxTexMax || (cfg.Height+step-1)/step > fbxTexMax {
		step++
	}
	w, h := (cfg.Width+step-1)/step, (cfg.Height+step-1)/step
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	b0 := src.Bounds()
	for y := range h {
		for x := range w {
			out.Set(x, y, src.At(b0.Min.X+x*step, b0.Min.Y+y*step))
		}
	}
	return out
}

// fbxTexBytes resolves a texture's image bytes: embedded Video Content → embedded content of a
// same-named Video (Blender writes duplicate Video objects, some without Content) → the files
// named by FileName/RelativeFilename (absolute, dir-relative, and dir + base name - exporters
// often keep stale absolute paths).
func fbxTexBytes(tex, video *fbxNode, contentByBase map[string][]byte, dir string) []byte {
	var names []string
	addNames := func(n *fbxNode) {
		if n == nil {
			return
		}
		for _, key := range []string{"FileName", "Filename", "RelativeFilename"} {
			if s := n.strS(key); s != "" {
				names = append(names, s)
			}
		}
	}
	addNames(video)
	addNames(tex)
	if video != nil {
		if c := video.strS("Content"); len(c) > 0 {
			return []byte(c)
		}
	}
	for _, s := range names {
		if b := contentByBase[strings.ToLower(fbxBaseName(s))]; len(b) > 0 {
			return b
		}
	}
	seen := map[string]bool{}
	for _, s := range names {
		p := filepath.FromSlash(strings.ReplaceAll(s, "\\", "/"))
		for _, cand := range []string{p, filepath.Join(dir, p), filepath.Join(dir, filepath.Base(p))} {
			if cand == "" || seen[cand] {
				continue
			}
			seen[cand] = true
			if dir == "" && !filepath.IsAbs(cand) {
				continue
			}
			if b, err := os.ReadFile(cand); err == nil {
				return b
			}
		}
	}
	return nil
}

// fbxBaseName returns the final path component of a foreign-OS path (either separator).
func fbxBaseName(s string) string {
	if i := strings.LastIndexAny(s, "/\\"); i >= 0 {
		return s[i+1:]
	}
	return s
}

// fbxDiffuse reads a material's DiffuseColor as NRGBA (ok=false when absent).
func fbxDiffuse(mat *fbxNode) (color.NRGBA, bool) {
	v, ok := p70Vec3(mat, "DiffuseColor")
	if !ok {
		return color.NRGBA{}, false
	}
	ch := func(f float64) uint8 {
		return uint8(math.Round(math.Min(math.Max(f, 0), 1) * 255))
	}
	return color.NRGBA{R: ch(v[0]), G: ch(v[1]), B: ch(v[2]), A: 255}, true
}

// ── humanoid heuristic ───────────────────────────────────────────────────────

// fbxNormName lowercases, strips non-alphanumerics and any "mixamorig<N>" prefix.
func fbxNormName(name string) string {
	b := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b = append(b, c+32)
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b = append(b, c)
		}
	}
	s := string(b)
	if strings.HasPrefix(s, "mixamorig") {
		s = s[len("mixamorig"):]
		for len(s) > 0 && s[0] >= '0' && s[0] <= '9' {
			s = s[1:]
		}
	}
	return s
}

// fbxBoneAlias: normalized node name → lowercase VRM humanoid bone key.
var fbxBoneAlias = func() map[string]string {
	m := map[string]string{"hips": "hips", "pelvis": "hips", "spine": "spine", "neck": "neck", "head": "head"}
	limbs := [][2]string{
		{"shoulder", "shoulder"}, {"arm", "upperarm"}, {"upperarm", "upperarm"},
		{"forearm", "lowerarm"}, {"lowerarm", "lowerarm"}, {"elbow", "lowerarm"},
		{"hand", "hand"}, {"wrist", "hand"},
		{"upleg", "upperleg"}, {"thigh", "upperleg"}, {"upperleg", "upperleg"},
		{"leg", "lowerleg"}, {"lowerleg", "lowerleg"}, {"calf", "lowerleg"}, {"shin", "lowerleg"},
		{"foot", "foot"},
	}
	for _, side := range []string{"left", "right"} {
		for _, lm := range limbs {
			tgt := side + lm[1]
			for _, k := range []string{side + lm[0], lm[0] + side, side[:1] + lm[0], lm[0] + side[:1]} {
				if _, dup := m[k]; !dup {
					m[k] = tgt
				}
			}
		}
	}
	return m
}()

// ── scene → Model ────────────────────────────────────────────────────────────

// hasParent reports whether parents[id] contains want.
func hasParent(parents map[int64][]int64, id, want int64) bool {
	for _, p := range parents[id] {
		if p == want {
			return true
		}
	}
	return false
}

// fbxApplyScale uniformly rescales the model to meters: node/rest translations,
// vertex positions, inverse-bind translations (linear parts are scale-invariant).
func fbxApplyScale(m *Model, s float32) {
	for i := range m.Nodes {
		m.Nodes[i].Local[12] *= s
		m.Nodes[i].Local[13] *= s
		m.Nodes[i].Local[14] *= s
		m.restLocal[i] = m.Nodes[i].Local
	}
	for mi := range m.Meshes {
		vs := m.Meshes[mi].Verts
		for vi := range vs {
			vs[vi].Pos[0] *= s
			vs[vi].Pos[1] *= s
			vs[vi].Pos[2] *= s
		}
	}
	for i := range m.InverseBind {
		m.InverseBind[i][12] *= s
		m.InverseBind[i][13] *= s
		m.InverseBind[i][14] *= s
	}
}

// ParseFBX builds a Model from binary FBX bytes (no base dir - external texture files are only
// found via absolute paths; use Load for sibling-file resolution).
func ParseFBX(data []byte) (*Model, error) { return parseFBX(data, "") }

// parseFBX builds a Model from binary FBX bytes: nodes from Models, fan-triangulated per-material
// sub-meshes with per-vertex UVs/normals (vertex-split per unique corner tuple) from Geometry,
// top-4 skin weights from Deformer clusters, diffuse color/texture from Material/Texture/Video,
// humanoid map from names. dir = the .fbx's directory, for external-texture resolution.
func parseFBX(data []byte, dir string) (*Model, error) {
	if !isBinaryFBX(data) {
		return nil, errors.New("fbx: missing binary FBX magic")
	}
	if len(data) < 27 {
		return nil, errors.New("fbx: truncated header")
	}
	ver := binary.LittleEndian.Uint32(data[23:27])
	c := &fbxCur{b: data, pos: 27, big: ver >= 7500}
	top := c.parseKids("", len(data))
	if c.err != nil {
		return nil, c.err
	}

	var objects, connections, global *fbxNode
	for _, n := range top {
		switch n.name {
		case "Objects":
			objects = n
		case "Connections":
			connections = n
		case "GlobalSettings":
			global = n
		}
	}
	if objects == nil {
		return nil, errors.New("fbx: no Objects section")
	}

	scale := 0.01 // FBX default unit = cm
	if f, ok := p70Float(global, "UnitScaleFactor"); ok && f > 0 {
		scale = f / 100
	}

	// Object tables, file order for determinism.
	type objRef struct {
		id int64
		n  *fbxNode
	}
	m := &Model{Humanoid: map[string]int{}}
	modelIdx := map[int64]int{}
	var modelIDs []int64
	var geomList, clusterList []objRef
	skinIDs := map[int64]bool{}
	matNodes := map[int64]*fbxNode{}
	texNodes := map[int64]*fbxNode{}
	videoNodes := map[int64]*fbxNode{}
	for _, o := range objects.kids {
		if len(o.props) == 0 || (o.props[0].typ != 'L' && o.props[0].typ != 'I') {
			continue
		}
		id := o.props[0].i
		switch o.name {
		case "Model":
			modelIdx[id] = len(m.Nodes)
			modelIDs = append(modelIDs, id)
			m.Nodes = append(m.Nodes, Node{Name: fbxObjName(o), Parent: -1, Local: fbxLocal(o)})
		case "Geometry":
			if fbxObjClass(o) == "Mesh" {
				geomList = append(geomList, objRef{id, o})
			}
		case "Deformer":
			switch fbxObjClass(o) {
			case "Skin":
				skinIDs[id] = true
			case "Cluster":
				clusterList = append(clusterList, objRef{id, o})
			}
		case "Material":
			matNodes[id] = o
		case "Texture":
			texNodes[id] = o
		case "Video":
			videoNodes[id] = o
		}
	}

	// Connections: OO child → parents / parent → children; OP texture→material on DiffuseColor.
	parents := map[int64][]int64{}
	children := map[int64][]int64{}
	texOfMat := map[int64]int64{}
	if connections != nil {
		for _, cn := range connections.kids {
			if cn.name != "C" || len(cn.props) < 3 {
				continue
			}
			ch, pa := cn.props[1].i, cn.props[2].i
			switch cn.props[0].s {
			case "OO":
				parents[ch] = append(parents[ch], pa)
				children[pa] = append(children[pa], ch)
			case "OP":
				if len(cn.props) >= 4 && cn.props[3].s == "DiffuseColor" && texNodes[ch] != nil {
					if _, dup := texOfMat[pa]; !dup {
						texOfMat[pa] = ch
					}
				}
			}
		}
	}

	// Texture loading (lazy, cached per texture id). contentByBase indexes embedded Video bytes
	// by base filename so content-less duplicate Videos still resolve.
	contentByBase := map[string][]byte{}
	for _, vn := range videoNodes {
		c := vn.strS("Content")
		if len(c) == 0 {
			continue
		}
		for _, key := range []string{"FileName", "Filename", "RelativeFilename"} {
			if s := vn.strS(key); s != "" {
				base := strings.ToLower(fbxBaseName(s))
				if _, dup := contentByBase[base]; !dup {
					contentByBase[base] = []byte(c)
				}
			}
		}
	}
	texCache := map[int64]*image.NRGBA{}
	loadTex := func(texID int64) *image.NRGBA {
		tex := texNodes[texID]
		if tex == nil {
			return nil
		}
		if img, ok := texCache[texID]; ok {
			return img
		}
		var video *fbxNode
		for _, ch := range children[texID] {
			if videoNodes[ch] != nil {
				video = videoNodes[ch]
				break
			}
		}
		img := fbxDecodeTex(fbxTexBytes(tex, video, contentByBase, dir))
		texCache[texID] = img
		return img
	}

	// Hierarchy: first model parent wins; parent 0 / non-model = root.
	for i, id := range modelIDs {
		for _, pa := range parents[id] {
			if pi, ok := modelIdx[pa]; ok && pi != i {
				m.Nodes[i].Parent = pi
				m.Nodes[pi].Children = append(m.Nodes[pi].Children, i)
				break
			}
		}
		if m.Nodes[i].Parent == -1 {
			m.Roots = append(m.Roots, i)
		}
	}

	// Skin joints: one slot per unique bone node, IBM = inv(TransformLink) (bone global bind).
	// Cluster "Transform" is deliberately NOT composed - Blender exports store link⁻¹·meshGlobal
	// there, which would double-apply the inverse; verts are already in bind-world space.
	slotOf := map[int]int{}
	addJoint := func(bone int, cl *fbxNode) int {
		if s, ok := slotOf[bone]; ok {
			return s
		}
		s := len(m.SkinJoints)
		slotOf[bone] = s
		m.SkinJoints = append(m.SkinJoints, bone)
		ib := Identity()
		if link := cl.arrD("TransformLink"); len(link) == 16 {
			var a [16]float64
			copy(a[:], link)
			ib = invAffine(a)
		}
		m.InverseBind = append(m.InverseBind, ib)
		return s
	}

	// vkey identifies a unique (control point, uv, normal) tuple for vertex splitting -
	// per-polygon-vertex attributes mean one control point can need several render vertices.
	type vkey struct {
		ctrl       int
		u, v       float32
		nx, ny, nz float32
	}
	for _, g := range geomList {
		owner, ownerID := -1, int64(0)
		for _, pa := range parents[g.id] {
			if pi, ok := modelIdx[pa]; ok {
				owner, ownerID = pi, pa
				break
			}
		}
		if owner < 0 {
			continue
		}
		pts := g.n.arrD("Vertices")
		nv := len(pts) / 3
		if nv == 0 {
			continue
		}
		cverts := make([]Vertex, nv) // per control point (pre-split)
		for i := range nv {
			cverts[i].Pos = [3]float32{float32(pts[i*3]), float32(pts[i*3+1]), float32(pts[i*3+2])}
		}

		// Clusters reach this geometry via a Skin deformer; the bone Model connects TO the cluster.
		type inf struct {
			slot int
			w    float32
		}
		per := make([][]inf, nv)
		skinned := false
		for _, cl := range clusterList {
			linked := false
			for _, pa := range parents[cl.id] {
				if skinIDs[pa] && hasParent(parents, pa, g.id) {
					linked = true
					break
				}
			}
			if !linked {
				continue
			}
			bone := -1
			for _, ch := range children[cl.id] {
				if bi, ok := modelIdx[ch]; ok {
					bone = bi
					break
				}
			}
			if bone < 0 {
				continue
			}
			skinned = true
			slot := addJoint(bone, cl.n)
			if slot > math.MaxUint16 {
				continue
			}
			idxs := cl.n.arrL("Indexes")
			ws := cl.n.arrD("Weights")
			for k := 0; k < len(idxs) && k < len(ws); k++ {
				vi := int(idxs[k])
				w := float32(ws[k])
				if vi < 0 || vi >= nv || w <= 0 {
					continue
				}
				per[vi] = append(per[vi], inf{slot, w})
			}
		}
		if skinned {
			for i, infs := range per {
				if len(infs) == 0 {
					continue
				}
				sort.Slice(infs, func(a, b int) bool {
					if infs[a].w != infs[b].w {
						return infs[a].w > infs[b].w
					}
					return infs[a].slot < infs[b].slot // deterministic tie-break
				})
				top := min(len(infs), 4)
				var sum float32
				for k := range top {
					sum += infs[k].w
				}
				if sum <= 0 {
					continue
				}
				for k := range top {
					cverts[i].Joints[k] = uint16(infs[k].slot)
					cverts[i].Weights[k] = infs[k].w / sum
				}
			}
		}

		poly := g.n.arrL("PolygonVertexIndex")
		tris, triFace := fbxTriangulateCorners(poly)
		uvL := newFBXLayer(g.n.child("LayerElementUV"), "UV", "UVIndex", 2)
		nrmL := newFBXLayer(g.n.child("LayerElementNormal"), "Normals", "NormalsIndex", 3)

		// No normal layer → smooth per-control-point normals (area-weighted: |cross| = 2·area),
		// computed pre-split so UV seams don't become shading seams.
		var smooth [][3]float32
		if nrmL == nil {
			smooth = make([][3]float32, nv)
			for t := 0; t+2 < len(tris); t += 3 {
				c0, c1, c2 := fbxCtrl(poly, tris[t]), fbxCtrl(poly, tris[t+1]), fbxCtrl(poly, tris[t+2])
				if c0 >= nv || c1 >= nv || c2 >= nv {
					continue
				}
				a, b, c := cverts[c0].Pos, cverts[c1].Pos, cverts[c2].Pos
				ux, uy, uz := b[0]-a[0], b[1]-a[1], b[2]-a[2]
				vx, vy, vz := c[0]-a[0], c[1]-a[1], c[2]-a[2]
				n := [3]float32{uy*vz - uz*vy, uz*vx - ux*vz, ux*vy - uy*vx}
				for _, ci := range [3]int{c0, c1, c2} {
					smooth[ci][0] += n[0]
					smooth[ci][1] += n[1]
					smooth[ci][2] += n[2]
				}
			}
			for i := range smooth {
				smooth[i] = norm3(smooth[i])
			}
		}

		// Material per face → triangle buckets (stable first-appearance order).
		var ownerMats []int64
		for _, ch := range children[ownerID] {
			if matNodes[ch] != nil {
				ownerMats = append(ownerMats, ch)
			}
		}
		matL := g.n.child("LayerElementMaterial")
		var matIdx []int64
		byPoly := false
		if matL != nil {
			matIdx = matL.arrL("Materials")
			mp := matL.strS("MappingInformationType")
			byPoly = mp == "ByPolygon" || mp == "ByPolygone"
		}
		matOfFace := func(face int) int64 {
			k := 0
			if byPoly {
				if face < len(matIdx) {
					k = int(matIdx[face])
				}
			} else if len(matIdx) > 0 {
				k = int(matIdx[0])
			}
			if k >= 0 && k < len(ownerMats) {
				return ownerMats[k]
			}
			return 0
		}
		var order []int64
		buckets := map[int64][]int{}
		for t := range triFace {
			mid := matOfFace(triFace[t])
			if _, ok := buckets[mid]; !ok {
				order = append(order, mid)
			}
			buckets[mid] = append(buckets[mid], t)
		}

		// Emit one sub-mesh per material, splitting vertices per unique corner tuple
		// (skin weights ride along via cverts).
		for _, mid := range order {
			vmap := map[vkey]uint32{}
			var verts []Vertex
			var uvs [][2]float32
			var nrms [][3]float32
			var indices []uint32
			for _, t := range buckets[mid] {
				var vi [3]uint32
				ok := true
				for c := range 3 {
					k := tris[t*3+c]
					ctrl := fbxCtrl(poly, k)
					if ctrl < 0 || ctrl >= nv {
						ok = false
						break
					}
					key := vkey{ctrl: ctrl}
					var uv [2]float32
					if uvL != nil {
						if e := uvL.at(k, ctrl); e != nil {
							uv = [2]float32{float32(e[0]), float32(e[1])}
						}
						key.u, key.v = uv[0], uv[1]
					}
					var nn [3]float32
					if nrmL != nil {
						if e := nrmL.at(k, ctrl); e != nil {
							nn = norm3([3]float32{float32(e[0]), float32(e[1]), float32(e[2])})
						}
						key.nx, key.ny, key.nz = nn[0], nn[1], nn[2]
					} else {
						nn = smooth[ctrl]
					}
					id, dup := vmap[key]
					if !dup {
						id = uint32(len(verts))
						vmap[key] = id
						verts = append(verts, cverts[ctrl])
						if uvL != nil {
							uvs = append(uvs, uv)
						}
						nrms = append(nrms, nn)
					}
					vi[c] = id
				}
				if ok {
					indices = append(indices, vi[0], vi[1], vi[2])
				}
			}
			if len(indices) == 0 {
				continue
			}
			mesh := Mesh{Verts: verts, Indices: indices, NodeIdx: owner, Skinned: skinned, UV: uvs, Normals: nrms}
			if d, ok := fbxDiffuse(matNodes[mid]); ok {
				mesh.Diffuse = d
			}
			if texID, ok := texOfMat[mid]; ok {
				mesh.Tex = loadTex(texID)
			}
			m.Meshes = append(m.Meshes, mesh)
		}
	}

	// Humanoid heuristic: first node whose normalized name maps to a bone wins.
	for i := range m.Nodes {
		if key := fbxBoneAlias[fbxNormName(m.Nodes[i].Name)]; key != "" {
			if _, dup := m.Humanoid[key]; !dup {
				m.Humanoid[key] = i
			}
		}
	}

	m.restLocal = make([]Mat4, len(m.Nodes))
	for i := range m.Nodes {
		m.restLocal[i] = m.Nodes[i].Local
	}

	// Unit scale → meters, with a deterministic sanity fallback (avatars are human-sized).
	s := float32(scale)
	if len(m.Meshes) > 0 {
		lo, hi := m.Bounds() // raw units at this point
		if h := hi[1] - lo[1]; h > 0 {
			if hs := h * s; hs < 0.1 || hs > 100 {
				switch {
				case h*0.01 >= 0.5 && h*0.01 <= 3:
					s = 0.01
				case h >= 0.5 && h <= 3:
					s = 1
				}
			}
		}
	}
	if s != 1 {
		fbxApplyScale(m, s)
	}
	return m, nil
}
