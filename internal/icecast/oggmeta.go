package icecast

import (
	"encoding/binary"
	"strings"
)

// maxOggBuf caps the unparsed scanner buffer so a stream we can't frame (we connected
// mid-page, or it isn't really Ogg) can't grow memory without bound.
const maxOggBuf = 1 << 20 // 1 MiB

// oggScanner extracts now-playing metadata from a live Ogg stream incrementally: it frames
// Ogg pages out of arbitrary byte chunks, reassembles the logical packets, and parses any
// Vorbis-comment / OpusTags header packet into a Meta. On a chained stream (Traktor starts a
// fresh logical bitstream per track) the new comment header re-fires onMeta. It is
// resilient to joining mid-stream - partial leading bytes are resynced to the next "OggS".
type oggScanner struct {
	onMeta  func(Meta)
	buf     []byte
	pending map[uint32][]byte // per-serial partial packet carried across pages
}

func newOggScanner(onMeta func(Meta)) *oggScanner {
	return &oggScanner{onMeta: onMeta, pending: map[uint32][]byte{}}
}

// feed ingests a chunk of the stream and parses out whatever complete pages it now holds.
func (s *oggScanner) feed(b []byte) {
	s.buf = append(s.buf, b...)
	for {
		n := s.tryPage()
		if n == 0 {
			break // need more data for the page at buf[0]
		}
		if n < 0 {
			if !s.resync() {
				break
			}
			continue
		}
		s.buf = s.buf[n:]
	}
	if len(s.buf) > maxOggBuf {
		s.buf = s.buf[len(s.buf)-maxOggBuf:]
	}
}

// tryPage returns the byte length of the complete Ogg page at buf[0] (>0), 0 if more data is
// needed, or -1 if buf[0] is not a page capture pattern.
func (s *oggScanner) tryPage() int {
	const minHeader = 27
	if len(s.buf) < minHeader {
		return 0
	}
	if string(s.buf[0:4]) != "OggS" {
		return -1
	}
	headerType := s.buf[5]
	serial := binary.LittleEndian.Uint32(s.buf[14:18])
	nseg := int(s.buf[26])
	if len(s.buf) < minHeader+nseg {
		return 0
	}
	segTable := s.buf[27 : 27+nseg]
	bodyLen := 0
	for _, l := range segTable {
		bodyLen += int(l)
	}
	pageLen := minHeader + nseg + bodyLen
	if len(s.buf) < pageLen {
		return 0
	}
	s.handlePage(serial, headerType, segTable, s.buf[27+nseg:pageLen])
	return pageLen
}

// resync drops bytes up to the next "OggS" capture pattern (we joined mid-page / mid-garbage).
func (s *oggScanner) resync() bool {
	if i := indexAfter(s.buf, 1); i >= 0 {
		s.buf = s.buf[i:]
		return true
	}
	// keep the last 3 bytes - a capture pattern may straddle the next chunk boundary.
	if len(s.buf) > 3 {
		s.buf = s.buf[len(s.buf)-3:]
	}
	return false
}

// indexAfter returns the index of the next "OggS" at or after start, or -1.
func indexAfter(b []byte, start int) int {
	if start < 0 {
		start = 0
	}
	if start >= len(b) {
		return -1
	}
	if i := strings.Index(string(b[start:]), "OggS"); i >= 0 {
		return start + i
	}
	return -1
}

// handlePage assembles packets from one page and dispatches completed ones.
func (s *oggScanner) handlePage(serial uint32, headerType byte, segTable, body []byte) {
	cur := s.pending[serial]
	if headerType&0x02 != 0 { // BOS: fresh logical bitstream
		cur = nil
	}
	off := 0
	for _, l := range segTable {
		end := min(off+int(l), len(body))
		cur = append(cur, body[off:end]...)
		off = end
		if l < 255 { // lacing < 255 terminates the packet
			s.onPacket(cur)
			cur = nil
		}
	}
	s.pending[serial] = cur
}

// onPacket parses a Vorbis-comment (Vorbis) or OpusTags (Opus) header packet into Meta.
func (s *oggScanner) onPacket(p []byte) {
	switch {
	case len(p) >= 7 && p[0] == 0x03 && string(p[1:7]) == "vorbis":
		if m, ok := parseVorbisComments(p[7:]); ok {
			s.onMeta(m)
		}
	case len(p) >= 8 && string(p[0:8]) == "OpusTags":
		if m, ok := parseVorbisComments(p[8:]); ok {
			s.onMeta(m)
		}
	}
}

// parseVorbisComments parses the vendor + user-comment list shared by Vorbis comment headers
// and OpusTags, extracting TITLE/ARTIST. Bounds-checked so a truncated mid-stream packet
// never panics.
func parseVorbisComments(b []byte) (Meta, bool) {
	vlen, ok := readU32(b)
	if !ok {
		return Meta{}, false
	}
	b = b[4:]
	if int(vlen) > len(b) {
		return Meta{}, false
	}
	b = b[vlen:] // skip vendor string
	n, ok := readU32(b)
	if !ok {
		return Meta{}, false
	}
	b = b[4:]
	var m Meta
	for range n {
		clen, ok := readU32(b)
		if !ok {
			break
		}
		b = b[4:]
		if int(clen) > len(b) {
			break
		}
		c := string(b[:clen])
		b = b[clen:]
		key, val := splitComment(c)
		switch strings.ToUpper(key) {
		case "TITLE":
			m.Title = strings.TrimSpace(val)
		case "ARTIST":
			m.Artist = strings.TrimSpace(val)
		}
	}
	if m.empty() {
		return Meta{}, false
	}
	return m, true
}

func splitComment(c string) (key, val string) {
	key, val, _ = strings.Cut(c, "=")
	return key, val
}

func readU32(b []byte) (uint32, bool) {
	if len(b) < 4 {
		return 0, false
	}
	return binary.LittleEndian.Uint32(b[:4]), true
}
