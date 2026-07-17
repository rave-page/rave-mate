package avataratlas

// fbx.go - semantic binary-FBX parser scoped to avatar scanning (contract §11 v1.3.1): builds
// the same Document shapes the shared sampler consumes. Geometry (Vertices +
// PolygonVertexIndex negative-terminator ngons, fan-triangulated; LayerElementUV
// ByPolygonVertex/ByControlPoint x Direct/IndexToDirect; LayerElementMaterial
// ByPolygon/AllSame), Models (nodes), Deformer Skin/Cluster skinning, Material
// DiffuseColor/BaseColor + connected texture (embedded Video Content preferred, else
// RelativeFilename next to the .fbx), GlobalSettings UnitScaleFactor.
//
// Units: metres = raw * UnitScaleFactor / 100 (FBX canonical cm; validated against real rigs -
// Blender exports carry factor 100 with metre-valued raws).
// Bone-local bind space per contract: p_boneLocal = TransformLink^-1 * Transform * v, which
// sidesteps the FBX node pivot/PreRotation stack entirely; the per-joint composite lands in
// Skin.IBMs so sample.go stays shared with glTF. Blender exporters store Transform already
// bone-relative - see buildSkin for the per-skin convention detection. UV V flips at parse (FBX bottom-left origin ->
// sampler's glTF top-left convention). Texture decode failures degrade to factor-only colour
// with a Warnings entry (missing external texture files are routine in the wild); structural
// malformations reject with clean errors, never panic.

import (
	"bytes"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadFBX reads + parses a binary .fbx file.
func LoadFBX(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseFBX(data, filepath.Dir(path))
}

// ParseFBX builds a Document from binary FBX bytes; external textures resolve against baseDir.
func ParseFBX(data []byte, baseDir string) (*Document, error) {
	_, roots, err := parseFBXTree(data)
	if err != nil {
		return nil, err
	}
	b := &fbxBuilder{baseDir: baseDir, scale: 0.01, factor: 1, texCache: map[int64]*Texture{}}
	for _, r := range roots {
		switch r.name {
		case "GlobalSettings":
			b.globalSettings(r)
		case "Objects":
			b.objects = r.children
		case "Connections":
			b.connections(r)
		}
	}
	if b.objects == nil {
		return nil, fmt.Errorf("fbx: no Objects section")
	}
	return b.build()
}

type fbxConn struct {
	child, parent int64
	prop          string // OP property name; "" for OO
}

type fbxBuilder struct {
	baseDir string
	factor  float64 // GlobalSettings UnitScaleFactor (cm per raw unit)
	scale   float64 // metres per raw unit = factor/100
	objects []*fbxNode
	conns   []fbxConn

	warnings []string
	texCache map[int64]*Texture

	// indexes assigned during build()
	byID       map[int64]*fbxNode
	nodeIdx    map[int64]int
	texNodes   map[int64]*fbxNode
	videoNodes map[int64]*fbxNode
	videoOfTex map[int64]int64
}

func (b *fbxBuilder) warnf(format string, args ...any) {
	b.warnings = append(b.warnings, fmt.Sprintf(format, args...))
}

// prop70 finds a Properties70 "P" entry by name under n; returns nil if absent.
func prop70(n *fbxNode, name string) *fbxNode {
	p70 := n.child("Properties70")
	if p70 == nil {
		return nil
	}
	for _, p := range p70.children {
		if p.name != "P" {
			continue
		}
		if s, ok := p.propString(0); ok && s == name {
			return p
		}
	}
	return nil
}

// prop70Floats returns the trailing numeric values of a P entry (after the 4 header strings).
func prop70Floats(p *fbxNode) []float64 {
	var out []float64
	for k := 4; k < len(p.props); k++ {
		if v, ok := p.propFloat(k); ok {
			out = append(out, v)
		}
	}
	return out
}

func (b *fbxBuilder) globalSettings(gs *fbxNode) {
	if p := prop70(gs, "UnitScaleFactor"); p != nil {
		if vs := prop70Floats(p); len(vs) > 0 && vs[0] > 0 {
			b.factor = vs[0]
			b.scale = vs[0] / 100
		}
	}
}

func (b *fbxBuilder) connections(cn *fbxNode) {
	for _, c := range cn.children {
		if c.name != "C" {
			continue
		}
		typ, _ := c.propString(0)
		child, okC := c.propInt(1)
		parent, okP := c.propInt(2)
		if !okC || !okP {
			continue
		}
		conn := fbxConn{child: child, parent: parent}
		switch typ {
		case "OO":
		case "OP":
			conn.prop, _ = c.propString(3)
			if conn.prop == "" {
				continue
			}
		default:
			continue
		}
		b.conns = append(b.conns, conn)
	}
}

// objName returns the name part of an object node's "Name\x00\x01Class" property.
func objName(n *fbxNode) string {
	s, _ := n.propString(1)
	return fbxObjName(s)
}

func objID(n *fbxNode) int64 {
	id, _ := n.propInt(0)
	return id
}

func objSubclass(n *fbxNode) string {
	s, _ := n.propString(2)
	return s
}

// ── matrix helpers (FBX 16-double layout matches the glTF column-major layout under
// mat4MulPoint: translation at 12..14) ───────────────────────────────────────────

var fbxIdentity = [16]float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}

