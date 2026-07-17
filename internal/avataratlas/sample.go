package avataratlas

// sample.go - surface-area-weighted uniform triangle sampling with a DETERMINISTIC seeded PRNG.
// Reproducibility is a CONTRACT requirement (§11 goldens): math/rand with a fixed caller seed,
// no time/Date anywhere in the path, fixed draw order (3 rng.Float64 per point: triangle pick,
// then barycentric u, v), triangles enumerated in document order (node -> primitive -> index).
// The same file + seed + point count always yields the identical atlas.

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"math/rand"
	"sort"
)

// SampledPoint is one surface sample resolved to a §5 bone slot.
type SampledPoint struct {
	Slot  int        // §5 slot of the mapped bone
	Local [3]float64 // bone-local bind-space metres (mapped bone's inverseBindMatrix * model pos)
	RGB   [3]uint8   // albedo, sRGB8
}

// SampleResult is the sampler output + accounting for the CLI report.
type SampleResult struct {
	Points       []SampledPoint
	Requested    int
	Dropped      int // draws whose ancestor walk found no mapped bone (or no IBM for it)
	SkippedPrims int // unskinned primitives (no joints/weights, or mesh node without skin)
	PerSlot      [BoneSlots]int
	// ModelMin/ModelMax bound the emitted points in model space (metres) - a humanoid
	// plausibility check for the CLI report. Zero when nothing was emitted.
	ModelMin, ModelMax [3]float64
}

// tri is one sampleable triangle (document order preserved).
type tri struct {
	prim    *Primitive
	skin    *Skin
	a, b, c int // vertex indices
	cum     float64
}

// Sample draws n surface points from doc's skinned meshes. Dominant joint (max barycentric-
// blended weight, ties -> lowest joint) -> VRM humanoid slot; unmapped joints walk to the
// nearest mapped ANCESTOR via the node tree; draws that resolve to nothing are dropped and
// counted (output length = n - Dropped).
func Sample(doc *Document, n int, seed int64) (*SampleResult, error) {
	if n <= 0 {
		return nil, fmt.Errorf("sample: point count %d must be > 0", n)
	}
	if len(doc.NodeSlot) == 0 {
		return nil, fmt.Errorf("sample: no humanoid bone mapping (VRM humanoid extension, MapHumanoid name heuristics, or a -bonemap table)")
	}

	res := &SampleResult{Requested: n}

	// Gather triangles in document order; area-weighted cumulative table.
	var tris []tri
	var total float64
	for ni := range doc.Nodes {
		node := &doc.Nodes[ni]
		if node.Mesh < 0 || node.Mesh >= len(doc.Meshes) {
			continue
		}
		if node.Skin < 0 || node.Skin >= len(doc.Skins) {
			// unskinned mesh: skipped + counted (contract v1.3.1)
			res.SkippedPrims += len(doc.Meshes[node.Mesh].Primitives)
			continue
		}
		skin := &doc.Skins[node.Skin]
		for pi := range doc.Meshes[node.Mesh].Primitives {
			prim := &doc.Meshes[node.Mesh].Primitives[pi]
			if prim.Joints == nil || prim.Weights == nil || len(prim.Pos) == 0 {
				res.SkippedPrims++
				continue
			}
			for i := 0; i+2 < len(prim.Indices); i += 3 {
				a, b, c := int(prim.Indices[i]), int(prim.Indices[i+1]), int(prim.Indices[i+2])
				ar := triArea(prim.Pos[a], prim.Pos[b], prim.Pos[c])
				if ar <= 0 { // degenerate: zero weight, never selected
					continue
				}
				total += ar
				tris = append(tris, tri{prim: prim, skin: skin, a: a, b: b, c: c, cum: total})
			}
		}
	}
	if total <= 0 || len(tris) == 0 {
		return nil, fmt.Errorf("sample: no sampleable skinned surface (need skinned triangle primitives with JOINTS_0/WEIGHTS_0)")
	}
	cums := make([]float64, len(tris))
	for i := range tris {
		cums[i] = tris[i].cum
	}

	rng := rand.New(rand.NewSource(seed))
	res.Points = make([]SampledPoint, 0, n)
	for i := 0; i < n; i++ {
		// Fixed draw order - part of the determinism contract.
		r := rng.Float64() * total
		u := rng.Float64()
		v := rng.Float64()
		ti := sort.SearchFloat64s(cums, r)
		if ti >= len(tris) {
			ti = len(tris) - 1
		}
		t := &tris[ti]
		if u+v > 1 {
			u, v = 1-u, 1-v
		}
		wa, wb, wc := 1-u-v, u, v

		p, pos, err := samplePoint(doc, t, wa, wb, wc)
		if err != nil {
			return nil, err
		}
		if p == nil {
			res.Dropped++
			continue
		}
		if len(res.Points) == 0 {
			res.ModelMin, res.ModelMax = pos, pos
		} else {
			for ax := 0; ax < 3; ax++ {
				res.ModelMin[ax] = math.Min(res.ModelMin[ax], pos[ax])
				res.ModelMax[ax] = math.Max(res.ModelMax[ax], pos[ax])
			}
		}
		res.PerSlot[p.Slot]++
		res.Points = append(res.Points, *p)
	}
	return res, nil
}

