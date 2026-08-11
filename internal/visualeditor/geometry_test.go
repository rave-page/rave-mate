package visualeditor

import (
	"math"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestFlattenLeavesSkipsHiddenAndTransformedGroups(t *testing.T) {
	d := NewDocument(1000, 500)
	a := NewSolid("a", 0, 0, 100, 100, RGBA{}.NRGBA())
	hid := NewSolid("hid", 0, 0, 10, 10, RGBA{}.NRGBA())
	hid.Visible = false
	g := NewGroup("g") // identity: recurses
	b := NewText("b", 10, 10, 50, 20, "x", "", 12, RGBA{}.NRGBA())
	g.Children = []*Layer{b}
	tg := NewGroup("tg") // transformed: children excluded
	tg.Transform.Rotation = 10
	tg.Children = []*Layer{NewSolid("c", 0, 0, 5, 5, RGBA{}.NRGBA())}
	d.Root.Children = []*Layer{a, hid, g, tg}

	boxes, ids := FlattenLeaves(d)
	if len(boxes) != 2 || len(ids) != 2 || ids[0] != a.ID || ids[1] != b.ID {
		t.Fatalf("flatten: boxes=%d ids=%v", len(boxes), ids)
	}
	if boxes[0].SX != 1 || boxes[0].SY != 1 {
		t.Fatalf("zero scale must fold to 1: %+v", boxes[0])
	}
}

func TestHitTestTopmostAndRotation(t *testing.T) {
	lo := FlatBox{X: 0, Y: 0, W: 100, H: 100, SX: 1, SY: 1}
	hi := FlatBox{X: 50, Y: 50, W: 100, H: 100, SX: 1, SY: 1}
	if got := HitTest([]FlatBox{lo, hi}, 75, 75); got != 1 {
		t.Fatalf("topmost wins: got %d", got)
	}
	if got := HitTest([]FlatBox{lo, hi}, 10, 10); got != 0 {
		t.Fatalf("lower only: got %d", got)
	}
	if got := HitTest([]FlatBox{lo, hi}, 500, 500); got != -1 {
		t.Fatalf("miss: got %d", got)
	}
	// 45°-rotated box: former corner region no longer contains the point
	rot := FlatBox{X: 0, Y: 0, W: 100, H: 100, SX: 1, SY: 1, Rot: 45}
	if rot.Contains(2, 2) {
		t.Fatal("rotated box must not contain its old corner")
	}
	if !rot.Contains(50, 2) {
		t.Fatal("rotated box must contain its new top vertex region")
	}
}

func TestMapInvMapRoundTrip(t *testing.T) {
	b := FlatBox{X: 40, Y: 60, W: 200, H: 100, SX: 1.5, SY: 0.75, Rot: 30}
	for _, p := range [][2]float64{{0, 0}, {200, 100}, {100, 50}, {13, 87}} {
		dx, dy := b.Map(p[0], p[1])
		cx, cy, ok := b.InvMap(dx, dy)
		if !ok || !near(cx, p[0]) || !near(cy, p[1]) {
			t.Fatalf("roundtrip %v -> (%v,%v) -> (%v,%v)", p, dx, dy, cx, cy)
		}
	}
}

func TestHandleAtCornersAndRotate(t *testing.T) {
	b := FlatBox{X: 100, Y: 100, W: 200, H: 100, SX: 1, SY: 1}
	if h := HandleAt(b, 100, 100, 10, 30); h != HandleNW {
		t.Fatalf("nw: got %d", h)
	}
	if h := HandleAt(b, 300, 150, 10, 30); h != HandleE {
		t.Fatalf("e: got %d", h)
	}
	if h := HandleAt(b, 200, 70, 10, 30); h != HandleRotate {
		t.Fatalf("rotate: got %d", h)
	}
	if h := HandleAt(b, 200, 150, 10, 30); h != HandleNone {
		t.Fatalf("center is no handle: got %d", h)
	}
}

func TestSnapMoveToCanvasCenter(t *testing.T) {
	// box center at 495 proposed; canvas center 500 within thresh 8 → snaps +5
	b := FlatBox{X: 445, Y: 100, W: 100, H: 50, SX: 1, SY: 1}
	dx, dy, guides := SnapMove([]FlatBox{b}, 0, 0, 0, 8, 1000, 600)
	if !near(dx, 5) || dy != 0 {
		t.Fatalf("snap dx=%v dy=%v", dx, dy)
	}
	if len(guides) != 1 || !guides[0].Vert || !near(guides[0].Pos, 500) {
		t.Fatalf("guides=%v", guides)
	}
}

func TestSnapMoveToOtherBoxEdge(t *testing.T) {
	mover := FlatBox{X: 0, Y: 0, W: 100, H: 100, SX: 1, SY: 1}
	anchor := FlatBox{X: 300, Y: 0, W: 100, H: 100, SX: 1, SY: 1}
	// proposed right edge at 297 → snaps to anchor's left edge 300
	dx, _, guides := SnapMove([]FlatBox{mover, anchor}, 0, 197, 0, 8, 2000, 2000)
	if !near(dx, 200) {
		t.Fatalf("dx=%v", dx)
	}
	found := false
	for _, g := range guides {
		if g.Vert && near(g.Pos, 300) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected vert guide at 300, got %v", guides)
	}
}

func TestResizeBoxSEAnchorsNW(t *testing.T) {
	b := FlatBox{X: 100, Y: 100, W: 200, H: 100, SX: 1, SY: 1}
	nw, nh, nx, ny := ResizeBox(b, HandleSE, 400, 300, false)
	if !near(nw, 300) || !near(nh, 200) || !near(nx, 100) || !near(ny, 100) {
		t.Fatalf("se resize: %v %v %v %v", nw, nh, nx, ny)
	}
}

func TestResizeBoxNWAnchorsSE(t *testing.T) {
	b := FlatBox{X: 100, Y: 100, W: 200, H: 100, SX: 1, SY: 1}
	// drag NW corner to (150,150): box becomes 150×50, SE corner stays (300,200)
	nw, nh, nx, ny := ResizeBox(b, HandleNW, 150, 150, false)
	if !near(nw, 150) || !near(nh, 50) || !near(nx, 150) || !near(ny, 150) {
		t.Fatalf("nw resize: %v %v %v %v", nw, nh, nx, ny)
	}
}

func TestResizeBoxUniformCorner(t *testing.T) {
	b := FlatBox{X: 0, Y: 0, W: 200, H: 100, SX: 1, SY: 1}
	// drag SE to (400,110): kw=2 dominates → 400×200, anchored at origin
	nw, nh, nx, ny := ResizeBox(b, HandleSE, 400, 110, true)
	if !near(nw, 400) || !near(nh, 200) || !near(nx, 0) || !near(ny, 0) {
		t.Fatalf("uniform: %v %v %v %v", nw, nh, nx, ny)
	}
}

func TestResizeBoxRotatedAnchorStable(t *testing.T) {
	b := FlatBox{X: 100, Y: 100, W: 200, H: 100, SX: 1, SY: 1, Rot: 30}
	ax, ay := b.Map(0, 0) // NW anchor for a SE drag
	px, py := b.Map(250, 130)
	nw, nh, nx, ny := ResizeBox(b, HandleSE, px, py, false)
	nb := FlatBox{X: nx, Y: ny, W: nw, H: nh, SX: 1, SY: 1, Rot: 30}
	gx, gy := nb.Map(0, 0)
	if !near(gx, ax) || !near(gy, ay) {
		t.Fatalf("anchor drifted: (%v,%v) vs (%v,%v)", gx, gy, ax, ay)
	}
	if !near(nw, 250) || !near(nh, 130) {
		t.Fatalf("size: %v×%v", nw, nh)
	}
}

func TestResizeBoxMinClamp(t *testing.T) {
	b := FlatBox{X: 0, Y: 0, W: 100, H: 100, SX: 1, SY: 1}
	nw, nh, _, _ := ResizeBox(b, HandleSE, 2, 2, false)
	if nw != minSizePx || nh != minSizePx {
		t.Fatalf("clamp: %v×%v", nw, nh)
	}
}

func TestRotateFrom(t *testing.T) {
	if r := RotateFrom(10, 0, 35, false); !near(r, 45) {
		t.Fatalf("rotate: %v", r)
	}
	if r := RotateFrom(10, 0, 33, true); !near(r, 45) {
		t.Fatalf("snap15: %v", r)
	}
	if r := RotateFrom(170, 0, 30, false); !near(r, -160) {
		t.Fatalf("normalize: %v", r)
	}
}