// fbxMat4Mul composes a∘b: mat4MulPoint(result, v) == mat4MulPoint(a, mat4MulPoint(b, v)).
func fbxMat4Mul(a, b [16]float64) [16]float64 {
	var c [16]float64
	for col := 0; col < 4; col++ {
		for row := 0; row < 4; row++ {
			var s float64
			for k := 0; k < 4; k++ {
				s += a[k*4+row] * b[col*4+k]
			}
			c[col*4+row] = s
		}
	}
	return c
}

// fbxMat4AffineInverse inverts an affine bind matrix (last row must be ~[0 0 0 1]).
func fbxMat4AffineInverse(m [16]float64) ([16]float64, error) {
	const eps = 1e-6
	if math.Abs(m[3]) > eps || math.Abs(m[7]) > eps || math.Abs(m[11]) > eps || math.Abs(m[15]-1) > eps {
		return [16]float64{}, fmt.Errorf("non-affine matrix")
	}
	// 3x3 linear block: column j at m[j*4..j*4+2].
	a, d, g := m[0], m[1], m[2]
	bb, e, h := m[4], m[5], m[6]
	cc, f, i := m[8], m[9], m[10]
	det := a*(e*i-f*h) - bb*(d*i-f*g) + cc*(d*h-e*g)
	if math.Abs(det) < 1e-12 {
		return [16]float64{}, fmt.Errorf("singular matrix")
	}
	id := 1 / det
	var r [16]float64
	r[0] = (e*i - f*h) * id
	r[4] = (cc*h - bb*i) * id
	r[8] = (bb*f - cc*e) * id
	r[1] = (f*g - d*i) * id
	r[5] = (a*i - cc*g) * id
	r[9] = (cc*d - a*f) * id
	r[2] = (d*h - e*g) * id
	r[6] = (bb*g - a*h) * id
	r[10] = (a*e - bb*d) * id
	tx, ty, tz := m[12], m[13], m[14]
	r[12] = -(r[0]*tx + r[4]*ty + r[8]*tz)
	r[13] = -(r[1]*tx + r[5]*ty + r[9]*tz)
	r[14] = -(r[2]*tx + r[6]*ty + r[10]*tz)
	r[15] = 1
	return r, nil
}

// ── document build ───────────────────────────────────────────────────────────

