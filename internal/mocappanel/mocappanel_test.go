package mocappanel

// Stateless-core tests: golden wire bytes (hand-computed), round trip, smallest-three
// idempotence, corruption semantics, geometry reject, calibration, unity fixture.

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"
)

func pxAt(img *image.NRGBA, x, y int) [4]uint8 {
	c := img.NRGBAAt(x, y)
	return [4]uint8{c.R, c.G, c.B, c.A}
}

func goldenImage() *image.NRGBA {
	h, d := GoldenFrame()
	return Encode(h, d)
}

// breakParity repaints data cell idx with a B channel far off the R^G parity (value bytes kept).
func breakParity(img *image.NRGBA, idx int) {
	x0 := (idx % DataCols) * DataCellPx
	y0 := DataY0 + (idx/DataCols)*DataCellPx
	for y := y0; y < y0+DataCellPx; y++ {
		for x := x0; x < x0+DataCellPx; x++ {
			c := img.NRGBAAt(x, y)
			c.B = (c.R ^ c.G) ^ 0x80
			img.SetNRGBA(x, y, c)
		}
	}
}

func TestGoldenWireCells(t *testing.T) {
	img := goldenImage()

	// Hand-computed wire bytes. Multi-cell ints big-endian: serverTimeMs 1234567890123 =
	// 0x0000_011F_71FB_04CB -> cells 12..15. W(0,0) = 0<<30|512<<20|512<<10|432 = 0x200801B0.
	// W(1,9) = 1<<30|569<<20|399<<10|553 = 0x63963E29 at dancer1 base 52 + 8 + 2*9 = idx 78.
	checks := []struct {
		name string
		x, y int
		want [4]uint8
	}{
		{"MAGIC0 0x5250", 16, 16, [4]uint8{0x52, 0x50, 0x02, 255}},
		{"MAGIC1 0x4D31", 48, 16, [4]uint8{0x4D, 0x31, 0x7C, 255}},
		{"cal black", 80, 16, [4]uint8{0, 0, 0, 255}},
		{"cal mid", 112, 16, [4]uint8{128, 128, 128, 255}},
		{"cal white", 144, 16, [4]uint8{255, 255, 255, 255}},
		{"serverTimeMs cell12", 12*32 + 16, 16, [4]uint8{0x00, 0x00, 0x00, 255}},
		{"serverTimeMs cell13", 13*32 + 16, 16, [4]uint8{0x01, 0x1F, 0x1E, 255}},
		{"serverTimeMs cell14", 14*32 + 16, 16, [4]uint8{0x71, 0xFB, 0x8A, 255}},
		{"serverTimeMs cell15", 15*32 + 16, 16, [4]uint8{0x04, 0xCB, 0xCF, 255}},
		{"d0 localId idx0", 8, 40, [4]uint8{0x00, 0x07, 0x07, 255}},
		{"d0 hips qx idx4", 4*16 + 8, 40, [4]uint8{0x94, 0x00, 0x94, 255}},
		{"d0 W(0,0) hi idx8", 8*16 + 8, 40, [4]uint8{0x20, 0x08, 0x28, 255}},
		{"d0 W(0,0) lo idx9", 9*16 + 8, 40, [4]uint8{0x01, 0xB0, 0xB1, 255}},
		{"d1 mask hi idx54", 54*16 + 8, 40, [4]uint8{0x00, 0x30, 0x30, 255}},
		{"d1 W(1,9) hi idx78", 78*16 + 8, 40, [4]uint8{0x63, 0x96, 0xF5, 255}},
		{"d1 W(1,9) lo idx79", 79*16 + 8, 40, [4]uint8{0x3E, 0x29, 0x17, 255}},
	}
	for _, c := range checks {
		if got := pxAt(img, c.x, c.y); got != c.want {
			t.Errorf("%s at (%d,%d): got %v want %v", c.name, c.x, c.y, got, c.want)
		}
	}

	// Every pixel of a cell painted, not just the sample point: MAGIC0 corners.
	for _, p := range [][2]int{{0, 0}, {31, 0}, {0, 31}, {31, 31}} {
		if got := pxAt(img, p[0], p[1]); got != ([4]uint8{0x52, 0x50, 0x02, 255}) {
			t.Errorf("MAGIC0 corner (%d,%d)=%v not painted", p[0], p[1], got)
		}
	}
	// Data cell corner too (hips qx cell spans x[64,80) y[32,48)).
	if got := pxAt(img, 64, 32); got != ([4]uint8{0x94, 0x00, 0x94, 255}) {
		t.Errorf("hips qx cell corner=%v not painted", got)
	}
	// Unused pixels black opaque (8 px bottom margin).
	if got := pxAt(img, 0, CanvasH-1); got != ([4]uint8{0, 0, 0, 255}) {
		t.Errorf("bottom margin=%v want opaque black", got)
	}
}

