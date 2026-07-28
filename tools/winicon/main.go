// Command winicon compiles a PNG into a Windows resource object (.syso) that embeds an
// application icon (RT_ICON + RT_GROUP_ICON). `go build` auto-links any *_windows_amd64.syso
// in the main package, so the produced .exe carries the icon in the taskbar, launcher and
// Explorer - both for the local Windows build and the CI mingw cross-build (no CI changes).
//
// Pure stdlib (no external deps) per the repo supply-chain rule. Icons are stored as
// PNG-compressed entries (Vista+; target is Win11), which sidesteps DIB/AND-mask encoding.
//
// Run from this dir:  go run .   (defaults below). Regenerate when the source icon changes.
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"image"
	"image/png"
	"log"
	"os"
)

// Icon sizes baked into the resource - native sizes for every common DPI so small
// renderings (16px taskbar) stay crisp instead of being downscaled from one big image.
var sizes = []int{16, 24, 32, 48, 64, 128, 256}

func main() {
	in := flag.String("in", "../../internal/ui/assets/icon.png", "source PNG")
	out := flag.String("out", "../../cmd/rave-mate/rsrc_windows_amd64.syso", "output .syso")
	ico := flag.String("ico", "../../native/zigui/src/shell/rave-shell.ico", "output .ico (rave-shell.exe resource; empty = skip)")
	flag.Parse()

	srcBytes, err := os.ReadFile(*in)
	if err != nil {
		log.Fatalf("read %s: %v", *in, err)
	}
	src, err := png.Decode(bytes.NewReader(srcBytes))
	if err != nil {
		log.Fatalf("decode %s: %v", *in, err)
	}

	pngs := make([][]byte, len(sizes))
	for i, s := range sizes {
		pngs[i], err = encodePNG(resize(src, s))
		if err != nil {
			log.Fatalf("encode %dpx: %v", s, err)
		}
	}

	rsrc, relocs := buildRsrc(pngs)
	obj := buildCOFF(rsrc, relocs)
	if err := os.WriteFile(*out, obj, 0o644); err != nil {
		log.Fatalf("write %s: %v", *out, err)
	}
	log.Printf("wrote %s (%d icon sizes, %d bytes)", *out, len(sizes), len(obj))

	// rave-shell.exe (the Zig window child) is a SEPARATE exe, so it needs its own copy of the
	// icon: a window's taskbar/title-bar icon comes from ITS process, not from the daemon's.
	// zig's resource compiler takes an .ico via a .rc, hence this second output from the one
	// source PNG - a hand-made second icon would be a second brand truth.
	if *ico != "" {
		b := buildICO(pngs)
		if err := os.WriteFile(*ico, b, 0o644); err != nil {
			log.Fatalf("write %s: %v", *ico, err)
		}
		log.Printf("wrote %s (%d icon sizes, %d bytes)", *ico, len(sizes), len(b))
	}
}

// buildICO lays out a standalone .ico: ICONDIR + ICONDIRENTRY[n] + the PNG blobs. Identical to
// groupIconDir's entries except the trailing member is a 4-byte FILE OFFSET instead of the
// 2-byte RT_ICON id - that one field is the whole difference between the file and resource forms.
func buildICO(pngs [][]byte) []byte {
	n := len(pngs)
	hdr := 6 + 16*n
	out := make([]byte, hdr, hdr+totalLen(pngs))
	binary.LittleEndian.PutUint16(out[0:], 0) // reserved
	binary.LittleEndian.PutUint16(out[2:], 1) // type: icon
	binary.LittleEndian.PutUint16(out[4:], uint16(n))
	off := hdr
	for i, p := range pngs {
		e := 6 + 16*i
		s := sizes[i]
		out[e+0] = byte(s & 0xFF)                    // width (0 == 256)
		out[e+1] = byte(s & 0xFF)                    // height
		out[e+2] = 0                                 // color count
		out[e+3] = 0                                 // reserved
		binary.LittleEndian.PutUint16(out[e+4:], 1)  // planes
		binary.LittleEndian.PutUint16(out[e+6:], 32) // bit count
		binary.LittleEndian.PutUint32(out[e+8:], uint32(len(p)))
		binary.LittleEndian.PutUint32(out[e+12:], uint32(off))
		off += len(p)
	}
	for _, p := range pngs {
		out = append(out, p...)
	}
	return out
}