func (b *fbxBuilder) build() (*Document, error) {
	doc := &Document{NodeSlot: map[int]int{}, InputKind: "fbx", FBXUnitScaleFactor: b.factor}
	b.videoOfTex = map[int64]int64{}

	// Objects classification (document order preserved everywhere).
	byID := map[int64]*fbxNode{}
	nodeIdx := map[int64]int{} // Model id -> doc.Nodes index
	matIdx := map[int64]int{}  // Material id -> doc.Materials index
	geoms := []*fbxNode{}      // Geometry/Mesh, document order
	skinNodes := map[int64]*fbxNode{}
	clusterNodes := map[int64]*fbxNode{}
	texNodes := map[int64]*fbxNode{}
	videoNodes := map[int64]*fbxNode{}
	var matNodes []*fbxNode

	for _, o := range b.objects {
		id := objID(o)
		if _, dup := byID[id]; dup {
			continue // duplicate id: first wins (deterministic)
		}
		byID[id] = o
		switch o.name {
		case "Model":
			nodeIdx[id] = len(doc.Nodes)
			doc.Nodes = append(doc.Nodes, Node{Name: objName(o), Parent: -1, Mesh: -1, Skin: -1})
		case "Geometry":
			if objSubclass(o) == "Mesh" {
				geoms = append(geoms, o)
			}
		case "Material":
			matIdx[id] = len(matNodes)
			matNodes = append(matNodes, o)
		case "Deformer":
			switch objSubclass(o) {
			case "Skin":
				skinNodes[id] = o
			case "Cluster":
				clusterNodes[id] = o
			}
		case "Texture":
			texNodes[id] = o
		case "Video":
			videoNodes[id] = o
		}
	}
	b.byID = byID
	b.nodeIdx = nodeIdx
	b.texNodes = texNodes
	b.videoNodes = videoNodes

	// Connection indexes (first connection wins where a single target is expected).
	geomOwner := map[int64]int64{}  // geometry -> owning model
	skinOfGeom := map[int64]int64{} // geometry -> skin deformer
	clustersOfSkin := map[int64][]int64{}
	boneOfCluster := map[int64]int64{} // cluster -> bone model
	matsOfModel := map[int64][]int64{} // model -> materials (order = connection order)
	texOfMat := map[int64]int64{}      // material -> colour texture
	for _, c := range b.conns {
		child, parent := byID[c.child], byID[c.parent]
		if child == nil {
			continue
		}
		switch {
		case c.prop == "" && child.name == "Model" && (c.parent == 0 || (parent != nil && parent.name == "Model")):
			ni := nodeIdx[c.child]
			if doc.Nodes[ni].Parent == -1 && c.parent != 0 && c.child != c.parent {
				pi := nodeIdx[c.parent]
				doc.Nodes[ni].Parent = pi
				doc.Nodes[pi].Children = append(doc.Nodes[pi].Children, ni)
			}
		case c.prop == "" && child.name == "Geometry" && parent != nil && parent.name == "Model":
			if _, ok := geomOwner[c.child]; !ok {
				geomOwner[c.child] = c.parent
			}
		case c.prop == "" && child.name == "Deformer" && objSubclass(child) == "Skin" && parent != nil && parent.name == "Geometry":
			if _, ok := skinOfGeom[c.parent]; !ok {
				skinOfGeom[c.parent] = c.child
			}
		case c.prop == "" && child.name == "Deformer" && objSubclass(child) == "Cluster" && parent != nil && parent.name == "Deformer":
			clustersOfSkin[c.parent] = append(clustersOfSkin[c.parent], c.child)
		case c.prop == "" && child.name == "Model" && parent != nil && parent.name == "Deformer" && objSubclass(parent) == "Cluster":
			if _, ok := boneOfCluster[c.parent]; !ok {
				boneOfCluster[c.parent] = c.child
			}
		case c.prop == "" && child.name == "Material" && parent != nil && parent.name == "Model":
			matsOfModel[c.parent] = append(matsOfModel[c.parent], c.child)
		case c.prop != "" && child.name == "Texture" && parent != nil && parent.name == "Material":
			base := c.prop
			if i := strings.LastIndexByte(base, '|'); i >= 0 {
				base = base[i+1:]
			}
			if base == "DiffuseColor" || base == "BaseColor" {
				if _, ok := texOfMat[c.parent]; !ok {
					texOfMat[c.parent] = c.child
				}
			}
		case c.prop == "" && child.name == "Video" && parent != nil && parent.name == "Texture":
			if _, ok := b.videoOfTex[c.parent]; !ok {
				b.videoOfTex[c.parent] = c.child
			}
		}
	}

	// Model hierarchy cycle guard (Connections are attacker-controlled).
	for i := range doc.Nodes {
		walk, hops := i, 0
		for walk >= 0 {
			if hops++; hops > len(doc.Nodes) {
				return nil, fmt.Errorf("fbx: model hierarchy cycle at %q", doc.Nodes[i].Name)
			}
			walk = doc.Nodes[walk].Parent
		}
	}

	// Materials.
	doc.Materials = make([]Material, len(matNodes))
	for i, mn := range matNodes {
		m := Material{BaseColorFactor: [4]float64{1, 1, 1, 1}}
		colorP := prop70(mn, "BaseColor")
		if colorP == nil {
			colorP = prop70(mn, "DiffuseColor")
		}
		if colorP != nil {
			if vs := prop70Floats(colorP); len(vs) >= 3 {
				m.BaseColorFactor = [4]float64{vs[0], vs[1], vs[2], 1}
			}
		}
		if texID, ok := texOfMat[objID(mn)]; ok {
			m.Tex = b.texture(texID)
		}
		doc.Materials[i] = m
	}

	// Geometry -> Mesh (+ Skin) per geometry, document order.
	for _, g := range geoms {
		gid := objID(g)
		ownerID, owned := geomOwner[gid]
		if !owned {
			b.warnf("geometry %q not connected to any model - skipped", objName(g))
			continue
		}
		ownerNode := nodeIdx[ownerID]
		if doc.Nodes[ownerNode].Mesh >= 0 {
			b.warnf("model %q has multiple geometries - only the first is scanned", doc.Nodes[ownerNode].Name)
			continue
		}

		var skin *Skin
		var joints4 [][4]int
		var weights4 [][4]float64
		cpCount := 0
		if vsNode := g.child("Vertices"); vsNode != nil {
			if vs, ok := vsNode.propFloats(0); ok {
				cpCount = len(vs) / 3
			}
		}
		if skinID, ok := skinOfGeom[gid]; ok {
			if _, exists := skinNodes[skinID]; !exists {
				return nil, fmt.Errorf("fbx: geometry %q references missing skin deformer %d", objName(g), skinID)
			}
			s, j4, w4, err := b.buildSkin(clustersOfSkin[skinID], clusterNodes, boneOfCluster, cpCount)
			if err != nil {
				return nil, fmt.Errorf("fbx: geometry %q: %w", objName(g), err)
			}
			skin, joints4, weights4 = s, j4, w4
		}

		mesh, err := b.geometryMesh(g, matsOfModel[ownerID], matIdx, joints4, weights4)
		if err != nil {
			return nil, fmt.Errorf("fbx: geometry %q: %w", objName(g), err)
		}
		doc.Nodes[ownerNode].Mesh = len(doc.Meshes)
		doc.Meshes = append(doc.Meshes, mesh)
		if skin != nil {
			doc.Nodes[ownerNode].Skin = len(doc.Skins)
			doc.Skins = append(doc.Skins, *skin)
		}
	}

	doc.Warnings = b.warnings
	return doc, nil
}