func TestGoldenRoundTrip(t *testing.T) {
	wantH, wantD := GoldenFrame()
	img := Encode(wantH, wantD)

	gotH, gotD, err := DecodeFrame(img)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if gotH != wantH {
		t.Fatalf("header mismatch:\n got %+v\nwant %+v", gotH, wantH)
	}
	if !reflect.DeepEqual(gotD, wantD) {
		t.Fatalf("dancers mismatch:\n got %+v\nwant %+v", gotD, wantD)
	}

	// Re-encode of the decode is byte-identical.
	img2 := Encode(gotH, gotD)
	if img.Rect != img2.Rect || img.Stride != img2.Stride || !bytes.Equal(img.Pix, img2.Pix) {
		t.Fatal("re-encode not byte-identical")
	}
}

func TestGoldenWIdempotence(t *testing.T) {
	_, dancers := GoldenFrame()
	for d, dc := range dancers {
		for k, present := range dc.Present {
			if !present {
				continue
			}
			w := GoldenW(d, k)
			q, ok := UnpackQuat(w)
			if !ok {
				t.Fatalf("W(%d,%d)=%#x norm-rejected", d, k, w)
			}
			if got := PackQuat(q); got != w {
				t.Errorf("W(%d,%d): repack %#x != %#x", d, k, got, w)
			}
		}
	}
}

func TestParityCorruptionDropsBone(t *testing.T) {
	img := goldenImage()
	breakParity(img, OffBones+2*3) // dancer0 slot 3 hi cell (idx 14)

	_, dancers, err := DecodeFrame(img)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dancers) != 2 {
		t.Fatalf("dancers=%d want 2 (bone loss never drops the dancer)", len(dancers))
	}
	d0 := dancers[0]
	if d0.Present[3] {
		t.Error("slot 3 still present after parity corruption")
	}
	if d0.Rots[3] != 0 || d0.Quats[3] != ([4]float64{}) {
		t.Errorf("slot 3 data not zeroed: rot=%#x quat=%v", d0.Rots[3], d0.Quats[3])
	}
	if d0.BoneMask != 0x003FFFFF {
		t.Errorf("wire mask changed: %#x", d0.BoneMask)
	}
	if !d0.Present[2] || !d0.Present[4] {
		t.Error("neighbour slots damaged (fixed-slot semantics violated)")
	}
}

func TestCoreMaskCorruptionRejectsDancer(t *testing.T) {
	img := goldenImage()
	putData(img, OffBoneMask+1, 0xFFFE) // dancer0 mask lo: clear bit0 (hips) -> core violation

	_, dancers, err := DecodeFrame(img)
	if err != nil {
		t.Fatalf("decode: %v (dancer problems must not reject the frame)", err)
	}
	if len(dancers) != 1 || dancers[0].LocalID != 9 {
		t.Fatalf("want only dancer localId=9 to survive, got %d dancers", len(dancers))
	}
}

func TestGeometryReject(t *testing.T) {
	_, _, err := DecodeFrame(image.NewNRGBA(image.Rect(0, 0, 1280, 720)))
	if err == nil {
		t.Fatal("1280x720 accepted")
	}
	if !strings.Contains(err.Error(), "1920x1080") || !strings.Contains(err.Error(), "1280x720") {
		t.Errorf("error lacks expected-vs-actual: %v", err)
	}
}

