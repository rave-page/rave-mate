package mocapmaster

// region.go - the v1.2 COMPOSITE MOCAP REGION (MOCAP_PANEL_CONTRACT.md §10): the master
// re-renders active dancers into the free area of the VRSL composite between the LO mirror grid
// and the HI strip, below the meta band. Reuses the panel's data-cell vocabulary (16px cells,
// R=hi/G=lo/B=hi^lo, §4 dancer layout) and the composite's OWN meta-band calibration triad -
// the region carries no triad of its own. Transport is the CDN video, so all VRSL-extension
// robustness rules apply (centre sampling, MAGIC +-4/byte, parity-invalid cell -> bone absent).
//
// NOTE (frozen-in overlap): region rows 0-1 (y in [32,64)) share pixels with the VRSL
// extension's semantic-lane band (VRSL_VIDEO_STREAM_CONTRACT.md meta row 1) at x in [216,472).
// The lighting GRIDS (x < 208, x >= 1712) are never touched - that is the invariant the tests
// pin; lane cohabitation is a contract-level fact, not this package's call.

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"

	"rave.page/mate/internal/mocappanel"
	"rave.page/mate/internal/vrslgrid"
)

// Region geometry (absolute px on the canonical 1920x1080 composite, top-left origin).
// Cell (c,r) sampled at (RegionX0 + c*16 + 8, RegionY0 + r*16 + 8); row-major idx = r*93 + c.
const (
	RegionX0     = 216 // col 0 left edge (right of the LO mirror grid, == composite meta band x0)
	RegionY0     = 32  // below the composite meta band row 0
	RegionCellPx = 16
	RegionCols   = 93
	RegionRows   = 65
	RegionCells  = RegionCols * RegionRows // 6045; top rows used, rest stays black

	RegionMagic0  = 0x5250 // 'R','P'
	RegionMagic1  = 0x4D32 // 'M','2' - region, contract v1.2
	RegionVersion = 1

	// Region header flags (col 3).
	RegionFlagLive     = 1 << 0
	RegionFlagRecorded = 1 << 1 // recorded-take

	RegionDancerBase   = RegionCols               // dancers from cell idx 93 (row 1)
	RegionMaxDataCells = RegionCells - RegionCols // 5952: D*stride budget == region capacity
)

// Region header column map (row 0; multi-cell ints big-endian across cells).
const (
	regColMagic0       = 0
	regColMagic1       = 1
	regColVersion      = 2
	regColFlags        = 3
	regColBoneSlots    = 4
	regColDancerCount  = 5
	regColFrameCounter = 6 // 32b, 2 cells
	regColStageMin     = 8 // X,Y,Z signed 16b fixed x256, 3 cells
	regColStageSize    = 11
	regColReserved     = 14 // 14..15 = 0
	regHeaderCells     = 16
)

// RegionHeader is the decoded region header (contract §10 row 0). StageMin/StageSize hold
// metres reconstructed from the x256 fixed-point cells, so encode(decode(x)) is cell-exact.
type RegionHeader struct {
	Version      uint16
	Flags        uint16
	BoneSlots    int // S, 1..32; stride = 8 + 2*S, fixed for the stream
	DancerCount  int // D, 0..10
	FrameCounter uint32
	StageMin     [3]float64 // metres
	StageSize    [3]float64 // metres
}

// RegionSample returns the sample point of region cell idx (row-major).
func RegionSample(idx int) (x, y int) {
	c, r := idx%RegionCols, idx/RegionCols
	return RegionX0 + c*RegionCellPx + RegionCellPx/2, RegionY0 + r*RegionCellPx + RegionCellPx/2
}

