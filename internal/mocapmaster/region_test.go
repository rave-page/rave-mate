package mocapmaster

// region_test.go - golden round trip (render -> decode exact -> re-render byte-identical),
// MAGIC tolerance, reject paths, and the lighting-grid non-collision invariant.

import (
	"bytes"
	"image"
	"image/color"
	"reflect"
	"testing"

	"rave.page/mate/internal/mocappanel"
	"rave.page/mate/internal/vrslgrid"
)

// mapReader is a minimal vrslgrid.Reader for hermetic composites.
type mapReader map[int][512]byte

func (m mapReader) Get(u uint16) ([512]byte, bool) { d, ok := m[int(u)]; return d, ok }

// composite renders a 1-universe extended frame (carries the calibration triad the region
// decode rides on) with an optional overlay.
func composite(overlay func(*image.RGBA)) *image.RGBA {
	return vrslgrid.RenderComposite(mapReader{}, vrslgrid.CompositeSpec{
		Universes: []int{0}, Mono: true, Extended: true, Overlay: overlay,
	})
}

// goldenRegion is the §10 conformance vector: the §6 golden frame's dancers rendered into the
// region (S=22, D=2, frameCounter=42, golden bounds), flags = live.
func goldenRegion() (RegionHeader, []mocappanel.Dancer) {
	gh, gd := mocappanel.GoldenFrame()
	return RegionHeader{
		Version:      RegionVersion,
		Flags:        RegionFlagLive,
		BoneSlots:    gh.BoneSlots,
		DancerCount:  gh.DancerCount,
		FrameCounter: gh.FrameCounter,
		StageMin:     gh.StageMin,
		StageSize:    gh.StageSize,
	}, gd
}

func TestGoldenRegionRoundTrip(t *testing.T) {
	rh, gd := goldenRegion()
	img := composite(func(im *image.RGBA) { renderRegionInto(im, rh, gd) })

	h, dancers, err := DecodeRegion(img)
	if err != nil {
		t.Fatalf("DecodeRegion: %v", err)
	}
	if h != rh {
		t.Fatalf("header mismatch:\n got %+v\nwant %+v", h, rh)
	}
	if len(dancers) != len(gd) {
		t.Fatalf("dancer count: got %d want %d", len(dancers), len(gd))
	}
	for i := range gd {
		if !reflect.DeepEqual(dancers[i], gd[i]) {
			t.Errorf("dancer %d mismatch:\n got %+v\nwant %+v", i, dancers[i], gd[i])
		}
	}

	// Exact inverse: re-render from the decode products, byte-identical frame.
	img2 := composite(func(im *image.RGBA) { renderRegionInto(im, h, dancers) })
	if !bytes.Equal(img.Pix, img2.Pix) {
		t.Fatal("re-rendered frame not byte-identical")
	}
}

// TestRenderRegionNRGBA drives the exported NRGBA signature and decodes off a hand-painted
// composite triad (region calibration is the composite's, not its own).
func TestRenderRegionNRGBA(t *testing.T) {
	rh, gd := goldenRegion()
	img := image.NewNRGBA(image.Rect(0, 0, vrslgrid.FrameWidth, vrslgrid.FrameRefHeight))
	paintTriad(img)
	RenderRegion(img, rh, gd)

	h, dancers, err := DecodeRegion(img)
	if err != nil {
		t.Fatalf("DecodeRegion: %v", err)
	}
	if h != rh {
		t.Fatalf("header mismatch:\n got %+v\nwant %+v", h, rh)
	}
	if !reflect.DeepEqual(dancers, gd) {
		t.Fatal("dancers mismatch on NRGBA path")
	}
}

// paintTriad paints the composite meta-band calibration triad (black/mid/white, 32px cells).
func paintTriad(img *image.NRGBA) {
	for c, v := range []uint8{0, 128, 255} {
		x0 := vrslgrid.MetaBandX0 + c*vrslgrid.MetaCellPx
		for y := 0; y < vrslgrid.MetaCellPx; y++ {
			for x := x0; x < x0+vrslgrid.MetaCellPx; x++ {
				img.SetNRGBA(x, y, color.NRGBA{v, v, v, 255})
			}
		}
	}
}