func TestCalibration(t *testing.T) {
	img := goldenImage()
	cal, err := calibrate(ImageSampler(img))
	if err != nil {
		t.Fatalf("calibrate: %v", err)
	}
	if cal.MidWarn {
		t.Error("MidWarn on a native capture")
	}
	// Identity on exact BLACK/WHITE.
	for _, v := range [][3]uint8{{0x52, 0x50, 0x02}, {0, 0, 0}, {255, 255, 255}, {128, 128, 128}} {
		if got := cal.Apply(v); got != v {
			t.Errorf("identity Apply(%v)=%v", v, got)
		}
	}

	// Skewed capture: black->10, white->245 recovers the endpoints and mid.
	skew := Calib{black: [3]float64{10, 10, 10}, scale: [3]float64{255.0 / 235, 255.0 / 235, 255.0 / 235}}
	if got := skew.Apply([3]uint8{10, 10, 10}); got != ([3]uint8{0, 0, 0}) {
		t.Errorf("skew black=%v", got)
	}
	if got := skew.Apply([3]uint8{245, 245, 245}); got != ([3]uint8{255, 255, 255}) {
		t.Errorf("skew white=%v", got)
	}
	if got := skew.Apply([3]uint8{128, 128, 128}); got != ([3]uint8{128, 128, 128}) {
		t.Errorf("skew mid=%v want 128 (128.04 rounds down)", got)
	}
	// Out-of-range clamps.
	if got := skew.Apply([3]uint8{5, 250, 128}); got[0] != 0 || got[1] != 255 {
		t.Errorf("clamp=%v", got)
	}

	// MID off by >6 -> warn only, frame still decodes.
	warned := goldenImage()
	fillRect(warned, ColCalMid*MetaCellPx, 0, MetaCellPx, color.NRGBA{150, 150, 150, 255})
	cal, err = calibrate(ImageSampler(warned))
	if err != nil {
		t.Fatalf("calibrate warned: %v", err)
	}
	if !cal.MidWarn {
		t.Error("MidWarn not raised for MID=150")
	}
	if _, _, err := DecodeFrame(warned); err != nil {
		t.Errorf("MID warn rejected the frame: %v", err)
	}

	// Degenerate calibration (white <= black) is a frame reject.
	dead := goldenImage()
	fillRect(dead, ColCalWhite*MetaCellPx, 0, MetaCellPx, color.NRGBA{0, 0, 0, 255})
	if _, _, err := DecodeFrame(dead); err == nil {
		t.Error("degenerate calibration accepted")
	}
}

// TestUnityGoldenFixture decodes the Unity-captured golden frame once it lands from the world
// side; until then the test skips.
func TestUnityGoldenFixture(t *testing.T) {
	f, err := os.Open("testdata/unity_golden.png")
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("testdata/unity_golden.png not yet captured (lands with the world-side encoder)")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("png: %v", err)
	}

	gotH, gotD, err := DecodeFrame(img)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantH, wantD := GoldenFrame()
	if gotH != wantH {
		t.Errorf("header mismatch:\n got %+v\nwant %+v", gotH, wantH)
	}
	if len(gotD) != len(wantD) {
		t.Fatalf("dancers=%d want %d", len(gotD), len(wantD))
	}
	for i := range wantD {
		if gotD[i].HipsQ != wantD[i].HipsQ {
			t.Errorf("dancer %d hips q: got %#v want %#v", i, gotD[i].HipsQ, wantD[i].HipsQ)
		}
		if !reflect.DeepEqual(gotD[i].Rots, wantD[i].Rots) {
			t.Errorf("dancer %d wire words: got %#v want %#v", i, gotD[i].Rots, wantD[i].Rots)
		}
		if !reflect.DeepEqual(gotD[i], wantD[i]) {
			t.Errorf("dancer %d mismatch:\n got %+v\nwant %+v", i, gotD[i], wantD[i])
		}
	}
}
