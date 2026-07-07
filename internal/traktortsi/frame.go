// Package traktortsi reads + edits Traktor's binary controller-mapping blob (the "DIOM"
// payload of the DeviceIO.Config.Controller entry inside Traktor Settings.tsi). The blob is
// FourCC-framed: each frame is a 4-byte ASCII tag + uint32 big-endian length + that many
// payload bytes. Container frames start their payload with a uint32 child count; strings are
// a uint32 big-endian character count followed by UTF-16BE code units. This package lets
// rave-mate list / add / remove / retarget controller mappings (the RavePage + Denon maps)
// without hand-importing .tsi files in Traktor's Controller Manager.
package traktortsi

import (
	"encoding/binary"
	"errors"
	"unicode/utf16"
)

var errTrunc = errors.New("traktortsi: truncated frame")

// frame is one TAG+LEN+payload unit. payload aliases the source buffer (read-only use).
type frame struct {
	tag     string
	payload []byte
}

// readFrames parses sequential frames until b is exhausted.
func readFrames(b []byte) ([]frame, error) {
	var out []frame
	for len(b) > 0 {
		if len(b) < 8 {
			return nil, errTrunc
		}
		tag := string(b[:4])
		n := binary.BigEndian.Uint32(b[4:8])
		if uint64(n) > uint64(len(b)-8) {
			return nil, errTrunc
		}
		out = append(out, frame{tag: tag, payload: b[8 : 8+n]})
		b = b[8+n:]
	}
	return out, nil
}

// find returns the first child frame with tag t (ok=false if absent).
func find(frames []frame, t string) (frame, bool) {
	for _, f := range frames {
		if f.tag == t {
			return f, true
		}
	}
	return frame{}, false
}

// readString reads a uint32-BE char count + UTF-16BE string; returns the string + remainder.
func readString(b []byte) (string, []byte, error) {
	if len(b) < 4 {
		return "", nil, errTrunc
	}
	n := int(binary.BigEndian.Uint32(b[:4]))
	b = b[4:]
	if n < 0 || len(b) < n*2 {
		return "", nil, errTrunc
	}
	u := make([]uint16, n)
	for i := 0; i < n; i++ {
		u[i] = binary.BigEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u)), b[n*2:], nil
}

// ── writers (used by the add/remove/retarget editor) ─────────────────────────

// putString appends a uint32-BE char count + UTF-16BE encoding of s to dst.
func putString(dst []byte, s string) []byte {
	u := utf16.Encode([]rune(s))
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(u)))
	for _, c := range u {
		dst = binary.BigEndian.AppendUint16(dst, c)
	}
	return dst
}

// putFrame appends a TAG+LEN+payload frame to dst.
func putFrame(dst []byte, tag string, payload []byte) []byte {
	dst = append(dst, tag...)
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(payload)))
	return append(dst, payload...)
}
