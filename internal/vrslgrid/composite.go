package vrslgrid

// composite.go builds the streamable 16:9 VRSL frame from the DMX store, per
// .devnotes/VRSL_VIDEO_STREAM.md (mirror of the frozen world-repo contract VRSL_VIDEO_STREAM_CONTRACT.md).
//
// Two modes share ONE layout so the strip is always stock-VRSL-valid:
//   - standard: high-byte grid ONLY, at the rightmost 208 px of a 1920×H frame; rest black.
//     A stock VRSL world crops the right strip and lights normally (8-bit).
//   - extended: superset - adds a LEFT low-byte mirror grid (x[0,208]) + a 32-px-cell METADATA
//     band in the gap (calibration triad, integrity header {MAGIC,version,flags,baseUniverse,
//     universeCount,frameCounter,CRC8}, semantic lanes). RaveVRSLGridReader auto-detects the MAGIC
//     marker and decodes the richer data; a stock VRSL world still reads only the right strip.
//
// 16-bit: strip cell = high byte, low grid cell = low byte, v16 = high<<8|low. With an 8-bit
// Art-Net source there is no true fine byte, so we BIT-REPLICATE (low = high): high<<8|high =
// high*257 → /65535 == high/255 exactly (lossless vs 8-bit). A future 16-bit source overwrites the
// low byte with the real fine channel. loFrameValid is set so the reader does the 16-bit combine.
//
// COLORSPACE: raw DMX bytes, LINEAR, no gamma. The caller's ffmpeg MUST NOT apply any
// sRGB<->linear/colorspace filter (encode straight from rawvideo rgba).

import (
	"image"
	"image/color"
	"image/draw"
)

// Composite frame geometry (reference 1920×1080; height grows to fit the grid, width fixed so the
// reader's "rightmost ceil(w*208/1920)" crop lands on exactly 208 px).
const (
	FrameWidth     = 1920                     // fixed canvas width (strip = 208/1920 of it)
	FrameRefHeight = 1080                     // reference height; actual = max(this, gridHeight)
	StripX0        = FrameWidth - GridWidthPx // high-byte grid left edge (1712)
	LowGridX0      = 0                        // low-byte mirror grid left edge

	MetaCellPx = 32  // metadata band cell size (bigger than 16 = more transcode-robust for critical bytes)
	MetaBandX0 = 216 // metadata band left edge (col 0 origin), in the gap between the two grids
)

// Integrity header (extended mode) - byte values written into metadata band row-0 cells.
const (
	MagicR  = 0x52 // 'R' - marker byte 0
	MagicV  = 0x56 // 'V' - marker byte 1
	Version = 1    // header layout version

	FlagRGB9         = 1 << 0 // bit0: strip/low grids are rgb9-packed (else mono)
	FlagLoFrameValid = 1 << 1 // bit1: left low-byte grid carries valid data (do 16-bit combine)
)

// Metadata band row-0 cell columns (32-px cells, col 0 at x=MetaBandX0). Row 0 = calibration triad
// + integrity header; row 1 = semantic lanes.
const (
	metaColCal0     = 0  // calibration black (value 0)
	metaColCal1     = 1  // calibration mid   (value 128)
	metaColCal2     = 2  // calibration white (value 255)
	metaColMagic0   = 3  // 'R'
	metaColMagic1   = 4  // 'V'
	metaColVersion  = 5  // header version
	metaColFlags    = 6  // FlagRGB9 | FlagLoFrameValid
	metaColBaseUni  = 7  // first streamed universe (low byte)
	metaColUniCount = 8  // number of universe blocks
	metaColFrameCtr = 9  // wraps 0..255, advances every emitted frame (liveness)
	metaColCRC      = 10 // CRC8 over high+low grid bytes + semantic lanes

	metaLaneLookID   = 0 // row 1 col 0
	metaLaneSceneID  = 1 // row 1 col 1
	metaLaneBlackout = 2 // row 1 col 2 (0=normal, 255=hard blackout)
)