// RenderRegion draws the v1.2 region cells into a 1920x1080 composite frame: header row 0
// cols 0..15, then each dancer block per §4 (localId, flags, boneMask, hips q, rotation wire
// words). Every pixel of each painted 16px cell is painted; untouched cells stay whatever the
// composite already is (black). The caller keeps h.DancerCount == len(dancers) and
// h.BoneSlots == the stream S; bone cells are driven by BoneMask + Rots (Quats/Present are
// decode-side products and not consulted). Cells that would fall outside dst are clipped.
func RenderRegion(dst *image.NRGBA, h RegionHeader, dancers []mocappanel.Dancer) {
	renderRegionInto(dst, h, dancers)
}

// renderRegionInto is RenderRegion over any draw.Image (fast paths for NRGBA/RGBA - opaque
// pixels share one byte layout; the encoder loop's composite is *image.RGBA).
func renderRegionInto(dst draw.Image, h RegionHeader, dancers []mocappanel.Dancer) {
	for c, v := range regionHeaderCells(h) {
		putRegion(dst, c, v)
	}
	stride := mocappanel.Stride(h.BoneSlots)
	for d := range dancers {
		dc := &dancers[d]
		base := RegionDancerBase + d*stride
		putRegion(dst, base+mocappanel.OffLocalID, dc.LocalID)
		putRegion(dst, base+mocappanel.OffFlags, dc.Flags)
		putRegion(dst, base+mocappanel.OffBoneMask, uint16(dc.BoneMask>>16))
		putRegion(dst, base+mocappanel.OffBoneMask+1, uint16(dc.BoneMask))
		for i := 0; i < 3; i++ {
			putRegion(dst, base+mocappanel.OffHips+i, dc.HipsQ[i])
		}
		putRegion(dst, base+mocappanel.OffReserved, 0)
		for k := 0; k < h.BoneSlots; k++ {
			var w uint32
			if dc.BoneMask>>k&1 == 1 && k < len(dc.Rots) {
				w = dc.Rots[k]
			}
			putRegion(dst, base+mocappanel.OffBones+2*k, uint16(w>>16))
			putRegion(dst, base+mocappanel.OffBones+2*k+1, uint16(w))
		}
	}
}

// regionHeaderCells lays the header out as 16-bit cell values (row 0 cols 0..15).
func regionHeaderCells(h RegionHeader) [regHeaderCells]uint16 {
	var m [regHeaderCells]uint16
	m[regColMagic0] = RegionMagic0
	m[regColMagic1] = RegionMagic1
	m[regColVersion] = h.Version
	m[regColFlags] = h.Flags
	m[regColBoneSlots] = uint16(h.BoneSlots)
	m[regColDancerCount] = uint16(h.DancerCount)
	m[regColFrameCounter] = uint16(h.FrameCounter >> 16)
	m[regColFrameCounter+1] = uint16(h.FrameCounter)
	for i := 0; i < 3; i++ {
		m[regColStageMin+i] = fix16s(h.StageMin[i])
		m[regColStageSize+i] = fix16u(h.StageSize[i])
	}
	return m
}

// putRegion paints every pixel of region cell idx with the value encoding (R=hi, G=lo,
// B=hi XOR lo). Out-of-region indices are dropped - the render can never escape [216,1704).
func putRegion(dst draw.Image, idx int, v uint16) {
	if idx < 0 || idx >= RegionCells {
		return
	}
	hi, lo := uint8(v>>8), uint8(v)
	x0 := RegionX0 + (idx%RegionCols)*RegionCellPx
	y0 := RegionY0 + (idx/RegionCols)*RegionCellPx
	fillRegionCell(dst, x0, y0, hi, lo, hi^lo)
}

