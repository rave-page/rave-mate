package pointcloud

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"testing"

	"rave.page/mate/internal/vrm"
)

// oneMeshModel builds a minimal unskinned single-node model (identity placement) so
// PosedPositions returns bind positions verbatim - no file / restLocal needed.
func oneMeshModel(verts [][3]float32, diffuse color.NRGBA) (*vrm.Model, []vrm.Mat4) {
	vs := make([]vrm.Vertex, len(verts))
	uv := make([][2]float32, len(verts))
	for i, p := range verts {
		vs[i] = vrm.Vertex{Pos: p}
		uv[i] = [2]float32{0, 0}
	}
	m := &vrm.Model{
		Nodes:  []vrm.Node{{Parent: -1}},
		Roots:  []int{0},
		Meshes: []vrm.Mesh{{Verts: vs, NodeIdx: 0, Diffuse: diffuse, UV: uv}},
	}
	return m, []vrm.Mat4{vrm.Identity()}
}

func TestSelectAndPositions(t *testing.T) {
	verts := [][3]float32{{0, 0, 0}, {1, 2, 3}, {-1, -2, -3}, {0.5, 0.5, 0.5}}
	m, world := oneMeshModel(verts, color.NRGBA{R: 10, G: 20, B: 30, A: 255})

	sel := Select(m, 100, true) // target > vert count -> stride 1 -> all verts
	if sel.Count() != len(verts) {
		t.Fatalf("Count=%d want %d", sel.Count(), len(verts))
	}
	if len(sel.Colors) != 3*len(verts) {
		t.Fatalf("Colors len=%d want %d", len(sel.Colors), 3*len(verts))
	}
	// no texture -> diffuse colour
	if sel.Colors[0] != 10 || sel.Colors[1] != 20 || sel.Colors[2] != 30 {
		t.Fatalf("colour = %v, want diffuse 10,20,30", sel.Colors[:3])
	}
	pts := sel.Positions(m, world, nil, nil)
	for i, p := range pts {
		if p != verts[i] {
			t.Fatalf("point %d = %v want %v", i, p, verts[i])
		}
	}

	// determinism: same selection, reused buffer -> identical output
	buf := make([][3]float32, 0, 4)
	again := sel.Positions(m, world, nil, buf)
	for i := range again {
		if again[i] != verts[i] {
			t.Fatalf("reuse point %d = %v want %v", i, again[i], verts[i])
		}
	}
}

func TestSelectStride(t *testing.T) {
	verts := make([][3]float32, 100)
	for i := range verts {
		verts[i] = [3]float32{float32(i), 0, 0}
	}
	m, _ := oneMeshModel(verts, color.NRGBA{A: 255})
	sel := Select(m, 10, false) // stride 100/10 = 10 -> ~10 points
	if sel.Count() != 10 {
		t.Fatalf("stride select Count=%d want 10", sel.Count())
	}
	if sel.Colors != nil {
		t.Fatalf("Colors should be nil when withColor=false")
	}
}

func TestVertColorTexture(t *testing.T) {
	tex := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	tex.SetNRGBA(0, 1, color.NRGBA{R: 200, G: 100, B: 50, A: 255}) // bottom-left (v=0 -> row 1)
	m, _ := oneMeshModel([][3]float32{{0, 0, 0}}, color.NRGBA{})
	m.Meshes[0].Tex = tex
	m.Meshes[0].UV = [][2]float32{{0, 0}} // u=0,v=0 -> texel (0, H-1) = (0,1)
	sel := Select(m, 1, true)
	if sel.Colors[0] != 200 || sel.Colors[1] != 100 || sel.Colors[2] != 50 {
		t.Fatalf("texture colour = %v want 200,100,50", sel.Colors[:3])
	}
}

func TestFormatRoundTrip(t *testing.T) {
	pointCount := 3
	b := NewBounds()
	frames := [][][3]float32{
		{{0, 0, 0}, {1, 1, 1}, {0.5, 0.25, 0.75}},
		{{-1, -1, -1}, {2, 2, 2}, {0, 1, 0}},
	}
	for _, fr := range frames {
		for _, p := range fr {
			b.Expand(p)
		}
	}
	if !b.Valid() {
		t.Fatal("bounds invalid")
	}
	colors := []byte{255, 0, 0, 0, 255, 0, 0, 0, 255}
	h := Header{
		Generator:  "test",
		Source:     "take",
		FPS:        30,
		FrameCount: len(frames),
		PointCount: pointCount,
		HasColor:   true,
		Bounds:     b,
	}
	var buf bytes.Buffer
	enc, err := NewEncoder(&buf, h, colors)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	for _, fr := range frames {
		if err := enc.WriteFrame(fr); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
	}

	dec, err := NewDecoder(&buf)
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	dh := dec.Header()
	if dh.Version != Version || dh.PointCount != pointCount || dh.FrameCount != len(frames) || !dh.HasColor {
		t.Fatalf("header mismatch: %+v", dh)
	}
	if !bytes.Equal(dec.Colors(), colors) {
		t.Fatalf("colours mismatch: %v", dec.Colors())
	}
	// quantization tolerance: one step of the largest axis extent / 65535
	var maxExt float32
	for a := 0; a < 3; a++ {
		if e := b.Max[a] - b.Min[a]; e > maxExt {
			maxExt = e
		}
	}
	tol := maxExt/float32(QuantMax) + 1e-4
	for fi, want := range frames {
		got, err := dec.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame %d: %v", fi, err)
		}
		if len(got) != pointCount {
			t.Fatalf("frame %d len %d want %d", fi, len(got), pointCount)
		}
		for pi := range want {
			for a := 0; a < 3; a++ {
				if d := float32(math.Abs(float64(got[pi][a] - want[pi][a]))); d > tol {
					t.Fatalf("frame %d point %d axis %d: got %f want %f (tol %f)", fi, pi, a, got[pi][a], want[pi][a], tol)
				}
			}
		}
	}
	if _, err := dec.ReadFrame(); err == nil {
		t.Fatal("expected EOF after last frame")
	}
}

func TestEncoderRejectsBadFrame(t *testing.T) {
	h := Header{Generator: "t", PointCount: 2, Bounds: Bounds{Max: [3]float32{1, 1, 1}}}
	enc, err := NewEncoder(&bytes.Buffer{}, h, nil)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}
	if err := enc.WriteFrame([][3]float32{{0, 0, 0}}); err == nil {
		t.Fatal("expected error for wrong point count")
	}
}
