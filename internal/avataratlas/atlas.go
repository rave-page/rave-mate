package avataratlas

// atlas.go - RPA1 PNG codec (contract §11): per-bone tight AABB (+1mm pad) over bone-local
// points, 16-bit quantization, header row 0 + self-test row 1 + 3px/point linear packing.
// All multi-byte fields big-endian. Encode/Decode are exact inverses; Verify implements the
// §11 reject list. Alpha handling: A=255 everywhere except pixels whose layout assigns A a
// field byte; "all zero" pixels (unused bone-table slots) are RGBA(0,0,0,0) because their A
// position IS a (zero) field byte. Padding pixels (row-0 tail, row-1 px256+, point tail) carry
// no fields -> RGB=0, A=255 per the canvas rule.

import (
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
)

// Box is one bone's bind-space AABB in millimetres (wire ints; zero = unused slot).
type Box struct {
	Min  [3]int16  // mm, signed
	Size [3]uint16 // mm; 0 on a used axis = degenerate, decoded coord = Min
}

// Used reports whether the box carries any extent (unused table slots are all-zero).
func (b Box) Used() bool { return b != Box{} }

// AtlasPoint is one quantized point (wire values).
type AtlasPoint struct {
	Q      [3]uint16 // quantized bone-local pos: p = boxMin + q/65535 * boxSize
	Slot   uint8     // §5 bone slot 0..31
	Weight uint8     // v1 = 255 (rigid); <255 reserved v2
	RGB    [3]uint8  // albedo sRGB8 as authored
}

// Atlas is a decoded/encodable RPA1 atlas.
type Atlas struct {
	Version   int // 1
	Flags     int // reserved 0
	SlotIndex int // performer/dancer fixed slot 0..15
	BoneCount int // used bone slots (<=32), informational
	Boxes     [BoneSlots]Box
	Points    []AtlasPoint
}

// PointPos reconstructs a point's bone-local position in metres (degenerate size=0 axis -> min).
func (a *Atlas) PointPos(p AtlasPoint) [3]float64 {
	var out [3]float64
	box := a.Boxes[p.Slot]
	for ax := 0; ax < 3; ax++ {
		min := float64(box.Min[ax]) / 1000
		size := float64(box.Size[ax]) / 1000
		if box.Size[ax] == 0 {
			out[ax] = min
			continue
		}
		out[ax] = min + float64(p.Q[ax])/65535*size
	}
	return out
}

