package shellplaces

// Portable, dependency-free readers for the Windows Quick Access pinned jump list:
//   %APPDATA%\Microsoft\Windows\Recent\AutomaticDestinations\f01b4d95cf55d32a.automaticDestinations-ms
// It is an MS-CFB (OLE compound file) whose "DestList" stream lists every Quick Access target with a
// pin-status int32 (-1 = frequent, >= 0 = pinned POSITION). readCFBStream extracts a stream by name;
// parseDestListPins returns the pinned real-filesystem paths in pin order. Both are pure byte code
// (no OS calls, no build tag) so they unit-test on any platform. The Windows caller
// (shellplaces_windows.go) validates the paths against the real fs and fails soft on any error.

import (
	"encoding/binary"
	"strings"
	"unicode/utf16"
)

func le16(b []byte, o int) uint16 {
	if o+2 > len(b) {
		return 0
	}
	return binary.LittleEndian.Uint16(b[o:])
}
func le32(b []byte, o int) uint32 {
	if o+4 > len(b) {
		return 0
	}
	return binary.LittleEndian.Uint32(b[o:])
}
func le64(b []byte, o int) uint64 {
	if o+8 > len(b) {
		return 0
	}
	return binary.LittleEndian.Uint64(b[o:])
}

const (
	cfbEndOfChain = 0xFFFFFFFE
	cfbFreeSect   = 0xFFFFFFFF
)

// readCFBStream extracts the named stream from an MS-CFB blob. ok=false on any malformation.
func readCFBStream(raw []byte, want string) (data []byte, ok bool) {
	defer func() { // pure arithmetic, but never let a crafted blob panic a caller
		if recover() != nil {
			data, ok = nil, false
		}
	}()
	if len(raw) < 512 || raw[0] != 0xD0 || raw[1] != 0xCF || raw[2] != 0x11 || raw[3] != 0xE0 {
		return nil, false
	}
	secSize := 1 << le16(raw, 30)
	miniSize := 1 << le16(raw, 32)
	miniCutoff := le32(raw, 56)
	firstDir := le32(raw, 48)
	firstMiniFAT := le32(raw, 60)
	numMiniFAT := le32(raw, 64)
	if secSize < 512 || secSize > 1<<16 || miniSize < 1 {
		return nil, false
	}

	sect := func(id uint32) []byte {
		off := (int(id) + 1) * secSize
		if id == cfbEndOfChain || id == cfbFreeSect || off < 0 || off >= len(raw) {
			return nil
		}
		if off+secSize > len(raw) { // final sector may be a partial file tail (data streams)
			return raw[off:]
		}
		return raw[off : off+secSize]
	}
	// FAT from the header DIFAT (first 109 entries; deep DIFAT unsupported - jump lists are small)
	var fat []uint32
	appendU32s := func(s []byte) {
		for i := 0; i+4 <= len(s); i += 4 {
			fat = append(fat, le32(s, i))
		}
	}
	for i := 0; i < 109; i++ {
		fs := le32(raw, 76+i*4)
		if fs == cfbFreeSect || fs == cfbEndOfChain {
			continue
		}
		appendU32s(sect(fs))
	}
	follow := func(start uint32, tbl []uint32) []uint32 {
		var chain []uint32
		seen := map[uint32]bool{}
		for s := start; s != cfbEndOfChain && s != cfbFreeSect && int(s) < len(tbl); s = tbl[s] {
			if seen[s] { // cycle guard
				break
			}
			seen[s] = true
			chain = append(chain, s)
			if len(chain) > 1<<20 {
				break
			}
		}
		return chain
	}
	streamFAT := func(start uint32) []byte {
		var out []byte
		for _, s := range follow(start, fat) {
			out = append(out, sect(s)...)
		}
		return out
	}
	var dir []byte
	for _, s := range follow(firstDir, fat) {
		dir = append(dir, sect(s)...)
	}
	if len(dir) < 128 {
		return nil, false
	}
	var miniFAT []uint32
	if numMiniFAT > 0 {
		mb := streamFAT(firstMiniFAT)
		for i := 0; i+4 <= len(mb); i += 4 {
			miniFAT = append(miniFAT, le32(mb, i))
		}
	}
	miniStream := streamFAT(le32(dir, 116)) // root entry #0 start = mini-stream container
	streamMini := func(start uint32, size uint64) []byte {
		var out []byte
		seen := map[uint32]bool{}
		for s := start; s != cfbEndOfChain && s != cfbFreeSect && int(s) < len(miniFAT); s = miniFAT[s] {
			if seen[s] {
				break
			}
			seen[s] = true
			o := int(s) * miniSize
			if o+miniSize <= len(miniStream) {
				out = append(out, miniStream[o:o+miniSize]...)
			}
			if uint64(len(out)) >= size {
				break
			}
		}
		if uint64(len(out)) > size {
			out = out[:size]
		}
		return out
	}

	for i := 0; i+128 <= len(dir); i += 128 {
		e := dir[i : i+128]
		nameLen := int(le16(e, 64))
		if nameLen < 2 || nameLen > 64 || e[66] != 2 { // objType 2 = stream
			continue
		}
		u16 := make([]uint16, (nameLen-2)/2)
		for j := range u16 {
			u16[j] = le16(e, j*2)
		}
		if string(utf16.Decode(u16)) != want {
			continue
		}
		start := le32(e, 116)
		size := le64(e, 120)
		if size == 0 || size > uint64(len(raw)) {
			return nil, false
		}
		if size < uint64(miniCutoff) {
			data = streamMini(start, size)
		} else {
			data = streamFAT(start)
		}
		if uint64(len(data)) > size {
			data = data[:size]
		}
		return data, uint64(len(data)) == size
	}
	return nil, false
}

