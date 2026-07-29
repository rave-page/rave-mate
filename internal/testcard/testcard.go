// Package testcard renders and decodes a deterministic diagnostic picture for the media pipeline.
//
// Every frame carries its own identity IN THE PIXELS: sequence number, generation timestamp,
// session id, target fps and a "generator fell behind" flag, packed into a 16x7 grid of large
// black/white cells with a CRC16. The grid survives what the pipeline does to a frame - BGRA→NV12
// conversion, H.264 encode/decode, and scaling (OBS stretching the card to its canvas) - because
// cells are sampled at their centers from RELATIVE coordinates, not pixel offsets.
//
// That makes loss attribution deterministic instead of inferential: a receiver that decodes
// seq 100,101,101,101,105 has PROVEN two frozen repeats and three skipped frames, and the embedded
// timestamp turns "it's lagging more and more" into a measured drift curve. Routing the card
// DIRECTLY over a media route vs THROUGH OBS bisects the chain: if direct is clean and via-OBS
// freezes, the loss is in the OBS leg (their composition or our capture of their sender), full stop.
//
// The card also feeds the human eye and the framedebug oracle: 7-seg seq + wall clock, a per-frame
// parity flash, a sweeping bar and a hue band together move >4% of the frame every frame, so a
// frozen card is obvious in Resolume and PeakFrac reads well above framedebug.StaticFrac.
package testcard

import (
	"fmt"
	"image"
	"time"
)

// Geometry: everything is placed on a 48x27 cell lattice scaled to the frame, so any resolution
// >= ~480x270 renders and decodes the same card. Data cells are ~2% of frame width - big enough
// that a 4x downscale plus H.264 at streaming bitrates leaves cell centers unambiguous.
const (
	latW = 48
	latH = 27

	gridCols = 16
	gridRows = 7
	gridX    = 4 // cells; data grid top-left
	gridY    = 7

	calibY = 5 // calibration strip row (16 cells at gridX, alternating white/black)

	markerN = 3 // finder squares are 3x3 cells at TL/TR/BL
)

// Payload is one frame's identity. 112 bits on the wire (14 bytes = gridCols*gridRows cells).
type Payload struct {
	Session uint16 // 12 bits: random per generator run; a change = generator restarted
	Seq     uint32 // frame number, increments per SENT frame
	T0ms    uint32 // wall-clock ms at render, mod 2^32 (wrap-safe deltas up to ±24 days)
	FPS     uint8  // generator's target rate, so a receiver can compute expected seq progression
	Flags   uint8
}

// Flag bits.
const (
	FlagBehind = 1 << 0 // generator missed its previous tick: gaps at the receiver may be ITS fault
)

const payloadVer = 1 // 4-bit format version; bump on any layout/packing change

// DecodeErr says why a frame did not decode. NoCard is the cheap common case (not a testcard);
// the others mean a card was detected but arrived damaged.
type DecodeErr int

const (
	DecodeOK DecodeErr = iota
	ErrNoCard
	ErrLowContrast // calibration strip too flat: crushed or washed out beyond reading
	ErrCRC         // grid read but checksum failed (torn/blended frame)
	ErrVersion
)

func (e DecodeErr) String() string {
	switch e {
	case DecodeOK:
		return "ok"
	case ErrNoCard:
		return "no card"
	case ErrLowContrast:
		return "low contrast"
	case ErrCRC:
		return "crc mismatch"
	case ErrVersion:
		return "unknown version"
	}
	return fmt.Sprintf("decode-err %d", int(e))
}

// pack serializes p (14 bytes: ver:4 session:12 seq:32 t0:32 fps:8 flags:8 crc:16).
func pack(p Payload) [14]byte {
	var b [14]byte
	b[0] = payloadVer<<4 | byte(p.Session>>8)&0x0F
	b[1] = byte(p.Session)
	b[2], b[3], b[4], b[5] = byte(p.Seq>>24), byte(p.Seq>>16), byte(p.Seq>>8), byte(p.Seq)
	b[6], b[7], b[8], b[9] = byte(p.T0ms>>24), byte(p.T0ms>>16), byte(p.T0ms>>8), byte(p.T0ms)
	b[10] = p.FPS
	b[11] = p.Flags
	c := crc16(b[:12])
	b[12], b[13] = byte(c>>8), byte(c)
	return b
}