// BuildAtlas quantizes sampled points into an atlas: per-bone tight AABB over bone-local
// points +1mm pad (floor/ceil to mm guarantees containment), q = round((p-min)/size*65535)
// clamped. Point order is preserved (part of golden determinism).
func BuildAtlas(samples []SampledPoint, slotIndex int) (*Atlas, error) {
	if slotIndex < 0 || slotIndex > MaxSlotIndex {
		return nil, fmt.Errorf("atlas: slotIndex %d out of range 0..%d", slotIndex, MaxSlotIndex)
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("atlas: no points")
	}
	if len(samples) > MaxPoints {
		return nil, fmt.Errorf("atlas: %d points exceed max-height capacity %d", len(samples), MaxPoints)
	}
	a := &Atlas{Version: Version, SlotIndex: slotIndex}

	// Tight bounds per used slot.
	var lo, hi [BoneSlots][3]float64
	var used [BoneSlots]bool
	for _, s := range samples {
		if s.Slot < 0 || s.Slot >= BoneSlots {
			return nil, fmt.Errorf("atlas: point slot %d out of range", s.Slot)
		}
		if !used[s.Slot] {
			used[s.Slot] = true
			lo[s.Slot], hi[s.Slot] = s.Local, s.Local
			continue
		}
		for ax := 0; ax < 3; ax++ {
			lo[s.Slot][ax] = math.Min(lo[s.Slot][ax], s.Local[ax])
			hi[s.Slot][ax] = math.Max(hi[s.Slot][ax], s.Local[ax])
		}
	}
	for slot := 0; slot < BoneSlots; slot++ {
		if !used[slot] {
			continue
		}
		a.BoneCount++
		for ax := 0; ax < 3; ax++ {
			minMm := int(math.Floor(lo[slot][ax]*1000)) - BoxPadMm
			maxMm := int(math.Ceil(hi[slot][ax]*1000)) + BoxPadMm
			sizeMm := maxMm - minMm
			if minMm < math.MinInt16 || minMm > math.MaxInt16 {
				return nil, fmt.Errorf("atlas: bone %d (%s) axis %d min %dmm exceeds int16 box range", slot, SlotName(slot), ax, minMm)
			}
			if sizeMm > math.MaxUint16 {
				return nil, fmt.Errorf("atlas: bone %d (%s) axis %d size %dmm exceeds uint16 box range", slot, SlotName(slot), ax, sizeMm)
			}
			a.Boxes[slot].Min[ax] = int16(minMm)
			a.Boxes[slot].Size[ax] = uint16(sizeMm)
		}
	}

	// Quantize against the mm-rounded boxes (the wire truth) so decode is exact.
	a.Points = make([]AtlasPoint, len(samples))
	for i, s := range samples {
		p := AtlasPoint{Slot: uint8(s.Slot), Weight: WeightV1, RGB: s.RGB}
		box := a.Boxes[s.Slot]
		for ax := 0; ax < 3; ax++ {
			minM := float64(box.Min[ax]) / 1000
			sizeM := float64(box.Size[ax]) / 1000
			if box.Size[ax] == 0 {
				p.Q[ax] = 0
				continue
			}
			q := math.Round((s.Local[ax] - minM) / sizeM * 65535)
			if q < 0 {
				q = 0
			}
			if q > 65535 {
				q = 65535
			}
			p.Q[ax] = uint16(q)
		}
		a.Points[i] = p
	}
	return a, nil
}

// ── encode ───────────────────────────────────────────────────────────────────