// buildSkin assembles a Skin (joints = bone-model node indices, IBMs = bone-local bind
// transforms) + per-control-point top-4 influences.
//
// Cluster-matrix conventions in the wild (detected per skin, deterministic):
//   - SDK-style (Maya/Unity exporters): Transform = mesh global bind -> IBM =
//     TransformLink^-1 * Transform (the contract formula). Transform is IDENTICAL across
//     the skin's clusters.
//   - Blender-style: Transform = TransformLink^-1 * meshGlobal (already bone-relative) ->
//     IBM = Transform directly; the SDK formula would double-apply the bone inverse.
//     Here TransformLink*Transform (= meshGlobal) is identical across clusters instead.
//
// The reading whose recovered mesh-global is constant across clusters wins; on a tie
// (single cluster / identical spreads) TransformLink*Transform ~ identity picks Blender.
func (b *fbxBuilder) buildSkin(clusterIDs []int64, clusterNodes map[int64]*fbxNode, boneOfCluster map[int64]int64, cpCount int) (*Skin, [][4]int, [][4]float64, error) {
	skin := &Skin{jointIdx: map[int]int{}}
	type infl struct {
		j int
		w float64
	}
	cpInf := make([][]infl, cpCount)

	type clusterData struct {
		node       *fbxNode
		boneNode   int
		t, tl, tlt [16]float64 // Transform, TransformLink, TransformLink*Transform
	}
	var cds []clusterData
	for _, cid := range clusterIDs {
		cl := clusterNodes[cid]
		if cl == nil {
			return nil, nil, nil, fmt.Errorf("skin references missing cluster %d", cid)
		}
		boneID, ok := boneOfCluster[cid]
		if !ok {
			return nil, nil, nil, fmt.Errorf("cluster %q has no bone model connection", objName(cl))
		}
		boneNode, ok := b.nodeIdx[boneID]
		if !ok {
			return nil, nil, nil, fmt.Errorf("cluster %q references missing model %d", objName(cl), boneID)
		}
		transform, err := clusterMat(cl, "Transform")
		if err != nil {
			return nil, nil, nil, fmt.Errorf("cluster %q: %w", objName(cl), err)
		}
		transformLink, err := clusterMat(cl, "TransformLink")
		if err != nil {
			return nil, nil, nil, fmt.Errorf("cluster %q: %w", objName(cl), err)
		}
		cds = append(cds, clusterData{
			node: cl, boneNode: boneNode,
			t: transform, tl: transformLink, tlt: fbxMat4Mul(transformLink, transform),
		})
	}

	blenderStyle := false
	if len(cds) > 0 {
		sdkSpread, blenderSpread := 0.0, 0.0
		for i := 1; i < len(cds); i++ {
			sdkSpread = math.Max(sdkSpread, matDeviation(cds[i].t, cds[0].t))
			blenderSpread = math.Max(blenderSpread, matDeviation(cds[i].tlt, cds[0].tlt))
		}
		switch {
		case blenderSpread < sdkSpread:
			blenderStyle = true
		case blenderSpread == sdkSpread: // incl. single cluster
			blenderStyle = matDeviation(cds[0].tlt, fbxIdentity) < 1e-3
		}
		if blenderStyle {
			b.warnf("skin: Blender-style cluster Transform detected (already bone-relative) - using Transform directly as the bind IBM")
		}
	}

	for _, cd := range cds {
		var ibm [16]float64
		if blenderStyle {
			ibm = cd.t
		} else {
			linkInv, err := fbxMat4AffineInverse(cd.tl)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("cluster %q TransformLink: %w", objName(cd.node), err)
			}
			ibm = fbxMat4Mul(linkInv, cd.t)
		}
		// Pos is pre-scaled to metres; the linear block is unit-free, only the
		// translation column carries raw units.
		ibm[12] *= b.scale
		ibm[13] *= b.scale
		ibm[14] *= b.scale

		jpos := len(skin.Joints)
		skin.Joints = append(skin.Joints, cd.boneNode)
		skin.IBMs = append(skin.IBMs, ibm)
		if _, dup := skin.jointIdx[cd.boneNode]; !dup {
			skin.jointIdx[cd.boneNode] = jpos
		}

		var idxs []int64
		var ws []float64
		if n := cd.node.child("Indexes"); n != nil {
			idxs, _ = n.propInts(0)
		}
		if n := cd.node.child("Weights"); n != nil {
			ws, _ = n.propFloats(0)
		}
		if len(idxs) != len(ws) {
			return nil, nil, nil, fmt.Errorf("cluster %q: %d indexes / %d weights", objName(cd.node), len(idxs), len(ws))
		}
		for k, ci := range idxs {
			if ci < 0 || ci >= int64(cpCount) {
				return nil, nil, nil, fmt.Errorf("cluster %q: control point index %d out of range (%d points)", objName(cd.node), ci, cpCount)
			}
			cpInf[ci] = append(cpInf[ci], infl{j: jpos, w: ws[k]})
		}
	}

	// Top-4 influences per control point: weight desc, joint asc (deterministic).
	joints4 := make([][4]int, cpCount)
	weights4 := make([][4]float64, cpCount)
	for ci, infs := range cpInf {
		sort.SliceStable(infs, func(a, bb int) bool {
			if infs[a].w != infs[bb].w {
				return infs[a].w > infs[bb].w
			}
			return infs[a].j < infs[bb].j
		})
		for k := 0; k < 4 && k < len(infs); k++ {
			joints4[ci][k] = infs[k].j
			weights4[ci][k] = infs[k].w
		}
	}
	return skin, joints4, weights4, nil
}