// destPinNcharsBack is the byte distance from a DestList entry's path-length field (uint16) back to
// its pin-status int32 - constant within a file, 0x14 on Windows 11 (DestList version 6, observed).
const destPinNcharsBack = 0x14

// parseDestListPins returns the pinned real-filesystem paths from a DestList stream, in pin-position
// order. v6 entries are variable-length, so it locks onto each [nchars uint16][UTF-16 path] run
// (path must start with a drive letter or UNC) and reads the pin int32 at nchars-0x14, keeping the
// ones whose pin is a valid position [0, pinnedCount). Fail-soft: garbage/other versions -> nil.
func parseDestListPins(d []byte) []string {
	if len(d) < 32 {
		return nil
	}
	nEntries := le32(d, 4)
	nPinned := int(int32(le32(d, 8)))
	if nPinned <= 0 || nPinned > int(nEntries) || nPinned > 4096 {
		return nil
	}
	byPin := map[int]string{}
	for pos := 32 + destPinNcharsBack; pos+2 < len(d); pos += 2 {
		n := int(le16(d, pos))
		if n < 2 || n > 260 || pos+2+n*2 > len(d) {
			continue
		}
		p := decodeUTF16(d[pos+2 : pos+2+n*2])
		if !looksLikePath(p) {
			continue
		}
		pin := int(int32(le32(d, pos-destPinNcharsBack)))
		if pin >= 0 && pin < nPinned {
			if _, dup := byPin[pin]; !dup {
				byPin[pin] = p
			}
		}
		pos += n * 2 // skip the path body (loop adds +2)
	}
	out := make([]string, 0, len(byPin))
	for i := 0; i < nPinned; i++ {
		if p := byPin[i]; p != "" {
			out = append(out, p)
		}
	}
	return out
}

func decodeUTF16(b []byte) string {
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u16))
}

// looksLikePath accepts a printable "X:\…" or "\\…" target (rejects the known-folder shell IDs the
// default pins are stored as, and any binary noise).
func looksLikePath(p string) bool {
	if len(p) < 3 {
		return false
	}
	for _, r := range p {
		if r < 0x20 || r == 0xFFFD {
			return false
		}
	}
	if p[1] == ':' && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) {
		return true
	}
	return strings.HasPrefix(p, `\\`)
}
