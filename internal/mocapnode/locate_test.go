package mocapnode

// Locator + rectified-decode tests. All synthetic: the v1.1 golden panel warped through known
// homographies with bilinear resampling (scale down/up, slight rotation, off-axis keystone),
// the real Unity capture fixture, and negative frames. ffmpeg never runs here.

import (
	"errors"
	"image"
	"image/png"
	"math"
	"math/rand"
	"os"
	"reflect"
	"testing"

	"rave.page/mate/internal/mocappanel"
)

// goldenPanel returns the encoded v1.1 golden panel (fiducials drawn by mocappanel.Encode).
func goldenPanel() *image.NRGBA {
	h, d := mocappanel.GoldenFrame()
	return mocappanel.Encode(h, d)
}

// affine builds the affine homography [a b c; d e f; 0 0 1].
func affine(a, b, c, d, e, f float64) Homography {
	return Homography{a, b, c, d, e, f, 0, 0}
}

// invert3x3 inverts a homography (adjugate / det), renormalized to h33=1.
func invert3x3(t *testing.T, m Homography) Homography {
	t.Helper()
	a, b, c, d, e, f, g, h := m[0], m[1], m[2], m[3], m[4], m[5], m[6], m[7]
	adj := [9]float64{
		e - f*h, -(b - c*h), b*f - c*e,
		-(d - f*g), a - c*g, -(a*f - c*d),
		d*h - e*g, -(a*h - b*g), a*e - b*d,
	}
	det := a*adj[0] + b*adj[3] + c*adj[6]
	if math.Abs(det) < 1e-12 || math.Abs(adj[8]/det) < 1e-12 {
		t.Fatal("invert3x3: singular")
	}
	var out Homography
	for k := 0; k < 8; k++ {
		out[k] = adj[k] / adj[8]
	}
	return out
}

// bilinearAt samples img at a continuous point, black outside.
func bilinearAt(img *image.NRGBA, x, y float64) (r, g, b uint8) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	if x < 0 || y < 0 || x > float64(w-1) || y > float64(h-1) {
		return 0, 0, 0
	}
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	x1, y1 := x0+1, y0+1
	if x1 > w-1 {
		x1 = w - 1
	}
	if y1 > h-1 {
		y1 = h - 1
	}
	fx, fy := x-float64(x0), y-float64(y0)
	mix := func(ch func(px, py int) float64) uint8 {
		top := ch(x0, y0)*(1-fx) + ch(x1, y0)*fx
		bot := ch(x0, y1)*(1-fx) + ch(x1, y1)*fx
		return uint8(math.Round(top*(1-fy) + bot*fy))
	}
	at := func(px, py int, off int) float64 {
		return float64(img.Pix[py*img.Stride+px*4+off])
	}
	return mix(func(px, py int) float64 { return at(px, py, 0) }),
		mix(func(px, py int) float64 { return at(px, py, 1) }),
		mix(func(px, py int) float64 { return at(px, py, 2) })
}

// warpGolden renders the golden panel through fwd (canonical->canvas) onto a w x h black canvas
// by inverse-mapping every canvas pixel and bilinear-sampling the panel.
func warpGolden(t *testing.T, fwd Homography, w, h int) Frame {
	t.Helper()
	src := goldenPanel()
	inv := invert3x3(t, fwd)
	pix := make([]byte, w*h*3)
	i := 0
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			sx, sy := inv.Apply(float64(x), float64(y))
			r, g, b := bilinearAt(src, sx, sy)
			pix[i], pix[i+1], pix[i+2] = r, g, b
			i += 3
		}
	}
	return Frame{Pix: pix, W: w, H: h, Stride: w * 3, Fmt: FmtRGB24}
}

// assertGoldenDecode checks a rectified decode reproduces EVERY golden field exactly.
func assertGoldenDecode(t *testing.T, lk Lock, f *Frame) {
	t.Helper()
	gotH, gotD, err := mocappanel.DecodeSampled(lk.Sampler(f))
	if err != nil {
		t.Fatalf("rectified decode: %v", err)
	}
	wantH, wantD := mocappanel.GoldenFrame()
	if gotH != wantH {
		t.Fatalf("header mismatch:\n got %+v\nwant %+v", gotH, wantH)
	}
	if !reflect.DeepEqual(gotD, wantD) {
		t.Fatalf("dancers mismatch:\n got %+v\nwant %+v", gotD, wantD)
	}
}

