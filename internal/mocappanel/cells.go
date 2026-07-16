package mocappanel

// cells.go - 16-bit cell <-> RGB encoding, parity guard, and capture calibration (contract §2).
// Contract values are the bytes IN THE CAPTURED FRAME; the decoder reads raw captured bytes,
// no colour transform (the panel shader owns the sRGB inverse).

import (
	"fmt"
	"image"
	"math"
)

// CellBytes returns the cell colour of a 16-bit value: R=hi, G=lo, B=hi XOR lo (parity guard).
// A is always 255 (ignored by decode).
func CellBytes(v uint16) (r, g, b uint8) {
	hi, lo := uint8(v>>8), uint8(v)
	return hi, lo, hi ^ lo
}

// cellValue reassembles v from post-calibration bytes and checks the parity guard.
// Invalid means the cell is ABSENT (field-hold/easing semantics, never a frame reject).
func cellValue(r, g, b uint8) (uint16, bool) {
	return uint16(r)<<8 | uint16(g), absDiff(b, r^g) <= ParityTol
}

func absDiff(a, b uint8) int {
	d := int(a) - int(b)
	if d < 0 {
		d = -d
	}
	return d
}

// Calib is the two-point per-channel gain+offset derived from the BLACK/WHITE meta cells.
// Identity on a native capture (black=0, white=255). MidWarn flags the MID cell off by >6
// after correction - a sanity warning, never a reject.
type Calib struct {
	black   [3]float64
	scale   [3]float64 // 255/(white-black)
	MidWarn bool
}

// calibrate samples the calibration triad and builds the correction. Errors only when a channel
// is degenerate (white <= black - no invertible mapping exists).
func calibrate(img image.Image) (Calib, error) {
	b := sampleMetaRaw(img, ColCalBlack)
	m := sampleMetaRaw(img, ColCalMid)
	w := sampleMetaRaw(img, ColCalWhite)
	var c Calib
	for ch := 0; ch < 3; ch++ {
		if w[ch] <= b[ch] {
			return Calib{}, fmt.Errorf("mocappanel: degenerate calibration ch%d: black=%d white=%d", ch, b[ch], w[ch])
		}
		c.black[ch] = float64(b[ch])
		c.scale[ch] = 255 / float64(w[ch]-b[ch])
	}
	mid := c.Apply(m)
	for ch := 0; ch < 3; ch++ {
		if absDiff(mid[ch], 128) > 6 {
			c.MidWarn = true
		}
	}
	return c, nil
}

// Apply corrects raw captured bytes into contract bytes (round + clamp). Exact identity when
// calibration cells captured bit-exact.
func (c Calib) Apply(raw [3]uint8) [3]uint8 {
	var out [3]uint8
	for ch := 0; ch < 3; ch++ {
		v := (float64(raw[ch]) - c.black[ch]) * c.scale[ch]
		out[ch] = uint8(math.Round(math.Min(255, math.Max(0, v))))
	}
	return out
}

// metaCell reads meta cell col as a calibrated, parity-checked 16-bit value.
func (c Calib) metaCell(img image.Image, col int) (uint16, bool) {
	x, y := MetaSample(col)
	px := c.Apply(rawAt(img, x, y))
	return cellValue(px[0], px[1], px[2])
}

// dataCell reads data cell idx (row-major) as a calibrated, parity-checked 16-bit value.
func (c Calib) dataCell(img image.Image, idx int) (uint16, bool) {
	x, y := DataSample(idx)
	px := c.Apply(rawAt(img, x, y))
	return cellValue(px[0], px[1], px[2])
}

// sampleMetaRaw samples a meta cell centre WITHOUT calibration (the triad itself).
func sampleMetaRaw(img image.Image, col int) [3]uint8 {
	x, y := MetaSample(col)
	return rawAt(img, x, y)
}

// rawAt reads the 8-bit RGB at panel coords (x,y), honouring a non-zero bounds origin.
func rawAt(img image.Image, x, y int) [3]uint8 {
	b := img.Bounds()
	r, g, bl, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
	return [3]uint8{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8)}
}