// CompositeSpec parametrises one rendered frame.
type CompositeSpec struct {
	Universes []int // Art-Net port-addresses, in block order
	Mono      bool  // true = mono packing; false = rgb9 (mirrors Mode)
	Extended  bool  // true = emit low-byte mirror grid + metadata band

	// Extended metadata (ignored when !Extended). Reserved lanes default to zero and the reader
	// tolerates/ignores unknown lanes (forward-compat).
	FrameCounter byte // advances every emitted frame incl. keepalives (caller owns the counter)
	LookID       byte // semantic lane: active look id (reserved for a VJ-state integration)
	SceneID      byte // semantic lane: active scene id (reserved)
	Blackout     byte // semantic lane: 0=normal, 255=hard blackout
}

// RenderComposite rasterizes the DMX store into the streamable frame for spec. Always opaque; cells
// carry raw DMX bytes (LINEAR, no gamma).
func RenderComposite(r Reader, spec CompositeSpec) *image.RGBA {
	mode := ModeRGB9
	if spec.Mono {
		mode = ModeMono
	}
	// Read every streamed universe once; reuse the bytes for BOTH the strip render and the CRC.
	unis := readUniverses(r, spec.Universes)
	gh := gridHeight(len(spec.Universes), mode)

	h := FrameRefHeight
	if gh > h {
		h = gh
	}
	if h%2 != 0 { // H.264 needs even dims
		h++
	}
	img := image.NewRGBA(image.Rect(0, 0, FrameWidth, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{0, 0, 0, 255}), image.Point{}, draw.Src)

	// High-byte grid → right strip (stock VRSL reads only this).
	drawGrid(img, StripX0, unis, mode, highByte)

	if !spec.Extended {
		return img
	}
	// Low-byte mirror grid → left. 8-bit source: low = high (bit-replication → lossless 16-bit).
	drawGrid(img, LowGridX0, unis, mode, lowByte)
	drawMetaBand(img, unis, mode, spec)
	return img
}

// gridHeight returns the strip's pixel height for n universes in the given mode.
func gridHeight(n int, mode Mode) int {
	blocks := n
	if mode == ModeRGB9 {
		blocks = 3 // u[0:3]→R, [3:6]→G, [6:9]→B always fold onto 3 blocks
	}
	if blocks < 1 {
		blocks = 1
	}
	return blocks * RowsPerUni * CellPx
}

// readUniverses snapshots each universe's 512 slots once (missing = zeroed).
func readUniverses(r Reader, universes []int) [][512]byte {
	out := make([][512]byte, len(universes))
	for i, u := range universes {
		if u < 0 {
			continue
		}
		out[i], _ = r.Get(uint16(u))
	}
	return out
}

// byteFn selects which byte of a channel value goes into a cell (high vs low of the 16-bit view).
type byteFn func(v byte) byte

func highByte(v byte) byte { return v } // strip carries the value (== high byte)
func lowByte(v byte) byte  { return v } // 8-bit source: low replicates high (lossless 16-bit)

// drawGrid paints one universe grid (mono or rgb9) into img with its left edge at xOff, top-aligned.
func drawGrid(img *image.RGBA, xOff int, unis [][512]byte, mode Mode, bf byteFn) {
	if mode == ModeRGB9 {
		get := func(idx int) [512]byte {
			if idx < len(unis) {
				return unis[idx]
			}
			return [512]byte{}
		}
		for j := 0; j < 3; j++ {
			dr, dg, db := get(j), get(j+3), get(j+6)
			for ch := 0; ch < ChPerUni; ch++ {
				cx, cy := cellForChannel(ch)
				fillCellAt(img, xOff, cx, j*RowsPerUni+cy, color.RGBA{bf(dr[ch]), bf(dg[ch]), bf(db[ch]), 255})
			}
		}
		return
	}
	blocks := len(unis)
	if blocks < 1 {
		blocks = 1
	}
	for i := 0; i < len(unis); i++ {
		data := unis[i]
		for ch := 0; ch < ChPerUni; ch++ {
			v := bf(data[ch])
			cx, cy := cellForChannel(ch)
			fillCellAt(img, xOff, cx, i*RowsPerUni+cy, color.RGBA{v, v, v, 255})
		}
	}
}

