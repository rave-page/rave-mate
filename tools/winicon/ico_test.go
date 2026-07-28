package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"testing"
)

// solid is an n×n brand-pink source image - the layout tests care about offsets, not pixels.
func solid(n int) image.Image {
	m := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := range n {
		for x := range n {
			i := m.PixOffset(x, y)
			m.Pix[i+0], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3] = 0xF7, 0x08, 0x64, 0xFF
		}
	}
	return m
}

// pngSig is the 8-byte PNG signature every entry blob must start with (entries are stored
// PNG-compressed, Vista+).
var pngSig = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// TestBuildICOLayout pins the ICONDIR/ICONDIRENTRY layout. System.Drawing refuses to ToBitmap a
// PNG-compressed 256px entry, so a GDI+ round-trip can NOT be the proof the file is well-formed;
// the offsets are checked directly instead.
func TestBuildICOLayout(t *testing.T) {
	pngs := make([][]byte, len(sizes))
	for i, s := range sizes {
		var err error
		if pngs[i], err = encodePNG(resize(solid(s), s)); err != nil {
			t.Fatalf("encode %dpx: %v", s, err)
		}
	}
	b := buildICO(pngs)

	if got := binary.LittleEndian.Uint16(b[0:]); got != 0 {
		t.Errorf("reserved = %d, want 0", got)
	}
	if got := binary.LittleEndian.Uint16(b[2:]); got != 1 {
		t.Errorf("type = %d, want 1 (icon)", got)
	}
	n := int(binary.LittleEndian.Uint16(b[4:]))
	if n != len(sizes) {
		t.Fatalf("count = %d, want %d", n, len(sizes))
	}

	wantOff := 6 + 16*n
	for i := range n {
		e := 6 + 16*i
		w, h := b[e+0], b[e+1]
		// 256 must be encoded as 0 in the single width/height byte - the only size that can't
		// be stored literally, and the one GDI+ then refuses to decode.
		want := byte(sizes[i] & 0xFF)
		if w != want || h != want {
			t.Errorf("entry %d (%dpx): w,h = %d,%d, want %d", i, sizes[i], w, h, want)
		}
		if got := binary.LittleEndian.Uint16(b[e+4:]); got != 1 {
			t.Errorf("entry %d: planes = %d, want 1", i, got)
		}
		if got := binary.LittleEndian.Uint16(b[e+6:]); got != 32 {
			t.Errorf("entry %d: bitcount = %d, want 32", i, got)
		}
		size := int(binary.LittleEndian.Uint32(b[e+8:]))
		off := int(binary.LittleEndian.Uint32(b[e+12:]))
		if size != len(pngs[i]) {
			t.Errorf("entry %d: size = %d, want %d", i, size, len(pngs[i]))
		}
		if off != wantOff { // blobs are contiguous, in entry order, right after the directory
			t.Errorf("entry %d: offset = %d, want %d", i, off, wantOff)
		}
		if off+size > len(b) {
			t.Fatalf("entry %d: offset+size = %d overruns file (%d bytes)", i, off+size, len(b))
		}
		if !bytes.Equal(b[off:off+8], pngSig) {
			t.Errorf("entry %d: blob at %d is not a PNG", i, off)
		}
		if !bytes.Equal(b[off:off+size], pngs[i]) {
			t.Errorf("entry %d: blob bytes differ from source PNG", i)
		}
		wantOff += size
	}
	if wantOff != len(b) {
		t.Errorf("trailing bytes: last blob ends at %d, file is %d bytes", wantOff, len(b))
	}
}

// TestBuildICOMatchesGroupIconEntries guards the invariant that the file and resource forms
// describe the SAME images: every field but the trailing id/offset must agree.
func TestBuildICOMatchesGroupIconEntries(t *testing.T) {
	pngs := make([][]byte, len(sizes))
	for i, s := range sizes {
		var err error
		if pngs[i], err = encodePNG(resize(solid(s), s)); err != nil {
			t.Fatalf("encode %dpx: %v", s, err)
		}
	}
	ico, grp := buildICO(pngs), groupIconDir(pngs)
	for i := range sizes {
		fe, ge := 6+16*i, 6+14*i
		if !bytes.Equal(ico[fe:fe+12], grp[ge:ge+12]) {
			t.Errorf("entry %d: first 12 bytes differ between .ico and RT_GROUP_ICON", i)
		}
		if got := binary.LittleEndian.Uint16(grp[ge+12:]); int(got) != i+1 {
			t.Errorf("entry %d: RT_ICON id = %d, want %d", i, got, i+1)
		}
	}
}