// matDeviation is the max absolute entry difference, normalized by the largest magnitude
// involved (>=1) so raw-unit scale doesn't skew the convention detection.
func matDeviation(a, b [16]float64) float64 {
	maxMag, dev := 1.0, 0.0
	for k := 0; k < 16; k++ {
		maxMag = math.Max(maxMag, math.Max(math.Abs(a[k]), math.Abs(b[k])))
		dev = math.Max(dev, math.Abs(a[k]-b[k]))
	}
	return dev / maxMag
}

// clusterMat reads a 16-double matrix child; absent = identity.
func clusterMat(cl *fbxNode, name string) ([16]float64, error) {
	n := cl.child(name)
	if n == nil {
		return fbxIdentity, nil
	}
	vs, ok := n.propFloats(0)
	if !ok || len(vs) != 16 {
		return [16]float64{}, fmt.Errorf("%s is not a 16-element matrix", name)
	}
	var m [16]float64
	copy(m[:], vs)
	return m, nil
}

type fbxCorner struct {
	cp int // control point index
	pv int // position in the original PolygonVertexIndex array
}

// geometryMesh expands a Geometry into triangle primitives grouped by material slot.
// joints4/weights4 are per-control-point (nil = unskinned).
func (b *fbxBuilder) geometryMesh(g *fbxNode, modelMats []int64, matIdx map[int64]int, joints4 [][4]int, weights4 [][4]float64) (Mesh, error) {
	var mesh Mesh

	var raw []float64
	if n := g.child("Vertices"); n != nil {
		raw, _ = n.propFloats(0)
	}
	if len(raw)%3 != 0 {
		return mesh, fmt.Errorf("Vertices length %d not a multiple of 3", len(raw))
	}
	cps := make([][3]float64, len(raw)/3)
	for i := range cps {
		cps[i] = [3]float64{raw[i*3] * b.scale, raw[i*3+1] * b.scale, raw[i*3+2] * b.scale}
	}

	var pvi []int64
	if n := g.child("PolygonVertexIndex"); n != nil {
		pvi, _ = n.propInts(0)
	}
	// Negative-terminator decode: index -x-1 (= ^x) closes the polygon.
	var polys [][]fbxCorner
	var cur []fbxCorner
	for pv, ix := range pvi {
		cp := ix
		last := false
		if ix < 0 {
			cp = ^ix
			last = true
		}
		if cp < 0 || cp >= int64(len(cps)) {
			return mesh, fmt.Errorf("PolygonVertexIndex %d out of range (%d control points)", cp, len(cps))
		}
		cur = append(cur, fbxCorner{cp: int(cp), pv: pv})
		if last {
			polys = append(polys, cur)
			cur = nil
		}
	}
	if len(cur) > 0 { // tolerate a missing final terminator
		polys = append(polys, cur)
	}

	getUV, err := b.uvLookup(g)
	if err != nil {
		return mesh, err
	}

	slotOf, err := materialSlots(g, len(polys))
	if err != nil {
		return mesh, err
	}

	// Group fan-triangulated polygons by material slot; emit primitives in ascending
	// slot order (deterministic).
	prims := map[int]*Primitive{}
	var slots []int
	for p, poly := range polys {
		if len(poly) < 3 {
			continue // degenerate polygon
		}
		slot := slotOf(p)
		prim := prims[slot]
		if prim == nil {
			mi := -1
			if slot >= 0 && slot < len(modelMats) {
				if i, ok := matIdx[modelMats[slot]]; ok {
					mi = i
				}
			}
			prim = &Primitive{Mat: mi}
			prims[slot] = prim
			slots = append(slots, slot)
		}
		emit := func(c fbxCorner) error {
			prim.Pos = append(prim.Pos, cps[c.cp])
			if getUV != nil {
				uv, err := getUV(c)
				if err != nil {
					return err
				}
				prim.UV = append(prim.UV, uv)
			}
			if joints4 != nil {
				prim.Joints = append(prim.Joints, joints4[c.cp])
				prim.Weights = append(prim.Weights, weights4[c.cp])
			}
			return nil
		}
		for i := 1; i+1 < len(poly); i++ { // fan triangulation
			for _, c := range [3]fbxCorner{poly[0], poly[i], poly[i+1]} {
				if err := emit(c); err != nil {
					return mesh, err
				}
			}
		}
	}
	sort.Ints(slots)
	for _, slot := range slots {
		prim := prims[slot]
		prim.Indices = make([]uint32, len(prim.Pos))
		for i := range prim.Indices {
			prim.Indices[i] = uint32(i)
		}
		mesh.Primitives = append(mesh.Primitives, *prim)
	}
	return mesh, nil
}