func unpack(b [14]byte) (Payload, DecodeErr) {
	if crc16(b[:12]) != uint16(b[12])<<8|uint16(b[13]) {
		return Payload{}, ErrCRC
	}
	if b[0]>>4 != payloadVer {
		return Payload{}, ErrVersion
	}
	return Payload{
		Session: uint16(b[0]&0x0F)<<8 | uint16(b[1]),
		Seq:     uint32(b[2])<<24 | uint32(b[3])<<16 | uint32(b[4])<<8 | uint32(b[5]),
		T0ms:    uint32(b[6])<<24 | uint32(b[7])<<16 | uint32(b[8])<<8 | uint32(b[9]),
		FPS:     b[10],
		Flags:   b[11],
	}, DecodeOK
}

// crc16 is CRC-16/CCITT-FALSE (poly 0x1021, init 0xFFFF) - detects the torn/blended frames the
// pipeline can produce, which a parity bit would wave through.
func crc16(data []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, d := range data {
		crc ^= uint16(d) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

// DeltaMs is now-minus-render time in ms, wrap-correct for the uint32 field. Positive = the frame
// is old. The result mixes both machines' clocks; DRIFT between successive deltas is offset-free.
func (p Payload) DeltaMs(now time.Time) int64 {
	return int64(int32(uint32(now.UnixMilli()) - p.T0ms))
}

// cellRect maps lattice cells [cx0,cy0)..(cx1,cy1) to pixels.
func cellRect(w, h, cx0, cy0, cx1, cy1 int) image.Rectangle {
	return image.Rect(cx0*w/latW, cy0*h/latH, cx1*w/latW, cy1*h/latH)
}

// cellCenter is the pixel center of one lattice cell.
func cellCenter(w, h, cx, cy int) (int, int) {
	return (cx*2 + 1) * w / (latW * 2), (cy*2 + 1) * h / (latH * 2)
}

// fillRect paints r solid. NRGBA, alpha 255 (a 0-alpha card renders empty in every viewer).
func fillRect(img *image.NRGBA, r image.Rectangle, cr, cg, cb byte) {
	r = r.Intersect(img.Rect)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := img.Pix[(y-img.Rect.Min.Y)*img.Stride:]
		for x := r.Min.X; x < r.Max.X; x++ {
			o := (x - img.Rect.Min.X) * 4
			row[o], row[o+1], row[o+2], row[o+3] = cr, cg, cb, 255
		}
	}
}

// luma3x3 averages a 3x3 patch's luminance around (x,y). Cheap, and enough: cells are tens of
// pixels wide, so the center never sits on a compression edge.
func luma3x3(img *image.NRGBA, x, y int) int {
	sum, n := 0, 0
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			px, py := x+dx, y+dy
			if px < img.Rect.Min.X || px >= img.Rect.Max.X || py < img.Rect.Min.Y || py >= img.Rect.Max.Y {
				continue
			}
			o := (py-img.Rect.Min.Y)*img.Stride + (px-img.Rect.Min.X)*4
			sum += int(img.Pix[o]) + int(img.Pix[o+1]) + int(img.Pix[o+2])
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / (3 * n)
}

// cellLuma samples one lattice cell's center.
func cellLuma(img *image.NRGBA, cx, cy int) int {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	x, y := cellCenter(w, h, cx, cy)
	return luma3x3(img, img.Rect.Min.X+x, img.Rect.Min.Y+y)
}

// markers: TL, TR, BL finder squares (QR-style, minus the fourth corner - that slot holds the
// parity flash, which alternates and would break a naive "four bright corners" check anyway).
var markers = [3][2]int{{1, 1}, {latW - markerN - 1, 1}, {1, latH - markerN - 1}}

// bgProbes are known-background cells near each marker; detection is RELATIVE (marker vs local
// background), so a range-squeezed decode (video-range YUV) still detects.
var bgProbes = [3][2]int{{markerN + 2, 2}, {latW - markerN - 3, 2}, {markerN + 2, latH - 3}}

// Render draws the full card into img (any size >= ~480x270; img must be W*4 stride, whole-frame
// Rect). now supplies the human clock; p supplies everything machine-read.
func Render(img *image.NRGBA, p Payload, now time.Time) {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	fillRect(img, img.Rect, 20, 20, 26) // dark slate, distinct from data black

	// Finder markers.
	for _, m := range markers {
		fillRect(img, cellRect(w, h, m[0], m[1], m[0]+markerN, m[1]+markerN), 255, 255, 255)
	}

	// Calibration strip: alternating white/black, white first.
	for i := range gridCols {
		v := byte(0)
		if i%2 == 0 {
			v = 255
		}
		fillRect(img, cellRect(w, h, gridX+i, calibY, gridX+i+1, calibY+1), v, v, v)
	}

	// Data grid, row-major, MSB first.
	bits := pack(p)
	for i := range gridCols * gridRows {
		v := byte(0)
		if bits[i/8]&(1<<(7-i%8)) != 0 {
			v = 255
		}
		cx, cy := gridX+i%gridCols, gridY+i/gridCols
		fillRect(img, cellRect(w, h, cx, cy, cx+1, cy+1), v, v, v)
	}

	// Parity flash (BR corner): alternates every frame - the classic dropped-frame checker.
	pv := byte(0)
	if p.Seq%2 == 0 {
		pv = 255
	}
	fillRect(img, cellRect(w, h, latW-markerN-1, latH-markerN-1, latW-1, latH-1), pv, pv, pv)

	// Hue band: full-width color that steps each frame - guarantees >3% of the frame changes, so
	// framedebug/PeakFrac reads MOVING, never "static with a live element".
	hr, hg, hb := hue(p.Seq)
	fillRect(img, cellRect(w, h, 1, latH-4, latW-markerN-2, latH-3), hr, hg, hb)

	// Sweep bar: 6-cell block cycling the width - motion the eye locks onto in Resolume.
	sx := 1 + int(p.Seq%uint32(latW-8))
	fillRect(img, cellRect(w, h, 1, latH-2, latW-1, latH-1), 45, 45, 55)
	fillRect(img, cellRect(w, h, sx, latH-2, sx+6, latH-1), 255, 255, 255)

	// Human text: seq (6 digits) and wall clock HH:MM:SS, 7-seg (no font dependency).
	drawDigits(img, cellRect(w, h, 22, 6, 46, 12), fmt.Sprintf("%06d", p.Seq%1000000))
	drawDigits(img, cellRect(w, h, 22, 14, 46, 20), now.Format("15:04:05"))

	// Session tag, small, under the clock: restarts must be visible to the eye too.
	drawDigits(img, cellRect(w, h, 34, 21, 46, 23), fmt.Sprintf("%04d", p.Session%10000))
}

// hue maps seq to a stepping color (period 96 frames).
func hue(seq uint32) (byte, byte, byte) {
	ph := seq % 96
	switch {
	case ph < 32:
		return byte(255 - ph*8), byte(ph * 8), 60
	case ph < 64:
		return 60, byte(255 - (ph-32)*8), byte((ph - 32) * 8)
	default:
		return byte((ph - 64) * 8), 60, byte(255 - (ph-64)*8)
	}
}

// Decode reads a full-frame card back out of img. The card must FILL the frame (direct routes
// always do; in OBS the card source must be stretched to the canvas - documented in the ctl help).
func Decode(img *image.NRGBA) (Payload, DecodeErr) {
	if img == nil || img.Rect.Dx() < latW*4 || img.Rect.Dy() < latH*4 {
		return Payload{}, ErrNoCard
	}
	// Finder check first: 6 tiny samples, so calling this on every non-card frame costs nothing.
	for i, m := range markers {
		mk := cellLuma(img, m[0]+markerN/2, m[1]+markerN/2)
		bg := cellLuma(img, bgProbes[i][0], bgProbes[i][1])
		if mk-bg < 48 {
			return Payload{}, ErrNoCard
		}
	}
	// Calibration: threshold from THIS frame's strip, so encode range shifts cancel out.
	whites, blacks := 0, 0
	for i := range gridCols {
		l := cellLuma(img, gridX+i, calibY)
		if i%2 == 0 {
			whites += l
		} else {
			blacks += l
		}
	}
	whites, blacks = whites/(gridCols/2), blacks/(gridCols/2)
	if whites-blacks < 40 {
		return Payload{}, ErrLowContrast
	}
	thresh := (whites + blacks) / 2

	var b [14]byte
	for i := range gridCols * gridRows {
		if cellLuma(img, gridX+i%gridCols, gridY+i/gridCols) > thresh {
			b[i/8] |= 1 << (7 - i%8)
		}
	}
	return unpack(b)
}
