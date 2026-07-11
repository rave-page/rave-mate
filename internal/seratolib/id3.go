package seratolib

// Minimal, careful ID3v2.3/2.4 frame splicer. Goal: replace/insert exactly ONE GEOB frame
// (description "Serato BeatGrid") while every other frame passes through byte-exact and the
// audio region is never touched. We refuse anything we can't rewrite provably-safely
// (ID3v2.2, v2.4 tag-level unsync, non-syncsafe v2.4 sizes, corrupt frame IDs) rather than
// guess on users' music files.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf16"
)

const id3Header = 10

// id3Frame is one raw frame: header+body verbatim as stored (after any tag-level resync).
type id3Frame struct {
	id  string
	raw []byte
}

// id3Tag is a parsed tag sufficient for lossless frame splicing.
type id3Tag struct {
	major, revision, flags byte
	frames                 []id3Frame
	padding                int
	hadFooter              bool
	droppedExt             bool // extended header stripped (stale CRC) - flag cleared on write
	resynced               bool // v2.3 tag-level unsynchronisation removed - flag cleared on write
}

// parseID3 parses the leading ID3v2 tag of data. Returns (nil, 0, nil) when data has no tag;
// audioOff is the first byte after tag (+footer).
func parseID3(data []byte) (*id3Tag, int, error) {
	if len(data) < id3Header || string(data[:3]) != "ID3" {
		return nil, 0, nil
	}
	t := &id3Tag{major: data[3], revision: data[4], flags: data[5]}
	if t.major != 3 && t.major != 4 {
		return nil, 0, fmt.Errorf("seratolib: unsupported ID3v2.%d tag", t.major)
	}
	size, ok := syncsafe(data[6:10])
	if !ok {
		return nil, 0, errors.New("seratolib: corrupt ID3 tag size")
	}
	if id3Header+size > len(data) {
		return nil, 0, errors.New("seratolib: ID3 tag size exceeds file")
	}
	body := data[id3Header : id3Header+size]
	audioOff := id3Header + size
	if t.flags&0x10 != 0 { // v2.4 footer follows the tag body
		t.hadFooter = true
		if audioOff+id3Header > len(data) || string(data[audioOff:audioOff+3]) != "3DI" {
			return nil, 0, errors.New("seratolib: ID3 footer flag set but no footer present")
		}
		audioOff += id3Header
	}
	if t.flags&0x80 != 0 { // tag-level unsynchronisation
		if t.major == 4 {
			// v2.4 tag flag = "every frame has its unsync bit"; files that set only the tag
			// flag are ambiguous. Refuse rather than mis-slice frames.
			return nil, 0, errors.New("seratolib: ID3v2.4 tag-level unsynchronisation unsupported")
		}
		body = resync(body)
		t.resynced = true
	}
	pos := 0
	if t.flags&0x40 != 0 { // extended header: skip + drop (its CRC would go stale)
		if len(body) < 4 {
			return nil, 0, errors.New("seratolib: truncated ID3 extended header")
		}
		if t.major == 4 {
			es, ok := syncsafe(body[0:4])
			if !ok || es < 6 || es > len(body) {
				return nil, 0, errors.New("seratolib: corrupt ID3v2.4 extended header")
			}
			pos = es // v2.4 size includes the size field
		} else {
			es := int(binary.BigEndian.Uint32(body[0:4]))
			if es < 0 || 4+es > len(body) {
				return nil, 0, errors.New("seratolib: corrupt ID3v2.3 extended header")
			}
			pos = 4 + es // v2.3 size excludes the size field
		}
		t.droppedExt = true
	}
	for pos+id3Header <= len(body) && body[pos] != 0 {
		id := string(body[pos : pos+4])
		if !validFrameID(id) {
			return nil, 0, fmt.Errorf("seratolib: corrupt frame ID %q at %d", id, pos)
		}
		var fsize int
		if t.major == 4 {
			s, ok := syncsafe(body[pos+4 : pos+8])
			if !ok {
				// Non-syncsafe v2.4 sizes (broken writers) - refuse, don't guess.
				return nil, 0, fmt.Errorf("seratolib: frame %s has non-syncsafe v2.4 size", id)
			}
			fsize = s
		} else {
			fsize = int(binary.BigEndian.Uint32(body[pos+4 : pos+8]))
		}
		if fsize < 0 || pos+id3Header+fsize > len(body) {
			return nil, 0, fmt.Errorf("seratolib: frame %s size %d overruns tag", id, fsize)
		}
		t.frames = append(t.frames, id3Frame{id: id, raw: body[pos : pos+id3Header+fsize]})
		pos += id3Header + fsize
	}
	t.padding = len(body) - pos
	return t, audioOff, nil
}