// Image renders the atlas to an RGBA8 image (NRGBA: bytes stored exactly as written; RGBA
// would premultiply and corrupt field bytes where A<255).
func (a *Atlas) Image() (*image.NRGBA, error) {
	pc := len(a.Points)
	if pc == 0 || pc > MaxPoints {
		return nil, fmt.Errorf("atlas: point count %d out of range 1..%d", pc, MaxPoints)
	}
	if a.SlotIndex < 0 || a.SlotIndex > MaxSlotIndex {
		return nil, fmt.Errorf("atlas: slotIndex %d out of range 0..%d", a.SlotIndex, MaxSlotIndex)
	}
	if a.BoneCount < 0 || a.BoneCount > BoneSlots {
		return nil, fmt.Errorf("atlas: boneCount %d out of range 0..%d", a.BoneCount, BoneSlots)
	}
	h := AtlasHeight(pc)
	img := image.NewNRGBA(image.Rect(0, 0, Width, h))
	// Canvas default: black, A=255 (premultiply hazard avoidance).
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i+3] = 255
	}
	set := func(x, y int, r, g, b, al uint8) {
		o := img.PixOffset(x, y)
		img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = r, g, b, al
	}

	// Row 0 header.
	set(0, 0, MagicR, MagicG, MagicB, MagicA)
	set(1, 0, uint8(a.Version), uint8(a.Flags), uint8(a.SlotIndex), uint8(a.BoneCount))
	set(2, 0, uint8(pc>>16), uint8(pc>>8), uint8(pc), 255)
	w16, h16 := uint16(Width), uint16(h)
	set(3, 0, uint8(w16>>8), uint8(w16), uint8(h16>>8), uint8(h16))
	// px4..7 reserved: RGB=0 A=255 (canvas default already).
	for slot := 0; slot < BoneSlots; slot++ {
		box := a.Boxes[slot]
		x := BoxTableX + slot*3
		if !box.Used() {
			// "all zero" - A positions here are (zero) field bytes.
			set(x, 0, 0, 0, 0, 0)
			set(x+1, 0, 0, 0, 0, 0)
			set(x+2, 0, 0, 0, 0, 0)
			continue
		}
		mx, my, mz := uint16(box.Min[0]), uint16(box.Min[1]), uint16(box.Min[2])
		set(x, 0, uint8(mx>>8), uint8(mx), uint8(my>>8), uint8(my))
		set(x+1, 0, uint8(mz>>8), uint8(mz), uint8(box.Size[0]>>8), uint8(box.Size[0]))
		set(x+2, 0, uint8(box.Size[1]>>8), uint8(box.Size[1]), uint8(box.Size[2]>>8), uint8(box.Size[2]))
	}

	// Row 1 self-test.
	for i := 0; i < SelfTestLen; i++ {
		set(i, 1, uint8(i), uint8(255-i), uint8((i*37)&0xFF), 255)
	}

	// Rows 2+ points, 3 px per point, linear packing (row-straddling allowed).
	for p, pt := range a.Points {
		k := p * PxPerPoint
		x, y := k%Width, HeaderRows+k/Width
		put := func(dx int, r, g, b, al uint8) {
			xx, yy := x+dx, y
			if xx >= Width {
				xx -= Width
				yy++
			}
			set(xx, yy, r, g, b, al)
		}
		put(0, uint8(pt.Q[0]>>8), uint8(pt.Q[0]), uint8(pt.Q[1]>>8), uint8(pt.Q[1]))
		put(1, uint8(pt.Q[2]>>8), uint8(pt.Q[2]), pt.Slot, pt.Weight)
		put(2, pt.RGB[0], pt.RGB[1], pt.RGB[2], 255)
	}
	return img, nil
}

// EncodePNG writes the atlas PNG.
func (a *Atlas) EncodePNG(w io.Writer) error {
	img, err := a.Image()
	if err != nil {
		return err
	}
	return png.Encode(w, img)
}

// ── decode ───────────────────────────────────────────────────────────────────

// nrgbaAt reads raw stored bytes; requires *image.NRGBA (what png.Decode yields for 8-bit
// RGBA) - anything else has already lost byte-exactness.
func nrgbaPix(img image.Image) (*image.NRGBA, error) {
	n, ok := img.(*image.NRGBA)
	if !ok {
		return nil, fmt.Errorf("atlas: image is %T, want 8-bit RGBA PNG (*image.NRGBA)", img)
	}
	return n, nil
}

func pxAt(img *image.NRGBA, x, y int) (r, g, b, a uint8) {
	o := img.PixOffset(img.Rect.Min.X+x, img.Rect.Min.Y+y)
	return img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3]
}

func be16(hi, lo uint8) uint16 { return uint16(hi)<<8 | uint16(lo) }