func totalLen(bs [][]byte) int {
	n := 0
	for _, b := range bs {
		n += len(b)
	}
	return n
}

func encodePNG(img image.Image) ([]byte, error) {
	var b bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&b, img); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// resize area-averages src to a dst×dst RGBA image, working in premultiplied alpha so
// transparent source pixels don't bleed their (undefined) colour into the average.
func resize(src image.Image, dst int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	out := image.NewRGBA(image.Rect(0, 0, dst, dst))
	scaleX := float64(sw) / float64(dst)
	scaleY := float64(sh) / float64(dst)
	for dy := range dst {
		y0 := float64(dy) * scaleY
		y1 := y0 + scaleY
		for dx := range dst {
			x0 := float64(dx) * scaleX
			x1 := x0 + scaleX
			var rs, gs, bs, as, wsum float64
			for sy := int(y0); sy < int(y1)+1 && sy < sh; sy++ {
				wy := overlap(y0, y1, float64(sy), float64(sy+1))
				if wy <= 0 {
					continue
				}
				for sx := int(x0); sx < int(x1)+1 && sx < sw; sx++ {
					wx := overlap(x0, x1, float64(sx), float64(sx+1))
					if wx <= 0 {
						continue
					}
					w := wx * wy
					// color.RGBA64 from At() is already alpha-premultiplied.
					cr, cg, cb, ca := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					rs += float64(cr) * w
					gs += float64(cg) * w
					bs += float64(cb) * w
					as += float64(ca) * w
					wsum += w
				}
			}
			if wsum == 0 {
				continue
			}
			// Unpremultiply to straight RGBA8 for image/png.
			pa := as / wsum // premultiplied alpha, 0..65535
			a8 := uint8(pa / 257.0)
			var r8, g8, b8 uint8
			if pa > 0 {
				r8 = clamp8((rs / wsum) / pa * 255.0)
				g8 = clamp8((gs / wsum) / pa * 255.0)
				b8 = clamp8((bs / wsum) / pa * 255.0)
			}
			i := out.PixOffset(dx, dy)
			out.Pix[i+0] = r8
			out.Pix[i+1] = g8
			out.Pix[i+2] = b8
			out.Pix[i+3] = a8
		}
	}
	return out
}

func overlap(a0, a1, b0, b1 float64) float64 {
	lo := a0
	if b0 > lo {
		lo = b0
	}
	hi := a1
	if b1 < hi {
		hi = b1
	}
	if hi <= lo {
		return 0
	}
	return hi - lo
}

func clamp8(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}

// ── Windows resource (.rsrc) + COFF object ─────────────────────────────────────

const (
	rtIcon      = 3
	rtGroupIcon = 14
	langID      = 0x0409 // en-US

	imageRelAmd64Addr32NB = 0x0003
	imageSymClassStatic   = 3
	imageScnCntInitData   = 0x00000040
	imageScnMemRead       = 0x40000000
)

// reloc is a 32-bit image-relative fixup applied to an IMAGE_RESOURCE_DATA_ENTRY.OffsetToData
// field; the linker adds the final .rsrc RVA to the stored section-relative offset.
type reloc struct{ vaddr uint32 }