// uvLookup builds a per-corner UV resolver from the lowest-index LayerElementUV
// (nil = no UVs). V flips: FBX bottom-left origin -> sampler top-left convention.
func (b *fbxBuilder) uvLookup(g *fbxNode) (func(fbxCorner) ([2]float64, error), error) {
	var layer *fbxNode
	layerIdx := int64(0)
	for _, c := range g.children {
		if c.name != "LayerElementUV" {
			continue
		}
		idx, _ := c.propInt(0)
		if layer == nil || idx < layerIdx {
			layer, layerIdx = c, idx
		}
	}
	if layer == nil {
		return nil, nil
	}
	mapping := ""
	ref := "Direct"
	if n := layer.child("MappingInformationType"); n != nil {
		mapping, _ = n.propString(0)
	}
	if n := layer.child("ReferenceInformationType"); n != nil {
		ref, _ = n.propString(0)
	}
	var uv []float64
	var idx []int64
	if n := layer.child("UV"); n != nil {
		uv, _ = n.propFloats(0)
	}
	if n := layer.child("UVIndex"); n != nil {
		idx, _ = n.propInts(0)
	}
	if mapping != "ByPolygonVertex" && mapping != "ByControlPoint" {
		b.warnf("geometry %q: UV mapping %q unsupported - sampling without UVs", objName(g), mapping)
		return nil, nil
	}
	indexToDirect := ref == "IndexToDirect" || ref == "Index"
	return func(c fbxCorner) ([2]float64, error) {
		k := int64(c.pv)
		if mapping == "ByControlPoint" {
			k = int64(c.cp)
		}
		if indexToDirect {
			if k < 0 || k >= int64(len(idx)) {
				return [2]float64{}, fmt.Errorf("UVIndex lookup %d out of range (%d entries)", k, len(idx))
			}
			k = idx[k]
		}
		if k < 0 || 2*k+1 >= int64(len(uv)) {
			return [2]float64{}, fmt.Errorf("UV index %d out of range (%d UVs)", k, len(uv)/2)
		}
		return [2]float64{uv[2*k], 1 - uv[2*k+1]}, nil
	}, nil
}