// fillRegionCell paints one 16px cell, clipped to dst bounds. NRGBA/RGBA fast path writes Pix
// directly (identical byte layout for opaque pixels); anything else goes through Set.
func fillRegionCell(dst draw.Image, x0, y0 int, r, g, b uint8) {
	cell := image.Rect(x0, y0, x0+RegionCellPx, y0+RegionCellPx)
	var pix []uint8
	var stride int
	var rect image.Rectangle
	switch im := dst.(type) {
	case *image.NRGBA:
		pix, stride, rect = im.Pix, im.Stride, im.Rect
	case *image.RGBA:
		pix, stride, rect = im.Pix, im.Stride, im.Rect
	default:
		c := color.NRGBA{r, g, b, 255}
		clip := cell.Intersect(dst.Bounds())
		for y := clip.Min.Y; y < clip.Max.Y; y++ {
			for x := clip.Min.X; x < clip.Max.X; x++ {
				dst.Set(x, y, c)
			}
		}
		return
	}
	clip := cell.Intersect(rect)
	for y := clip.Min.Y; y < clip.Max.Y; y++ {
		o := (y-rect.Min.Y)*stride + (clip.Min.X-rect.Min.X)*4
		for x := clip.Min.X; x < clip.Max.X; x++ {
			pix[o], pix[o+1], pix[o+2], pix[o+3] = r, g, b, 255
			o += 4
		}
	}
}

// DecodeRegion is the exact inverse of RenderRegion (tests + future spillover diagnostics):
// calibrates off the composite's OWN meta-band triad (vrslgrid extended frame, black/mid/white
// at meta cols 0/1/2 - the region has no triad), checks MAGIC +-MagicTol per byte, parses the
// header, then the dancer blocks with §4/§7 semantics: dancer problems reject the dancer, bone
// problems the bone - never the frame. Input must be a canonical-width composite (1920 x >=1080).
func DecodeRegion(img image.Image) (RegionHeader, []mocappanel.Dancer, error) {
	b := img.Bounds()
	if b.Dx() != vrslgrid.FrameWidth || b.Dy() < vrslgrid.FrameRefHeight {
		return RegionHeader{}, nil, fmt.Errorf("mocapmaster: bad geometry: want %dx>=%d, got %dx%d",
			vrslgrid.FrameWidth, vrslgrid.FrameRefHeight, b.Dx(), b.Dy())
	}
	sample := mocappanel.ImageSampler(img)
	cal, err := calibrateComposite(sample)
	if err != nil {
		return RegionHeader{}, nil, err
	}
	if !regionMagicOK(sample, cal) {
		return RegionHeader{}, nil, fmt.Errorf("mocapmaster: region MAGIC mismatch")
	}
	h, err := parseRegionHeader(sample, cal)
	if err != nil {
		return RegionHeader{}, nil, err
	}
	return h, parseRegionDancers(sample, cal, h), nil
}

// regionMagicOK checks the four MAGIC bytes independently within +-MagicTol post-calibration
// (3-miss hysteresis is a stream-level concern and lives with the stream consumer).
func regionMagicOK(sample func(x, y int) (r, g, b uint8), cal calib) bool {
	ok := func(idx int, want uint16) bool {
		x, y := RegionSample(idx)
		px := cal.apply(sampleAt(sample, x, y))
		return absDiff(px[0], uint8(want>>8)) <= mocappanel.MagicTol &&
			absDiff(px[1], uint8(want)) <= mocappanel.MagicTol
	}
	return ok(regColMagic0, RegionMagic0) && ok(regColMagic1, RegionMagic1)
}