// samplePoint resolves one barycentric draw; nil = dropped (no mapped ancestor / no IBM).
// pos is the model-space sample position (bounds accounting).
func samplePoint(doc *Document, t *tri, wa, wb, wc float64) (*SampledPoint, [3]float64, error) {
	prim := t.prim
	pos := bary3(prim.Pos[t.a], prim.Pos[t.b], prim.Pos[t.c], wa, wb, wc)

	// Dominant joint: barycentric-blended per-joint weight accumulation, max wins,
	// ties -> lowest joint index (deterministic).
	accJ := make(map[int]float64, 12)
	for _, vw := range [3]struct {
		v int
		w float64
	}{{t.a, wa}, {t.b, wb}, {t.c, wc}} {
		for k := 0; k < 4; k++ {
			if wt := prim.Weights[vw.v][k]; wt > 0 {
				accJ[prim.Joints[vw.v][k]] += vw.w * wt
			}
		}
	}
	domJ, domW := -1, 0.0
	for j, w := range accJ {
		if w > domW || (w == domW && (domJ < 0 || j < domJ)) {
			domJ, domW = j, w
		}
	}
	if domJ < 0 {
		return nil, pos, nil // all-zero weights
	}
	if domJ >= len(t.skin.Joints) {
		return nil, pos, fmt.Errorf("sample: JOINTS_0 index %d exceeds skin joint count %d", domJ, len(t.skin.Joints))
	}

	// Joint node -> §5 slot; unmapped -> nearest mapped ancestor (must also be a joint of this
	// skin so an inverseBindMatrix exists for the bone-local transform).
	node := t.skin.Joints[domJ]
	slot, jointIdx := -1, -1
	for walk, hops := node, 0; walk >= 0 && hops <= len(doc.Nodes); walk, hops = doc.Nodes[walk].Parent, hops+1 {
		if s, ok := doc.NodeSlot[walk]; ok {
			if ji, ok := t.skin.JointIndex(walk); ok {
				slot, jointIdx = s, ji
				break
			}
		}
	}
	if slot < 0 {
		return nil, pos, nil // dropped: walk found nothing usable
	}

	local := mat4MulPoint(t.skin.IBMs[jointIdx], pos)

	// Colour: bilinear baseColorTexture sample at UV (sRGB bytes, no conversion) x
	// baseColorFactor applied in linear, re-encoded with the exact §2 OETF.
	rgb := [3]uint8{255, 255, 255}
	factor := [4]float64{1, 1, 1, 1}
	var tex *Texture
	if prim.Mat >= 0 && prim.Mat < len(doc.Materials) {
		factor = doc.Materials[prim.Mat].BaseColorFactor
		tex = doc.Materials[prim.Mat].Tex
	}
	var srgb [3]float64 // 0..255, sRGB space
	if tex != nil && prim.UV != nil {
		uv := bary2(prim.UV[t.a], prim.UV[t.b], prim.UV[t.c], wa, wb, wc)
		srgb = bilinearSRGB(tex, uv[0], uv[1])
	} else {
		srgb = [3]float64{255, 255, 255}
	}
	for ch := 0; ch < 3; ch++ {
		lin := SRGBToLinear(srgb[ch]/255) * factor[ch]
		rgb[ch] = LinearToSRGBByte(lin)
	}

	return &SampledPoint{Slot: slot, Local: local, RGB: rgb}, pos, nil
}

