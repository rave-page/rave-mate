package musiclib

import (
	"encoding/binary"
	"path/filepath"
	"testing"
)

// Synthetic export.pdb builder - validates the page/row-group/row offset arithmetic end-to-end
// without needing a real USB device. Layout: page 0 = file header, then one single-row data
// page per table (first_page == last_page).

const tLenPage = 4096

func putU16(b []byte, off int, v uint16) { binary.LittleEndian.PutUint16(b[off:], v) }
func putU32(b []byte, off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }

// putShortStr writes a DeviceSQL short-ASCII string at off, returns bytes written.
func putShortStr(b []byte, off int, s string) int {
	b[off] = byte(((len(s) + 1) << 1) | 1) // length incl. flag byte, <<1 | 1
	copy(b[off+1:], s)
	return 1 + len(s)
}

// buildPage returns a 4096-byte data page with header set and a single row whose body bytes are
// `row`, placed at the heap start (ofs_row = 0). pageIndex/pageType fill the header; the row
// index encodes one present row in group 0.
func buildPage(pageIndex, pageType uint32, row []byte) []byte {
	p := make([]byte, tLenPage)
	putU32(p, 0x04, pageIndex)
	putU32(p, 0x08, pageType)
	putU32(p, 0x0C, pageIndex) // next_page = self → walker stops (also first==last)
	// header bitfield @0x18: num_row_offsets(13) | num_rows(11), big-endian across 3 bytes.
	// 1 offset, 1 row → value24 = (1<<11) | 1.
	v := (uint32(1) << 11) | 1
	p[0x18] = byte(v >> 16)
	p[0x19] = byte(v >> 8)
	p[0x1A] = byte(v)
	p[0x1B] = 0x24 // page_flags: data page (bit 0x40 clear)
	// row body at heap start (0x28).
	copy(p[pdbHeapStart:], row)
	// row index, group 0: base = len_page. present_flags @ base-4; ofs_row[0] @ base-6 = 0.
	putU16(p, tLenPage-4, 0x0001) // row 0 present
	putU16(p, tLenPage-6, 0)      // ofs_row[0] = 0 (heap start)
	return p
}

// nameRow builds a genre/label-style row: id u4 + short string at +4.
func nameRow(id uint32, name string) []byte {
	b := make([]byte, 64)
	putU32(b, 0, id)
	putShortStr(b, 4, name)
	return b
}

// artistRow builds an artist row (subtype 0x60 → near u1 name offset @0x09).
func artistRow(id uint32, name string) []byte {
	b := make([]byte, 64)
	putU16(b, 0x00, 0x0060)
	putU32(b, 0x04, id)
	b[0x09] = 0x0a // ofs_name_near → name at row+0x0a
	putShortStr(b, 0x0a, name)
	return b
}

// trackRow builds a track row with the FK ids + a couple strings (title @ ofs_strings[17],
// file_path @ [20]). Strings are packed right after the 0x88-byte fixed area.
func trackRow(id, artistID, genreID uint32, bpmX100 uint32, title, path string) []byte {
	b := make([]byte, 512)
	putU16(b, 0x00, 0x0024) // subtype
	putU32(b, 0x30, 320)    // bitrate
	putU32(b, 0x38, bpmX100)
	putU32(b, 0x3C, genreID)
	putU32(b, 0x44, artistID)
	putU32(b, 0x48, id)
	putU16(b, 0x54, 312) // duration
	b[0x59] = 4          // rating
	// strings live after ofs_strings array (0x5E + 21*2 = 0x88).
	strBase := 0x88
	titleOfs := strBase
	n := putShortStr(b, titleOfs, title)
	pathOfs := strBase + n
	putShortStr(b, pathOfs, path)
	putU16(b, 0x5E+2*17, uint16(titleOfs)) // ofs_strings[17] = title
	putU16(b, 0x5E+2*20, uint16(pathOfs))  // ofs_strings[20] = file_path
	return b
}

// buildPDB assembles a 4-page file: header + artists(1) + genres(2) + tracks(3).
func buildPDB() []byte {
	pages := [][]byte{
		make([]byte, tLenPage), // page 0: file header region
		buildPage(1, pdbArtists, artistRow(10, "DJ Synthetic")),
		buildPage(2, pdbGenres, nameRow(20, "Tech House")),
		buildPage(3, pdbTracks, trackRow(100, 10, 20, 12800, "Synthetic Drop", "/Contents/Test/drop.mp3")),
	}
	file := make([]byte, 0, tLenPage*len(pages))
	for _, p := range pages {
		file = append(file, p...)
	}
	// File header: len_page, num_tables, then 16-byte table pointers @0x1C.
	putU32(file, 0x04, tLenPage)
	putU32(file, 0x08, 3) // 3 tables
	tbl := func(idx int, typ, page uint32) {
		off := 0x1C + idx*16
		putU32(file, off+0, typ)
		putU32(file, off+8, page)  // first_page
		putU32(file, off+12, page) // last_page
	}
	tbl(0, pdbArtists, 1)
	tbl(1, pdbGenres, 2)
	tbl(2, pdbTracks, 3)
	return file
}

func TestParseRekordboxPDB(t *testing.T) {
	lib, err := ParseRekordboxPDB(buildPDB(), "E:\\")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(lib.Tracks) != 1 {
		t.Fatalf("tracks=%d, want 1", len(lib.Tracks))
	}
	tr := lib.Tracks[0]
	if tr.Title != "Synthetic Drop" {
		t.Errorf("title=%q", tr.Title)
	}
	if tr.Artist != "DJ Synthetic" {
		t.Errorf("artist=%q (FK resolve)", tr.Artist)
	}
	if tr.Genre != "Tech House" {
		t.Errorf("genre=%q (FK resolve)", tr.Genre)
	}
	if tr.BPM != 128 {
		t.Errorf("bpm=%v, want 128", tr.BPM)
	}
	if tr.Rating != 4 || tr.DurationSec != 312 || tr.BitrateBps != 320000 {
		t.Errorf("rating/dur/bitrate: %d %v %d", tr.Rating, tr.DurationSec, tr.BitrateBps)
	}
	// device path absolutized against the mount root (OS-native separators; deviceRoot was "E:\\").
	wantPath := filepath.Join("E:\\", filepath.FromSlash("/Contents/Test/drop.mp3"))
	if tr.Path != wantPath {
		t.Errorf("path=%q, want %q", tr.Path, wantPath)
	}
}

func TestDeviceSQLString(t *testing.T) {
	r := &pdbReader{}
	// short ASCII "Hi": flag = ((2+1)<<1)|1 = 7, then "Hi"
	r.d = []byte{0x07, 'H', 'i'}
	if got := r.devStr(0); got != "Hi" {
		t.Errorf("short=%q", got)
	}
	// long ASCII "Hello": flag 0x40, length u2 = 4+5 = 9, pad u1, then text
	long := []byte{0x40, 0x09, 0x00, 0x00, 'H', 'e', 'l', 'l', 'o'}
	r.d = long
	if got := r.devStr(0); got != "Hello" {
		t.Errorf("longASCII=%q", got)
	}
	// long UTF-16LE "Aé": flag 0x90, length = 4 + 4 bytes, pad, then UTF-16LE
	u16 := []byte{0x90, 0x08, 0x00, 0x00, 'A', 0x00, 0xe9, 0x00}
	r.d = u16
	if got := r.devStr(0); got != "Aé" {
		t.Errorf("utf16=%q", got)
	}
}
