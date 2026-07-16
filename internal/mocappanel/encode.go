package mocappanel

// encode.go - reference panel encoder: Header + Dancers -> the full 1920x1080 contract frame.
// Every pixel of every cell is painted (the decoder samples centres; fat cells survive
// +-few-LSB capture noise). Non-cell pixels stay opaque black.

import (
	"image"
	"image/color"
	"math"
)

// Encode rasterizes one panel frame. The caller keeps h.DancerCount == len(dancers) and
// h.BoneSlots == len(d.Rots); bone cells are driven by BoneMask + Rots (Quats/Present are
// decode-side products and not consulted).
func Encode(h Header, dancers []Dancer) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, CanvasW, CanvasH))
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255 // opaque black background
	}

	meta := metaCells(h)
	for c := 0; c < MetaCols; c++ {
		switch c {
		case ColCalBlack:
			fillMetaCell(img, c, color.NRGBA{0, 0, 0, 255})
		case ColCalMid:
			fillMetaCell(img, c, color.NRGBA{128, 128, 128, 255})
		case ColCalWhite:
			fillMetaCell(img, c, color.NRGBA{255, 255, 255, 255})
		default:
			r, g, b := CellBytes(meta[c])
			fillMetaCell(img, c, color.NRGBA{r, g, b, 255})
		}
	}

	// v1.1 fiducials (§8b): inverted-parity corner anchors. Invisible to the v1-exact decode
	// (meta 59 is never read; the two data cells lie beyond any legal D*stride).
	fillMetaCell(img, ColFidTR, fidColor(FidTR))
	fillDataCell(img, FidRow*DataCols+FidBLCol, fidColor(FidBL))
	fillDataCell(img, FidRow*DataCols+FidBRCol, fidColor(FidBR))

	stride := Stride(h.BoneSlots)
	for d, dc := range dancers {
		base := d * stride
		putData(img, base+OffLocalID, dc.LocalID)
		putData(img, base+OffFlags, dc.Flags)
		putData(img, base+OffBoneMask, uint16(dc.BoneMask>>16))
		putData(img, base+OffBoneMask+1, uint16(dc.BoneMask))
		for i := 0; i < 3; i++ {
			putData(img, base+OffHips+i, dc.HipsQ[i])
		}
		putData(img, base+OffReserved, 0)
		for k := 0; k < h.BoneSlots; k++ {
			var w uint32
			if dc.BoneMask>>k&1 == 1 && k < len(dc.Rots) {
				w = dc.Rots[k]
			}
			putData(img, base+OffBones+2*k, uint16(w>>16))
			putData(img, base+OffBones+2*k+1, uint16(w))
		}
	}
	return img
}

// metaCells lays the header out as 16-bit cell values (big-endian across multi-cell ints).
// Calibration cols carry placeholders (painted raw by Encode); reserved cols stay 0.
func metaCells(h Header) [MetaCols]uint16 {
	var m [MetaCols]uint16
	m[ColMagic0] = Magic0
	m[ColMagic1] = Magic1
	m[ColVersion] = h.Version
	m[ColFlags] = h.Flags
	put32(m[:], ColSourceTag, h.SourceTag)
	m[ColSessionNonce] = h.SessionNonce
	put32(m[:], ColPanelSeq, h.PanelSeq)
	put64(m[:], ColServerTimeMs, uint64(h.ServerTimeMs))
	put64(m[:], ColNetUtcTicks, uint64(h.NetUtcTicks))
	m[ColBpmX100] = h.BpmX100
	put64(m[:], ColDownbeatMs, uint64(h.DownbeatServerTimeMs))
	m[ColBoneSlots] = uint16(h.BoneSlots)
	m[ColDancerCount] = uint16(h.DancerCount)
	put32(m[:], ColFrameCounter, h.FrameCounter)
	for i := 0; i < 3; i++ {
		m[ColStageMin+i] = fix16s(h.StageMin[i])
		m[ColStageSize+i] = fix16u(h.StageSize[i])
	}
	return m
}

func put32(cells []uint16, col int, v uint32) {
	cells[col] = uint16(v >> 16)
	cells[col+1] = uint16(v)
}

func put64(cells []uint16, col int, v uint64) {
	for i := 0; i < 4; i++ {
		cells[col+i] = uint16(v >> (48 - 16*i))
	}
}

// fix16s quantizes metres to signed 16-bit fixed-point x256 (range +-128 m, ~4 mm), clamped.
func fix16s(metres float64) uint16 {
	v := math.Round(metres * StageFixedScale)
	if v < math.MinInt16 {
		v = math.MinInt16
	} else if v > math.MaxInt16 {
		v = math.MaxInt16
	}
	return uint16(int16(v))
}

// fix16u quantizes metres to unsigned 16-bit fixed-point x256, clamped.
func fix16u(metres float64) uint16 {
	v := math.Round(metres * StageFixedScale)
	if v < 0 {
		v = 0
	} else if v > math.MaxUint16 {
		v = math.MaxUint16
	}
	return uint16(v)
}

// fillMetaCell paints every pixel of the 32x32 meta cell col.
func fillMetaCell(img *image.NRGBA, col int, c color.NRGBA) {
	fillRect(img, col*MetaCellPx, 0, MetaCellPx, c)
}

// putData paints every pixel of data cell idx (row-major) with the value encoding.
func putData(img *image.NRGBA, idx int, v uint16) {
	r, g, b := CellBytes(v)
	fillDataCell(img, idx, color.NRGBA{r, g, b, 255})
}

// fillDataCell paints every pixel of data cell idx (row-major).
func fillDataCell(img *image.NRGBA, idx int, c color.NRGBA) {
	fillRect(img, (idx%DataCols)*DataCellPx, DataY0+(idx/DataCols)*DataCellPx, DataCellPx, c)
}

// fidColor is a fiducial value's inverted-parity cell colour (§8b).
func fidColor(v uint16) color.NRGBA {
	r, g, b := FidBytes(v)
	return color.NRGBA{r, g, b, 255}
}

func fillRect(img *image.NRGBA, x0, y0, size int, c color.NRGBA) {
	for y := y0; y < y0+size; y++ {
		for x := x0; x < x0+size; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
}
