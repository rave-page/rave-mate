package icecast

import (
	"encoding/binary"
	"testing"
)

// buildVorbisCommentPacket builds a Vorbis comment header packet (0x03 "vorbis" + comments).
func buildVorbisCommentPacket(comments ...string) []byte {
	var p []byte
	p = append(p, 0x03)
	p = append(p, []byte("vorbis")...)
	p = appendU32(p, 0) // vendor length 0
	p = appendU32(p, uint32(len(comments)))
	for _, c := range comments {
		p = appendU32(p, uint32(len(c)))
		p = append(p, []byte(c)...)
	}
	p = append(p, 0x01) // framing bit
	return p
}

// buildOggPage wraps one packet (<255 bytes) into a single Ogg page with the given serial.
func buildOggPage(serial uint32, bos bool, packet []byte) []byte {
	if len(packet) >= 255 {
		panic("test packet too large for single-segment page")
	}
	var pg []byte
	pg = append(pg, []byte("OggS")...)
	pg = append(pg, 0) // version
	ht := byte(0)
	if bos {
		ht = 0x02
	}
	pg = append(pg, ht)
	pg = append(pg, make([]byte, 8)...) // granule
	pg = appendU32(pg, serial)
	pg = appendU32(pg, 0)               // page seq
	pg = append(pg, make([]byte, 4)...) // CRC (scanner ignores)
	pg = append(pg, 1)                  // 1 segment
	pg = append(pg, byte(len(packet)))  // lacing < 255 => packet terminates
	pg = append(pg, packet...)
	return pg
}

func appendU32(b []byte, v uint32) []byte {
	var u [4]byte
	binary.LittleEndian.PutUint32(u[:], v)
	return append(b, u[:]...)
}

func TestOggScannerExtractsComments(t *testing.T) {
	var got []Meta
	sc := newOggScanner(func(m Meta) { got = append(got, m) })
	pkt := buildVorbisCommentPacket("ARTIST=Boris Brejcha", "TITLE=Gravity")
	sc.feed(buildOggPage(1, true, pkt))

	if len(got) != 1 {
		t.Fatalf("want 1 meta, got %d (%+v)", len(got), got)
	}
	if got[0].Artist != "Boris Brejcha" || got[0].Title != "Gravity" {
		t.Fatalf("bad meta: %+v", got[0])
	}
}

func TestOggScannerChainedStreamReFires(t *testing.T) {
	var got []Meta
	sc := newOggScanner(func(m Meta) { got = append(got, m) })
	sc.feed(buildOggPage(1, true, buildVorbisCommentPacket("ARTIST=A", "TITLE=One")))
	sc.feed(buildOggPage(2, true, buildVorbisCommentPacket("ARTIST=B", "TITLE=Two")))
	if len(got) != 2 || got[1].Title != "Two" || got[1].Artist != "B" {
		t.Fatalf("chained stream metas: %+v", got)
	}
}

func TestOggScannerByteAtATime(t *testing.T) {
	var got []Meta
	sc := newOggScanner(func(m Meta) { got = append(got, m) })
	page := buildOggPage(7, true, buildVorbisCommentPacket("TITLE=Dripping"))
	for _, b := range page { // pathological 1-byte chunks must still frame the page
		sc.feed([]byte{b})
	}
	if len(got) != 1 || got[0].Title != "Dripping" {
		t.Fatalf("byte-at-a-time metas: %+v", got)
	}
}

func TestOggScannerResyncsMidStream(t *testing.T) {
	var got []Meta
	sc := newOggScanner(func(m Meta) { got = append(got, m) })
	// Garbage (as if we joined mid-page) followed by a real page - scanner must resync.
	sc.feed([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00, 0x11})
	sc.feed(buildOggPage(3, true, buildVorbisCommentPacket("ARTIST=Resync", "TITLE=OK")))
	if len(got) != 1 || got[0].Artist != "Resync" {
		t.Fatalf("resync metas: %+v", got)
	}
}

func TestOggScannerIgnoresTruncatedPacket(t *testing.T) {
	var got []Meta
	sc := newOggScanner(func(m Meta) { got = append(got, m) })
	pkt := buildVorbisCommentPacket("TITLE=Whole")
	sc.feed(buildOggPage(1, true, pkt[:len(pkt)-3])) // chop the comment short
	if len(got) != 0 {
		t.Fatalf("truncated packet should yield no meta, got %+v", got)
	}
}

func TestParseSong(t *testing.T) {
	if m := parseSong("Charlotte de Witte - Doppler"); m.Artist != "Charlotte de Witte" || m.Title != "Doppler" {
		t.Fatalf("artist-title split: %+v", m)
	}
	if m := parseSong("JustATitle"); m.Title != "JustATitle" || m.Artist != "" {
		t.Fatalf("no-separator song: %+v", m)
	}
}

func TestFormatFromContentType(t *testing.T) {
	cases := map[string]string{
		"audio/ogg":              "ogg",
		"application/ogg":        "ogg",
		"audio/mpeg":             "mp3",
		"audio/aac":              "aac",
		"audio/mpeg; charset=u8": "mp3",
		"weird/thing":            "bin",
	}
	for ct, want := range cases {
		if got := formatFromContentType(ct); got != want {
			t.Errorf("formatFromContentType(%q)=%q want %q", ct, got, want)
		}
	}
}