// buildRsrc lays out the resource directory tree (RT_ICON ids 1..N + one RT_GROUP_ICON id 1)
// and returns the section bytes plus the relocations for each leaf's OffsetToData.
func buildRsrc(pngs [][]byte) ([]byte, []reloc) {
	n := len(pngs)
	const (
		dirHdr   = 16 // IMAGE_RESOURCE_DIRECTORY
		dirEnt   = 8  // IMAGE_RESOURCE_DIRECTORY_ENTRY
		dataLeaf = 16 // IMAGE_RESOURCE_DATA_ENTRY
	)
	// Layout (offsets from section start):
	// root dir (2 type entries) | icon-type dir (n entries) | group-type dir (1) |
	// n icon id-dirs (1 lang each) | 1 group id-dir | (n+1) data leaves | blobs.
	off := 0
	rootOff := off
	off += dirHdr + 2*dirEnt
	iconTypeOff := off
	off += dirHdr + n*dirEnt
	groupTypeOff := off
	off += dirHdr + 1*dirEnt
	iconIDOff := make([]int, n)
	for i := range n {
		iconIDOff[i] = off
		off += dirHdr + 1*dirEnt
	}
	groupIDOff := off
	off += dirHdr + 1*dirEnt
	iconLeafOff := make([]int, n)
	for i := range n {
		iconLeafOff[i] = off
		off += dataLeaf
	}
	groupLeafOff := off
	off += dataLeaf
	// blobs (8-byte aligned)
	iconBlobOff := make([]int, n)
	for i := range n {
		off = align(off, 8)
		iconBlobOff[i] = off
		off += len(pngs[i])
	}
	off = align(off, 8)
	groupBlobOff := off
	grp := groupIconDir(pngs)
	off += len(grp)
	total := off

	buf := make([]byte, total)
	put16 := func(o int, v uint16) { binary.LittleEndian.PutUint16(buf[o:], v) }
	put32 := func(o int, v uint32) { binary.LittleEndian.PutUint32(buf[o:], v) }

	// root dir: 2 id entries (RT_ICON=3, RT_GROUP_ICON=14), sorted ascending.
	put16(rootOff+12, 0) // named
	put16(rootOff+14, 2) // id
	put32(rootOff+16+0, rtIcon)
	put32(rootOff+16+4, uint32(iconTypeOff)|0x80000000) // subdir
	put32(rootOff+16+8, rtGroupIcon)
	put32(rootOff+16+12, uint32(groupTypeOff)|0x80000000)

	// icon-type dir: n id entries (ids 1..n) → per-id dirs.
	put16(iconTypeOff+14, uint16(n))
	for i := range n {
		e := iconTypeOff + dirHdr + i*dirEnt
		put32(e+0, uint32(i+1)) // icon id
		put32(e+4, uint32(iconIDOff[i])|0x80000000)
	}
	// group-type dir: 1 id entry (id 1) → group id dir.
	put16(groupTypeOff+14, 1)
	put32(groupTypeOff+dirHdr+0, 1)
	put32(groupTypeOff+dirHdr+4, uint32(groupIDOff)|0x80000000)

	// per-icon id dir: 1 language entry → leaf.
	for i := range n {
		d := iconIDOff[i]
		put16(d+14, 1)
		put32(d+dirHdr+0, langID)
		put32(d+dirHdr+4, uint32(iconLeafOff[i])) // leaf (high bit clear)
	}
	// group id dir: 1 language entry → leaf.
	put16(groupIDOff+14, 1)
	put32(groupIDOff+dirHdr+0, langID)
	put32(groupIDOff+dirHdr+4, uint32(groupLeafOff))

	var relocs []reloc
	// icon leaves.
	for i := range n {
		l := iconLeafOff[i]
		put32(l+0, uint32(iconBlobOff[i])) // OffsetToData (section-relative; relocated)
		put32(l+4, uint32(len(pngs[i])))   // Size
		// CodePage(8)=0, Reserved(12)=0
		relocs = append(relocs, reloc{vaddr: uint32(l)})
	}
	// group leaf.
	put32(groupLeafOff+0, uint32(groupBlobOff))
	put32(groupLeafOff+4, uint32(len(grp)))
	relocs = append(relocs, reloc{vaddr: uint32(groupLeafOff)})

	// blobs.
	for i := range n {
		copy(buf[iconBlobOff[i]:], pngs[i])
	}
	copy(buf[groupBlobOff:], grp)

	return buf, relocs
}