// ── geometry / texture helpers ───────────────────────────────────────────────

func triArea(a, b, c [3]float64) float64 {
	ab := [3]float64{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
	ac := [3]float64{c[0] - a[0], c[1] - a[1], c[2] - a[2]}
	cx := ab[1]*ac[2] - ab[2]*ac[1]
	cy := ab[2]*ac[0] - ab[0]*ac[2]
	cz := ab[0]*ac[1] - ab[1]*ac[0]
	return 0.5 * math.Sqrt(cx*cx+cy*cy+cz*cz)
}

func bary3(a, b, c [3]float64, wa, wb, wc float64) [3]float64 {
	return [3]float64{
		wa*a[0] + wb*b[0] + wc*c[0],
		wa*a[1] + wb*b[1] + wc*c[1],
		wa*a[2] + wb*b[2] + wc*c[2],
	}
}

func bary2(a, b, c [2]float64, wa, wb, wc float64) [2]float64 {
	return [2]float64{wa*a[0] + wb*b[0] + wc*c[0], wa*a[1] + wb*b[1] + wc*c[1]}
}

// mat4MulPoint applies a column-major glTF mat4 to point p (w=1).
func mat4MulPoint(m [16]float64, p [3]float64) [3]float64 {
	return [3]float64{
		m[0]*p[0] + m[4]*p[1] + m[8]*p[2] + m[12],
		m[1]*p[0] + m[5]*p[1] + m[9]*p[2] + m[13],
		m[2]*p[0] + m[6]*p[1] + m[10]*p[2] + m[14],
	}
}

// wrapTexel wraps texel index i into [0,n) per the glTF sampler mode.
func wrapTexel(i, n, mode int) int {
	switch mode {
	case WrapClamp:
		if i < 0 {
			return 0
		}
		if i >= n {
			return n - 1
		}
		return i
	case WrapMirror:
		m := ((i % (2 * n)) + 2*n) % (2 * n)
		if m >= n {
			return 2*n - 1 - m
		}
		return m
	default: // WrapRepeat
		return ((i % n) + n) % n
	}
}

// texelSRGB reads non-premultiplied sRGB bytes (NRGBAModel avoids the premultiplied RGBA()
// path that would darken texels with alpha).
func texelSRGB(img image.Image, x, y int) [3]float64 {
	b := img.Bounds()
	c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
	return [3]float64{float64(c.R), float64(c.G), float64(c.B)}
}

// bilinearSRGB samples the texture at UV (glTF top-left origin, texel-centre convention),
// interpolating raw sRGB bytes ("texture bytes ARE sRGB - no conversion" per §11).
func bilinearSRGB(tex *Texture, u, v float64) [3]float64 {
	b := tex.Img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return [3]float64{255, 255, 255}
	}
	fx := u*float64(w) - 0.5
	fy := v*float64(h) - 0.5
	x0 := int(math.Floor(fx))
	y0 := int(math.Floor(fy))
	tx := fx - float64(x0)
	ty := fy - float64(y0)
	c00 := texelSRGB(tex.Img, wrapTexel(x0, w, tex.WrapS), wrapTexel(y0, h, tex.WrapT))
	c10 := texelSRGB(tex.Img, wrapTexel(x0+1, w, tex.WrapS), wrapTexel(y0, h, tex.WrapT))
	c01 := texelSRGB(tex.Img, wrapTexel(x0, w, tex.WrapS), wrapTexel(y0+1, h, tex.WrapT))
	c11 := texelSRGB(tex.Img, wrapTexel(x0+1, w, tex.WrapS), wrapTexel(y0+1, h, tex.WrapT))
	var out [3]float64
	for ch := 0; ch < 3; ch++ {
		top := c00[ch]*(1-tx) + c10[ch]*tx
		bot := c01[ch]*(1-tx) + c11[ch]*tx
		out[ch] = top*(1-ty) + bot*ty
	}
	return out
}
