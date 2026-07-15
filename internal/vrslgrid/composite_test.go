package vrslgrid

import (
	"image"
	"testing"
)

// mapReader is a Reader backed by a universe→data map (Generation unused here).
type mapReader map[uint16][512]byte

func (m mapReader) Get(u uint16) ([512]byte, bool) { d, ok := m[u]; return d, ok }

// centre samples the middle of the 16-px cell at cell coords (cx,cy) offset by xOff.
func cellCentre(img *image.RGBA, xOff, cx, cy int) (r, g, b byte) {
	c := img.RGBAAt(xOff+cx*CellPx+CellPx/2, cy*CellPx+CellPx/2)
	return c.R, c.G, c.B
}

// metaCentre samples the middle of the 32-px metadata cell at (col,row).
func metaCentre(img *image.RGBA, col, row int) byte {
	return img.RGBAAt(MetaBandX0+col*MetaCellPx+MetaCellPx/2, row*MetaCellPx+MetaCellPx/2).R
}

func TestCompositeStandardDims(t *testing.T) {
	r := mapReader{0: {}}
	img := RenderComposite(r, CompositeSpec{Universes: []int{0}, Mono: true, Extended: false})
	if img.Bounds().Dx() != FrameWidth {
		t.Fatalf("width = %d, want %d", img.Bounds().Dx(), FrameWidth)
	}
	if img.Bounds().Dy() != FrameRefHeight {
		t.Fatalf("height = %d, want %d (1 universe fits the reference)", img.Bounds().Dy(), FrameRefHeight)
	}
}

func TestCompositeHeightGrowsWithUniverses(t *testing.T) {
	// 2 universes = 2*640 = 1280 > 1080 → frame grows to the grid height.
	img := RenderComposite(mapReader{}, CompositeSpec{Universes: []int{0, 1}, Mono: true})
	if got := img.Bounds().Dy(); got != 2*RowsPerUni*CellPx {
		t.Fatalf("height = %d, want %d", got, 2*RowsPerUni*CellPx)
	}
}

func TestCompositeStripCarriesValue(t *testing.T) {
	var d [512]byte
	d[0] = 200  // ch0 → cell (0,0)
	d[13] = 111 // ch13 → cell (0,1)
	r := mapReader{0: d}
	img := RenderComposite(r, CompositeSpec{Universes: []int{0}, Mono: true})
	if rr, _, _ := cellCentre(img, StripX0, 0, 0); rr != 200 {
		t.Fatalf("strip ch0 = %d, want 200", rr)
	}
	if rr, _, _ := cellCentre(img, StripX0, 0, 1); rr != 111 {
		t.Fatalf("strip ch13 = %d, want 111", rr)
	}
	// Standard mode leaves the left region black (no low grid).
	if rr, _, _ := cellCentre(img, LowGridX0, 0, 0); rr != 0 {
		t.Fatalf("standard-mode left region ch0 = %d, want 0 (black)", rr)
	}
}

func TestCompositeExtendedMirrorAndHeader(t *testing.T) {
	var d [512]byte
	d[5] = 77
	r := mapReader{0: d}
	img := RenderComposite(r, CompositeSpec{
		Universes: []int{2}, // baseUniverse low byte = 2, but data comes from universe 2 (unseen → 0)
		Mono:      true, Extended: true, FrameCounter: 42, LookID: 3, SceneID: 9, Blackout: 0,
	})
	// baseUniverse header
	if got := metaCentre(img, metaColBaseUni, 0); got != 2 {
		t.Fatalf("baseUniverse = %d, want 2", got)
	}
	if got := metaCentre(img, metaColUniCount, 0); got != 1 {
		t.Fatalf("universeCount = %d, want 1", got)
	}
	if got := metaCentre(img, metaColMagic0, 0); got != MagicR {
		t.Fatalf("magic0 = %#x, want %#x", got, MagicR)
	}
	if got := metaCentre(img, metaColMagic1, 0); got != MagicV {
		t.Fatalf("magic1 = %#x, want %#x", got, MagicV)
	}
	if got := metaCentre(img, metaColVersion, 0); got != Version {
		t.Fatalf("version = %d, want %d", got, Version)
	}
	if got := metaCentre(img, metaColFrameCtr, 0); got != 42 {
		t.Fatalf("frameCounter = %d, want 42", got)
	}
	if got := metaCentre(img, metaColFlags, 0); got&FlagLoFrameValid == 0 {
		t.Fatalf("flags = %#x, want loFrameValid set", got)
	}
	// calibration triad
	if got := metaCentre(img, metaColCal0, 0); got != 0 {
		t.Fatalf("cal0 = %d, want 0", got)
	}
	if got := metaCentre(img, metaColCal1, 0); got != 128 {
		t.Fatalf("cal1 = %d, want 128", got)
	}
	if got := metaCentre(img, metaColCal2, 0); got != 255 {
		t.Fatalf("cal2 = %d, want 255", got)
	}
	// semantic lanes (row 1)
	if got := metaCentre(img, metaLaneLookID, 1); got != 3 {
		t.Fatalf("lookId = %d, want 3", got)
	}
	if got := metaCentre(img, metaLaneSceneID, 1); got != 9 {
		t.Fatalf("sceneId = %d, want 9", got)
	}
}

func TestCompositeExtendedLowMirrorsHigh(t *testing.T) {
	var d [512]byte
	d[0] = 123
	r := mapReader{0: d}
	img := RenderComposite(r, CompositeSpec{Universes: []int{0}, Mono: true, Extended: true})
	hi, _, _ := cellCentre(img, StripX0, 0, 0)
	lo, _, _ := cellCentre(img, LowGridX0, 0, 0)
	if hi != 123 || lo != 123 {
		t.Fatalf("high=%d low=%d, want both 123 (bit-replicated)", hi, lo)
	}
}

func TestCRC8KnownVector(t *testing.T) {
	// CRC-8/ITU-ish, poly 0x07, init 0: "123456789" → 0xF4.
	if got := crc8([]byte("123456789")); got != 0xF4 {
		t.Fatalf("crc8(check) = %#x, want 0xF4", got)
	}
	if got := crc8(nil); got != 0 {
		t.Fatalf("crc8(empty) = %#x, want 0", got)
	}
}

func TestCRC8RoundTripFromPixels(t *testing.T) {
	var d [512]byte
	for i := range d {
		d[i] = byte(i * 3)
	}
	r := mapReader{0: d}
	spec := CompositeSpec{Universes: []int{0}, Mono: true, Extended: true, LookID: 5, SceneID: 6, Blackout: 7}
	img := RenderComposite(r, spec)
	// Recompute the CRC from the DECODED pixels the way the world reader would, and compare with
	// the header cell.
	unis := readUniverses(r, spec.Universes)
	want := crc8Frame(unis, spec)
	if got := metaCentre(img, metaColCRC, 0); got != want {
		t.Fatalf("header CRC = %#x, recomputed = %#x", got, want)
	}
}
