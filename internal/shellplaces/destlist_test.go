package shellplaces

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
	"unicode/utf16"
)

// destEntry builds one synthetic DestList entry: [pin int32][16 pad][nchars uint16][UTF-16 path][4
// trailer]. pin sits exactly 0x14 bytes before the path-length field, matching the observed v6 layout.
func destEntry(pin int32, path string) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, pin)
	b.Write(make([]byte, 0x14-4)) // pad so nchars lands 0x14 after pin
	u16 := utf16.Encode([]rune(path))
	_ = binary.Write(&b, binary.LittleEndian, uint16(len(u16)))
	for _, c := range u16 {
		_ = binary.Write(&b, binary.LittleEndian, c)
	}
	b.Write(make([]byte, 4)) // trailer
	return b.Bytes()
}

func destList(version, nPinned uint32, entries ...[]byte) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, version)
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(entries)))
	_ = binary.Write(&b, binary.LittleEndian, nPinned)
	b.Write(make([]byte, 32-12)) // rest of the 32-byte header
	for _, e := range entries {
		b.Write(e)
	}
	return b.Bytes()
}

func TestParseDestListPins(t *testing.T) {
	blob := destList(6, 2,
		destEntry(0, `C:\Pinned\First`),
		destEntry(-1, `C:\Frequent\Recent`),
		destEntry(1, `D:\Second Pin`),
		destEntry(-1, `E:\alsofrequent`),
	)
	got := parseDestListPins(blob)
	want := []string{`C:\Pinned\First`, `D:\Second Pin`} // pin order 0,1; frequent dropped
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseDestListPins = %#v, want %#v", got, want)
	}
}

func TestParseDestListPinsRejectsGarbage(t *testing.T) {
	if got := parseDestListPins([]byte{1, 2, 3}); got != nil {
		t.Errorf("short blob must yield nil, got %v", got)
	}
	// nPinned > entries is malformed -> nil (fail-soft)
	bad := destList(6, 99, destEntry(0, `C:\x\y`))
	if got := parseDestListPins(bad); got != nil {
		t.Errorf("malformed pinned-count must yield nil, got %v", got)
	}
}

// buildCFB writes a minimal MS-CFB (512-byte sectors, v3) holding one stream. data must be >= the
// 4096 mini cutoff so it lives in the regular FAT (keeps the writer small - no mini stream).
func buildCFB(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	const sec = 512
	pad := func(b []byte) []byte {
		if r := len(b) % sec; r != 0 {
			b = append(b, make([]byte, sec-r)...)
		}
		return b
	}
	dataSecs := (len(data) + sec - 1) / sec
	// sector map: 0 = FAT, 1 = directory, 2.. = stream data
	firstDataSec := 2
	// FAT
	fat := make([]uint32, sec/4)
	for i := range fat {
		fat[i] = 0xFFFFFFFF // FREESECT
	}
	fat[0] = 0xFFFFFFFD // FATSECT
	fat[1] = 0xFFFFFFFE // dir: ENDOFCHAIN
	for i := 0; i < dataSecs; i++ {
		if i == dataSecs-1 {
			fat[firstDataSec+i] = 0xFFFFFFFE
		} else {
			fat[firstDataSec+i] = uint32(firstDataSec + i + 1)
		}
	}
	fatBytes := make([]byte, sec)
	for i, v := range fat {
		binary.LittleEndian.PutUint32(fatBytes[i*4:], v)
	}
	// directory: entry0 Root, entry1 the stream
	dir := make([]byte, sec)
	putEntry := func(idx int, nm string, objType byte, start uint32, size uint64) {
		o := idx * 128
		u16 := utf16.Encode([]rune(nm))
		for i, c := range u16 {
			binary.LittleEndian.PutUint16(dir[o+i*2:], c)
		}
		binary.LittleEndian.PutUint16(dir[o+64:], uint16((len(u16)+1)*2))
		dir[o+66] = objType
		binary.LittleEndian.PutUint32(dir[o+68:], 0xFFFFFFFF)
		binary.LittleEndian.PutUint32(dir[o+72:], 0xFFFFFFFF)
		binary.LittleEndian.PutUint32(dir[o+76:], 0xFFFFFFFF)
		binary.LittleEndian.PutUint32(dir[o+116:], start)
		binary.LittleEndian.PutUint64(dir[o+120:], size)
	}
	putEntry(0, "Root Entry", 5, 0xFFFFFFFE, 0)
	putEntry(1, name, 2, uint32(firstDataSec), uint64(len(data)))
	// header
	h := make([]byte, sec)
	copy(h, []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1})
	binary.LittleEndian.PutUint16(h[28:], 0xFFFE) // byte order
	binary.LittleEndian.PutUint16(h[30:], 9)      // sector shift -> 512
	binary.LittleEndian.PutUint16(h[32:], 6)      // mini shift -> 64
	binary.LittleEndian.PutUint32(h[44:], 1)      // num FAT sectors
	binary.LittleEndian.PutUint32(h[48:], 1)      // first dir sector
	binary.LittleEndian.PutUint32(h[56:], 4096)   // mini cutoff
	binary.LittleEndian.PutUint32(h[60:], 0xFFFFFFFE)
	binary.LittleEndian.PutUint32(h[64:], 0) // num mini FAT
	binary.LittleEndian.PutUint32(h[68:], 0xFFFFFFFE)
	for i := 0; i < 109; i++ {
		binary.LittleEndian.PutUint32(h[76+i*4:], 0xFFFFFFFF)
	}
	binary.LittleEndian.PutUint32(h[76:], 0) // DIFAT[0] = FAT sector 0

	var out bytes.Buffer
	out.Write(h)
	out.Write(fatBytes)
	out.Write(dir)
	out.Write(pad(append([]byte(nil), data...)))
	return out.Bytes()
}

func TestReadCFBStream(t *testing.T) {
	payload := bytes.Repeat([]byte("DESTLIST-PAYLOAD"), 300) // 4800 bytes -> FAT stream
	cfb := buildCFB(t, "DestList", payload)
	got, ok := readCFBStream(cfb, "DestList")
	if !ok {
		t.Fatal("readCFBStream ok=false on a valid synthetic CFB")
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("stream mismatch: got %d bytes, want %d", len(got), len(payload))
	}
	if _, ok := readCFBStream(cfb, "Nope"); ok {
		t.Error("readCFBStream found a stream that isn't there")
	}
	if _, ok := readCFBStream([]byte("not a cfb"), "DestList"); ok {
		t.Error("readCFBStream accepted a non-CFB blob")
	}
}

// TestDestListRoundTrip: a synthetic CFB whose DestList stream carries pinned entries -> the pins
// come back through the full path (CFB extract + DestList parse), the way the Windows reader runs it.
func TestDestListRoundTrip(t *testing.T) {
	dl := destList(6, 1,
		destEntry(0, `C:\Projects\Keep`),
		destEntry(-1, `C:\tmp\junk`),
	)
	if len(dl) < 4096 {
		dl = append(dl, make([]byte, 4096-len(dl))...) // push into the FAT path
	}
	cfb := buildCFB(t, "DestList", dl)
	stream, ok := readCFBStream(cfb, "DestList")
	if !ok {
		t.Fatal("readCFBStream failed")
	}
	got := parseDestListPins(stream)
	if len(got) != 1 || got[0] != `C:\Projects\Keep` {
		t.Fatalf("round-trip pins = %#v, want [C:\\Projects\\Keep]", got)
	}
}