// fillCellAt paints a 16×16 cell at cell coords (cx,cy) offset by xOff pixels.
func fillCellAt(img *image.RGBA, xOff, cx, cy int, col color.RGBA) {
	x0, y0 := xOff+cx*CellPx, cy*CellPx
	for y := y0; y < y0+CellPx; y++ {
		for x := x0; x < x0+CellPx; x++ {
			img.SetRGBA(x, y, col)
		}
	}
}

// drawMetaBand paints the 32-px-cell metadata band: calibration triad + integrity header (row 0)
// and semantic lanes (row 1).
func drawMetaBand(img *image.RGBA, unis [][512]byte, mode Mode, spec CompositeSpec) {
	flags := byte(0)
	if mode == ModeRGB9 {
		flags |= FlagRGB9
	}
	flags |= FlagLoFrameValid // low grid present (bit-replicated for an 8-bit source)

	base := byte(0)
	if len(spec.Universes) > 0 {
		base = byte(spec.Universes[0]) // low byte; rigs use small universe ids
	}
	crc := crc8Frame(unis, spec)

	// Row 0: calibration triad, then the header.
	meta := func(col int, v byte) { fillMetaCell(img, col, 0, v) }
	meta(metaColCal0, 0)
	meta(metaColCal1, 128)
	meta(metaColCal2, 255)
	meta(metaColMagic0, MagicR)
	meta(metaColMagic1, MagicV)
	meta(metaColVersion, Version)
	meta(metaColFlags, flags)
	meta(metaColBaseUni, base)
	meta(metaColUniCount, byte(len(spec.Universes)))
	meta(metaColFrameCtr, spec.FrameCounter)
	meta(metaColCRC, crc)

	// Row 1: semantic lanes (reserved cols default 0).
	fillMetaCell(img, metaLaneLookID, 1, spec.LookID)
	fillMetaCell(img, metaLaneSceneID, 1, spec.SceneID)
	fillMetaCell(img, metaLaneBlackout, 1, spec.Blackout)
}

// fillMetaCell paints a 32×32 metadata cell at (col,row) as R=G=B=v.
func fillMetaCell(img *image.RGBA, col, row int, v byte) {
	x0 := MetaBandX0 + col*MetaCellPx
	y0 := row * MetaCellPx
	c := color.RGBA{v, v, v, 255}
	for y := y0; y < y0+MetaCellPx; y++ {
		for x := x0; x < x0+MetaCellPx; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

// crc8Frame computes the integrity CRC over the canonical byte order the reader recomputes:
// every universe's 512 HIGH bytes (block order), then every universe's 512 LOW bytes, then the
// semantic lanes [lookId, sceneId, blackout].
func crc8Frame(unis [][512]byte, spec CompositeSpec) byte {
	buf := make([]byte, 0, len(unis)*512*2+3)
	for _, u := range unis {
		for ch := 0; ch < ChPerUni; ch++ {
			buf = append(buf, highByte(u[ch]))
		}
	}
	for _, u := range unis {
		for ch := 0; ch < ChPerUni; ch++ {
			buf = append(buf, lowByte(u[ch]))
		}
	}
	buf = append(buf, spec.LookID, spec.SceneID, spec.Blackout)
	return crc8(buf)
}

// crc8 is CRC-8 (poly 0x07, init 0x00, MSB-first, no reflection, no final xor) - the reader
// recomputes it identically to validate the frame.
func crc8(data []byte) byte {
	var crc byte
	for _, b := range data {
		crc ^= b
		for i := 0; i < 8; i++ {
			if crc&0x80 != 0 {
				crc = crc<<1 ^ 0x07
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
