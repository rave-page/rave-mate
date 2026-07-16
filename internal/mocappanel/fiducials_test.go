package mocappanel

// v1.1 fiducial tests (contract §8b): the three inverted-parity corner cells, anchor point
// constants, invisibility to the v1-exact decode, and the DecodeSampled seam.

import (
	"reflect"
	"testing"
)

func TestFiducialCells(t *testing.T) {
	img := goldenImage()

	// Inverted parity lands all three on B=0 with R != G (never a valid cell colour).
	checks := []struct {
		name string
		x, y int
		want [4]uint8
	}{
		{"TR 0xC33C", AnchorTRX, AnchorTRY, [4]uint8{0xC3, 0x3C, 0x00, 255}},
		{"BL 0xA55A", AnchorBLX, AnchorBLY, [4]uint8{0xA5, 0x5A, 0x00, 255}},
		{"BR 0x5AA5", AnchorBRX, AnchorBRY, [4]uint8{0x5A, 0xA5, 0x00, 255}},
		{"TL = MAGIC0", AnchorTLX, AnchorTLY, [4]uint8{0x52, 0x50, 0x02, 255}},
	}
	for _, c := range checks {
		if got := pxAt(img, c.x, c.y); got != c.want {
			t.Errorf("%s at (%d,%d): got %v want %v", c.name, c.x, c.y, got, c.want)
		}
	}

	// Every pixel of each fiducial cell painted: TR meta cell spans x[1888,1920) y[0,32);
	// BL data cell x[0,16) y[1056,1072); BR x[1904,1920) y[1056,1072).
	for _, p := range [][2]int{{1888, 0}, {1919, 31}} {
		if got := pxAt(img, p[0], p[1]); got != ([4]uint8{0xC3, 0x3C, 0x00, 255}) {
			t.Errorf("TR corner (%d,%d)=%v not painted", p[0], p[1], got)
		}
	}
	if got := pxAt(img, 0, 1071); got != ([4]uint8{0xA5, 0x5A, 0x00, 255}) {
		t.Errorf("BL corner=%v not painted", got)
	}
	if got := pxAt(img, 1919, 1056); got != ([4]uint8{0x5A, 0xA5, 0x00, 255}) {
		t.Errorf("BR corner=%v not painted", got)
	}
}

func TestFidBytesInvertedParity(t *testing.T) {
	for _, v := range []uint16{FidTR, FidBL, FidBR} {
		r, g, b := FidBytes(v)
		if b != 0 || r == g {
			t.Errorf("FidBytes(%#04x) = (%#02x,%#02x,%#02x): want B=0, R!=G", v, r, g, b)
		}
		// A valid cell of the same value differs by the full parity inversion.
		if _, _, vb := CellBytes(v); vb != b^0xFF {
			t.Errorf("FidBytes(%#04x) B=%#02x not the inversion of CellBytes B=%#02x", v, b, vb)
		}
	}
}

func TestAnchorConstantsMatchGeometry(t *testing.T) {
	// The frozen anchor constants are the fiducial cell centres per §1 geometry.
	if x, y := MetaSample(ColMagic0); x != AnchorTLX || y != AnchorTLY {
		t.Errorf("TL anchor (%d,%d) != MetaSample(0) (%d,%d)", AnchorTLX, AnchorTLY, x, y)
	}
	if x, y := MetaSample(ColFidTR); x != AnchorTRX || y != AnchorTRY {
		t.Errorf("TR anchor (%d,%d) != MetaSample(59) (%d,%d)", AnchorTRX, AnchorTRY, x, y)
	}
	if x, y := DataSample(FidRow*DataCols + FidBLCol); x != AnchorBLX || y != AnchorBLY {
		t.Errorf("BL anchor (%d,%d) != DataSample(0,64) (%d,%d)", AnchorBLX, AnchorBLY, x, y)
	}
	if x, y := DataSample(FidRow*DataCols + FidBRCol); x != AnchorBRX || y != AnchorBRY {
		t.Errorf("BR anchor (%d,%d) != DataSample(119,64) (%d,%d)", AnchorBRX, AnchorBRY, x, y)
	}
}

// TestFiducialsInvisibleToV1 pins the addendum's core promise: the v1-exact decode of a
// v1.1 frame (fiducials drawn) reproduces the golden fields untouched.
func TestFiducialsInvisibleToV1(t *testing.T) {
	wantH, wantD := GoldenFrame()
	gotH, gotD, err := DecodeFrame(Encode(wantH, wantD))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if gotH != wantH || !reflect.DeepEqual(gotD, wantD) {
		t.Fatal("fiducials leaked into the v1 decode")
	}
}

func TestDecodeSampledMatchesDecodeFrame(t *testing.T) {
	img := goldenImage()
	wantH, wantD, err := DecodeFrame(img)
	if err != nil {
		t.Fatalf("DecodeFrame: %v", err)
	}
	gotH, gotD, err := DecodeSampled(ImageSampler(img))
	if err != nil {
		t.Fatalf("DecodeSampled: %v", err)
	}
	if gotH != wantH || !reflect.DeepEqual(gotD, wantD) {
		t.Fatal("DecodeSampled diverges from DecodeFrame on the same pixels")
	}
}
