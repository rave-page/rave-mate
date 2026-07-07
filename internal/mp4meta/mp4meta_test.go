package mp4meta

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// ── fake-MP4 builders ──

func mkBox(typ string, payload ...[]byte) []byte {
	n := 8
	for _, p := range payload {
		n += len(p)
	}
	b := make([]byte, 0, n)
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(n))
	copy(hdr[4:], typ)
	b = append(b, hdr[:]...)
	for _, p := range payload {
		b = append(b, p...)
	}
	return b
}

func mkHdlr(handler string) []byte {
	p := make([]byte, 24) // ver/flags + pre_defined + handler_type + reserved(12)
	copy(p[8:12], handler)
	return mkBox("hdlr", p)
}

func mkStco(offsets ...uint32) []byte {
	p := make([]byte, 8+4*len(offsets))
	binary.BigEndian.PutUint32(p[4:8], uint32(len(offsets)))
	for i, o := range offsets {
		binary.BigEndian.PutUint32(p[8+4*i:], o)
	}
	return mkBox("stco", p)
}

func mkCo64(offsets ...uint64) []byte {
	p := make([]byte, 8+8*len(offsets))
	binary.BigEndian.PutUint32(p[4:8], uint32(len(offsets)))
	for i, o := range offsets {
		binary.BigEndian.PutUint64(p[8+8*i:], o)
	}
	return mkBox("co64", p)
}

// fakeMP4 builds ftyp + moov(mvhd + audio trak + video trak w/ stco+co64) [+ mdat].
// moovFirst=true mimics +faststart (mdat after moov → offsets must shift).
func fakeMP4(moovFirst bool, stcoOffs []uint32, co64Offs []uint64) []byte {
	ftyp := mkBox("ftyp", []byte("isom\x00\x00\x00\x00"))
	atrak := mkBox("trak", mkBox("mdia", mkHdlr("soun"), mkBox("minf", mkBox("stbl", mkStco(1)))))
	stbl := []byte{}
	stbl = append(stbl, mkStco(stcoOffs...)...)
	if co64Offs != nil {
		stbl = append(stbl, mkCo64(co64Offs...)...)
	}
	vtrak := mkBox("trak",
		mkBox("tkhd", make([]byte, 12)),
		mkBox("mdia", mkHdlr("vide"), mkBox("minf", mkBox("stbl", stbl))))
	moov := mkBox("moov", mkBox("mvhd", make([]byte, 16)), atrak, vtrak)
	mdat := mkBox("mdat", []byte("framedataframedata"))
	var f []byte
	f = append(f, ftyp...)
	if moovFirst {
		f = append(f, moov...)
		f = append(f, mdat...)
	} else {
		f = append(f, mdat...)
		f = append(f, moov...)
	}
	return f
}

func writeTmp(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "test.mp4")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// findBoxPath walks nested boxes by type path (nth trak selectable via index list).
func boxAt(t *testing.T, buf []byte, start, end int, typ string, nth int) box {
	t.Helper()
	kids, err := children(buf, start, end)
	if err != nil {
		t.Fatalf("children: %v", err)
	}
	seen := 0
	for _, k := range kids {
		if k.typ == typ {
			if seen == nth {
				return k
			}
			seen++
		}
	}
	t.Fatalf("box %s[%d] not found in [%d,%d)", typ, nth, start, end)
	return box{}
}

// ── tests ──