// render serializes the tag: header (unsync + ext-header flags cleared) + frames + padding
// (+ regenerated footer when the original had one).
func (t *id3Tag) render() ([]byte, error) {
	var frames []byte
	for _, f := range t.frames {
		frames = append(frames, f.raw...)
	}
	size := len(frames) + t.padding
	if size >= 1<<28 {
		return nil, errors.New("seratolib: ID3 tag exceeds 256MB syncsafe limit")
	}
	flags := t.flags &^ (0x80 | 0x40) // never write unsynced; extended header dropped
	out := make([]byte, 0, id3Header+size)
	out = append(out, 'I', 'D', '3', t.major, t.revision, flags)
	out = appendSyncsafe(out, size)
	out = append(out, frames...)
	out = append(out, make([]byte, t.padding)...)
	if t.hadFooter {
		out = append(out, '3', 'D', 'I', t.major, t.revision, flags)
		out = appendSyncsafe(out, size)
	}
	return out, nil
}

// spliceID3Beatgrid returns a copy of data (a whole MP3 file) with its "Serato BeatGrid" GEOB
// replaced by payload (inserted when absent; a fresh ID3v2.3 tag is created when the file has
// none). Everything else - other frames, padding length, audio - is preserved.
func spliceID3Beatgrid(data, payload []byte) ([]byte, error) {
	return spliceID3Geob(data, payload, beatgridDesc, nil)
}

// spliceID3Geob returns a copy of data with its GEOB frame of description desc replaced by
// payload (inserted when absent; a fresh ID3v2.3 tag is created when the file has none).
// GEOBs whose description is in drop are removed. Everything else is preserved.
func spliceID3Geob(data, payload []byte, desc string, drop map[string]bool) ([]byte, error) {
	tag, audioOff, err := parseID3(data)
	if err != nil {
		return nil, err
	}
	if tag == nil {
		tag = &id3Tag{major: 3, padding: 1024}
	}
	frames := make([]id3Frame, 0, len(tag.frames)+1)
	for _, f := range tag.frames {
		if f.id == "GEOB" {
			if d := geobDescription(tag.major, f); d == desc || drop[d] {
				continue
			}
		}
		frames = append(frames, f)
	}
	frames = append(frames, buildGeobFrame(tag.major, desc, payload))
	tag.frames = frames
	rendered, err := tag.render()
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(rendered)+len(data)-audioOff)
	out = append(out, rendered...)
	return append(out, data[audioOff:]...), nil
}

// readID3Beatgrid extracts the "Serato BeatGrid" GEOB payload from data.
// found=false when the file has no tag / no beatgrid frame.
func readID3Beatgrid(data []byte) ([]byte, bool, error) {
	return readID3Geob(data, beatgridDesc)
}

// readID3Geob extracts the payload of the GEOB frame with description desc.
// found=false when the file has no tag / no such frame.
func readID3Geob(data []byte, desc string) ([]byte, bool, error) {
	tag, _, err := parseID3(data)
	if err != nil || tag == nil {
		return nil, false, err
	}
	for _, f := range tag.frames {
		if f.id != "GEOB" || geobDescription(tag.major, f) != desc {
			continue
		}
		body, ok := geobBody(tag.major, f)
		if !ok {
			return nil, false, fmt.Errorf("seratolib: undecodable %q GEOB", desc)
		}
		_, _, _, payload, perr := splitGeob(body)
		if perr != nil {
			return nil, false, perr
		}
		return payload, true, nil
	}
	return nil, false, nil
}

// buildGeobFrame renders a fresh GEOB frame for the given tag major (size encoding
// differs), flags zero, encoding latin1 (Serato's own convention).
func buildGeobFrame(major byte, desc string, payload []byte) id3Frame {
	body := make([]byte, 0, 1+len(octetStream)+1+1+len(desc)+1+len(payload))
	body = append(body, 0x00)           // text encoding latin1
	body = append(body, octetStream...) // MIME
	body = append(body, 0x00, 0x00)     // MIME NUL + empty filename NUL
	body = append(body, desc...)        // description
	body = append(body, 0x00)           // description NUL
	body = append(body, payload...)
	raw := make([]byte, 0, id3Header+len(body))
	raw = append(raw, 'G', 'E', 'O', 'B')
	if major == 4 {
		raw = appendSyncsafe(raw, len(body))
	} else {
		raw = binary.BigEndian.AppendUint32(raw, uint32(len(body)))
	}
	raw = append(raw, 0x00, 0x00) // frame flags
	return id3Frame{id: "GEOB", raw: append(raw, body...)}
}