// Verify implements the §11 reject list: MAGIC/version mismatch, dim mismatch (px3 vs actual
// texture), self-test row mismatch, slotIndex out of range, pointCount exceeding capacity.
// Any failure means: fall back to the default ghost body, never render this atlas.
func Verify(img image.Image) error {
	n, err := nrgbaPix(img)
	if err != nil {
		return err
	}
	w, h := n.Rect.Dx(), n.Rect.Dy()
	if w != Width || h < HeaderRows || h > MaxHeight {
		return fmt.Errorf("atlas: dims %dx%d (want %dx[%d..%d])", w, h, Width, HeaderRows, MaxHeight)
	}
	r, g, b, a := pxAt(n, 0, 0)
	if r != MagicR || g != MagicG || b != MagicB || a != MagicA {
		return fmt.Errorf("atlas: MAGIC mismatch %02x%02x%02x%02x (want RPA1)", r, g, b, a)
	}
	ver, _, slot, _ := pxAt(n, 1, 0)
	if int(ver) != Version {
		return fmt.Errorf("atlas: version %d (want %d)", ver, Version)
	}
	if int(slot) > MaxSlotIndex {
		return fmt.Errorf("atlas: slotIndex %d out of range 0..%d", slot, MaxSlotIndex)
	}
	wr, wg, wb, wa := pxAt(n, 3, 0)
	if int(be16(wr, wg)) != w || int(be16(wb, wa)) != h {
		return fmt.Errorf("atlas: px3 dims %dx%d != texture %dx%d", be16(wr, wg), be16(wb, wa), w, h)
	}
	pr, pg, pb, _ := pxAt(n, 2, 0)
	pc := int(pr)<<16 | int(pg)<<8 | int(pb)
	if PxPerPoint*pc > (h-HeaderRows)*Width {
		return fmt.Errorf("atlas: pointCount %d exceeds capacity %d of %dx%d", pc, (h-HeaderRows)*Width/PxPerPoint, w, h)
	}
	for i := 0; i < SelfTestLen; i++ {
		r, g, b, a := pxAt(n, i, 1)
		if r != uint8(i) || g != uint8(255-i) || b != uint8((i*37)&0xFF) || a != 255 {
			return fmt.Errorf("atlas: self-test row mismatch at px%d: %d,%d,%d,%d", i, r, g, b, a)
		}
	}
	return nil
}

// DecodeImage verifies + decodes an atlas image, field-for-field inverse of Image().
func DecodeImage(img image.Image) (*Atlas, error) {
	if err := Verify(img); err != nil {
		return nil, err
	}
	n, _ := nrgbaPix(img)
	ver, flags, slot, bones := pxAt(n, 1, 0)
	if int(bones) > BoneSlots {
		return nil, fmt.Errorf("atlas: boneCount %d > %d", bones, BoneSlots)
	}
	a := &Atlas{Version: int(ver), Flags: int(flags), SlotIndex: int(slot), BoneCount: int(bones)}
	pr, pg, pb, _ := pxAt(n, 2, 0)
	pc := int(pr)<<16 | int(pg)<<8 | int(pb)

	for s := 0; s < BoneSlots; s++ {
		x := BoxTableX + s*3
		r0, g0, b0, a0 := pxAt(n, x, 0)
		r1, g1, b1, a1 := pxAt(n, x+1, 0)
		r2, g2, b2, a2 := pxAt(n, x+2, 0)
		a.Boxes[s] = Box{
			Min:  [3]int16{int16(be16(r0, g0)), int16(be16(b0, a0)), int16(be16(r1, g1))},
			Size: [3]uint16{be16(b1, a1), be16(r2, g2), be16(b2, a2)},
		}
	}

	a.Points = make([]AtlasPoint, pc)
	for p := 0; p < pc; p++ {
		k := p * PxPerPoint
		get := func(dx int) (uint8, uint8, uint8, uint8) {
			kk := k + dx
			return pxAt(n, kk%Width, HeaderRows+kk/Width)
		}
		r0, g0, b0, a0 := get(0)
		r1, g1, b1, a1 := get(1)
		r2, g2, b2, _ := get(2)
		if b1 >= BoneSlots {
			return nil, fmt.Errorf("atlas: point %d bone slot %d out of range", p, b1)
		}
		a.Points[p] = AtlasPoint{
			Q:      [3]uint16{be16(r0, g0), be16(b0, a0), be16(r1, g1)},
			Slot:   b1,
			Weight: a1,
			RGB:    [3]uint8{r2, g2, b2},
		}
	}
	return a, nil
}

// DecodePNG reads + verifies + decodes an atlas PNG stream.
func DecodePNG(r io.Reader) (*Atlas, error) {
	img, err := png.Decode(r)
	if err != nil {
		return nil, fmt.Errorf("atlas: png decode: %w", err)
	}
	return DecodeImage(img)
}