// parseRegionHeader reads row-0 cols 2..13 (reserved 14..15 ignored for forward-compat). Any
// parity-invalid header cell rejects the frame - without a trusted S/D the blocks cannot parse.
func parseRegionHeader(sample func(x, y int) (r, g, b uint8), cal calib) (RegionHeader, error) {
	var cells [regHeaderCells]uint16
	for c := regColVersion; c < regColReserved; c++ {
		v, ok := cal.regionCell(sample, c)
		if !ok {
			return RegionHeader{}, fmt.Errorf("mocapmaster: region header cell %d parity-invalid", c)
		}
		cells[c] = v
	}
	h := RegionHeader{
		Version:      cells[regColVersion],
		Flags:        cells[regColFlags],
		BoneSlots:    int(cells[regColBoneSlots]),
		DancerCount:  int(cells[regColDancerCount]),
		FrameCounter: uint32(cells[regColFrameCounter])<<16 | uint32(cells[regColFrameCounter+1]),
	}
	for i := 0; i < 3; i++ {
		h.StageMin[i] = float64(int16(cells[regColStageMin+i])) / mocappanel.StageFixedScale
		h.StageSize[i] = float64(cells[regColStageSize+i]) / mocappanel.StageFixedScale
	}
	if h.Version != RegionVersion {
		return RegionHeader{}, fmt.Errorf("mocapmaster: unsupported region version %d (decoder speaks %d)", h.Version, RegionVersion)
	}
	if h.BoneSlots < 1 || h.BoneSlots > mocappanel.BoneSlotMax {
		return RegionHeader{}, fmt.Errorf("mocapmaster: boneSlots %d outside 1..%d", h.BoneSlots, mocappanel.BoneSlotMax)
	}
	if h.DancerCount > mocappanel.MaxDancers {
		return RegionHeader{}, fmt.Errorf("mocapmaster: dancerCount %d exceeds cap %d", h.DancerCount, mocappanel.MaxDancers)
	}
	if n := h.DancerCount * mocappanel.Stride(h.BoneSlots); n > RegionMaxDataCells {
		return RegionHeader{}, fmt.Errorf("mocapmaster: D*stride %d exceeds region budget %d", n, RegionMaxDataCells)
	}
	return h, nil
}

// parseRegionDancers mirrors the panel decoder: rejected (skipped) dancers = unreadable header
// cells, present bit clear, or missing mandatory core bones; invalid bone cells only mark that
// bone absent. Rots keeps raw wire words; Present requires mask bit AND parity AND norm.
func parseRegionDancers(sample func(x, y int) (r, g, b uint8), cal calib, h RegionHeader) []mocappanel.Dancer {
	dancers := make([]mocappanel.Dancer, 0, h.DancerCount)
	stride := mocappanel.Stride(h.BoneSlots)
	for d := 0; d < h.DancerCount; d++ {
		base := RegionDancerBase + d*stride
		var hd [mocappanel.DancerHeaderCells]uint16
		ok := true
		for i := range hd {
			v, valid := cal.regionCell(sample, base+i)
			if !valid {
				ok = false
				break
			}
			hd[i] = v
		}
		if !ok {
			continue
		}
		mask := uint32(hd[mocappanel.OffBoneMask])<<16 | uint32(hd[mocappanel.OffBoneMask+1])
		if hd[mocappanel.OffFlags]&mocappanel.DancerPresent == 0 || mask&mocappanel.CoreMask != mocappanel.CoreMask {
			continue
		}
		dc := mocappanel.Dancer{
			LocalID:  hd[mocappanel.OffLocalID],
			Flags:    hd[mocappanel.OffFlags],
			BoneMask: mask,
			HipsQ:    [3]uint16{hd[mocappanel.OffHips], hd[mocappanel.OffHips+1], hd[mocappanel.OffHips+2]},
			Rots:     make([]uint32, h.BoneSlots),
			Quats:    make([][4]float64, h.BoneSlots),
			Present:  make([]bool, h.BoneSlots),
		}
		for k := 0; k < h.BoneSlots; k++ {
			if mask>>k&1 == 0 {
				continue
			}
			hi, okHi := cal.regionCell(sample, base+mocappanel.OffBones+2*k)
			lo, okLo := cal.regionCell(sample, base+mocappanel.OffBones+2*k+1)
			if !okHi || !okLo {
				continue // parity-invalid -> this bone absent this frame
			}
			w := uint32(hi)<<16 | uint32(lo)
			dc.Rots[k] = w
			q, okQ := mocappanel.UnpackQuat(w)
			if !okQ {
				continue // norm-reject -> absent
			}
			dc.Quats[k] = q
			dc.Present[k] = true
		}
		dancers = append(dancers, dc)
	}
	return dancers
}