func TestRegionMagicTolerance(t *testing.T) {
	rh, gd := goldenRegion()
	perturbed := func(delta int) *image.RGBA {
		img := composite(func(im *image.RGBA) { renderRegionInto(im, rh, gd) })
		hi := uint8(int(RegionMagic0>>8) + delta)
		lo := uint8(int(RegionMagic0&0xFF) + delta)
		fillRegionCell(img, RegionX0, RegionY0, hi, lo, hi^lo)
		return img
	}

	if _, _, err := DecodeRegion(perturbed(mocappanel.MagicTol)); err != nil {
		t.Fatalf("MAGIC within +-%d must decode: %v", mocappanel.MagicTol, err)
	}
	if _, _, err := DecodeRegion(perturbed(mocappanel.MagicTol + 1)); err == nil {
		t.Fatalf("MAGIC off by %d must reject", mocappanel.MagicTol+1)
	}
}

func TestDecodeRegionRejects(t *testing.T) {
	if _, _, err := DecodeRegion(image.NewNRGBA(image.Rect(0, 0, 208, 1080))); err == nil {
		t.Fatal("non-canonical geometry must reject")
	}

	rh, gd := goldenRegion()
	for name, mutate := range map[string]func(*RegionHeader){
		"version":     func(h *RegionHeader) { h.Version = RegionVersion + 1 },
		"boneSlots":   func(h *RegionHeader) { h.BoneSlots = 0 },
		"dancerCount": func(h *RegionHeader) { h.DancerCount = mocappanel.MaxDancers + 1 },
	} {
		h := rh
		mutate(&h)
		img := composite(func(im *image.RGBA) { renderRegionInto(im, h, nil) })
		if _, _, err := DecodeRegion(img); err == nil {
			t.Errorf("%s: bad header must reject", name)
		}
	}

	// Parity-invalid header cell (version cell with inverted parity byte) rejects the frame.
	img := composite(func(im *image.RGBA) { renderRegionInto(im, rh, gd) })
	x, y := RegionSample(regColVersion)
	fillRegionCell(img, x-RegionCellPx/2, y-RegionCellPx/2, 0, uint8(RegionVersion), ^uint8(RegionVersion))
	if _, _, err := DecodeRegion(img); err == nil {
		t.Fatal("parity-invalid header cell must reject the frame")
	}
}

// TestRegionNoCollisionWithVRSLCells pins the contract invariant: the region x range
// [216,1704) never touches the lighting grids (x < 208, x >= 1712), even fully painted.
func TestRegionNoCollisionWithVRSLCells(t *testing.T) {
	if RegionX0 < vrslgrid.GridWidthPx {
		t.Fatalf("region x0 %d inside low-byte grid (<%d)", RegionX0, vrslgrid.GridWidthPx)
	}
	if end := RegionX0 + RegionCols*RegionCellPx; end > vrslgrid.StripX0 {
		t.Fatalf("region end %d inside high-byte strip (>%d)", end, vrslgrid.StripX0)
	}

	data := [512]byte{}
	for ch := range data {
		data[ch] = byte(ch)
	}
	r := mapReader{0: data}
	spec := vrslgrid.CompositeSpec{Universes: []int{0}, Mono: true, Extended: true, FrameCounter: 7}
	base := vrslgrid.RenderComposite(r, spec)

	spec.Overlay = func(img *image.RGBA) {
		for idx := 0; idx < RegionCells; idx++ { // worst case: every region cell painted
			putRegion(img, idx, 0xFFFF)
		}
	}
	over := vrslgrid.RenderComposite(r, spec)

	if base.Bounds() != over.Bounds() {
		t.Fatal("overlay changed frame geometry")
	}
	b := base.Bounds()
	painted := false
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			same := base.RGBAAt(x, y) == over.RGBAAt(x, y)
			if !same {
				painted = true
			}
			if same {
				continue
			}
			if x < vrslgrid.GridWidthPx || x >= vrslgrid.StripX0 {
				t.Fatalf("lighting grid pixel touched at (%d,%d)", x, y)
			}
			if y < RegionY0 {
				t.Fatalf("meta band row 0 pixel touched at (%d,%d)", x, y)
			}
		}
	}
	if !painted {
		t.Fatal("overlay painted nothing - test is vacuous")
	}
}