// groupIconDir builds the GRPICONDIR (header for RT_GROUP_ICON) referencing RT_ICON ids 1..N.
func groupIconDir(pngs [][]byte) []byte {
	n := len(pngs)
	b := make([]byte, 6+14*n)
	binary.LittleEndian.PutUint16(b[0:], 0) // reserved
	binary.LittleEndian.PutUint16(b[2:], 1) // type: icon
	binary.LittleEndian.PutUint16(b[4:], uint16(n))
	for i, p := range pngs {
		e := 6 + 14*i
		s := sizes[i]
		b[e+0] = byte(s & 0xFF)                    // width (0 == 256)
		b[e+1] = byte(s & 0xFF)                    // height
		b[e+2] = 0                                 // color count
		b[e+3] = 0                                 // reserved
		binary.LittleEndian.PutUint16(b[e+4:], 1)  // planes
		binary.LittleEndian.PutUint16(b[e+6:], 32) // bit count
		binary.LittleEndian.PutUint32(b[e+8:], uint32(len(p)))
		binary.LittleEndian.PutUint16(b[e+12:], uint16(i+1)) // RT_ICON id
	}
	return b
}

func align(n, a int) int { return (n + a - 1) &^ (a - 1) }

// buildCOFF wraps the .rsrc bytes in an amd64 COFF object with one section, its relocations,
// and a single .rsrc section symbol the relocations target. This is what Go's host-object
// loader (cmd/link loadpe) expects from a resource .syso.
func buildCOFF(rsrc []byte, relocs []reloc) []byte {
	const (
		fileHdr = 20
		secHdr  = 40
		relSize = 10
		symSize = 18
	)
	dataOff := fileHdr + secHdr
	relOff := dataOff + len(rsrc)
	symOff := relOff + len(relocs)*relSize

	var b bytes.Buffer
	w16 := func(v uint16) { binary.Write(&b, binary.LittleEndian, v) }
	w32 := func(v uint32) { binary.Write(&b, binary.LittleEndian, v) }

	// IMAGE_FILE_HEADER
	w16(0x8664) // Machine: amd64
	w16(1)      // NumberOfSections
	w32(0)      // TimeDateStamp
	w32(uint32(symOff))
	w32(1) // NumberOfSymbols (1 section symbol, no aux)
	w16(0) // SizeOfOptionalHeader
	w16(0) // Characteristics

	// Section header: .rsrc
	name := [8]byte{}
	copy(name[:], ".rsrc")
	b.Write(name[:])
	w32(0)                 // VirtualSize
	w32(0)                 // VirtualAddress
	w32(uint32(len(rsrc))) // SizeOfRawData
	w32(uint32(dataOff))   // PointerToRawData
	w32(uint32(relOff))    // PointerToRelocations
	w32(0)                 // PointerToLinenumbers
	w16(uint16(len(relocs)))
	w16(0)                                     // NumberOfLinenumbers
	w32(imageScnCntInitData | imageScnMemRead) // Characteristics

	// Raw section data.
	b.Write(rsrc)

	// Relocations (target symbol index 0 = the .rsrc section symbol).
	for _, r := range relocs {
		w32(r.vaddr) // VirtualAddress (section-relative)
		w32(0)       // SymbolTableIndex
		w16(imageRelAmd64Addr32NB)
	}

	// Symbol table: one ".rsrc" static symbol, value 0, section 1.
	var sname [8]byte
	copy(sname[:], ".rsrc")
	b.Write(sname[:])
	w32(0) // Value
	w16(1) // SectionNumber (1-based)
	w16(0) // Type
	b.WriteByte(imageSymClassStatic)
	b.WriteByte(0) // NumberOfAuxSymbols

	// String table: minimal (size field = 4, no entries).
	w32(4)

	return b.Bytes()
}