// geobBody returns a GEOB frame's decoded body. Frames using compression/encryption are
// skipped (ok=false → treated as non-matching, copied verbatim); v2.4 per-frame unsync and
// data-length-indicator are undone so the body parses.
func geobBody(major byte, f id3Frame) ([]byte, bool) {
	if len(f.raw) < id3Header {
		return nil, false
	}
	format := f.raw[9]
	body := f.raw[id3Header:]
	if major == 4 {
		if format&(0x08|0x04) != 0 { // compression / encryption
			return nil, false
		}
		if format&0x02 != 0 { // per-frame unsynchronisation
			body = resync(body)
		}
		if format&0x01 != 0 { // data-length indicator prefixes the body
			if len(body) < 4 {
				return nil, false
			}
			body = body[4:]
		}
	} else if format&(0x80|0x40) != 0 { // v2.3 compression / encryption
		return nil, false
	}
	return body, true
}

// geobDescription parses a GEOB frame's content-description ("" when unparsable).
func geobDescription(major byte, f id3Frame) string {
	body, ok := geobBody(major, f)
	if !ok {
		return ""
	}
	_, _, desc, _, err := splitGeob(body)
	if err != nil {
		return ""
	}
	return desc
}

// splitGeob splits a GEOB body into MIME, filename, description, payload.
func splitGeob(body []byte) (mime, filename, desc string, payload []byte, err error) {
	if len(body) < 1 {
		return "", "", "", nil, errors.New("seratolib: empty GEOB body")
	}
	enc := body[0]
	rest := body[1:]
	mimeB, rest, err := cutTerminated(rest, false) // MIME is always latin1
	if err != nil {
		return "", "", "", nil, err
	}
	wide := enc == 1 || enc == 2
	fileB, rest, err := cutTerminated(rest, wide)
	if err != nil {
		return "", "", "", nil, err
	}
	descB, rest, err := cutTerminated(rest, wide)
	if err != nil {
		return "", "", "", nil, err
	}
	return string(mimeB), decodeID3Text(enc, fileB), decodeID3Text(enc, descB), rest, nil
}

// cutTerminated splits b at its first NUL terminator (double-NUL, 16-bit aligned, when wide).
func cutTerminated(b []byte, wide bool) (head, tail []byte, err error) {
	if !wide {
		i := bytes.IndexByte(b, 0)
		if i < 0 {
			return nil, nil, errors.New("seratolib: unterminated GEOB string")
		}
		return b[:i], b[i+1:], nil
	}
	for i := 0; i+1 < len(b); i += 2 {
		if b[i] == 0 && b[i+1] == 0 {
			return b[:i], b[i+2:], nil
		}
	}
	return nil, nil, errors.New("seratolib: unterminated GEOB UTF-16 string")
}

// decodeID3Text decodes an ID3 text value per its encoding byte (0/3 byte-wise, 1/2 UTF-16).
func decodeID3Text(enc byte, b []byte) string {
	if enc != 1 && enc != 2 {
		return string(b)
	}
	be := enc == 2
	if enc == 1 && len(b) >= 2 { // BOM
		switch {
		case b[0] == 0xFE && b[1] == 0xFF:
			be, b = true, b[2:]
		case b[0] == 0xFF && b[1] == 0xFE:
			be, b = false, b[2:]
		}
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if be {
			u = append(u, binary.BigEndian.Uint16(b[i:]))
		} else {
			u = append(u, binary.LittleEndian.Uint16(b[i:]))
		}
	}
	return string(utf16.Decode(u))
}

// resync undoes ID3 unsynchronisation: every 0xFF 0x00 pair becomes 0xFF.
func resync(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		out = append(out, b[i])
		if b[i] == 0xFF && i+1 < len(b) && b[i+1] == 0x00 {
			i++
		}
	}
	return out
}

// syncsafe decodes a 4-byte syncsafe integer; ok=false if any byte has its high bit set.
func syncsafe(b []byte) (int, bool) {
	if len(b) < 4 || b[0]|b[1]|b[2]|b[3] >= 0x80 {
		return 0, false
	}
	return int(b[0])<<21 | int(b[1])<<14 | int(b[2])<<7 | int(b[3]), true
}

// appendSyncsafe appends v as a 4-byte syncsafe integer (caller checks v < 1<<28).
func appendSyncsafe(b []byte, v int) []byte {
	return append(b, byte(v>>21&0x7F), byte(v>>14&0x7F), byte(v>>7&0x7F), byte(v&0x7F))
}

// validFrameID reports whether id is 4 chars of A-Z / 0-9.
func validFrameID(id string) bool {
	if len(id) != 4 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}