func TestInjectSphericalFaststart(t *testing.T) {
	orig := fakeMP4(true, []uint32{1000, 2000}, []uint64{3000})
	p := writeTmp(t, orig)
	if err := InjectSphericalV1(p); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	ubLen := len(buildSphericalBox())
	if len(got) != len(orig)+ubLen {
		t.Fatalf("file grew %d, want %d", len(got)-len(orig), ubLen)
	}

	tops, err := scanTop(bytes.NewReader(got), int64(len(got)))
	if err != nil {
		t.Fatal(err)
	}
	if len(tops) != 3 || tops[0].typ != "ftyp" || tops[1].typ != "moov" || tops[2].typ != "mdat" {
		t.Fatalf("top boxes: %+v", tops)
	}
	moov := got[tops[1].off : tops[1].off+tops[1].size]

	// moov + video trak grew by exactly the uuid box.
	origMoovSize := int(binary.BigEndian.Uint32(orig[len(mkBox("ftyp", []byte("isom\x00\x00\x00\x00"))):]))
	if len(moov) != origMoovSize+ubLen {
		t.Fatalf("moov size %d, want %d", len(moov), origMoovSize+ubLen)
	}
	vtrak := boxAt(t, moov, 8, len(moov), "trak", 1)
	if !trakIsVideo(moov, vtrak) {
		t.Fatal("second trak should be video")
	}
	// uuid is the trak's LAST child with our UUID + XML.
	kids, err := children(moov, vtrak.off+8, vtrak.off+vtrak.size)
	if err != nil {
		t.Fatal(err)
	}
	last := kids[len(kids)-1]
	if last.typ != "uuid" {
		t.Fatalf("last video-trak child = %q, want uuid", last.typ)
	}
	if !bytes.Equal(moov[last.off+8:last.off+24], SphericalUUID[:]) {
		t.Fatal("uuid mismatch")
	}
	if string(moov[last.off+24:last.off+last.size]) != SphericalXML {
		t.Fatal("xml payload mismatch")
	}

	// audio trak untouched (bytes identical, stco offset 1 < insertion point kept).
	atrak := boxAt(t, moov, 8, len(moov), "trak", 0)
	if trakIsVideo(moov, atrak) {
		t.Fatal("first trak should be audio")
	}

	// chunk offsets shifted by ubLen (moov precedes mdat; offsets pointed past insertion).
	mdia := boxAt(t, moov, vtrak.off+8, vtrak.off+vtrak.size, "mdia", 0)
	minf := boxAt(t, moov, mdia.off+8, mdia.off+mdia.size, "minf", 0)
	st := boxAt(t, moov, minf.off+8, minf.off+minf.size, "stbl", 0)
	stco := boxAt(t, moov, st.off+8, st.off+st.size, "stco", 0)
	for i, want := range []uint32{1000 + uint32(ubLen), 2000 + uint32(ubLen)} {
		if v := binary.BigEndian.Uint32(moov[stco.off+16+4*i:]); v != want {
			t.Fatalf("stco[%d] = %d, want %d", i, v, want)
		}
	}
	co64 := boxAt(t, moov, st.off+8, st.off+st.size, "co64", 0)
	if v := binary.BigEndian.Uint64(moov[co64.off+16:]); v != 3000+uint64(ubLen) {
		t.Fatalf("co64[0] = %d, want %d", v, 3000+uint64(ubLen))
	}

	// mdat payload byte-identical.
	if !bytes.Equal(got[tops[2].off:tops[2].off+tops[2].size], orig[len(orig)-int(tops[2].size):]) {
		t.Fatal("mdat changed")
	}
}

func TestInjectSphericalMdatFirstKeepsOffsets(t *testing.T) {
	orig := fakeMP4(false, []uint32{24}, nil) // offsets point into mdat, before moov
	p := writeTmp(t, orig)
	if err := InjectSphericalV1(p); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	tops, err := scanTop(bytes.NewReader(got), int64(len(got)))
	if err != nil {
		t.Fatal(err)
	}
	moov := got[tops[2].off : tops[2].off+tops[2].size]
	vtrak := boxAt(t, moov, 8, len(moov), "trak", 1)
	mdia := boxAt(t, moov, vtrak.off+8, vtrak.off+vtrak.size, "mdia", 0)
	minf := boxAt(t, moov, mdia.off+8, mdia.off+mdia.size, "minf", 0)
	st := boxAt(t, moov, minf.off+8, minf.off+minf.size, "stbl", 0)
	stco := boxAt(t, moov, st.off+8, st.off+st.size, "stco", 0)
	if v := binary.BigEndian.Uint32(moov[stco.off+16:]); v != 24 {
		t.Fatalf("stco[0] = %d, want 24 (pre-moov offsets must not shift)", v)
	}
}

func TestInjectSphericalIdempotent(t *testing.T) {
	p := writeTmp(t, fakeMP4(true, []uint32{500}, nil))
	if err := InjectSphericalV1(p); err != nil {
		t.Fatal(err)
	}
	once, _ := os.ReadFile(p)
	if err := InjectSphericalV1(p); err != nil {
		t.Fatal(err)
	}
	twice, _ := os.ReadFile(p)
	if !bytes.Equal(once, twice) {
		t.Fatal("second inject modified the file")
	}
}

func TestInjectSphericalNoVideoTrak(t *testing.T) {
	ftyp := mkBox("ftyp", []byte("isom\x00\x00\x00\x00"))
	moov := mkBox("moov", mkBox("trak", mkBox("mdia", mkHdlr("soun"))))
	orig := append(append([]byte{}, ftyp...), moov...)
	p := writeTmp(t, orig)
	if err := InjectSphericalV1(p); err == nil {
		t.Fatal("want error for audio-only mp4")
	}
	got, _ := os.ReadFile(p)
	if !bytes.Equal(got, orig) {
		t.Fatal("failed inject must leave file untouched")
	}
}