// calib is the two-point per-channel gain+offset from the COMPOSITE meta-band triad (contract
// §10: the region reuses the composite's calibration - vrslgrid extended frame, 32px cells at
// x0=216, row 0: col 0 black(0), col 1 mid(128), col 2 white(255)). Identity on a pristine
// frame; MidWarn flags the mid cell off by >6 after correction (sanity only, never a reject).
type calib struct {
	black   [3]float64
	scale   [3]float64 // 255/(white-black)
	MidWarn bool
}

// Composite triad columns (order differs from the panel's own triad - black/mid/white here).
const (
	compCalBlack = 0
	compCalMid   = 1
	compCalWhite = 2
)

func calibrateComposite(sample func(x, y int) (r, g, b uint8)) (calib, error) {
	tri := func(col int) [3]uint8 {
		x := vrslgrid.MetaBandX0 + col*vrslgrid.MetaCellPx + vrslgrid.MetaCellPx/2
		return sampleAt(sample, x, vrslgrid.MetaCellPx/2)
	}
	b, m, w := tri(compCalBlack), tri(compCalMid), tri(compCalWhite)
	var c calib
	for ch := 0; ch < 3; ch++ {
		if w[ch] <= b[ch] {
			return calib{}, fmt.Errorf("mocapmaster: degenerate composite calibration ch%d: black=%d white=%d", ch, b[ch], w[ch])
		}
		c.black[ch] = float64(b[ch])
		c.scale[ch] = 255 / float64(w[ch]-b[ch])
	}
	mid := c.apply(m)
	for ch := 0; ch < 3; ch++ {
		if absDiff(mid[ch], 128) > 6 {
			c.MidWarn = true
		}
	}
	return c, nil
}

// apply corrects raw captured bytes into contract bytes (round + clamp). Exact identity when
// the triad captured bit-exact.
func (c calib) apply(raw [3]uint8) [3]uint8 {
	var out [3]uint8
	for ch := 0; ch < 3; ch++ {
		v := (float64(raw[ch]) - c.black[ch]) * c.scale[ch]
		out[ch] = uint8(math.Round(math.Min(255, math.Max(0, v))))
	}
	return out
}

// regionCell reads region cell idx as a calibrated, parity-checked 16-bit value. Invalid means
// the cell is ABSENT (field-hold/easing semantics, never a frame reject).
func (c calib) regionCell(sample func(x, y int) (r, g, b uint8), idx int) (uint16, bool) {
	x, y := RegionSample(idx)
	px := c.apply(sampleAt(sample, x, y))
	return uint16(px[0])<<8 | uint16(px[1]), absDiff(px[2], px[0]^px[1]) <= mocappanel.ParityTol
}

func sampleAt(sample func(x, y int) (r, g, b uint8), x, y int) [3]uint8 {
	r, g, b := sample(x, y)
	return [3]uint8{r, g, b}
}

func absDiff(a, b uint8) int {
	d := int(a) - int(b)
	if d < 0 {
		d = -d
	}
	return d
}

// fix16s quantizes metres to signed 16-bit fixed-point x256 (range +-128 m, ~4 mm), clamped.
// Same math as the panel encoder (contract vocabulary shared by panel and region).
func fix16s(metres float64) uint16 {
	v := math.Round(metres * mocappanel.StageFixedScale)
	if v < math.MinInt16 {
		v = math.MinInt16
	} else if v > math.MaxInt16 {
		v = math.MaxInt16
	}
	return uint16(int16(v))
}

// fix16u quantizes metres to unsigned 16-bit fixed-point x256, clamped.
func fix16u(metres float64) uint16 {
	v := math.Round(metres * mocappanel.StageFixedScale)
	if v < 0 {
		v = 0
	} else if v > math.MaxUint16 {
		v = math.MaxUint16
	}
	return uint16(v)
}