// materialSlots resolves per-polygon material slot indices from LayerElementMaterial
// (ByPolygon table or AllSame/absent = slot 0).
func materialSlots(g *fbxNode, npolys int) (func(int) int, error) {
	layer := g.child("LayerElementMaterial")
	if layer == nil {
		return func(int) int { return 0 }, nil
	}
	mapping := "AllSame"
	if n := layer.child("MappingInformationType"); n != nil {
		mapping, _ = n.propString(0)
	}
	var mats []int64
	if n := layer.child("Materials"); n != nil {
		mats, _ = n.propInts(0)
	}
	switch mapping {
	case "ByPolygon":
		if len(mats) < npolys {
			return nil, fmt.Errorf("LayerElementMaterial has %d entries for %d polygons", len(mats), npolys)
		}
		return func(p int) int { return int(mats[p]) }, nil
	default: // AllSame and anything else degrade to the first slot
		slot := 0
		if len(mats) > 0 {
			slot = int(mats[0])
		}
		return func(int) int { return slot }, nil
	}
}

// texture resolves a Texture object to decoded image bytes: embedded Video Content first,
// then RelativeFilename (texture, then video) against baseDir, then bare basenames.
// Failure degrades to nil (factor-only colour) with a warning - missing texture files are
// routine for FBX in the wild and must not kill the scan.
func (b *fbxBuilder) texture(texID int64) *Texture {
	if t, ok := b.texCache[texID]; ok {
		return t
	}
	b.texCache[texID] = nil // failure-cache
	tn := b.texNodes[texID]
	if tn == nil {
		return nil
	}
	var vn *fbxNode
	if vid, ok := b.videoOfTex[texID]; ok {
		vn = b.videoNodes[vid]
	}

	var img image.Image
	if vn != nil {
		if content := vn.child("Content"); content != nil {
			if raw, ok := content.propBytes(0); ok && len(raw) > 0 {
				if dec, _, err := image.Decode(bytes.NewReader(raw)); err == nil {
					img = dec
				} else {
					b.warnf("texture %q: embedded content: %v", objName(tn), err)
				}
			}
		}
	}
	if img == nil {
		var names []string
		addRel := func(n *fbxNode, child string) {
			if n == nil {
				return
			}
			if c := n.child(child); c != nil {
				if s, ok := c.propString(0); ok && s != "" {
					names = append(names, s)
				}
			}
		}
		addRel(tn, "RelativeFilename")
		addRel(vn, "RelativeFilename")
		addRel(tn, "FileName")
		addRel(vn, "FileName")
		tried := map[string]bool{}
		for _, name := range names {
			slashed := strings.ReplaceAll(name, "\\", "/")
			for _, cand := range []string{slashed, filepath.ToSlash(filepath.Base(slashed))} {
				full := filepath.Join(b.baseDir, filepath.FromSlash(cand))
				if tried[full] {
					continue
				}
				tried[full] = true
				raw, err := os.ReadFile(full)
				if err != nil {
					continue
				}
				dec, _, err := image.Decode(bytes.NewReader(raw))
				if err != nil {
					b.warnf("texture %q: %s: %v", objName(tn), cand, err)
					continue
				}
				img = dec
				break
			}
			if img != nil {
				break
			}
		}
		if img == nil {
			b.warnf("texture %q: no embedded content and no readable file (%s) - factor-only colour", objName(tn), strings.Join(names, ", "))
			return nil
		}
	}

	tex := &Texture{Img: img, WrapS: WrapRepeat, WrapT: WrapRepeat}
	if p := prop70(tn, "WrapModeU"); p != nil {
		if vs := prop70Floats(p); len(vs) > 0 && vs[0] == 1 {
			tex.WrapS = WrapClamp
		}
	}
	if p := prop70(tn, "WrapModeV"); p != nil {
		if vs := prop70Floats(p); len(vs) > 0 && vs[0] == 1 {
			tex.WrapT = WrapClamp
		}
	}
	b.texCache[texID] = tex
	return tex
}
