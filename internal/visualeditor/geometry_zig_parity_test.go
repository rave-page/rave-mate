//go:build zigdsp && cgo

package visualeditor

// Parity gate: the edgeo kernel must match the Go geometry bit-for-bit on the
// flat-box form. Skips when the lib isn't linked. Inputs stay far below Go's
// trig-reduce threshold (the kernel's parity range).

import (
	"math"
	"math/rand"
	"testing"

	"rave.page/mate/internal/zignative"
)

func requireZigGeo(t testing.TB) {
	t.Helper()
	if !zignative.Available() {
		t.Skip("zig core not available")
	}
}

func randGeoBox(rng *rand.Rand) FlatBox {
	b := FlatBox{
		X: rng.Float64()*4000 - 1000, Y: rng.Float64()*4000 - 1000,
		W: rng.Float64() * 2000, H: rng.Float64() * 2000,
		SX: rng.Float64()*6 - 3, SY: rng.Float64()*6 - 3,
		Rot: rng.Float64()*1440 - 720,
	}
	switch rng.Intn(8) { // pin the special paths
	case 0:
		b.SX = 1e-12 // degenerate det → InvMap false
	case 1:
		b.Rot = 0
	case 2:
		b.SX, b.SY = 1, 1
	case 3:
		b.SY = 1e-12 // rotate-anchor guard
	}
	return b
}

func eqBits(a, b float64) bool { return math.Float64bits(a) == math.Float64bits(b) }

// TestZigGeoParity fuzzes all six entry points, Go body vs kernel, bit-exact.
func TestZigGeoParity(t *testing.T) {
	requireZigGeo(t)
	rng := rand.New(rand.NewSource(11))
	for iter := 0; iter < 3000; iter++ {
		n := 1 + rng.Intn(7)
		boxes := make([]FlatBox, n)
		for i := range boxes {
			boxes[i] = randGeoBox(rng)
		}
		px, py := rng.Float64()*5000-1500, rng.Float64()*5000-1500
		b := boxes[rng.Intn(n)]

		if g, z := hitTestGo(boxes, px, py), zigHitTest(boxes, px, py); g != z {
			t.Fatalf("iter %d: HitTest go=%d zig=%d", iter, g, z)
		}

		tol, rotOff := 1+rng.Float64()*30, 10+rng.Float64()*50
		if g, z := handleAtGo(b, px, py, tol, rotOff), zigHandleAt(b, px, py, tol, rotOff); g != z {
			t.Fatalf("iter %d: HandleAt go=%d zig=%d box=%+v p=(%v,%v)", iter, g, z, b, px, py)
		}

		dx, dy := rng.Float64()*600-300, rng.Float64()*600-300
		thresh := 1 + rng.Float64()*20
		docW, docH := 320+rng.Float64()*3000, 320+rng.Float64()*3000
		mi := rng.Intn(n)
		gdx, gdy, gg := snapMoveGo(boxes, mi, dx, dy, thresh, docW, docH)
		zdx, zdy, zg := zigSnapMove(boxes, mi, dx, dy, thresh, docW, docH)
		if !eqBits(gdx, zdx) || !eqBits(gdy, zdy) || len(gg) != len(zg) {
			t.Fatalf("iter %d: SnapMove go=(%x,%x,%d) zig=(%x,%x,%d)", iter,
				math.Float64bits(gdx), math.Float64bits(gdy), len(gg),
				math.Float64bits(zdx), math.Float64bits(zdy), len(zg))
		}
		for i := range gg {
			if gg[i].Vert != zg[i].Vert || !eqBits(gg[i].Pos, zg[i].Pos) {
				t.Fatalf("iter %d: SnapMove guide %d go=%+v zig=%+v", iter, i, gg[i], zg[i])
			}
		}

		for hd := HandleNW; hd <= HandleW; hd++ {
			uniform := rng.Intn(2) == 0
			gw, gh, gx, gy := resizeBoxGo(b, hd, px, py, uniform)
			zw, zh, zx, zy := zigResizeBox(b, hd, px, py, uniform)
			if !eqBits(gw, zw) || !eqBits(gh, zh) || !eqBits(gx, zx) || !eqBits(gy, zy) {
				t.Fatalf("iter %d: ResizeBox hd=%d uni=%v go=(%v,%v,%v,%v) zig=(%v,%v,%v,%v) box=%+v",
					iter, hd, uniform, gw, gh, gx, gy, zw, zh, zx, zy, b)
			}
		}

		if g, z := angleAtGo(b, px, py), zigAngleAt(b, px, py); !eqBits(g, z) {
			t.Fatalf("iter %d: AngleAt go=%x zig=%x box=%+v p=(%v,%v)", iter,
				math.Float64bits(g), math.Float64bits(z), b, px, py)
		}

		or, da, na := rng.Float64()*2000-1000, rng.Float64()*2000-1000, rng.Float64()*2000-1000
		snap := rng.Intn(2) == 0
		if g, z := rotateFromGo(or, da, na, snap), zigRotateFrom(or, da, na, snap); !eqBits(g, z) {
			t.Fatalf("iter %d: RotateFrom(%v,%v,%v,%v) go=%x zig=%x", iter, or, da, na, snap,
				math.Float64bits(g), math.Float64bits(z))
		}
	}
}

func benchGeoBoxes(rng *rand.Rand, n int) []FlatBox {
	boxes := make([]FlatBox, n)
	for i := range boxes {
		boxes[i] = randGeoBox(rng)
	}
	return boxes
}

func BenchmarkSnapMoveGo(b *testing.B) {
	boxes := benchGeoBoxes(rand.New(rand.NewSource(12)), 24)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snapMoveGo(boxes, 3, 17, -9, 8, 1920, 1080)
	}
}

func BenchmarkSnapMoveZig(b *testing.B) {
	requireZigGeo(b)
	boxes := benchGeoBoxes(rand.New(rand.NewSource(12)), 24)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		zigSnapMove(boxes, 3, 17, -9, 8, 1920, 1080)
	}
}