func TestLocateWarpedGolden(t *testing.T) {
	const rad = math.Pi / 180
	rot, scale := 1.5*rad, 0.95
	cases := []struct {
		name string
		fwd  Homography
		w, h int
	}{
		{"scale0.6+offset", affine(0.6, 0, 40, 0, 0.6, 60), 1600, 900},
		{"scale1.3", affine(1.3, 0, 16, 0, 1.3, 12), 2560, 1440},
		{"rotate1.5deg", affine(
			scale*math.Cos(rot), -scale*math.Sin(rot), 60,
			scale*math.Sin(rot), scale*math.Cos(rot), 40), 1920, 1200},
	}

	// Keystone ~25 deg off-axis dock: right edge foreshortened to ~cos(25deg) of the left.
	keystone, err := SolveHomography(
		[4][2]float64{{0, 0}, {1920, 0}, {0, 1080}, {1920, 1080}},
		[4][2]float64{{140, 90}, {1740, 125}, {150, 1010}, {1730, 958}})
	if err != nil {
		t.Fatalf("keystone solve: %v", err)
	}
	cases = append(cases, struct {
		name string
		fwd  Homography
		w, h int
	}{"keystone25deg", keystone, 1920, 1080})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := warpGolden(t, tc.fwd, tc.w, tc.h)
			lk, err := Locate(&fr)
			if err != nil {
				t.Fatalf("locate: %v", err)
			}
			if lk.Identity {
				t.Error("warped frame took the identity fast-path")
			}
			for i, a := range lk.Anchors {
				wx, wy := tc.fwd.Apply(canonicalAnchors[i][0], canonicalAnchors[i][1])
				if d := math.Hypot(a[0]-wx, a[1]-wy); d > 2 {
					t.Errorf("anchor %d off by %.2f px (got %.2f,%.2f want %.2f,%.2f)", i, d, a[0], a[1], wx, wy)
				}
			}
			assertGoldenDecode(t, lk, &fr)
			if !lk.Revalidate(&fr) {
				t.Error("revalidate failed on the frame that produced the lock")
			}
		})
	}
}

// TestLocateUnityGoldenIdentity runs the locator on the REAL Unity capture (which carries the
// v1.1 fiducials): native 1920x1080 must hit the identity fast-path and decode the golden
// fields exactly.
func TestLocateUnityGoldenIdentity(t *testing.T) {
	f, err := os.Open("../mocappanel/testdata/unity_golden.png")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	defer func() { _ = f.Close() }()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("png: %v", err)
	}
	fr := imageToFrame(img)

	lk, err := Locate(&fr)
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if !lk.Identity {
		t.Errorf("native capture missed the identity fast-path (anchors %v)", lk.Anchors)
	}
	for i, a := range lk.Anchors {
		if d := math.Hypot(a[0]-canonicalAnchors[i][0], a[1]-canonicalAnchors[i][1]); d > 1 {
			t.Errorf("anchor %d off canonical by %.2f px", i, d)
		}
	}
	assertGoldenDecode(t, lk, &fr)
}

func TestLocateNegative(t *testing.T) {
	// Black frame: no fiducials, no lock, no panic.
	black := Frame{Pix: make([]byte, 1920*1080*3), W: 1920, H: 1080, Stride: 1920 * 3, Fmt: FmtRGB24}
	if _, err := Locate(&black); !errors.Is(err, ErrNoLock) {
		t.Errorf("black frame: err=%v want ErrNoLock", err)
	}

	// Deterministic noise: blobs (if any) can never pass validation.
	rng := rand.New(rand.NewSource(1))
	noise := Frame{Pix: make([]byte, 960*540*3), W: 960, H: 540, Stride: 960 * 3, Fmt: FmtRGB24}
	for i := range noise.Pix {
		noise.Pix[i] = byte(rng.Intn(256))
	}
	if _, err := Locate(&noise); !errors.Is(err, ErrNoLock) {
		t.Errorf("noise frame: err=%v want ErrNoLock", err)
	}
}

func TestSolveHomography(t *testing.T) {
	// Exact fit reproduces a known projective map on points OFF the fit quad.
	want := Homography{1.1, 0.02, 30, -0.03, 0.97, 55, 1e-5, -2e-5}
	var src, dst [4][2]float64
	for i, p := range [4][2]float64{{0, 0}, {1920, 0}, {0, 1080}, {1920, 1080}} {
		src[i] = p
		x, y := want.Apply(p[0], p[1])
		dst[i] = [2]float64{x, y}
	}
	got, err := SolveHomography(src, dst)
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	for _, p := range [][2]float64{{16, 16}, {1904, 16}, {8, 1064}, {1912, 1064}, {960, 540}} {
		wx, wy := want.Apply(p[0], p[1])
		gx, gy := got.Apply(p[0], p[1])
		if math.Hypot(gx-wx, gy-wy) > 1e-6 {
			t.Errorf("point %v: got (%f,%f) want (%f,%f)", p, gx, gy, wx, wy)
		}
	}

	// Degenerate quad (three collinear SOURCE points -> dependent DLT rows) errors instead of
	// exploding.
	if _, err := SolveHomography([4][2]float64{{0, 0}, {1, 1}, {2, 2}, {5, 0}}, dst); err == nil {
		t.Error("collinear source quad accepted")
	}
}
